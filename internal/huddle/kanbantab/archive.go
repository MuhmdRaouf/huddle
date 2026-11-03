package kanbantab

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// archiveDue archives the mounted board a state read names once its cards have
// all been done for 24 hours. The decision comes from the snapshot the board's
// app already holds, so a read with nothing due touches no card file; only a
// due board reaches the write, which re-checks under the hold the flow commands
// keep. The read starts no loop, and a failed archive leaves the board open for
// the next read.
func (t *Tab) archiveDue(ctx context.Context, key string) {
	t.mu.Lock()
	e, ok := t.apps[key]
	t.mu.Unlock()
	if !ok || len(board.DueToArchive(e.app.Snapshot().Cards, t.now())) == 0 {
		return
	}
	t.flowMu.Lock()
	defer t.flowMu.Unlock()
	_, _ = t.archive(ctx, e.board)
}
