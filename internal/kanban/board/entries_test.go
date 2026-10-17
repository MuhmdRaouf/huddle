package board

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// One board shape, two carriers: every read the watcher makes answers the same
// on a folder board and on a db board.
func TestColumnEntriesMatchOnBothBoards(t *testing.T) {
	ctx := context.Background()

	// The folder board, holding two tickets in one column and one in another.
	folderRoot := filepath.Join(t.TempDir(), "board")
	if _, err := Init(ctx, folderRoot, "", fixedNow); err != nil {
		t.Fatalf("init the folder board: %v", err)
	}
	folder := Board{Root: folderRoot, Now: func() time.Time { return fixedNow }}
	writeFile(t, filepath.Join(folderRoot, "backlog", "ENT-2.md"), "---\nid: ENT-2\ntitle: \"two\"\n---\n\ntwo\n")
	writeFile(t, filepath.Join(folderRoot, "backlog", "ENT-1.md"), "---\nid: ENT-1\ntitle: \"one\"\n---\n\none\n")
	writeFile(t, filepath.Join(folderRoot, "ready", "ENT-3.md"), "---\nid: ENT-3\ntitle: \"three\"\n---\n\nthree\n")
	writeFile(t, filepath.Join(folderRoot, "EPOCH"), "4")

	// The db board, holding the same tickets as rows.
	dbRoot := filepath.Join(t.TempDir(), "board")
	st, err := store.Open(ctx, filepath.Join(dbRoot, "kanban.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := dbFiles{root: dbRoot, st: st, now: func() time.Time { return dbNow }}
	for _, row := range []struct{ col, id, text string }{
		{"backlog", "ENT-2", "---\nid: ENT-2\ntitle: \"two\"\n---\n\ntwo\n"},
		{"backlog", "ENT-1", "---\nid: ENT-1\ntitle: \"one\"\n---\n\none\n"},
		{"ready", "ENT-3", "---\nid: ENT-3\ntitle: \"three\"\n---\n\nthree\n"},
	} {
		if err := f.writeFile(ctx, filepath.Join(dbRoot, row.col, row.id+".md"), []byte(row.text)); err != nil {
			t.Fatalf("write %s: %v", row.id, err)
		}
	}
	if err := f.writeFile(ctx, filepath.Join(dbRoot, "EPOCH"), []byte("4")); err != nil {
		t.Fatalf("write EPOCH: %v", err)
	}
	db, closeBoard, err := Open(ctx, Board{Root: dbRoot, Now: func() time.Time { return dbNow }})
	if err != nil {
		t.Fatalf("open the db board: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })

	// The entries match by name, order and size; the paths differ by root.
	folderEntries := folder.ColumnEntries("backlog")
	dbEntries := db.ColumnEntries("backlog")
	if len(folderEntries) != 2 || len(dbEntries) != 2 {
		t.Fatalf("entries = %d folder, %d db; want 2 each", len(folderEntries), len(dbEntries))
	}
	for i := range folderEntries {
		fe, de := folderEntries[i], dbEntries[i]
		if fe.Name != de.Name || fe.Size != de.Size {
			t.Errorf("entry %d: folder %q/%d, db %q/%d", i, fe.Name, fe.Size, de.Name, de.Size)
		}
		if fe.Path != filepath.Join(folderRoot, "backlog", fe.Name) {
			t.Errorf("folder path = %q, want it under %s", fe.Path, folderRoot)
		}
		if de.Path != filepath.Join(dbRoot, "backlog", de.Name) {
			t.Errorf("db path = %q, want it under %s", de.Path, dbRoot)
		}
		if fe.ModTime.IsZero() || de.ModTime.IsZero() {
			t.Errorf("entry %s: folder modtime %v, db modtime %v; both must answer", fe.Name, fe.ModTime, de.ModTime)
		}
		if folderEntries[0].Name > folderEntries[1].Name {
			t.Errorf("entries are not sorted by name: %v", folderEntries)
		}
	}
	if got := folder.ColumnEntries("review"); got != nil {
		t.Errorf("an empty column answered %v, want nil", got)
	}
	if got := db.ColumnEntries("review"); got != nil {
		t.Errorf("an empty db column answered %v, want nil", got)
	}

	// A read through the seam answers the same bytes on both boards.
	for _, b := range []Board{folder, db} {
		data, err := b.ReadPath(filepath.Join(b.Root, "backlog", "ENT-1.md"))
		if err != nil {
			t.Fatalf("%s: read ENT-1: %v", b.Root, err)
		}
		if !bytes.Equal(data, []byte("---\nid: ENT-1\ntitle: \"one\"\n---\n\none\n")) {
			t.Errorf("%s: ENT-1 = %q", b.Root, data)
		}
		// A stat through the seam answers the size on both boards.
		info, ok := b.StatPath(filepath.Join(b.Root, "backlog", "ENT-1.md"))
		if !ok || info.Size != int64(len("---\nid: ENT-1\ntitle: \"one\"\n---\n\none\n")) {
			t.Errorf("%s: stat ENT-1 = %+v, %v", b.Root, info, ok)
		}
		// The board's own files read the same way.
		if data, err = b.ReadPath(filepath.Join(b.Root, "EPOCH")); err != nil || string(data) != "4" {
			t.Errorf("%s: EPOCH = %q, %v; want \"4\"", b.Root, data, err)
		}
	}
}

// writeFile lands one file whole, creating its directory.
func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}
