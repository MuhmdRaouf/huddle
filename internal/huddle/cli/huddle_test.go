// The huddle command itself: its guards, its admin calls, the operations it forwards, and the
// text it prints, against a scripted Huddle that records every request.
package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCall is one request the CLI made.
type fakeCall struct {
	Method string
	Path   string
	Query  string
	Body   string
	Token  string
}

// fakeHuddle is a scripted Huddle server: it records what the CLI asked and answers from the
// test's own function.
type fakeHuddle struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []fakeCall
	answer func(call fakeCall) (int, string)
}

// startFakeHuddle answers from answer until the test ends.
func startFakeHuddle(t *testing.T, answer func(call fakeCall) (int, string)) *fakeHuddle {
	t.Helper()
	fake := &fakeHuddle{answer: answer}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		call := fakeCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(raw), Token: r.Header.Get("x-huddle-token")}
		fake.mu.Lock()
		fake.calls = append(fake.calls, call)
		fake.mu.Unlock()
		status, reply := answer(call)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// asked lists the recorded calls.
func (f *fakeHuddle) asked() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCall{}, f.calls...)
}

// lastCall is the most recent call.
func (f *fakeHuddle) lastCall() fakeCall {
	calls := f.asked()
	if len(calls) == 0 {
		return fakeCall{}
	}
	return calls[len(calls)-1]
}

// huddleWorld is one CLI run's world: a session of channel c as a, against url.
func huddleWorld(t *testing.T, url string, env map[string]string) *World {
	t.Helper()
	vars := map[string]string{
		"HUDDLE_URL":     url,
		"HUDDLE_CHANNEL": "c",
		"HUDDLE_AS":      "a",
		"HUDDLE_SESSION": "s",
		"XDG_STATE_HOME": filepath.Join(t.TempDir(), "state"),
	}
	for key, value := range env {
		if value == "" {
			delete(vars, key)
			continue
		}
		vars[key] = value
	}
	return world(t, t.TempDir(), vars)
}

