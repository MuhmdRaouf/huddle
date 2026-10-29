package server

// Web Push: the needs-you items, a failing Anthropic path and the owner alerts reach
// the paired devices' subscriptions, each payload encrypted to that device's own keys. A payload
// carries who (the session and its machine), what kind, the tool name, the app address the
// notification opens and, on a needs-you push, the open needs-you count for the Home Screen
// badge — never the command, path, prompt or answer text, which stay in the registry's rows. An item pushes once at create and once more only on its five-minute
// escalation; a subscription the push service calls gone is dropped on the spot. While a hub tab
// on one device is visible and focused, the sweep sends nothing to the other devices.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/SherClockHolmes/webpush-go"
)

// The send's shape: a subscription the push service cannot deliver to dies at once, and the
// message itself outlives the server by no more than an item's own waiting time.
const (
	pushSweepEvery  = 30 * time.Second
	pushSendTimeout = 10 * time.Second
	pushTTL         = 300 // seconds, the five minutes an escalation may take to reach its item
	// how long one focus report keeps the other devices quiet; a focused tab reports every 30 s
	deskQuietEvery = 60 * time.Second
	// the contact the VAPID JWT's sub claim names; the library writes it as a mailto: URL
	pushSubscriber = "huddle-push@localhost"
)

// pushPayload is one needs-you item's push: who, what kind, the tool name and the
// open needs-you count the Home Screen badge shows. The summary, the input and every other
// field of the item's row stay off it — the payload crosses a push service outside this
// machine, and its plaintext is decrypted only by the devices.
type pushPayload struct {
	Item    int64  `json:"item"`
	Session string `json:"session"`
	Machine string `json:"machine"`
	Kind    string `json:"kind"`
	Tool    string `json:"tool,omitempty"`
	URL     string `json:"url"`
	Badge   int    `json:"badge"`
}

// sessionPushPayload is one session's needs-you entry push: who, that it waits, and
// the open needs-you count. No item rides it — an entry with an open item has the item's own
// push — and nothing of the session's words does.
type sessionPushPayload struct {
	Kind    string `json:"kind"` // "session"
	Session string `json:"session"`
	Machine string `json:"machine,omitempty"`
	URL     string `json:"url"`
	Badge   int    `json:"badge"`
}

// alertPayload is one owner alert's push: the alert's own name for the kind, who it names, and
// the tool when the alert carries one. An alert names no item row to decide on.
type alertPayload struct {
	Kind    string `json:"kind"`
	Session string `json:"session,omitempty"`
	Machine string `json:"machine"`
	Tool    string `json:"tool,omitempty"`
	URL     string `json:"url"`
}

// upstreamPayload is a failing Anthropic path's push: the kind, the machine whose router saw it,
// and radar's detail, which is the failure code and its fix hint and nothing of a request.
type upstreamPayload struct {
	Kind    string `json:"kind"` // "upstream_failing"
	Machine string `json:"machine"`
	Detail  string `json:"detail"`
	URL     string `json:"url"`
}

// The app addresses a notification opens: the session's screen (at the item when there is
// one), Radar's alerts for a failing path, and the Now list for an owner alert.
const (
	nowURL          = "/#/now"
	radarAlertsURL  = "/#/radar/alerts"
	upstreamFailing = "upstream_failing"
)

// sessionURL is the app address of one session's screen.
func sessionURL(sessionID string) string {
	return nowURL + "/" + url.PathEscape(sessionID)
}

// needsPushPayload builds one needs-you item's push payload; badge is the open needs-you count.
func needsPushPayload(item hub.Item, machine string, badge int) pushPayload {
	return pushPayload{
		Item:    item.ID,
		Session: item.SessionID,
		Machine: machine,
		Kind:    item.Kind,
		Tool:    item.Tool,
		URL:     sessionURL(item.SessionID) + "?item=" + strconv.FormatInt(item.ID, 10),
		Badge:   badge,
	}
}

// sessionPushOf builds one session's needs-you entry push payload; badge is the open needs-you
// count.
func sessionPushOf(row hub.SessionRow, badge int) sessionPushPayload {
	return sessionPushPayload{
		Kind: "session", Session: row.SessionID, Machine: row.Machine, URL: sessionURL(row.SessionID),
		Badge: badge,
	}
}

// upstreamPushOf builds a failing path's push payload from radar's upstream alert.
func upstreamPushOf(alert radarAlert, machine string) upstreamPayload {
	return upstreamPayload{Kind: upstreamFailing, Machine: machine, Detail: alert.Detail, URL: radarAlertsURL}
}

