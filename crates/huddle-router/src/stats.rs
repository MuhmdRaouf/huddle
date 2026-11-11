//! huddle-router: per-model request stats. Finished requests of the last 15 minutes become the
//! 30-second chart buckets and the model table rows. Pure: every time is a parameter.

use std::collections::{BTreeMap, HashMap, HashSet, VecDeque};

/// Width of one chart bucket.
pub const BUCKET_MS: u64 = 30_000;
/// Buckets in the chart; with `BUCKET_MS` this is the 15 minute window.
pub const BUCKETS: usize = 30;
/// Finished requests one model keeps; the oldest go first.
pub const MAX_SAMPLES: usize = 2000;
/// Models tracked at once; a request for another model is left out of the stats.
pub const MAX_MODELS: usize = 64;

/// One finished request, as the stats need it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Sample {
    pub at_ms: u64,
    /// A failure on the lane's side; a client that left is not one.
    pub failed: bool,
    pub ttft_ms: Option<u64>,
    pub dur_ms: u64,
    pub tokens_in: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub tokens_out: u64,
    pub session: Option<String>,
}

/// One model's finished requests, oldest first, and the lane that served it last.
#[derive(Debug, Default)]
pub struct ModelSeries {
    pub lane: String,
    pub samples: VecDeque<Sample>,
}

impl ModelSeries {
    pub fn push(&mut self, lane: &str, sample: Sample) {
        if self.lane != lane {
            self.lane = lane.to_owned();
        }
        self.samples.push_back(sample);
        if self.samples.len() > MAX_SAMPLES {
            self.samples.pop_front();
        }
    }

    /// Drops the samples older than the window that ends at the bucket after `now_ms`.
    pub fn prune(&mut self, now_ms: u64) {
        let start = window_start(now_ms);
        while self
            .samples
            .front()
            .is_some_and(|sample| sample.at_ms < start)
        {
            self.samples.pop_front();
        }
    }

    pub fn is_empty(&self) -> bool {
        self.samples.is_empty()
    }
}

/// The end of the bucket `now_ms` falls in.
fn window_end(now_ms: u64) -> u64 {
    (now_ms / BUCKET_MS + 1) * BUCKET_MS
}

fn window_start(now_ms: u64) -> u64 {
    window_end(now_ms).saturating_sub(BUCKETS as u64 * BUCKET_MS)
}

/// One 30 second slot of one model.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct BucketRow {
    pub n: u32,
    pub err: u32,
    pub tok_in: u64,
    pub tok_out: u64,
    pub ttft_p50: Option<u64>,
}

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Buckets {
    pub width_ms: u64,
    pub end_ms: u64,
    pub models: BTreeMap<String, Vec<BucketRow>>,
}

/// One model over the window, as the table shows it.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct ModelRow {
    pub model: String,
    pub lane: String,
    pub requests: u64,
    pub errors: u64,
    pub ttft_p50: Option<u64>,
    pub ttft_p95: Option<u64>,
    pub out_tps_p50: Option<u64>,
    pub tokens_in: u64,
    pub cache_read: u64,
    pub cache_write: u64,
    pub tokens_out: u64,
    pub sessions: u64,
    pub last_ms: u64,
}

/// Thirty buckets per model that has a sample in the window, oldest first, the last one holding
/// `now_ms`.
pub fn buckets(models: &HashMap<String, ModelSeries>, now_ms: u64) -> Buckets {
    let end = window_end(now_ms);
    let start = window_start(now_ms);
    let mut out = BTreeMap::new();
    for (model, series) in models {
        let mut slots: Vec<(BucketRow, Vec<u64>)> = (0..BUCKETS)
            .map(|_| {
                (
                    BucketRow {
                        n: 0,
                        err: 0,
                        tok_in: 0,
                        tok_out: 0,
                        ttft_p50: None,
                    },
                    Vec::new(),
                )
            })
            .collect();
        let mut any = false;
        for sample in series
            .samples
            .iter()
            .filter(|s| s.at_ms >= start && s.at_ms < end)
        {
            any = true;
            let index = ((sample.at_ms - start) / BUCKET_MS) as usize;
            let Some((row, ttfts)) = slots.get_mut(index) else {
                continue;
            };
            row.n += 1;
            row.err += u32::from(sample.failed);
            row.tok_in += sample.tokens_in;
            row.tok_out += sample.tokens_out;
            ttfts.extend(sample.ttft_ms);
        }
        if any {
            out.insert(
                model.clone(),
                slots
                    .into_iter()
                    .map(|(mut row, mut ttfts)| {
                        ttfts.sort_unstable();
                        row.ttft_p50 = percentile(&ttfts, 50);
                        row
                    })
                    .collect(),
            );
        }
    }
    Buckets {
        width_ms: BUCKET_MS,
        end_ms: end,
        models: out,
    }
}

