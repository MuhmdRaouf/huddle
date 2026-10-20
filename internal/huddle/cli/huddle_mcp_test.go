package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// mcpRootToken is the root credential of every in-process Huddle the stdio bridge suite runs.
const mcpRootToken = "test-root-credential-for-the-bridge"

// mcpLog collects the bridge's stdout: every written line whole, plus the raw bytes for
// byte-for-byte comparisons.
type mcpLog struct {
	mu    sync.Mutex
	raw   []byte
	lines []string
}

func (l *mcpLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.raw = append(l.raw, p...)
	for _, line := range strings.Split(string(p), "\n") {
		if line != "" {
			l.lines = append(l.lines, line)
		}
	}
	return len(p), nil
}

func (l *mcpLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.raw)
}

// find answers the first line matching, decoded.
func (l *mcpLog) find(keep func(*jsfmt.Value) bool) *jsfmt.Value {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		value, err := jsfmt.DecodeValue([]byte(line))
		if err != nil {
			continue
		}
		if keep(value) {
			return value
		}
	}
	return nil
}

// findAll answers every line matching, decoded.
func (l *mcpLog) findAll(keep func(*jsfmt.Value) bool) []*jsfmt.Value {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []*jsfmt.Value{}
	for _, line := range l.lines {
		value, err := jsfmt.DecodeValue([]byte(line))
		if err != nil {
			continue
		}
		if keep(value) {
			out = append(out, value)
		}
	}
	return out
}

// mcpBridgeRun is one bridge session the test drives over a pipe, the way Claude Code does.
type mcpBridgeRun struct {
	t      *testing.T
	in     io.WriteCloser
	out    *mcpLog
	cancel context.CancelFunc
	done   chan int
}

