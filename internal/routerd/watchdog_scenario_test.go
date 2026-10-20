package routerd

// The session's side of the daemon, as scenarios over the daemon rig: a Watch runs the way
// `huddle mcp` runs it — the real health probe, the real router lock, small windows — and the
// scenarios prove what a session's presence keeps up.

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// scenarioWatch is one running watchdog over the rig: the real health probe and lock, a Start
// that brings the rig's daemon back, and counted calls.
type scenarioWatch struct {
	rig    *daemonRig
	log    *daemonLog
	cancel context.CancelFunc
	starts atomic.Int64
}

func (w *scenarioWatch) startCall(context.Context) error {
	// The start is the rig's own daemon once more: the same shape the real Start spawns, with
	// the process swapped for the goroutine the rig runs it in.
	w.starts.Add(1)
	w.rig.start()
	return nil
}

func (w *scenarioWatch) startCount() int64 { return w.starts.Load() }

// begin runs the watchdog the way the bridge does, with the given windows.
func (w *scenarioWatch) begin(every, silence time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go Watch(ctx, WatchOptions{
		Env: w.rig.env, Every: every, Silence: silence,
		Healthy: func(ctx context.Context) bool {
			_, up := Health(ctx, w.rig.port)
			return up
		},
		Start: w.startCall, Log: w.log.add,
	})
}

// stop ends the watchdog.
func (w *scenarioWatch) stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

// killDaemon ends the rig's daemon the way a crash ends it: its lock and port let go.
func killDaemon(t *testing.T, rig *daemonRig) {
	t.Helper()
	rig.mu.Lock()
	cancel, done := rig.cancel, rig.done
	rig.cancel = nil // cleanup must not wait on the killed run again
	rig.mu.Unlock()
	if cancel == nil {
		t.Fatal("no daemon is running")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon never stopped")
	}
}

func TestSessionWatchdogScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "the session keeps huddle-router up", func(f *bdd.F) {
		f.Scenario("a killed router comes back while a session runs", func(s *bdd.S) {
			var rig *daemonRig
			var watch *scenarioWatch
			s.Given("zai is enabled, the daemon serves it and the session's watchdog runs", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
				watch = &scenarioWatch{rig: rig, log: &daemonLog{}}
				watch.begin(30*time.Millisecond, 80*time.Millisecond)
				// The watchdog lives through the scenario: bdd runs every step as a subtest,
				// so a cleanup on the step's own t would end the watch at the step's end.
				s.T.Cleanup(watch.stop)
			})
			s.When("the daemon dies", func(t *testing.T) {
				killDaemon(t, rig)
			})
			s.Then("the watchdog started it again and health answers", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
				if watch.startCount() == 0 {
					t.Errorf("the watchdog started nothing")
				}
			})
			s.And("the restart is on the record", func(t *testing.T) {
				if got := watch.log.joined(); !strings.Contains(got, "restarted by the session watchdog") {
					t.Errorf("log = %q, want the watchdog's restart line", got)
				}
			})
		})

		f.Scenario("a router that stops answering is replaced", func(s *bdd.S) {
			var rig *daemonRig
			var watch *scenarioWatch
			var holderAlive func() bool
			var holderStop func()
			s.Given("zai is enabled and a hung daemon of ours holds the router lock", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				_, holderAlive, holderStop = spawnHolder(t, lockPathOf(rig.env), true)
				s.T.Cleanup(holderStop)
				watch = &scenarioWatch{rig: rig, log: &daemonLog{}}
				watch.begin(30*time.Millisecond, 80*time.Millisecond)
				// The watchdog lives through the scenario: bdd runs every step as a subtest,
				// so a cleanup on the step's own t would end the watch at the step's end.
				s.T.Cleanup(watch.stop)
			})
			s.When("the session's watchdog finds the silence", func(t *testing.T) {
				deadline := time.Now().Add(5 * time.Second)
				for watch.startCount() == 0 {
					if !time.Now().Before(deadline) {
						t.Fatalf("the watchdog never started a daemon; log:\n%s", watch.log.joined())
					}
					time.Sleep(20 * time.Millisecond)
				}
			})
			s.Then("the hung holder is gone", func(t *testing.T) {
				deadline := time.Now().Add(5 * time.Second)
				for holderAlive() {
					if !time.Now().Before(deadline) {
						t.Fatalf("the hung holder survived")
					}
					time.Sleep(20 * time.Millisecond)
				}
			})
			s.And("the start is on the record", func(t *testing.T) {
				if got := watch.log.joined(); !strings.Contains(got, "restarted by the session watchdog") {
					t.Errorf("log = %q, want the watchdog's restart line", got)
				}
			})
		})

		f.Scenario("with no provider enabled the watchdog starts nothing", func(s *bdd.S) {
			var rig *daemonRig
			var watch *scenarioWatch
			s.Given("a home with no provider enabled and a running watchdog", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				watch = &scenarioWatch{rig: rig, log: &daemonLog{}}
				watch.begin(20*time.Millisecond, 50*time.Millisecond)
				s.T.Cleanup(watch.stop)
			})
			s.Then("several ticks later nothing has been started", func(t *testing.T) {
				time.Sleep(400 * time.Millisecond)
				if watch.startCount() != 0 {
					t.Errorf("starts = %d, want none while nothing is enabled", watch.startCount())
				}
			})
		})
	})
}
