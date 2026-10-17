package board

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// readmeText is the loop init writes: the worker loop and the board page line.
const readmeText = `# Kanban board

Move tickets only with ` + "`kanban`" + `; a ticket's column is its folder.

` + "```" + `
Board: .agents/kanban; a ticket's column is its folder. Move tickets only with ` + "`kanban`" + ` (hand moves are flagged).
  kanban pull --actor <you> [--id ID --branch B --worktree W]   one ticket in progress per actor; expedite first
  ... read the whole ticket; failing test first; implement; append evidence with kanban log ...
  kanban review ID --actor <you> --note '<what to look at>'      fix/feature/test
  kanban test ID --actor <verifier> --note '<commands + exit codes>'   a different actor
  kanban done ID --actor <verifier> --note '<result>'            docs/ops/decision/chore: done directly
  kanban block ID --actor <you> --note '<cause>' --on X --until '<condition>'; unblock returns it to ready
Check ` + "`kanban epoch`" + ` before edits and commits; a broken main is ` + "`kanban stop-line`" + `.
` + "```" + `

Board page: ` + "`kanban open`" + ` answers the live board's URL; ` + "`kanban start`" + ` serves it.
`

// referenceColumns are the pre-kanban board's columns; the pool folds into
// backlog.
var referenceColumns = []string{
	"tickets", "backlog", "blocked", "ready", "in-progress", "review", "testing", "done",
}

// Init creates a board at root: the seven columns, README.md, EPOCH and LOG,
// never overwriting what is there. With from set, it migrates that reference
// board into the new one: tickets move byte for byte (the pool into backlog),
// LOG, EPOCH and STOP-LINE carry over, and the old folder is left holding only
// a README pointing at the new board.
func Init(ctx context.Context, root, from string, now time.Time) ([]string, error) {
	b := Board{Root: root, Now: func() time.Time { return now }}
	if from != "" {
		if ids := b.idCounts(""); len(ids) > 0 {
			return nil, refused("cannot migrate into %s: it already holds tickets; --from needs a fresh board (move them away first)", root)
		}
	}
	var created []string
	for _, col := range Columns {
		dir := filepath.Join(root, col)
		if _, err := os.Stat(dir); err == nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, refused("cannot create %s: %s; run init from a writable repo root or pass --board <dir>",
				dir, permissionReason(err))
		}
		created = append(created, col+"/")
	}
	if _, err := os.Stat(filepath.Join(root, "README.md")); err != nil {
		if err := b.writeCardText(ctx, filepath.Join(root, "README.md"), readmeText); err != nil {
			return nil, err
		}
		created = append(created, "README.md")
	}
	if _, err := os.Stat(b.epochPath()); err != nil {
		if err := b.writeEpoch(ctx, 1); err != nil {
			return nil, err
		}
		created = append(created, "EPOCH")
	}
	if _, err := os.Stat(b.logPath()); err != nil {
		if err := b.writeCardText(ctx, b.logPath(), ""); err != nil {
			return nil, err
		}
		created = append(created, "LOG")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	var lines []string
	if len(created) > 0 {
		lines = append(lines, fmt.Sprintf("%s: created %s", abs, strings.Join(created, ", ")))
	} else {
		lines = append(lines, fmt.Sprintf("%s: nothing to create, the layout is complete", abs))
	}
	if from != "" {
		moved, err := migrate(ctx, b, from, abs)
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("migrated %d ticket(s) from %s", moved, from))
	}
	return lines, nil
}

// InitDB creates a board that lives in one kanban.db at root: the database
// and its meta rows, with no column folders. It never overwrites what is
// there, and answers the created lines Init prints.
func InitDB(ctx context.Context, root string, now time.Time) ([]string, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, refused("cannot create %s: %s; run init from a writable repo root or pass --board <dir>",
			root, permissionReason(err))
	}
	file := filepath.Join(root, "kanban.db")
	var created []string
	if _, err := os.Stat(file); err != nil {
		created = append(created, "kanban.db")
	}
	st, err := store.Open(ctx, file)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	f := dbFiles{root: root, st: st, now: func() time.Time { return now }}
	for _, boardFile := range []struct{ name, text string }{
		{"README.md", readmeText}, {"EPOCH", "1"}, {"LOG", ""},
	} {
		path := filepath.Join(root, boardFile.name)
		if _, ok := f.stat(path); ok {
			continue
		}
		if err := f.writeFile(ctx, path, []byte(boardFile.text)); err != nil {
			return nil, err
		}
		created = append(created, boardFile.name)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	if len(created) == 0 {
		return []string{fmt.Sprintf("%s: nothing to create, the layout is complete", abs)}, nil
	}
	return []string{fmt.Sprintf("%s: created %s", abs, strings.Join(created, ", "))}, nil
}

// migrate moves a reference board's tickets into the new board and leaves the
// old folder holding only a README pointing at it.
func migrate(ctx context.Context, b Board, from, newRoot string) (int, error) {
	moved := 0
	for _, col := range referenceColumns {
		entries, err := os.ReadDir(filepath.Join(from, col))
		if err != nil {
			continue
		}
		dst := col
		if col == "tickets" {
			dst = "backlog" // the pool becomes backlog: it was only ever a holding pen
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			if err := os.MkdirAll(filepath.Join(b.Root, dst), 0o755); err != nil {
				return 0, err
			}
			if err := os.Rename(filepath.Join(from, col, e.Name()), filepath.Join(b.Root, dst, e.Name())); err != nil {
				return 0, err
			}
			moved++
		}
	}
	for _, name := range []string{"LOG", "EPOCH", "STOP-LINE"} {
		data, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			continue
		}
		if _, err := fsutil.WriteFileAtomic(ctx, filepath.Join(b.Root, name), data,
			fsutil.WriteOptions{Mode: fsutil.Perm(0o644)}); err != nil {
			return 0, err
		}
	}
	if err := b.appendLog("migrate", logStr("from", from)); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(from, 0o755); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(from)
	if err != nil {
		return moved, nil
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(from, e.Name())); err != nil {
			return 0, err
		}
	}
	pointer := fmt.Sprintf("The board moved to %s; run kanban commands against the repo root from now on.\n", newRoot)
	if _, err := fsutil.WriteFileAtomic(ctx, filepath.Join(from, "README.md"), []byte(pointer),
		fsutil.WriteOptions{Mode: fsutil.Perm(0o644)}); err != nil {
		return 0, err
	}
	return moved, nil
}