// runMcpBridge starts one bridge session in a throwaway project, with the initialize and
// notifications/initialized lines the harness sends first already sent.
func runMcpBridge(t *testing.T, version string, env map[string]string) *mcpBridgeRun {
	t.Helper()
	if version == "" {
		version = "test"
	}
	proj := t.TempDir()
	vars := map[string]string{
		"HOME":                   t.TempDir(),
		"CLAUDE_PROJECT_DIR":     proj,
		"XDG_STATE_HOME":         t.TempDir(),
		"HUDDLE_NO_PROJECT_CRED": "1",
	}
	for key, value := range env {
		if value == "" {
			delete(vars, key)
			continue
		}
		vars[key] = value
	}
	entries := make([]string, 0, len(vars))
	for key, value := range vars {
		entries = append(entries, key+"="+value)
	}
	w := &World{
		Getenv:  func(key string) string { return vars[key] },
		Environ: func() []string { return append([]string{}, entries...) },
		Dir:     vars["CLAUDE_PROJECT_DIR"],
		Now:     func() time.Time { return time.Now() },
	}
	reader, writer := io.Pipe()
	out := &mcpLog{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	b := &mcpBridgeRun{t: t, in: writer, out: out, cancel: cancel, done: done}
	go func() { done <- McpStdio(ctx, w, version, reader, out, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		_ = writer.Close()
		select {
		case <-b.done:
		case <-time.After(2 * time.Second):
		}
	})
	b.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`)
	b.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return b
}

// send writes one line, failing fast when the bridge takes no input (a dead one must not hold
// the test until the package timeout).
func (b *mcpBridgeRun) send(line string) {
	b.t.Helper()
	written := make(chan error, 1)
	go func() { _, err := b.in.Write([]byte(line + "\n")); written <- err }()
	select {
	case err := <-written:
		if err != nil {
			b.t.Fatalf("send %s: %v", line, err)
		}
	case <-time.After(5 * time.Second):
		b.t.Fatalf("the bridge took no input for 5 s: %s", line)
	}
}

// until polls for a matching line every 25 ms, up to ms.
func (b *mcpBridgeRun) until(ms int, keep func(*jsfmt.Value) bool) *jsfmt.Value {
	b.t.Helper()
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(deadline) {
		if value := b.out.find(keep); value != nil {
			return value
		}
		time.Sleep(25 * time.Millisecond)
	}
	return b.out.find(keep)
}

// closeIn ends the session the way the harness does, and answers the bridge's exit code.
func (b *mcpBridgeRun) closeIn() int {
	b.t.Helper()
	if err := b.in.Close(); err != nil {
		b.t.Fatalf("close stdin: %v", err)
	}
	select {
	case code := <-b.done:
		return code
	case <-time.After(2 * time.Second):
		b.t.Fatal("the bridge did not end within 2 s of stdin closing")
		return 1
	}
}

// pushContent answers the content of one claude/channel notification.
func pushContent(v *jsfmt.Value) string {
	return mcpText(mcpField(v, "params"), "content")
}

// pushMeta answers one field of a push's meta.
func pushMeta(v *jsfmt.Value, key string) string {
	return mcpText(mcpField(mcpField(v, "params"), "meta"), key)
}

// isPushWith answers a matcher for a claude/channel notification whose content contains text.
func isPushWith(text string) func(*jsfmt.Value) bool {
	return func(v *jsfmt.Value) bool {
		return mcpText(v, "method") == "notifications/claude/channel" && strings.Contains(pushContent(v), text)
	}
}

// startMcpHuddle runs one Huddle server in-process on a free port until the test ends; the
// bridge reaches it over HTTP, as it does any Huddle.
func startMcpHuddle(t *testing.T, token string) string {
	t.Helper()
	port := freePort(t)
	home := filepath.Join(t.TempDir(), "home")
	data := filepath.Join(t.TempDir(), "data")
	ctx, cancel := context.WithCancel(context.Background())
	log := &mcpLog{}
	served := make(chan struct{})
	go func() {
		_ = server.Serve(ctx, server.Options{
			Version: "test", Host: "127.0.0.1", Port: port,
			Home:  home,
			Data:  data,
			Token: token, AutoCreate: true, Stdout: log, Stderr: io.Discard,
		})
		close(served)
	}()
	// the channel databases must be closed before the temp homes are removed.
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(u + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return u
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the test's Huddle never answered /health: %s", log.text())
	return ""
}

// mcpOp runs one channel operation as a plain HTTP client, the way the tests drive the service.
func mcpOp(t *testing.T, u, channel, name, as, body, token string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u+"/api/c/"+channel+"/op/"+name+"?as="+as, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-huddle-token", token)
	req.Header.Set(server.ActionHeader, "/api/c/"+channel+"/op/"+name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("op %s: %v", name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("op %s = %d: %s", name, resp.StatusCode, string(raw))
	}
}

// mcpField answers one field of a decoded message, nil when absent.
func mcpField(v *jsfmt.Value, key string) *jsfmt.Value {
	field, _ := fieldOf(v, key)
	return field
}

// mcpText answers one string field of a decoded message.
func mcpText(v *jsfmt.Value, key string) string {
	if v == nil {
		return ""
	}
	field := mcpField(v, key)
	if field == nil || field.Kind() != jsfmt.KindString {
		return ""
	}
	return field.Text()
}

// mcpContentText answers the first text of a tool result's content.
func mcpContentText(v *jsfmt.Value) string {
	content := mcpField(mcpField(v, "result"), "content")
	if content == nil || content.Kind() != jsfmt.KindArray || len(content.Items()) == 0 {
		return ""
	}
	return mcpText(content.Items()[0], "text")
}

// mcpToolNames answers the names a tools/list reply lists.
func mcpToolNames(v *jsfmt.Value) []string {
	tools := mcpField(mcpField(v, "result"), "tools")
	names := []string{}
	if tools == nil || tools.Kind() != jsfmt.KindArray {
		return names
	}
	for _, tool := range tools.Items() {
		names = append(names, mcpText(tool, "name"))
	}
	return names
}

// mcpIDIs answers a matcher for one reply id.
func mcpIDIs(id string) func(*jsfmt.Value) bool {
	return func(v *jsfmt.Value) bool {
		field := mcpField(v, "id")
		return field != nil && field.EncodeJSON() == id
	}
}

// mcpErrorOf answers a tool result's isError flag.
func mcpErrorOf(v *jsfmt.Value) bool {
	field := mcpField(mcpField(v, "result"), "isError")
	return field != nil && field.EncodeJSON() == "true"
}

func TestStdioBridge(t *testing.T) {
	t.Run("stdio bridge: proxies tools and pushes an ask as a claude/channel notification", func(t *testing.T) {
		u := startMcpHuddle(t, mcpRootToken)
		mcpOp(t, u, "c6", "join", "a", "{}", mcpRootToken)
		mcpOp(t, u, "c6", "join", "b", "{}", mcpRootToken)
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": u, "HUDDLE_CHANNEL": "c6", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		})
		b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		init := b.until(8000, mcpIDIs("1"))
		if init == nil {
			t.Fatal("no initialize answer")
		}
		if name := mcpText(mcpField(mcpField(init, "result"), "serverInfo"), "name"); name != "huddle" {
			t.Errorf("serverInfo.name = %q, want huddle", name)
		}
		status := b.until(8000, mcpIDIs("2"))
		if status == nil {
			t.Fatal("no answer to the status call")
		}
		if text := mcpContentText(status); !strings.Contains(text, "you are b") {
			t.Errorf("status = %q, want it to say who b is", text)
		}
		mcpOp(t, u, "c6", "send", "a", `{"to":"b","msg":"are you there?","ask":true}`, mcpRootToken)
		push := b.until(8000, isPushWith("are you there?"))
		if push == nil {
			t.Fatal("the ask never arrived as a claude/channel notification")
		}
		if topic := pushMeta(push, "topic"); topic != "ask" {
			t.Errorf("push topic = %q, want ask", topic)
		}
	})

	t.Run("stdio bridge: an ask sent before the bridge connects is pushed on connect, once", func(t *testing.T) {
		u := startMcpHuddle(t, mcpRootToken)
		mcpOp(t, u, "c8", "join", "a", "{}", mcpRootToken)
		mcpOp(t, u, "c8", "join", "b", "{}", mcpRootToken)
		mcpOp(t, u, "c8", "send", "a", `{"to":"b","msg":"asked while you were away","ask":true}`, mcpRootToken)
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": u, "HUDDLE_CHANNEL": "c8", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		})
		push := b.until(8000, isPushWith("while you were away"))
		mcpOp(t, u, "c8", "send", "a", `{"to":"b","msg":"and one more","ask":true}`, mcpRootToken)
		if got := b.until(8000, isPushWith("one more")); got == nil {
			t.Fatal("the second ask never arrived")
		}
		if push == nil {
			t.Fatal("the ask sent before connecting never arrived")
		}
		if topic := pushMeta(push, "topic"); topic != "ask" {
			t.Errorf("push topic = %q, want ask", topic)
		}
		if again := b.out.findAll(isPushWith("while you were away")); len(again) != 1 {
			t.Errorf("the away ask was pushed %d times, want once", len(again))
		}
	})

	t.Run("stdio bridge: a call to a Huddle that never answers gets an error back, not a hang", func(t *testing.T) {
		muted := make(chan struct{})
		mute := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-muted
		}))
		// the held requests must be released before the server waits for them
		t.Cleanup(func() {
			close(muted)
			closeMute(mute)
		})
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": mute.URL, "HUDDLE_CHANNEL": "c10", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
			"HUDDLE_PUSH": "off", "HUDDLE_CALL_TIMEOUT_MS": "500",
		})
		b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		late := b.until(8000, mcpIDIs("2"))
		if late == nil {
			t.Fatal("the call never came back")
		}
		if !mcpErrorOf(late) {
			t.Errorf("the late answer is not an error: %s", mcpContentText(late))
		}
		if text := mcpContentText(late); !strings.Contains(text, "no answer") {
			t.Errorf("late answer = %q, want it to say there was no answer", text)
		}
	})

	t.Run("stdio bridge: a session not in a huddle lists two tools; once it is in, list_changed and every tool", func(t *testing.T) {
		u := startMcpHuddle(t, mcpRootToken)
		state := t.TempDir()
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_TOKEN": "", "HUDDLE_URL": "", "HUDDLE_CHANNEL": "", "HUDDLE_AS": "",
			"HUDDLE_HOME":        filepath.Join(state, "home"),
			"CLAUDE_PROJECT_DIR": state,
			"XDG_STATE_HOME":     state,
			"HUDDLE_SESSION":     "s-m",
			"HUDDLE_PUSH":        "off",
		})
		init := b.until(8000, mcpIDIs("1"))
		if init == nil {
			t.Fatal("no initialize answer")
		}
		if text := mcpText(mcpField(init, "result"), "instructions"); !strings.Contains(text, "mcp__plugin_huddle_huddle__") {
			t.Errorf("instructions = %q, want the not-in-a-huddle wording with the tool prefix", text)
		}
		b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		listed := b.until(8000, mcpIDIs("2"))
		if listed == nil {
			t.Fatal("no tools/list answer")
		}
		if got := mcpToolNames(listed); len(got) != 2 || got[0] != "status" || got[1] != "join" {
			t.Errorf("tools outside = %v, want [status join]", got)
		}
		invite := mcpInvite(t, u, "short")
		b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"join","arguments":{"host_port":"127.0.0.1:%s","token":%q,"as":"mcpuser"}}}`, mcpPort(u), invite))
		joined := b.until(8000, mcpIDIs("3"))
		if joined == nil {
			t.Fatal("no answer to the join call")
		}
		if text := mcpContentText(joined); !strings.Contains(text, "as mcpuser") {
			t.Errorf("join = %q, want it to name mcpuser", text)
		}
		if got := b.until(8000, func(v *jsfmt.Value) bool {
			return mcpText(v, "method") == "notifications/tools/list_changed"
		}); got == nil {
			t.Fatal("no tools/list_changed after the join")
		}
		b.send(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
		full := b.until(8000, mcpIDIs("4"))
		if full == nil {
			t.Fatal("no second tools/list answer")
		}
		if names := mcpToolNames(full); len(names) <= 20 {
			t.Errorf("tools once in = %d, want every operation (> 20)", len(names))
		}
		b.send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		status := b.until(8000, mcpIDIs("5"))
		if status == nil {
			t.Fatal("no answer to the status call")
		}
		if text := mcpContentText(status); !strings.Contains(text, "you are mcpuser") {
			t.Errorf("status = %q, want it to name mcpuser", text)
		}
	})
}

