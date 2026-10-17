package board

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// InitDB creates a board that lives in one kanban.db: the file and its meta
// rows, with no column folders, and the flow commands work on it at once.
func TestInitDBCreatesAnEmptyDbBoard(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), ".agents", "kanban")
	lines := mustLines(t, func() ([]string, error) { return InitDB(ctx, root, fixedNow) })
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := abs + ": created kanban.db, README.md, EPOCH, LOG"; len(lines) != 1 || lines[0] != want {
		t.Fatalf("InitDB lines = %q, want %q", lines, want)
	}
	if _, err := os.Stat(filepath.Join(root, "kanban.db")); err != nil {
		t.Fatalf("kanban.db missing: %v", err)
	}
	for _, col := range Columns {
		if _, err := os.Stat(filepath.Join(root, col)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("column dir %s = %v; init --db creates no folders", col, err)
		}
	}

	// the meta rows hold the board's own files
	st, err := store.Open(ctx, filepath.Join(root, "kanban.db"))
	if err != nil {
		t.Fatalf("reopen the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for key, want := range map[string]string{
		"file:README.md": readmeText, "file:EPOCH": "1", "file:LOG": "",
	} {
		if value, ok, err := st.GetMeta(ctx, key); err != nil || !ok || value != want {
			t.Errorf("%s = %q, %v, %v; want %q", key, value, ok, err, want)
		}
	}

	// the flow commands run on it at once
	b, closeBoard, err := Open(ctx, Board{Root: root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("open the db board: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte(fullBody("DB-1")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.New(ctx, NewTicket{
		Actor: "dev", ID: "DB-1", Title: "First db card", Cluster: "fix",
		Type: "fix", Priority: "P2", Est: "S", Summary: longSummary, BodyFile: body,
	}); err != nil {
		t.Fatalf("new on the db board: %v", err)
	}
	if _, err := b.Promote(ctx, Args{Actor: "dev", ID: "DB-1"}); err != nil {
		t.Fatalf("promote on the db board: %v", err)
	}
	if _, col, ok := b.Find("DB-1"); !ok || col != "ready" {
		t.Fatalf("DB-1 = %q, %v; want it in ready", col, ok)
	}

	// a second init --db never overwrites what is there
	again := mustLines(t, func() ([]string, error) { return InitDB(ctx, root, fixedNow) })
	if len(again) != 1 || !strings.Contains(again[0], "nothing to create") {
		t.Fatalf("the second InitDB = %q, want nothing to create", again)
	}
}

// MigrateDB turns a folder board into a db board in place: every ticket as a
// row with its file mtime, the board's own files as meta rows, and the folders
// kept aside.
func TestMigrateDBMovesEveryTicketAndBoardFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	one := f.card("backlog", "MIG-1", "id: MIG-1\ntitle: \"one\"\ntype: docs")
	two := f.card("ready", "MIG-2", "id: MIG-2\ntitle: \"two\"\ntype: docs")
	f.writeFile("STOP-LINE", `{"epoch":1,"actor":"lead","note":"red","ts":"2026-10-15T12:00:00+0300"}`)

	wantText := map[string]string{}
	wantAt := map[string]time.Time{}
	for _, p := range []string{one, two} {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		wantText[filepath.Base(p)] = string(data)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		wantAt[filepath.Base(p)] = info.ModTime()
	}
	wantMeta := map[string]string{}
	for _, name := range []string{"EPOCH", "LOG", "STOP-LINE"} {
		data, err := os.ReadFile(filepath.Join(f.root, name))
		if err != nil {
			t.Fatal(err)
		}
		wantMeta["file:"+name] = string(data)
	}

	lines := mustLines(t, func() ([]string, error) { return MigrateDB(ctx, f.b) })
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "migrated 2 tickets into ") ||
		!strings.Contains(lines[0], filepath.Join(f.root, "kanban.db")+"; the folders are kept in ") {
		t.Fatalf("MigrateDB lines = %q", lines)
	}

	// the root keeps the database and the archive, nothing else where the
	// folders and files stood
	entries, err := os.ReadDir(f.root)
	if err != nil {
		t.Fatal(err)
	}
	archive := ""
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".folders-") {
			archive = filepath.Join(f.root, e.Name())
			continue
		}
		if e.Name() != "kanban.db" {
			t.Errorf("the root holds %s after the migration; want only kanban.db and the archive", e.Name())
		}
	}
	if archive == "" {
		t.Fatalf("the root holds no .folders- directory: %v", entries)
	}
	for _, col := range Columns {
		if _, err := os.Stat(filepath.Join(archive, col)); err != nil {
			t.Errorf("the archive keeps no %s/: %v", col, err)
		}
	}
	for _, name := range []string{"EPOCH", "LOG", "STOP-LINE"} {
		if _, err := os.Stat(filepath.Join(archive, name)); err != nil {
			t.Errorf("the archive keeps no %s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(f.root, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s still sits at the root: %v", name, err)
		}
	}

	// the board reads the database now, with every text and time it carried
	db, closeBoard, err := Open(ctx, Board{Root: f.root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("reopen as a db board: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })
	for name, want := range wantText {
		data, err := db.ReadPath(filepath.Join(f.root, "backlog", name))
		if name == "MIG-2.md" {
			data, err = db.ReadPath(filepath.Join(f.root, "ready", name))
		}
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want its original bytes", name, data, err)
		}
	}
	for key, want := range wantMeta {
		if value, ok, err := db.Store.GetMeta(ctx, key); err != nil || !ok || value != want {
			t.Errorf("%s = %q, %v, %v; want its original bytes", key, value, ok, err)
		}
	}
	for name, at := range wantAt {
		col := "backlog"
		if name == "MIG-2.md" {
			col = "ready"
		}
		info, ok := db.StatPath(filepath.Join(f.root, col, name))
		if !ok {
			t.Fatalf("stat %s answered nothing", name)
		}
		if !info.ModTime.Equal(at) {
			t.Errorf("%s carried %v, want its file mtime %v", name, info.ModTime, at)
		}
	}
	if _, col, ok := db.Find("MIG-1"); !ok || col != "backlog" {
		t.Errorf("Find MIG-1 = %q, %v; want backlog", col, ok)
	}
}

// A root already holding kanban.db refuses a second migration.
func TestMigrateDBRefusesASecondRun(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "SEC-1", "id: SEC-1\ntitle: \"s\"\ntype: docs")
	mustLines(t, func() ([]string, error) { return MigrateDB(ctx, f.b) })
	_, err := MigrateDB(ctx, f.b)
	contains(t, refuseErr(t, err), f.root+" already holds kanban.db", "second migrate-db")
}

