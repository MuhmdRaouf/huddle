package board

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// dbNow is the clock dbFiles stamps its rows with.
var dbNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func TestDirFilesIsTheOS(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	d := dirFiles{}

	if err := d.mkdirAll(filepath.Join(root, "backlog")); err != nil {
		t.Fatalf("mkdirAll: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "backlog")); err != nil || !info.IsDir() {
		t.Fatalf("mkdirAll left no directory: %v, %v", info, err)
	}

	if err := d.writeFile(ctx, filepath.Join(root, "EPOCH"), []byte("1")); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "EPOCH"))
	if err != nil || string(data) != "1" {
		t.Fatalf("the file on disk = %q, %v; want \"1\"", data, err)
	}
	if info, err := os.Stat(filepath.Join(root, "EPOCH")); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v; want 0o644", info.Mode(), err)
	}

	info, ok := d.stat(filepath.Join(root, "EPOCH"))
	if !ok || info.ModTime.IsZero() {
		t.Fatalf("stat = %+v, %v; want the file's modTime", info, ok)
	}
	if _, ok := d.stat(filepath.Join(root, "gone")); ok {
		t.Fatal("stat of a missing path answered ok")
	}

	for _, name := range []string{"b.md", "a.md", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := d.readDir(root), []string{"a.md", "b.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("readDir = %v, want %v", got, want)
	}
	if got := d.readDir(filepath.Join(root, "no-such-column")); got != nil {
		t.Fatalf("readDir of a missing directory = %v, want nil", got)
	}

	data, err = d.readFile(filepath.Join(root, "EPOCH"))
	if err != nil || string(data) != "1" {
		t.Fatalf("readFile = %q, %v; want \"1\"", data, err)
	}
	if _, err := d.readFile(filepath.Join(root, "gone")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("readFile of a missing path = %v, want an fs.ErrNotExist", err)
	}

	if err := d.appendLine(filepath.Join(root, "LOG"), "first"); err != nil {
		t.Fatalf("appendLine: %v", err)
	}
	if err := d.appendLine(filepath.Join(root, "LOG"), "second"); err != nil {
		t.Fatalf("appendLine again: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(root, "LOG"))
	if err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("LOG = %q, %v; want \"first\\nsecond\\n\"", data, err)
	}

	if err := d.rename(ctx, filepath.Join(root, "a.md"), filepath.Join(root, "c.md")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "c.md")); err != nil {
		t.Fatalf("rename left no file: %v", err)
	}

	if err := d.remove(filepath.Join(root, "c.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := d.remove(filepath.Join(root, "c.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the second remove = %v, want an fs.ErrNotExist", err)
	}
}

// Two writes inside one second must still read as a change: the row stamp
// carries nanoseconds, so a watcher comparing size and mtime rereads.
func TestDbStampsMoveWithinOneSecond(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(root, "kanban.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	at := dbNow
	f := dbFiles{root: root, st: st, now: func() time.Time { return at }}
	card := filepath.Join(root, "backlog", "KBN-1.md")

	if err := f.writeFile(ctx, card, []byte("alpha")); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	first, ok := f.stat(card)
	if !ok {
		t.Fatal("stat answered not ok after the first write")
	}
	if first.Size != int64(len("alpha")) {
		t.Errorf("size = %d, want %d", first.Size, len("alpha"))
	}

	// the same length, a fraction of a second later
	at = dbNow.Add(300 * time.Millisecond)
	if err := f.writeFile(ctx, card, []byte("gamma")); err != nil {
		t.Fatalf("the second writeFile: %v", err)
	}
	second, ok := f.stat(card)
	if !ok {
		t.Fatal("stat answered not ok after the second write")
	}
	if second.Size != int64(len("gamma")) {
		t.Errorf("size = %d, want %d", second.Size, len("gamma"))
	}
	if second.ModTime.Equal(first.ModTime) {
		t.Fatalf("two writes inside one second answer one stamp %v; the stamp must carry nanoseconds",
			first.ModTime)
	}
	if got := second.ModTime.Sub(first.ModTime); got != 300*time.Millisecond {
		t.Errorf("stamp step = %v, want 300ms", got)
	}
	// the stamp stays a plain RFC 3339 reader's to parse
	data, err := f.readFile(card)
	if err != nil || string(data) != "gamma" {
		t.Fatalf("readFile = %q, %v; want \"gamma\"", data, err)
	}
}

func TestDbFilesMapsPathsToRowsAndMeta(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(root, "kanban.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := dbFiles{root: root, st: st, now: func() time.Time { return dbNow }}
	card := filepath.Join(root, "backlog", "KBN-1.md")

	// A mkdir under the root creates nothing: rows need no directories.
	if err := f.mkdirAll(filepath.Join(root, "backlog")); err != nil {
		t.Fatalf("mkdirAll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "backlog")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat the column dir = %v, want no directory on disk", err)
	}

	// A ticket path writes a row, not a file.
	text := "---\nid: KBN-1\n---\n\nBody.\n"
	if err := f.writeFile(ctx, card, []byte(text)); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	row, err := st.Get(ctx, "KBN-1")
	if err != nil {
		t.Fatalf("get the row: %v", err)
	}
	want := store.Ticket{ID: "KBN-1", Column: "backlog", Text: text, UpdatedAt: dbNow.Format(time.RFC3339)}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("row = %+v, want %+v", row, want)
	}
	if _, err := os.Stat(card); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat the ticket path = %v, want no file on disk", err)
	}

	// A second ticket, so readDir has two names to sort.
	if err := st.Put(ctx, store.Ticket{ID: "KBN-2", Column: "backlog", Text: "two", UpdatedAt: dbNow.Format(time.RFC3339)}); err != nil {
		t.Fatalf("put KBN-2: %v", err)
	}
	if got, want := f.readDir(filepath.Join(root, "backlog")), []string{"KBN-1.md", "KBN-2.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("readDir = %v, want %v", got, want)
	}
	if got := f.readDir(filepath.Join(root, "review")); got != nil {
		t.Fatalf("readDir of an empty column = %v, want nil", got)
	}
	if got := f.readDir(filepath.Join(root, "not-a-column")); got != nil {
		t.Fatalf("readDir of a non-column dir = %v, want nil", got)
	}

	// A ticket path reads the row's text.
	data, err := f.readFile(card)
	if err != nil || string(data) != text {
		t.Fatalf("readFile = %q, %v; want the card text", data, err)
	}
	if _, err := f.readFile(filepath.Join(root, "review", "KBN-1.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("readFile of a missing ticket = %v, want an fs.ErrNotExist", err)
	}

	// stat answers the row's updated_at and its text's size.
	fileInfo, ok := f.stat(card)
	if !ok || !fileInfo.ModTime.Equal(dbNow) {
		t.Fatalf("stat = %+v, %v; want the row's stamp at %v", fileInfo, ok, dbNow)
	}
	if fileInfo.Size != int64(len(text)) {
		t.Errorf("size = %d, want %d", fileInfo.Size, len(text))
	}
	if _, ok := f.stat(filepath.Join(root, "review", "KBN-1.md")); ok {
		t.Fatal("stat of a missing ticket answered ok")
	}
	// An updated_at that does not parse is the zero time, not a miss.
	if err := st.Put(ctx, store.Ticket{ID: "KBN-2", Column: "backlog", Text: "two", UpdatedAt: "not-a-time"}); err != nil {
		t.Fatalf("put an unparsable stamp: %v", err)
	}
	if fileInfo, ok := f.stat(filepath.Join(root, "backlog", "KBN-2.md")); !ok || !fileInfo.ModTime.IsZero() {
		t.Fatalf("stat with an unparsable stamp = %+v, %v; want the zero time", fileInfo, ok)
	}

	// A rename across columns is one Move: the row's column changes, the text
	// rides along, and no directory appears for the destination.
	if err := f.rename(ctx, card, filepath.Join(root, "review", "KBN-1.md")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	row, err = st.Get(ctx, "KBN-1")
	if err != nil {
		t.Fatalf("get after the rename: %v", err)
	}
	if row.Column != "review" || row.Text != text {
		t.Fatalf("row after the rename = %+v, want it in review with its text", row)
	}
	if _, err := os.Stat(filepath.Join(root, "review")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat the destination column dir = %v, want no directory on disk", err)
	}
	// The old path goes dark: the column is part of the address.
	if _, ok := f.stat(card); ok {
		t.Fatal("stat of the old column path answered ok after the rename")
	}
	if _, err := f.readFile(card); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("readFile of the old column path = %v, want an fs.ErrNotExist", err)
	}

	// The board's own top-level files live in meta.
	if err := f.writeFile(ctx, filepath.Join(root, "LOG"), []byte("")); err != nil {
		t.Fatalf("writeFile LOG: %v", err)
	}
	if value, ok, _ := st.GetMeta(ctx, "file:LOG"); !ok || value != "" {
		t.Fatalf("file:LOG = %q, %v; want an empty row", value, ok)
	}
	if err := f.appendLine(filepath.Join(root, "LOG"), "first"); err != nil {
		t.Fatalf("appendLine: %v", err)
	}
	if err := f.appendLine(filepath.Join(root, "LOG"), "second"); err != nil {
		t.Fatalf("appendLine again: %v", err)
	}
	data, err = f.readFile(filepath.Join(root, "LOG"))
	if err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("readFile LOG = %q, %v; want \"first\\nsecond\\n\"", data, err)
	}
	if err := f.writeFile(ctx, filepath.Join(root, "STOP-LINE"), []byte("stopped")); err != nil {
		t.Fatalf("writeFile STOP-LINE: %v", err)
	}
	if value, ok, _ := st.GetMeta(ctx, "file:STOP-LINE"); !ok || value != "stopped" {
		t.Fatalf("file:STOP-LINE = %q, %v; want \"stopped\"", value, ok)
	}
	if err := f.writeFile(ctx, filepath.Join(root, "FEATURES"), []byte(`{"closed":[]}`)); err != nil {
		t.Fatalf("writeFile FEATURES: %v", err)
	}
	if value, ok, _ := st.GetMeta(ctx, "file:FEATURES"); !ok || value != `{"closed":[]}` {
		t.Fatalf("file:FEATURES = %q, %v; want the closed list", value, ok)
	}
	if data, err := f.readFile(filepath.Join(root, "FEATURES")); err != nil || string(data) != `{"closed":[]}` {
		t.Fatalf("readFile FEATURES = %q, %v; want the closed list", data, err)
	}

	// remove drops the row; a missing path wraps fs.ErrNotExist.
	if err := f.remove(filepath.Join(root, "review", "KBN-1.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := st.Get(ctx, "KBN-1"); !errors.As(err, new(*store.NotFoundError)) {
		t.Fatalf("get after the remove = %v, want a *NotFoundError", err)
	}
	if err := f.remove(filepath.Join(root, "review", "KBN-1.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the second remove = %v, want an fs.ErrNotExist", err)
	}
	if err := f.remove(filepath.Join(root, "STOP-LINE")); err != nil {
		t.Fatalf("remove STOP-LINE: %v", err)
	}
	if err := f.remove(filepath.Join(root, "STOP-LINE")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the second STOP-LINE remove = %v, want an fs.ErrNotExist", err)
	}
	if err := f.remove(filepath.Join(root, "FEATURES")); err != nil {
		t.Fatalf("remove FEATURES: %v", err)
	}
	if err := f.remove(filepath.Join(root, "FEATURES")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the second FEATURES remove = %v, want an fs.ErrNotExist", err)
	}

	// A path outside the root is the user's own file: the OS answers it.
	outside := filepath.Join(t.TempDir(), "user-files")
	if err := f.mkdirAll(filepath.Join(outside, "nested")); err != nil {
		t.Fatalf("mkdirAll outside: %v", err)
	}
	if info, err := os.Stat(filepath.Join(outside, "nested")); err != nil || !info.IsDir() {
		t.Fatalf("mkdirAll outside left no directory: %v, %v", info, err)
	}
	if err := f.writeFile(ctx, filepath.Join(outside, "nested", "body.md"), []byte("mine")); err != nil {
		t.Fatalf("writeFile outside: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "nested", "body.md")); err != nil || string(data) != "mine" {
		t.Fatalf("the file outside = %q, %v; want \"mine\"", data, err)
	}
	if data, err := f.readFile(filepath.Join(outside, "nested", "body.md")); err != nil || string(data) != "mine" {
		t.Fatalf("readFile outside = %q, %v; want \"mine\"", data, err)
	}
	if _, ok := f.stat(filepath.Join(outside, "nested", "body.md")); !ok {
		t.Fatal("stat outside answered not ok")
	}
	if got, want := f.readDir(filepath.Join(outside, "nested")), []string{"body.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("readDir outside = %v, want %v", got, want)
	}
	if err := f.rename(ctx, filepath.Join(outside, "nested", "body.md"), filepath.Join(outside, "nested", "moved.md")); err != nil {
		t.Fatalf("rename outside: %v", err)
	}
	if err := f.remove(filepath.Join(outside, "nested", "moved.md")); err != nil {
		t.Fatalf("remove outside: %v", err)
	}
	if err := f.remove(filepath.Join(outside, "nested", "body.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove of a missing outside path = %v, want an fs.ErrNotExist", err)
	}

	// Writing a ticket path with no row yet lands the row at the path's column,
	// the way a fresh file lands where new points it.
	if err := f.writeFile(ctx, filepath.Join(root, "ready", "KBN-9.md"), []byte("nine")); err != nil {
		t.Fatalf("writeFile of a new ticket: %v", err)
	}
	row, err = st.Get(ctx, "KBN-9")
	if err != nil {
		t.Fatalf("get the new ticket's row: %v", err)
	}
	if row.Column != "ready" || row.Text != "nine" {
		t.Fatalf("the new ticket's row = %+v, want it in ready", row)
	}

	// A closed store answers errors, not panics.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := f.readFile(filepath.Join(root, "backlog", "KBN-2.md")); err == nil {
		t.Fatal("readFile on a closed store passed")
	}
	if err := f.writeFile(ctx, filepath.Join(root, "backlog", "KBN-2.md"), []byte("x")); err == nil {
		t.Fatal("writeFile on a closed store passed")
	}
	if err := f.rename(ctx, filepath.Join(root, "backlog", "KBN-2.md"), filepath.Join(root, "ready", "KBN-2.md")); err == nil {
		t.Fatal("rename on a closed store passed")
	}
	if err := f.appendLine(filepath.Join(root, "LOG"), "x"); err == nil {
		t.Fatal("appendLine on a closed store passed")
	}
	if _, ok := f.stat(filepath.Join(root, "backlog", "KBN-2.md")); ok {
		t.Fatal("stat on a closed store answered ok")
	}
}