// closeMute ends the mute server without waiting for its held requests: the client has already
// given up on them.
func closeMute(mute *httptest.Server) {
	mute.CloseClientConnections()
	mute.Close()
}

// mcpPort pulls the port out of a loopback url.
func mcpPort(u string) string {
	at := strings.LastIndex(u, ":")
	if at < 0 {
		return ""
	}
	return u[at+1:]
}

// mcpInvite makes one invite for a channel and answers its token.
func mcpInvite(t *testing.T, u, channel string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u+"/api/tokens", strings.NewReader(fmt.Sprintf(`{"channel":%q}`, channel)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-huddle-token", mcpRootToken)
	req.Header.Set(server.ActionHeader, "/api/tokens")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Token == "" {
		t.Fatalf("no invite: %v %q", err, out.Token)
	}
	return out.Token
}

// TestTheStdioBridgeTranscriptMatchesTheRecording replays the recorded bridge session
// (initialize, tools/list, one tools/call against a canned service) and wants the same bytes
// on stdout.
func TestTheStdioBridgeTranscriptMatchesTheRecording(t *testing.T) {
	const reply = `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"#4 sent to a"}]}}`
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp/golden" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		msg, err := jsfmt.DecodeValue(raw)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if id := mcpField(msg, "id"); id == nil || id.Kind() == jsfmt.KindNull {
			w.WriteHeader(http.StatusAccepted) // a notification: the service answers nothing
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(reply))
	}))
	defer fake.Close()
	b := runMcpBridge(t, mcpPluginVersion(t), map[string]string{
		"HUDDLE_URL": fake.URL, "HUDDLE_CHANNEL": "golden", "HUDDLE_AS": "b",
		"HUDDLE_TOKEN": "test-golden-token", "HUDDLE_PUSH": "off",
	})
	b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if b.until(8000, mcpIDIs("2")) == nil {
		t.Fatal("no tools/list answer")
	}
	b.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"send","arguments":{"to":"a","msg":"hi"}}}`)
	if b.until(8000, mcpIDIs("3")) == nil {
		t.Fatal("no tools/call answer")
	}
	if code := b.closeIn(); code != 0 {
		t.Errorf("the bridge exited %d, want 0", code)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "huddle_mcp_transcript.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := b.out.text(); got != string(want) {
		gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gotLines) && i < len(wantLines); i++ {
			if gotLines[i] != wantLines[i] {
				t.Errorf("line %d differs:\ngot:  %.200s\nwant: %.200s", i+1, gotLines[i], wantLines[i])
			}
		}
		if len(gotLines) != len(wantLines) {
			t.Errorf("the transcript has %d lines, want %d", len(gotLines), len(wantLines))
		}
	}
}

// mcpPluginVersion answers the version the recordings name. It is fixed, not read from plugin.json,
// so a plugin version bump never changes what the bridge is compared against.
func mcpPluginVersion(t *testing.T) string {
	t.Helper()
	return "0.0.2"
}

// mcpFake is a canned service: every /mcp POST gets reply, /api/join gets joinReply, and the
// last /mcp body is kept for the assertions.
type mcpFake struct {
	reply     string
	joinReply string
	joinCode  int
	mu        sync.Mutex
	bodies    []string
	server    *httptest.Server
}

func newMcpFake(reply string) *mcpFake {
	f := &mcpFake{reply: reply, joinCode: http.StatusOK}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/join":
			f.mu.Lock()
			code, reply := f.joinCode, f.joinReply
			f.mu.Unlock()
			w.Header().Set("content-type", "application/json")
			if code != 0 && code != http.StatusOK {
				w.WriteHeader(code)
			}
			_, _ = w.Write([]byte(reply))
		case strings.HasPrefix(r.URL.Path, "/mcp/"):
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.bodies = append(f.bodies, string(raw))
			reply := f.reply
			f.mu.Unlock()
			// A notification gets the service's 202 and no body: the canned reply belongs to a
			// call, and handing it to the bridge's forwarded notifications/initialized would
			// answer that call's id before the call itself arrives.
			var msg map[string]json.RawMessage
			if json.Unmarshal(raw, &msg) == nil {
				if _, isCall := msg["id"]; !isCall {
					w.WriteHeader(http.StatusAccepted)
					return
				}
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(reply))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return f
}

// bodyWithID answers the /mcp body of the call that carried one JSON-RPC id: forwards run
// concurrently, so arrival order says nothing about which call each body belongs to.
func (f *mcpFake) bodyWithID(id string) string {
	want := `"id":` + id + ","
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, body := range f.bodies {
		if strings.Contains(body, want) {
			return body
		}
	}
	return ""
}

func TestStdioBridgeLocalAndFailureAnswers(t *testing.T) {
	t.Run("what needs no service is answered without it, and garbage gets a parse error", func(t *testing.T) {
		closed := freePort(t) // nothing listens: every forwarded call would fail
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": fmt.Sprintf("http://127.0.0.1:%d", closed), "HUDDLE_PUSH": "off",
		})
		b.send(`this is not json`)
		got := b.until(8000, mcpIDIs("null"))
		if got == nil {
			t.Fatal("no parse error reply")
		}
		want := `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`
		if line := got.EncodeJSON(); line != want {
			t.Errorf("parse error = %s, want %s", line, want)
		}
		for _, call := range []struct {
			line string
			want string
		}{
			{`{"jsonrpc":"2.0","id":2,"method":"ping"}`, `{}`},
			{`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`, `{"resources":[]}`},
			{`{"jsonrpc":"2.0","id":4,"method":"prompts/list"}`, `{"prompts":[]}`},
		} {
			b.send(call.line)
			got := b.until(8000, mcpIDIs(strings.Trim(regexp.MustCompile(`"id":(\d+)`).FindStringSubmatch(call.line)[1], "")))
			if got == nil {
				t.Fatalf("no answer to %s", call.line)
			}
			if result := mcpFieldOf(got, "result").EncodeJSON(); result != call.want {
				t.Errorf("%s result = %s, want %s", call.line, result, call.want)
			}
		}
		// outside a huddle: status answers without an error flag, everything else with one
		b.send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		status := b.until(8000, mcpIDIs("5"))
		if status == nil {
			t.Fatal("no answer to the status call")
		}
		if text := mcpContentText(status); text != "huddle: "+notInYet || mcpErrorOf(status) {
			t.Errorf("status outside = (%q, %v), want the not-in text without an error flag", text, mcpErrorOf(status))
		}
		b.send(`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"reply","arguments":{"seq":1}}}`)
		other := b.until(8000, mcpIDIs("6"))
		if other == nil {
			t.Fatal("no answer to the reply call")
		}
		if text := mcpContentText(other); text != "huddle: "+notInYet || !mcpErrorOf(other) {
			t.Errorf("reply outside = (%q, %v), want the not-in text with an error flag", text, mcpErrorOf(other))
		}
	})

	t.Run("a service that refuses the credential is a tool error, not a crash", func(t *testing.T) {
		fake := newMcpFake(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"never"}]}}`)
		defer fake.server.Close()
		fake.mu.Lock()
		fake.reply = ""
		fake.mu.Unlock()
		refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"not in this huddle: ask for an invite"}`))
		}))
		defer refused.Close()
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": refused.URL, "HUDDLE_CHANNEL": "c401", "HUDDLE_AS": "b",
			"HUDDLE_TOKEN": mcpRootToken, "HUDDLE_PUSH": "off",
		})
		b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		got := b.until(8000, mcpIDIs("2"))
		if got == nil {
			t.Fatal("no answer")
		}
		if text := mcpContentText(got); text != "huddle: not in this huddle: ask for an invite" || !mcpErrorOf(got) {
			t.Errorf("401 = (%q, %v), want the service's error as a tool error", text, mcpErrorOf(got))
		}
	})

	t.Run("a service that is down says so, and the caller retries", func(t *testing.T) {
		closed := freePort(t)
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": fmt.Sprintf("http://127.0.0.1:%d", closed), "HUDDLE_CHANNEL": "cd",
			"HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken, "HUDDLE_PUSH": "off",
		})
		b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		got := b.until(8000, mcpIDIs("2"))
		if got == nil {
			t.Fatal("no answer")
		}
		if text := mcpContentText(got); !strings.Contains(text, "service unreachable at http://127.0.0.1:") ||
			!strings.Contains(text, "(start it: huddle up)") || !mcpErrorOf(got) {
			t.Errorf("down service = %q, want the unreachable text", text)
		}
	})

	t.Run("a blocking tool gets the long stdio timeout unless the caller chose one", func(t *testing.T) {
		fake := newMcpFake("")
		defer fake.server.Close()
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": fake.server.URL, "HUDDLE_CHANNEL": "bt", "HUDDLE_AS": "b",
			"HUDDLE_TOKEN": mcpRootToken, "HUDDLE_PUSH": "off",
		})
		call := func(id string, arguments string) {
			b.t.Helper()
			fake.mu.Lock()
			fake.reply = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"ok"}]}}`, id)
			fake.mu.Unlock()
			b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"wait","arguments":%s}}`, id, arguments))
			got := b.until(8000, mcpIDIs(id))
			if got == nil {
				b.t.Fatalf("no answer to call %s", id)
			}
			if fake.bodyWithID(id) == "" {
				fake.mu.Lock()
				defer fake.mu.Unlock()
				b.t.Fatalf("call %s was answered %s without reaching the service, which saw %q", id, got.EncodeJSON(), fake.bodies)
			}
		}
		call("2", `{"topics":["x"]}`)
		if body := fake.bodyWithID("2"); !strings.Contains(body, `"timeout":1500`) {
			t.Errorf("wait's forwarded body = %s, want the default 1500 injected", body)
		}
		fake.mu.Lock()
		fake.reply = `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}`
		fake.mu.Unlock()
		b.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"gate","arguments":{}}}`)
		if b.until(8000, mcpIDIs("3")) == nil {
			t.Fatal("no answer to gate")
		}
		if body := fake.bodyWithID("3"); !strings.Contains(body, `"timeout":0`) {
			t.Errorf("gate's forwarded body = %s, want timeout 0 (the server's hour)", body)
		}
		call("4", `{"topics":["x"],"timeout":90}`)
		if body := fake.bodyWithID("4"); !strings.Contains(body, `"timeout":90`) {
			t.Errorf("a chosen timeout was rewritten: %s", body)
		}
	})
}

