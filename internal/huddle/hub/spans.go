package hub

import (
	"context"
	"time"
)

// SessionSpan is one session's life in the registry: from the moment it entered the registry to
// the moment it ended. A zero End is a session not yet ended.
type SessionSpan struct {
	SessionID string
	Start     time.Time
	End       time.Time
}

// SessionSpans answers every registered session's span, in id order, from the start and end the
// session row keeps, so a span outlives the raw events. A session without a stored start has no
// span.
func (d *DB) SessionSpans(ctx context.Context) ([]SessionSpan, error) {
	rows, err := d.st.All(ctx,
		"SELECT session_id, started_at, ended_at FROM sessions WHERE started_at IS NOT NULL ORDER BY session_id")
	if err != nil {
		return nil, err
	}
	out := make([]SessionSpan, 0, len(rows))
	for _, row := range rows {
		out = append(out, SessionSpan{
			SessionID: textOfRow(row, "session_id"),
			Start:     parseEventTime(textOfRow(row, "started_at")),
			End:       parseEventTime(textOfRow(row, "ended_at")),
		})
	}
	return out, nil
}