/// One row per model with a sample in the window, the busiest first.
pub fn model_rows(models: &HashMap<String, ModelSeries>, now_ms: u64) -> Vec<ModelRow> {
    let start = window_start(now_ms);
    let mut rows: Vec<ModelRow> = models
        .iter()
        .filter_map(|(model, series)| model_row(model, series, start))
        .collect();
    rows.sort_by(|a, b| {
        b.requests
            .cmp(&a.requests)
            .then_with(|| a.model.cmp(&b.model))
    });
    rows
}

fn model_row(model: &str, series: &ModelSeries, start: u64) -> Option<ModelRow> {
    let mut row = ModelRow {
        model: model.to_owned(),
        lane: series.lane.clone(),
        requests: 0,
        errors: 0,
        ttft_p50: None,
        ttft_p95: None,
        out_tps_p50: None,
        tokens_in: 0,
        cache_read: 0,
        cache_write: 0,
        tokens_out: 0,
        sessions: 0,
        last_ms: 0,
    };
    let mut ttfts = Vec::new();
    let mut speeds = Vec::new();
    let mut sessions: HashSet<&str> = HashSet::new();
    for sample in series.samples.iter().filter(|s| s.at_ms >= start) {
        row.requests += 1;
        row.errors += u64::from(sample.failed);
        row.tokens_in += sample.tokens_in;
        row.cache_read += sample.cache_read;
        row.cache_write += sample.cache_write;
        row.tokens_out += sample.tokens_out;
        row.last_ms = row.last_ms.max(sample.at_ms);
        ttfts.extend(sample.ttft_ms);
        speeds.extend(speed(sample));
        sessions.extend(sample.session.as_deref());
    }
    if row.requests == 0 {
        return None;
    }
    ttfts.sort_unstable();
    speeds.sort_unstable();
    row.ttft_p50 = percentile(&ttfts, 50);
    row.ttft_p95 = percentile(&ttfts, 95);
    row.out_tps_p50 = percentile(&speeds, 50);
    row.sessions = sessions.len() as u64;
    Some(row)
}

/// Output tokens per second between the first byte and the end; unknown without tokens or time.
fn speed(sample: &Sample) -> Option<u64> {
    let streaming = sample.dur_ms.checked_sub(sample.ttft_ms?)?;
    (streaming > 0 && sample.tokens_out > 0).then(|| sample.tokens_out * 1000 / streaming)
}

/// Nearest rank over an ascending list.
pub fn percentile(sorted: &[u64], percent: usize) -> Option<u64> {
    if sorted.is_empty() {
        return None;
    }
    let rank = (percent * sorted.len()).div_ceil(100).max(1);
    sorted.get(rank - 1).copied()
}

#[cfg(test)]
mod tests {
    use super::*;

    const T0: u64 = 1_794_300_000_000 - 1_794_300_000_000 % BUCKET_MS;

    fn sample(at_ms: u64) -> Sample {
        Sample {
            at_ms,
            failed: false,
            ttft_ms: Some(100),
            dur_ms: 1100,
            tokens_in: 10,
            cache_read: 4,
            cache_write: 2,
            tokens_out: 50,
            session: Some("cse_a".into()),
        }
    }

    fn one(model: &str, lane: &str, samples: Vec<Sample>) -> HashMap<String, ModelSeries> {
        let mut series = ModelSeries::default();
        for sample in samples {
            series.push(lane, sample);
        }
        HashMap::from([(model.to_string(), series)])
    }

    #[test]
    fn percentile_is_nearest_rank() {
        let sorted: Vec<u64> = (1..=100).collect();
        assert_eq!(percentile(&sorted, 50), Some(50));
        assert_eq!(percentile(&sorted, 95), Some(95));
        assert_eq!(percentile(&[7], 95), Some(7));
        assert_eq!(percentile(&[], 50), None);
    }