func TestStdioBridgeJoinErrors(t *testing.T) {
	notIn := func(t *testing.T, extra map[string]string) *mcpBridgeRun {
		t.Helper()
		env := map[string]string{"HUDDLE_TOKEN": "", "HUDDLE_URL": "", "HUDDLE_CHANNEL": "", "HUDDLE_AS": "", "HUDDLE_PUSH": "off"}
		for key, value := range extra {
			env[key] = value
		}
		return runMcpBridge(t, "", env)
	}
	join := func(b *mcpBridgeRun, id, arguments string) *jsfmt.Value {
		b.t.Helper()
		b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"method":"tools/call","params":{"name":"join","arguments":%s}}`, id, arguments))
		got := b.until(8000, mcpIDIs(id))
		if got == nil {
			b.t.Fatalf("no answer to join %s", arguments)
		}
		return got
	}
	t.Run("join needs a host:port and a token", func(t *testing.T) {
		b := notIn(t, nil)
		got := join(b, "2", `{"host_port":"not a host","token":"x"}`)
		if text := mcpContentText(got); text != "huddle: join needs host_port (host:port) and token" || !mcpErrorOf(got) {
			t.Errorf("join(nope) = %q, want the usage error", text)
		}
		b2 := notIn(t, nil)
		got = join(b2, "2", `{"host_port":"127.0.0.1:1234","token":5}`)
		if text := mcpContentText(got); text != "huddle: join needs host_port (host:port) and token" {
			t.Errorf("join(token: 5) = %q, want the usage error", text)
		}
	})
	t.Run("an unreachable invite host says so", func(t *testing.T) {
		closed := freePort(t)
		b := notIn(t, nil)
		got := join(b, "2", fmt.Sprintf(`{"host_port":"127.0.0.1:%d","token":"x.y"}`, closed))
		if text := mcpContentText(got); text != fmt.Sprintf("huddle: Huddle unreachable at http://127.0.0.1:%d", closed) {
			t.Errorf("join(unreachable) = %q", text)
		}
	})
	t.Run("the service's refusal is passed through", func(t *testing.T) {
		fake := newMcpFake("")
		defer fake.server.Close()
		fake.joinCode = http.StatusUnauthorized
		fake.joinReply = `{"error":"invalid or expired join token: ask the owner for a new join command"}`
		port := mcpPort(fake.server.URL)
		b := notIn(t, nil)
		got := join(b, "2", fmt.Sprintf(`{"host_port":"127.0.0.1:%s","token":"zzzzzz.0123456789abcdef"}`, port))
		if text := mcpContentText(got); text != "huddle: invalid or expired join token: ask the owner for a new join command" {
			t.Errorf("join(refused) = %q", text)
		}
	})
	t.Run("an invite that names no channel is refused", func(t *testing.T) {
		fake := newMcpFake("")
		defer fake.server.Close()
		fake.joinReply = `{"credential":"hcred-x","name":"nobody"}`
		port := mcpPort(fake.server.URL)
		b := notIn(t, nil)
		got := join(b, "2", fmt.Sprintf(`{"host_port":"127.0.0.1:%s","token":"abcdef.0123456789abcdef"}`, port))
		if text := mcpContentText(got); text != "huddle: joined, but the invite names no channel: ask for an invite made in a channel" {
			t.Errorf("join(no channel) = %q", text)
		}
	})
}

func TestStdioBridgePushes(t *testing.T) {
	t.Run("stdio bridge: refs, overheard messages and pushed topics ride along", func(t *testing.T) {
		u := startMcpHuddle(t, mcpRootToken)
		for _, as := range []string{"a", "b", "c"} {
			mcpOp(t, u, "c9", "join", as, "{}", mcpRootToken)
		}
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": u, "HUDDLE_CHANNEL": "c9", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		})
		// an ask reaches the session on either path (live, or the inbox catch-up on connect):
		// waiting for it proves the stream is up, so the events below ride it
		mcpOp(t, u, "c9", "send", "a", `{"to":"b","msg":"are you with us?","ask":true}`, mcpRootToken)
		if b.until(8000, isPushWith("are you with us?")) == nil {
			t.Fatal("the first ask never arrived")
		}
		mcpOp(t, u, "c9", "publish", "a", `{"topic":"note.ready","msg":"with a ref","to":"b","ref":"src/x.go"}`, mcpRootToken)
		if push := b.until(8000, isPushWith("with a ref")); push == nil {
			t.Fatal("the referenced event never arrived")
		} else if !strings.Contains(pushContent(push), "(src/x.go)") {
			t.Errorf("push = %q, want the ref in it", pushContent(push))
		}
		mcpOp(t, u, "c9", "send", "a", `{"to":"c","msg":"between others"}`, mcpRootToken)
		over := b.until(8000, isPushWith("between others"))
		if over == nil {
			t.Fatal("the overheard message never arrived")
		}
		if !strings.Contains(pushContent(over), "(overheard: between other sessions, for your knowledge)") {
			t.Errorf("overheard push = %q, want the overheard note", pushContent(over))
		}
		mcpOp(t, u, "c9", "publish", "a", `{"topic":"turn.pass","msg":"the turn is yours"}`, mcpRootToken)
		topiced := b.until(8000, isPushWith("the turn is yours"))
		if topiced == nil {
			t.Fatal("the pushed topic never arrived")
		}
		if topic := pushMeta(topiced, "topic"); topic != "turn_pass" {
			t.Errorf("push topic = %q, want turn_pass", topic)
		}
	})

	t.Run("stdio bridge: a live stream that ends is reconnected and the gap re-read", func(t *testing.T) {
		var lives atomic.Int32
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "/live"):
				lives.Add(1)
				w.Header().Set("content-type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "data: {\"type\":\"hello\",\"data\":{\"last\":5}}\n\n")
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				return // the stream ends right after hello
			case strings.HasSuffix(r.URL.Path, "/op/inbox"):
				w.Header().Set("content-type", "application/json")
				fmt.Fprint(w, `{"result":[]}`)
			case strings.HasSuffix(r.URL.Path, "/op/events"):
				w.Header().Set("content-type", "application/json")
				fmt.Fprint(w, `{"result":[{"seq":6,"from":"a","to":"b","topic":"msg","msg":"after the break"}]}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer fake.Close()
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_URL": fake.URL, "HUDDLE_CHANNEL": "q", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		})
		push := b.until(8000, isPushWith("after the break"))
		if push == nil {
			t.Fatalf("the missed event never arrived (streams opened: %d)", lives.Load())
		}
		if lives.Load() < 2 {
			t.Errorf("the stream was opened %d times, want a reconnect", lives.Load())
		}
		if seq := pushMeta(push, "seq"); seq != "6" {
			t.Errorf("push seq = %q, want 6", seq)
		}
	})

	t.Run("a session that joins outside the bridge gets the full list announced", func(t *testing.T) {
		u := startMcpHuddle(t, mcpRootToken)
		state := t.TempDir()
		b := runMcpBridge(t, "", map[string]string{
			"HUDDLE_TOKEN": "", "HUDDLE_URL": "", "HUDDLE_CHANNEL": "", "HUDDLE_AS": "",
			"HUDDLE_HOME":        filepath.Join(state, "home"),
			"CLAUDE_PROJECT_DIR": state,
			"XDG_STATE_HOME":     state,
			"HUDDLE_SESSION":     "w-l",
			"HUDDLE_PUSH":        "off",
		})
		b.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		if listed := b.until(8000, mcpIDIs("2")); listed == nil {
			t.Fatal("no tools/list answer")
		} else if got := mcpToolNames(listed); len(got) != 2 {
			t.Fatalf("tools outside = %v, want the short list", got)
		}
		// the session joins elsewhere: its credential appears where identity reads it
		sessions := filepath.Join(state, "home", "sessions")
		if err := os.MkdirAll(sessions, 0o700); err != nil {
			t.Fatal(err)
		}
		cred := fmt.Sprintf(`{"url":%q,"channel":"wl","as":"wluser","credential":%q}`, u, mcpRootToken)
		for _, key := range []string{"w-l", "terminal"} {
			if err := os.WriteFile(filepath.Join(sessions, key+".json"), []byte(cred), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := b.until(9000, func(v *jsfmt.Value) bool {
			return mcpText(v, "method") == "notifications/tools/list_changed"
		}); got == nil {
			t.Fatal("no tools/list_changed after the outside join")
		}
		b.send(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`)
		if full := b.until(8000, mcpIDIs("4")); full == nil {
			t.Fatal("no second tools/list answer")
		} else if names := mcpToolNames(full); len(names) <= 20 {
			t.Errorf("tools once in = %d, want every operation", len(names))
		}
	})
}

// isUpdatedWith answers a matcher for one resources/updated notification naming uri.
func isUpdatedWith(uri string) func(*jsfmt.Value) bool {
	return func(v *jsfmt.Value) bool {
		return mcpText(v, "method") == "notifications/resources/updated" &&
			mcpText(mcpField(v, "params"), "uri") == uri
	}
}

// mcpResources answers every resource entry of a resources/list reply.
func mcpResources(v *jsfmt.Value) []*jsfmt.Value {
	resources := mcpField(mcpField(v, "result"), "resources")
	if resources == nil || resources.Kind() != jsfmt.KindArray {
		return nil
	}
	return resources.Items()
}

// mcpResourceOf answers one resource entry of a resources/list reply by its uri, nil when absent.
func mcpResourceOf(v *jsfmt.Value, uri string) *jsfmt.Value {
	for _, r := range mcpResources(v) {
		if mcpText(r, "uri") == uri {
			return r
		}
	}
	return nil
}

// mcpOpJSON runs one channel operation as a plain HTTP client and answers the decoded body.
func mcpOpJSON(t *testing.T, u, channel, name, as, body, token string) *jsfmt.Value {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, u+"/api/c/"+channel+"/op/"+name+"?as="+as, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-huddle-token", token)
	req.Header.Set(server.ActionHeader, "/api/c/"+channel+"/op/"+name)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("op %s: %v", name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("op %s = %d: %s", name, resp.StatusCode, string(raw))
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		t.Fatalf("op %s body: %v", name, err)
	}
	return value
}

// mcpLastSeq answers the channel's newest event seq.
func mcpLastSeq(t *testing.T, u, channel, token string) int64 {
	t.Helper()
	answer := mcpOpJSON(t, u, channel, "events", "a", `{"limit":1}`, token)
	result := mcpField(answer, "result")
	if result == nil || result.Kind() != jsfmt.KindArray || len(result.Items()) == 0 {
		return 0
	}
	return int64(mcpNumberOf(mcpField(result.Items()[len(result.Items())-1], "seq")))
}

func TestInitializeDeclaresResourcesAndListChanged(t *testing.T) {
	b := runMcpBridge(t, "", map[string]string{"HUDDLE_PUSH": "off"})
	init := b.until(8000, mcpIDIs("1"))
	if init == nil {
		t.Fatal("no initialize answer")
	}
	caps := mcpField(mcpField(init, "result"), "capabilities")
	resources := mcpField(caps, "resources")
	if resources == nil {
		t.Fatalf("capabilities = %s, want a resources object", caps.EncodeJSON())
	}
	for _, flag := range []string{"subscribe", "listChanged"} {
		if field := mcpField(resources, flag); field == nil || field.EncodeJSON() != "true" {
			t.Errorf("resources.%s = %v, want true", flag, field)
		}
	}
	if field := mcpField(mcpField(caps, "tools"), "listChanged"); field == nil || field.EncodeJSON() != "true" {
		t.Errorf("tools.listChanged = %v, want true", field)
	}
}

func TestResourcesListNamesTheChannelTopics(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	mcpOp(t, u, "cr", "join", "a", "{}", mcpRootToken)
	mcpOp(t, u, "cr", "join", "b", "{}", mcpRootToken)
	mcpOp(t, u, "cr", "publish", "a", `{"topic":"note.ready","msg":"one"}`, mcpRootToken)
	mcpOp(t, u, "cr", "publish", "a", `{"topic":"build.done","msg":"two"}`, mcpRootToken)
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_URL": u, "HUDDLE_CHANNEL": "cr", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		"HUDDLE_PUSH": "off",
	})
	b.send(`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`)
	listed := b.until(8000, mcpIDIs("2"))
	if listed == nil {
		t.Fatal("no resources/list answer")
	}
	for _, uri := range []string{"huddle://cr/events", "huddle://cr/topic/note.ready", "huddle://cr/topic/build.done"} {
		entry := mcpResourceOf(listed, uri)
		if entry == nil {
			t.Errorf("resources/list has no %s (got %v)", uri, mcpResources(listed))
			continue
		}
		if mime := mcpText(entry, "mimeType"); mime != "application/json" {
			t.Errorf("%s mimeType = %q, want application/json", uri, mime)
		}
	}
}

func TestResourcesReadReturnsEventsAfterCursor(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	mcpOp(t, u, "cd", "join", "a", "{}", mcpRootToken)
	mcpOp(t, u, "cd", "join", "b", "{}", mcpRootToken)
	mcpOp(t, u, "cd", "publish", "a", `{"topic":"ev.one","msg":"first"}`, mcpRootToken)
	mcpOp(t, u, "cd", "publish", "a", `{"topic":"ev.two","msg":"second"}`, mcpRootToken)
	seq := mcpLastSeq(t, u, "cd", mcpRootToken)
	mcpOp(t, u, "cd", "publish", "a", `{"topic":"ev.three","msg":"third"}`, mcpRootToken)
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_URL": u, "HUDDLE_CHANNEL": "cd", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		"HUDDLE_PUSH": "off",
	})
	uri := fmt.Sprintf("huddle://cd/events?after=%d", seq)
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":%q}}`, uri))
	read := b.until(8000, mcpIDIs("2"))
	if read == nil {
		t.Fatal("no resources/read answer")
	}
	contents := mcpField(mcpField(read, "result"), "contents")
	if contents == nil || contents.Kind() != jsfmt.KindArray || len(contents.Items()) != 1 {
		t.Fatalf("read = %s, want one content entry", read.EncodeJSON())
	}
	entry := contents.Items()[0]
	if got := mcpText(entry, "uri"); got != uri {
		t.Errorf("content uri = %q, want %q", got, uri)
	}
	if mime := mcpText(entry, "mimeType"); mime != "application/json" {
		t.Errorf("content mimeType = %q, want application/json", mime)
	}
	events, err := jsfmt.DecodeValue([]byte(mcpText(entry, "text")))
	if err != nil {
		t.Fatalf("content text is not JSON: %v: %q", err, mcpText(entry, "text"))
	}
	if events.Kind() != jsfmt.KindArray || len(events.Items()) != 1 {
		t.Fatalf("content holds %d events, want the one after the cursor", len(events.Items()))
	}
	if topic := mcpText(events.Items()[0], "topic"); topic != "ev.three" {
		t.Errorf("event topic = %q, want ev.three", topic)
	}
	// a read answers at most 100 events
	for i := 0; i < 105; i++ {
		mcpOp(t, u, "cd", "publish", "a", fmt.Sprintf(`{"topic":"ev.bulk","msg":"n%d"}`, i), mcpRootToken)
	}
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":%q}}`, uri))
	capped := b.until(8000, mcpIDIs("3"))
	if capped == nil {
		t.Fatal("no answer to the capped read")
	}
	capContents := mcpField(mcpField(capped, "result"), "contents")
	if capContents == nil || capContents.Kind() != jsfmt.KindArray || len(capContents.Items()) != 1 {
		t.Fatalf("capped read = %s, want one content entry", capped.EncodeJSON())
	}
	capEvents, err := jsfmt.DecodeValue([]byte(mcpText(capContents.Items()[0], "text")))
	if err != nil {
		t.Fatalf("capped content text is not JSON: %v", err)
	}
	if n := len(capEvents.Items()); n != 100 {
		t.Errorf("capped read holds %d events, want at most 100", n)
	}
}

func TestSubscribeSendsUpdatedOnAMatchingEvent(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	mcpOp(t, u, "cs", "join", "a", "{}", mcpRootToken)
	mcpOp(t, u, "cs", "join", "b", "{}", mcpRootToken)
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_URL": u, "HUDDLE_CHANNEL": "cs", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
	})
	uri := "huddle://cs/topic/note.ready"
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"resources/subscribe","params":{"uri":%q}}`, uri))
	if ack := b.until(8000, mcpIDIs("2")); ack == nil {
		t.Fatal("no answer to the subscribe")
	}
	mcpOp(t, u, "cs", "publish", "a", `{"topic":"note.ready","msg":"for the subscriber"}`, mcpRootToken)
	if updated := b.until(2000, isUpdatedWith(uri)); updated == nil {
		t.Fatal("no resources/updated within 2 s of the publish")
	}
}

func TestUnsubscribeStops(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	mcpOp(t, u, "cu", "join", "a", "{}", mcpRootToken)
	mcpOp(t, u, "cu", "join", "b", "{}", mcpRootToken)
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_URL": u, "HUDDLE_CHANNEL": "cu", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
	})
	uri := "huddle://cu/topic/note.ready"
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"resources/subscribe","params":{"uri":%q}}`, uri))
	if ack := b.until(8000, mcpIDIs("2")); ack == nil {
		t.Fatal("no answer to the subscribe")
	}
	mcpOp(t, u, "cu", "publish", "a", `{"topic":"note.ready","msg":"while subscribed"}`, mcpRootToken)
	if b.until(8000, isUpdatedWith(uri)) == nil {
		t.Fatal("no resources/updated while subscribed")
	}
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"resources/unsubscribe","params":{"uri":%q}}`, uri))
	if ack := b.until(8000, mcpIDIs("3")); ack == nil {
		t.Fatal("no answer to the unsubscribe")
	}
	mcpOp(t, u, "cu", "publish", "a", `{"topic":"note.ready","msg":"after unsubscribing"}`, mcpRootToken)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := len(b.out.findAll(isUpdatedWith(uri))); got != 1 {
			t.Fatalf("resources/updated arrived %d times after the unsubscribe, want once", got)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestSubscribeBeforeTheStreamStillGetsTheUpdate proves a subscription taken before the live
// stream is up still hears an event published before the stream's hello: the stream does not
// replay what came before it connected, so the bridge must catch that gap up from the channel's
// log. The proxy holds every /live connect for 1.5 s to make the gap deterministic.
func TestSubscribeBeforeTheStreamStillGetsTheUpdate(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	mcpOp(t, u, "cw", "join", "a", "{}", mcpRootToken)
	mcpOp(t, u, "cw", "join", "b", "{}", mcpRootToken)
	target, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// the server answers 421 to a Host it does not serve, so the proxy must present the target's
	forward := proxy.Director
	proxy.Director = func(req *http.Request) {
		forward(req)
		req.Host = target.Host
	}
	proxy.FlushInterval = -1 // frames must reach the bridge as the stream writes them
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/live") {
			time.Sleep(1500 * time.Millisecond)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(slow.Close)
	t.Cleanup(slow.CloseClientConnections)
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_URL": slow.URL, "HUDDLE_CHANNEL": "cw", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
	})
	uri := "huddle://cw/topic/note.ready"
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"resources/subscribe","params":{"uri":%q}}`, uri))
	if ack := b.until(8000, mcpIDIs("2")); ack == nil {
		t.Fatal("no answer to the subscribe")
	}
	mcpOp(t, u, "cw", "publish", "a", `{"topic":"note.ready","msg":"before the stream was up"}`, mcpRootToken)
	if updated := b.until(8000, isUpdatedWith(uri)); updated == nil {
		t.Fatal("no resources/updated for the event published before the stream was up")
	}
}