// runH runs one huddle command in w and answers its exit code and both outputs.
func runH(t *testing.T, w *World, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := Huddle(context.Background(), w, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// okJSON is the answer of an operation that says one line.
func okJSON(text string) (int, string) {
	return 200, `{"result":{"seq":9},"text":` + quoteJSON(text) + `}`
}

// quoteJSON quotes s as a JSON string.
func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// help prints the command block, the same lines the source's comment carries.
func TestHuddleHelpPrintsTheCommandsBlock(t *testing.T) {
	w := huddleWorld(t, "http://127.0.0.1:1", nil)
	for _, cmd := range []string{"help", "-h"} {
		code, out, errOut := runH(t, w, cmd)
		if code != 0 || errOut != "" {
			t.Fatalf("%s = %d %q", cmd, code, errOut)
		}
		if !strings.HasPrefix(out, "Background subagents have Bash but no MCP tools") {
			t.Errorf("help starts with %q", out[:min(60, len(out))])
		}
		for _, want := range []string{
			"huddle join [--role \"…\"] [--fresh|--sync]",
			"huddle token create [--ttl 24h|0]",
			"huddle up [--port n] | down | server",
			"Exit: 0 ok · 2 usage/error · 3 message · 4 paused · 5 Huddle unreachable · 124 timeout.",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("help lacks %q", want)
			}
		}
		if !strings.HasSuffix(out, "\n") {
			t.Error("help does not end with a newline")
		}
	}
}

// whoami prints the identity, compact.
func TestHuddleWhoamiPrintsTheIdentityCompact(t *testing.T) {
	w := huddleWorld(t, "http://127.0.0.1:1", nil)
	code, out, errOut := runH(t, w, "whoami")
	if code != 0 || errOut != "" {
		t.Fatalf("whoami = %d %q", code, errOut)
	}
	wantPrefix := `{"url":"http://127.0.0.1:1","source":"env","channel":"c","as":"a","push":[`
	if !strings.HasPrefix(out, wantPrefix) {
		t.Errorf("whoami = %q", out)
	}
	if !strings.Contains(out, `"listen":[],"wait":1500,"context":"auto","autostart":true,"detail":"digest"}`) {
		t.Errorf("whoami = %q", out)
	}
}

// A session with no channel, or with no url, is told how to get into one.
func TestHuddleAGuardNamesWhatIsMissing(t *testing.T) {
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_URL": "http://127.0.0.1:1"})
	code, _, errOut := runH(t, w, "send", "hi")
	if code != 2 || !strings.HasPrefix(errOut, "huddle: not in a huddle: ") {
		t.Fatalf("no channel: %d %q", code, errOut)
	}
	if !strings.Contains(errOut, "/huddle:invite") || !strings.Contains(errOut, "/huddle:setup") {
		t.Errorf("the guard gives no way in: %q", errOut)
	}
	w2 := world(t, t.TempDir(), map[string]string{"HUDDLE_CHANNEL": "c", "HUDDLE_AS": "a"})
	code, _, errOut = runH(t, w2, "send", "hi")
	if code != 5 || !strings.Contains(errOut, "Huddle has no port here yet: start it with huddle up") {
		t.Fatalf("no url: %d %q", code, errOut)
	}
}

// token create, list and delete; the ttl's shapes.
func TestHuddleTokenCreateListDelete(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Method == "POST" && call.Path == "/api/tokens":
			return 200, `{"token":"abc123.0123456789abcdef","id":"abc123","expires":null}`
		case call.Method == "GET" && call.Path == "/api/tokens":
			return 200, `[{"id":"abc123","expires":null,"single_use":true,"uses":1,"can_invite":true,"channel":"c","description":"for you"}]`
		case call.Method == "DELETE" && call.Path == "/api/tokens/abc123":
			return 200, `{}`
		}
		return 404, `{"error":"nope"}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, out, errOut := runH(t, w, "token", "create", "--ttl", "1h")
	if code != 0 || errOut != "" {
		t.Fatalf("create: %d %q", code, errOut)
	}
	if out != "abc123.0123456789abcdef  (id abc123, expires never)\n" {
		t.Errorf("create said %q", out)
	}
	if body := fake.lastCall().Body; body != `{"ttl":3600,"single_use":false,"can_invite":false,"channel":"c"}` {
		t.Errorf("create body %q", body)
	}
	// a join line instead of the token, with and without single use
	code, out, _ = runH(t, w, "token", "create", "--print-join-command")
	if code != 0 {
		t.Fatalf("print join: %d", code)
	}
	if out != "join: /huddle:join 127.0.0.1"+strings.TrimPrefix(fake.server.URL, "http://127.0.0.1")+" --token abc123.0123456789abcdef   (valid forever)\n" {
		t.Errorf("join line %q", out)
	}
	code, out, _ = runH(t, w, "token", "create", "--print-join-command", "--single-use")
	if !strings.HasSuffix(out, "   (valid forever, single use)\n") {
		t.Errorf("single use line %q", out)
	}
	code, _, errOut = runH(t, w, "token", "create", "--ttl", "banana")
	if code != 2 || errOut != "huddle: --ttl banana: a number with s, m, h or d (0 = never expires)\n" {
		t.Fatalf("bad ttl: %d %q", code, errOut)
	}
	// ttl shapes: plain seconds, m, d, 0
	for _, c := range []struct{ given, want string }{
		{"90m", `"ttl":5400`}, {"2d", `"ttl":172800`}, {"45", `"ttl":45`}, {"45s", `"ttl":45`}, {"0", `"ttl":0`},
	} {
		runH(t, w, "token", "create", "--ttl", c.given)
		if !strings.Contains(fake.lastCall().Body, c.want) {
			t.Errorf("ttl %s gave %q", c.given, fake.lastCall().Body)
		}
	}
	code, out, _ = runH(t, w, "token", "list")
	if code != 0 {
		t.Fatalf("list: %d", code)
	}
	if out != "abc123  expires never  single-use  uses 1  can-invite  channel c  for you\n" {
		t.Errorf("list said %q", out)
	}
	code, out, _ = runH(t, w, "token", "delete", "abc123.secret")
	if code != 0 {
		t.Fatalf("delete: %d", code)
	}
	if out != "token abc123 deleted\n" {
		t.Errorf("delete said %q", out)
	}
	if last := fake.lastCall(); last.Method != "DELETE" || last.Path != "/api/tokens/abc123" {
		t.Errorf("delete called %s %s", last.Method, last.Path)
	}
	code, _, errOut = runH(t, w, "token", "bogus")
	if code != 2 || errOut != "huddle: token create|list|delete\n" {
		t.Fatalf("unknown token sub: %d %q", code, errOut)
	}
}

// members and kick, against the admin API.
func TestHuddleMembersAndKick(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Method == "GET" && call.Path == "/api/members":
			return 200, `[{"name":"dev","since":"2026-10-15T10:00:00.000Z","seen":"2026-10-15T12:00:00.000Z","invite":"abc123","can_invite":true}]`
		case call.Method == "DELETE" && call.Path == "/api/members/dev":
			return 200, `{}`
		}
		return 404, `{"error":"nope"}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, out, _ := runH(t, w, "members")
	if code != 0 {
		t.Fatalf("members: %d", code)
	}
	if out != "dev  since 2026-10-15T10:00:00.000Z  last seen 2026-10-15T12:00:00.000Z  invite abc123  can-invite\n" {
		t.Errorf("members said %q", out)
	}
	code, out, _ = runH(t, w, "kick", "dev")
	if code != 0 {
		t.Fatalf("kick: %d", code)
	}
	if out != "dev kicked: its credential no longer works\n" {
		t.Errorf("kick said %q", out)
	}
	empty := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, `[]` })
	w2 := huddleWorld(t, empty.server.URL, nil)
	_, out, _ = runH(t, w2, "members")
	if out != "no members (the creator holds the root credential)\n" {
		t.Errorf("empty members said %q", out)
	}
}

