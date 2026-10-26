package hub

// The Web Push storage (SPEC 7.6): one subscription per paired device's endpoint, the device's
// own notification choices beside it, and the items' push bookkeeping — one push at create, one
// escalation at five minutes, nothing once decided. A device's subscriptions and its choice die
// with the device (the foreign keys cascade), and a dead subscription is dropped the moment the
// push service calls it gone.

import (
	"context"
	"fmt"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// PushEscalatesAfter is how long an item may stay open before its one escalation push goes out.
const PushEscalatesAfter = 5 * time.Minute

// The notification choices a device makes (SPEC 7.6, Settings › Notifications): needs-you items
// only, needs-you items and alerts, or nothing at all.
const (
	NotifyNeeds = "needs"
	NotifyAll   = "all"
	NotifyOff   = "off"
)

// PushSubscription is one device's Web Push subscription as the sender holds it: the endpoint
// the push service POSTs to, the keys the payload is encrypted to, and the device's choices
// riding beside it: Notify for the asks and alerts, Turn for the finished turns. AddedMS names
// the moment it was stored.
type PushSubscription struct {
	Device   string
	Endpoint string
	P256dh   string
	Auth     string
	Notify   string
	Turn     bool
	AddedMS  int64
}

// UnknownNotifyError names a notification choice that is none of the three.
type UnknownNotifyError struct{ Value string }

func (e *UnknownNotifyError) Error() string {
	return fmt.Sprintf("notify: %q, %q or %q", NotifyNeeds, NotifyAll, NotifyOff)
}

// knownNotify says whether s is a choice a device may make.
func knownNotify(s string) bool {
	return s == NotifyNeeds || s == NotifyAll || s == NotifyOff
}

// SavePushSubscription stores one device's subscription under its endpoint: the endpoint is the
// push service's own name for one subscription, so a re-subscribe — the same browser back with
// fresh keys, or the subscription now held by another device — replaces the row wholesale. The
// device must be paired: the row hangs off its name.
func (d *DB) SavePushSubscription(ctx context.Context, sub PushSubscription, addedMS int64) error {
	return d.st.Run(ctx,
		"INSERT INTO push_subscriptions (endpoint, device, p256dh, auth, added) VALUES (?, ?, ?, ?, ?) "+
			"ON CONFLICT(endpoint) DO UPDATE SET device = excluded.device, p256dh = excluded.p256dh, "+
			"auth = excluded.auth, added = excluded.added",
		sub.Endpoint, sub.Device, sub.P256dh, sub.Auth, addedMS)
}

// PushSubscriptions answers every stored subscription, oldest first, each with its device's
// notification choices riding on it.
func (d *DB) PushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := d.st.All(ctx,
		"SELECT s.endpoint, s.device, s.p256dh, s.auth, s.added, d.notify, d.notify_turn "+
			"FROM push_subscriptions s JOIN devices d ON d.name = s.device "+
			"ORDER BY s.added, s.endpoint")
	if err != nil {
		return nil, err
	}
	out := make([]PushSubscription, 0, len(rows))
	for _, row := range rows {
		added, _ := row["added"].(int64)
		out = append(out, PushSubscription{
			Endpoint: textOfRow(row, "endpoint"),
			Device:   textOfRow(row, "device"),
			P256dh:   textOfRow(row, "p256dh"),
			Auth:     textOfRow(row, "auth"),
			Notify:   textOfRow(row, "notify"),
			Turn:     row["notify_turn"] == int64(1),
			AddedMS:  added,
		})
	}
	return out, nil
}

// SetDeviceNotify records one device's notification choice (Settings › Notifications).
func (d *DB) SetDeviceNotify(ctx context.Context, device, notify string) error {
	if !knownNotify(notify) {
		return &UnknownNotifyError{Value: notify}
	}
	return d.st.Run(ctx, "UPDATE devices SET notify = ? WHERE name = ?", notify, device)
}

