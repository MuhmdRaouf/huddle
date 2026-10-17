// The needs-you and answer routes (SPEC 6.1, 6.3, 6.4): the items list with the channels'
// blocked tasks kept in step, the owner's answer and send queueing one headed message per
// session, delivery through a channel push while the session's bridge acks one and through the
// next prompt otherwise, and pause, resume, snooze and dismiss over the channels' own controls.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The send limits (SPEC 6.3): 1 to 20 sessions, 1 to 4,000 characters.
const (
	maxSendSessions = 20
	maxMessageChars = 4000
)

// needsUnavailable is the refusal every needs route answers when the registry's database never
// opened: the channels serve on, the owner's actions wait for the next start.
func (s *server) needsUnavailable() error {
	return &channel.HuddleError{Status: 500, Msg: "the registry's database is unavailable"}
}

// needsRoute answers the needs-you list: the channels' blocked tasks sync in first, then every
// waiting item, newest first, with its snooze read at now.
func (s *server) needsRoute(w http.ResponseWriter, r *http.Request) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	if err := s.syncBlocked(r.Context()); err != nil {
		return err
	}
	items, err := s.registry.Items(r.Context(), s.now())
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Items []hub.Item `json:"items"`
	}{items}, nil)
	return nil
}

// needsItemRoute answers one item's actions: snooze behind one of the three spans, or dismiss.
func (s *server) needsItemRoute(w http.ResponseWriter, r *http.Request, path string) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	parts := needsItemPath.FindStringSubmatch(path)
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return &channel.HuddleError{Status: 404, Msg: "no such item"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	switch parts[2] {
	case "snooze":
		until, ok := snoozeUntil(textOf(fieldOf(body, "until")), s.now())
		if !ok {
			return &channel.HuddleError{Status: 400, Msg: "until is one of: 15m, 1h, tomorrow"}
		}
		err = s.registry.SnoozeItem(r.Context(), id, until)
	default:
		err = s.registry.DismissItem(r.Context(), id)
	}
	var missing *hub.MissingItemError
	if errors.As(err, &missing) {
		return &channel.HuddleError{Status: 404, Msg: missing.Error()}
	}
	return err
}

// auditActor names who drove a request: the paired device when one did, else the signed-in
// browser, else the owner's own credential.
func auditActor(me auth.Who) string {
	switch {
	case me.Device != "":
		return me.Device
	case me.Browser != "":
		return me.Browser
	}
	return "owner"
}

// snoozeUntil maps one snooze span to its wake time (SPEC 6.3): a quarter, an hour, or tomorrow
// at nine in the machine's own morning.
func snoozeUntil(kind string, now time.Time) (time.Time, bool) {
	switch kind {
	case "15m":
		return now.Add(15 * time.Minute), true
	case "1h":
		return now.Add(time.Hour), true
	case "tomorrow":
		next := now.AddDate(0, 0, 1)
		return time.Date(next.Year(), next.Month(), next.Day(), 9, 0, 0, 0, now.Location()), true
	}
	return time.Time{}, false
}

