package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

func TestPruneEventsCutsPastTheTTLAndKeepsCursorReadsWhole(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	at := func(age time.Duration) string { return jsscalar.ISOTime(now.Add(-age)) }
	for _, e := range []struct{ session, at string }{
		{"s-old", at(100 * 24 * time.Hour)},
		{"s-edge", at(90 * 24 * time.Hour)}, // exactly at the cutoff: stays
		{"s-inside", at(89 * 24 * time.Hour)},
		{"s-young", at(time.Hour)},
	} {
		if err := db.st.Run(ctx, "INSERT INTO events (session_id, at, kind, json) VALUES (?, ?, 'Stop', '{}')",
			e.session, e.at); err != nil {
			t.Fatalf("seed %s: %v", e.session, err)
		}
	}
	seeded, err := db.EventsAfter(ctx, 0, 10)
	if err != nil || len(seeded) != 4 {
		t.Fatalf("seeded %d events (%v), want 4", len(seeded), err)
	}
	prunedSeq := seeded[0].Seq

	n, err := db.PruneEvents(ctx, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("PruneEvents pruned %d rows, want 1", n)
	}
	// a reader walking a cursor past the pruned row still answers the rest, in order
	after, err := db.EventsAfter(ctx, prunedSeq, 10)
	if err != nil {
		t.Fatalf("read after seq %d: %v", prunedSeq, err)
	}
	if len(after) != 3 {
		t.Fatalf("%d events after seq %d, want the 3 survivors", len(after), prunedSeq)
	}
	for i, e := range after {
		if want := prunedSeq + int64(i) + 1; e.Seq != want {
			t.Errorf("event %d is seq %d, want %d", i, e.Seq, want)
		}
	}
}
