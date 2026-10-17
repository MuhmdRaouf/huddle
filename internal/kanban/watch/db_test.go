package watch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// newDbFixture builds a watcher over a db board: a root holding only kanban.db,
// its rows written through the store, its clock pinned.
func newDbFixture(t *testing.T) *fixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "board")
	st, err := store.Open(context.Background(), filepath.Join(root, "kanban.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{t: t, root: root, clock: &clock{now: baseTime}}
	for key, value := range map[string]string{"file:EPOCH": "0", "file:LOG": ""} {
		if err := st.SetMeta(context.Background(), key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
	f.b, _, err = board.Open(context.Background(), board.Board{Root: root, Now: f.clock.Now})
	if err != nil {
		t.Fatalf("open the db board: %v", err)
	}
	f.w = New(f.b, Options{Now: f.clock.Now})
	return f
}

// seed writes one ticket row into backlog, stamped with the fixture's clock.
func (f *fixture) seed(id, text string) {
	f.t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(f.root, "kanban.db"))
	if err != nil {
		f.t.Fatalf("open the store to seed: %v", err)
	}
	defer st.Close()
	if err := st.Put(context.Background(), store.Ticket{
		ID: id, Column: "backlog", Text: text,
		UpdatedAt: f.clock.now.UTC().Format(time.RFC3339Nano),
	}); err != nil {
		f.t.Fatalf("put %s: %v", id, err)
	}
}

// A watcher whose board holds kanban.db sees a write made through a second
// handle on the same root: the rows are the board, whatever handle wrote them.
func TestAWatcherSeesADbBoardChange(t *testing.T) {
	f := newDbFixture(t)
	f.seed("T-1", "---\nid: T-1\ntitle: \"gamma\"\ncluster: kanban\ntype: docs\n---\n\n# T-1\n\nBody.\n")
	d := f.scan()
	if _, ok := cardIn(d.Upsert, "T-1"); !ok {
		t.Fatalf("the first scan answered %+v; want T-1", d.Upsert)
	}
	if card, _ := cardIn(f.w.Snapshot().Cards, "T-1"); card.Column != "backlog" {
		t.Fatalf("the snapshot holds T-1 in %q, want backlog", card.Column)
	}
	f.quietScan()

	// a second board.Open on the same root writes through its own handle
	second, closeSecond, err := board.Open(context.Background(), board.Board{Root: f.root, Now: f.clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeSecond() })
	f.clock.advance(300 * time.Millisecond)
	if _, err := second.Set(context.Background(), "w1", "T-1", []string{"title=damma"}); err != nil {
		t.Fatalf("set through the second handle: %v", err)
	}
	d = f.scan()
	card, ok := cardIn(d.Upsert, "T-1")
	if !ok || card.Title != "damma" || card.Column != "backlog" {
		t.Fatalf("upsert = %+v; want T-1 with its new title", d.Upsert)
	}
	f.quietScan()
}

// Two writes inside one second are still two changes: the row stamp carries
// nanoseconds, so the size staying put does not hide the second write.
func TestAWatcherSeesTwoWritesInOneSecond(t *testing.T) {
	f := newDbFixture(t)
	f.seed("T-2", "---\nid: T-2\ntitle: \"gamma\"\ncluster: kanban\ntype: docs\n---\n\n# T-2\n\nBody.\n")
	f.scan()

	// the second write, same second, same length: only the stamp moves
	f.clock.advance(300 * time.Millisecond)
	if _, err := f.b.Set(context.Background(), "w1", "T-2", []string{"title=damma"}); err != nil {
		t.Fatalf("the second write: %v", err)
	}
	if d, changed := f.w.Scan(context.Background()); !changed {
		t.Fatal("the second write inside the same second answered no change")
	} else if card, ok := cardIn(d.Upsert, "T-2"); !ok || card.Title != "damma" {
		t.Fatalf("upsert = %+v; want T-2 reread", d.Upsert)
	}

	// the third write, still inside that second and back at the first title
	f.clock.advance(300 * time.Millisecond)
	if _, err := f.b.Set(context.Background(), "w1", "T-2", []string{"title=gamma"}); err != nil {
		t.Fatalf("the third write: %v", err)
	}
	d := f.scan()
	if card, ok := cardIn(d.Upsert, "T-2"); !ok || card.Title != "gamma" {
		t.Fatalf("upsert = %+v; want T-2 with its first title back", d.Upsert)
	}
	f.quietScan()
}
