// The owner's words carried into a session (SPEC 6.3, 6.4): one queued row per session, headed
// by who sent it, delivered through a channel push while the session's bridge proves its pushes
// flow and its claude runs with the channels flag (route now), and otherwise taken by the listen
// hook on the session's next prompt (route next_prompt). The registry row says which route a
// message to the session would take.
package hub

import (
	"context"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// The two delivery routes (SPEC 6.4), and the message senders: the owner, another session whose
// words the owner passed on, or a channel a session is linked to (SPEC 8). A channel message's
// FromName is <channel>/<member>.
const (
	RouteNow        = "now"
	RouteNextPrompt = "next_prompt"
	FromOwner       = "owner"
	FromSession     = "session"
	FromChannel     = "channel"
	FromDigest      = "digest"
)

// ackFreshFor is how long a bridge's proof holds: an ack older than this leaves the session on
// the next-prompt route until the bridge says its pushes flow again.
const ackFreshFor = 10 * time.Minute

// MessageIn is one message to queue for a session.
type MessageIn struct {
	SessionID string
	FromKind  string
	// FromName is the sending session's name; the session header names it.
	FromName string
	Text     string
}

// Header answers the text as the session reads it. The shapes are the spec's own words, so a
// session can always tell an owner's answer, a peer's relayed words and a linked channel's post
// apart.
func (m MessageIn) Header() string {
	if m.FromKind == FromChannel {
		// FromName is <channel>/<member>: the channel the post came from and who spoke there
		channel, member, _ := strings.Cut(m.FromName, "/")
		return "In #" + channel + ", " + member + " said: " + m.Text
	}
	if m.FromKind == FromDigest {
		return "Digest from session " + m.FromName + " via huddle: " + m.Text
	}
	if m.FromKind == FromSession {
		return "From session " + m.FromName + " via huddle (sent for the owner): " + m.Text
	}
	return "From the owner via huddle: " + m.Text
}

// Message is one waiting message row, as the ack flush reads it.
type Message struct {
	ID    int64
	In    MessageIn
	State string
	At    string
}

// QueueMessage records one message as waiting for its session and answers its id.
func (d *DB) QueueMessage(ctx context.Context, in MessageIn, at string) (int64, error) {
	var id int64
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx,
			"INSERT INTO messages (session_id, from_kind, from_name, text, state, created_at) VALUES (?, ?, ?, ?, 'pending', ?)",
			in.SessionID, in.FromKind, in.FromName, in.Text, at); err != nil {
			return err
		}
		row, err := q.Get(ctx, "SELECT last_insert_rowid() AS id")
		if err != nil {
			return err
		}
		if row != nil {
			id = rowID(row)
		}
		return nil
	})
	return id, err
}

// TakeMessages answers the session's waiting messages as the session reads them, oldest first,
// and marks each delivered on the next-prompt route (SPEC 6.4's second route: the listen hook
// adds them to the next UserPromptSubmit). A take is once: a second run answers nothing.
func (d *DB) TakeMessages(ctx context.Context, session, at string) ([]string, error) {
	var out []string
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		rows, err := q.All(ctx,
			"SELECT id, from_kind, from_name, text FROM messages WHERE session_id = ? AND state = 'pending' ORDER BY id",
			session)
		if err != nil {
			return err
		}
		out = make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, MessageIn{
				SessionID: session,
				FromKind:  textOfRow(row, "from_kind"),
				FromName:  textOfRow(row, "from_name"),
				Text:      textOfRow(row, "text"),
			}.Header())
			if err := q.Run(ctx,
				"UPDATE messages SET state = 'delivered', route = ?, delivered_at = ? WHERE id = ?",
				RouteNextPrompt, at, rowID(row)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MarkDelivered records one message delivered on its route.
func (d *DB) MarkDelivered(ctx context.Context, id int64, route, at string) error {
	return d.st.Run(ctx,
		"UPDATE messages SET state = 'delivered', route = ?, delivered_at = ? WHERE id = ?", route, at, id)
}

// PendingOf answers the messages still waiting on one channel member, oldest first: what a fresh
// push ack promotes to the now route.
func (d *DB) PendingOf(ctx context.Context, channel, member string) ([]Message, error) {
	rows, err := d.st.All(ctx,
		`SELECT m.id, m.session_id, m.from_kind, m.from_name, m.text, m.created_at FROM messages m
		 JOIN sessions s ON s.session_id = m.session_id
		 WHERE m.state = 'pending' AND s.channel = ? AND s.member = ? ORDER BY m.id`, channel, member)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, Message{
			ID: rowID(row),
			In: MessageIn{
				SessionID: textOfRow(row, "session_id"),
				FromKind:  textOfRow(row, "from_kind"),
				FromName:  textOfRow(row, "from_name"),
				Text:      textOfRow(row, "text"),
			},
			State: "pending",
			At:    textOfRow(row, "created_at"),
		})
	}
	return out, nil
}

