package board

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// openRoot builds an initialised board at its own temp root.
func openRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agents", "kanban")
	if _, err := Init(context.Background(), root, "", fixedNow); err != nil {
		t.Fatalf("init: %v", err)
	}
	return root
}

func TestOpenLeavesAFolderBoardAlone(t *testing.T) {
	ctx := context.Background()
	root := openRoot(t)
	b, closeBoard, err := Open(ctx, Board{Root: root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if b.Store != nil {
		t.Fatal("a folder board opened with a store")
	}
	if b.Root != root {
		t.Fatalf("root = %q, want %q", b.Root, root)
	}
	if err := closeBoard(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "kanban.db")); !os.IsNotExist(err) {
		t.Fatalf("stat kanban.db = %v, want no database file", err)
	}
	if cards, err := b.List(ctx, ""); err != nil || len(cards) != 0 {
		t.Fatalf("List = %v, %v; want an empty board", cards, err)
	}
}

func TestOpenUsesKanbanDbWhenPresent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(root, "kanban.db"))
	if err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Put(ctx, store.Ticket{ID: "KBN-1", Column: "backlog", Text: "seeded", UpdatedAt: dbNow.Format(time.RFC3339)}); err != nil {
		t.Fatalf("seed the ticket: %v", err)
	}

	b, closeBoard, err := Open(ctx, Board{Root: root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })
	if b.Store == nil {
		t.Fatal("a root holding kanban.db opened without its store")
	}
	if _, col, ok := b.Find("KBN-1"); !ok || col != "backlog" {
		t.Fatalf("Find KBN-1 = %q, %v; want it in backlog", col, ok)
	}
	_, err = b.New(ctx, NewTicket{
		Actor: "dev", ID: "KBN-2", Title: "Second card", Cluster: "fix",
		Type: "fix", Priority: "P1", Est: "S", Summary: longSummary,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	row, err := st.Get(ctx, "KBN-2")
	if err != nil {
		t.Fatalf("get the new card's row: %v", err)
	}
	if row.Column != "backlog" {
		t.Fatalf("the new card's column = %q, want backlog", row.Column)
	}
	if _, err := os.Stat(filepath.Join(root, "backlog", "KBN-2.md")); !os.IsNotExist(err) {
		t.Fatalf("stat the new card's file = %v, want no file on disk", err)
	}
	if err := closeBoard(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOpenRefusesACorruptDatabase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kanban.db"), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(context.Background(), Board{Root: root}); err == nil {
		t.Fatal("open over a file that is no database passed")
	}
}