func TestPollToolReturnsAfterCursorOrTimesOut(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	mcpOp(t, u, "cpl", "join", "a", "{}", mcpRootToken)
	mcpOp(t, u, "cpl", "join", "b", "{}", mcpRootToken)
	mcpOp(t, u, "cpl", "publish", "a", `{"topic":"poll.one","msg":"early"}`, mcpRootToken)
	seq := mcpLastSeq(t, u, "cpl", mcpRootToken)
	mcpOp(t, u, "cpl", "publish", "a", `{"topic":"poll.two","msg":"later"}`, mcpRootToken)
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_URL": u, "HUDDLE_CHANNEL": "cpl", "HUDDLE_AS": "b", "HUDDLE_TOKEN": mcpRootToken,
		"HUDDLE_PUSH": "off",
	})
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"poll","arguments":{"after":%d}}}`, seq))
	got := b.until(8000, mcpIDIs("2"))
	if got == nil {
		t.Fatal("no answer to poll")
	}
	if mcpErrorOf(got) {
		t.Fatalf("poll is an error: %s", mcpContentText(got))
	}
	events, err := jsfmt.DecodeValue([]byte(mcpContentText(got)))
	if err != nil {
		t.Fatalf("poll result is not JSON: %v: %q", err, mcpContentText(got))
	}
	if events.Kind() != jsfmt.KindArray || len(events.Items()) != 1 {
		t.Fatalf("poll returned %d events, want the one after the cursor", len(events.Items()))
	}
	if topic := mcpText(events.Items()[0], "topic"); topic != "poll.two" {
		t.Errorf("poll event topic = %q, want poll.two", topic)
	}
	after := mcpLastSeq(t, u, "cpl", mcpRootToken)
	// b has unread events, so a single server wait would wake at once: poll must still hold the
	// call until its timeout, or a harness spins on empty answers.
	started := time.Now()
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"poll","arguments":{"after":%d,"timeout_ms":1500}}}`, after))
	timed := b.until(8000, mcpIDIs("3"))
	if timed == nil {
		t.Fatal("no answer to the timing-out poll")
	}
	if text := mcpContentText(timed); text != "[]" {
		t.Errorf("timing-out poll = %q, want []", text)
	}
	if held := time.Since(started); held < 1400*time.Millisecond {
		t.Errorf("timing-out poll answered after %v, want it held about 1.5 s", held)
	}
}

