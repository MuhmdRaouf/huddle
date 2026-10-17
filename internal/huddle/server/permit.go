package server

// The permission flow's routes (SPEC 6.2): the PermissionRequest hook posts an item on the
// shared needs-you list and holds a long poll for a decision; the owner's app allows or denies
// it. Whichever answers first wins — the app's decision or the terminal's own — and the loser
// reads withdrawn, so the prompt is never answered twice. An allow passes the machine's
// switches (6.6) and a step-up (7.4); nothing here writes a permission rule that outlives the
// one prompt.

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// permitItemPath matches the permission flow's actions on one item: the hook's long poll and
// the owner's decision. The number is the shared list's row id.
var permitItemPath = regexp.MustCompile(`^/api/needs/([1-9][0-9]*)/(wait|decide)$`)

// waitMax is the longest one wait holds (SPEC 6.2.1: the hook re-polls).
const waitMax = 30 * time.Second

// permitSweepEvery is how often the sweeper withdraws the open permission items whose hook is
// gone, so the shared list and the sessions' needs-you states drop a dead prompt without
// waiting for the next read.
const permitSweepEvery = 30 * time.Second

// permitWaiters wakes the long polls holding one permission item: every waiter gets its own
// channel, closed when the item changes, so each re-reads it and answers.
type permitWaiters struct {
	mu      sync.Mutex
	waiters map[int64]map[chan struct{}]struct{}
}

func newPermitWaiters() *permitWaiters {
	return &permitWaiters{waiters: map[int64]map[chan struct{}]struct{}{}}
}

// arm registers one waiter for one item and answers its channel.
func (n *permitWaiters) arm(id int64) chan struct{} {
	wake := make(chan struct{})
	n.mu.Lock()
	defer n.mu.Unlock()
	set, ok := n.waiters[id]
	if !ok {
		set = map[chan struct{}]struct{}{}
		n.waiters[id] = set
	}
	set[wake] = struct{}{}
	return wake
}

// done drops one waiter that finished without a wake.
func (n *permitWaiters) done(id int64, wake chan struct{}) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if set, ok := n.waiters[id]; ok {
		delete(set, wake)
		if len(set) == 0 {
			delete(n.waiters, id)
		}
	}
}

// wake closes and drops every waiter of one item.
func (n *permitWaiters) wake(id int64) {
	n.mu.Lock()
	set := n.waiters[id]
	delete(n.waiters, id)
	n.mu.Unlock()
	for wake := range set {
		close(wake)
	}
}

// permitItemRoute answers /api/needs/<id>/(wait|decide).
func (s *server) permitItemRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) error {
	m := permitItemPath.FindStringSubmatch(path)
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return &channel.HuddleError{Status: 404, Msg: "no such item"}
	}
	if m[2] == "wait" {
		if r.Method != http.MethodGet {
			return &channel.HuddleError{Status: 405, Msg: "this endpoint answers GET"}
		}
		return s.permitWaitRoute(w, r, id)
	}
	if r.Method != http.MethodPost {
		return &channel.HuddleError{Status: 405, Msg: "this endpoint answers POST"}
	}
	return s.permitDecideRoute(w, r, id, me)
}

// permitCreateRoute takes the PermissionRequest hook's item (SPEC 6.2.1) and answers the id
// its poll holds. Any credential may ask — the asking session's own — because asking is what a
// session does when its terminal is prompted.
func (s *server) permitCreateRoute(w http.ResponseWriter, r *http.Request) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	toolInput := fieldOf(body, "tool_input")
	if textOf(fieldOf(body, "session_id")) == "" || textOf(fieldOf(body, "tool_name")) == "" ||
		toolInput == nil || toolInput.Kind() == jsfmt.KindNull {
		return &channel.HuddleError{Status: 400, Msg: "a permission item needs session_id, tool_name and tool_input"}
	}
	id, err := s.registry.CreatePermissionItem(r.Context(), textOf(fieldOf(body, "session_id")),
		textOf(fieldOf(body, "tool_name")), []byte(toolInput.EncodeJSON()), s.now())
	if err != nil {
		return err
	}
	s.emitPermissionItem(r.Context(), id)
	s.wakePushes()
	s.writeJSON(w, 200, struct {
		ID int64 `json:"id"`
	}{id}, nil)
	return nil
}

