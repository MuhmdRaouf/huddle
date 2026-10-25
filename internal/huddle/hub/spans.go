package hub

import (
	"context"
	"time"
)

// SessionSpan is one session's life in the registry: from its first reported event to the
// moment it ended. A zero End is a session not yet ended.
type SessionSpan struct {
	SessionID string
	Start     time.Time
	End       time.Time
}

// SessionSpans answers every registered session's span, in id order. The first event is the
// earliest stored at: reports carry UTC RFC 3339 stamps, which sort as text.
func (d *DB) SessionSpans(ctx context.Context) ([]SessionSpan, error) {
	rows, err := d.st.All(ctx,
		"SELECT s.session_id, MIN(e.at) AS started_at, s.ended_at FROM sessions s "+
			"JOIN events e ON e.session_id = s.session_id GROUP BY s.session_id ORDER BY s.session_id")
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
