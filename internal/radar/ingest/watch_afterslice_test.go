package ingest_test

import (
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
)

// The after-slice hook runs once a slice of a pass has ended, outside the watcher's lock: a test
// holds the pass open in it, and everything that asks the watcher meanwhile is answered.
func TestTheAfterSliceHookHoldsAPassOpenWithoutHoldingTheWatchersLock(t *testing.T) {
	e := makeEnv(t)
	writeText(t, todaySpoolPath(t, e.env), jsonl(t, map[string]any{
		"ts": iso(watchT0), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "one",
	}))
	parked := make(chan struct{})
	release := make(chan struct{})
	w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env:        e.env,
		Store:      newStore(),
		SinceMs:    watchDay,
		IntervalMs: 3_600_000,
		Now:        func() int64 { return watchT0 },
		Alive:      ownPidAlive,
		NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
		AfterSlice: func() {
			select {
			case parked <- struct{}{}:
				<-release
			case <-release:
			}
		},
	})
	t.Cleanup(w.Stop)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	select {
	case <-parked:
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never ran after the first slice")
	}
	answered := make(chan bool, 1)
	go func() { answered <- w.CatchingUp() }()
	select {
	case catching := <-answered:
		if !catching {
			t.Error("catching up = false while the hook holds the pass open")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not answer CatchingUp while the hook held the pass: the hook runs under its lock")
	}
	close(release)
	waitFor(t, "the pass to end once the hook lets it go", 10*time.Second, func() bool { return !w.CatchingUp() })
}
