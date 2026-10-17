package board

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// files is every file touch the board makes, addressed by the same paths the
// folder board uses, so the flow code keeps its path variables whatever the
// board reads and writes: a root holding kanban.db is a dbFiles, every other
// root a dirFiles.
type files interface {
	stat(path string) (FileInfo, bool)
	readDir(dir string) []string                                   // the ".md" file names in one column dir, sorted; nil when none
	readFile(path string) ([]byte, error)                          // a missing path wraps fs.ErrNotExist
	writeFile(ctx context.Context, path string, data []byte) error // whole, atomic
	appendLine(path, line string) error                            // LOG only
	rename(ctx context.Context, src, dst string) error
	remove(path string) error // a missing path wraps fs.ErrNotExist
	mkdirAll(dir string) error
}

// FileInfo is what a stat answers: the size and the last write a watcher
// compares to spot a change. A db row answers the text's length and its row
// stamp, so a change inside one second still moves the mtime.
type FileInfo struct {
	Size    int64
	ModTime time.Time
}

// dirFiles is the folder board: every path is a real file under the root.
type dirFiles struct{}

func (dirFiles) stat(path string) (FileInfo, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, false
	}
	return FileInfo{Size: info.Size(), ModTime: info.ModTime()}, true
}

func (dirFiles) readDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // a board without the column renders it empty, not broken
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	return names
}

func (dirFiles) readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func (dirFiles) writeFile(ctx context.Context, path string, data []byte) error {
	_, err := fsutil.WriteFileAtomic(ctx, path, data, fsutil.WriteOptions{
		Mode: fsutil.Perm(0o644),
	})
	return err
}

func (dirFiles) appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

func (dirFiles) rename(_ context.Context, src, dst string) error { return os.Rename(src, dst) }

func (dirFiles) remove(path string) error { return os.Remove(path) }

func (dirFiles) mkdirAll(dir string) error { return os.MkdirAll(dir, 0o755) }

// dbFiles is the kanban.db board: a ticket path reads and writes one row, the
// board's own top-level files live in meta rows, and anything outside the root
// stays a real file — it is the user's own, as new's --body-file is.
type dbFiles struct {
	root string
	st   *store.Store
	now  func() time.Time
}

func (f dbFiles) stat(path string) (FileInfo, bool) {
	rel := relUnder(f.root, path)
	if rel == "" {
		return dirFiles{}.stat(path)
	}
	if col, id, ok := ticketOf(rel); ok {
		row, err := f.st.Get(context.Background(), id)
		if err != nil || row.Column != col {
			// the column is part of the address: a row in another column does
			// not answer this path, the way no file answers an empty folder
			return FileInfo{}, false
		}
		return FileInfo{Size: int64(len(row.Text)), ModTime: parseStamp(row.UpdatedAt)}, true
	}
	if key := metaKey(rel); key != "" {
		value, ok, err := f.st.GetMeta(context.Background(), key)
		if err != nil || !ok {
			return FileInfo{}, false
		}
		// a meta row keeps no clock; its size still tells a rewrite apart
		return FileInfo{Size: int64(len(value))}, true
	}
	return dirFiles{}.stat(path)
}

func (f dbFiles) readDir(dir string) []string {
	rel := relUnder(f.root, dir)
	if rel == "" {
		return dirFiles{}.readDir(dir)
	}
	if !validColumn(rel) {
		return nil
	}
	rows, err := f.st.Column(context.Background(), rel)
	if err != nil {
		return nil // a board without the column renders it empty, not broken
	}
	var names []string
	for _, row := range rows {
		names = append(names, row.ID+".md")
	}
	sort.Strings(names) // the folder board reads the directory's name order
	return names
}

func (f dbFiles) readFile(path string) ([]byte, error) {
	rel := relUnder(f.root, path)
	if rel == "" {
		return dirFiles{}.readFile(path)
	}
	if col, id, ok := ticketOf(rel); ok {
		row, err := f.st.Get(context.Background(), id)
		if err != nil {
			return nil, asMissing(err)
		}
		if row.Column != col {
			return nil, asMissing(&store.NotFoundError{ID: id})
		}
		return []byte(row.Text), nil
	}
	if key := metaKey(rel); key != "" {
		value, ok, err := f.st.GetMeta(context.Background(), key)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, asMissing(&store.NotFoundError{ID: key})
		}
		return []byte(value), nil
	}
	return dirFiles{}.readFile(path)
}

