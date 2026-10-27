package watch

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// FEATURES on the watcher: a closed feature's cards leave the snapshot, the
// counts and every delta, and come back whole when the feature reopens.

// seedCard writes one ticket carrying the named cluster: a file on a folder
// board, a row on a db board.
func (f *fixture) seedCard(col, id, cluster string) {
	f.t.Helper()
	fm := "id: " + id + "\ntitle: \"" + id + " title\"\ncluster: " + cluster + "\ntype: docs"
	if f.b.Store == nil {
		f.card(col, id, fm)
		return
	}
	text := "---\n" + fm + "\n---\n\n# " + id + "\n\nBody text.\n"
	st, err := store.Open(context.Background(), filepath.Join(f.root, "kanban.db"))
	if err != nil {
		f.t.Fatalf("open the store to seed: %v", err)
	}
	defer func() { _ = st.Close() }()
	if err := st.Put(context.Background(), store.Ticket{
		ID: id, Column: col, Text: text,
		UpdatedAt: f.clock.now.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		f.t.Fatalf("put %s: %v", id, err)
	}
}

func TestAClosedFeatureLeavesTheSnapshot(t *testing.T) {
	t.Run("on a folder board", func(t *testing.T) {
		followAClosedFeature(t, newFixture(t))
	})

	t.Run("on a db board", func(t *testing.T) {
		followAClosedFeature(t, newDbFixture(t))
	})
}

// followAClosedFeature walks one scenario: scan, close, scan, reopen, scan,
// then a quiet pass.
func followAClosedFeature(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()

	f.seedCard("backlog", "SWR-1", "swr")
	f.seedCard("ready", "SWR-2", "swr")
	f.seedCard("review", "SWR-3", "swr")
	f.seedCard("backlog", "WEB-1", "web")

	// the first scan answers the four cards and no closed feature
	d := f.scan()
	if len(d.Upsert) != 4 {
		t.Fatalf("the first scan upserted %+v, want the four cards", d.Upsert)
	}
	if d.Meta == nil || d.Meta.Features.Closed == nil || len(d.Meta.Features.Closed) != 0 {
		t.Fatalf("the first delta's features = %+v, want an empty closed list", d.Meta)
	}
	if encoded := string(marshal(t, d.Meta.Features)); encoded != `{"closed":[]}` {
		t.Fatalf("meta features = %s, want {\"closed\":[]}", encoded)
	}
	snap := f.w.Snapshot()
	if len(snap.Cards) != 4 {
		t.Fatalf("the snapshot holds %+v, want the four cards", snap.Cards)
	}
	if snap.Features.Closed == nil || len(snap.Features.Closed) != 0 {
		t.Fatalf("snapshot features.closed = %#v, want an empty list", snap.Features.Closed)
	}
	if encoded := string(marshal(t, snap.Features)); encoded != `{"closed":[]}` {
		t.Fatalf("snapshot features = %s, want {\"closed\":[]}", encoded)
	}
	asTheyWere := map[string]board.Card{}
	for _, c := range snap.Cards {
		asTheyWere[c.ID] = c
	}

	// closing swr hides its three cards: out of the delta, the snapshot and
	// the counts, named in features.closed
	f.clock.advance(time.Minute)
	if _, err := f.b.CloseFeature(ctx, board.Args{Actor: "lead", ID: "swr"}); err != nil {
		t.Fatal(err)
	}
	d = f.scan()
	if want := []string{"SWR-1", "SWR-2", "SWR-3"}; !reflect.DeepEqual(d.Remove, want) {
		t.Fatalf("remove = %v, want %v", d.Remove, want)
	}
	if len(d.Upsert) != 0 {
		t.Fatalf("upsert = %+v, want none", d.Upsert)
	}
	if d.Meta == nil {
		t.Fatal("the close answered no meta")
	}
	counts := map[string]int{}
	for _, col := range d.Meta.Columns {
		counts[col.ID] = col.Count
	}
	if counts["backlog"] != 1 || counts["ready"] != 0 || counts["review"] != 0 {
		t.Fatalf("counts = %v, want backlog 1, ready 0, review 0", counts)
	}
	closed := ClosedFeature{
		Name: "swr", ClosedAt: f.clock.now.Format("2006-01-02T15:04:05-0700"), Cards: 3,
	}
	if got := d.Meta.Features.Closed; len(got) != 1 || got[0] != closed {
		t.Fatalf("features.closed = %+v, want [%+v]", got, closed)
	}
	snap = f.w.Snapshot()
	if len(snap.Cards) != 1 || snap.Cards[0].ID != "WEB-1" {
		t.Fatalf("snapshot cards = %+v, want WEB-1 only", snap.Cards)
	}
	if got := snap.Features.Closed; len(got) != 1 || got[0] != closed {
		t.Fatalf("snapshot features.closed = %+v, want [%+v]", got, closed)
	}

	// reopening brings the three cards back, unchanged
	f.clock.advance(time.Minute)
	if _, err := f.b.ReopenFeature(ctx, board.Args{Actor: "lead", ID: "swr"}); err != nil {
		t.Fatal(err)
	}
	d = f.scan()
	if len(d.Remove) != 0 {
		t.Fatalf("remove = %v, want none", d.Remove)
	}
	if len(d.Upsert) != 3 {
		t.Fatalf("upsert = %+v, want the three swr cards back", d.Upsert)
	}
	for _, id := range []string{"SWR-1", "SWR-2", "SWR-3"} {
		card, ok := cardIn(d.Upsert, id)
		if !ok {
			t.Fatalf("upsert lacks %s: %+v", id, d.Upsert)
		}
		if !reflect.DeepEqual(card, asTheyWere[id]) {
			t.Fatalf("%s came back as %+v, want it as it was: %+v", id, card, asTheyWere[id])
		}
	}
	if d.Meta == nil {
		t.Fatal("the reopen answered no meta")
	}
	counts = map[string]int{}
	for _, col := range d.Meta.Columns {
		counts[col.ID] = col.Count
	}
	if counts["backlog"] != 2 || counts["ready"] != 1 || counts["review"] != 1 {
		t.Fatalf("counts = %v, want backlog 2, ready 1, review 1", counts)
	}
	if len(d.Meta.Features.Closed) != 0 {
		t.Fatalf("features.closed = %+v, want it empty", d.Meta.Features.Closed)
	}
	if n := len(f.w.Snapshot().Cards); n != 4 {
		t.Fatalf("the snapshot holds %d cards, want 4", n)
	}

	// a pass whose features did not move answers no change
	f.quietScan()
}
