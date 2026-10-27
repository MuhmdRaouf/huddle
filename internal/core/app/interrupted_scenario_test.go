package app

// InterruptDead moves every queued, running or verifying job whose driver died to interrupted:
// these scenarios pin the dead-driver rule (lock pid not alive, or alive with a start time other
// than the sidecar's), the fallbacks (no sidecar, missing lock), and that a version conflict or
// a missing lock never invents a move. It starts nothing and signals nothing.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// putRunning puts a running job with one open attempt in the world.
func putRunning(w *appWorld, id string) domain.Job {
	return put(w, id, func(job *domain.Job) {
		job.State = domain.JobStateRunning
		job.Attempts = []domain.Attempt{testkit.AnAttempt()}
	})
}

// putInState puts a job in any active state with one open attempt.
func putInState(w *appWorld, id string, state domain.JobState) domain.Job {
	return put(w, id, func(job *domain.Job) {
		job.State = state
		job.Attempts = []domain.Attempt{testkit.AnAttempt()}
	})
}

// writeDriverLock plants the driver lock text beside the job's record.
func writeDriverLock(t *testing.T, w *appWorld, id, text string) {
	t.Helper()
	if err := os.WriteFile(w.store.Paths(id).DriverLock, []byte(text), 0o600); err != nil {
		t.Fatalf("write the driver lock: %v", err)
	}
}

// writeDriverStart plants the driver lock's start-time sidecar.
func writeDriverStart(t *testing.T, w *appWorld, id, text string) {
	t.Helper()
	if err := os.WriteFile(w.store.Paths(id).DriverStart, []byte(text), 0o600); err != nil {
		t.Fatalf("write the driver start sidecar: %v", err)
	}
}

// readDriverLockFile reads the driver lock text back.
func readDriverLockFile(t *testing.T, w *appWorld, id string) string {
	t.Helper()
	text, err := os.ReadFile(w.store.Paths(id).DriverLock)
	if err != nil {
		t.Fatalf("read the driver lock: %v", err)
	}
	return string(text)
}

// sameIds compares two id lists order-insensitively.
func sameIds(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("moved ids = %v, want %v", got, want)
	}
}

// conflictStore refuses every Update with a version conflict, as the store answers when the
// driver wrote between the sweep's read and its write.
type conflictStore struct {
	ports.JobStore
}

func (s *conflictStore) Update(ctx context.Context, job domain.Job) (domain.Job, error) {
	current, err := s.JobStore.Get(ctx, job.ID)
	if err != nil {
		return domain.Job{}, err
	}
	return domain.Job{}, &ports.StoreError{
		Kind:     ports.StoreErrorVersionConflict,
		ID:       job.ID,
		Expected: job.Version,
		Actual:   current.Version,
	}
}