// writeFile lands the row at the path's column, the way a file lands where the
// path says — a row already there is replaced — or one meta row whole.
func (f dbFiles) writeFile(ctx context.Context, path string, data []byte) error {
	rel := relUnder(f.root, path)
	if rel == "" {
		return dirFiles{}.writeFile(ctx, path, data)
	}
	if col, id, ok := ticketOf(rel); ok {
		return f.st.Put(ctx, store.Ticket{
			ID: id, Column: col, Text: string(data), UpdatedAt: f.stamp(),
		})
	}
	if key := metaKey(rel); key != "" {
		return f.st.SetMeta(ctx, key, string(data))
	}
	return dirFiles{}.writeFile(ctx, path, data)
}

// appendLine adds the line to one meta row in one transaction: the db board's
// audit trail is a value holding whole lines, the way the LOG file reads.
func (f dbFiles) appendLine(path, line string) error {
	key := metaKey(relUnder(f.root, path))
	if key == "" {
		return dirFiles{}.appendLine(path, line)
	}
	return f.st.Tx(context.Background(), func(tx *store.Store) error {
		value, _, err := tx.GetMeta(context.Background(), key)
		if err != nil {
			return err
		}
		return tx.SetMeta(context.Background(), key, value+line+"\n")
	})
}

// rename moves one ticket row to its new column with one Move; paths that do
// not name two tickets stay a file rename.
func (f dbFiles) rename(ctx context.Context, src, dst string) error {
	srcCol, srcID, srcOK := ticketOf(relUnder(f.root, src))
	dstCol, _, dstOK := ticketOf(relUnder(f.root, dst))
	if !srcOK || !dstOK {
		return dirFiles{}.rename(ctx, src, dst)
	}
	row, err := f.st.Get(ctx, srcID)
	if err != nil {
		return asMissing(err)
	}
	if row.Column != srcCol {
		return asMissing(&store.NotFoundError{ID: srcID})
	}
	return f.st.Move(ctx, srcID, dstCol, row.Text, f.stamp())
}

func (f dbFiles) remove(path string) error {
	rel := relUnder(f.root, path)
	if rel == "" {
		return dirFiles{}.remove(path)
	}
	if _, id, ok := ticketOf(rel); ok {
		return asMissing(f.st.Delete(context.Background(), id))
	}
	if key := metaKey(rel); key != "" {
		return asMissing(f.st.DeleteMeta(context.Background(), key))
	}
	return dirFiles{}.remove(path)
}

// mkdirAll under the root is a no-op: rows need no directories.
func (f dbFiles) mkdirAll(dir string) error {
	if relUnder(f.root, dir) == "" {
		return dirFiles{}.mkdirAll(dir)
	}
	return nil
}

// stamp answers the row timestamp: the board's clock in RFC 3339 UTC with
// nanoseconds, so two writes inside one second never answer one stamp.
func (f dbFiles) stamp() string { return f.now().UTC().Format(time.RFC3339Nano) }

// relUnder answers path relative to root when path sits under root, else "".
func relUnder(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return rel
}

// ticketOf splits a root-relative path into a column and an id when it names a
// ticket file of one of the board's columns.
func ticketOf(rel string) (col, id string, ok bool) {
	dir, base := filepath.Split(rel)
	col = strings.TrimSuffix(dir, string(filepath.Separator))
	if !validColumn(col) || !strings.HasSuffix(base, ".md") {
		return "", "", false
	}
	return col, strings.TrimSuffix(base, ".md"), true
}

// metaKey answers the meta row a root-relative path maps to, "" when the path
// is not one of the board's own top-level files.
func metaKey(rel string) string {
	switch rel {
	case "LOG", "EPOCH", "STOP-LINE", "README.md":
		return "file:" + rel
	}
	return ""
}

// parseStamp answers the row's timestamp, RFC 3339 with or without fractional
// seconds; one that does not parse is the zero time.
func parseStamp(stamp string) time.Time {
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}
	}
	return at
}

// asMissing wraps the store's not-found answer so a missing path reads as
// fs.ErrNotExist, the way the folder board's errors read.
func asMissing(err error) error {
	var missing *store.NotFoundError
	if errors.As(err, &missing) {
		return missingPath{missing: missing}
	}
	return err
}

// missingPath is the store's not-found answer carrying fs.ErrNotExist with it.
type missingPath struct{ missing *store.NotFoundError }

func (e missingPath) Error() string { return e.missing.Error() }

func (e missingPath) Unwrap() []error { return []error{fs.ErrNotExist, e.missing} }
