package hub

import (
	"context"
	"strings"
)

// EndedAmong answers which of ids name a session the registry has ended; an id the registry
// never saw is not in the answer.
func (d *DB) EndedAmong(ctx context.Context, ids []string) (map[string]bool, error) {
	ended := make(map[string]bool, len(ids))
	if len(ids) == 0 {
		return ended, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.st.All(ctx,
		"SELECT session_id FROM sessions WHERE ended_at IS NOT NULL AND session_id IN ("+
			strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		ended[textOfRow(row, "session_id")] = true
	}
	return ended, nil
}
