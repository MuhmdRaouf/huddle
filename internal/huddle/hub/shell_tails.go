// The shell_tails table: the last lines of a background shell that has ended, copied from its
// output file before the OS clears the file. A tail is taken once per shell and read back only
// by the output route, behind the same credential as the live output.
package hub

import (
	"context"
	"strings"
	"time"
)

// ShellTailTTL is how long the registry keeps a shell's tail: 90 days.
const ShellTailTTL = 90 * 24 * time.Hour

// SaveShellTail keeps text as the tail of one agent's shell, saved at at; a shell that already
// has a tail keeps it.
func (d *DB) SaveShellTail(ctx context.Context, session, agent, shell, text, at string) error {
	return d.st.Run(ctx, `INSERT INTO shell_tails (session_id, agent, shell_id, tail, lines, saved_at)
VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(session_id, agent, shell_id) DO NOTHING`,
		session, agent, shell, text, strings.Count(text, "\n"), at)
}

// ShellTail answers the tail kept for one agent's shell; ok is false when none is kept.
func (d *DB) ShellTail(ctx context.Context, session, agent, shell string) (text string, ok bool, err error) {
	row, err := d.st.Get(ctx, "SELECT tail FROM shell_tails WHERE session_id = ? AND agent = ? AND shell_id = ?",
		session, agent, shell)
	if err != nil || row == nil {
		return "", false, err
	}
	return textOfRow(row, "tail"), true, nil
}

// ShellTailIDs answers the ids of the shells of one agent that have a tail kept, without
// reading the tails.
func (d *DB) ShellTailIDs(ctx context.Context, session, agent string) (map[string]bool, error) {
	rows, err := d.st.All(ctx, "SELECT shell_id FROM shell_tails WHERE session_id = ? AND agent = ?", session, agent)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(rows))
	for _, row := range rows {
		ids[textOfRow(row, "shell_id")] = true
	}
	return ids, nil
}
