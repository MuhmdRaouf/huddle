package server

// The pairing routes over the real server (SPEC 7.3): a virtual ES256 authenticator pairs a
// device through /api/pair, signs in later through /api/login, the cookie it earns carries it,
// and removing the device — the way the terminal's `huddle devices rm` does, straight through
// the shared hub database — kills the session on the next request. The refusals each get their
// own line: a foreign origin never writes, an unknown link answers 401, and garbage for a
// passkey response answers 400.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// creationOptions is the shape of a pair begin's options the tests read: the challenge and RP ID
// the ceremony issued, and the user handle it enrolled, all in the base64url text the wire speaks.
type creationOptions struct {
	Challenge  string `json:"challenge"`
	RPID       string `json:"-"`
	UserHandle string `json:"-"`
}

// pairCore opens one pairing core over the child's shared hub database, the way both the server
// and the terminal's own words reach it.
func pairCore(t *testing.T, srv *childServer) *auth.Pairing {
	t.Helper()
	db, err := hub.Open(bg, home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the child's hub.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rpID, origins := pairRP(srv.home, srv.port)
	p, err := auth.NewPairing(db, auth.PairOptions{RPID: rpID, Origins: origins})
	if err != nil {
		t.Fatalf("new pairing: %v", err)
	}
	return p
}

// beginPair opens one registration over HTTP and answers the options JSON the soft key answers,
// the fields the tests read out of it, and the code both sides compare.
func beginPair(t *testing.T, srv *childServer, token string, name string) (json.RawMessage, creationOptions, string) {
	t.Helper()
	code, out := srv.post(t, "/api/pair/begin", fmt.Sprintf(`{"token":%q,"name":%q}`, token, name), nil)
	if code != 200 {
		t.Fatalf("pair begin = %d: %s", code, out)
	}
	var body struct {
		Options json.RawMessage `json:"options"`
		Code    string          `json:"code"`
	}
	if err := json.Unmarshal([]byte(out), &body); err != nil {
		t.Fatalf("pair begin body %s: %v", out, err)
	}
	var begin struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(body.Options, &begin); err != nil {
		t.Fatalf("pair begin options %s: %v", body.Options, err)
	}
	o := creationOptions{
		Challenge:  begin.PublicKey.Challenge,
		RPID:       begin.PublicKey.RP.ID,
		UserHandle: begin.PublicKey.User.ID,
	}
	if o.Challenge == "" || o.RPID == "" || o.UserHandle == "" {
		t.Fatalf("pair begin options are short: %+v", o)
	}
	return body.Options, o, body.Code
}

// pairDevice runs one whole pairing over HTTP and answers the Set-Cookie line its finish wrote
// with the user handle its options enrolled.
func pairDevice(t *testing.T, srv *childServer, key *webauthntest.SoftKey, name string) (string, string) {
	t.Helper()
	invite := pairCore(t, srv)
	minted, err := invite.Mint(bg)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	options, parsed, code := beginPair(t, srv, minted.Token, name)
	if code != auth.PairCode(minted.Token) {
		t.Errorf("begin's code %q is not the link's %q", code, auth.PairCode(minted.Token))
	}
	finish, err := json.Marshal(map[string]any{"token": minted.Token, "response": key.Create(t, options, srv.origin())})
	if err != nil {
		t.Fatalf("encode the finish body: %v", err)
	}
	status, header, out, err := srv.request(bg, http.MethodPost, "/api/pair/finish", string(finish),
		map[string]string{"content-type": "application/json"})
	if err != nil {
		t.Fatalf("pair finish: %v", err)
	}
	if status != 200 {
		t.Fatalf("pair finish = %d: %s", status, out)
	}
	return header.Get("Set-Cookie"), parsed.UserHandle
}

// origin is the loopback origin a browser on this machine speaks.
func (s *childServer) origin() string { return fmt.Sprintf("http://localhost:%d", s.port) }

// cookieValueOf reads one Set-Cookie line's value.
func cookieValueOf(t *testing.T, setCookie string) string {
	t.Helper()
	name, value, found := strings.Cut(setCookie, "=")
	if !found {
		t.Fatalf("Set-Cookie %q carries no value", setCookie)
	}
	if value, _, _ = strings.Cut(value, ";"); value == "" {
		t.Fatalf("Set-Cookie %q carries no value", name)
	}
	return value
}

// TestPairingOverHTTP pairs, signs in, carries the cookie, and feels the removal.
func TestPairingOverHTTP(t *testing.T) {
	srv := startServer(t, nil)
	key := webauthntest.NewSoftKey(t)

	setCookie, userHandle := pairDevice(t, srv, key, "iPhone")
	// the loopback listener serves plain http, so the cookie takes the plain name and no Secure
	if !strings.HasPrefix(setCookie, auth.PairPlainCookie+"=hdev_") {
		t.Fatalf("the loopback Set-Cookie %q does not use the plain name", setCookie)
	}
	if strings.Contains(setCookie, "Secure") {
		t.Errorf("the loopback Set-Cookie %q asks for Secure, which http refuses", setCookie)
	}
	for _, want := range []string{"Path=/", "Max-Age=43200", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(setCookie, want) {
			t.Errorf("the Set-Cookie %q misses %q", setCookie, want)
		}
	}
	cookie := cookieValueOf(t, setCookie)

	// an explicit empty token header: the helper would send the root's, and the cookie must
	// carry this request on its own
	headers := map[string]string{"Cookie": auth.PairPlainCookie + "=" + cookie, "x-huddle-token": ""}
	code, _, out := srv.get(t, "/api/whoami", headers)
	if code != 200 {
		t.Fatalf("whoami with the device cookie = %d: %s", code, out)
	}
	var who auth.Who
	if err := json.Unmarshal([]byte(out), &who); err != nil {
		t.Fatalf("whoami body %s: %v", out, err)
	}
	if who.Name != "owner" || !who.Root || who.Device != "iPhone" {
		t.Errorf("the paired device is %+v, want the owner on iPhone", who)
	}

	// a later sign-in through the same passkey mints a fresh cookie
	code, out = srv.post(t, "/api/login/begin", `{}`, nil)
	if code != 200 {
		t.Fatalf("login begin = %d: %s", code, out)
	}
	var begin struct {
		Options struct {
			PublicKey struct {
				Challenge string `json:"challenge"`
				RPID      string `json:"rpId"`
			} `json:"publicKey"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(out), &begin); err != nil {
		t.Fatalf("login begin body %s: %v", out, err)
	}
	challenge := begin.Options.PublicKey.Challenge
	if challenge == "" || begin.Options.PublicKey.RPID == "" {
		t.Fatalf("login begin options are short: %s", out)
	}
	finish, err := json.Marshal(map[string]any{
		"response": key.Get(t, challenge, begin.Options.PublicKey.RPID, srv.origin(), userHandle),
	})
	if err != nil {
		t.Fatalf("encode the login body: %v", err)
	}
	code, out = srv.post(t, "/api/login/finish", string(finish), nil)
	if code != 200 {
		t.Fatalf("login finish = %d: %s", code, out)
	}

	// the removal the terminal runs — through the shared database — kills the session at once
	if !pairCore(t, srv).Remove(bg, "iPhone") {
		t.Fatal("remove iPhone answered false")
	}
	code, _, out = srv.get(t, "/api/whoami", headers)
	if code != 401 {
		t.Errorf("whoami after the removal = %d: %s, want 401", code, out)
	}
}

// TestPairingRouteRefusals covers what the routes and the guard refuse: a foreign origin, an
// unknown link, garbage for a passkey response, and the page the printed link opens.
func TestPairingRouteRefusals(t *testing.T) {
	srv := startServer(t, nil)

	code, _, _, err := srv.request(bg, http.MethodPost, "/api/pair/begin", `{"token":"x","name":"iPhone"}`,
		map[string]string{"Origin": "https://evil.example"})
	if err != nil || code != 403 {
		t.Errorf("pair begin from a foreign origin = %d (%v), want 403", code, err)
	}

	code, out := srv.post(t, "/api/pair/begin", `{"token":"A234567890BCDEFGHIJKLMNOPQR","name":"iPhone"}`, nil)
	if code != 401 {
		t.Errorf("pair begin on an unknown link = %d (%s), want 401", code, out)
	}

	core := pairCore(t, srv)
	minted, err := core.Mint(bg)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	options, _, _ := beginPair(t, srv, minted.Token, "iPhone")
	finish, err := json.Marshal(map[string]any{"token": minted.Token, "response": json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	code, out = srv.post(t, "/api/pair/finish", string(finish), nil)
	if code != 400 {
		t.Errorf("pair finish on garbage = %d (%s), want 400", code, out)
	}

	// the link stays good after the refused garbage
	finish, err = json.Marshal(map[string]any{"token": minted.Token, "response": webauthntest.NewSoftKey(t).Create(t, options, srv.origin())})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	code, out = srv.post(t, "/api/pair/finish", string(finish), nil)
	if code != 200 {
		t.Errorf("pair finish after the refusal = %d (%s), want 200", code, out)
	}

	// the printed link opens the app
	code, _, page := srv.get(t, "/pair", nil)
	if code != 200 || !strings.Contains(page, "<!doctype html>") {
		t.Errorf("GET /pair = %d, want the app shell", code)
	}
}
