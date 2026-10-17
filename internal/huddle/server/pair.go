package server

// The pairing routes (SPEC 7.3): the four ceremonies a browser runs to pair a device with a
// passkey and sign in with it later, and the page the printed link opens. They are the only
// uncredentialed /api paths beside the join: they are how a session begins. Every write still
// passes the guard — JSON that names its action, our Host, our own Origin — and the answers set
// no token in a body: the session rides a cookie the browser keeps for itself.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// pairRP answers the relying party the passkeys belong to and the origins a browser may run the
// ceremonies from: the hub's own name when the config carries one, else localhost with both its
// loopback spellings. The hub listener's fixed port (the role word refuses a moving one) adds the
// origin a phone pairing over the private network speaks.
func pairRP(homeDir string, port int) (string, []string) {
	rpID := "localhost"
	origins := []string{
		fmt.Sprintf("http://localhost:%d", port),
		fmt.Sprintf("http://127.0.0.1:%d", port),
	}
	cfg, err := home.LoadConfig(home.Home{Root: homeDir})
	if err != nil || cfg.Hub.Host == "" {
		return rpID, origins
	}
	rpID = cfg.Hub.Host
	origins = append([]string{"https://" + cfg.Hub.Host}, origins...)
	if _, listenPort, err := net.SplitHostPort(cfg.Hub.Listen); err == nil {
		if n, err := strconv.Atoi(listenPort); err == nil && n > 0 && n < 65536 {
			origins = append([]string{fmt.Sprintf("https://%s:%d", cfg.Hub.Host, n)}, origins...)
		}
	}
	return rpID, origins
}

// deviceAlive answers whether the device session a request carries still lives: a removed
// device's open stream ends on its next heartbeat instead of running on.
func (s *server) deviceAlive(r *http.Request) bool {
	cookie := auth.PairCookie(r)
	if cookie == "" || s.pairs == nil {
		return true
	}
	_, alive := s.pairs.Session(r.Context(), cookie)
	return alive
}

// pairBeginRoute opens one registration ceremony on a pairing link: the creation options the
// browser turns into a passkey, with the code both sides compare.
func (s *server) pairBeginRoute(w http.ResponseWriter, r *http.Request) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	body, err := s.body(r)
	if err != nil {
		return err
	}
	token := textOf(fieldOf(body, "token"))
	name := textOf(fieldOf(body, "name"))
	creation, err := s.pairs.BeginPair(r.Context(), token, name)
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Options   any    `json:"options"`
		Code      string `json:"code"`
		ExpiresIn int64  `json:"expires_in"`
	}{creation, auth.PairCode(token), auth.PairLinkMS / 1000}, nil)
	return nil
}

// pairFinishRoute verifies one registration and sets the session cookie the pairing earned.
// The body carries the pairing token beside the passkey response; the token never leaves this
// handler, and the answer carries no secret at all.
func (s *server) pairFinishRoute(w http.ResponseWriter, r *http.Request) error {
	session, err := s.pairFinish(r)
	if err != nil {
		return err
	}
	// the finish stored the pairing's alert inside its own transaction; the push sender hears
	// it from here
	s.emitNewestAlert(r.Context())
	w.Header().Set("Set-Cookie", auth.PairCookieSet(r.TLS != nil, session.Cookie))
	s.writeJSON(w, 200, struct {
		Device    string `json:"device"`
		ExpiresIn int64  `json:"expires_in"`
	}{session.Device, session.ExpiresIn}, nil)
	return nil
}

// pairFinish runs the finish away from the answer, so the cookie's write happens once the
// device is truly recorded.
func (s *server) pairFinish(r *http.Request) (auth.PairSession, error) {
	if s.pairs == nil {
		return auth.PairSession{}, &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return auth.PairSession{}, &channel.HuddleError{Status: 400, Msg: "body is not JSON"}
	}
	var body struct {
		Token    string          `json:"token"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Response) == 0 {
		return auth.PairSession{}, &channel.HuddleError{Status: 400, Msg: "the pairing needs a token and a passkey response"}
	}
	return s.pairs.FinishPair(r.Context(), body.Token, body.Response)
}

// loginBeginRoute opens one discoverable sign-in: the resident key names its device.
func (s *server) loginBeginRoute(w http.ResponseWriter, r *http.Request) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	assertion, err := s.pairs.BeginLogin(r.Context())
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, struct {
		Options any `json:"options"`
	}{assertion}, nil)
	return nil
}

// loginFinishRoute verifies one assertion and sets the session cookie the sign-in earned. The
// body carries the passkey response under "response", the shape both finish routes speak.
func (s *server) loginFinishRoute(w http.ResponseWriter, r *http.Request) error {
	if s.pairs == nil {
		return &channel.HuddleError{Status: 500, Msg: "the pairing store is unavailable"}
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return &channel.HuddleError{Status: 400, Msg: "body is not JSON"}
	}
	var body struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Response) == 0 {
		return &channel.HuddleError{Status: 400, Msg: "the sign-in needs a passkey response"}
	}
	session, err := s.pairs.FinishLogin(r.Context(), body.Response)
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