// permitDecideRoute records the app's decision (SPEC 6.2): allow or deny, once. An allow passes
// the machine's switches in their fixed order — a cut input that cannot be checked (6.2.4),
// step-up (6.2.5), remote permissions, the floor and the allow list (6.6) — and a deny passes
// nothing: refusing is always safe (7.4). Only the owner decides; a session never may, least
// of all its own permission.
func (s *server) permitDecideRoute(w http.ResponseWriter, r *http.Request, id int64, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	if !(me.Root || me.Browser != "") {
		return &channel.HuddleError{Status: 403, Msg: "only the owner may answer a permission item"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	decision := textOf(fieldOf(body, "decision"))
	if decision != "allow" && decision != "deny" {
		return &channel.HuddleError{Status: 400, Msg: "decision: allow or deny"}
	}
	ctx := r.Context()
	item, err := s.registry.PermissionItemByID(ctx, id, s.now())
	if err != nil {
		return err
	}
	if item == nil {
		return &channel.HuddleError{Status: 404, Msg: "no such item"}
	}
	if item.State != hub.StateOpen {
		return &channel.HuddleError{Status: 409, Msg: "already answered: the terminal or the app got there first"}
	}
	if decision == "allow" {
		if item.Truncated {
			return &channel.HuddleError{Status: 409, Msg: "the input was cut; allow it in the terminal"}
		}
		if s.stepUp.Required(r, me) {
			return &channel.HuddleError{Status: 401, Msg: "step-up required"}
		}
		cfg, err := home.LoadConfig(home.Home{Root: s.o.Home})
		if err != nil {
			return &channel.HuddleError{Status: 403, Msg: "the machine's remote switches cannot be read; allow it in the terminal"}
		}
		if !cfg.Remote.Permissions {
			return &channel.HuddleError{Status: 403, Msg: "remote permissions are off on this machine"}
		}
		if reason, ok := hub.RemoteAllow(cfg.Remote.Allow, item.Tool, item.Input); !ok {
			return &channel.HuddleError{Status: 403, Msg: reason}
		}
	}
	won, err := s.registry.DecidePermission(ctx, id, decision, textOf(fieldOf(body, "message")),
		jsfmt.ISOTime(s.now()), s.now())
	if err != nil {
		return err
	}
	if !won {
		return &channel.HuddleError{Status: 409, Msg: "already answered: the terminal or the app got there first"}
	}
	// the decision stands once won, so a row that cannot be written is answered as an error:
	// an unaudited allow or deny never passes silently (SPEC 7.7)
	actor := "owner"
	if me.Device != "" {
		actor = me.Device
	} else if me.Browser != "" {
		actor = me.Browser
	}
	if _, err := s.audit(ctx, actor, decision, strconv.FormatInt(id, 10), nil); err != nil {
		return err
	}
	if decision == "allow" {
		// the alert names the session and the tool, never the command or a path (SPEC 7.6's
		// rule, kept for every owner-facing notice)
		if _, err := s.raiseAlert(ctx, "alert.remote_allow", struct {
			Item    int64  `json:"item"`
			Session string `json:"session"`
			Tool    string `json:"tool"`
		}{id, item.SessionID, item.Tool}); err != nil {
			return err
		}
	}
	// a decision on a satellite's forwarded item travels to the machine (SPEC 6.7): its link
	// applies it there, under the machine's own switches
	s.offerDecision(ctx, *item, decision, textOf(fieldOf(body, "message")))
	return s.answerPermission(w, r, id)
}

// permitWaitRoute is the hook's long poll (SPEC 6.2.1): it holds until the item is decided,
// withdrawn or expired — or its own timeout passes, when it answers the still-open item and
// the hook re-polls. The poll itself is what keeps the item from reading silent (6.2.6), and
// the item's age can still end it mid-wait, so the wait ends with it.
func (s *server) permitWaitRoute(w http.ResponseWriter, r *http.Request, id int64) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	seconds := int64(waitMax.Seconds())
	if raw := r.URL.Query().Get("timeout"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return &channel.HuddleError{Status: 400, Msg: "timeout: seconds, at most 30"}
		}
		if n < 1 {
			n = 1
		}
		if time.Duration(n)*time.Second > waitMax {
			n = int64(waitMax.Seconds())
		}
		seconds = n
	}
	ctx := r.Context()
	item, err := s.registry.PermissionItemByID(ctx, id, s.now())
	if err != nil {
		return err
	}
	if item == nil {
		return &channel.HuddleError{Status: 404, Msg: "no such item"}
	}
	if item.State == hub.StateOpen {
		if err := s.registry.TouchPoll(ctx, id, jsfmt.ISOTime(s.now())); err != nil {
			return err
		}
		if hold := s.permitHold(*item, seconds); hold > 0 {
			wake := s.permits.arm(id)
			timer := time.NewTimer(hold)
			select {
			case <-wake:
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				s.permits.done(id, wake)
				return nil // the hook went away: nothing to answer
			}
			timer.Stop()
			s.permits.done(id, wake)
			item, err = s.registry.PermissionItemByID(ctx, id, s.now())
			if err != nil {
				return err
			}
			if item == nil {
				return &channel.HuddleError{Status: 404, Msg: "no such item"}
			}
		}
	}
	s.writeJSON(w, 200, item, nil)
	return nil
}

