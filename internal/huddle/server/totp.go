package server

// The authenticator routes: the enrolment a link session or a signed-in session opens (a bare
// visit can never enrol — the route demands a credential, and replacing an enrolment demands
// the step-up the first one waives), the uncredentialed code sign-in under /api/login/, and
// the passkey a code session saves on its own device row without a pairing link. No answer of
// these routes carries a code, a secret or a recovery code beyond the one moment the enrolment
// hands the recovery codes over.

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
)

// totpBeginRoute opens one enrolment for the browser that already proved it may: the owner's
// link session, a paired device, a code session — any root credential but never a bare visit.
// Replacing an enrolment is the dangerous half, so it asks the step-up the first enrolment
// waives: the link itself (or the passkey that signed in) is the proof of the first one.
func (s *server) totpBeginRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.totp == nil {
		return &channel.HuddleError{Status: 500, Msg: "the authenticator store is unavailable"}
	}
	if !me.Root {
		return &channel.HuddleError{Status: 403, Msg: "only the owner may enrol an authenticator"}
	}
	if s.totp.Enrolled(r.Context()) && s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	if _, err := s.body(r); err != nil {
		return err
	}
	begun, err := s.totp.BeginEnrol(r.Context())
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, begun, nil)
	return nil
}

// totpFinishRoute verifies the confirming code and answers the recovery codes — the only
// moment they exist in plain. The audit row lands inside the enrolment's own transaction, so
// an enrolment that cannot be recorded never happens.
func (s *server) totpFinishRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.totp == nil {
		return &channel.HuddleError{Status: 500, Msg: "the authenticator store is unavailable"}
	}
	if !me.Root {
		return &channel.HuddleError{Status: 403, Msg: "only the owner may enrol an authenticator"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	recovery, err := s.totp.FinishEnrol(r.Context(), auditActor(me), textOf(fieldOf(body, "code")))
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Recovery []string `json:"recovery"`
	}{recovery}, nil)
	return nil
}

// totpRemoveRoute drops the enrolment and ends every session a code minted. It is the one
// dangerous act of the authenticator's own life, so it passes the step-up like a device's
// removal does.
func (s *server) totpRemoveRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.totp == nil {
		return &channel.HuddleError{Status: 500, Msg: "the authenticator store is unavailable"}
	}
	if !me.Root {
		return &channel.HuddleError{Status: 403, Msg: "only the owner may remove the authenticator"}
	}
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	removed, err := s.totp.Remove(r.Context(), auditActor(me))
	if err != nil {
		return err
	}
	if !removed {
		return &channel.HuddleError{Status: 404, Msg: "no authenticator is enrolled"}
	}
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// loginTotpRoute is the code sign-in: uncredentialed, under /api/login/, the way a session
// begins. The browser's own name comes from its user agent, and the answer sets no secret in a
// body — the session rides the cookie the passkey sign-ins ride.
func (s *server) loginTotpRoute(w http.ResponseWriter, r *http.Request) error {
	body, err := s.body(r)
	if err != nil {
		return err
	}
	code := textOf(fieldOf(body, "code"))
	if s.totp == nil || code == "" {
		return &channel.HuddleError{Status: 401, Msg: auth.TotpWrongMsg}
	}
	device := auth.DeviceNameFromUA(r.UserAgent())
	session, err := s.totp.SignIn(r.Context(), code, device)
	if err != nil {
		return err
	}
	w.Header().Set("Set-Cookie", auth.PairCookieSet(r.TLS != nil, session.Cookie))
	s.writeJSON(w, 200, struct {
		Device    string `json:"device"`
		ExpiresIn int64  `json:"expires_in"`
	}{session.Device, session.ExpiresIn}, nil)
	return nil
}

// passkeyBeginRoute opens one registration a signed-in session runs on its own device row: the
// shape the biometrics offer after a code sign-in wants. A browser with no device session (the
// owner's link) has no row to land one on — the pairing mint is its way.
func (s *server) passkeyBeginRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	if me.Device == "" {
		return &channel.HuddleError{Status: 403, Msg: "passkey saving runs in a signed-in browser: sign in first"}
	}
	if _, err := s.body(r); err != nil {
		return err
	}
	creation, err := s.pairs.BeginSessionPasskey(r.Context(), me.Device)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Options any `json:"options"`
	}{creation}, nil)
	return nil
}

// passkeyFinishRoute verifies the registration and lands it on the device row the session
// belongs to; the row keeps its name, so the device's other sessions live on.
func (s *server) passkeyFinishRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	if me.Device == "" {
		return &channel.HuddleError{Status: 403, Msg: "passkey saving runs in a signed-in browser: sign in first"}
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return &channel.HuddleError{Status: 400, Msg: "body is not JSON"}
	}
	var body struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Response) == 0 {
		return &channel.HuddleError{Status: 400, Msg: "saving a passkey needs a passkey response"}
	}
	device, err := s.pairs.FinishSessionPasskey(r.Context(), me.Device, body.Response)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Device string `json:"device"`
	}{device}, nil)
	return nil
}
