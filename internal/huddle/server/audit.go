package server

// The audit log's server face (SPEC 7.7): the one-time link's sign-in appends its row through
// hub.Record — beside the pairing, sign-in and removal rows pairing writes — while the owner
// alerts fan out in-process for the push sender and stay stored with a read state. A detail
// passes through the run's redaction first: the log is append-only and serves the owner's
// devices, so no credential, code or challenge of this run may land in it.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// errNoRegistry names a registry that cannot take rows: the action that wanted one is refused,
// because an unaudited security event never happens.
var errNoRegistry = errors.New("the registry's database is unavailable")

// auditLimitMax is the most rows one audit read may answer.
const auditLimitMax = 500

// kanbanAuditDetail is one kanban flow row's detail: whether the board took the
// write the row names.
type kanbanAuditDetail struct {
	OK bool `json:"ok"`
}

// AlertEvent is one raised owner alert as it fans out in-process: Type is the alert's own name
// (alert.device_paired, alert.remote_allow, …), the same string the alerts table stores as its
// kind. The body arrives already redacted.
type AlertEvent struct {
	Type string       `json:"type"`
	ID   int64        `json:"id"`
	At   string       `json:"at"`
	Body *jsfmt.Value `json:"json"`
}

// alertFanout is the in-process alert bus: every raised alert reaches every subscriber at once,
// in the order they were raised.
type alertFanout struct {
	mu   sync.Mutex
	subs map[*alertListener]struct{}
}

// alertListener is one subscribed listener, removable by its pointer.
type alertListener struct{ fn func(AlertEvent) }

func newAlertFanout() *alertFanout {
	return &alertFanout{subs: map[*alertListener]struct{}{}}
}

// subscribe adds one listener; the answer removes it.
func (f *alertFanout) subscribe(fn func(AlertEvent)) func() {
	l := &alertListener{fn: fn}
	f.mu.Lock()
	f.subs[l] = struct{}{}
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.subs, l)
		f.mu.Unlock()
	}
}

// emit reaches every listener; one that panics costs nothing.
func (f *alertFanout) emit(e AlertEvent) {
	f.mu.Lock()
	subs := make([]*alertListener, 0, len(f.subs))
	for l := range f.subs {
		subs = append(subs, l)
	}
	f.mu.Unlock()
	for _, l := range subs {
		func() {
			defer func() { _ = recover() }()
			l.fn(e)
		}()
	}
}

// audit appends one action to the audit log (the row's moment is this server's clock) and
// answers its row id. The detail is stripped of this run's secrets first; one that cannot be
// encoded is refused, never stored half-cleaned.
func (s *server) audit(ctx context.Context, actor, action, target string, detail any) (int64, error) {
	if s.registry == nil {
		return 0, errNoRegistry
	}
	redacted, err := s.redactDetail(detail)
	if err != nil {
		return 0, err
	}
	return s.registry.Record(ctx, s.now().UnixMilli(), actor, action, target, redacted)
}

// auditSignin records one one-time-link sign-in: the actor is the member whose link signed the
// browser in, the owner for the root's own link, and the target is the dashboard it entered.
// Nothing but the fact is written — no code, no credential. A sign-in whose row cannot be
// written fails, and the caller refuses the sign-in.
func (s *server) auditSignin(ctx context.Context, cred string) error {
	actor := "owner"
	if who, ok := s.auth.Who(cred); ok && who.Browser != "" {
		actor = who.Browser
	}
	_, err := s.audit(ctx, actor, "signin", "dashboard", nil)
	return err
}

// raiseAlert stores one owner alert, unread, and fans it out for the push sender. The kinds are
// the pushed ones (SPEC 7.7): a device paired, a satellite added, a rule added or changed, a
// remote allow.
func (s *server) raiseAlert(ctx context.Context, kind string, body any) (int64, error) {
	if s.registry == nil {
		return 0, errNoRegistry
	}
	redacted, err := s.redactDetail(body)
	if err != nil {
		return 0, err
	}
	id, err := s.registry.RaiseAlert(ctx, s.now().UnixMilli(), kind, redacted)
	if err != nil {
		return 0, err
	}
	s.emitAlert(id, kind, redacted)
	return id, nil
}

// emitNewestAlert fans the newest unread alert out: the pairing ceremony stores its alert
// inside its own transaction, so the route that finished it passes the row on from here. Best
// effort — the row stays stored whatever a listener does with it.
func (s *server) emitNewestAlert(ctx context.Context) {
	if s.registry == nil {
		return
	}
	rows, err := s.registry.Alerts(ctx, hub.AlertsFilter{UnreadOnly: true, Limit: 1})
	if err != nil || len(rows) == 0 {
		return
	}
	s.emitAlert(rows[0].ID, rows[0].Kind, rows[0].Body)
}

// emitAlert fans one stored alert out with this server's clock naming the moment.
func (s *server) emitAlert(id int64, kind string, body *jsfmt.Value) {
	s.alerts.emit(AlertEvent{Type: kind, ID: id, At: jsfmt.ISOTime(s.now()), Body: body})
}

// redactDetail encodes one detail and strips every credential, code and invite of this run out
// of it; nil stays nil.
func (s *server) redactDetail(detail any) (*jsfmt.Value, error) {
	if detail == nil {
		return nil, nil
	}
	raw, err := domain.MarshalCompactJSON(detail)
	if err != nil {
		return nil, &channel.HuddleError{Status: 400, Msg: "detail is not JSON"}
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		return nil, &channel.HuddleError{Status: 400, Msg: "detail is not JSON"}
	}
	return s.auth.Redact(value), nil
}

// auditRoute answers the audit log's read (SPEC 7.7): newest first, one page of at most 500
// rows, paged by before — the last row id a page served. Owner-only: the log is the owner's
// security record, and a member's credential or browser is refused it.
func (s *server) auditRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !me.Root {
		return &channel.HuddleError{Status: 403, Msg: "only the owner reads the audit log"}
	}
	if s.registry == nil {
		return &channel.HuddleError{Status: 500, Msg: errNoRegistry.Error()}
	}
	filter := hub.AuditFilter{}
	if raw := r.URL.Query().Get("before"); raw != "" {
		before, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			return &channel.HuddleError{Status: 400, Msg: "before: a row id greater than 0"}
		}
		filter.Before = &before
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > auditLimitMax {
			return &channel.HuddleError{Status: 400, Msg: "limit: a number from 1 to 500"}
		}
		filter.Limit = limit
	}
	entries, err := s.registry.Audit(r.Context(), filter)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Entries []hub.AuditRow `json:"entries"`
	}{entries}, nil)
	return nil
}
