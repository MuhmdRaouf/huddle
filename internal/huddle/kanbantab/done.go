package kanbantab

import (
	"context"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// DoneBetween counts the cards every board the tab knows moved to done, from <= ts < to. Each
// board opens the way the cli opens it, so a kanban.db board's LOG is read from its rows; a
// board that will not open counts nothing.
func (t *Tab) DoneBetween(ctx context.Context, from, to time.Time) int {
	count := 0
	for _, row := range t.boardsOf(ctx) {
		b, closeBoard, err := board.Open(ctx, board.Board{Root: row.Root, Now: t.now})
		if err != nil {
			continue
		}
		count += b.DoneBetween(from, to)
		_ = closeBoard()
	}
	return count
}
