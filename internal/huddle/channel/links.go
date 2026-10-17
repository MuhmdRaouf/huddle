// Which Claude Code session ids are which huddle session (top-level name), per channel: learnt
// from the SessionStart join (its claude_session) and the approval hook, so radar's alerts and
// costs, which know Claude session ids only, land on huddle names.
package channel

import (
	"context"
	"regexp"
)

var claudeIDRe = regexp.MustCompile(`^[\w-]{1,128}$`)

// ensureLinks creates the session-link table once per channel.
func ensureLinks(ctx context.Context, c *Channel) error {
	c.stateMu.Lock()
	made := c.linksMade
	c.stateMu.Unlock()
	if made {
		return nil
	}
	err := c.serial(ctx, func(ctx context.Context) error {
		return c.dbRun(ctx, "CREATE TABLE IF NOT EXISTS x_session_links (claude_id TEXT PRIMARY KEY, name TEXT NOT NULL, at TEXT NOT NULL)")
	})
	if err != nil {
		return err
	}
	c.stateMu.Lock()
	c.linksMade = true
	c.stateMu.Unlock()
	return nil
}

// Link records that claudeID is the named session's Claude Code session; a subagent's link is
// its parent's. An id or name that is not fit to store is ignored, as is a write that fails.
func Link(ctx context.Context, c *Channel, name string, claudeID string) {
	if !claudeIDRe.MatchString(claudeID) || !NameRe.MatchString(name) || name == Owner {
		return
	}
	top := name // a subagent works for its session
	if parent, ok := ParentOf(name); ok {
		top = parent
	}
	if err := ensureLinks(ctx, c); err != nil {
		return
	}
	_ = c.serial(ctx, func(ctx context.Context) error {
		return c.dbRun(ctx, "INSERT INTO x_session_links (claude_id, name, at) VALUES (?,?,?) ON CONFLICT(claude_id) DO UPDATE SET name=excluded.name, at=excluded.at",
			claudeID, top, isoOfMs(c.nowMs()))
	})
}

// Links answers every Claude session id to huddle session mapping of the channel.
func Links(ctx context.Context, c *Channel) (map[string]string, error) {
	if err := ensureLinks(ctx, c); err != nil {
		return nil, err
	}
	rows, err := c.dbAll(ctx, "SELECT claude_id, name FROM x_session_links")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[rStr(r, "claude_id")] = rStr(r, "name")
	}
	return out, nil
}
