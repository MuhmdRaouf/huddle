package server

// The sign-in routes a browser without a terminal runs (SPEC 7.3): the signed-out page's one
// question — does this home hold a passkey at all — answered without a credential, the pairing
// link the owner's own browser mints for itself, and the rolling device session whose cookie a
// request past its renewal hour earns again. The refusals each get their own line: a mint
// without a credential, from a member, or from a foreign origin never draws a link.

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// TestLoginAvailableAnswersTheMethods proves the signed-out page's two facts — does this home
// hold a passkey, does it hold an authenticator enrolment — as the two booleans and nothing
// else: no device names, no counts, nothing that differs in shape or timing between a home
// with a method and one without.
func TestLoginAvailableAnswersTheMethods(t *testing.T) {
	srv := startServer(t, nil)

	code, _, out := srv.get(t, "/api/login/available", map[string]string{"x-huddle-token": ""})
	if code != 200 {
		t.Fatalf("available with no device = %d: %s", code, out)
	}
	if out != `{"passkeys":false,"totp":false}` {
		t.Errorf("available with no device = %s, want the two booleans alone", out)
	}

	pairDevice(t, srv, webauthntest.NewSoftKey(t), "iPhone")
	code, _, out = srv.get(t, "/api/login/available", map[string]string{"x-huddle-token": ""})
	if code != 200 || out != `{"passkeys":true,"totp":false}` {
		t.Errorf("available after a pairing = %d %s, want the two booleans alone", code, out)
	}

	_, _ = enrolTOTP(t, srv, linkBrowser(t, srv))
	code, _, out = srv.get(t, "/api/login/available", map[string]string{"x-huddle-token": ""})
	if code != 200 || out != `{"passkeys":true,"totp":true}` {
		t.Errorf("available after an enrolment = %d %s, want the two booleans alone", code, out)
	}
}

// TestPairMintServesTheOwnerAndDevicesOnly proves who may draw a pairing link without a
// terminal: the owner and a paired device's browser yes, a member no, a request with no
// credential no, a foreign origin no, and a body that names no action no.
func TestPairMintServesTheOwnerAndDevicesOnly(t *testing.T) {
	srv := startServer(t, nil)

	// no credential
	code, out := srv.post(t, "/api/pair/mint", `{"action":"mint"}`, map[string]string{"x-huddle-token": ""})
	if code != 401 {
		t.Errorf("a mint with no credential = %d (%s), want 401", code, out)
	}

	// a member: join first, then ask
	_, body := srv.post(t, "/api/tokens", "{}", nil)
	invite, _ := bodyMap(t, body)["token"].(string)
	_, body = srv.post(t, "/api/join", `{"token": "`+invite+`", "name": "dev"}`, nil)
	cred, _ := bodyMap(t, body)["credential"].(string)
	code, out = srv.post(t, "/api/pair/mint", `{"action":"mint"}`, map[string]string{"x-huddle-token": cred})
	if code != 403 {
		t.Errorf("a member's mint = %d (%s), want 403", code, out)
	}

	// a foreign origin never writes
	code, _, out, err := srv.request(bg, http.MethodPost, "/api/pair/mint", `{"action":"mint"}`,
		map[string]string{"content-type": "application/json", "Origin": "https://evil.example"})
	if err != nil || code != 403 || strings.Contains(out, "token") {
		t.Errorf("a mint from a foreign origin = %d (%v): %s, want 403 and no token", code, err, out)
	}

	// a body that names no action is not the write it claims
	code, out = srv.post(t, "/api/pair/mint", `{}`, nil)
	if code != 400 {
		t.Errorf("a mint without its action word = %d (%s), want 400", code, out)
	}

	// the owner draws a link, and it spends on a real pairing
	code, out = srv.post(t, "/api/pair/mint", `{"action":"mint"}`, nil)
	if code != 200 {
		t.Fatalf("the owner's mint = %d: %s", code, out)
	}
	token, _ := bodyMap(t, out)["token"].(string)
	if !regexp.MustCompile(`^[A-Z2-7]{26}$`).MatchString(token) {
		t.Fatalf("the minted token %q is not 26 base32 characters", token)
	}
	if len(bodyMap(t, out)) != 1 {
		t.Errorf("the mint's answer = %s, want the token alone", out)
	}

	// a paired device's browser may draw one too (pairing a second browser)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "iPhone")
	device := map[string]string{
		"x-huddle-token": "",
		"Cookie":         auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie),
	}
	code, out = srv.post(t, "/api/pair/mint", `{"action":"mint"}`, device)
	if code != 200 {
		t.Errorf("a device's mint = %d: %s, want 200", code, out)
	}
}

