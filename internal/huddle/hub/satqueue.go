package hub

// The satellite queue (SPEC 6.7): what the hub owes a satellite machine — a decision the owner
// made on one of the machine's permission items, a message the owner addressed to one of its
// sessions — waits in sat_queue until the machine's long poll takes it. A row stays pending
// while it is out: the satellite's result ends it (delivered, refused with a reason), busy
// moves its offer forward so it is handed again after SatReofferAfter, and a satellite that
// never answers is re-offered on the same clock, so a poll that dies mid-flight strands
// nothing. A decision handout carries the item's key, not its row id: the satellite finds its
// own row by the key it forwarded, and never trusts an id from the wire.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// The handout kinds, the queue's two states, and the results a satellite may answer with.
const (
	SatDecision = "decision"
	SatMessage  = "message"

	satPending = "pending"
	satDone    = "done"

	SatDelivered = "delivered"
	SatRefused   = "refused"
	SatBusy      = "busy"
)

// SatReofferAfter is how long a handout rests before the hub offers it again: the span SPEC 6.7
// fixes for a busy answer, and the same grace a satellite that never answers gets.
const SatReofferAfter = 15 * time.Second

// SatInviteTTL is how long a `huddle pair --satellite` invite lives (SPEC 6.7: one use,
// 10 minutes).
const SatInviteTTL = 10 * time.Minute

// SatHandout is one queued row as the machine's poll reads it: the payload is the exact JSON
// body the satellite applies, keyed by the handout's own id for the result it owes.
type SatHandout struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	SessionID string `json:"-"`
	ItemID    int64  `json:"-"`
	MessageID int64  `json:"-"`
	Payload   string `json:"payload"`
}

// SatEnqueue records one handout for a machine and answers its id.
func (d *DB) SatEnqueue(ctx context.Context, machine, kind, session string, itemID, messageID int64, payload, at string) (int64, error) {
	var id int64
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx,
			"INSERT INTO sat_queue (machine, kind, session_id, item_id, message_id, payload, state, created_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			machine, kind, session, nilZero(itemID), nilZero(messageID), payload, satPending, at); err != nil {
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

// SatOffer answers the rows a machine's poll may take, oldest first, and marks each offered at
// now: a row offered inside the last SatReofferAfter stays put (it is in flight, or its machine
// answered busy), and one older than that is offered again.
func (d *DB) SatOffer(ctx context.Context, machine string, now time.Time) ([]SatHandout, error) {
	reoffer := jsscalar.ISOTime(now.Add(-SatReofferAfter))
	rows, err := d.st.All(ctx,
		"SELECT id, kind, session_id, item_id, message_id, payload FROM sat_queue "+
			"WHERE machine = ? AND state = ? AND (offered_at IS NULL OR offered_at <= ?) ORDER BY id",
		machine, satPending, reoffer)
	if err != nil {
		return nil, err
	}
	out := make([]SatHandout, 0, len(rows))
	at := jsscalar.ISOTime(now)
	for _, row := range rows {
		handout := satHandoutOf(row)
		out = append(out, handout)
		if err := d.st.Run(ctx, "UPDATE sat_queue SET offered_at = ? WHERE id = ?", at, handout.ID); err != nil {
			return out, err
		}
	}
	return out, nil
}

// SatResult records one machine's answer for its handout. delivered and refused end the row;
// busy leaves it pending with its offer moved to now, so the hub re-offers it after
// SatReofferAfter. A row that is not pending answers false: it was already answered, and the
// caller tells the satellite so instead of taking a second result.
func (d *DB) SatResult(ctx context.Context, id int64, result, reason, at string) (SatHandout, bool, error) {
	handout := SatHandout{}
	live := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		row, err := q.Get(ctx,
			"SELECT id, kind, session_id, item_id, message_id, payload, state FROM sat_queue WHERE id = ?", id)
		if err != nil || row == nil {
			return err
		}
		handout = satHandoutOf(row)
		if textOfRow(row, "state") != satPending {
			return nil
		}
		live = true
		if result == SatBusy {
			return q.Run(ctx, "UPDATE sat_queue SET offered_at = ?, result = NULL, reason = '' WHERE id = ?", at, id)
		}
		return q.Run(ctx, "UPDATE sat_queue SET state = ?, result = ?, reason = ? WHERE id = ?",
			satDone, result, reason, id)
	})
	return handout, live, err
}

// SatelliteKnown answers whether a machine name is a satellite this hub holds a credential for:
// only its handouts are queued, and only its reports are stamped with its name.
func (d *DB) SatelliteKnown(ctx context.Context, machine string) (bool, error) {
	row, err := d.st.Get(ctx, "SELECT 1 FROM satellites WHERE name = ?", machine)
	return err == nil && row != nil, err
}

