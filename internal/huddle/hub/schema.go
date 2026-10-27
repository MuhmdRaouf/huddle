package hub

// The registry's schema, in migrations numbered from 1: every migration runs once, in its
// number's order, at open, and the version it brought the file to sits in the meta table. An
// older huddle refuses a newer file rather than guess at its shape.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// SchemaVersion is the version an opened database is brought to, and the newest this build reads.
const SchemaVersion = 24

// migrations holds migration n's statements at index n-1.
var migrations = []string{
	// 1: one row per reported event, the whole report as it came.
	`CREATE TABLE events (
	   seq INTEGER PRIMARY KEY AUTOINCREMENT,
	   session_id TEXT NOT NULL,
	   at TEXT NOT NULL,
	   kind TEXT NOT NULL,
	   json TEXT NOT NULL
	 );
	 CREATE INDEX IF NOT EXISTS events_session ON events(session_id, seq);`,
	// 2: one row per session, upserted by every report.
	`CREATE TABLE sessions (
	   session_id TEXT PRIMARY KEY,
	   machine TEXT,
	   project TEXT,
	   branch TEXT,
	   title TEXT,
	   task TEXT,
	   model TEXT,
	   context_percent REAL,
	   cwd TEXT,
	   pid INTEGER,
	   channel TEXT,
	   last_event TEXT NOT NULL,
	   last_event_at TEXT NOT NULL
	 );`,
	// 3: the two facts that end a session: a SessionEnd report sets ended_at, the pid check
	// sets it when the session's process is gone, a SessionStart clears it. The state itself
	// stays computed on read.
	`ALTER TABLE sessions ADD COLUMN ended_at TEXT;`,
	// 4: device pairing (SPEC 7.3): one-use links kept as digests only, the devices their
	// passkeys enrolled, the sessions their cookies carry, and the audit and alert
	// rows a pairing owes. Times are whole milliseconds; a session dies with its device (the
	// removal cascades), and audit rows are never deleted by the app.
	`CREATE TABLE pairings (
	   digest TEXT PRIMARY KEY,
	   expires INTEGER NOT NULL
	 );
	 CREATE TABLE devices (
	   name TEXT PRIMARY KEY,
	   handle BLOB NOT NULL UNIQUE,
	   credential TEXT NOT NULL,
	   added INTEGER NOT NULL,
	   seen INTEGER NOT NULL
	 );
	 CREATE TABLE device_sessions (
	   digest TEXT PRIMARY KEY,
	   device TEXT NOT NULL REFERENCES devices(name) ON DELETE CASCADE,
	   expires INTEGER NOT NULL
	 );
	 CREATE INDEX IF NOT EXISTS device_sessions_device ON device_sessions(device);
	 CREATE TABLE audit (
	   id INTEGER PRIMARY KEY AUTOINCREMENT,
	   at INTEGER NOT NULL,
	   actor TEXT NOT NULL,
	   action TEXT NOT NULL,
	   target TEXT NOT NULL
	 );
	 CREATE TABLE alerts (
	   id INTEGER PRIMARY KEY AUTOINCREMENT,
	   at INTEGER NOT NULL,
	   kind TEXT NOT NULL,
	   json TEXT NOT NULL
	 );`,
	// 5: what waits on the owner and what carries their words to a session. items holds the
	// needs-you list (key dedupes a replayed report and names a sync's blocked task), messages
	// hold the owner's answers and sends until delivered, push_acks hold when a session's
	// bridge last proved its channel push flows. A session's member name ties its row to the
	// bridge that acts it in the channel, and opened_at says when the owner last read its screen.
	`CREATE TABLE items (
	   id INTEGER PRIMARY KEY AUTOINCREMENT,
	   key TEXT NOT NULL UNIQUE,
	   session_id TEXT NOT NULL,
	   kind TEXT NOT NULL,
	   summary TEXT NOT NULL,
	   created_at TEXT NOT NULL,
	   state TEXT NOT NULL,
	   snoozed_until TEXT
	 );
	 CREATE INDEX IF NOT EXISTS items_open ON items(state, session_id);
	 CREATE TABLE messages (
	   id INTEGER PRIMARY KEY AUTOINCREMENT,
	   session_id TEXT NOT NULL,
	   from_kind TEXT NOT NULL,
	   from_name TEXT NOT NULL DEFAULT '',
	   text TEXT NOT NULL,
	   state TEXT NOT NULL,
	   route TEXT,
	   created_at TEXT NOT NULL,
	   delivered_at TEXT
	 );
	 CREATE INDEX IF NOT EXISTS messages_pending ON messages(state, session_id);
	 CREATE TABLE push_acks (
	   channel TEXT NOT NULL,
	   member TEXT NOT NULL,
	   at TEXT NOT NULL,
	   PRIMARY KEY (channel, member)
	 );
	 ALTER TABLE sessions ADD COLUMN member TEXT;
	 ALTER TABLE sessions ADD COLUMN opened_at TEXT;`,
	// 6: the audit rows' detail and the alerts' read state. A detail carries what an action
	// worked on (never a credential — the server strips this run's secrets before storing);
	// an alert stays unread until the owner reads it, and the marking only ever moves forward.
	`ALTER TABLE audit ADD COLUMN detail TEXT;
	 ALTER TABLE alerts ADD COLUMN read_at INTEGER;`,
	// 7: satellite credentials (SPEC 6.7): the sha256 digest of a credential the hub minted is
	// the key, exactly as a pairing token's digest is, so a leaked database names no live
	// credential. `huddle pair --satellite` mints one and the satellite routes take it.
	`CREATE TABLE satellites (
		   digest TEXT PRIMARY KEY,
		   name TEXT NOT NULL,
		   added INTEGER NOT NULL,
		   seen INTEGER NOT NULL
		 );`,
	// 8: the permission flow's own columns on the items it grows: the tool and its input cut
	// to 8 KiB with its truncated flag, the poll that proves the asking hook lives, and the
	// decision one answer wrote first (state holds the generic outcome: answered for a
	// decision, withdrawn for the terminal's own answer).
	`ALTER TABLE items ADD COLUMN tool TEXT NOT NULL DEFAULT '';
	 ALTER TABLE items ADD COLUMN input TEXT NOT NULL DEFAULT '';
	 ALTER TABLE items ADD COLUMN truncated INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE items ADD COLUMN last_poll_at TEXT;
	 ALTER TABLE items ADD COLUMN decided_at TEXT;
	 ALTER TABLE items ADD COLUMN decision TEXT NOT NULL DEFAULT '';
	 ALTER TABLE items ADD COLUMN message TEXT NOT NULL DEFAULT '';`,
	// 9: step-up (SPEC 7.4): the moment a device session last proved its passkey, on the
	// session's own row so one browser's assertion never covers another's. The ceremonies that
	// mint a session stamp it (their user verification is itself an assertion); the step-up
	// finish refreshes it; NULL is a session that never asserted.
	`ALTER TABLE device_sessions ADD COLUMN stepup_at INTEGER;`,
	// 10: Web Push (SPEC 7.6): one subscription per paired device's endpoint, the device's own
	// notification choice beside it, and the items' push bookkeeping — one push at create, one
	// escalation at five minutes, nothing once decided. An endpoint is the push service's own
	// name for one subscription, so it is the row's key; a device's removal cascades to its
	// subscriptions, and its choice goes with it.
	`ALTER TABLE devices ADD COLUMN notify TEXT NOT NULL DEFAULT 'needs';
	 ALTER TABLE items ADD COLUMN pushed_at TEXT;
	 ALTER TABLE items ADD COLUMN escalated_at TEXT;
	 CREATE TABLE push_subscriptions (
	   endpoint TEXT PRIMARY KEY,
	   device TEXT NOT NULL REFERENCES devices(name) ON DELETE CASCADE,
	   p256dh TEXT NOT NULL,
	   auth TEXT NOT NULL,
	   added INTEGER NOT NULL
	 );`,
	// 11: the hub's handouts to satellites (SPEC 6.7): a decision or a message addressed to a
	// machine's sessions waits here until the machine's long poll takes it. A row a poll hands
	// out stays pending with offered_at naming the moment: the satellite answers delivered or
	// refused to end it, busy to be offered again after 15 s, and a satellite that never answers
	// is re-offered the same way, so no decision or message is lost to a dead poll.
	`CREATE TABLE sat_queue (
		   id INTEGER PRIMARY KEY AUTOINCREMENT,
		   machine TEXT NOT NULL,
		   kind TEXT NOT NULL,
		   session_id TEXT NOT NULL,
		   item_id INTEGER,
		   message_id INTEGER,
		   payload TEXT NOT NULL,
		   state TEXT NOT NULL,
		   offered_at TEXT,
		   result TEXT,
		   reason TEXT,
		   created_at TEXT NOT NULL
		 );
		 CREATE INDEX IF NOT EXISTS sat_queue_machine ON sat_queue(machine, state);`,
	// 12: links tie a session to a channel at a level (SPEC 8); a linked session hears the
	// channel through the owner-message queue, so it needs no membership of its own. A session
	// sits in a channel once, and linking it again only moves its level.
	`CREATE TABLE links (
	   channel TEXT NOT NULL,
	   session_id TEXT NOT NULL,
	   level TEXT NOT NULL,
	   linked_at TEXT NOT NULL,
	   PRIMARY KEY (channel, session_id)
	 );
	 CREATE INDEX IF NOT EXISTS links_channel ON links(channel);`,
	// 13: each session's newest digest (SPEC 8), one row per session: the Stop report that
	// carries one replaces the row whole, so a peer is always briefed with the session's latest
	// words and never a blend.
	`CREATE TABLE digests (
	   session_id TEXT PRIMARY KEY,
	   text TEXT NOT NULL,
	   at TEXT NOT NULL
	 );`,
	// 14: the needs-you entry push (SPEC 7.6): one push when a session enters needs-you, none
	// while it stays there. The mark names the moment the entry's push went out; NULL reads as
	// an entry that has had none, and the pass clears it when the session leaves the state, so
	// a re-entry pushes again.
	`ALTER TABLE sessions ADD COLUMN state_pushed_at TEXT;`,
	// 15: the harness a session runs in — codex, gemini, qwen, opencode, crush — as its hook
	// reported it; NULL and empty are Claude Code itself, which names no harness.
	`ALTER TABLE sessions ADD COLUMN harness TEXT;`,
	// 16: the rolling device sessions: the moment a session last renewed, so a request more than
	// an hour past it rolls the expiry seven days on and no other request writes. NULL is a
	// session an older build granted: its next request renews it.
	`ALTER TABLE device_sessions ADD COLUMN renewed INTEGER;`,
	// 17: authenticator-app sign-in (RFC 6238): one totp row per hub (id pinned to 1) holding
	// the shared secret with the last step a code paid for and the lockout's counters, the
	// sha256 digests of the one-time recovery codes, and how each device session was minted —
	// 'totp' sessions end when the enrolment is removed, 'passkey' ones ride their device. A
	// code-born device row carries the credential 'null' until a passkey lands on it.
	`ALTER TABLE device_sessions ADD COLUMN method TEXT NOT NULL DEFAULT 'passkey';
	 CREATE TABLE totp (
	   id INTEGER PRIMARY KEY CHECK (id = 1),
	   secret TEXT NOT NULL,
	   last_step INTEGER NOT NULL,
	   fails INTEGER NOT NULL,
	   window_start INTEGER NOT NULL,
	   locked_until INTEGER NOT NULL
	 );
	 CREATE TABLE totp_recovery (
	   digest TEXT PRIMARY KEY,
	   added INTEGER NOT NULL
	 );`,
	// 18: older builds took Claude Code's own injected text (a task notification, a system
	// reminder, `!` shell input, a slash-command wrapper) for a session's title or task, and
	// the registry kept it; those rows are cleared so a real prompt can fill them.
	`UPDATE sessions SET title = NULL
	   WHERE ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<command-name>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<command-message>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<command-args>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<local-command-stdout>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<local-command-stderr>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<system-reminder>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<task-notification>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<bash-input>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<bash-stdout>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<bash-stderr>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE '<user-prompt-submit-hook>%'
	      OR ltrim(title, ' ' || char(10) || char(13) || char(9)) LIKE 'Caveat:%';
	 UPDATE sessions SET task = NULL
	   WHERE ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<command-name>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<command-message>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<command-args>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<local-command-stdout>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<local-command-stderr>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<system-reminder>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<task-notification>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<bash-input>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<bash-stdout>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<bash-stderr>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE '<user-prompt-submit-hook>%'
	      OR ltrim(task, ' ' || char(10) || char(13) || char(9)) LIKE 'Caveat:%';`,
	// 19: the agent-team teammates under a session, one row per teammate holding the newest
	// state it reported.
	`CREATE TABLE teammates (
	   session_id TEXT NOT NULL,
	   name TEXT NOT NULL,
	   state TEXT NOT NULL,
	   at TEXT NOT NULL,
	   PRIMARY KEY (session_id, name)
	 );`,
	// 20: the finished-turn push (Settings › Notifications): its own wake choice per device, off
	// until the device turns it on.
	`ALTER TABLE devices ADD COLUMN notify_turn INTEGER NOT NULL DEFAULT 0;`,
	// 21: whether the acking session's claude runs with the channels flag: Claude Code drops a
	// channel push the session did not load huddle's server for, so an ack without it routes
	// nothing now.
	`ALTER TABLE push_acks ADD COLUMN listens INTEGER NOT NULL DEFAULT 0;`,
	// 22: the session's transcript JSONL on its own machine, as the hook's stdin names it, for
	// the hub to read the conversation from; no row the web reads carries it.
	`ALTER TABLE sessions ADD COLUMN transcript_path TEXT NOT NULL DEFAULT '';`,
	// 23: the session's messaging inbox, the Unix socket Claude Code hands its hooks, for the hub
	// to deliver owner messages to; no row the web reads carries it.
	`ALTER TABLE sessions ADD COLUMN inbox_socket TEXT NOT NULL DEFAULT '';`,
	// 24: the answers an owner picked on an AskUserQuestion allow, as compact JSON; empty for
	// every other item.
	`ALTER TABLE items ADD COLUMN answers TEXT NOT NULL DEFAULT '';`,
}