// open prints the dashboard link, or says why there is none.
func TestHuddleOpenPrintsTheDashboardLinkOrExplains(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Path == "/api/login" {
			return 200, `{"code":"the code"}`
		}
		return 403, `{"error":"no"}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, out, _ := runH(t, w, "open")
	if code != 0 {
		t.Fatalf("open: %d", code)
	}
	if out != "dashboard: "+fake.server.URL+"/?code=the%20code   (signs your browser in once, within 5 min)\n" {
		t.Errorf("open said %q", out)
	}
	refuses := startFakeHuddle(t, func(call fakeCall) (int, string) { return 403, `{"error":"no"}` })
	w2 := huddleWorld(t, refuses.server.URL, nil)
	code, _, errOut := runH(t, w2, "open")
	if code != 2 || !strings.Contains(errOut, "this session is not in a huddle here: join with the command its owner gives you (/huddle:join <host:port> --token …)") {
		t.Fatalf("refused open: %d %q", code, errOut)
	}
	noURL := world(t, t.TempDir(), map[string]string{"HUDDLE_CHANNEL": "c", "HUDDLE_AS": "a"})
	code, _, errOut = runH(t, noURL, "open")
	if code != 5 || !strings.Contains(errOut, "Huddle has no port here yet") {
		t.Fatalf("open with no url: %d %q", code, errOut)
	}
}

// An invite joins: this session gets its own credential, the invite's channel wins, and the
// session then joins it.
func TestHuddleJoinWithAnInviteKeepsItsOwnCredentialAndJoins(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		switch {
		case call.Method == "POST" && call.Path == "/api/join":
			return 200, `{"name":"shop-2","channel":"team","credential":"hcred_member"}`
		case call.Method == "POST" && call.Path == "/api/c/team/op/join":
			return okJSON("you are shop-2 in team")
		case call.Path == "/api/login":
			return 200, `{"code":"c1"}`
		}
		return 404, `{"error":"nope"}`
	})
	w := world(t, t.TempDir(), map[string]string{
		"HUDDLE_SESSION": "s1",
		"HUDDLE_CHANNEL": "mine",
	})
	code, out, errOut := runH(t, w, "join", strings.TrimPrefix(fake.server.URL, "http://"), "--token", "abc123.secret")
	if code != 0 {
		t.Fatalf("join: %d %q %q", code, out, errOut)
	}
	if !strings.Contains(errOut, "huddle: joined "+fake.server.URL+" as shop-2; this project's next sessions are in it too (the invite is not kept)") {
		t.Errorf("join said %q", errOut)
	}
	if !strings.Contains(errOut, "huddle: joined channel team (the invite's); this project's file named mine") {
		t.Errorf("the channel note %q", errOut)
	}
	if !strings.Contains(errOut, "dashboard: "+fake.server.URL+"/?code=c1") {
		t.Errorf("the dashboard link %q", errOut)
	}
	cred := readFile(t, filepath.Join(w.getenv("HOME"), ".config", "huddle", "sessions", "s1.json"))
	if !strings.Contains(cred, `"channel":"team"`) || strings.Contains(cred, "abc123.secret") || !strings.Contains(cred, "hcred_member") {
		t.Errorf("the credential file %q", cred)
	}
	if out != "you are shop-2 in team\n" {
		t.Errorf("the join's own output %q", out)
	}
	for _, call := range fake.asked() {
		if call.Path == "/api/join" && call.Token != "" {
			t.Errorf("the invite join sent a credential")
		}
	}
	if last := fake.lastCall(); last.Path != "/api/c/team/op/join" || last.Token != "hcred_member" {
		t.Errorf("the join op %s %s with %q", last.Method, last.Path, last.Token)
	}
}

// Every switch command forwards the operation with the body the server expects.
func TestHuddleOpCommandsBuildTheirBodies(t *testing.T) {
	tests := []struct {
		name string
		args []string
		op   string
		body string
	}{
		{"join with a role and task", []string{"join", "--role", "r", "the task"}, "join", `{"role":"r","task":"the task"}`},
		{"join fresh", []string{"join", "--fresh"}, "join", `{"role":"","context":"fresh"}`},
		{"map", []string{"map", "--json"}, "map", `{"json":true}`},
		{"assign fresh with a task", []string{"assign", "s", "--fresh", "--task", "t"}, "assign", `{"session":"s","context":"fresh","task":"t"}`},
		{"assign default", []string{"assign", "s"}, "assign", `{"session":"s","context":null}`},
		{"brief", []string{"brief", "s", "the text"}, "brief", `{"session":"s","msg":"the text"}`},
		{"ack", []string{"ack", "7"}, "ack", `{"seq":7}`},
		{"send to one", []string{"send", "beta", "hi", "--ask"}, "send", `{"to":"beta","msg":"hi","ask":true}`},
		{"send to everyone", []string{"send", "hi"}, "send", `{"msg":"hi","ask":false}`},
		{"send with --all keeps the name in the message", []string{"send", "beta", "hi", "--all"}, "send", `{"msg":"beta hi","ask":false}`},
		{"send --to wins", []string{"send", "beta", "hi", "--to", "gamma"}, "send", `{"to":"gamma","msg":"hi","ask":false}`},
		{"send --task", []string{"send", "hi", "--task", "t1"}, "send", `{"msg":"hi","ask":false,"task":"t1"}`},
		{"reply", []string{"reply", "5", "yes"}, "reply", `{"seq":5,"msg":"yes"}`},
		{"reply defaults to ack", []string{"reply", "5"}, "reply", `{"seq":5,"msg":"ack"}`},
		{"pub", []string{"pub", "build.ready", "api", "all green"}, "publish", `{"topic":"build.ready","ref":"api","msg":"all green","ask":false}`},
		{"doing with a note", []string{"doing", "t1", "started"}, "task_status", `{"id":"t1","status":"doing","note":"started"}`},
		{"done bare", []string{"done", "t1"}, "task_status", `{"id":"t1","status":"done","note":""}`},
		{"new", []string{"new", "Ship it", "--owner", "b", "--after", "a,b"}, "task_create", `{"title":"Ship it","owner":"b","after":["a","b"]}`},
		{"new with an id", []string{"new", "Ship it", "--id", "api"}, "task_create", `{"title":"Ship it","id":"api"}`},
		{"start names the task", []string{"start", "t1"}, "start", `{"id":"t1"}`},
		{"start takes the next", []string{"start"}, "start", `{}`},
		{"finish", []string{"finish", "tests green", "--id", "t1", "--result", "r"}, "finish", `{"id":"t1","note":"tests green","result":"r"}`},
		{"remember", []string{"remember", "fact", "a fact", "the body", "goes on", "--tags", "a,b"}, "remember", `{"kind":"fact","title":"a fact","body":"the body goes on","tags":["a","b"],"force":false}`},
		{"verify undo", []string{"verify", "3", "--undo"}, "verify", `{"id":3,"undo":true}`},
		{"verify", []string{"verify", "3"}, "verify", `{"id":3,"undo":false}`},
		{"share", []string{"share", "3"}, "share", `{"id":3}`},
		{"recall", []string{"recall", "some words"}, "recall", `{"q":"some words"}`},
		{"recall with a kind and limit", []string{"recall", "words", "--kind", "fact", "--limit", "3"}, "recall", `{"q":"words","kind":"fact","limit":3}`},
		{"kb", []string{"kb", "3"}, "kb", `{"id":3}`},
		{"task", []string{"task", "t1", "--json"}, "task", `{"id":"t1","json":true}`},
		{"pause", []string{"pause", "s", "the why"}, "pause", `{"target":"s","why":"the why"}`},
		{"resume", []string{"resume", "s"}, "resume", `{"target":"s","why":""}`},
		{"pass", []string{"pass", "to", "a note"}, "pass", `{"to":"to","msg":"a note"}`},
		{"leave", []string{"leave", "the summary"}, "leave", `{"summary":"the summary"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got fakeCall
			fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
				got = call
				return okJSON("fine")
			})
			w := huddleWorld(t, fake.server.URL, nil)
			code, out, errOut := runH(t, w, tt.args...)
			if code != 0 || errOut != "" {
				t.Fatalf("huddle %v = %d %q", tt.args, code, errOut)
			}
			if out != "fine\n" {
				t.Errorf("out %q", out)
			}
			if got.Path != "/api/c/c/op/"+tt.op || got.Body != tt.body {
				t.Errorf("called %s %s with %q, want op %s body %q", got.Method, got.Path, got.Body, tt.op, tt.body)
			}
		})
	}
}