func TestSessionWithoutClaudeJoinsAndPublishes(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	state := t.TempDir()
	b := runMcpBridge(t, "", map[string]string{
		"HUDDLE_TOKEN": "", "HUDDLE_URL": "", "HUDDLE_CHANNEL": "", "HUDDLE_AS": "",
		"HUDDLE_HOME":        filepath.Join(state, "home"),
		"CLAUDE_PROJECT_DIR": "",
		"XDG_STATE_HOME":     state,
		"HUDDLE_SESSION":     "s-agent",
		"HUDDLE_PUSH":        "off",
	})
	invite := mcpInvite(t, u, "cagent")
	b.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"join","arguments":{"host_port":"127.0.0.1:%s","token":%q,"as":"agentx"}}}`, mcpPort(u), invite))
	joined := b.until(8000, mcpIDIs("2"))
	if joined == nil {
		t.Fatal("no answer to the join")
	}
	if text := mcpContentText(joined); !strings.Contains(text, "as agentx") || mcpErrorOf(joined) {
		t.Fatalf("join = %q, want it to name agentx without an error", text)
	}
	b.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"publish","arguments":{"topic":"agent.ready","msg":"published without Claude"}}}`)
	pub := b.until(8000, mcpIDIs("3"))
	if pub == nil {
		t.Fatal("no answer to the publish")
	}
	if mcpErrorOf(pub) {
		t.Fatalf("publish = %q, want no error", mcpContentText(pub))
	}
	answer := mcpOpJSON(t, u, "cagent", "events", "a", `{"topic":"agent.ready"}`, mcpRootToken)
	result := mcpField(answer, "result")
	if result == nil || result.Kind() != jsfmt.KindArray || len(result.Items()) == 0 {
		t.Fatalf("the published event never reached the channel: %s", answer.EncodeJSON())
	}
	if from := mcpText(result.Items()[len(result.Items())-1], "from"); from != "agentx" {
		t.Errorf("the event is from %q, want agentx", from)
	}
}