// SessionMachine answers the machine a session reports from, and whether the session is known.
func (d *DB) SessionMachine(ctx context.Context, session string) (string, bool, error) {
	row, err := d.st.Get(ctx, "SELECT machine FROM sessions WHERE session_id = ?", session)
	if err != nil || row == nil {
		return "", false, err
	}
	machine := textOfRow(row, "machine")
	return machine, true, nil
}

// KeyOfItem answers the key one item row carries, the key a decision handout travels under.
func (d *DB) KeyOfItem(ctx context.Context, id int64) (string, error) {
	row, err := d.st.Get(ctx, "SELECT key FROM items WHERE id = ?", id)
	if err != nil || row == nil {
		return "", err
	}
	return textOfRow(row, "key"), nil
}

// ConsumePairing spends one live pairing (one use: the satellite invite spends the same table
// the device links do): the delete is the consume, so two joins racing leave exactly one
// winner.
func (d *DB) ConsumePairing(ctx context.Context, digest string, nowMS int64) (bool, error) {
	spent := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM pairings WHERE digest = ? AND expires > ?", digest, nowMS); err != nil {
			return err
		}
		gone, err := changesOf(ctx, q)
		spent = gone
		return err
	})
	return spent, err
}

// MintSatelliteInvite draws one join invite for `huddle pair --satellite`: its digest waits in
// pairings for SatInviteTTL, and the token itself appears exactly once, in the answer.
func (d *DB) MintSatelliteInvite(ctx context.Context, nowMS int64) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("draw the satellite invite: %w", err)
	}
	token := "hsat_" + base64.RawURLEncoding.EncodeToString(raw)
	if err := d.SavePairing(ctx, PairDigestOf(token), nowMS+SatInviteTTL.Milliseconds()); err != nil {
		return "", err
	}
	return token, nil
}

// ExchangeSatellite spends one invite and mints the machine's credential: the invite is
// consumed first, so a replayed join buys nothing, and the credential's digest is the only
// thing stored — with the audit row and the alert a satellite added owes (SPEC 7.7). The token
// itself appears exactly once, in the answer.
func (d *DB) ExchangeSatellite(ctx context.Context, invite, name string, nowMS int64) (string, error) {
	if spent, err := d.ConsumePairing(ctx, PairDigestOf(invite), nowMS); err != nil {
		return "", err
	} else if !spent {
		return "", fmt.Errorf("invalid or expired satellite invite: run `huddle pair --satellite` on the hub again")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("draw the satellite credential: %w", err)
	}
	credential := "hsat_" + base64.RawURLEncoding.EncodeToString(raw)
	if err := d.SaveSatellite(ctx, PairDigestOf(credential), name, nowMS); err != nil {
		return "", err
	}
	return credential, nil
}

// PairDigestOf is a satellite token's stored form: the hex of its sha256, the same digest the
// pairing tokens take.
func PairDigestOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Event is one stored report as the satellite link forwards it: the row's number is the link's
// cursor, the report is what the hub's own report route would have taken.
type Event struct {
	Seq    int64
	Report Report
}

// EventsAfter answers the events newer than after seq, oldest first, at most limit of them: a
// link that loses the hub walks this cursor forward once the batch lands.
func (d *DB) EventsAfter(ctx context.Context, after int64, limit int) ([]Event, error) {
	rows, err := d.st.All(ctx,
		"SELECT seq, json FROM events WHERE seq > ? ORDER BY seq LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(rows))
	for _, row := range rows {
		var rep Report
		if err := json.Unmarshal([]byte(textOfRow(row, "json")), &rep); err != nil {
			continue // a torn row is skipped, never a blocker for the ones behind it
		}
		seq, _ := row["seq"].(int64)
		out = append(out, Event{Seq: seq, Report: rep})
	}
	return out, nil
}

// NewestSeq answers the number of the newest stored event, 0 on none: where a link's cursor
// starts, so a restart never replays a history the hub may already hold.
func (d *DB) NewestSeq(ctx context.Context) (int64, error) {
	row, err := d.st.Get(ctx, "SELECT seq AS id FROM events ORDER BY seq DESC LIMIT 1")
	if err != nil || row == nil {
		return 0, err
	}
	return rowID(row), nil
}

// ForwardedItem is one permission item a satellite grew locally and hands the hub: the key is
// the satellite's own, so a replayed handout grows no second row.
type ForwardedItem struct {
	Key       string
	SessionID string
	Tool      string
	Input     string
	Truncated bool
	CreatedAt string
}

// SaveForwardedItem records one satellite's permission item under its own key. The input is cut
// again at this side's own limit — the satellite already cut it, and a row that arrives long
// still never stores more than the limit — with the truncated flag kept set when either side
// set it, because a cut input may never be allowed remotely whichever side noticed the cut. The
// row is born unpolled: the machine's link polls for it, and TouchPollsOfMachine is what keeps
// it alive.
func (d *DB) SaveForwardedItem(ctx context.Context, in ForwardedItem) error {
	kept, cut := cutInput(in.Input)
	summary := cleanSummary(in.Tool + ": " + JoinedInput(in.Tool, kept))
	return d.st.Run(ctx,
		"INSERT INTO items (key, session_id, kind, summary, created_at, state, "+permissionColumns+") "+
			"VALUES (?, ?, ?, ?, ?, 'open', ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(key) DO NOTHING",
		in.Key, in.SessionID, KindPermission, summary, in.CreatedAt,
		in.Tool, kept, cut || in.Truncated, nil, nil, "", "", "")
}

// ForwardList answers the machine's open permission items as its link forwards them, oldest
// first: every flush carries the whole open set, and the hub's ON CONFLICT(key) grows no
// second row for one it holds.
func (d *DB) ForwardList(ctx context.Context) ([]ForwardedItem, error) {
	rows, err := d.st.All(ctx,
		"SELECT key, session_id, tool, input, truncated, created_at FROM items "+
			"WHERE kind = ? AND state = 'open' ORDER BY id", KindPermission)
	if err != nil {
		return nil, err
	}
	out := make([]ForwardedItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, ForwardedItem{
			Key:       textOfRow(row, "key"),
			SessionID: textOfRow(row, "session_id"),
			Tool:      textOfRow(row, "tool"),
			Input:     textOfRow(row, "input"),
			Truncated: row["truncated"] == int64(1),
			CreatedAt: textOfRow(row, "created_at"),
		})
	}
	return out, nil
}

