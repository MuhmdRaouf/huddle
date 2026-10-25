package hub

import "context"

// SessionPid is one open session's process, as its reports named it.
type SessionPid struct {
	SessionID string
	Pid       int
}

// LivePids answers every not-yet-ended session that reported a pid, in id order.
func (d *DB) LivePids(ctx context.Context) ([]SessionPid, error) {
	rows, err := d.st.All(ctx,
		"SELECT session_id, pid FROM sessions WHERE ended_at IS NULL AND pid > 0 ORDER BY session_id")
	if err != nil {
		return nil, err
	}
	out := make([]SessionPid, 0, len(rows))
	for _, row := range rows {
		pid, _ := row["pid"].(int64)
		out = append(out, SessionPid{SessionID: textOfRow(row, "session_id"), Pid: int(pid)})
	}
	return out, nil
}
