// The hub's own database, hub.db in the home: the registry every session's reports build, one
// row per reported event and one per session, plus what the later parts of the hub (needs-you,
// decisions, devices, audit) grow beside them through numbered migrations (schema.go).
package hub

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// Report is one session event as the hooks send it (SPEC 5.1). Every field but the session, the
// event and its time is optional: a report that does not carry one leaves the session's stored
// value alone.
type Report struct {
	SessionID      string  `json:"session_id"`
	Machine        string  `json:"machine,omitempty"`
	Project        string  `json:"project,omitempty"`
	Branch         string  `json:"branch,omitempty"`
	Title          string  `json:"title,omitempty"`
	Task           string  `json:"task,omitempty"`
	Model          string  `json:"model,omitempty"`
	ContextPercent float64 `json:"context_percent,omitempty"`
	Cwd            string  `json:"cwd,omitempty"`
	Pid            int     `json:"pid,omitempty"`
	Channel        string  `json:"channel,omitempty"`
	// Member is the session's name in its channel: the bridge that acts it there holds the same
	// name, so a push ack under it belongs to this session.
	Member string `json:"member,omitempty"`
	// Digest is the session's digest, built by its Stop hook (SPEC 8); empty on every other
	// event.
	Digest string `json:"digest,omitempty"`
	// Notification is what a Notification event told the user, kept whole for the item it grows.
	Notification *Notification `json:"notification,omitempty"`
	LastEvent    string        `json:"last_event"`
	LastEventAt  string        `json:"last_event_at"`
}

// Notification is one Claude Code notification as the hook read it: its type and its message.
type Notification struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

// DB is the hub's database on one SQLite file, opened by Open.
type DB struct{ st *store.Store }

// Open opens (creating) the registry's database and brings its schema to the newest migration.
func Open(ctx context.Context, file string) (*DB, error) {
	st, err := store.Open(ctx, file)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, st); err != nil {
		_ = st.Close()
		return nil, err
	}
	return &DB{st: st}, nil
}

// Close ends the session.
func (d *DB) Close() error { return d.st.Close() }

// sessionUpsert records one report's session fields: the title keeps the first value said (the
// first prompt, until the session sets one), every other field keeps the newest said, and an
// empty value never erases what an earlier report said. A SessionEnd sets ended_at and a
// SessionStart clears it, so an ended session stays ended under an older report replayed after
// its end, and a resumed one comes back.
const sessionUpsert = `
INSERT INTO sessions (session_id, machine, project, branch, title, task, model, context_percent,
                      cwd, pid, channel, member, last_event, last_event_at, ended_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
  machine = COALESCE(NULLIF(excluded.machine, ''), sessions.machine),
  project = COALESCE(NULLIF(excluded.project, ''), sessions.project),
  branch = COALESCE(NULLIF(excluded.branch, ''), sessions.branch),
  title = COALESCE(NULLIF(sessions.title, ''), excluded.title),
  task = COALESCE(NULLIF(excluded.task, ''), sessions.task),
  model = COALESCE(NULLIF(excluded.model, ''), sessions.model),
  context_percent = COALESCE(NULLIF(excluded.context_percent, 0), sessions.context_percent),
  cwd = COALESCE(NULLIF(excluded.cwd, ''), sessions.cwd),
  pid = COALESCE(NULLIF(excluded.pid, 0), sessions.pid),
  channel = COALESCE(NULLIF(excluded.channel, ''), sessions.channel),
  member = COALESCE(NULLIF(excluded.member, ''), sessions.member),
  last_event = excluded.last_event,
  last_event_at = excluded.last_event_at,
  ended_at = CASE
    WHEN excluded.last_event = 'SessionStart' THEN NULL
    WHEN excluded.last_event = 'SessionEnd' THEN excluded.last_event_at
    ELSE sessions.ended_at
  END`

// Report records one report: a row in events holding it whole, the session's row upserted, and
// the needs-you items the event grows (SPEC 6.1): a notification that asks the owner a question,
// a finished turn nobody opened, and a prompt or a resume that ends the waiting turn items.
func (d *DB) Report(ctx context.Context, r Report) error {
	raw, err := domain.MarshalCompactJSON(r)
	if err != nil {
		return err
	}
	var endedAt any // NULL unless this report ends the session
	if r.LastEvent == "SessionEnd" {
		endedAt = r.LastEventAt
	}
	err = d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "INSERT INTO events (session_id, at, kind, json) VALUES (?, ?, ?, ?)",
			r.SessionID, r.LastEventAt, r.LastEvent, string(raw)); err != nil {
			return err
		}
		if err := q.Run(ctx, sessionUpsert, r.SessionID, r.Machine, r.Project, r.Branch, r.Title,
			r.Task, r.Model, r.ContextPercent, r.Cwd, r.Pid, r.Channel, r.Member,
			r.LastEvent, r.LastEventAt, endedAt); err != nil {
			return err
		}
		if r.Digest != "" {
			return saveDigestInTx(ctx, q, r.SessionID, r.Digest, r.LastEventAt)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return d.growItems(ctx, r)
}
