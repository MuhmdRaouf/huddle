package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// boardVerb is one verb of the board word: how many positionals follow it and
// what it runs. A verb in its own file adds its row to boardVerbs.
type boardVerb struct {
	args int
	run  func(ctx context.Context, w World, b board.Board, actor string, args []string) ([]string, error)
}

// boardVerbs is the board word's verb table.
var boardVerbs = map[string]boardVerb{
	"create": {args: 1, run: func(ctx context.Context, _ World, b board.Board, actor string, args []string) ([]string, error) {
		return b.CreateBoard(ctx, board.Args{Actor: actor, ID: args[0]})
	}},
	"move":    {args: 2, run: runBoardMove},
	"use":     {args: 1, run: runBoardUse},
	"suggest": {args: 0, run: runBoardSuggest},
}

// boardVerbList answers the verbs sorted, joined for a usage line.
func boardVerbList() string {
	verbs := make([]string, 0, len(boardVerbs))
	for verb := range boardVerbs {
		verbs = append(verbs, verb)
	}
	sort.Strings(verbs)
	return strings.Join(verbs, ", ")
}

// boardUsage answers the usage problem of a board invocation, "" when its verb
// is in the table and its positionals match the verb.
func boardUsage(positional []string) string {
	verb, ok := boardVerbs[positional[0]]
	if !ok {
		return positional[0] + " is not a verb; use one of " + boardVerbList()
	}
	if got := len(positional) - 1; got < verb.args {
		return fmt.Sprintf("%s needs %d argument(s) after it", positional[0], verb.args)
	} else if got > verb.args {
		return "unexpected argument " + positional[verb.args+1]
	}
	return ""
}

// runBoard runs the verb boardUsage accepted.
func runBoard(ctx context.Context, w World, b board.Board, actor string, positional []string) ([]string, error) {
	return boardVerbs[positional[0]].run(ctx, w, b, actor, positional[1:])
}
