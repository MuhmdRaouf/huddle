package hub

import (
	"context"
	"regexp"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// SessionCheckpoint is one live session's context and its two recency facts: the newest
// commit it ran and the newest handoff digest it left. A zero LastCommitAt or LastHandoffAt
// is one the window saw nothing of.
type SessionCheckpoint struct {
	SessionID      string
	ContextPercent float64 // 0-100
	LastEventAt    time.Time
	LastCommitAt   time.Time // zero: no commit seen in the window
	LastHandoffAt  time.Time // zero: no digest
}

// gitCommitRe matches the Bash command lines that run `git commit`: the verb at the line's
// start or after a `&&` or a `;`, with env assignments and git's own flags allowed before it.
// The boundary after `commit` is `(\s|$)` and not `\b`, because `\b` would take
// `git commit-tree` for a commit too.
var gitCommitRe = regexp.MustCompile(`(^|&&|;)\s*(\S+=\S+\s+)*git(\s+-\S+(\s+\S+)?)*\s+commit(\s|$)`)

// SessionCheckpoints answers every live session's checkpoint, in id order: the sessions still
// running (ended_at unset) whose context percent was ever reported. The column holds 0-100
// and is read as it lays. since bounds the commit search: a Bash run older than it never
// counts as the last commit.
func (d *DB) SessionCheckpoints(ctx context.Context, since time.Time) ([]SessionCheckpoint, error) {
	rows, err := d.st.All(ctx,
		"SELECT s.session_id, s.context_percent, s.last_event_at, digests.at AS digest_at"+
			" FROM sessions s LEFT JOIN digests ON digests.session_id = s.session_id"+
			" WHERE s.ended_at IS NULL AND s.context_percent IS NOT NULL"+
			" ORDER BY s.session_id")
	if err != nil {
		return nil, err
	}
	commits, err := d.lastCommits(ctx, since)
	if err != nil {
		return nil, err
	}
	out := make([]SessionCheckpoint, 0, len(rows))
	for _, row := range rows {
		id := textOfRow(row, "session_id")
		percent, _ := row["context_percent"].(float64)
		out = append(out, SessionCheckpoint{
			SessionID:      id,
			ContextPercent: percent,
			LastEventAt:    parseEventTime(textOfRow(row, "last_event_at")),
			LastCommitAt:   commits[id],
			LastHandoffAt:  parseEventTime(textOfRow(row, "digest_at")),
		})
	}
	return out, nil
}

// lastCommits answers each session's newest commit run at or after since: the PostToolUse
// Bash events whose command line gitCommitRe matches, keyed by session id. The events carry
// the whole report as JSON, so json_extract reads the tool and its target off it; the match
// itself is Go's, not SQLite's.
func (d *DB) lastCommits(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	rows, err := d.st.All(ctx,
		"SELECT session_id, at, json_extract(json, '$.tool_target') AS tool_target FROM events"+
			" WHERE kind = 'PostToolUse' AND at >= ? AND json_extract(json, '$.tool') = 'Bash'",
		jsscalar.ISOTime(since))
	if err != nil {
		return nil, err
	}
	commits := map[string]time.Time{}
	for _, row := range rows {
		if !gitCommitRe.MatchString(textOfRow(row, "tool_target")) {
			continue
		}
		id := textOfRow(row, "session_id")
		if at := parseEventTime(textOfRow(row, "at")); commits[id].Before(at) {
			commits[id] = at
		}
	}
	return commits, nil
}
