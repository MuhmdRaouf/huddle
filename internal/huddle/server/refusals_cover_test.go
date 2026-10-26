package server

// The refusals the routes answer before any work happens: numbers that are not ids, bodies that
// are not JSON, methods a path does not take, static names that are not there, and the few
// answers that need their exact shape (an id that overflows, a poll that its client drops, a
// sign-in link that carries more than its code).

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

func TestTheRoutesRefuseShapesTheyCannotActOn(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"}
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		code   int
		want   string
	}{
		{"an item id that overflows", "POST", "/api/needs/99999999999999999999/snooze", `{"until":"15m"}`, 404, "no such item"},
		{"a snooze whose body is not JSON", "POST", "/api/needs/1/snooze", `{not json`, 400, "body is not JSON"},
		{"a permission id that overflows", "GET", "/api/needs/99999999999999999999/wait?timeout=1", "", 404, "no such item"},
		{"a poll taken by POST", "POST", "/api/needs/1/wait", `{}`, 405, "answers GET"},
		{"a decision taken by GET", "GET", "/api/needs/1/decide", "", 405, "answers POST"},
		{"a permission item whose body is not JSON", "POST", "/api/needs", `{not json`, 400, "body is not JSON"},
		{"a decision whose body is not JSON", "POST", "/api/needs/1/decide", `{not json`, 400, "body is not JSON"},
		{"a push ack without its names", "POST", "/api/registry/push-ack", `{}`, 400, "needs channel and as"},
		{"a send whose sessions are not names", "POST", "/api/send", `{"sessions":[1],"text":"hi"}`, 400, "an array of 1 to 20"},
		{"a join whose body is not JSON", "POST", "/api/join", `{not json`, 400, "name must match"},
		{"a channel whose body is not JSON", "POST", "/api/channels", `{not json`, 400, "body is not JSON"},
		{"a channel whose name is not one", "POST", "/api/channels", `{"name":"NOT A NAME"}`, 400, "must match"},
		{"an invite whose ttl is not one", "POST", "/api/tokens", `{"ttl":"banana"}`, 400, "ttl: seconds"},
		{"an MCP body that is not JSON", "POST", "/mcp/team?as=a", `{not json`, 400, "body is not JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, body, err := srv.request(bg, tc.method, tc.path, tc.body, root)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if code != tc.code || !strings.Contains(body, tc.want) {
				t.Fatalf("%s = %d %s, want %d naming %q", tc.name, code, body, tc.code, tc.want)
			}
		})
	}
	// static names that are not there answer their own 404s
	if code, _, body := srv.get(t, "/static/no-such-file.css", nil); code != 404 || !strings.Contains(body, "not found") {
		t.Errorf("a missing static file = %d %s, want the 404", code, body)
	}
	if code, _, body := srv.get(t, "/icons/icon-no-such-size.png", nil); code != 404 || !strings.Contains(body, "missing build artifact") {
		t.Errorf("a missing icon = %d %s, want the artifact 404", code, body)
	}
	// the MCP endpoint names its channel from the header too, and a channel it cannot open
	// answers its refusal as tool text
	mcp := map[string]string{"x-huddle-token": srv.token, "content-type": "application/json", "x-huddle-channel": "NOT A CHANNEL"}
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"join","arguments":{}}}`
	code, _, out, err := srv.request(bg, http.MethodPost, "/mcp?as=stranger", body, mcp)
	if err != nil || code != 200 {
		t.Fatalf("the header-named MCP call = %d (%v)", code, err)
	}
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, `"huddle: `) {
		t.Errorf("the MCP failure = %s, want it as tool error text naming huddle", out)
	}
}

