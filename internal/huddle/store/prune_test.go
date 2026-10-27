package store

import (
	"context"
	"testing"
	"time"
)

func TestPruneEventsSparesAnUnansweredAskAndKeepsCursorReadsWhole(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	ts := func(age time.Duration) string { return now.Add(-age).UTC().Format("2006-01-02T15:04:05Z") }
	seed := func(age time.Duration, from string, needsReply int, replyTo any) {
		if err := s.Run(ctx,
			"INSERT INTO events (ts, from_name, topic, needs_reply, reply_to) VALUES (?, ?, 'chat', ?, ?)",
			ts(age), from, needsReply, replyTo); err != nil {
			t.Fatalf("seed %s: %v", ts(age), err)
		}
	}
	const old = 100 * 24 * time.Hour
	seed(old, "a", 0, nil)                  // a plain event past the TTL: goes
	seed(old, "a", 1, nil)                  // an ask past the TTL: goes, its reply is in
	seed(99*24*time.Hour, "b", 0, int64(2)) // the reply: goes with its ask
	seed(old, "a", 1, nil)                  // an ask nobody answered: stays whatever its age
	seed(90*24*time.Hour, "b", 0, nil)      // exactly at the cutoff: stays
	seed(time.Hour, "b", 0, nil)            // young: stays

	n, err := s.PruneEvents(ctx, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 3 {
		t.Fatalf("PruneEvents pruned %d rows, want 3", n)
	}
	left, err := s.All(ctx, "SELECT seq FROM events ORDER BY seq")
	if err != nil {
		t.Fatalf("read what is left: %v", err)
	}
	if len(left) != 3 {
		t.Fatalf("%d rows left, want 3 (the unanswered ask, the edge, the young one)", len(left))
	}
	// a member whose cursor sits past the pruned seqs still reads the right unread events
	if err := s.Run(ctx, "INSERT INTO sessions (name, joined_at, last_seen, cursor) VALUES ('x', ?, ?, 5)",
		ts(old), ts(old)); err != nil {
		t.Fatalf("seed the member: %v", err)
	}
	unread, err := s.All(ctx, "SELECT seq FROM events WHERE seq > ? AND from_name != ? ORDER BY seq", int64(5), "x")
	if err != nil {
		t.Fatalf("read the unread: %v", err)
	}
	if len(unread) != 1 {
		t.Fatalf("%d unread events for a cursor at seq 5, want the one young event", len(unread))
	}
}