// TouchPollsOfMachine marks every open permission item of a machine's sessions polled: its link
// relays its hooks' polls, so one long poll of the link is the proof those hooks live — SPEC
// 6.2.6's rule carried across the satellite protocol, without which the sweeper would retire a
// satellite's items seventy-five seconds after they grew.
func (d *DB) TouchPollsOfMachine(ctx context.Context, machine, at string) error {
	return d.st.Run(ctx,
		"UPDATE items SET last_poll_at = ? WHERE kind = ? AND state = 'open' "+
			"AND session_id IN (SELECT session_id FROM sessions WHERE machine = ?)",
		at, KindPermission, machine)
}

// PermissionItemByKey answers the permission item a key names, nil when no such item or the
// item is another kind: the key is what a decision handout carries, so the satellite finds its
// own row without ever trusting an id from the wire.
func (d *DB) PermissionItemByKey(ctx context.Context, key string, now time.Time) (*PermissionItem, error) {
	row, err := d.st.Get(ctx,
		"SELECT id, key, session_id, kind, summary, created_at, state, snoozed_until, "+permissionColumns+
			" FROM items WHERE key = ?", key)
	if err != nil || row == nil {
		return nil, err
	}
	if textOfRow(row, "kind") != KindPermission {
		return nil, nil
	}
	item := permissionOfRow(row)
	item.State = PermissionState(item, now)
	return &item, nil
}

// MarkPermissionRefused writes a satellite's refusal onto the hub's own row for the item, and
// answers whether a row moved. A row the owner's allow answered is rewritten to refused with
// the machine's reason, so the app shows why the allow never reached the terminal; a row still
// open is answered refused the same way, because the machine's answer is an answer. A row the
// terminal's own answer withdrew stays as it lays: that prompt needs no explaining.
func (d *DB) MarkPermissionRefused(ctx context.Context, id int64, reason, at string) (bool, error) {
	message := jsscalar.UTF16Slice(cleanSummary(reason), 0, messageLimit)
	if err := d.st.Run(ctx,
		"UPDATE items SET state = 'answered', decision = 'refused', message = ?, decided_at = ? "+
			"WHERE id = ? AND state IN ('open', 'answered')",
		message, at, id); err != nil {
		return false, err
	}
	return changesOf(ctx, d.st)
}

// SatRefusalBody is the payload a decision handout carries, as the satellite reads it.
type SatRefusalBody struct {
	Key       string            `json:"key"`
	SessionID string            `json:"session"`
	Decision  string            `json:"decision"`
	Message   string            `json:"message,omitempty"`
	Answers   map[string]string `json:"answers,omitempty"`
}

// SatMessageBody is the payload a message handout carries, as the satellite reads it.
type SatMessageBody struct {
	SessionID string `json:"session"`
	Text      string `json:"text"`
}

// EncodeSatPayload renders one handout payload.
func EncodeSatPayload(body any) (string, error) {
	raw, err := domain.MarshalCompactJSON(body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// DecodeSatPayload reads one handout payload back into its body.
func DecodeSatPayload(payload string, body any) error {
	return json.Unmarshal([]byte(payload), body)
}

// satHandoutOf reads one sat_queue row into its struct shape.
func satHandoutOf(row store.Row) SatHandout {
	return SatHandout{
		ID:        rowID(row),
		Kind:      textOfRow(row, "kind"),
		SessionID: textOfRow(row, "session_id"),
		ItemID:    intOfRow(row, "item_id"),
		MessageID: intOfRow(row, "message_id"),
		Payload:   textOfRow(row, "payload"),
	}
}

// nilZero answers nil for a zero id, so the row's column stays NULL.
func nilZero(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
