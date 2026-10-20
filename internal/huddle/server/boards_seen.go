// The boards the hub remembers. The kanban tab serves the boards of the
// sessions the registry knows; this store keeps every root those sessions
// resolved to, so a board stays listed after its last live session ends — for
// thirty days from the last sighting, and never past a directory that is gone.
package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// boardsSeenName is the home file holding the remembered boards.
const boardsSeenName = "kanban-boards.json"

// boardSeenWindow is how long a board stays listed past its last sighting.
const boardSeenWindow = 30 * 24 * time.Hour

// boardSeenWriteGap is the smallest gap between two sightings written for one root: a busy board
// never rewrites the file on every request.
const boardSeenWriteGap = time.Minute

// boardsSeenDoc is the file's shape.
type boardsSeenDoc struct {
	Boards []boardsSeenRow `json:"boards"`
}

// boardsSeenRow is one board's root and the moment it was last seen live, unix milliseconds.
type boardsSeenRow struct {
	Root string `json:"root"`
	Seen int64  `json:"seen"`
}

// boardsSeen is the in-memory half of the store: the per-root write throttle.
type boardsSeen struct {
	mu     sync.Mutex
	writes map[string]time.Time
}

// record stamps the roots as seen at now, reading the file once and writing it once when
// anything is due. A write the home refuses stamps nothing, so the next request tries again.
func (b *boardsSeen) record(ctx context.Context, homeRoot string, roots []string, now time.Time) {
	if homeRoot == "" || len(roots) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var due []string
	for _, root := range roots {
		if last, ok := b.writes[root]; ok && now.Sub(last) < boardSeenWriteGap {
			continue
		}
		due = append(due, root)
	}
	if len(due) == 0 {
		return
	}
	file := filepath.Join(homeRoot, boardsSeenName)
	doc, _ := fsutil.ReadJSONFile[boardsSeenDoc](ctx, file)
	if doc.Boards == nil {
		doc.Boards = []boardsSeenRow{}
	}
	index := map[string]int{}
	for i, row := range doc.Boards {
		index[row.Root] = i
	}
	stamp := now.UnixMilli()
	for _, root := range due {
		if i, ok := index[root]; ok {
			doc.Boards[i].Seen = stamp
			continue
		}
		index[root] = len(doc.Boards)
		doc.Boards = append(doc.Boards, boardsSeenRow{Root: root, Seen: stamp})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return
	}
	if _, err := fsutil.WriteFileAtomic(ctx, file, append(raw, '\n'),
		fsutil.WriteOptions{Mkdir: true}); err != nil {
		return
	}
	if b.writes == nil {
		b.writes = map[string]time.Time{}
	}
	for _, root := range due {
		b.writes[root] = now
	}
}

// noNamedEnv answers nothing, so the huddle process's own KANBAN_BOARD never picks a board for a
// session's working dir.
func noNamedEnv(string) (string, bool) { return "", false }

// seenBoards refreshes the remembered boards the live cwds resolve to and answers the dirs the
// tab looks for boards from: the live cwds first, then every remembered root seen inside the
// window whose directory still exists, deduped by root — a live cwd's board is never added again.
func (s *server) seenBoards(ctx context.Context, cwds []string) []string {
	now := s.now()
	live := map[string]bool{}
	var roots []string
	for _, cwd := range cwds {
		b, err := board.Resolve("", noNamedEnv, cwd)
		if err != nil {
			continue // a dir with no board has nothing to remember
		}
		root := b.Root
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		if !live[root] {
			live[root] = true
			roots = append(roots, root)
		}
	}
	s.boardSeen.record(ctx, s.o.Home, roots, now)
	file := filepath.Join(s.o.Home, boardsSeenName)
	doc, ok := fsutil.ReadJSONFile[boardsSeenDoc](ctx, file)
	if !ok {
		return cwds
	}
	windowStart := now.Add(-boardSeenWindow)
	out := cwds
	for _, row := range doc.Boards {
		if live[row.Root] {
			continue
		}
		if time.UnixMilli(row.Seen).Before(windowStart) {
			continue
		}
		if _, err := os.Stat(row.Root); err != nil {
			continue // a directory that is gone names no board
		}
		out = append(out, row.Root)
		live[row.Root] = true
	}
	return out
}
