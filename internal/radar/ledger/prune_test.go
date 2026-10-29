package ledger

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// The metadata prune: request rows and their facts live the days the caller hands Prune.
// Run against a ledger.db in a temp directory only.

// TestStorePrunesPastTheDaysGiven files rows started 100, 89 and 1 days before the prune,
// and one row the file heard of without a start time; at 90 days only the 100-day row and
// its facts go.
func TestStorePrunesPastTheDaysGiven(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

	row100 := Row{ID: "req_100", StartedAt: now.Add(-100 * 24 * time.Hour).UnixMilli()}
	row89 := Row{ID: "req_89", StartedAt: now.Add(-89 * 24 * time.Hour).UnixMilli()}
	row1 := Row{ID: "req_1", StartedAt: now.Add(-24 * time.Hour).UnixMilli()}
	for _, row := range []Row{row100, row89, row1} {
		if err := st.Upsert(ctx, row); err != nil {
			t.Fatalf("upsert %s: %v", row.ID, err)
		}
	}
	// Row.StartedAt carries no nil, so the row without a start goes in by hand.
	if err := st.db.Run("INSERT INTO ledger_requests (id) VALUES ('req_none')"); err != nil {
		t.Fatalf("insert the row without a start: %v", err)
	}
	// Facts may be filed before the row exists, so every id here has facts of its own.
	for _, id := range []string{"req_100", "req_89", "req_none"} {
		if err := st.db.Run("INSERT INTO ledger_facts (id, route, transcript) VALUES (?, '', '')", id); err != nil {
			t.Fatalf("file the facts of %s: %v", id, err)
		}
	}

	went, err := st.Prune(ctx, now, 90)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if went != 1 {
		t.Fatalf("the prune removed %d request rows, want only the 100-day one", went)
	}
	if row, err := st.Get(ctx, "req_100"); err != nil || row != nil {
		t.Fatalf("the 100-day row = %+v, %v; want it gone", row, err)
	}
	if holdsFacts(t, st, "req_100") {
		t.Fatal("the 100-day row's facts are still filed, want them gone with it")
	}
	got89, err := st.Get(ctx, "req_89")
	if err != nil || got89 == nil || !reflect.DeepEqual(*got89, row89) {
		t.Fatalf("the 89-day row = %+v, %v; want it kept whole", got89, err)
	}
	if row, err := st.Get(ctx, "req_1"); err != nil || row == nil {
		t.Fatalf("the 1-day row = %+v, %v; want it kept", row, err)
	}
	if row, err := st.Get(ctx, "req_none"); err != nil || row == nil {
		t.Fatalf("the row without a start = %+v, %v; want it kept", row, err)
	}
	for _, id := range []string{"req_89", "req_none"} {
		if !holdsFacts(t, st, id) {
			t.Fatalf("the facts of %s are gone, want them kept", id)
		}
	}

	t.Run("seven days keeps only the last week", func(t *testing.T) {
		week := openLedger(t)
		for _, row := range []Row{
			{ID: "req_8", StartedAt: now.Add(-8 * 24 * time.Hour).UnixMilli()},
			{ID: "req_6", StartedAt: now.Add(-6 * 24 * time.Hour).UnixMilli()},
		} {
			if err := week.Upsert(ctx, row); err != nil {
				t.Fatalf("upsert %s: %v", row.ID, err)
			}
			if err := week.db.Run("INSERT INTO ledger_facts (id, route, transcript) VALUES (?, '', '')", row.ID); err != nil {
				t.Fatalf("file the facts of %s: %v", row.ID, err)
			}
		}

		went, err := week.Prune(ctx, now, 7)
		if err != nil {
			t.Fatalf("prune: %v", err)
		}
		if went != 1 {
			t.Fatalf("the prune removed %d request rows, want only the 8-day one", went)
		}
		if row, err := week.Get(ctx, "req_8"); err != nil || row != nil {
			t.Fatalf("the 8-day row = %+v, %v; want it gone", row, err)
		}
		if holdsFacts(t, week, "req_8") {
			t.Fatal("the 8-day row's facts are still filed, want them gone with it")
		}
		if row, err := week.Get(ctx, "req_6"); err != nil || row == nil {
			t.Fatalf("the 6-day row = %+v, %v; want it kept", row, err)
		}
		if !holdsFacts(t, week, "req_6") {
			t.Fatal("the 6-day row's facts are gone, want them kept")
		}
	})

	// keepsEveryRow seeds a 1000-day row and a 1-day one, prunes at the days given, and
	// wants nothing to move.
	keepsEveryRow := func(t *testing.T, days int) {
		t.Helper()
		all := openLedger(t)
		for _, row := range []Row{
			{ID: "req_1000", StartedAt: now.Add(-1000 * 24 * time.Hour).UnixMilli()},
			{ID: "req_1", StartedAt: now.Add(-24 * time.Hour).UnixMilli()},
		} {
			if err := all.Upsert(ctx, row); err != nil {
				t.Fatalf("upsert %s: %v", row.ID, err)
			}
			if err := all.db.Run("INSERT INTO ledger_facts (id, route, transcript) VALUES (?, '', '')", row.ID); err != nil {
				t.Fatalf("file the facts of %s: %v", row.ID, err)
			}
		}

		went, err := all.Prune(ctx, now, days)
		if err != nil {
			t.Fatalf("prune: %v", err)
		}
		if went != 0 {
			t.Fatalf("the prune removed %d request rows at %d days, want none", went, days)
		}
		for _, id := range []string{"req_1000", "req_1"} {
			if row, err := all.Get(ctx, id); err != nil || row == nil {
				t.Fatalf("the row %s = %+v, %v; want it kept", id, row, err)
			}
			if !holdsFacts(t, all, id) {
				t.Fatalf("the facts of %s are gone, want them kept", id)
			}
		}
	}
	t.Run("zero days keeps every row", func(t *testing.T) { keepsEveryRow(t, 0) })
	t.Run("a negative day count keeps every row", func(t *testing.T) { keepsEveryRow(t, -1) })
}

// TestThePruneKeepsATranscriptOnlyRowItsTranscriptStamps: the facts of a request only a
// transcript saw carry its ts, so its row starts inside the window the prune keeps and
// stays with its facts.
func TestThePruneKeepsATranscriptOnlyRowItsTranscriptStamps(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	ts := now.Add(-24 * time.Hour).UnixMilli()

	if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_ts_guard", Ts: ts}); err != nil {
		t.Fatalf("file the transcript facts: %v", err)
	}
	went, err := st.Prune(ctx, now, 30)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	row, err := st.Get(ctx, "req_ts_guard")
	if err != nil || row == nil {
		t.Fatalf("the transcript-only row = %+v, %v; want it kept by the prune", row, err)
	}
	if row.StartedAt != ts {
		t.Errorf("StartedAt = %d, want the transcript's %d", row.StartedAt, ts)
	}
	if !holdsFacts(t, st, "req_ts_guard") {
		t.Fatal("the transcript-only row's facts are gone, want them kept")
	}
	if went != 0 {
		t.Errorf("the prune removed %d request rows, want none", went)
	}
}

// holdsFacts answers whether the file still holds a facts row for the id.
func holdsFacts(t *testing.T, st *Store, id string) bool {
	t.Helper()
	found, err := st.db.Get("SELECT id FROM ledger_facts WHERE id = ?", id)
	if err != nil {
		t.Fatalf("read the facts of %s: %v", id, err)
	}
	return found != nil
}