// sessionActRoute answers the owner's actions on one session: answer queues the owner's text as
// its next prompt and ends its open questions, messages hands the listen hook what waits for
// that prompt, and pause and resume reach the session's channel control.
func (s *server) sessionActRoute(w http.ResponseWriter, r *http.Request, path string, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	parts := sessionActPath.FindStringSubmatch(path)
	id, action := parts[1], parts[2]
	ctx := r.Context()
	row, err := s.registry.SessionRow(ctx, id, s.now())
	if err != nil {
		return err
	}
	if row == nil {
		return &channel.HuddleError{Status: 404, Msg: "no session " + id}
	}
	switch action {
	case "answer":
		// an answer carries the owner's words into a session (SPEC 7.4), so it is gated like an
		// allow; deny, snooze, dismiss and the reads are not
		if s.stepUp.Required(r, me) {
			return &channel.HuddleError{Status: 401, Msg: "step-up required"}
		}
		body, err := s.body(r)
		if err != nil {
			return err
		}
		text := textOf(fieldOf(body, "text"))
		if reason := messageTextErr(text); reason != "" {
			return &channel.HuddleError{Status: 400, Msg: reason}
		}
		in := hub.MessageIn{SessionID: id, FromKind: hub.FromOwner, Text: text}
		route, messageID, err := s.queueForSession(ctx, id, in)
		if err != nil {
			return err
		}
		if err := s.registry.CloseQuestions(ctx, id); err != nil {
			return err
		}
		// the message stands once queued, so a row that cannot be written is answered as an
		// error: an unaudited answer never passes silently (SPEC 7.7)
		if _, err := s.audit(ctx, auditActor(me), "answer", id, nil); err != nil {
			return err
		}
		s.emitRow(ctx, row.Channel, id)
		s.writeJSON(w, 200, struct {
			OK      bool   `json:"ok"`
			Route   string `json:"route"`
			Message int64  `json:"message"`
		}{true, route, messageID}, nil)
		return nil
	case "messages":
		taken, err := s.registry.TakeMessages(ctx, id, jsfmt.ISOTime(s.now()))
		if err != nil {
			return err
		}
		if _, err := s.audit(ctx, auditActor(me), "messages", id, nil); err != nil {
			return err
		}
		s.writeJSON(w, 200, struct {
			Messages []string `json:"messages"`
		}{taken}, nil)
		return nil
	default: // pause or resume
		if row.Channel == "" || row.Member == "" {
			return &channel.HuddleError{Status: 409,
				Msg: "session " + id + " is in no channel: pause it in its own session"}
		}
		body, err := s.body(r)
		if err != nil {
			return err
		}
		ch, err := s.hub.GetExisting(ctx, row.Channel)
		if err != nil {
			return err
		}
		result, err := ch.Control(ctx, channel.Owner, row.Member, action, textOf(fieldOf(body, "why")))
		if err != nil {
			return err
		}
		if _, err := s.audit(ctx, auditActor(me), action, id, nil); err != nil {
			return err
		}
		s.writeJSON(w, 200, struct {
			OK      bool   `json:"ok"`
			Control string `json:"control"`
			By      string `json:"by"`
		}{true, result.Control, result.By}, nil)
		return nil
	}
}

// sendRoute answers the owner's new work: one message per named session, each delivered on its
// own session's route. New work is the owner's words at their bluntest (SPEC 7.4), so the
// step-up gates it like an allow.
func (s *server) sendRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	sessions, ok := sessionListOf(body)
	if !ok || len(sessions) < 1 || len(sessions) > maxSendSessions {
		return &channel.HuddleError{Status: 400,
			Msg: fmt.Sprintf("sessions: an array of 1 to %d session ids", maxSendSessions)}
	}
	text := textOf(fieldOf(body, "text"))
	if reason := messageTextErr(text); reason != "" {
		return &channel.HuddleError{Status: 400, Msg: reason}
	}
	ctx := r.Context()
	sent := make([]sentRoute, 0, len(sessions))
	for _, id := range sessions {
		row, err := s.registry.SessionRow(ctx, id, s.now())
		if err != nil {
			return err
		}
		if row == nil {
			return &channel.HuddleError{Status: 404, Msg: "no session " + id}
		}
		in := hub.MessageIn{SessionID: id, FromKind: hub.FromOwner, Text: text}
		route, _, err := s.queueForSession(ctx, id, in)
		if err != nil {
			return err
		}
		// one row per session: a send the log cannot record is answered as an error, never silent
		if _, err := s.audit(ctx, auditActor(me), "send", id, nil); err != nil {
			return err
		}
		sent = append(sent, sentRoute{Session: id, Route: route})
	}
	s.writeJSON(w, 200, struct {
		Sent []sentRoute `json:"sent"`
	}{sent}, nil)
	return nil
}

// sentRoute names one send's target and the route its message took.
type sentRoute struct {
	Session string `json:"session"`
	Route   string `json:"route"`
}