// An operation's one-line text is printed when the server sends one; without it the result, as
// JSON (indented with --pretty).
func TestHuddleOutPrintsTheTextElseTheResult(t *testing.T) {
	withText := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, `{"result":{"seq":1},"text":"one line"}`
	})
	w := huddleWorld(t, withText.server.URL, nil)
	_, out, _ := runH(t, w, "status")
	if out != "one line\n" {
		t.Errorf("the text %q", out)
	}
	textless := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, `{"result":{"a":1,"nested":{"b":[true,null]}},"text":null}`
	})
	w2 := huddleWorld(t, textless.server.URL, nil)
	_, out, _ = runH(t, w2, "status")
	if out != `{"a":1,"nested":{"b":[true,null]}}`+"\n" {
		t.Errorf("the result %q", out)
	}
	code, out, _ := runH(t, w2, "status", "--pretty")
	if code != 0 {
		t.Fatalf("pretty: %d", code)
	}
	if out != "{\n \"a\": 1,\n \"nested\": {\n  \"b\": [\n   true,\n   null\n  ]\n }\n}\n" {
		t.Errorf("pretty result %q", out)
	}
}

// --as runs the operation as another session, merged last into the body.
func TestHuddleAsRunsTheOperationAsAnotherSession(t *testing.T) {
	var body string
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		body = call.Body
		return okJSON("fine")
	})
	w := huddleWorld(t, fake.server.URL, nil)
	if code, _, errOut := runH(t, w, "send", "hi", "--as", "parent.role"); code != 0 || errOut != "" {
		t.Fatalf("send --as: %q", errOut)
	}
	if body != `{"msg":"hi","ask":false,"as":"parent.role"}` {
		t.Errorf("body %q", body)
	}
}

