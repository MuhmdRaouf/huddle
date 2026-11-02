package hub

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

const day = 24 * time.Hour

// seedEvent stores one reported event of the given kind, as Report would, without the
// session row.
func seedEvent(t *testing.T, db *DB, session, at, kind string) {
	t.Helper()
	if err := db.st.Run(context.Background(),
		"INSERT INTO events (session_id, at, kind, json) VALUES (?, ?, ?, '{}')", session, at, kind); err != nil {
		t.Fatalf("seed %s %s: %v", session, kind, err)
	}
}

// countRow is one event_counts row.
type countRow struct {
	Session, Day, Kind string
	N                  int64
}

func countsOf(t *testing.T, db *DB) []countRow {
	t.Helper()
	rows, err := db.st.All(context.Background(),
		"SELECT session_id, day, kind, n FROM event_counts ORDER BY session_id, day, kind")
	if err != nil {
		t.Fatalf("read event_counts: %v", err)
	}
	out := make([]countRow, 0, len(rows))
	for _, r := range rows {
		n, _ := r["n"].(int64)
		out = append(out, countRow{textOfRow(r, "session_id"), textOfRow(r, "day"), textOfRow(r, "kind"), n})
	}
	return out
}

func eventCount(t *testing.T, db *DB) int64 {
	t.Helper()
	row, err := db.st.Get(context.Background(), "SELECT CAST(COUNT(*) AS INTEGER) AS n FROM events")
	if err != nil || row == nil {
		t.Fatalf("count events: %v", err)
	}
	return row["n"].(int64)
}