// NewerSchemaError names a database written by a newer huddle: an older one refuses it.
type NewerSchemaError struct {
	Version int
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("huddle hub.db was written by a newer huddle (schema %d); update huddle", e.Version)
}

// migrate brings an opened store to SchemaVersion, running every migration the file lacks.
func migrate(ctx context.Context, st *store.Store) error {
	if err := st.Run(ctx, "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		return fmt.Errorf("create the meta table: %w", err)
	}
	row, err := st.Get(ctx, "SELECT value FROM meta WHERE key = 'schema_version'")
	if err != nil {
		return fmt.Errorf("read the schema version: %w", err)
	}
	version := 0
	if row != nil {
		value, _ := row["value"].(string)
		// a value that parses as no number reads as version 0: every migration applies
		if n, err := strconv.Atoi(value); err == nil {
			version = n
		}
	}
	if version > SchemaVersion {
		return &NewerSchemaError{Version: version}
	}
	if version == SchemaVersion {
		return nil // an up-to-date database is left byte for byte as it lays
	}
	// each migration lands with its own version mark in one transaction: a start stopped
	// between two statements leaves a database the next start resumes, never one that says
	// a table already exists
	for n := version; n < SchemaVersion; n++ {
		err := st.Tx(ctx, func(ctx context.Context, q store.Q) error {
			if err := q.Run(ctx, migrations[n]); err != nil {
				return fmt.Errorf("migration %d: %w", n+1, err)
			}
			if err := q.Run(ctx, "INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)",
				strconv.Itoa(n+1)); err != nil {
				return fmt.Errorf("record schema version %d: %w", n+1, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