// RecordPushAck records a bridge's proof that its channel pushes flow (SPEC 6.4: the bridge
// reports whether pushes are acknowledged), with whether the member's claude, started as command,
// shows them. One row per channel member holds the newest proof.
func (d *DB) RecordPushAck(ctx context.Context, channel, member, command, at string) error {
	listens := 0
	if ListensForChannels(command) {
		listens = 1
	}
	return d.st.Run(ctx,
		"INSERT INTO push_acks (channel, member, at, listens) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(channel, member) DO UPDATE SET at = excluded.at, listens = excluded.listens",
		channel, member, at, listens)
}

// ListensForChannels answers whether a claude command line loads huddle's channel server:
// --dangerously-load-development-channels or --channels with a value (after "=", or the words up
// to the next flag) naming huddle. Claude Code shows a channel push only from a server one of
// them loads; any other push it drops and tells the sender nothing.
func ListensForChannels(command string) bool {
	words := strings.Fields(command)
	for i, word := range words {
		name, value, inline := strings.Cut(word, "=")
		if name != "--dangerously-load-development-channels" && name != "--channels" {
			continue
		}
		if inline && strings.Contains(value, "huddle") {
			return true
		}
		for _, next := range words[i+1:] {
			if strings.HasPrefix(next, "-") {
				break
			}
			if strings.Contains(next, "huddle") {
				return true
			}
		}
	}
	return false
}

// routeOfMember answers the route a message to one channel member takes at now: a push only
// while the member's bridge acked lately from a claude that shows channel pushes.
func (d *DB) routeOfMember(ctx context.Context, channel, member string, now time.Time) string {
	if channel == "" || member == "" {
		return RouteNextPrompt // no channel, no bridge: the next prompt is the only way in
	}
	row, err := d.st.Get(ctx, "SELECT at, listens FROM push_acks WHERE channel = ? AND member = ?", channel, member)
	if err != nil || row == nil {
		return RouteNextPrompt
	}
	if listens, _ := row["listens"].(int64); listens != 1 {
		return RouteNextPrompt
	}
	if at := parseEventTime(textOfRow(row, "at")); !at.IsZero() && now.Sub(at) < ackFreshFor {
		return RouteNow
	}
	return RouteNextPrompt
}

// SessionOfMember answers the newest session one channel member acts, or "" when none is
// registered: the blocked-item sync maps a channel task's owner to the session it belongs to.
func (d *DB) SessionOfMember(ctx context.Context, channel, member string) (string, error) {
	row, err := d.st.Get(ctx,
		"SELECT session_id FROM sessions WHERE channel = ? AND member = ? AND ended_at IS NULL "+
			"ORDER BY last_event_at DESC LIMIT 1", channel, member)
	if err != nil || row == nil {
		return "", err
	}
	return textOfRow(row, "session_id"), nil
}