// permitHold answers how long one wait may hold: its own timeout, or the less of the item's
// age limit, whichever ends first.
func (s *server) permitHold(item hub.PermissionItem, seconds int64) time.Duration {
	hold := time.Duration(seconds) * time.Second
	if created, ok := jsfmt.ParseISO(item.CreatedAt); ok {
		if left := created.Add(hub.PermissionExpiresAfter).Sub(s.now()); left < hold {
			return left
		}
	}
	return hold
}

// answerPermission answers one item's row after a change, and wakes the pollers and streams
// that change first.
func (s *server) answerPermission(w http.ResponseWriter, r *http.Request, id int64) error {
	item, err := s.registry.PermissionItemByID(r.Context(), id, s.now())
	if err != nil {
		return err
	}
	if item == nil {
		return &channel.HuddleError{Status: 404, Msg: "no such item"}
	}
	s.itemPermissionChanged(r.Context(), *item)
	s.writeJSON(w, 200, item, nil)
	return nil
}

// itemPermissionChanged wakes the pollers holding the item, streams its new row, and restates
// its session's row (a decision moves it out of needs-you).
func (s *server) itemPermissionChanged(ctx context.Context, item hub.PermissionItem) {
	s.permits.wake(item.ID)
	s.emitPermissionItem(ctx, item.ID)
	s.emitRegistrySession(ctx, item.SessionID)
}

// emitPermissionItem fans one permission item's row out as a needs.item event: to every open
// registry stream, and to its session's channel's own stream. Best effort, like every emit.
func (s *server) emitPermissionItem(ctx context.Context, id int64) {
	if s.registry == nil {
		return
	}
	item, err := s.registry.PermissionItemByID(ctx, id, s.now())
	if err != nil || item == nil {
		return
	}
	encoded, err := domain.MarshalCompactJSON(struct {
		Type string              `json:"type"`
		Data *hub.PermissionItem `json:"data"`
	}{"needs.item", item})
	if err != nil {
		return
	}
	s.fanRegistryRow("data: " + string(encoded) + "\n\n")
	row, err := s.registry.SessionRow(ctx, item.SessionID, s.now())
	if err != nil || row == nil || row.Channel == "" {
		return
	}
	ch, err := s.hub.GetExisting(ctx, row.Channel)
	if err != nil {
		return
	}
	ch.Emit("needs.item", item)
}

// withdrawPermissions marks the session's open permission items withdrawn (SPEC 6.2.2: the
// terminal answered its own prompt, so the app's pending decision arrives too late) and wakes
// every poller: each answers with no decision and Claude Code keeps the terminal's answer.
func (s *server) withdrawPermissions(ctx context.Context, sessionID string) {
	if s.registry == nil || sessionID == "" {
		return
	}
	ids, err := s.registry.WithdrawPermissions(ctx, sessionID, jsfmt.ISOTime(s.now()))
	if err != nil {
		fmt.Fprintf(s.o.Stderr, "permit: withdraw: %v\n", err)
		return
	}
	for _, id := range ids {
		s.permits.wake(id)
		s.emitPermissionItem(ctx, id)
	}
	if len(ids) > 0 {
		s.emitRegistrySession(ctx, sessionID)
	}
}

// watchPermissions sweeps the open permission items whose hook is gone: the sweeper withdraws
// them so the shared list never waits on a dead prompt, and the sessions they held leave
// needs-you on the live streams. The wait and decide routes compute the same expiry exactly;
// this is the list's own housekeeping.
func (s *server) watchPermissions(ctx context.Context) {
	interval := time.Duration(s.o.PermitSweepMS) * time.Millisecond
	if interval <= 0 {
		interval = permitSweepEvery
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sessions, err := s.registry.ExpirePermissions(ctx, s.now())
			if err != nil {
				fmt.Fprintf(s.o.Stderr, "permit: sweep: %v\n", err)
				continue
			}
			for _, id := range sessions {
				s.emitRegistrySession(ctx, id)
			}
		}
	}
}
