//! The daemon's periodic blocking reads on one clock. The caller awaits each due job before it
//! waits again, so at most one blocking-pool thread serves the clocks and it leaves before the
//! next one is due.

use std::time::{Duration, Instant};
use tokio::time::sleep_until;

/// A read that runs on a clock.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Job {
    /// Providers and dashboard hosts re-read their files.
    Reload,
    /// The resident size check.
    Rss,
    /// The history database drops old rows.
    Prune,
}

/// What a due job does.
pub trait Runner {
    fn run(&mut self, job: Job) -> impl Future<Output = ()>;
}

/// When each job is next due. A job that ran late is due again one period after it ran, never
/// in a burst to catch up.
pub struct Schedule {
    jobs: [(Job, Instant, Duration); 3],
}

impl Schedule {
    /// The first reload is due `reload` after `start`, the first RSS check `rss` after it, and
    /// the first prune `prune_first` after it; `prune` is the period of the prune after that.
    pub fn new(
        start: Instant,
        reload: Duration,
        rss: Duration,
        prune_first: Duration,
        prune: Duration,
    ) -> Self {
        Self {
            jobs: [
                (Job::Reload, start + reload, reload),
                (Job::Rss, start + rss, rss),
                (Job::Prune, start + prune_first, prune),
            ],
        }
    }

    /// Resolves when the earliest job is due.
    pub async fn wait(&self) {
        let next = self.jobs.iter().map(|(_, due, _)| *due).min();
        if let Some(next) = next {
            sleep_until(tokio::time::Instant::from_std(next)).await;
        }
    }

    /// Runs every job that is due, one after the other: a job finishes before the next starts.
    pub async fn run_due(&mut self, runner: &mut impl Runner) {
        for job in self.take_due() {
            runner.run(job).await;
        }
    }

    /// The jobs due now, in the order reload, RSS, prune; each is due again one period from now.
    pub fn take_due(&mut self) -> Vec<Job> {
        let now = Instant::now();
        let mut due = Vec::new();
        for (job, next, period) in &mut self.jobs {
            if *next <= now {
                due.push(*job);
                *next = now + *period;
            }
        }
        due
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;
    use std::sync::atomic::{AtomicUsize, Ordering};

    /// Fake reads that each outlast every period, so all three jobs are due together.
    struct Fakes {
        running: Arc<AtomicUsize>,
        widest: Arc<AtomicUsize>,
        ran: usize,
    }

    impl Runner for Fakes {
        async fn run(&mut self, _job: Job) {
            let (running, widest) = (Arc::clone(&self.running), Arc::clone(&self.widest));
            tokio::task::spawn_blocking(move || {
                let now = running.fetch_add(1, Ordering::SeqCst) + 1;
                widest.fetch_max(now, Ordering::SeqCst);
                std::thread::sleep(Duration::from_millis(30));
                running.fetch_sub(1, Ordering::SeqCst);
            })
            .await
            .expect("fake read");
            self.ran += 1;
        }
    }

    #[tokio::test]
    async fn l44_periodic_reads_never_overlap() {
        let mut schedule = Schedule::new(
            Instant::now(),
            Duration::from_millis(10),
            Duration::from_millis(15),
            Duration::ZERO,
            Duration::from_millis(5),
        );
        let mut fakes = Fakes {
            running: Arc::default(),
            widest: Arc::default(),
            ran: 0,
        };
        while fakes.ran < 12 {
            schedule.wait().await;
            schedule.run_due(&mut fakes).await;
        }
        assert_eq!(fakes.widest.load(Ordering::SeqCst), 1);
    }
}
