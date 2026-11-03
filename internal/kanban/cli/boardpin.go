package cli

// The board pin: a session names the board it works on once, and new and list default to it.

import (
	"context"
	"fmt"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// sessionOf answers the id of the session running the command: HUDDLE_SESSION, else
// CLAUDE_CODE_SESSION_ID, "" outside a session.
func sessionOf(w World) string {
	for _, key := range []string{"HUDDLE_SESSION", "CLAUDE_CODE_SESSION_ID"} {
		if v, ok := w.Getenv(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// pinnedBoard answers the board the running session is pinned to, "" for none.
func pinnedBoard(ctx context.Context, w World) string {
	session := sessionOf(w)
	if session == "" || w.PinGet == nil {
		return ""
	}
	return w.PinGet(ctx, session)
}

// onBoard keeps the cards of one board; no board keeps them all.
func onBoard(cards []board.Card, name string) []board.Card {
	if name == "" {
		return cards
	}
	var kept []board.Card
	for _, c := range cards {
		if c.Cluster == name {
			kept = append(kept, c)
		}
	}
	return kept
}

// runBoardUse pins the running session to a board of the store.
func runBoardUse(ctx context.Context, w World, b board.Board, _ string, args []string) ([]string, error) {
	session := sessionOf(w)
	if session == "" {
		return nil, &board.Refused{Message: "no session id here (HUDDLE_SESSION and CLAUDE_CODE_SESSION_ID are empty); board use pins a Claude Code session"}
	}
	name, err := b.ResolveBoard(args[0])
	if err != nil {
		return nil, err
	}
	if w.PinSet == nil {
		return nil, &board.Refused{Message: "this run keeps no session pins"}
	}
	if err := w.PinSet(ctx, session, name); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("session pinned to board %s", name)}, nil
}

// runBoardSuggest names the board the branch of the working directory names, else the board
// named like its work tree's directory.
func runBoardSuggest(_ context.Context, w World, b board.Board, _ string, _ []string) ([]string, error) {
	var names []string
	for _, e := range b.Boards() {
		names = append(names, e.Name)
	}
	if name, ok := board.SuggestBoardAt(w.Cwd(), names); ok {
		return []string{name}, nil
	}
	branch := board.WorkingBranch(w.Cwd())
	if branch == "" {
		branch = "(none)"
	}
	return nil, &board.Refused{Message: fmt.Sprintf(
		"no board matches branch %s or this work tree's directory; create one with kanban board create NAME", branch)}
}
