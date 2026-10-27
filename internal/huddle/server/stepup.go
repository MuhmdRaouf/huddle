package server

// The step-up routes and the check they feed: allow, answer, send, link, unlink,
// rule changes and pairing a new device each need a passkey assertion at most five minutes old
// in this browser session; reads, deny, snooze and dismiss need only the session. The
// assertion lives on the device session's own row, so one browser's never covers another's.

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
)

// StepUp is the passkey check the dangerous routes pass: Required answers whether
// the request lacks an assertion fresh enough, and the route answers 401 "step-up required".
type StepUp interface {
	Required(r *http.Request, me auth.Who) bool
}

// soloStepUp is the check while the pairing core is unavailable: nothing on this machine can
// hold a passkey, so nothing asks for one.
type soloStepUp struct{}

func (soloStepUp) Required(*http.Request, auth.Who) bool { return false }

// pairStepUp is the check once pairing exists. The hub listener is the network's door,
// so everything dangerous through it asserts a passkey: a paired device's browser carries its
// own fresh assertion, and a credential that holds none — the terminal's token, a satellite's —
// can never pass there. On loopback the same assertion is asked of a paired device's session
// alone; the machine's own credentials are the local sign-in and waive it, so the owner's
// desktop keeps working whatever is paired.
type pairStepUp struct {
	pairs *auth.Pairing
	now   func() time.Time
}

func (p pairStepUp) Required(r *http.Request, me auth.Who) bool {
	if me.Device != "" {
		// the paired device's session is the one thing that can carry an assertion, and it must
		// carry a fresh one wherever the request came from
		return p.pairs.StepUpDue(r.Context(), auth.PairCookie(r), p.now().UnixMilli())
	}
	return fromHubListener(r.Context())
}

// stepUpBeginRoute opens one step-up ceremony for the paired device the request's cookie names.
// Only a device session may begin one: the root token and a local browser's link session hold
// no passkey to assert with, and the options it answers are for that browser alone.
func (s *server) stepUpBeginRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	if me.Device == "" {
		return &channel.HuddleError{Status: 403, Msg: "step-up runs in a paired device's browser: sign in there first"}
	}
	assertion, err := s.pairs.BeginStepUp(r.Context())
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Options any `json:"options"`
	}{assertion}, nil)
	return nil
}

// stepUpFinishRoute verifies one assertion and stamps the five minutes it buys onto the
// cookie's own session row. The audit row lands before the answer: an unaudited step-up never
// happened, so one that cannot be recorded is refused.
func (s *server) stepUpFinishRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	if me.Device == "" {
		return &channel.HuddleError{Status: 403, Msg: "step-up runs in a paired device's browser: sign in there first"}
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return &channel.HuddleError{Status: 400, Msg: "body is not JSON"}
	}
	var body struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Response) == 0 {
		return &channel.HuddleError{Status: 400, Msg: "the step-up needs a passkey response"}
	}
	device, err := s.pairs.FinishStepUp(r.Context(), auth.PairCookie(r), body.Response)
	if err != nil {
		return err
	}
	if _, err := s.audit(r.Context(), device, "stepup", device, nil); err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Device   string `json:"device"`
		ValidFor int64  `json:"valid_for"`
	}{device, auth.StepUpWindowMS / 1000}, nil)
	return nil
}