// DeviceNotify answers one device's notification choice, found false for a device that is no
// longer there to hold one.
func (d *DB) DeviceNotify(ctx context.Context, device string) (string, bool, error) {
	row, err := d.st.Get(ctx, "SELECT notify FROM devices WHERE name = ?", device)
	if err != nil || row == nil {
		return "", false, err
	}
	return textOfRow(row, "notify"), true, nil
}

// SetDeviceTurn records whether one device is woken for a finished turn (Settings ›
// Notifications).
func (d *DB) SetDeviceTurn(ctx context.Context, device string, on bool) error {
	flag := 0
	if on {
		flag = 1
	}
	return d.st.Run(ctx, "UPDATE devices SET notify_turn = ? WHERE name = ?", flag, device)
}

// DeviceTurn answers whether one device is woken for a finished turn; a device that is no longer
// there answers off.
func (d *DB) DeviceTurn(ctx context.Context, device string) (bool, error) {
	row, err := d.st.Get(ctx, "SELECT notify_turn FROM devices WHERE name = ?", device)
	if err != nil || row == nil {
		return false, err
	}
	return row["notify_turn"] == int64(1), nil
}

// RemovePushSubscription drops one endpoint's row and answers whether this call held it: the
// push service's 404 or 410 names a subscription the browser has already lost, and keeping it
// would have every push carry a dead address.
func (d *DB) RemovePushSubscription(ctx context.Context, endpoint string) (bool, error) {
	return d.removeSubscriptionWhere(ctx, "endpoint = ?", endpoint)
}

// removeSubscriptionWhere runs one delete and answers whether it held a row, the way the
// pairing spends and the device removals count.
func (d *DB) removeSubscriptionWhere(ctx context.Context, where string, args ...any) (bool, error) {
	removed := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM push_subscriptions WHERE "+where, args...); err != nil {
			return err
		}
		held, err := changesOf(ctx, q)
		if err != nil {
			return err
		}
		removed = held
		return nil
	})
	return removed, err
}

// FreshPushItems answers the open items that wait for their first push at now, oldest first:
// nothing behind a snooze, nothing already pushed, and no permission item whose prompt is dead
// — the same two limits the sweeper withdraws by.
func (d *DB) FreshPushItems(ctx context.Context, now time.Time) ([]Item, error) {
	return d.pushItems(ctx, now,
		// a permission item whose prompt has expired or gone silent pushes nobody: the waiting
		// is over even before the sweeper's pass withdraws the row
		"AND (kind != ? OR (created_at >= ? AND COALESCE(last_poll_at, created_at) >= ?))",
		[]any{KindPermission, jsscalar.ISOTime(now.Add(-PermissionExpiresAfter)),
			jsscalar.ISOTime(now.Add(-PermissionSilentPollAfter))},
		"pushed_at IS NULL")
}

// EscalatePushItems answers the open items whose one escalation is due at now, oldest first:
// pushed once, never escalated, still open past PushEscalatesAfter. A dead prompt never gets
// here — the permission sweeper withdraws it long before the five minutes pass — so still open
// is the row's own state, the same rule the escalation's push reads.
func (d *DB) EscalatePushItems(ctx context.Context, now time.Time) ([]Item, error) {
	return d.pushItems(ctx, now, "", nil,
		"pushed_at IS NOT NULL AND escalated_at IS NULL AND created_at <= ?",
		jsscalar.ISOTime(now.Add(-PushEscalatesAfter)))
}

// pushItems answers the open items one push pass takes at now: open, past any snooze, the
// pass's own waiting clause, and the marks the pass names.
func (d *DB) pushItems(ctx context.Context, now time.Time,
	expiry string, expiryArgs []any, marks string, markArgs ...any) ([]Item, error) {
	query := "SELECT id, session_id, kind, summary, created_at, snoozed_until, tool, input, truncated, decision, message " +
		"FROM items WHERE state = 'open' " +
		"AND (snoozed_until IS NULL OR snoozed_until <= ?) " + expiry +
		" AND " + marks + " ORDER BY id"
	params := append([]any{jsscalar.ISOTime(now)}, expiryArgs...)
	params = append(params, markArgs...)
	rows, err := d.st.All(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		out = append(out, itemOfRow(row))
	}
	return out, nil
}

