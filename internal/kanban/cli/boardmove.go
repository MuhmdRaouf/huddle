package cli

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// runBoardMove moves a card to another board of the store: board move ID BOARD.
func runBoardMove(ctx context.Context, _ World, b board.Board, actor string, args []string) ([]string, error) {
	return b.MoveToBoard(ctx, board.Args{Actor: actor, ID: args[0], Board: args[1]})
}
