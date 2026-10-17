package board

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// boardFileNames are the board's own top-level files: a migration carries the
// ones present into meta rows, and an export writes the ones present back out.
var boardFileNames = []string{"README.md", "LOG", "EPOCH", "STOP-LINE"}

// MigrateDB converts the resolved folder board in place: every ticket file
// becomes a row carrying its whole text and its file mtime, and the board's
// own files become meta rows, all in one store transaction. After the commit
// the column folders and those files move into a kept archive directory; any
// failure before it leaves no kanban.db behind.
func MigrateDB(ctx context.Context, b Board) ([]string, error) {
	file := filepath.Join(b.Root, "kanban.db")
	if _, err := os.Stat(file); err == nil {
		return nil, refused("%s already holds kanban.db", b.Root)
	}
	f := b.fs()
	type ticketRow struct{ col, id, text, at string }
	var rows []ticketRow
	for _, col := range Columns {
		for _, name := range f.readDir(filepath.Join(b.Root, col)) {
			path := filepath.Join(b.Root, col, name)
			text, err := b.readCardText(path, strings.TrimSuffix(name, ".md"))
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			rows = append(rows, ticketRow{
				col: col, id: strings.TrimSuffix(name, ".md"), text: text,
				at: info.ModTime().UTC().Format(time.RFC3339Nano),
			})
		}
	}
	type metaRow struct{ key, text string }
	var metas []metaRow
	for _, name := range boardFileNames {
		path := filepath.Join(b.Root, name)
		if _, err := os.Stat(path); err != nil {
			continue // those present
		}
		data, err := f.readFile(path)
		if err != nil {
			return nil, err
		}
		metas = append(metas, metaRow{key: metaKey(name), text: string(data)})
	}

	st, err := store.Open(ctx, file)
	if err != nil {
		return nil, err
	}
	err = st.Tx(ctx, func(tx *store.Store) error {
		for _, row := range rows {
			if err := tx.Put(ctx, store.Ticket{
				ID: row.id, Column: row.col, Text: row.text, UpdatedAt: row.at,
			}); err != nil {
				return err
			}
		}
		for _, meta := range metas {
			if err := tx.SetMeta(ctx, meta.key, meta.text); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// the board stays a folder board: the database the failed run opened is
		// removed whole
		_ = st.Close()
		for _, leftover := range []string{file, file + "-wal", file + "-shm"} {
			_ = os.Remove(leftover)
		}
		return nil, err
	}
	if err := st.Close(); err != nil {
		return nil, err
	}

	archive := filepath.Join(b.Root, ".folders-"+b.now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(archive, 0o755); err != nil {
		return nil, err
	}
	keep := func(name string) error {
		err := os.Rename(filepath.Join(b.Root, name), filepath.Join(archive, name))
		if errors.Is(err, fs.ErrNotExist) {
			return nil // those present
		}
		return err
	}
	for _, col := range Columns {
		if err := keep(col); err != nil {
			return nil, err
		}
	}
	for _, name := range boardFileNames {
		if err := keep(name); err != nil {
			return nil, err
		}
	}
	return []string{
		fmt.Sprintf("migrated %d tickets into %s; the folders are kept in %s", len(rows), file, archive),
	}, nil
}

// ExportMarkdown writes the board back out as Markdown folders: one file per
// ticket under its column, plus the board's own files, read through the
// board's seam from a db board or a folder board alike. The directory must
// not exist, or must be empty.
func ExportMarkdown(ctx context.Context, b Board, dir string) ([]string, error) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return nil, refused("cannot export into %s: it is not empty; export needs a fresh or empty directory", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, refused("cannot create %s: %s", dir, permissionReason(err))
	}
	count := 0
	for _, col := range Columns {
		entries := b.ColumnEntries(col)
		if len(entries) == 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Join(dir, col), 0o755); err != nil {
			return nil, err
		}
		for _, entry := range entries {
			data, err := b.ReadPath(entry.Path)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(dir, col, entry.Name), data, 0o644); err != nil {
				return nil, err
			}
			count++
		}
	}
	for _, name := range boardFileNames {
		data, err := b.ReadPath(filepath.Join(b.Root, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // those present
			}
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return nil, err
		}
	}
	return []string{fmt.Sprintf("exported %d tickets to %s", count, dir)}, nil
}