// wait exits by what it saw: 0 an event, 3 a message for this session, 124 a timeout.
func TestHuddleWaitExitsByWhatItSaw(t *testing.T) {
	tests := []struct {
		name   string
		answer string
		code   int
	}{
		{"an event", `{"result":{"kind":"event","topic":"go.now"}}`, 0},
		{"a message", `{"result":{"kind":"message","msg":"for you"}}`, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, tt.answer })
			w := huddleWorld(t, fake.server.URL, nil)
			code, out, errOut := runH(t, w, "wait", "--timeout", "5")
			if code != tt.code || errOut != "" {
				t.Fatalf("wait = %d %q, want %d", code, errOut, tt.code)
			}
			if !strings.HasPrefix(out, `{"kind":`) {
				t.Errorf("wait printed %q", out)
			}
		})
	}
	// a timeout past the deadline: successive bounded waits, then 124
	count := 0
	now := time.Unix(1_000_000, 0)
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		count++
		now = now.Add(400 * time.Millisecond) // the clock advances with each round trip
		return 200, `{"result":{"kind":"timeout"}}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	w.Now = func() time.Time { return now }
	code, out, _ := runH(t, w, "wait", "--timeout", "1")
	if code != 124 {
		t.Fatalf("a timed-out wait exited %d", code)
	}
	if out != `{"kind":"timeout"}`+"\n" {
		t.Errorf("the timeout printed %q", out)
	}
	if count < 2 {
		t.Errorf("the wait ran %d bounded waits, want successive ones", count)
	}
}

// gate --no-block answers 4 while paused; a blocking gate dies on the server's refusal.
func TestHuddleGateNoBlockAndBlockingRefusal(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, `{"result":{"control":"pause","by":"owner"}}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, out, _ := runH(t, w, "gate", "--no-block")
	if code != 4 {
		t.Fatalf("a paused gate exited %d", code)
	}
	if out != `{"control":"pause","by":"owner"}`+"\n" {
		t.Errorf("the gate printed %q", out)
	}
	refuses := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, `{"error":"you may not"}` })
	w2 := huddleWorld(t, refuses.server.URL, nil)
	code, _, errOut := runH(t, w2, "gate")
	if code != 2 || errOut != "huddle: you may not\n" {
		t.Fatalf("a refused gate: %d %q", code, errOut)
	}
}

