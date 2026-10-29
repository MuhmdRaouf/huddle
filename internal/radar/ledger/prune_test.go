package ledger

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// The metadata prune: request rows and their facts live MetaTTL. Run against a ledger.db
// in a temp directory only.

// TestStorePrunesPastTheTTL files rows started 100, 89 and 1 days before the prune, and
// one row the file heard of without a start time; only the 100-day row and its facts go.
func TestStorePrunesPastTheTTL(t *testing.T) {
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

	went, err := st.Prune(ctx, now)
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
