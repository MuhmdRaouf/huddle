package hub

import (
	"context"
	"strings"
)

// MemberSessions answers every session registered in channel that carries a member: session id
// to member, ended sessions included, a subagent folded onto the top-level member it works for.
func (d *DB) MemberSessions(ctx context.Context, channel string) (map[string]string, error) {
	rows, err := d.st.All(ctx,
		"SELECT session_id, member FROM sessions WHERE channel = ? AND member != ''", channel)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		member := textOfRow(row, "member")
		// a subagent works for its top-level member, the fold the channel's ParentOf keeps
		if i := strings.IndexByte(member, '.'); i >= 0 {
			member = member[:i]
		}
		out[textOfRow(row, "session_id")] = member
	}
	return out, nil
}