    #[test]
    fn buckets_are_30_slots_of_30_seconds_oldest_first() {
        let now = T0 + 10_000;
        let models = one(
            "glm-5.3",
            "zai",
            vec![
                sample(T0 + 1_000),
                Sample {
                    failed: true,
                    ttft_ms: Some(300),
                    ..sample(T0 + 2_000)
                },
                sample(T0 - 30_000 + 5),
                sample(T0 + 30_000 - BUCKETS as u64 * BUCKET_MS + 1),
            ],
        );
        let buckets = buckets(&models, now);
        assert_eq!(buckets.width_ms, 30_000);
        assert_eq!(buckets.end_ms, T0 + 30_000);
        let row = &buckets.models["glm-5.3"];
        assert_eq!(row.len(), 30);
        let last = &row[29];
        assert_eq!((last.n, last.err), (2, 1));
        assert_eq!((last.tok_in, last.tok_out), (20, 100));
        assert_eq!(last.ttft_p50, Some(100));
        assert_eq!(row[28].n, 1);
        assert_eq!(row[0].n, 1);
        assert_eq!(row[1].n, 0);
        assert_eq!(row[1].ttft_p50, None);
    }

    #[test]
    fn a_499_counts_as_a_request_and_never_as_an_error() {
        let models = one(
            "glm-5.3",
            "zai",
            vec![Sample {
                failed: false,
                ..sample(T0)
            }],
        );
        let rows = model_rows(&models, T0 + 1);
        assert_eq!((rows[0].requests, rows[0].errors), (1, 0));
    }

    #[test]
    fn a_model_row_sums_percentiles_and_counts_sessions() {
        let mut samples = Vec::new();
        for n in 1..=20u64 {
            samples.push(Sample {
                ttft_ms: Some(n * 10),
                dur_ms: n * 10 + 1000,
                tokens_out: 100,
                session: Some(if n % 2 == 0 { "cse_a" } else { "cse_b" }.into()),
                failed: n == 20,
                ..sample(T0 + n * 1000)
            });
        }
        let models = one("glm-5.3", "zai", samples);
        let row = &model_rows(&models, T0 + 60_000)[0];
        assert_eq!(row.model, "glm-5.3");
        assert_eq!(row.lane, "zai");
        assert_eq!((row.requests, row.errors), (20, 1));
        assert_eq!(row.ttft_p50, Some(100));
        assert_eq!(row.ttft_p95, Some(190));
        assert_eq!(row.out_tps_p50, Some(100));
        assert_eq!(row.tokens_in, 200);
        assert_eq!((row.cache_read, row.cache_write), (80, 40));
        assert_eq!(row.tokens_out, 2000);
        assert_eq!(row.sessions, 2);
        assert_eq!(row.last_ms, T0 + 20_000);
    }

    #[test]
    fn speed_needs_tokens_and_time_after_the_first_byte() {
        let models = one(
            "m",
            "zai",
            vec![
                Sample {
                    tokens_out: 0,
                    ..sample(T0)
                },
                Sample {
                    ttft_ms: None,
                    dur_ms: 500,
                    tokens_out: 50,
                    ..sample(T0)
                },
                Sample {
                    ttft_ms: Some(500),
                    dur_ms: 500,
                    ..sample(T0)
                },
                Sample {
                    ttft_ms: Some(100),
                    dur_ms: 600,
                    tokens_out: 50,
                    ..sample(T0)
                },
            ],
        );
        let row = &model_rows(&models, T0 + 1)[0];
        assert_eq!(row.out_tps_p50, Some(100));
    }

    #[test]
    fn samples_older_than_the_window_drop_out_of_rows_and_buckets() {
        let models = one(
            "m",
            "zai",
            vec![sample(T0 - 20 * 60_000), sample(T0 + 1_000)],
        );
        let now = T0 + 5_000;
        assert_eq!(model_rows(&models, now)[0].requests, 1);
        let total: u32 = buckets(&models, now).models["m"].iter().map(|b| b.n).sum();
        assert_eq!(total, 1);
        let idle = one("m", "zai", vec![sample(T0 - 20 * 60_000)]);
        assert!(model_rows(&idle, now).is_empty());
        assert!(buckets(&idle, now).models.is_empty());
    }

    #[test]
    fn a_series_keeps_at_most_its_cap_of_newest_samples() {
        let mut series = ModelSeries::default();
        for n in 0..(MAX_SAMPLES as u64 + 5) {
            series.push("zai", sample(T0 + n));
        }
        assert_eq!(series.samples.len(), MAX_SAMPLES);
        assert_eq!(series.samples[0].at_ms, T0 + 5);
    }

    #[test]
    fn rows_list_the_busiest_model_first() {
        let mut models = one("a", "zai", vec![sample(T0)]);
        models.extend(one("b", "kimi", vec![sample(T0), sample(T0 + 1)]));
        let names: Vec<String> = model_rows(&models, T0 + 2)
            .into_iter()
            .map(|row| row.model)
            .collect();
        assert_eq!(names, ["b", "a"]);
    }
}