// MarkItemPushed records that the item's one push went out at at.
func (d *DB) MarkItemPushed(ctx context.Context, id int64, at string) error {
	return d.st.Run(ctx, "UPDATE items SET pushed_at = ? WHERE id = ?", at, id)
}

// MarkItemEscalated records that the item's one escalation went out at at.
func (d *DB) MarkItemEscalated(ctx context.Context, id int64, at string) error {
	return d.st.Run(ctx, "UPDATE items SET escalated_at = ? WHERE id = ?", at, id)
}

// FreshPushSessions answers the live sessions whose state at now is needs-you with no push sent
// for this entry, oldest activity first: the rows the session pass carries. An entry whose
// open item pushed already carries its mark, so it waits here no more than the item pass takes
// to set it.
func (d *DB) FreshPushSessions(ctx context.Context, now time.Time) ([]SessionRow, error) {
	rows, err := d.st.All(ctx,
		"SELECT "+sessionColumns+" FROM sessions "+
			"WHERE state_pushed_at IS NULL AND ended_at IS NULL ORDER BY last_event_at, session_id")
	if err != nil {
		return nil, err
	}
	waiting, err := d.OpenItemSessions(ctx, now)
	if err != nil {
		return nil, err
	}
	out := []SessionRow{}
	for _, row := range rows {
		id := textOfRow(row, "session_id")
		s := d.rowOfSession(ctx, row, now, waiting[id], d.asksOf(ctx, id, textOfRow(row, "last_event")))
		if s.State == StateNeedsYou {
			out = append(out, s)
		}
	}
	return out, nil
}

// MarkSessionPushed records that the session's entry push went out at at.
func (d *DB) MarkSessionPushed(ctx context.Context, session, at string) error {
	return d.st.Run(ctx, "UPDATE sessions SET state_pushed_at = ? WHERE session_id = ?", at, session)
}

// LeftNeedsYou answers the session ids whose state at now is no longer needs-you but whose
// entry push still stands: the marks the pass clears, so a later entry pushes again.
func (d *DB) LeftNeedsYou(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := d.st.All(ctx,
		"SELECT "+sessionColumns+" FROM sessions WHERE state_pushed_at IS NOT NULL")
	if err != nil {
		return nil, err
	}
	waiting, err := d.OpenItemSessions(ctx, now)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, row := range rows {
		id := textOfRow(row, "session_id")
		s := d.rowOfSession(ctx, row, now, waiting[id], d.asksOf(ctx, id, textOfRow(row, "last_event")))
		if s.State != StateNeedsYou {
			out = append(out, id)
		}
	}
	return out, nil
}

// ClearSessionPush drops one session's entry mark.
func (d *DB) ClearSessionPush(ctx context.Context, session string) error {
	return d.st.Run(ctx, "UPDATE sessions SET state_pushed_at = NULL WHERE session_id = ?", session)
}

// itemOfRow reads one item row into its served shape; the state is what is stored, for the
// reads that compute a fresh one over it.
func itemOfRow(row store.Row) Item {
	return Item{
		ID:           rowID(row),
		SessionID:    textOfRow(row, "session_id"),
		Kind:         textOfRow(row, "kind"),
		Summary:      textOfRow(row, "summary"),
		CreatedAt:    textOfRow(row, "created_at"),
		State:        textOfRow(row, "state"),
		SnoozedUntil: textOfRow(row, "snoozed_until"),
		Tool:         textOfRow(row, "tool"),
		Input:        textOfRow(row, "input"),
		Truncated:    row["truncated"] == int64(1),
		Decision:     textOfRow(row, "decision"),
		Message:      textOfRow(row, "message"),
	}
}
