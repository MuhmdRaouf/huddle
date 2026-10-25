// The needs-you items (SPEC 6.1): what waits on the owner. A question item grows from a
// Notification that asks the owner, a turn item from a Stop nobody opened since the session's
// last prompt, a blocked item from a channel task that waits on a task the owner owns (kept in
// step by SyncBlocked). An item's state moves through snooze and dismiss; the session's state
// reads needs-you while one of its items is open.
package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The item kinds (SPEC 6.1). permission belongs to the permission flow, not this list.
const (
	KindQuestion = "question"
	KindTurn     = "turn"
	KindBlocked  = "blocked"
)

// The item states. open is the waiting state; a snooze keeps it open behind a time; the other
// three are terminal.
const (
	StateOpen      = "open"
	StateAnswered  = "answered"
	StateDismissed = "dismissed"
	StateWithdrawn = "withdrawn"
	// StateSnoozed is what a read answers for an open item whose snooze still holds.
	StateSnoozed = "snoozed"
)

// Item is one needs-you item as the list answers it (SPEC 6.1: id, session, kind, one-line
// summary, created, state). State is computed when read: an open item behind its snooze answers
// snoozed, one whose snooze passed answers open again. The permission kind carries its tool
// call on the same row (items.go); every other kind leaves those fields empty.
type Item struct {
	ID           int64  `json:"id"`
	SessionID    string `json:"session"`
	Kind         string `json:"kind"`
	Summary      string `json:"summary"`
	CreatedAt    string `json:"created_at"`
	State        string `json:"state"`
	SnoozedUntil string `json:"snoozed_until,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Input        string `json:"input,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	Decision     string `json:"decision,omitempty"`
	Message      string `json:"message,omitempty"`
}

// ItemIn is one item to record. Key dedupes: a report replayed from the outbox grows no second
// item, and a blocked item keeps its identity across the syncs that restate it.
type ItemIn struct {
	Key       string
	SessionID string
	Kind      string
	Summary   string
}

// BlockedIn is one channel task the sync found waiting on the owner.
type BlockedIn struct {
	Key       string
	SessionID string
	Summary   string
}

// questionTypes are the notification types that name Claude asking the owner: an elicitation
// dialog (an MCP server or Claude itself wanting input) and a background agent that needs it.
var questionTypes = map[string]bool{
	"elicitation_dialog": true, "elicitation_url_dialog": true, "agent_needs_input": true,
}

// settledTypes are the notification types that never ask the owner a question: a permission
// prompt belongs to the permission flow and the rest report something that already happened.
var settledTypes = map[string]bool{
	"permission_prompt": true, "auth_success": true, "elicitation_complete": true,
	"elicitation_response": true, "agent_completed": true, "quota_auto_resume_fired": true,
	"quota_auto_resume_stale": true, "quota_auto_resume_disabled": true,
}

// asksOwner says whether one notification asks the owner a question: its type names it, or a
// type this build does not know falls back to the message ending in a question mark.
func asksOwner(n *Notification) bool {
	if n == nil {
		return false
	}
	if questionTypes[n.Type] {
		return true
	}
	if settledTypes[n.Type] {
		return false
	}
	return strings.HasSuffix(strings.TrimSpace(n.Message), "?")
}

// growItems records the items one report grows. A question is keyed by its event, so a replayed
// report grows nothing; a turn is keyed by the prompt it answers, so a stop-hook loop within one
// turn refreshes nothing either.
func (d *DB) growItems(ctx context.Context, r Report) error {
	switch {
	case r.LastEvent == "Notification" && asksOwner(r.Notification):
		summary := cleanSummary(r.Notification.Message)
		if summary == "" {
			return nil
		}
		// the summary hashes into the key: two questions in the same second stay two items
		digest := sha256.Sum256([]byte(summary))
		return d.addItem(ctx, ItemIn{
			Key:       fmt.Sprintf("question:%s:%s:%s", r.SessionID, r.LastEventAt, hex.EncodeToString(digest[:4])),
			SessionID: r.SessionID,
			Kind:      KindQuestion,
			Summary:   summary,
		}, r.LastEventAt)
	case r.LastEvent == "Stop":
		prompt, err := d.lastPromptAt(ctx, r.SessionID)
		if err != nil {
			return err
		}
		if prompt.IsZero() {
			return nil // no turn finished: the session stopped before it began
		}
		opened, err := d.openedAt(ctx, r.SessionID)
		if err != nil {
			return err
		}
		if opened.After(prompt) {
			return nil // the owner read the session since its last prompt
		}
		// the digest's first line says what the turn did; the task and the title say what the
		// session is about
		first, _, _ := strings.Cut(r.Digest, "\n")
		summary := cleanSummary(first)
		if summary == "" {
			summary = cleanSummary(r.Task)
		}
		if summary == "" {
			summary = cleanSummary(r.Title)
		}
		if summary == "" {
			summary = "a finished turn"
		}
		return d.addItem(ctx, ItemIn{
			Key:       "turn:" + r.SessionID + ":" + prompt.Format(time.RFC3339Nano),
			SessionID: r.SessionID,
			Kind:      KindTurn,
			Summary:   summary,
		}, r.LastEventAt)
	case r.LastEvent == "UserPromptSubmit" || r.LastEvent == "SessionStart":
		// the owner is talking to the session again, or reopened it: its finished turns wait no
		// longer
		return d.withdrawKind(ctx, KindTurn, r.SessionID)
	}
	return nil
}