// pushAckRoute records a bridge's proof that its channel pushes flow (SPEC 6.4) and promotes
// what waited on that member to the now route: a session answered while its bridge was gone is
// pushed the moment the bridge returns.
func (s *server) pushAckRoute(w http.ResponseWriter, r *http.Request) error {
	if s.registry == nil {
		return s.needsUnavailable()
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	name, member := textOf(fieldOf(body, "channel")), textOf(fieldOf(body, "as"))
	if name == "" || member == "" {
		return &channel.HuddleError{Status: 400, Msg: "a push ack needs channel and as"}
	}
	ctx := r.Context()
	if err := s.registry.RecordPushAck(ctx, name, member, jsfmt.ISOTime(s.now())); err != nil {
		return err
	}
	pushed := 0
	pending, err := s.registry.PendingOf(ctx, name, member)
	if err != nil {
		return err
	}
	for _, message := range pending {
		if s.pushMessage(ctx, message.ID, message.In) == hub.RouteNow {
			pushed++
		}
	}
	s.writeJSON(w, 200, struct {
		OK     bool `json:"ok"`
		Pushed int  `json:"pushed"`
	}{true, pushed}, nil)
	return nil
}

// pushMessage delivers one queued message: pushed into the session's channel when its bridge's
// proof is fresh (the push route the bridge turns into a claude/channel notification), else left
// for the listen hook on the session's next prompt. The route used is answered and marked on
// the row.
func (s *server) pushMessage(ctx context.Context, id int64, in hub.MessageIn) string {
	row, err := s.registry.SessionRow(ctx, in.SessionID, s.now())
	if err != nil || row == nil || row.DeliveryRoute != hub.RouteNow || row.Channel == "" || row.Member == "" {
		return hub.RouteNextPrompt
	}
	ch, err := s.hub.GetExisting(ctx, row.Channel)
	if err != nil {
		return hub.RouteNextPrompt
	}
	if _, err := ch.Send(ctx, channel.Owner, &row.Member, in.Header(), channel.SendOpts{}); err != nil {
		return hub.RouteNextPrompt // the channel refused: the next prompt still reaches it
	}
	_ = s.registry.MarkDelivered(ctx, id, hub.RouteNow, jsfmt.ISOTime(s.now()))
	return hub.RouteNow
}

// emitRow fans one session's computed row out to its channel's live stream, so an answer's
// state change reaches an open dashboard at once. Best effort, like every registry emit.
func (s *server) emitRow(ctx context.Context, channelName, id string) {
	if s.registry == nil || channelName == "" {
		return
	}
	row, err := s.registry.SessionRow(ctx, id, s.now())
	if err != nil || row == nil {
		return
	}
	ch, err := s.hub.GetExisting(ctx, channelName)
	if err != nil {
		return
	}
	ch.Emit("registry.session", row)
}

// syncBlocked brings the blocked items in step with every channel: a task blocked on a task the
// owner owns waits on the owner (usually the making of the depend op), and the session that
// owns it reads needs-you until the task moves.
func (s *server) syncBlocked(ctx context.Context) error {
	names, err := s.hub.Names(ctx)
	if err != nil {
		return err
	}
	in := []hub.BlockedIn{}
	for _, name := range names {
		ch, err := s.hub.GetExisting(ctx, name)
		if err != nil {
			continue
		}
		tasks, err := ch.Tasks(ctx, channel.TasksFilter{})
		if err != nil {
			continue
		}
		owners := map[string]string{}
		for _, task := range tasks {
			if task.Owner != nil {
				owners[task.ID] = *task.Owner
			}
		}
		for _, task := range tasks {
			if task.Status != "blocked" {
				continue
			}
			waitsOnOwner := false
			for _, dep := range task.BlockedBy {
				if owners[dep] == channel.Owner {
					waitsOnOwner = true
					break
				}
			}
			if !waitsOnOwner || task.Owner == nil {
				continue
			}
			session, err := s.registry.SessionOfMember(ctx, name, *task.Owner)
			if err != nil || session == "" {
				continue // its session is not reporting: the item grows when it does
			}
			in = append(in, hub.BlockedIn{
				Key:       "blocked:" + name + ":" + task.ID,
				SessionID: session,
				Summary:   task.ID + " " + task.Title + " waits on you",
			})
		}
	}
	return s.registry.SyncBlocked(ctx, in, jsfmt.ISOTime(s.now()))
}

// messageTextErr answers why a message's text is refused, or "" when it is not.
func messageTextErr(text string) string {
	length := jsfmt.UTF16Len(text)
	switch {
	case length == 0:
		return "text is required: 1 to 4000 characters"
	case length > maxMessageChars:
		return fmt.Sprintf("text is %d characters; the most is %d", length, maxMessageChars)
	}
	return ""
}

// sessionListOf reads the send route's sessions: an array of strings and nothing else.
func sessionListOf(body *jsfmt.Value) ([]string, bool) {
	field := fieldOf(body, "sessions")
	if field == nil || field.Kind() != jsfmt.KindArray {
		return nil, false
	}
	out := make([]string, 0, len(field.Items()))
	for _, item := range field.Items() {
		if item.Kind() != jsfmt.KindString {
			return nil, false
		}
		out = append(out, item.Text())
	}
	return out, true
}