func TestPruneEventsCutsPastSevenDaysAndKeepsCursorReadsWhole(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	at := func(age time.Duration) string { return jsscalar.ISOTime(now.Add(-age)) }
	for _, e := range []struct{ session, at string }{
		{"s-old", at(8 * day)},
		{"s-edge", at(7 * day)}, // exactly at the cutoff: stays
		{"s-inside", at(6 * day)},
		{"s-young", at(time.Hour)},
	} {
		seedEvent(t, db, e.session, e.at, "Stop")
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

func TestPruneEventsCountsWhatItCutsPerSessionDayAndKind(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	at := func(age time.Duration) string { return jsscalar.ISOTime(now.Add(-age)) }
	seedEvent(t, db, "s-a", at(8*day), "PreToolUse")
	seedEvent(t, db, "s-a", at(8*day-time.Minute), "PreToolUse")
	seedEvent(t, db, "s-a", at(8*day), "PostToolUse")
	seedEvent(t, db, "s-b", at(9*day), "Stop")
	seedEvent(t, db, "s-a", at(6*day), "PreToolUse") // inside the window: stays and is not counted

	n, err := db.PruneEvents(ctx, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 4 {
		t.Fatalf("PruneEvents pruned %d rows, want 4", n)
	}
	want := []countRow{
		{"s-a", "2026-10-19", "PostToolUse", 1},
		{"s-a", "2026-10-19", "PreToolUse", 2},
		{"s-b", "2026-10-18", "Stop", 1},
	}
	got := countsOf(t, db)
	if len(got) != len(want) {
		t.Fatalf("event_counts = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event_counts[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if left := eventCount(t, db); left != 1 {
		t.Errorf("%d events left, want the one inside the window", left)
	}
}

func TestPruneEventsAddsToTheCountsOfADaySweptInTwoPieces(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	seedEvent(t, db, "s-a", "2026-10-19T08:00:00.000Z", "Stop")
	seedEvent(t, db, "s-a", "2026-10-19T20:00:00.000Z", "Stop")
	// the first cutoff, 2026-10-19T14:00, falls inside the day: it takes the morning event
	if _, err := db.PruneEvents(ctx, time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if _, err := db.PruneEvents(ctx, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	got := countsOf(t, db)
	if len(got) != 1 || got[0].N != 2 {
		t.Fatalf("event_counts = %+v, want one row counting 2", got)
	}
}

func TestPruneEventsKeepsEventsAndCountsWhenTheDeleteFails(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	seedEvent(t, db, "s-a", jsscalar.ISOTime(now.Add(-8*day)), "Stop")
	if err := db.st.Run(ctx, `CREATE TRIGGER refuse_delete BEFORE DELETE ON events
	  BEGIN SELECT RAISE(ABORT, 'delete refused'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if _, err := db.PruneEvents(ctx, now); err == nil {
		t.Fatal("PruneEvents answered no error, want the refused delete")
	}
	if got := countsOf(t, db); len(got) != 0 {
		t.Errorf("event_counts = %+v after a failed sweep, want none", got)
	}
	if left := eventCount(t, db); left != 1 {
		t.Errorf("%d events left after a failed sweep, want 1", left)
	}
}

func TestPruneEventsHandsBackTheFreedPages(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	now := time.Now().UTC()
	pad := make([]byte, 600)
	for i := range pad {
		pad[i] = 'x'
	}
	if err := db.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		for i := 0; i < 3000; i++ {
			if err := q.Run(ctx, "INSERT INTO events (session_id, at, kind, json) VALUES ('s-a', ?, 'PreToolUse', ?)",
				jsscalar.ISOTime(now.Add(-9*day)), string(pad)); err != nil {
				return err
			}
		}
		return q.Run(ctx, "INSERT INTO events (session_id, at, kind, json) VALUES ('s-a', ?, 'Stop', '{}')",
			jsscalar.ISOTime(now))
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before := pagesOf(t, db, "page_count")
	if _, err := db.PruneEvents(ctx, now); err != nil {
		t.Fatalf("prune: %v", err)
	}
	after := pagesOf(t, db, "page_count")
	if after >= before/2 {
		t.Errorf("page_count %d -> %d after the sweep, want it to drop below half", before, after)
	}
	if free := pagesOf(t, db, "freelist_count"); free != 0 {
		t.Errorf("freelist_count = %d after the sweep, want 0", free)
	}
}

func pagesOf(t *testing.T, db *DB, pragma string) int64 {
	t.Helper()
	row, err := db.st.Get(context.Background(), "PRAGMA "+pragma)
	if err != nil || row == nil {
		t.Fatalf("pragma %s: %v", pragma, err)
	}
	n, _ := row[pragma].(int64)
	return n
}

func TestOpenKeepsTheFileOnIncrementalVacuum(t *testing.T) {
	db := openForTest(t)
	if mode := pagesOf(t, db, "auto_vacuum"); mode != 2 {
		t.Errorf("auto_vacuum = %d on a fresh file, want 2 (incremental)", mode)
	}
}

// TestMigration28CountsAndCutsEventsPastSevenDaysTogether builds the schema the previous
// release wrote, seeds it with old and young events, opens it with this build and proves the
// counts hold every event the cut took, the cut left only the young ones, and the file is on
// incremental vacuum.
func TestMigration28CountsAndCutsEventsPastSevenDaysTogether(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "hub.db")
	st, err := store.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Run(ctx, "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatalf("meta: %v", err)
	}
	for n, migration := range migrations[:27] {
		if err := st.Run(ctx, migration); err != nil {
			t.Fatalf("migration %d: %v", n+1, err)
		}
	}
	if err := st.Run(ctx, "INSERT INTO meta (key, value) VALUES ('schema_version', '27')"); err != nil {
		t.Fatalf("mark schema 27: %v", err)
	}
	now := time.Now().UTC()
	old := now.Add(-10 * day)
	for _, e := range []struct{ session, at, kind string }{
		{"s-a", jsscalar.ISOTime(old), "PreToolUse"},
		{"s-a", jsscalar.ISOTime(old.Add(time.Second)), "PreToolUse"},
		{"s-a", jsscalar.ISOTime(old), "PostToolUse"},
		{"s-b", jsscalar.ISOTime(now.Add(-8 * day)), "Stop"},
		{"s-b", jsscalar.ISOTime(now.Add(-2 * day)), "Stop"},
		{"s-b", jsscalar.ISOTime(now), "Stop"},
	} {
		if err := st.Run(ctx, "INSERT INTO events (session_id, at, kind, json) VALUES (?, ?, ?, '{}')",
			e.session, e.at, e.kind); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := Open(ctx, file)
	if err != nil {
		t.Fatalf("open at schema 28: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	want := []countRow{
		{"s-a", old.Format("2006-01-02"), "PostToolUse", 1},
		{"s-a", old.Format("2006-01-02"), "PreToolUse", 2},
		{"s-b", now.Add(-8 * day).Format("2006-01-02"), "Stop", 1},
	}
	got := countsOf(t, db)
	if len(got) != len(want) {
		t.Fatalf("event_counts = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event_counts[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if left := eventCount(t, db); left != 2 {
		t.Errorf("%d events left, want the 2 inside the window", left)
	}
	if mode := pagesOf(t, db, "auto_vacuum"); mode != 2 {
		t.Errorf("auto_vacuum = %d after the migration, want 2 (incremental)", mode)
	}
	// the sweep right after the migration finds nothing left to count
	if n, err := db.PruneEvents(ctx, now); err != nil || n != 0 {
		t.Errorf("the first sweep pruned %d rows (%v), want 0", n, err)
	}
	if again := countsOf(t, db); len(again) != len(want) || again[1].N != 2 {
		t.Errorf("event_counts after the first sweep = %+v, want it unchanged", again)
	}
}