func TestTheMCPToolsAnswerInTheirOwnWords(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.op(t, "mcp", "join", "a", "{}"); code != 200 {
		t.Fatalf("join: %d %v", code, out)
	}
	call := func(params string) string {
		t.Helper()
		reply := srv.rpc(t, "mcp", "a", "tools/call", params, 1)
		result, _ := reply["result"].(map[string]any)
		content, _ := result["content"].([]any)
		first, _ := content[0].(map[string]any)
		return fmt.Sprint(first["text"])
	}
	// a remember names its entry and its title
	if text := call(`{"name": "remember", "arguments": {"kind": "fact", "title": "short answers", "body": "one line each"}}`); !strings.Contains(text, "remembered: short answers") {
		t.Errorf("the remember text = %q", text)
	}
	// a blocking tool takes its wait as a string of seconds too; the call ends when its client
	// stops waiting, and the number was read before the hold began
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = srv.request(ctx, http.MethodPost, "/mcp/mcp?as=a",
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"wait","arguments":{"topics":["never"],"timeout":"45"}}}`,
			map[string]string{"content-type": "application/json", "x-huddle-token": srv.token})
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the blocking call never ended with its client")
	}
}

func TestAStartWhoseDataDirectoryIsAFileSaysItsStorage(t *testing.T) {
	data := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(data, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, map[string]string{"HUDDLE_DATA": data})
	// the server runs, and the health answer names no channels of its own
	if code, _, body := srv.get(t, "/health", nil); code != 200 || !strings.Contains(body, `"channels":0`) {
		t.Fatalf("/health = %d %s, want the server up with no channels", code, body)
	}
}

func TestAWaitPollItsClientDropsEndsQuietly(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/needs", `{"session_id":"s-1","tool_name":"Bash","tool_input":{"command":"npm test"}}`, nil); code != 200 {
		t.Fatalf("post the item: %d %s", code, out)
	}
	id := theQuestionID(t, srv)
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = srv.request(ctx, http.MethodGet, "/api/needs/"+id+"/wait?timeout=30", "",
			map[string]string{"x-huddle-token": srv.token})
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the dropped poll never ended")
	}
	// the item is still open: the hook re-polls
	if items := needsItems(t, srv); len(items) != 1 {
		t.Fatalf("items = %v, want the item still waiting", items)
	}
}

func TestTheWaitRouteClampsItsTimeoutToItsBounds(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/needs", `{"session_id":"s-1","tool_name":"Bash","tool_input":{"command":"npm test"}}`, nil); code != 200 {
		t.Fatalf("post the item: %d %s", code, out)
	}
	id := theQuestionID(t, srv)
	// a timeout of none is one second: the answer lands at once
	started := time.Now()
	code, _, out := srv.get(t, "/api/needs/"+id+"/wait?timeout=0", nil)
	if code != 200 {
		t.Fatalf("wait?timeout=0 = %d %s", code, out)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("wait?timeout=0 held %s, want the one-second floor", elapsed)
	}
	if state := bodyMap(t, out)["state"]; state != "open" {
		t.Errorf("the timed-out wait answered %v, want the item still open", state)
	}
	// a timeout past the cap is capped: the hold ends at thirty seconds, and a client that
	// gives up before that ends its own hold
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, _ = srv.request(ctx, http.MethodGet, "/api/needs/"+id+"/wait?timeout=99", "",
			map[string]string{"x-huddle-token": srv.token})
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the clamped poll never ended when its client gave up")
	}
}

func TestAnAllowPastAnUnreadableConfigSaysTheTerminalDecides(t *testing.T) {
	srv := startServer(t, nil)
	// the config is a directory: it cannot be read, so no remote switch can be checked
	if err := os.MkdirAll(filepath.Join(srv.home, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	id := permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
	code, out := srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id), `{"decision":"allow"}`, nil)
	if code != 403 || !strings.Contains(out, "cannot be read") {
		t.Fatalf("an allow past an unreadable config = %d %s, want the 403", code, out)
	}
}

func TestASignInLinkCarriesOnlyItsCode(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	_, out := srv.post(t, "/api/login", "{}", root)
	login := bodyMap(t, out)
	request, _ := http.NewRequest(http.MethodGet, srv.base+"/?code="+login["code"].(string)+"&page=2", nil)
	request.Header.Set("x-huddle-token", srv.token)
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bufio.NewReader(response.Body).ReadString('\n')
	_ = response.Body.Close()
	if response.Header.Get("Location") != "/?page=2" {
		t.Errorf("location = %q, want the rest of the query kept", response.Header.Get("Location"))
	}
}

func TestTheLoginCeremonyRefusesTheShapesItCannotUse(t *testing.T) {
	srv := startServer(t, nil)
	// a finish without a response, and one whose response is not an assertion
	if code, out := srv.post(t, "/api/login/finish", `{}`, nil); code != 400 || !strings.Contains(out, "passkey response") {
		t.Errorf("a login finish without a response = %d %s, want the 400", code, out)
	}
	if code, out := srv.post(t, "/api/login/finish", `{"response":{"no":"assertion"}}`, nil); code != 400 && code != 401 {
		t.Errorf("a login finish of a made-up response = %d %s, want a refusal", code, out)
	}
	// a pairing finish without its response
	if code, out := srv.post(t, "/api/pair/finish", `{"token":"A234567890BCDEFGHIJKLMNOPQR"}`, nil); code != 400 || !strings.Contains(out, "token and a passkey response") {
		t.Errorf("a pairing finish without its response = %d %s, want the 400", code, out)
	}
	// a pairing begin whose body is not JSON
	if code, out := srv.post(t, "/api/pair/begin", `{not json`, nil); code != 400 || !strings.Contains(out, "body is not JSON") {
		t.Errorf("a pairing begin past garbage = %d %s, want the 400", code, out)
	}
	// a step-up finish from a paired device that carries no response
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "Pad")
	device := map[string]string{
		"content-type": "application/json", "x-huddle-token": "",
		"Cookie": auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie),
	}
	if code, out := srv.post(t, "/api/stepup/finish", `{}`, device); code != 400 || !strings.Contains(out, "passkey response") {
		t.Errorf("a step-up finish without a response = %d %s, want the 400", code, out)
	}
}

func TestAStepUpFromAnythingButAPairedDeviceIsRefused(t *testing.T) {
	srv := startServer(t, nil)
	// the root token is not a device: neither ceremony runs for it
	if code, out := srv.post(t, "/api/stepup/begin", `{}`, nil); code != 403 || !strings.Contains(out, "paired device") {
		t.Errorf("a step-up begin from the root = %d %s, want the 403", code, out)
	}
	if code, out := srv.post(t, "/api/stepup/finish", `{"response":{}}`, nil); code != 403 || !strings.Contains(out, "paired device") {
		t.Errorf("a step-up finish from the root = %d %s, want the 403", code, out)
	}
	// a device that holds a cookie but no ceremony gets its refusal at the verification
	setCookie, _ := pairDevice(t, srv, webauthntest.NewSoftKey(t), "Phone")
	cookie := auth.PairPlainCookie + "=" + cookieValueOf(t, setCookie)
	headers := map[string]string{"content-type": "application/json", "x-huddle-token": "", "Cookie": cookie}
	code, _, out, err := srv.request(bg, http.MethodPost, "/api/stepup/finish", `{"response":{"no":"assertion"}}`, headers)
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil || answer.Error == "" {
		t.Errorf("a made-up assertion = %d %s, want the verification's error", code, out)
	}
}