// addItem records one item unless its key already holds one, so a replayed report or a restated
// sync grows nothing and never reopens what the owner dismissed.
func (d *DB) addItem(ctx context.Context, in ItemIn, at string) error {
	return d.st.Run(ctx,
		"INSERT INTO items (key, session_id, kind, summary, created_at, state) VALUES (?, ?, ?, ?, ?, 'open') "+
			"ON CONFLICT(key) DO NOTHING",
		in.Key, in.SessionID, in.Kind, in.Summary, at)
}

// lastPromptAt answers when the session's newest UserPromptSubmit arrived.
func (d *DB) lastPromptAt(ctx context.Context, session string) (time.Time, error) {
	row, err := d.st.Get(ctx,
		"SELECT at FROM events WHERE session_id = ? AND kind = 'UserPromptSubmit' ORDER BY seq DESC LIMIT 1", session)
	if err != nil || row == nil {
		return time.Time{}, err
	}
	return parseEventTime(textOfRow(row, "at")), nil
}

// openedAt answers when the owner last read the session's screen; the zero time when never.
func (d *DB) openedAt(ctx context.Context, session string) (time.Time, error) {
	row, err := d.st.Get(ctx, "SELECT opened_at FROM sessions WHERE session_id = ?", session)
	if err != nil || row == nil {
		return time.Time{}, err
	}
	return parseEventTime(textOfRow(row, "opened_at")), nil
}

// OpenTheSession records the owner reading one session (its detail screen): its open turn items
// are answered — a turn the owner opened waits no longer — and opened_at moves forward only, so
// a read the storage serves late never un-opens the session. It answers the items it answered,
// each in its answered state, so the caller can tell every open list.
func (d *DB) OpenTheSession(ctx context.Context, session, at string) ([]Item, error) {
	var answered []Item
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		answered = nil
		if err := q.Run(ctx,
			"UPDATE sessions SET opened_at = MAX(COALESCE(opened_at, ''), ?) WHERE session_id = ?", at, session); err != nil {
			return err
		}
		rows, err := q.All(ctx,
			"SELECT id, session_id, kind, summary, created_at, snoozed_until, tool, input, truncated, decision, message "+
				"FROM items WHERE session_id = ? AND kind = ? AND state = 'open' ORDER BY id", session, KindTurn)
		if err != nil {
			return err
		}
		for _, row := range rows {
			item := itemOfRow(row)
			if err := q.Run(ctx, "UPDATE items SET state = 'answered' WHERE id = ?", item.ID); err != nil {
				return err
			}
			item.State = StateAnswered
			answered = append(answered, item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return answered, nil
}

// CloseQuestions answers every open question item of one session: the owner's answer is on its
// way (SPEC 6.3).
func (d *DB) CloseQuestions(ctx context.Context, session string) error {
	return d.st.Run(ctx,
		"UPDATE items SET state = 'answered' WHERE session_id = ? AND kind = ? AND state = 'open'",
		session, KindQuestion)
}

// withdrawKind closes every open item of one kind for one session.
func (d *DB) withdrawKind(ctx context.Context, kind, session string) error {
	return d.st.Run(ctx,
		"UPDATE items SET state = 'withdrawn' WHERE session_id = ? AND kind = ? AND state = 'open'",
		session, kind)
}

// SnoozeItem pushes one open item behind until. An item in a terminal state refuses: its wait is
// over.
func (d *DB) SnoozeItem(ctx context.Context, id int64, until time.Time) error {
	return d.updateOpenItem(ctx, id, func() (string, []any) {
		return "UPDATE items SET snoozed_until = ? WHERE id = ?", []any{jsfmt.ISOTime(until), id}
	})
}

// DismissItem ends one open item: the owner wants it gone.
func (d *DB) DismissItem(ctx context.Context, id int64) error {
	return d.updateOpenItem(ctx, id, func() (string, []any) {
		return "UPDATE items SET state = 'dismissed' WHERE id = ?", []any{id}
	})
}

// updateOpenItem runs one statement against an item that must still be open.
func (d *DB) updateOpenItem(ctx context.Context, id int64, stmt func() (string, []any)) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		row, err := q.Get(ctx, "SELECT state FROM items WHERE id = ?", id)
		if err != nil {
			return err
		}
		if row == nil || textOfRow(row, "state") != StateOpen {
			return &MissingItemError{ID: id}
		}
		query, args := stmt()
		return q.Run(ctx, query, args...)
	})
}

