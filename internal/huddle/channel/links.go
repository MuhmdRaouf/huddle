// Which Claude Code session ids are which huddle session (top-level name), per channel: learnt
// from the SessionStart join (its claude_session) and the approval hook onto the member's own
// row, so radar's alerts and costs, which know Claude session ids only, land on huddle names.
package channel

import (
	"context"
	"maps"
	"regexp"
)

var claudeIDRe = regexp.MustCompile(`^[\w-]{1,128}$`)

// Link records that claudeID is the named session's Claude Code session; a subagent's link is
// its parent's. An id or name that is not fit to store is ignored, as is a member without a row
// or a write that fails.
func Link(ctx context.Context, c *Channel, name string, claudeID string) {
	if !claudeIDRe.MatchString(claudeID) || !NameRe.MatchString(name) || name == Owner {
		return
	}
	top := name // a subagent works for its session
	if parent, ok := ParentOf(name); ok {
		top = parent
	}
	_ = c.serial(ctx, func(ctx context.Context) error {
		return c.dbRun(ctx, "UPDATE sessions SET session_id=? WHERE name=?", claudeID, top)
	})
}

// Links answers every Claude session id to huddle session mapping of the channel: the member
// rows' own ids over the registry's answer, when one is wired — a row's own id wins.
func Links(ctx context.Context, c *Channel) (map[string]string, error) {
	rows, err := c.dbAll(ctx, "SELECT session_id, name FROM sessions WHERE session_id IS NOT NULL AND session_id != ''")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if c.MemberSessions != nil {
		if reg, ok := c.MemberSessions.MemberSessions(ctx, c.Name); ok {
			maps.Copy(out, reg)
		}
	}
	for _, r := range rows {
		out[rStr(r, "session_id")] = rStr(r, "name")
	}
	return out, nil
}
