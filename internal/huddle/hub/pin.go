package hub

import "context"

// SetBoard pins the session to a kanban board by name; an empty name clears the pin. It answers
// false when no session is registered under the id.
func (d *DB) SetBoard(ctx context.Context, id, board string) (bool, error) {
	row, err := d.st.Get(ctx, "SELECT session_id FROM sessions WHERE session_id = ?", id)
	if err != nil || row == nil {
		return false, err
	}
	return true, d.st.Run(ctx, "UPDATE sessions SET board = ? WHERE session_id = ?", board, id)
}