// TestWhoamiNamesTheCredentialKind proves the one fact the shell's banner reads: which kind of
// credential this browser holds — the owner's link session, a member's, or a paired device's.
func TestWhoamiNamesTheCredentialKind(t *testing.T) {
	srv := startServer(t, nil)

	kindOf := func(headers map[string]string) string {
		t.Helper()
		code, _, out := srv.get(t, "/api/whoami", headers)
		if code != 200 {
			t.Fatalf("whoami = %d: %s", code, out)
		}
		kind, _ := bodyMap(t, out)["kind"].(string)
		return kind
	}
	if kind := kindOf(nil); kind != "owner" {
		t.Errorf("the root token's kind = %q, want owner", kind)
	}

	_, body := srv.post(t, "/api/tokens", "{}", nil)
	invite, _ := bodyMap(t, body)["token"].(string)
	_, body = srv.post(t, "/api/join", `{"token": "`+invite+`", "name": "dev"}`, nil)
	cred, _ := bodyMap(t, body)["credential"].(string)
	if kind := kindOf(map[string]string{"x-huddle-token": cred}); kind != "member" {
		t.Errorf("a member's kind = %q, want member", kind)
	}

	// a member's browser session names the member's kind too
	devCode := loginCode(t, srv, map[string]string{"x-huddle-token": cred})
	_, devCookie := signInBrowser(t, srv, devCode)
	if kind := kindOf(map[string]string{"x-huddle-token": "", "Cookie": devCookie}); kind != "member" {
		t.Errorf("a member browser's kind = %q, want member", kind)
	}

	// the root's browser holds the owner's link session: the banner's own case
	rootCode := loginCode(t, srv, nil)
	_, rootCookie := signInBrowser(t, srv, rootCode)
	if kind := kindOf(map[string]string{"x-huddle-token": "", "Cookie": rootCookie}); kind != "owner" {
		t.Errorf("the owner browser's kind = %q, want owner", kind)
	}

	// a paired device names the device kind
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "iPhone")
	if kind := kindOf(map[string]string{
		"x-huddle-token": "", "Cookie": auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie),
	}); kind != "device" {
		t.Errorf("a paired device's kind = %q, want device", kind)
	}
}

// TestARenewalResendsTheCookie proves the server side of the rolling session: a request inside
// the renewal hour sends no cookie, one past the hour re-sends the session's cookie with the
// seven-day life, and a removed device answers 401 with nothing.
func TestARenewalResendsTheCookie(t *testing.T) {
	srv := startServer(t, nil)
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "iPhone")
	cookie := auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie)
	headers := map[string]string{"x-huddle-token": "", "Cookie": cookie}

	// the pairing just renewed: no Set-Cookie on this request
	code, header, out := srv.get(t, "/api/whoami", headers)
	if code != 200 {
		t.Fatalf("whoami with the fresh cookie = %d: %s", code, out)
	}
	if sent := header.Get("Set-Cookie"); sent != "" {
		t.Errorf("a session inside its renewal hour re-sent %q", sent)
	}

	// the renewal hour passed: the same cookie comes back with the seven-day life
	if err := ageSessionPastRenewal(t, srv, cookieValueOf(t, setCookie), 2*time.Hour); err != nil {
		t.Fatalf("age the session: %v", err)
	}
	code, header, out = srv.get(t, "/api/whoami", headers)
	if code != 200 {
		t.Fatalf("whoami past the renewal hour = %d: %s", code, out)
	}
	sent := header.Get("Set-Cookie")
	if !strings.HasPrefix(sent, auth.PairPlainCookie+"=") {
		t.Fatalf("the renewal sent no cookie: %q", sent)
	}
	for _, want := range []string{"Path=/", "Max-Age=604800", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the re-sent cookie %q misses %q", sent, want)
		}
	}

	// a removed device answers 401 and sends nothing
	if !pairCore(t, srv).Remove(bg, "iPhone") {
		t.Fatal("remove iPhone answered false")
	}
	code, header, out = srv.get(t, "/api/whoami", headers)
	if code != 401 {
		t.Errorf("whoami after the removal = %d (%s), want 401", code, out)
	}
	if sent := header.Get("Set-Cookie"); sent != "" {
		t.Errorf("a removed device's request sent %q", sent)
	}
}

// ageSessionPastRenewal moves one session's renewal moment back through the child's own hub
// database, the way hours of idle would have.
func ageSessionPastRenewal(t *testing.T, srv *childServer, cookie string, back time.Duration) error {
	t.Helper()
	st, err := store.Open(bg, home.Home{Root: srv.home}.HubDB())
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Run(bg, "UPDATE device_sessions SET renewed = ? WHERE digest = ?",
		time.Now().Add(-back).UnixMilli(), auth.PairDigest(cookie))
}
