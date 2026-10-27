package server

// The sign-out routes: /api/signout ends whatever credential this browser presents
// — its link session, a root token it presents, or its paired device's session — and
// /api/signout/all ends every browser and device session at once behind the same fresh
// assertion a revocation rides on, keeping the root tokens. Neither unpairs a device or ends a
// member's own credential, and a sign-out the audit log cannot record happens nowhere.

import (
	"fmt"
	"net/http"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
)

// signOutRoute ends the session this browser presents and clears both cookies it may carry.
// The audit row lands first: a sign-out that cannot be recorded never happens.
func (s *server) signOutRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if _, err := s.audit(r.Context(), auditActor(me), "signout", "dashboard", nil); err != nil {
		return err
	}
	if cred := auth.Given(r, s.cookie); cred != "" {
		s.auth.SignOut(cred)
	}
	if s.pairs != nil {
		s.pairs.EndSession(r.Context(), auth.PairCookie(r))
	}
	s.clearSessionCookies(w, r)
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// signOutAllRoute ends every browser and device session at once. Like the removal of a device,
// it is a dangerous act: it asks for the same fresh passkey assertion, and the audit
// row lands before anything ends.
func (s *server) signOutAllRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if s.stepUp.Required(r, me) {
		return &channel.HuddleError{Status: 401, Msg: "step-up required"}
	}
	if _, err := s.audit(r.Context(), auditActor(me), "signout.all", "dashboard", nil); err != nil {
		return err
	}
	s.auth.SignOutAll()
	if s.pairs != nil {
		if _, err := s.pairs.EndAllSessions(r.Context()); err != nil {
			return err
		}
	}
	s.clearSessionCookies(w, r)
	s.writeJSON(w, 200, struct {
		OK bool `json:"ok"`
	}{true}, nil)
	return nil
}

// clearSessionCookies sends the expiring Set-Cookie lines for both cookies a browser may carry,
// the link session's and the paired device's, so one answer ends the browser's session either
// way. Add keeps both lines: a device session renewed by this very request gets its fresh
// cookie first and the clearing line after it, and the browser's last word wins.
func (s *server) clearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := ""
	device := auth.PairPlainCookie
	if servedOverHTTPS(r) {
		secure = "; Secure"
		device = auth.PairHostCookie
	}
	w.Header().Add("Set-Cookie",
		fmt.Sprintf("%s=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict%s", s.cookie, secure))
	w.Header().Add("Set-Cookie",
		fmt.Sprintf("%s=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict%s", device, secure))
}
