package board

import (
	"context"
	"fmt"
)

// MoveToBoard changes a card's board (its cluster) to another registered, open board. The
// card keeps its column, holder, owner session and log; one line joins the card's log and
// one the board LOG.
func (b Board) MoveToBoard(ctx context.Context, a Args) ([]string, error) {
	return b.atomic(ctx, func(b Board) ([]string, error) { return b.moveToBoard(ctx, a) })
}

func (b Board) moveToBoard(ctx context.Context, a Args) ([]string, error) {
	if err := actorRefusal(a.Actor); err != nil {
		return nil, err
	}
	if r := scanArgs(false, [2]string{"<ID>", a.ID}, [2]string{"<BOARD>", a.Board}); r != nil {
		return nil, r
	}
	src, col, ok := b.Find(a.ID)
	if !ok {
		return nil, refused("%s: not on the board; run list to see ids, or new to create it", a.ID)
	}
	t, _ := b.loadCard(col, a.ID)
	if err := b.closedRefusal(t); err != nil {
		return nil, err
	}
	to, err := b.ResolveBoard(a.Board)
	if err != nil {
		return nil, err
	}
	if to == t.Cluster {
		return nil, refused("%s is already on board %s", a.ID, to)
	}
	if b.closedSet()[to] {
		return nil, refused("board %s is archived; reopen it with kanban feature reopen %s", to, to)
	}
	text, err := b.readFMText(src, a.ID)
	if err != nil {
		return nil, err
	}
	line := fmt.Sprintf("moved to board %s from %s", to, t.Cluster)
	text = appendLogEntry(setFieldPanicFree(clearHand(text), "cluster", to), b.logLine(a.Actor, line))
	if err := b.writeCardText(ctx, src, text); err != nil {
		return nil, err
	}
	if err := b.appendLog("board-move", logStr("ticket", a.ID), logStr("actor", a.Actor),
		logStr("from", t.Cluster), logStr("to", to)); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("%s %s", a.ID, line)}, nil
}