// ask prints the answer, or times out; depend and handoff exit by what woke them.
func TestHuddleAskDependAndHandoffExits(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		answer string
		code   int
		out    string
	}{
		{"ask answered", []string{"ask", "b", "ready?"}, `{"result":{"answer":"yes"}}`, 0, "yes\n"},
		{"ask unanswered", []string{"ask", "b", "ready?", "--timeout", "1"}, `{"result":{"kind":"timeout"}}`, 124, `{"kind":"timeout"}` + "\n"},
		{"depend woken by an event", []string{"depend", "b", "the task"}, `{"result":{"woke":{"kind":"event"}}}`, 0, `{"woke":{"kind":"event"}}` + "\n"},
		{"depend timed out", []string{"depend", "b", "the task"}, `{"result":{"woke":{"kind":"timeout"}}}`, 124, `{"woke":{"kind":"timeout"}}` + "\n"},
		{"depend not woken", []string{"depend", "b", "the task"}, `{"result":{}}`, 0, `{}` + "\n"},
		{"handoff woken by an event", []string{"handoff", "b", "over"}, `{"result":{"woke":{"kind":"event"}}}`, 0, `{"woke":{"kind":"event"}}` + "\n"},
		{"handoff woken by a message", []string{"handoff", "b", "over"}, `{"result":{"woke":{"kind":"message"}}}`, 3, `{"woke":{"kind":"message"}}` + "\n"},
		{"handoff timed out", []string{"handoff", "b", "over"}, `{"result":{"woke":{"kind":"timeout"}}}`, 124, `{"woke":{"kind":"timeout"}}` + "\n"},
		{"handoff not woken", []string{"handoff", "b", "over"}, `{"result":{}}`, 0, `{}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return 200, tt.answer })
			w := huddleWorld(t, fake.server.URL, nil)
			code, out, errOut := runH(t, w, tt.args...)
			if code != tt.code || errOut != "" {
				t.Fatalf("huddle %v = %d %q, want %d", tt.args, code, errOut, tt.code)
			}
			if out != tt.out {
				t.Errorf("out %q, want %q", out, tt.out)
			}
		})
	}
}

// The missing-argument messages, one per command that needs one.
func TestHuddleMissingArgumentsAreUsageErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"assign"}, "huddle: assign <session> --fresh|--sync|--default [--task id]"},
		{[]string{"brief"}, "huddle: brief <session> <text>"},
		{[]string{"brief", "s"}, "huddle: brief <session> <text>"},
		{[]string{"wait-task"}, "huddle: wait-task <id>"},
		{[]string{"ack"}, "huddle: ack <seq>"},
		{[]string{"pub", "t", "r"}, "huddle: pub <topic> <ref> <msg>"},
		{[]string{"doing"}, "huddle: doing <task id> [note]"},
		{[]string{"finish"}, "huddle: finish <evidence>"},
		{[]string{"depend"}, "huddle: depend <session> <title>"},
		{[]string{"depend", "s"}, "huddle: depend <session> <title>"},
		{[]string{"handoff"}, "huddle: handoff <to> <msg>"},
		{[]string{"handoff", "to"}, "huddle: handoff <to> <msg>"},
		{[]string{"ask"}, "huddle: ask <session> <question>"},
		{[]string{"ask", "s"}, "huddle: ask <session> <question>"},
		{[]string{"remember", "fact"}, "huddle: remember <kind> <title> <body>"},
		{[]string{"verify"}, "huddle: verify <id> [--undo]"},
		{[]string{"share"}, "huddle: share <id>"},
		{[]string{"kb"}, "huddle: kb <id>"},
		{[]string{"task"}, "huddle: task <id>"},
		{[]string{"pause"}, "huddle: pause <session>"},
		{[]string{"pass"}, "huddle: pass <to> [msg]"},
		{[]string{"token", "delete"}, "huddle: token delete <id>"},
		{[]string{"kick"}, "huddle: kick <name>"},
		{[]string{"join", "127.0.0.1:9"}, "huddle: join <host:port> --token <id.secret>"},
	}
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) { return okJSON("fine") })
	w := huddleWorld(t, fake.server.URL, nil)
	for _, tt := range tests {
		code, _, errOut := runH(t, w, tt.args...)
		if code != 2 {
			t.Errorf("huddle %v = %d, want 2", tt.args, code)
		}
		if errOut != tt.want+"\n" {
			t.Errorf("huddle %v said %q, want %q", tt.args, errOut, tt.want)
		}
	}
}

// ops lists the server's tools, the name padded.
func TestHuddleOpsListsTheTools(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		return 200, `{"result":{"tools":[{"name":"send","description":"Send a message"},{"name":"task_create","description":"Create a task"}]}}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, out, errOut := runH(t, w, "ops")
	if code != 0 || errOut != "" {
		t.Fatalf("ops: %d %q", code, errOut)
	}
	if out != "send         Send a message\ntask_create  Create a task\n" {
		t.Errorf("ops said %q", out)
	}
	if last := fake.lastCall(); last.Path != "/mcp/c" || !strings.Contains(last.Body, `"method":"tools/list"`) {
		t.Errorf("ops called %s with %q", last.Path, last.Body)
	}
	unreachable := huddleWorld(t, "http://127.0.0.1:1", nil)
	code, _, errOut = runH(t, unreachable, "ops")
	if code != 5 || errOut != "huddle: unreachable\n" {
		t.Fatalf("ops unreachable: %d %q", code, errOut)
	}
}

// knowledge export writes the markdown as it came.
func TestHuddleKnowledgeExportWritesTheBody(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if call.Query == "verified=1" {
			return 200, "### Facts\n\n- one\n"
		}
		return 200, "### Facts\n\n- one\n- unverified\n"
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, out, errOut := runH(t, w, "knowledge", "export", "--verified")
	if code != 0 || errOut != "" {
		t.Fatalf("export: %d %q", code, errOut)
	}
	if out != "### Facts\n\n- one\n" {
		t.Errorf("export wrote %q", out)
	}
	if last := fake.lastCall(); last.Path != "/api/c/c/knowledge.md" {
		t.Errorf("export called %s", last.Path)
	}
	code, _, errOut = runH(t, w, "knowledge", "nope")
	if code != 2 || errOut != "huddle: knowledge export [--verified]\n" {
		t.Fatalf("knowledge nope: %d %q", code, errOut)
	}
}

// Any operation by name: --key value pairs, numbers and booleans parsed, the list fields split,
// the plain words as the message.
func TestHuddleDefaultOpCoercesNumbersBooleansAndLists(t *testing.T) {
	var path, body string
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		path, body = call.Path, call.Body
		return okJSON("fine")
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, _, errOut := runH(t, w, "sessions", "--count", "3", "--name", "x", "--on", "true", "--off", "false",
		"--topics", "a, b", "the message", "goes here")
	if code != 0 || errOut != "" {
		t.Fatalf("sessions: %d %q", code, errOut)
	}
	if path != "/api/c/c/op/sessions" {
		t.Errorf("the op path %q", path)
	}
	if body != `{"count":3,"name":"x","on":true,"off":false,"topics":["a","b"],"msg":"the message goes here"}` {
		t.Errorf("the body %q", body)
	}
	// a dashed command name runs the underscored operation
	runH(t, w, "task-graph", "--x", "1")
	if path != "/api/c/c/op/task_graph" {
		t.Errorf("the dashed op path %q", path)
	}
	// a message already given is not overwritten by the plain words
	runH(t, w, "status", "--msg", "given", "words")
	if body != `{"msg":"given"}` {
		t.Errorf("the kept message body %q", body)
	}
}

// A server that refuses an unknown operation names the command, the way help would; a paused
// channel is exit 4.
func TestHuddleNoCommandOrOperationComesFromTheServer(t *testing.T) {
	fake := startFakeHuddle(t, func(call fakeCall) (int, string) {
		op := strings.TrimPrefix(call.Path, "/api/c/c/op/")
		return 404, `{"error":"no operation ` + op + `"}`
	})
	w := huddleWorld(t, fake.server.URL, nil)
	code, _, errOut := runH(t, w, "sessions")
	if code != 2 || errOut != "huddle: no command or operation \"sessions\" (huddle help lists them)\n" {
		t.Fatalf("unknown op: %d %q", code, errOut)
	}
	code, _, errOut = runH(t, w, "nope-x")
	if code != 2 || errOut != "huddle: no command or operation \"nope-x\" (huddle help lists them)\n" {
		t.Fatalf("a dashed unknown op: %d %q", code, errOut)
	}
	// a refusal that is not about this operation passes through; 423 means paused
	refuses := startFakeHuddle(t, func(call fakeCall) (int, string) {
		if strings.Contains(call.Body, `"msg"`) {
			return 423, `{"error":"paused"}`
		}
		return 404, `{"error":"unknown operation other"}`
	})
	w2 := huddleWorld(t, refuses.server.URL, nil)
	code, _, errOut = runH(t, w2, "send", "hi")
	if code != 4 || errOut != "huddle: paused\n" {
		t.Fatalf("paused: %d %q", code, errOut)
	}
	code, _, errOut = runH(t, w2, "status")
	if code != 2 || errOut != "huddle: unknown operation other\n" {
		t.Fatalf("a mismatched refusal: %d %q", code, errOut)
	}
}

// An unreachable Huddle is exit 5, with the way to start it.
func TestHuddleUnreachableIsFive(t *testing.T) {
	w := huddleWorld(t, "http://127.0.0.1:1", nil)
	for _, args := range [][]string{
		{"send", "hi"}, {"members"}, {"token", "list"}, {"status"},
	} {
		code, _, errOut := runH(t, w, args...)
		if code != 5 {
			t.Errorf("huddle %v = %d %q, want 5", args, code, errOut)
		}
		if !strings.Contains(errOut, "Huddle unreachable at http://127.0.0.1:1") {
			t.Errorf("huddle %v said %q", args, errOut)
		}
	}
}
