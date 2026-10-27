package server

// Step-up over the real server: the dangerous routes — an allow, an answer, a
// send — answer the one 401 without a passkey assertion fresh enough, and work with one. The
// paired device's browser session owes that assertion wherever the request lands; the hub
// listener asks it of every dangerous act, and loopback waives it for the machine's own
// credentials — the root token and the browser a one-time link signed in. Reads, deny, snooze,
// dismiss and the session's own delivery need only the session.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// stepUpRequiredBody is the exact body every dangerous route answers without the assertion.
const stepUpRequiredBody = `{"error":"step-up required"}`

// deviceHeaders carries one paired device's cookie alone, so no root credential vouches for the
// request beside it.
func deviceHeaders(cookie string) map[string]string {
	return map[string]string{"Cookie": auth.PairPlainCookie + "=" + cookie, "x-huddle-token": ""}
}

// stepUpStamp writes one device session's assertion moment straight into the home's hub.db, the
// test's way of moving the five-minute window without waiting it out.
func stepUpStamp(t *testing.T, srv *childServer, cookie string, atMS int64) {
	t.Helper()
	db, err := store.SQLite(context.Background(), filepath.Join(srv.home, "hub.db"))
	if err != nil {
		t.Fatalf("open the home's hub.db: %v", err)
	}
	defer db.Close()
	if err := db.Run(context.Background(),
		"UPDATE device_sessions SET stepup_at = ? WHERE digest = ?", atMS, auth.PairDigest(cookie)); err != nil {
		t.Fatalf("stamp the assertion moment: %v", err)
	}
}

// stepUpOverHTTP runs one assertion over HTTP for the browser its cookie names, the way the app
// earns its five minutes, and answers the finish's status and body.
func stepUpOverHTTP(t *testing.T, srv *childServer, key *webauthntest.SoftKey, cookie, userHandle string) (int, string) {
	t.Helper()
	headers := deviceHeaders(cookie)
	code, out := srv.post(t, "/api/stepup/begin", `{}`, headers)
	if code != 200 {
		t.Fatalf("stepup begin = %d: %s", code, out)
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
		t.Fatalf("stepup begin body %s: %v", out, err)
	}
	if begin.Options.PublicKey.Challenge == "" || begin.Options.PublicKey.RPID == "" {
		t.Fatalf("stepup begin options are short: %s", out)
	}
	finish, err := json.Marshal(map[string]any{
		"response": key.Get(t, begin.Options.PublicKey.Challenge, begin.Options.PublicKey.RPID,
			srv.origin(), userHandle),
	})
	if err != nil {
		t.Fatalf("encode the finish body: %v", err)
	}
	return srv.post(t, "/api/stepup/finish", string(finish), headers)
}

// signInOverHTTP signs a paired device in again and answers the fresh session's cookie: another
// browser session, born covered by its own assertion.
func signInOverHTTP(t *testing.T, srv *childServer, key *webauthntest.SoftKey, userHandle string) string {
	t.Helper()
	code, out := srv.post(t, "/api/login/begin", `{}`, nil)
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
	finish, err := json.Marshal(map[string]any{
		"response": key.Get(t, begin.Options.PublicKey.Challenge, begin.Options.PublicKey.RPID,
			srv.origin(), userHandle),
	})
	if err != nil {
		t.Fatalf("encode the login body: %v", err)
	}
	status, header, out, err := srv.request(context.Background(), "POST", "/api/login/finish",
		string(finish), map[string]string{"content-type": "application/json"})
	if err != nil || status != 200 {
		t.Fatalf("login finish = %d: %s (%v)", status, out, err)
	}
	return cookieValueOf(t, header.Get("Set-Cookie"))
}