// Any failure before the commit leaves the board as it stood: no kanban.db.
func TestMigrateDBLeavesNothingOnFailure(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "GOOD-1", "id: GOOD-1\ntitle: \"g\"\ntype: docs")
	raw := append([]byte("---\nid: BAD-1\ntitle: \"b\"\n"), 0xff, 0xfe, '\n')
	raw = append(raw, []byte("---\n\n# BAD-1\n")...)
	if err := os.WriteFile(f.path("ready", "BAD-1"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := MigrateDB(ctx, f.b)
	contains(t, refuseErr(t, err), "BAD-1", "not UTF-8")
	if _, err := os.Stat(filepath.Join(f.root, "kanban.db")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a failed migration left kanban.db behind: %v", err)
	}
	if _, err := os.Stat(f.path("backlog", "GOOD-1")); err != nil {
		t.Errorf("the failed migration touched the folders: %v", err)
	}
}

// A db board exports back into folders byte for byte.
func TestExportMarkdownRoundTripsByteForByte(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "RT-1", "id: RT-1\ntitle: \"one\"\ntype: docs")
	f.card("ready", "RT-2", "id: RT-2\ntitle: \"two\"\ntype: docs")
	f.writeFile("STOP-LINE", `{"epoch":1,"actor":"lead","note":"red","ts":"2026-10-15T12:00:00+0300"}`)

	orig := map[string][]byte{}
	err := filepath.WalkDir(f.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(f.root, path)
		if err != nil {
			return err
		}
		orig[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	mustLines(t, func() ([]string, error) { return MigrateDB(ctx, f.b) })
	db, closeBoard, err := Open(ctx, Board{Root: f.root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("reopen as a db board: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })
	out := filepath.Join(t.TempDir(), "exported")
	lines := mustLines(t, func() ([]string, error) { return ExportMarkdown(ctx, db, out) })
	if len(lines) != 1 || lines[0] != "exported 2 tickets to "+out {
		t.Fatalf("ExportMarkdown lines = %q, want one line naming %s", lines, out)
	}

	got := map[string][]byte{}
	err = filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(out, path)
		if err != nil {
			return err
		}
		got[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range orig {
		if !bytes.Equal(got[rel], want) {
			t.Errorf("%s = %q, want its original bytes %q", rel, got[rel], want)
		}
	}
	if len(got) != len(orig) {
		t.Errorf("the export holds %d files, the board had %d", len(got), len(orig))
	}
}

// A directory that already holds files refuses the export.
func TestExportMarkdownRefusesANonEmptyDir(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "FULL-1", "id: FULL-1\ntitle: \"f\"\ntype: docs")
	out := filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "keep.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ExportMarkdown(ctx, f.b, out)
	contains(t, refuseErr(t, err), "not empty", "export-md refusal")
	// an empty directory is fine, and the board is untouched
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	mustLines(t, func() ([]string, error) { return ExportMarkdown(ctx, f.b, empty) })
	if data, err := os.ReadFile(filepath.Join(empty, "backlog", "FULL-1.md")); err != nil || len(data) == 0 {
		t.Errorf("the folder-board export = %q, %v", data, err)
	}
}
