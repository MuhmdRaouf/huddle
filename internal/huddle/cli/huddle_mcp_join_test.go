package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// joinBodyOf is the body a join call carried.
type joinBodyOf struct {
	Role          string `json:"role"`
	Task          string `json:"task"`
	Context       string `json:"context"`
	ClaudeSession string `json:"claude_session"`
}

// memberHub is a fake Huddle that holds one member row and records every join it is sent. Its
// sessions list answers the row it was given; a join answers 200 like the real op.
type memberHub struct {
	*httptest.Server
	mu    sync.Mutex
	joins []joinBodyOf
}

// newMemberHub starts a fake whose channel homelab lists proxbeam with the given last_seen and
// state.
func newMemberHub(t *testing.T, state, lastSeen string) *memberHub {
	t.Helper()
	hub := &memberHub{}
	hub.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/c/homelab/sessions":
			fmt.Fprintf(w, `{"sessions":[{"name":"proxbeam","state":%q,"last_seen":%q}],"turn":{"holder":null,"since":null},"config":{}}`,
				state, lastSeen)
		case r.Method == http.MethodPost && r.URL.Path == "/api/c/homelab/op/join":
			raw, _ := io.ReadAll(r.Body)
			var body joinBodyOf
			_ = json.Unmarshal(raw, &body)
			hub.mu.Lock()
			hub.joins = append(hub.joins, body)
			hub.mu.Unlock()
			fmt.Fprint(w, `{"result":{},"text":"joined"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/timeline"):
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hub.Close)
	return hub
}

func (h *memberHub) joined() []joinBodyOf {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]joinBodyOf{}, h.joins...)
}

// waitJoins waits up to ms for the hub to have seen at least n joins.
func (h *memberHub) waitJoins(n, ms int) []joinBodyOf {
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := h.joined(); len(got) >= n {
			return got
		}
		time.Sleep(25 * time.Millisecond)
	}
	return h.joined()
}

func proxbeamEnv(url string) map[string]string {
	return map[string]string{
		"HUDDLE_URL": url, "HUDDLE_CHANNEL": "homelab", "HUDDLE_AS": "proxbeam",
		"HUDDLE_ROLE": "builder", "HUDDLE_CONTEXT": "fresh", "HUDDLE_SESSION": "a4c70c75-0000",
		"HUDDLE_TOKEN": mcpRootToken,
	}
}

// firstToolCall is the call that wakes the bridge's join check without waiting for its start
// delay.
const firstToolCall = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`

// TestTheBridgeJoinsWhenTheHookDidNot: the channel holds an old row for the member and nothing
// from this session, so the bridge joins once with the fields the session start sends.
func TestTheBridgeJoinsWhenTheHookDidNot(t *testing.T) {
	hub := newMemberHub(t, "working", "2026-10-15T09:00:00.000Z")
	b := runMcpBridge(t, "", proxbeamEnv(hub.URL))
	b.send(firstToolCall)
	got := hub.waitJoins(1, 5000)
	if len(got) != 1 {
		t.Fatalf("the hub saw %d joins, want 1", len(got))
	}
	want := joinBodyOf{Role: "builder", Task: "session started", Context: "fresh", ClaudeSession: "a4c70c75-0000"}
	if got[0] != want {
		t.Errorf("join body = %+v, want %+v", got[0], want)
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(hub.joined()); n != 1 {
		t.Errorf("the hub saw %d joins after the first landed, want the bridge to stop at 1", n)
	}
}

// TestTheBridgeJoinsAtStartWithoutAToolCall: a session that makes no call still ends up joined
// once the session start's own budget has passed.
func TestTheBridgeJoinsAtStartWithoutAToolCall(t *testing.T) {
	hub := newMemberHub(t, "working", "2026-10-15T09:00:00.000Z")
	runMcpBridge(t, "", proxbeamEnv(hub.URL))
	if got := hub.waitJoins(1, 8000); len(got) != 1 {
		t.Fatalf("the hub saw %d joins within 8 s of the bridge starting, want 1", len(got))
	}
}

// TestTheBridgeLeavesAJoinedSessionAlone: the hook's join is on the channel's list, fresh, so
// the bridge sends none.
func TestTheBridgeLeavesAJoinedSessionAlone(t *testing.T) {
	hub := newMemberHub(t, "working", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	b := runMcpBridge(t, "", proxbeamEnv(hub.URL))
	b.send(firstToolCall)
	if got := hub.waitJoins(1, 1500); len(got) != 0 {
		t.Errorf("the hub saw %d joins for a session that is in, want none", len(got))
	}
}

// TestTheBridgeKeepsTryingUntilTheHubAnswers: a hub that refuses the first joins is tried again
// after a pause, and the bridge stops at the first one that lands.
func TestTheBridgeKeepsTryingUntilTheHubAnswers(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/c/homelab/op/join":
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n < 3 {
				http.Error(w, `{"error":"busy"}`, http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, `{"result":{},"text":"joined"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/timeline"):
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hub.Close)
	b := runMcpBridge(t, "", proxbeamEnv(hub.URL))
	b.send(firstToolCall)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Errorf("the hub saw %d joins, want 3: two refused, one landed, then no more", calls)
	}
}

// sessionRow reads one member of a channel from the real service's sessions list.
func sessionRow(t *testing.T, u, channel, name string) map[string]any {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u+"/api/c/"+channel+"/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", mcpRootToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	for _, row := range list.Sessions {
		if row["name"] == name {
			return row
		}
	}
	return nil
}

// TestABridgeJoinKeepsTheHooksRoleAndContext: a member that joined with a role and was given a
// join context, then left, comes back through the bridge's join without losing either.
func TestABridgeJoinKeepsTheHooksRoleAndContext(t *testing.T) {
	u := startMcpHuddle(t, mcpRootToken)
	mcpOp(t, u, "homelab", "join", "proxbeam", `{"role":"reviewer","task":"session started"}`, mcpRootToken)
	mcpOp(t, u, "homelab", "assign", "owner", `{"session":"proxbeam","context":"sync"}`, mcpRootToken)
	mcpOp(t, u, "homelab", "leave", "proxbeam", `{"summary":"gone"}`, mcpRootToken)
	env := proxbeamEnv(u)
	delete(env, "HUDDLE_ROLE") // the project file names no role: the join must not blank the stored one
	delete(env, "HUDDLE_CONTEXT")
	b := runMcpBridge(t, "", env)
	b.send(firstToolCall)
	deadline := time.Now().Add(8 * time.Second)
	var row map[string]any
	for time.Now().Before(deadline) {
		if row = sessionRow(t, u, "homelab", "proxbeam"); row != nil && row["state"] != "left" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if row == nil || row["state"] == "left" {
		t.Fatalf("the bridge never brought proxbeam back: %v", row)
	}
	if row["role"] != "reviewer" {
		t.Errorf("role = %v, want reviewer kept", row["role"])
	}
	if row["context"] != "sync" {
		t.Errorf("context = %v, want sync kept", row["context"])
	}
	if row["task"] != "session started" {
		t.Errorf("task = %v, want the session-start task", row["task"])
	}
}