func TestMcpReadsJavaScriptShapes(t *testing.T) {
	value := func(text string) *jsfmt.Value {
		v, err := jsfmt.DecodeValue([]byte(text))
		if err != nil {
			t.Fatalf("decode %s: %v", text, err)
		}
		return v
	}
	tests := []struct {
		name   string
		json   string
		truthy bool
		text   string
		number float64
	}{
		{"a nonempty string", `"x"`, true, "x", 0},
		{"an empty string", `""`, false, "", 0},
		{"a number besides zero", `5`, true, "5", 5},
		{"zero", `0`, false, "0", 0},
		{"a fractional number", `1.5`, true, "1.5", 1.5},
		{"a numeric string", `"12"`, true, "12", 12},
		{"an unparsable numeric string", `"x"`, true, "x", 0},
		{"true", `true`, true, "true", 1},
		{"false", `false`, false, "false", 0},
		{"null", `null`, false, "null", 0},
		{"an object", `{}`, true, "{}", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := value(tt.json)
			if got := mcpTruthy(v); got != tt.truthy {
				t.Errorf("truthy(%s) = %v, want %v", tt.json, got, tt.truthy)
			}
			if got := mcpStringOf(v); got != tt.text {
				t.Errorf("stringOf(%s) = %q, want %q", tt.json, got, tt.text)
			}
			if got := mcpNumberOf(v); got != tt.number {
				t.Errorf("numberOf(%s) = %v, want %v", tt.json, got, tt.number)
			}
		})
	}
	// an absent or non-object value reads as an empty object
	for _, v := range []*jsfmt.Value{nil, value(`"s"`), value(`[1]`)} {
		if got := mcpObjectOf(v); got == nil || got.Kind() != jsfmt.KindObject || len(got.Keys()) != 0 {
			t.Errorf("objectOf(%v) is not an empty object", v)
		}
	}
	if got := mcpGlobExpr("a.b+*"); got != `a\.b\+.*` {
		t.Errorf("globExpr = %q", got)
	}
}
