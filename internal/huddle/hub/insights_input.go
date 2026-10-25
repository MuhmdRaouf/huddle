package hub

// The registry's own history as the insights route reads it: every needs-you item in a window,
// each Bash permission ask carrying the command its input holds.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// ItemRecord is one needs-you item's row as a read-only reader sees it.
type ItemRecord struct {
	SessionID string
	Kind      string // KindPermission, KindQuestion, KindTurn or KindBlocked
	Tool      string // a permission ask's tool; "" for the other kinds
	Command   string // a Bash permission ask's command; "" for every other row
	Decision  string // a permission ask's answer as stored, "allow" or "deny"; "" otherwise
	CreatedAt time.Time
	DecidedAt time.Time // zero while the item still waits for its answer
}

// ItemHistory answers every item created at or after since, oldest first. Read-only: readers
// of the history never touch the rows the needs routes own.
func (d *DB) ItemHistory(ctx context.Context, since time.Time) ([]ItemRecord, error) {
	rows, err := d.st.All(ctx,
		"SELECT session_id, kind, tool, input, decision, created_at, decided_at FROM items"+
			" WHERE created_at >= ? ORDER BY created_at, id", jsfmt.ISOTime(since))
	if err != nil {
		return nil, err
	}
	records := make([]ItemRecord, 0, len(rows))
	for _, row := range rows {
		tool := textOfRow(row, "tool")
		records = append(records, ItemRecord{
			SessionID: textOfRow(row, "session_id"),
			Kind:      textOfRow(row, "kind"),
			Tool:      tool,
			Command:   bashCommand(tool, textOfRow(row, "input")),
			Decision:  textOfRow(row, "decision"),
			CreatedAt: parseEventTime(textOfRow(row, "created_at")),
			DecidedAt: parseEventTime(textOfRow(row, "decided_at")),
		})
	}
	return records, nil
}

// bashCommand is the command a Bash ask's input holds; an input that decodes as no such object
// leaves the command empty.
func bashCommand(tool, input string) string {
	if tool != "Bash" {
		return ""
	}
	var decoded struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &decoded); err != nil {
		return ""
	}
	return decoded.Command
}