// alertPushPayload builds one alert's push payload from the alert's own body.
func alertPushPayload(event AlertEvent, machine string) alertPayload {
	payload := alertPayload{Kind: event.Type, Machine: machine, URL: nowURL}
	if session := fieldOf(event.Body, "session"); session != nil {
		payload.Session = textOf(session)
	}
	if tool := fieldOf(event.Body, "tool"); tool != nil {
		payload.Tool = textOf(tool)
	}
	return payload
}

// watchPushes runs the push sweep on its interval and on every wake from a fresh item: one pass
// carries the first pushes and the due escalations alike.
func (s *server) watchPushes(ctx context.Context) {
	interval := time.Duration(s.o.PushSweepMS) * time.Millisecond
	if interval <= 0 {
		interval = pushSweepEvery
	}
	s.health.Register("hub.pushes", domain.HealthLoop, interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.pushWake:
		}
		if err := s.sweepPushes(ctx); err != nil {
			s.health.Fail("hub.pushes", err)
			continue
		}
		s.health.OK("hub.pushes")
	}
}

// wakePushes nudges the sweep; a nudge that lands while the sweep runs is dropped, and the
// ticker is the floor.
func (s *server) wakePushes() {
	if s.pushWake == nil {
		return
	}
	select {
	case s.pushWake <- struct{}{}:
	default:
	}
}

// sweepPushes carries one pass: every item waiting for its first push, then every escalation
// due, then the sessions' own needs-you entries. It answers the first step error the pass met,
// or nil when the pass ran clean; a server with no registry or no keys sweeps nothing and
// answers nil. An item with no device to hear it waits unmarked, so the next device paired
// still hears of it once; one the pass did carry is marked, whatever the push service said, so
// an item never pushes twice because a service answered slowly.
func (s *server) sweepPushes(ctx context.Context) error {
	if s.registry == nil || s.vapid == nil {
		return nil
	}
	now := s.now()
	subs, err := s.registry.PushSubscriptions(ctx)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: subscriptions: %v\n", err)
		return err
	}
	// the badge every needs-you push carries: the sessions waiting on the owner, the Now tab's count
	waiting, err := s.registry.Sessions(ctx, hub.SessionsFilter{State: hub.StateNeedsYou}, now)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: needs-you count: %v\n", err)
		return err
	}
	badge := len(waiting)
	fresh, err := s.registry.FreshPushItems(ctx, now)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: fresh items: %v\n", err)
		return err
	}
	// the item's first push is its session's entry push: the session pass sends no second one
	// for the same entry
	s.pushItems(ctx, now, subs, badge, fresh, func(ctx context.Context, item hub.Item, at string) error {
		if err := s.registry.MarkItemPushed(ctx, item.ID, at); err != nil {
			return err
		}
		return s.registry.MarkSessionPushed(ctx, item.SessionID, at)
	})
	due, err := s.registry.EscalatePushItems(ctx, now)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: escalations: %v\n", err)
		return err
	}
	s.pushItems(ctx, now, subs, badge, due, func(ctx context.Context, item hub.Item, at string) error {
		return s.registry.MarkItemEscalated(ctx, item.ID, at)
	})
	s.pushSessionEntries(ctx, now, subs, badge)
	s.pushUpstream(ctx, now, subs)
	return nil
}

// deskTab names one hub tab that reports its focus: the paired device it runs on (empty for the
// owner's own browser) and the tab's own id.
type deskTab struct {
	device string
	tab    string
}

// deskQuiet answers how long one focus report keeps the other devices quiet.
func (s *server) deskQuiet() time.Duration {
	if s.o.DeskQuietMS > 0 {
		return time.Duration(s.o.DeskQuietMS) * time.Millisecond
	}
	return deskQuietEvery
}

// noteFocus records one tab's report: focused holds it for 60 s, hidden or blurred drops it at
// once. Reports older than the quiet are dropped on the way.
func (s *server) noteFocus(device, tab string, focused bool, at time.Time) {
	s.focusMu.Lock()
	defer s.focusMu.Unlock()
	if s.focus == nil {
		s.focus = make(map[deskTab]time.Time)
	}
	for key, seen := range s.focus {
		if at.Sub(seen) > s.deskQuiet() {
			delete(s.focus, key)
		}
	}
	key := deskTab{device: device, tab: tab}
	if focused {
		s.focus[key] = at
		return
	}
	delete(s.focus, key)
}

// quietFor says whether a device stays quiet at now: a tab on another device reported itself
// visible and focused within the last 60 s. A device's own tab never quiets it.
func (s *server) quietFor(device string, now time.Time) bool {
	s.focusMu.Lock()
	defer s.focusMu.Unlock()
	for key, seen := range s.focus {
		if key.device != device && now.Sub(seen) <= s.deskQuiet() {
			return true
		}
	}
	return false
}

