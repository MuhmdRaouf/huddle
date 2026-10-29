package server

// The hub's background loops report into the health registry: a pass that fails shows failing,
// a loop a panic ended shows failing with the panic's own line, and a loop that misses three
// periods shows stale. Each test drives one loop over a real registry in a temp home, with the
// health clock in the case's hands.

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/health"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// loopsClock is the moment the loop cases read their timestamps against.
var loopsClock = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

// loopsServerForTest builds the server face over a real registry in a temp home, with the
// health registry reading the case's clock: the loops run in-process against the one database.
func loopsServerForTest(t *testing.T, now func() time.Time) *server {
	t.Helper()
	dir := t.TempDir()
	db, err := hub.Open(context.Background(), home.Home{Root: dir}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &server{
		o:        Options{Home: dir, Data: dir, Stdout: io.Discard, Stderr: io.Discard},
		hub:      channel.NewHub(dir, true),
		registry: db,
		now:      now,
		vapid:    newVapidSource(nil, filepath.Join(dir, "keys", "vapid"), func(string, ...any) {}),
		health:   health.New(now),
	}
}

// startLoop runs one loop on its own goroutine; stop cancels it and waits for the return.
func startLoop(t *testing.T, loop func(context.Context)) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the loop never returned after its context ended")
		}
	}
}

