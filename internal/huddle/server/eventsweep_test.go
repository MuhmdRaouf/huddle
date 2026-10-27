package server

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

func TestWatchEventSweepPrunesTheRegistryAndEveryChannel(t *testing.T) {
	ctx, cancel := context.WithCancel(bg)
	defer cancel()
	home := t.TempDir()
	reg, err := hub.Open(ctx, filepath.Join(home, "hub.db"))
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if err := reg.Report(ctx, hub.Report{
		SessionID: "s-old", LastEvent: "Stop",
		LastEventAt: jsscalar.ISOTime(now.Add(-100 * 24 * time.Hour)),
	}); err != nil {
		t.Fatalf("seed the old report: %v", err)
	}
	if err := reg.Report(ctx, hub.Report{
		SessionID: "s-new", LastEvent: "Stop",
		LastEventAt: jsscalar.ISOTime(now.Add(-time.Hour)),
	}); err != nil {
		t.Fatalf("seed the young report: %v", err)
	}

	chHub := channel.NewHub(home, false)
	st, err := store.Open(ctx, chHub.File("main"))
	if err != nil {
		t.Fatalf("open the channel store: %v", err)
	}
	ch, err := channel.Open(ctx, "main", st)
	if err != nil {
		t.Fatalf("open the channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close(ctx); _ = st.Close() })
	seedTS := func(age time.Duration) {
		t.Helper()
		ts := now.Add(-age).UTC().Format("2006-01-02T15:04:05Z")
		if err := ch.Store.Run(ctx, "INSERT INTO events (ts, from_name, topic) VALUES (?, 'seed', 'chat')", ts); err != nil {
			t.Fatalf("seed %s: %v", ts, err)
		}
	}
	seedTS(100 * 24 * time.Hour)
	seedTS(time.Hour)

	s := &server{
		registry: reg,
		hub:      chHub,
		now:      func() time.Time { return now },
		o:        Options{Home: home, Stderr: io.Discard},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.watchEventSweep(ctx)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		events, err := reg.EventsAfter(ctx, 0, 10)
		if err != nil {
			t.Fatalf("read the registry events: %v", err)
		}
		left, err := ch.Store.Get(ctx, "SELECT COUNT(*) AS n FROM events")
		if err != nil {
			t.Fatalf("read the channel events: %v", err)
		}
		if len(events) == 1 {
			if n, _ := left["n"].(int64); n == 1 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sweep never pruned: %d registry events, %v channel events, want one of each",
				len(events), left["n"])
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchEventSweep never returned after the context ended")
	}
}