// radarAnswerer is radar's in-process API as the push sweep reads it: the alerts radar raises.
type radarAnswerer interface {
	RadarAnswer(ctx context.Context, path string) ([]byte, bool)
}

// radarAlert is the slice of one radar alert the sweep reads.
type radarAlert struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// pushUpstream carries a failing Anthropic path to the devices that chose needs-and-alerts: a
// device on needs-you only hears the items and nothing else. Radar's upstream alert names one
// failure by its id, and each id pushes once. A path that fails while no device hears it stays
// unmarked, so the next device that chooses alerts hears of it.
func (s *server) pushUpstream(ctx context.Context, now time.Time, subs []hub.PushSubscription) {
	s.radarMu.Lock()
	radar := s.radarAlerts
	s.radarMu.Unlock()
	if radar == nil {
		return
	}
	// only a NotifyAll device hears an upstream alert, and the fetch rebuilds radar's whole
	// alert set: with no listener the pass stops here, and a failure seen now stays unmarked
	// for the next pass that has one.
	listener := false
	for _, sub := range subs {
		if sub.Notify == hub.NotifyAll {
			listener = true
			break
		}
	}
	if !listener {
		return
	}
	raw, ok := radar.RadarAnswer(ctx, "/api/alerts")
	if !ok {
		return
	}
	var answer struct {
		Alerts []radarAlert `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		fmt.Fprintf(s.o.Stderr, "push: radar alerts: %v\n", err)
		return
	}
	for _, alert := range answer.Alerts {
		if alert.Kind != "upstream" || alert.ID == s.upstreamPushed {
			continue
		}
		payload, err := json.Marshal(upstreamPushOf(alert, s.thisMachine(ctx)))
		if err != nil {
			return
		}
		hearing := 0
		for _, sub := range subs {
			if sub.Notify == hub.NotifyAll {
				if !s.quietFor(sub.Device, now) {
					s.sendPush(ctx, sub, payload, webpush.UrgencyHigh)
				}
				hearing++
			}
		}
		if hearing > 0 {
			s.upstreamPushed = alert.ID
		}
	}
}

// pushSessionEntries carries the pass's session halves: one push for every session whose state
// at now is needs-you with no push sent for this entry, then the marks of the sessions that
// left the state, so a later entry pushes again. A session with no device to hear it waits
// unmarked, the same rule the items keep.
func (s *server) pushSessionEntries(ctx context.Context, now time.Time, subs []hub.PushSubscription, badge int) {
	hearing := make([]hub.PushSubscription, 0, len(subs))
	for _, sub := range subs {
		if sub.Notify != hub.NotifyOff {
			hearing = append(hearing, sub)
		}
	}
	if len(hearing) == 0 {
		return
	}
	entries, err := s.registry.FreshPushSessions(ctx, now)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: fresh sessions: %v\n", err)
		return
	}
	for _, row := range entries {
		payload, err := json.Marshal(sessionPushOf(row, badge))
		if err != nil {
			continue
		}
		for _, sub := range hearing {
			if !s.quietFor(sub.Device, now) {
				s.sendPush(ctx, sub, payload, webpush.UrgencyHigh)
			}
		}
		if err := s.registry.MarkSessionPushed(ctx, row.SessionID, jsscalar.ISOTime(now)); err != nil {
			fmt.Fprintf(s.o.Stderr, "push: mark session %s: %v\n", row.SessionID, err)
		}
	}
	left, err := s.registry.LeftNeedsYou(ctx, now)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: sessions left: %v\n", err)
		return
	}
	for _, id := range left {
		if err := s.registry.ClearSessionPush(ctx, id); err != nil {
			fmt.Fprintf(s.o.Stderr, "push: clear session %s: %v\n", id, err)
		}
	}
}

// hearsItem says whether one subscription is woken for one item: a device on off hears nothing,
// and a finished turn reaches only a device that turned its own finished-turn choice on.
func hearsItem(sub hub.PushSubscription, item hub.Item) bool {
	if sub.Notify == hub.NotifyOff {
		return false
	}
	return item.Kind != hub.KindTurn || sub.Turn
}

// pushItems carries one pass's items to every device that chose to hear them, each with the open
// needs-you count, then marks each carried item once. A finished turn no awake device chose
// still takes its mark, so the session pass does not wake those devices for it instead; so does
// an item a focused desk tab kept quiet, which never pushes late.
func (s *server) pushItems(ctx context.Context, now time.Time, subs []hub.PushSubscription, badge int,
	items []hub.Item, mark func(context.Context, hub.Item, string) error) {
	if len(items) == 0 {
		return
	}
	hearing := make([]hub.PushSubscription, 0, len(subs))
	for _, sub := range subs {
		if sub.Notify != hub.NotifyOff {
			hearing = append(hearing, sub)
		}
	}
	if len(hearing) == 0 {
		return
	}
	for _, item := range items {
		payload, err := json.Marshal(needsPushPayload(item, s.machineOf(ctx, item.SessionID, now), badge))
		if err != nil {
			continue
		}
		for _, sub := range hearing {
			if hearsItem(sub, item) && !s.quietFor(sub.Device, now) {
				s.sendPush(ctx, sub, payload, webpush.UrgencyHigh)
			}
		}
		if err := mark(ctx, item, jsscalar.ISOTime(now)); err != nil {
			fmt.Fprintf(s.o.Stderr, "push: mark item %d: %v\n", item.ID, err)
		}
	}
}

// pushAlertEvent carries one raised alert to the devices that chose needs-and-alerts.
func (s *server) pushAlertEvent(ctx context.Context, event AlertEvent) {
	if s.registry == nil || s.vapid == nil {
		return
	}
	subs, err := s.registry.PushSubscriptions(ctx)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: subscriptions: %v\n", err)
		return
	}
	payload, err := json.Marshal(alertPushPayload(event, s.thisMachine(ctx)))
	if err != nil {
		return
	}
	for _, sub := range subs {
		if sub.Notify == hub.NotifyAll {
			s.sendPush(ctx, sub, payload, webpush.UrgencyNormal)
		}
	}
}

// sendPush encrypts one payload to one subscription and carries it to its endpoint. A
// subscription the push service calls gone (404 or 410) is dropped here, so the next sweep
// stops carrying it; any other failure keeps the subscription and costs a log line.
func (s *server) sendPush(ctx context.Context, sub hub.PushSubscription, payload []byte, urgency webpush.Urgency) {
	keys, err := s.vapid.Keys(ctx)
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "push: vapid keys: %v\n", err)
		return
	}
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{Auth: sub.Auth, P256dh: sub.P256dh},
	}, &webpush.Options{
		VAPIDPublicKey: keys.Public, VAPIDPrivateKey: keys.Private,
		Subscriber: pushSubscriber, TTL: pushTTL, Urgency: urgency,
		HTTPClient: s.pushHTTP,
	})
	if err != nil {
		// the endpoint's service may be down; the subscription stays for the next sweep
		fmt.Fprintf(s.o.Stderr, "push: send: %v\n", err)
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		if _, err := s.registry.RemovePushSubscription(ctx, sub.Endpoint); err != nil {
			fmt.Fprintf(s.o.Stderr, "push: drop %s: %v\n", sub.Device, err)
		}
	case resp.StatusCode >= 300:
		fmt.Fprintf(s.o.Stderr, "push: send to %s answered %d\n", sub.Device, resp.StatusCode)
	}
}

// machineOf answers the machine a session runs on, the who a payload names; a session that
// never reported names none.
func (s *server) machineOf(ctx context.Context, sessionID string, now time.Time) string {
	row, err := s.registry.SessionRow(ctx, sessionID, now)
	if err != nil || row == nil {
		return ""
	}
	return row.Machine
}

// thisMachine answers the machine this server runs on, the who an alert names.
func (s *server) thisMachine(ctx context.Context) string {
	cfg, err := home.LoadConfig(home.Home{Root: s.o.Home})
	if err != nil {
		return ""
	}
	return cfg.Machine
}

// ── the routes ─────────────────────────────────────────────────────────────────────────────────────────────────────

// deviceOnly holds a route to paired devices: a subscription and its choice belong to the
// browser whose passkey enrolled the device, and nothing else carries one.
func deviceOnly(me auth.Who) error {
	if me.Device == "" {
		return &channel.HuddleError{Status: 403, Msg: "only a paired device holds a push subscription"}
	}
	return nil
}

// pushUnavailable is what a server without its registry or its keys answers: nothing here can
// be promised.
func pushUnavailable() error {
	return &channel.HuddleError{Status: 503, Msg: "push is unavailable"}
}

// pushKeyRoute answers the VAPID public key the browser subscribes with: the P-256 point, 65
// bytes, base64url — the same pair every push send signs with.
func (s *server) pushKeyRoute(w http.ResponseWriter, r *http.Request) error {
	if s.vapid == nil {
		return pushUnavailable()
	}
	keys, err := s.vapid.Keys(r.Context())
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		PublicKey string `json:"public_key"`
	}{keys.Public}, nil)
	return nil
}

// pushSubscribeRoute stores the browser's subscription for the device at the cookie:
// the endpoint and the keys it encrypted for. A re-subscribe replaces the row the endpoint
// holds — the push service's own name for one subscription.
func (s *server) pushSubscribeRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if err := deviceOnly(me); err != nil {
		return err
	}
	if s.registry == nil {
		return pushUnavailable()
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	endpoint := textOf(fieldOf(body, "endpoint"))
	keys := fieldOf(body, "keys")
	p256dh, authSecret := "", ""
	if keys != nil {
		p256dh = textOf(fieldOf(keys, "p256dh"))
		authSecret = textOf(fieldOf(keys, "auth"))
	}
	if endpoint == "" || p256dh == "" || authSecret == "" {
		return &channel.HuddleError{Status: 400, Msg: "a subscription needs an endpoint and its p256dh and auth keys"}
	}
	if err := s.registry.SavePushSubscription(r.Context(), hub.PushSubscription{
		Device: me.Device, Endpoint: endpoint, P256dh: p256dh, Auth: authSecret,
	}, s.now().UnixMilli()); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// pushUnsubscribeRoute ends the device's own subscription; an endpoint that is nobody's here —
// already gone, or another device's — answers 404, never a touch.
func (s *server) pushUnsubscribeRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if err := deviceOnly(me); err != nil {
		return err
	}
	if s.registry == nil {
		return pushUnavailable()
	}
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		return &channel.HuddleError{Status: 400, Msg: "endpoint: the subscription's endpoint"}
	}
	subs, err := s.registry.PushSubscriptions(r.Context())
	if err != nil {
		return err
	}
	ours := false
	for _, sub := range subs {
		if sub.Endpoint == endpoint {
			ours = sub.Device == me.Device
			break
		}
	}
	if !ours {
		return &channel.HuddleError{Status: 404, Msg: "no such subscription"}
	}
	if _, err := s.registry.RemovePushSubscription(r.Context(), endpoint); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// pushNotifyRoute answers the device's own notification choices: what wakes it, and whether a
// finished turn does.
func (s *server) pushNotifyRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if err := deviceOnly(me); err != nil {
		return err
	}
	if s.registry == nil {
		return pushUnavailable()
	}
	notify, _, err := s.registry.DeviceNotify(r.Context(), me.Device)
	if err != nil {
		return err
	}
	if notify == "" {
		notify = hub.NotifyNeeds
	}
	turn, err := s.registry.DeviceTurn(r.Context(), me.Device)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Notify string `json:"notify"`
		Turn   bool   `json:"turn"`
	}{notify, turn}, nil)
	return nil
}

// pushSetNotifyRoute records the device's own notification choices, either or both: notify is
// needs-you only, needs-you and alerts, or off; turn is true or false.
func (s *server) pushSetNotifyRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if err := deviceOnly(me); err != nil {
		return err
	}
	if s.registry == nil {
		return pushUnavailable()
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	notifyField, turnField := fieldOf(body, "notify"), fieldOf(body, "turn")
	if notifyField == nil && turnField == nil {
		return &channel.HuddleError{Status: 400, Msg: "notify or turn: the choice to record"}
	}
	if turnField != nil && turnField.Kind() != jsfmt.KindBool {
		return &channel.HuddleError{Status: 400, Msg: "turn: true or false"}
	}
	if notifyField != nil {
		if err := s.registry.SetDeviceNotify(r.Context(), me.Device, textOf(notifyField)); err != nil {
			var unknown *hub.UnknownNotifyError
			if errors.As(err, &unknown) {
				return &channel.HuddleError{Status: 400, Msg: unknown.Error()}
			}
			return err
		}
	}
	if turnField != nil {
		if err := s.registry.SetDeviceTurn(r.Context(), me.Device, turnField.Boolean()); err != nil {
			return err
		}
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// pushFocusRoute records one hub tab's report (owner browsers and paired devices): its id, and
// whether it is visible and focused. A focused tab keeps every other device's pushes quiet.
func (s *server) pushFocusRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !me.Root {
		return &channel.HuddleError{Status: 403, Msg: "only the owner's browsers report their focus"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	tab, focused := textOf(fieldOf(body, "tab")), fieldOf(body, "focused")
	if tab == "" || len(tab) > 64 {
		return &channel.HuddleError{Status: 400, Msg: "tab: the reporting tab's id, 1 to 64 characters"}
	}
	if focused == nil || focused.Kind() != jsfmt.KindBool {
		return &channel.HuddleError{Status: 400, Msg: "focused: true or false"}
	}
	s.noteFocus(me.Device, tab, focused.Boolean(), s.now())
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}
