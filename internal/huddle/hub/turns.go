package hub

import (
	"context"
	"slices"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// editTools are the tools whose run changes a file: the PostToolUse events an open turn counts
// as edits. The four stand together for "touched a file", so they keep one name.
var editTools = map[string]bool{
	"Edit":         true,
	"Write":        true,
	"MultiEdit":    true,
	"NotebookEdit": true,
}

// OpenTurn is one live session's turn still running: when its prompt came, how many distinct
// files it edited since, and its newest commit since. A zero LastCommitAt is one the window
// saw nothing of.
type OpenTurn struct {
	SessionID    string
	StartedAt    time.Time
	Edits        int       // distinct files the turn's edit tools touched
	LastCommitAt time.Time // zero: no commit since the turn started
}

// OpenTurns answers every live session's open turn, in session id order: for each session
// still running (ended_at unset), the turn its newest UserPromptSubmit at or after since
// started. A session with no prompt in the window has no open turn, and a Stop of the session
// at or after the start closed the one it had. Edits and commits count only after the start.
func (d *DB) OpenTurns(ctx context.Context, since time.Time) ([]OpenTurn, error) {
	// one query per kind, filtered in Go — never one per session. The events carry the whole
	// report, so json_extract reads the tool and its target off it the way lastCommits does,
	// and the commit match is Go's, not SQLite's.
	live := map[string]bool{}
	rows, err := d.st.All(ctx, "SELECT session_id FROM sessions WHERE ended_at IS NULL")
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		live[textOfRow(row, "session_id")] = true
	}
	starts := map[string]time.Time{}
	rows, err = d.st.All(ctx,
		"SELECT session_id, at FROM events WHERE kind = 'UserPromptSubmit' AND at >= ?",
		jsscalar.ISOTime(since))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id := textOfRow(row, "session_id")
		if !live[id] {
			continue
		}
		if at := parseEventTime(textOfRow(row, "at")); at.After(starts[id]) {
			starts[id] = at
		}
	}
	closed := map[string]bool{}
	rows, err = d.st.All(ctx,
		"SELECT session_id, at FROM events WHERE kind = 'Stop' AND at >= ?", jsscalar.ISOTime(since))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id := textOfRow(row, "session_id")
		if at := parseEventTime(textOfRow(row, "at")); !at.Before(starts[id]) {
			closed[id] = true
		}
	}
	edits := map[string]map[string]bool{}
	commits := map[string]time.Time{}
	rows, err = d.st.All(ctx,
		"SELECT session_id, at,"+
			" json_extract(json, '$.tool') AS tool, json_extract(json, '$.tool_target') AS tool_target"+
			" FROM events WHERE kind = 'PostToolUse' AND at >= ?", jsscalar.ISOTime(since))
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		id := textOfRow(row, "session_id")
		start, open := starts[id]
		if !open || closed[id] {
			continue
		}
		at := parseEventTime(textOfRow(row, "at"))
		if at.Before(start) {
			continue
		}
		tool := textOfRow(row, "tool")
		target := textOfRow(row, "tool_target")
		switch {
		case editTools[tool] && target != "":
			if edits[id] == nil {
				edits[id] = map[string]bool{}
			}
			edits[id][target] = true
		case tool == "Bash" && gitCommitRe.MatchString(target):
			if commits[id].Before(at) {
				commits[id] = at
			}
		}
	}
	ids := make([]string, 0, len(starts))
	for id := range starts {
		if !closed[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	out := make([]OpenTurn, 0, len(ids))
	for _, id := range ids {
		out = append(out, OpenTurn{
			SessionID:    id,
			StartedAt:    starts[id],
			Edits:        len(edits[id]),
			LastCommitAt: commits[id],
		})
	}
	return out, nil
}