// waitLoopReport polls the snapshot until name shows state, and answers that component.
func waitLoopReport(t *testing.T, snapshot func() []health.Part, name, state string) health.Part {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		parts := snapshot()
		for _, part := range parts {
			if part.Name == name && part.State == state {
				return part
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the %s component never showed %s: components = %+v", name, state, parts)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// loopPart answers the named component of one snapshot, failing the case when it is missing.
func loopPart(t *testing.T, parts []health.Part, name string) health.Part {
	t.Helper()
	for _, part := range parts {
		if part.Name == name {
			return part
		}
	}
	t.Fatalf("components = %+v, want %s listed", parts, name)
	return health.Part{}
}

func TestThePidWatchReportsEachPass(t *testing.T) {
	t.Run("a good pass reports ok with its period", func(t *testing.T) {
		clock := loopsClock
		s := loopsServerForTest(t, func() time.Time { return clock })
		s.o.PidCheckMS = 20
		stop := startLoop(t, s.watchPids)
		part := waitLoopReport(t, s.health.Snapshot, "hub.pids", string(domain.HealthOK))
		stop()
		if part.Kind != string(domain.HealthLoop) {
			t.Fatalf("hub.pids = %+v, want a loop", part)
		}
		if part.Errors != 0 {
			t.Fatalf("hub.pids = %+v, want no errors on a good pass", part)
		}
		// the registration carries the watch's own 20 ms period: exactly three missed periods
		// leaves the watch ok, one millisecond past them makes it stale
		clock = loopsClock.Add(60 * time.Millisecond)
		if part := loopPart(t, s.health.Snapshot(), "hub.pids"); part.State != string(domain.HealthOK) {
			t.Fatalf("hub.pids at three missed periods = %+v, want ok", part)
		}
		clock = loopsClock.Add(61 * time.Millisecond)
		if part := loopPart(t, s.health.Snapshot(), "hub.pids"); part.State != string(domain.HealthStale) {
			t.Fatalf("hub.pids past three periods = %+v, want stale", part)
		}
	})

	t.Run("a failing pass reports the error", func(t *testing.T) {
		s := loopsServerForTest(t, func() time.Time { return loopsClock })
		s.o.PidCheckMS = 20
		if err := s.registry.Close(); err != nil {
			t.Fatalf("close the registry: %v", err)
		}
		stop := startLoop(t, s.watchPids)
		defer stop()
		part := waitLoopReport(t, s.health.Snapshot, "hub.pids", string(domain.HealthFailing))
		if part.Errors < 1 {
			t.Fatalf("hub.pids = %+v, want at least one error", part)
		}
		if part.LastError == "" {
			t.Fatalf("hub.pids = %+v, want the error's own line", part)
		}
	})
}

func TestThePermitSweepReportsEachPass(t *testing.T) {
	t.Run("a good pass reports ok", func(t *testing.T) {
		s := loopsServerForTest(t, func() time.Time { return loopsClock })
		s.o.PermitSweepMS = 20
		stop := startLoop(t, s.watchPermissions)
		defer stop()
		part := waitLoopReport(t, s.health.Snapshot, "hub.permits", string(domain.HealthOK))
		if part.Kind != string(domain.HealthLoop) {
			t.Fatalf("hub.permits = %+v, want a loop", part)
		}
	})

	t.Run("a failing pass reports the error", func(t *testing.T) {
		s := loopsServerForTest(t, func() time.Time { return loopsClock })
		s.o.PermitSweepMS = 20
		if err := s.registry.Close(); err != nil {
			t.Fatalf("close the registry: %v", err)
		}
		stop := startLoop(t, s.watchPermissions)
		defer stop()
		part := waitLoopReport(t, s.health.Snapshot, "hub.permits", string(domain.HealthFailing))
		if part.Errors < 1 {
			t.Fatalf("hub.permits = %+v, want at least one error", part)
		}
	})
}

func TestTheTranscriptWatchReportsWithNoStreamOpen(t *testing.T) {
	s := loopsServerForTest(t, func() time.Time { return loopsClock })
	s.o.TranscriptCheckMS = 20
	stop := startLoop(t, s.watchTranscripts)
	defer stop()
	part := waitLoopReport(t, s.health.Snapshot, "hub.transcripts", string(domain.HealthOK))
	if part.Kind != string(domain.HealthLoop) {
		t.Fatalf("hub.transcripts = %+v, want a loop", part)
	}
	if part.Errors != 0 {
		t.Fatalf("hub.transcripts = %+v, want no errors with no stream open", part)
	}
}

func TestThePushSweepReportsItsFirstError(t *testing.T) {
	t.Run("a closed registry fails the sweep", func(t *testing.T) {
		s := loopsServerForTest(t, func() time.Time { return loopsClock })
		s.o.PushSweepMS = 20
		if err := s.registry.Close(); err != nil {
			t.Fatalf("close the registry: %v", err)
		}
		stop := startLoop(t, s.watchPushes)
		defer stop()
		part := waitLoopReport(t, s.health.Snapshot, "hub.pushes", string(domain.HealthFailing))
		if part.LastError == "" {
			t.Fatalf("hub.pushes = %+v, want the sweep's first error", part)
		}
	})

	t.Run("the sweep answers its first error", func(t *testing.T) {
		s := loopsServerForTest(t, func() time.Time { return loopsClock })
		if err := s.registry.Close(); err != nil {
			t.Fatalf("close the registry: %v", err)
		}
		if err := s.sweepPushes(bg); err == nil {
			t.Fatal("sweepPushes = nil, want the subscriptions error")
		}
	})

	t.Run("a server with no vapid sweeps nothing", func(t *testing.T) {
		s := loopsServerForTest(t, func() time.Time { return loopsClock })
		s.vapid = nil
		if err := s.sweepPushes(bg); err != nil {
			t.Fatalf("sweepPushes = %v, want nil with no vapid", err)
		}
	})
}

func TestTheEventSweepReportsBothHalves(t *testing.T) {
	s := loopsServerForTest(t, func() time.Time { return loopsClock })
	if err := s.registry.Close(); err != nil {
		t.Fatalf("close the registry: %v", err)
	}
	stop := startLoop(t, s.watchEventSweep)
	defer stop()
	part := waitLoopReport(t, s.health.Snapshot, "hub.event-sweep", string(domain.HealthFailing))
	if part.Kind != string(domain.HealthLoop) {
		t.Fatalf("hub.event-sweep = %+v, want a loop", part)
	}
	if part.LastError == "" {
		t.Fatalf("hub.event-sweep = %+v, want the failed half's error", part)
	}
}

func TestAPanickingLoopShowsFailing(t *testing.T) {
	s := loopsServerForTest(t, func() time.Time { return loopsClock })
	safego.Go(s.loopWarn("hub.pids"), func() { panic("boom") })
	part := waitLoopReport(t, s.health.Snapshot, "hub.pids", string(domain.HealthFailing))
	if part.LastError != "panic: boom" {
		t.Fatalf("hub.pids lastError = %q, want the panic's first line only", part.LastError)
	}
	if part.Errors != 1 {
		t.Fatalf("hub.pids = %+v, want exactly one error", part)
	}
}

func TestADeadLoopGoesStale(t *testing.T) {
	clock := loopsClock
	s := loopsServerForTest(t, func() time.Time { return clock })
	s.health.Register("hub.pids", domain.HealthLoop, 30*time.Second)
	s.health.OK("hub.pids")
	clock = loopsClock.Add(91 * time.Second)
	part := loopPart(t, s.health.Snapshot(), "hub.pids")
	if part.State != string(domain.HealthStale) {
		t.Fatalf("hub.pids 91 s after its last pass = %+v, want stale", part)
	}
	if s.health.Healthy() {
		t.Fatal("healthy = true, want false while a loop is stale")
	}
}