func TestInterruptDead(t *testing.T) {
	bdd.Feature(t, "a job whose driver died is interrupted before anything reads it", func(f *bdd.F) {
		f.Scenario("a running job whose lock names a dead pid is interrupted", func(s *bdd.S) {
			var deps Deps
			var w *appWorld
			var job domain.Job
			s.Given("a running job whose driver lock names a pid that is not alive", func(t *testing.T) {
				deps, w = newWorld(s.T, nil)
				job = putRunning(w, "j1")
				writeDriverLock(t, w, "j1", "4242\n")
			})
			s.When("the sweep runs", func(t *testing.T) {
				moved := InterruptDead(context.Background(), deps)
				sameIds(t, moved, []string{"j1"})
			})
			s.Then("the job is interrupted, its attempt is closed with no verdict, and nothing was signalled", func(t *testing.T) {
				stored, err := deps.Store.Get(context.Background(), job.ID)
				if err != nil {
					t.Fatalf("get the job: %v", err)
				}
				if stored.State != domain.JobStateInterrupted {
					t.Fatalf("the job is %s, want interrupted", stored.State)
				}
				last := stored.Attempts[len(stored.Attempts)-1]
				if last.EndedAt == nil {
					t.Error("the attempt's EndedAt stayed nil")
				}
				if last.Outcome != nil || last.Verdict != nil {
					t.Errorf("the attempt grew an outcome %+v / verdict %+v, want none invented", last.Outcome, last.Verdict)
				}
				if graces := w.process.gracesOf(); len(graces) != 0 {
					t.Errorf("terminations were signalled with graces %v", graces)
				}
				if text := readDriverLockFile(t, w, "j1"); text != "4242\n" {
					t.Errorf("the driver lock changed to %q", text)
				}
			})
		})

		f.Scenario("a live pid whose start time differs from the sidecar's is a reused pid: the job is interrupted", func(s *bdd.S) {
			var deps Deps
			var w *appWorld
			s.Given("a running job whose lock's pid is alive but began again after the sidecar's start time", func(t *testing.T) {
				deps, w = newWorld(s.T, nil)
				putRunning(w, "j1")
				writeDriverLock(t, w, "j1", "4242\n")
				writeDriverStart(t, w, "j1", "1700000000000\n")
				w.process.MarkAlive(4242)
				w.process.MarkStart(4242, 1799999000000)
			})
			s.When("the sweep runs", func(t *testing.T) {
				sameIds(t, InterruptDead(context.Background(), deps), []string{"j1"})
			})
			s.Then("the job is interrupted", func(t *testing.T) {
				stored, err := deps.Store.Get(context.Background(), "j1")
				if err != nil {
					t.Fatalf("get the job: %v", err)
				}
				if stored.State != domain.JobStateInterrupted {
					t.Errorf("the job is %s, want interrupted", stored.State)
				}
			})
		})

		f.Scenario("a live pid whose start time matches the sidecar is still the driver: the job stays running", func(s *bdd.S) {
			var deps Deps
			var w *appWorld
			s.Given("a running job whose lock's pid is alive and began at the sidecar's start time", func(t *testing.T) {
				deps, w = newWorld(s.T, nil)
				putRunning(w, "j1")
				writeDriverLock(t, w, "j1", "4242\n")
				writeDriverStart(t, w, "j1", "1700000000000\n")
				w.process.MarkAlive(4242)
				w.process.MarkStart(4242, 1700000000000)
			})
			s.When("the sweep runs", func(t *testing.T) {
				sameIds(t, InterruptDead(context.Background(), deps), nil)
			})
			s.Then("the job is still running", func(t *testing.T) {
				stored, err := deps.Store.Get(context.Background(), "j1")
				if err != nil {
					t.Fatalf("get the job: %v", err)
				}
				if stored.State != domain.JobStateRunning {
					t.Errorf("the job is %s, want running", stored.State)
				}
			})
		})

		f.Scenario("a lock without a sidecar reads by liveness alone", func(s *bdd.S) {
			var deps Deps
			var w *appWorld
			s.Given("a running job whose lock carries a dead pid with no sidecar, and one whose pid lives on", func(t *testing.T) {
				deps, w = newWorld(s.T, nil)
				putRunning(w, "j-dead")
				putRunning(w, "j-live")
				writeDriverLock(t, w, "j-dead", "4243\n")
				writeDriverLock(t, w, "j-live", "4244\n")
				w.process.MarkAlive(4244)
			})
			s.When("the sweep runs", func(t *testing.T) {
				sameIds(t, InterruptDead(context.Background(), deps), []string{"j-dead"})
			})
			s.Then("the dead one is interrupted and the live one runs on", func(t *testing.T) {
				for id, want := range map[string]domain.JobState{"j-dead": domain.JobStateInterrupted, "j-live": domain.JobStateRunning} {
					stored, err := deps.Store.Get(context.Background(), id)
					if err != nil {
						t.Fatalf("get %s: %v", id, err)
					}
					if stored.State != want {
						t.Errorf("%s is %s, want %s", id, stored.State, want)
					}
				}
			})
		})

		f.Scenario("a missing lock file says nothing: every active state stays", func(s *bdd.S) {
			var deps Deps
			var w *appWorld
			s.Given("a queued, a running and a verifying job, none holding a lock", func(t *testing.T) {
				deps, w = newWorld(s.T, nil)
				putInState(w, "j-queued", domain.JobStateQueued)
				putRunning(w, "j-running")
				putInState(w, "j-verifying", domain.JobStateVerifying)
			})
			s.When("the sweep runs", func(t *testing.T) {
				sameIds(t, InterruptDead(context.Background(), deps), nil)
			})
			s.Then("every job keeps its state", func(t *testing.T) {
				for id, want := range map[string]domain.JobState{
					"j-queued":    domain.JobStateQueued,
					"j-running":   domain.JobStateRunning,
					"j-verifying": domain.JobStateVerifying,
				} {
					stored, err := deps.Store.Get(context.Background(), id)
					if err != nil {
						t.Fatalf("get %s: %v", id, err)
					}
					if stored.State != want {
						t.Errorf("%s is %s, want %s", id, stored.State, want)
					}
				}
			})
		})

		f.Scenario("a version conflict leaves the job alone: the driver just wrote", func(s *bdd.S) {
			var deps Deps
			var w *appWorld
			s.Given("a running job whose lock names a dead pid, over a store that answers every update with a conflict", func(t *testing.T) {
				deps, w = newWorld(s.T, nil)
				putRunning(w, "j1")
				writeDriverLock(t, w, "j1", "4242\n")
				deps.Store = &conflictStore{JobStore: deps.Store}
			})
			s.When("the sweep runs", func(t *testing.T) {
				sameIds(t, InterruptDead(context.Background(), deps), nil)
			})
			s.Then("the job is still running and the error was skipped, not fatal", func(t *testing.T) {
				stored, err := deps.Store.Get(context.Background(), "j1")
				if err != nil {
					t.Fatalf("get the job: %v", err)
				}
				if stored.State != domain.JobStateRunning {
					t.Errorf("the job is %s, want running", stored.State)
				}
			})
		})
	})
}