// MissingItemError names an item that is not there to act on: gone, or no longer open.
type MissingItemError struct {
	ID int64
}

func (e *MissingItemError) Error() string {
	return fmt.Sprintf("no open item %d", e.ID)
}

// Items answers the waiting items, newest first: every open one, its snooze read at now. The
// terminal states stay off the list.
func (d *DB) Items(ctx context.Context, now time.Time) ([]Item, error) {
	rows, err := d.st.All(ctx,
		"SELECT id, session_id, kind, summary, created_at, snoozed_until, tool, input, truncated, decision, message "+
			"FROM items WHERE state = 'open' "+
			"ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		item := itemOfRow(row)
		item.State = StateOpen
		if item.SnoozedUntil != "" && parseEventTime(item.SnoozedUntil).After(now) {
			item.State = StateSnoozed
		}
		out = append(out, item)
	}
	return out, nil
}

// OpenItemSessions answers the sessions with an item waiting at now: one whose snooze still
// holds does not wait.
func (d *DB) OpenItemSessions(ctx context.Context, now time.Time) (map[string]bool, error) {
	rows, err := d.st.All(ctx, "SELECT session_id, snoozed_until FROM items WHERE state = 'open'")
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, row := range rows {
		if until := textOfRow(row, "snoozed_until"); until != "" && parseEventTime(until).After(now) {
			continue
		}
		out[textOfRow(row, "session_id")] = true
	}
	return out, nil
}

// SyncBlocked brings the blocked items in step with the channels: every task the sync names gets
// its open item, and an open item whose task left the list withdraws — the task moved on. An
// item the owner dismissed or snoozed keeps its state: the sync only restates what still waits.
func (d *DB) SyncBlocked(ctx context.Context, in []BlockedIn, at string) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		rows, err := q.All(ctx, "SELECT key FROM items WHERE kind = ? AND state = 'open'", KindBlocked)
		if err != nil {
			return err
		}
		open := map[string]bool{}
		for _, row := range rows {
			open[textOfRow(row, "key")] = true
		}
		for _, one := range in {
			if open[one.Key] {
				delete(open, one.Key) // what stays is what must withdraw
				continue
			}
			// a withdrawn row is the same task come back blocked; a dismissed or snoozed one is
			// the owner's own answer to it and stays as it is
			if err := q.Run(ctx,
				"INSERT INTO items (key, session_id, kind, summary, created_at, state) VALUES (?, ?, ?, ?, ?, 'open') "+
					"ON CONFLICT(key) DO UPDATE SET state = 'open', summary = excluded.summary, "+
					"created_at = excluded.created_at WHERE items.state = 'withdrawn'",
				one.Key, one.SessionID, KindBlocked, cleanSummary(one.Summary), at); err != nil {
				return err
			}
			delete(open, one.Key)
		}
		for key := range open {
			if err := q.Run(ctx,
				"UPDATE items SET state = 'withdrawn' WHERE kind = ? AND key = ? AND state = 'open'",
				KindBlocked, key); err != nil {
				return err
			}
		}
		return nil
	})
}

// cleanSummary makes one line of s: control and bidirectional characters go (they steer
// terminals and reorder text), and whatever goes reads as a break, so words never glue; the
// line is cut to 200 UTF-16 units (SPEC 6.1: one line, cleaned).
func cleanSummary(s string) string {
	var out strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Bidi_Control, r), unicode.IsControl(r):
			space = out.Len() > 0
			continue
		case unicode.IsSpace(r):
			space = out.Len() > 0
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	return jsfmt.UTF16Slice(strings.TrimRight(out.String(), " "), 0, 200)
}

// rowID reads one row's id; NULL and any value that is not an integer read as 0.
func rowID(row store.Row) int64 {
	id, _ := row["id"].(int64)
	return id
}