// linkBrowser signs a browser in through a one-time link on the loopback listener and answers
// the headers its dashboard cookie carries: the machine's own browser, with no passkey of its
// own.
func linkBrowser(t *testing.T, srv *childServer) map[string]string {
	t.Helper()
	code, out := srv.post(t, "/api/login", `{}`, nil)
	if code != 200 {
		t.Fatalf("mint a login code = %d: %s", code, out)
	}
	var answer struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil || answer.Code == "" {
		t.Fatalf("the login answer %s carries no code", out)
	}
	request, err := http.NewRequest(http.MethodGet, srv.base+"/?code="+answer.Code, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	signedIn, doErr := client.Do(request)
	if doErr != nil || signedIn.StatusCode != 303 {
		t.Fatalf("the sign-in link failed: %v (want 303)", doErr)
	}
	setCookie := signedIn.Header.Get("Set-Cookie")
	_ = signedIn.Body.Close()
	cookie := strings.SplitN(setCookie, ";", 2)[0]
	name, value, found := strings.Cut(cookie, "=")
	if !found || value == "" {
		t.Fatalf("the sign-in's Set-Cookie %q carries no value", setCookie)
	}
	return map[string]string{"Cookie": name + "=" + value, "x-huddle-token": ""}
}

// theQuestionItemAt finds the one open question item's id.
func theQuestionItemAt(t *testing.T, srv *childServer) string {
	t.Helper()
	for id, pair := range needsItems(t, srv) {
		if pair[0] == "question" && pair[1] == "open" {
			return id
		}
	}
	t.Fatal("no open question item")
	return ""
}

func TestStepUpGatesTheDangerousRoutes(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")
	key := webauthntest.NewSoftKey(t)
	setCookie, userHandle := pairDevice(t, srv, key, "iPhone")
	cookie := cookieValueOf(t, setCookie)
	device := deviceHeaders(cookie)

	t.Run("the ceremony that minted the session covers its first five minutes", func(t *testing.T) {
		id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
		if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, device); code != 200 {
			t.Fatalf("allow on the freshly paired session = %d: %s", code, out)
		}
	})

	// every case below runs against a session whose assertion has aged out
	stepUpStamp(t, srv, cookie, time.Now().UnixMilli()-6*60*1000)

	t.Run("without the assertion, allow, answer and send answer the one 401", func(t *testing.T) {
		allowID := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
		for _, call := range []struct{ name, path, body string }{
			{"allow", fmt.Sprintf("/api/needs/%d/decide", allowID), `{"decision":"allow"}`},
			{"answer", "/api/sessions/s-1/answer", `{"text":"ship it"}`},
			{"send", "/api/send", `{"sessions":["s-1"],"text":"run it"}`},
		} {
			code, out := srv.post(t, call.path, call.body, device)
			if code != 401 || out != stepUpRequiredBody {
				t.Errorf("%s = %d %s, want 401 %s", call.name, code, out, stepUpRequiredBody)
			}
		}
		// the refused allow left its item open, and nothing was queued or answered
		if items := needsItems(t, srv); len(items) != 1 {
			t.Errorf("the refused acts left rows behind: %v", items)
		}
	})

	t.Run("reads, deny, snooze, dismiss and delivery need only the session", func(t *testing.T) {
		for _, path := range []string{"/api/needs", "/api/registry/sessions", "/api/registry/sessions/s-1"} {
			if code, _, out := srv.get(t, path, device); code != 200 {
				t.Errorf("GET %s = %d %s, want 200", path, code, out)
			}
		}
		denyID := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
		if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", denyID), `{"decision":"deny"}`, device); code != 200 {
			t.Errorf("deny = %d %s, want 200 without the assertion", code, out)
		}
		reportSession(t, srv, "s-q", "", "", "Notification")
		question := theQuestionItemAt(t, srv)
		if code, out := srv.post(t, "/api/needs/"+question+"/snooze", `{"until":"15m"}`, device); code != 200 {
			t.Errorf("snooze = %d %s, want 200", code, out)
		}
		if code, out := srv.post(t, "/api/needs/"+question+"/dismiss", `{}`, device); code != 200 {
			t.Errorf("dismiss = %d %s, want 200", code, out)
		}
		if code, out := srv.post(t, "/api/sessions/s-1/messages", `{}`, device); code != 200 {
			t.Errorf("the listen hook's take = %d %s, want 200", code, out)
		}
		// pause reaches the channel's own refusal, not the step-up's
		if code, out := srv.post(t, "/api/sessions/s-1/pause", `{"why":"lunch"}`, device); code != 409 || strings.Contains(out, "step-up") {
			t.Errorf("pause = %d %s, want the channel's own 409", code, out)
		}
	})

	t.Run("a fresh assertion opens the dangerous routes again", func(t *testing.T) {
		code, out := stepUpOverHTTP(t, srv, key, cookie, userHandle)
		if code != 200 {
			t.Fatalf("stepup finish = %d: %s", code, out)
		}
		var parsed struct {
			Device   string `json:"device"`
			ValidFor int64  `json:"valid_for"`
		}
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("stepup finish body %s: %v", out, err)
		}
		if parsed.Device != "iPhone" || parsed.ValidFor != 300 {
			t.Errorf("stepup finish = %s, want iPhone and 300 s", out)
		}
		allowID := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
		for _, call := range []struct{ name, path, body string }{
			{"allow", fmt.Sprintf("/api/needs/%d/decide", allowID), `{"decision":"allow"}`},
			{"answer", "/api/sessions/s-1/answer", `{"text":"ship it"}`},
			{"send", "/api/send", `{"sessions":["s-1"],"text":"run it"}`},
		} {
			if code, out := srv.post(t, call.path, call.body, device); code != 200 {
				t.Errorf("%s with the assertion = %d %s, want 200", call.name, code, out)
			}
		}
	})

	t.Run("the step-up writes its audit row", func(t *testing.T) {
		code, _, out := srv.get(t, "/api/audit", device)
		if code != 200 {
			t.Fatalf("the audit log = %d %s", code, out)
		}
		rows, _ := bodyMap(t, out)["entries"].([]any)
		found := false
		for _, row := range rows {
			if m, ok := row.(map[string]any); ok {
				if m["action"] == "stepup" && m["actor"] == "iPhone" && m["target"] == "iPhone" {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("the audit log holds no stepup row for iPhone: %s", out)
		}
	})

	t.Run("another browser session does not share the assertion", func(t *testing.T) {
		other := signInOverHTTP(t, srv, key, userHandle)
		stepUpStamp(t, srv, other, time.Now().UnixMilli()-6*60*1000)
		if code, out := srv.post(t, "/api/send", `{"sessions":["s-1"],"text":"hi"}`, deviceHeaders(other)); code != 401 || out != stepUpRequiredBody {
			t.Errorf("the second session's send = %d %s, want the step-up 401", code, out)
		}
		if code, out := srv.post(t, "/api/send", `{"sessions":["s-1"],"text":"hi"}`, device); code != 200 {
			t.Errorf("the stepped-up session's send = %d %s, want 200", code, out)
		}
	})

	t.Run("the machine's own credentials keep working on loopback beside a paired device", func(t *testing.T) {
		// the one-time-link browser and the root token hold no passkey and need none here: the
		// loopback listener is the machine's own door
		link := linkBrowser(t, srv)
		for _, call := range []struct{ name, path, body string }{
			{"the link browser's answer", "/api/sessions/s-1/answer", `{"text":"hi"}`},
			{"the link browser's send", "/api/send", `{"sessions":["s-1"],"text":"hi"}`},
		} {
			if code, out := srv.post(t, call.path, call.body, link); code != 200 {
				t.Errorf("%s with a device paired = %d %s, want 200", call.name, code, out)
			}
		}
		if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"hi"}`, nil); code != 200 {
			t.Errorf("the root credential's answer with a device paired = %d %s, want 200", code, out)
		}
		// stepping up itself stays a paired device's ceremony: no device cookie, no begin
		if code, out := srv.post(t, "/api/stepup/begin", `{}`, nil); code != 403 {
			t.Errorf("stepup begin without a device session = %d %s, want 403", code, out)
		}
	})
}

// TestStepUpOnTheHubListener pins the network side: a paired device's stale session
// passes the hub gate and still meets the step-up on a dangerous act — the one 401, not the
// gate's — and the assertion it earns on loopback carries it there.
func TestStepUpOnTheHubListener(t *testing.T) {
	listenPort := freePort(t)
	dir := roleHome(t, fmt.Sprintf("role = \"hub\"\n\n[hub]\nlisten = \"[::1]:%d\"\nhost = \"hub.mine.test\"\n", listenPort))
	selfSignedCert(t, filepath.Join(dir, "tls"), "hub.mine.test")
	srv := startServer(t, map[string]string{"HUDDLE_HOME": dir})
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")
	key := webauthntest.NewSoftKey(t)
	setCookie, userHandle := pairDevice(t, srv, key, "iPhone")
	cookie := cookieValueOf(t, setCookie)

	// the assertion aged out on loopback; the hub listener asks for a fresh one
	stepUpStamp(t, srv, cookie, time.Now().UnixMilli()-6*60*1000)
	allowID := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	path := fmt.Sprintf("/api/needs/%d/decide", allowID)
	headers := map[string]string{
		"Cookie":       auth.PairHostCookie + "=" + cookie,
		"content-type": "application/json",
		ActionHeader:   path,
	}
	code, _, out := hubDo(t, srv, http.MethodPost, path, `{"decision":"allow"}`, headers)
	if code != 401 || out != stepUpRequiredBody {
		t.Fatalf("the hub listener's allow on a stale session = %d %s, want 401 %s", code, out, stepUpRequiredBody)
	}

	// the assertion the device earns on loopback is the same row's: the act passes there
	if code, out := stepUpOverHTTP(t, srv, key, cookie, userHandle); code != 200 {
		t.Fatalf("stepup finish = %d: %s", code, out)
	}
	allowID = permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	path = fmt.Sprintf("/api/needs/%d/decide", allowID)
	headers[ActionHeader] = path
	code, _, out = hubDo(t, srv, http.MethodPost, path, `{"decision":"allow"}`, headers)
	if code != 200 {
		t.Fatalf("the hub listener's allow on a fresh assertion = %d %s, want 200", code, out)
	}
}

func TestSoloNeedsNoStepUp(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-1", "", "", "UserPromptSubmit")

	// no device is paired: the one-time link a terminal on this machine minted is the local
	// sign-in that satisfies step-up, and the terminal's own credential acts with it
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"hi"}`, nil); code != 200 {
		t.Fatalf("the root credential's answer = %d %s, want 200", code, out)
	}
	if code, out := srv.post(t, "/api/send", `{"sessions":["s-1"],"text":"hi"}`, nil); code != 200 {
		t.Fatalf("the root credential's send = %d %s, want 200", code, out)
	}
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	if code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil); code != 200 {
		t.Fatalf("the root credential's allow = %d %s, want 200", code, out)
	}

	// the browser the local link signed in
	_, out := srv.post(t, "/api/login", `{}`, nil)
	login := bodyMap(t, out)
	signInRequest, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("%s/?code=%s", srv.base, login["code"].(string)), nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	signedIn, doErr := client.Do(signInRequest)
	if doErr != nil || signedIn.StatusCode != 303 {
		t.Fatalf("the sign-in link failed: %v (want 303)", doErr)
	}
	setCookie := signedIn.Header.Get("Set-Cookie")
	_ = signedIn.Body.Close()
	link := map[string]string{"Cookie": strings.SplitN(setCookie, ";", 2)[0], "x-huddle-token": ""}
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"again"}`, link); code != 200 {
		t.Fatalf("the local browser's answer = %d %s, want 200 while no device is paired", code, out)
	}

	// pairing a device changes nothing for the machine's own credentials on loopback: its
	// browser and the terminal keep answering, and the paired device's own fresh
	// session answers too
	key := webauthntest.NewSoftKey(t)
	pairCookie, _ := pairDevice(t, srv, key, "iPhone")
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"once more"}`, nil); code != 200 {
		t.Errorf("the root credential after the pairing = %d %s, want 200", code, out)
	}
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"once more"}`, link); code != 200 {
		t.Errorf("the local browser after the pairing = %d %s, want 200", code, out)
	}
	if code, out := srv.post(t, "/api/sessions/s-1/answer", `{"text":"once more"}`,
		deviceHeaders(cookieValueOf(t, pairCookie))); code != 200 {
		t.Errorf("the paired device's answer = %d %s, want 200 on its fresh session", code, out)
	}
}
