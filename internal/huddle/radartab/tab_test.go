// The one-ingester rule both ways, the in-process hook record, and the in-process API the rest
// of huddle reads: radar's own lock decides who ingests, the lock huddle takes is the one radar's
// command line honours, and a hook payload recorded twice lands its id-addressed records once.
package radartab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/cli"
	"github.com/MuhmdRaouf/huddle/internal/radar/server"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// tempEnv answers a hermetic radar environment: every dir under one temp root.
func tempEnv(t *testing.T) domain.EnvLookup {
	t.Helper()
	root := t.TempDir()
	return domain.EnvLookup{
		"RADAR_HOME":         filepath.Join(root, "radar"),
		"HOME":               filepath.Join(root, "home"),
		"CLAUDE_CONFIG_DIR":  filepath.Join(root, "claude"),
		"ZAI_STATE_DIR":      filepath.Join(root, "zai"),
		"KIMI_STATE_DIR":     filepath.Join(root, "kimi"),
		"DEEPSEEK_STATE_DIR": filepath.Join(root, "deepseek"),
		"MINIMAX_STATE_DIR":  filepath.Join(root, "minimax"),
		"QWEN_STATE_DIR":     filepath.Join(root, "qwen"),
	}
}

// mountMux binds a loopback listener, mounts the tab on a fresh mux over it and serves it; the
// port the answers come from is the port the mount names.
func mountMux(t *testing.T, env domain.EnvLookup, options MountOptions) (*Tab, *http.Client, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	options.Env = env
	options.Port = port
	if options.Version == "" {
		options.Version = "test"
	}
	if options.NotifyRunner == nil {
		options.NotifyRunner = func(string, []string) {}
	}
	mux := http.NewServeMux()
	tab := Mount(context.Background(), mux, env["RADAR_HOME"], options)
	t.Cleanup(tab.Stop)
	httpSrv := &http.Server{Handler: mux}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = httpSrv.Serve(listener) }()
	t.Cleanup(func() { _ = httpSrv.Close(); <-serveDone })
	client := &http.Client{Timeout: 10 * time.Second}
	return tab, client, fmt.Sprintf("http://127.0.0.1:%d", port)
}

// getJSON fetches one path and answers its parsed body.
func getJSON(t *testing.T, client *http.Client, base, path string) (int, map[string]any) {
	t.Helper()
	response, err := client.Get(base + path)
	if err != nil {
		t.Fatalf("the %s request failed: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s answer: %v", path, err)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return response.StatusCode, body
}

// TestOneIngesterAStandaloneRadarOwnsTheLock: a recorded server that answers health keeps the
// spool — huddle proxies /api/radar to it, starts no watcher of its own, and leaves its
// server.json untouched.
func TestOneIngesterAStandaloneRadarOwnsTheLock(t *testing.T) {
	env := tempEnv(t)
	removal := 3_600_000
	app, err := server.StartApp(context.Background(), server.AppOptions{
		Env:               env,
		Version:           "standalone",
		IntervalMs:        quietMs,
		InsightsTickMs:    quietMs,
		RemovalIntervalMs: &removal,
		Exit:              func(int) {},
		NotifyRunner:      func(string, []string) {},
	})
	if err != nil {
		t.Fatalf("start the standalone radar: %v", err)
	}
	t.Cleanup(app.Stop)

	lines := &logRecorder{}
	tab, client, base := mountMux(t, env, MountOptions{Log: lines.record, IntervalMs: quietMs, InsightsTickMs: quietMs})
	if tab.Owns {
		t.Fatal("the mount started its own ingester while a standalone radar owns the lock")
	}
	if tab.Watcher != nil {
		t.Fatal("the proxying mount started a watcher")
	}
	if len(lines.lines) != 1 || !strings.Contains(lines.lines[0], "owns radar's dashboard") {
		t.Fatalf("the mount logged %v, want the one proxy line", lines.lines)
	}
	// the standalone keeps its own record: huddle never rewrote server.json
	recorded := server.ReadServerInfo(context.Background(), env)
	if recorded == nil || recorded.Port != app.Port {
		t.Fatalf("server.json = %+v, want the standalone's port %d left alone", recorded, app.Port)
	}
	status, theirs := getJSON(t, client, fmt.Sprintf("http://127.0.0.1:%d", app.Port), "/api/summary")
	if status != 200 {
		t.Fatalf("the standalone's own summary = %d", status)
	}
	status, ours := getJSON(t, client, base, Prefix+"/api/summary")
	if status != 200 {
		t.Fatalf("the proxied summary = %d", status)
	}
	if fmt.Sprint(ours["summary"]) != fmt.Sprint(theirs["summary"]) {
		t.Fatalf("the proxied summary = %v, want the standalone's %v", ours["summary"], theirs["summary"])
	}
	// huddle's own /api/health stays huddle's: only the owner of the lock answers radar there
	if status, _ := getJSON(t, client, base, "/api/health"); status != 404 {
		t.Fatalf("the proxying mount's /api/health = %d, want 404", status)
	}
}

// TestOneIngesterHuddleOwnsAndRadarDefers: with no live owner, huddle takes the lock the way
// radar's own app does, answers radar's health path, and radar's command line — the words the
// plugin runs — name the hub that owns the ingester and start no second one.
func TestOneIngesterHuddleOwnsAndRadarDefers(t *testing.T) {
	env := tempEnv(t)
	tab, client, base := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock on an empty radar home")
	}
	if tab.Watcher == nil {
		t.Fatal("the owning mount runs no watcher")
	}
	// the lock: this process's pid, huddle's port
	recorded := server.ReadServerInfo(context.Background(), env)
	if recorded == nil || recorded.Pid != int64(os.Getpid()) {
		t.Fatalf("server.json = %+v, want this process as the owner", recorded)
	}
	if parsed := recorded.URL[strings.LastIndex(recorded.URL, ":")+1:]; parsed != strconv.Itoa(tab.port) {
		t.Fatalf("server.json names %q, want huddle's port %d", recorded.URL, tab.port)
	}
	// radar's health path answers on huddle's port, the answer radar's own probe reads
	status, health := getJSON(t, client, base, "/api/health")
	if status != 200 || health["ok"] != true {
		t.Fatalf("/api/health = %d %v, want radar's ok answer", status, health)
	}

	// radar's start, run with radar's own decision logic, names the hub that owns
	// the ingester — the hub this mount runs — and starts nothing of its own
	deps := cli.Deps{
		Env:       env,
		Version:   "standalone",
		Out:       func(string) {},
		Err:       func(line string) { t.Logf("radar: %s", line) },
		IsAlive:   func(pid int64) bool { return pid == int64(os.Getpid()) },
		Now:       func() int64 { return time.Now().UnixMilli() },
		SinceMs:   cli.DefaultSinceMs,
		Hub:       func() (string, bool) { return base, true },
		EnsureHub: func(int) (string, bool, string) { return base, true, "" },
	}
	if code := cli.RunCli(context.Background(), []string{"start"}, deps); code != 0 {
		t.Fatalf("radar start = %d, want the hub page 0", code)
	}
	if code := cli.RunCli(context.Background(), []string{"status"}, deps); code != 0 {
		t.Fatalf("radar status = %d, want 0", code)
	}
}

// TestOneIngesterAStaleRecordIsTakenOver: a record whose pid is gone names no owner — huddle
// rewrites it, and the health path of the mount that took it answers.
func TestOneIngesterAStaleRecordIsTakenOver(t *testing.T) {
	env := tempEnv(t)
	if ok := server.WriteServerInfo(context.Background(), env, server.ServerInfo{
		Pid:       1 << 20, // a pid nothing here owns; a live one would still need the health probe
		Port:      1,       // never bound by these tests; the probe must fail, not find an owner
		URL:       "http://127.0.0.1:1",
		StartedAt: 0,
	}); !ok {
		t.Fatal("write the stale record")
	}
	tab, client, base := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("a stale record must not keep the lock")
	}
	recorded := server.ReadServerInfo(context.Background(), env)
	if recorded == nil || recorded.Pid != int64(os.Getpid()) {
		t.Fatalf("server.json = %+v, want the mount's own record", recorded)
	}
	if status, _ := getJSON(t, client, base, "/api/health"); status != 200 {
		t.Fatalf("/api/health = %d, want 200", status)
	}
}

// TestOneIngesterAnotherHuddleOwnsTheLock: the recorded owner may be another huddle serving the
// tab itself; the proxy keeps the /api/radar prefix for such a target instead of stripping it.
func TestOneIngesterAnotherHuddleOwnsTheLock(t *testing.T) {
	env := tempEnv(t)
	owner, _, ownerBase := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !owner.Owns {
		t.Fatal("the first mount did not take the lock")
	}
	_, health := getJSON(t, &http.Client{Timeout: 5 * time.Second}, ownerBase, "/api/health")
	if health["ok"] != true {
		t.Fatalf("the owner's health = %v", health)
	}

	lines := &logRecorder{}
	tab, client, base := mountMux(t, env, MountOptions{Log: lines.record, IntervalMs: quietMs, InsightsTickMs: quietMs})
	if tab.Owns {
		t.Fatal("the second mount started its own ingester while the first huddle owns the lock")
	}
	status, answer := getJSON(t, client, base, Prefix+"/api/health")
	if status != 200 || answer["ok"] != true {
		t.Fatalf("the proxied health = %d %v", status, answer)
	}
	status, summary := getJSON(t, client, base, Prefix+"/api/summary")
	if status != 200 || summary["summary"] == nil {
		t.Fatalf("the proxied summary = %d %v, want the owner's answer", status, summary)
	}
}

// TestRecordHookIsIdempotent: the same payload recorded twice — huddle's hook and the radar
// plugin's hook both firing for one Claude Code event — lands the id-addressed records once:
// one session, one tool call keyed by the tool use id. The timeline keeps both sightings.
func TestRecordHookIsIdempotent(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: 50, InsightsTickMs: 0})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	payloads := []string{
		`{"hook_event_name":"SessionStart","session_id":"sess-dup","cwd":"/w/dup","transcript_path":"/w/dup/t.jsonl"}`,
		`{"hook_event_name":"UserPromptSubmit","session_id":"sess-dup","prompt":"do it twice"}`,
		`{"hook_event_name":"PreToolUse","session_id":"sess-dup","tool_name":"Bash","tool_use_id":"toolu_dup"}`,
		`{"hook_event_name":"PostToolUse","session_id":"sess-dup","tool_name":"Bash","tool_use_id":"toolu_dup"}`,
	}
	environ := []string{"HOME=" + env["HOME"], "RADAR_HOME=" + env["RADAR_HOME"]}
	// two writers, one payload each: the in-process record and the plugin's own line
	for _, raw := range payloads {
		if _, ok := RecordHook(context.Background(), env, environ, raw); !ok {
			t.Fatalf("the in-process record of %s failed", raw)
		}
		if _, ok := RecordHook(context.Background(), env, environ, raw); !ok {
			t.Fatalf("the plugin's record of %s failed", raw)
		}
	}
	waitFor(t, "the recorded events in the store", 5*time.Second, func() bool {
		detail := tab.Store.SessionDetail("sess-dup")
		return detail != nil && len(tab.Store.Tools(store.ToolsFilter{Session: strPtr("sess-dup")})) > 0
	})
	ids := []string{}
	for _, session := range tab.Store.SessionList() {
		ids = append(ids, session.ID)
	}
	if len(ids) != 1 || ids[0] != "sess-dup" {
		t.Fatalf("the sessions = %v, want sess-dup alone", ids)
	}
	tools := tab.Store.Tools(store.ToolsFilter{Session: strPtr("sess-dup")})
	if len(tools) != 1 || tools[0].ID != "toolu_dup" {
		t.Fatalf("the tool calls = %+v, want the one id-addressed record", tools)
	}
	// both sightings stay in the timeline: the store keeps every line it read
	events := tab.Store.Events(store.EventsFilter{Session: strPtr("sess-dup")})
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Kind]++
	}
	if counts["SessionStart"] != 2 {
		t.Fatalf("the timeline's SessionStart rows = %d, want both sightings kept", counts["SessionStart"])
	}
}

// TestRadarAnswerServesInProcess: the mounted tab answers radar's API without a socket — the
// source the registry and the channel views read — and refuses the one path that never answers.
func TestRadarAnswerServesInProcess(t *testing.T) {
	env := tempEnv(t)
	tab, client, base := mountMux(t, env, MountOptions{IntervalMs: 50, InsightsTickMs: 0})
	RecordHook(context.Background(), env, nil,
		`{"hook_event_name":"SessionStart","session_id":"sess-ip","cwd":"/w/ip"}`)
	// a router's route line, the spool's own shape, so the window has a priced request
	today := time.Now().UTC().Format("2006-01-02")
	spoolLine := filepath.Join(env["RADAR_HOME"], "spool", today+".jsonl")
	if err := os.MkdirAll(filepath.Dir(spoolLine), 0o700); err != nil {
		t.Fatal(err)
	}
	route := `{"ts":` + strconv.Quote(time.Now().UTC().Format("2006-01-02T15:04:05.000Z")) +
		`,"event":"route","session_id":"sess-ip","agent_id":"main","model":"glm-5.3","upstream":"https://api.z.ai/api/anthropic","plugin":"zai","route":"direct","status":200,"latency_ms":900,"usage":{"input_tokens":50,"output_tokens":20,"cache_read_input_tokens":500,"cache_creation_input_tokens":100}}`
	if err := os.WriteFile(spoolLine, []byte(route+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session in the store", 5*time.Second, func() bool {
		detail := tab.Store.SessionDetail("sess-ip")
		return detail != nil && len(tab.Store.Requests(store.RequestFilter{Session: strPtr("sess-ip")})) > 0
	})
	body, ok := tab.RadarAnswer(context.Background(), "/api/attribution?by=session&range=day")
	if !ok {
		t.Fatal("the in-process attribution did not answer")
	}
	var attribution struct {
		Rows []struct {
			Key string `json:"key"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(body, &attribution); err != nil {
		t.Fatalf("the in-process attribution = %s", body)
	}
	if len(attribution.Rows) == 0 || attribution.Rows[0].Key != "sess-ip" {
		t.Fatalf("the in-process attribution rows = %+v", attribution.Rows)
	}
	// the same answer the socket serves
	response, err := client.Get(base + Prefix + "/api/attribution?by=session&range=day")
	if err != nil {
		t.Fatalf("the socket attribution failed: %v", err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(raw) != string(body) {
		t.Fatalf("the in-process answer = %s, want the socket's %s", body, raw)
	}
	if _, ok := tab.RadarAnswer(context.Background(), "/api/stream"); ok {
		t.Fatal("a stream path answered in-process; it never ends")
	}
}

// TestRadarAnswerFollowsTheOwnerHuddle: a lock record whose owner serves the prefix — another
// huddle — is asked under /api/radar with the token the record publishes, so the owner's
// credential guard lets the tab's reads through; the browser proxy carries the same token, and
// the inbound one never travels to the owner.
func TestRadarAnswerFollowsTheOwnerHuddle(t *testing.T) {
	env := tempEnv(t)
	var mu sync.Mutex
	var paths, tokens []string
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.EscapedPath())
		tokens = append(tokens, r.Header.Get("x-huddle-token"))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/health", "/api/radar/api/health", "/api/radar/api/sessions/s1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(owner.Close)
	host := strings.TrimPrefix(owner.URL, "http://")
	port, err := strconv.Atoi(host[strings.LastIndex(host, ":")+1:])
	if err != nil {
		t.Fatalf("the fake owner's port: %v", err)
	}
	if ok := server.WriteServerInfo(context.Background(), env, server.ServerInfo{
		Pid:   int64(os.Getpid()),
		Port:  port,
		URL:   owner.URL,
		Token: "tab-token",
	}); !ok {
		t.Fatal("write the owner's lock record")
	}

	lines := &logRecorder{}
	tab, client, base := mountMux(t, env, MountOptions{Log: lines.record, IntervalMs: quietMs, InsightsTickMs: quietMs})
	if tab.Owns {
		t.Fatal("the mount took the lock while the fake owner holds it")
	}
	last := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		if len(paths) == 0 {
			return "", ""
		}
		return paths[len(paths)-1], tokens[len(tokens)-1]
	}

	// the in-process read the registry's cost and provider fields use, addressed the way the
	// owner's own guard demands: the prefix on, the token on
	if _, ok := tab.RadarAnswer(context.Background(), "/api/sessions/s1"); !ok {
		t.Fatal("the owner huddle gave the tab's read no answer")
	}
	if path, token := last(); path != Prefix+"/api/sessions/s1" || token != "tab-token" {
		t.Fatalf("the tab's read reached %q with token %q, want %q with tab-token", path, token, Prefix+"/api/sessions/s1")
	}
	// the same answer the socket's proxy serves a browser
	if status, _ := getJSON(t, client, base, Prefix+"/api/sessions/s1"); status != 200 {
		t.Fatalf("the proxied session = %d, want the owner's 200", status)
	}
	if path, token := last(); path != Prefix+"/api/sessions/s1" || token != "tab-token" {
		t.Fatalf("the proxied read reached %q with token %q, want %q with tab-token", path, token, Prefix+"/api/sessions/s1")
	}
}

// appendSpoolRoute appends one router route line, the spool's own shape, to a radar home's
// spool, so its ingester carries one priced request for the session.
func appendSpoolRoute(t *testing.T, radarHome, session string) {
	t.Helper()
	spoolLine := filepath.Join(radarHome, "spool", time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.MkdirAll(filepath.Dir(spoolLine), 0o700); err != nil {
		t.Fatal(err)
	}
	route := `{"ts":` + strconv.Quote(time.Now().UTC().Format("2006-01-02T15:04:05.000Z")) +
		`,"event":"route","session_id":"` + session +
		`","agent_id":"main","model":"glm-5.3","upstream":"https://api.z.ai/api/anthropic",` +
		`"plugin":"zai","route":"direct","status":200,"latency_ms":900,` +
		`"usage":{"input_tokens":50,"output_tokens":20,"cache_read_input_tokens":500,` +
		`"cache_creation_input_tokens":100}}`
	file, err := os.OpenFile(spoolLine, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(route + "\n"); err != nil {
		t.Fatal(err)
	}
}

// TestAnOwningTabRecordsHooksAndAnswersFacts: the mount's own hook record (the method the
// report route calls) lands one session in the store, and the session's cost and provider come
// from the same API the dashboard reads — null until a request carries a price and a model.
func TestAnOwningTabRecordsHooksAndAnswersFacts(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: 50, InsightsTickMs: 0})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	if _, ok := tab.RecordHook(context.Background(),
		`{"hook_event_name":"SessionStart","session_id":"sess-tab","cwd":"/w/tab"}`); !ok {
		t.Fatal("the owning mount refused the record")
	}
	waitFor(t, "the recorded session in the store", 5*time.Second, func() bool {
		return tab.Store.SessionDetail("sess-tab") != nil
	})
	// a session with no request carries no price and no model: both facts null
	if facts := tab.FactsOf(context.Background(), "sess-tab"); facts.CostUSD != nil || facts.Provider != nil {
		t.Fatalf("the facts of an unpriced session = %+v, want both nil", facts)
	}
	appendSpoolRoute(t, env["RADAR_HOME"], "sess-tab")
	waitFor(t, "the priced request in the store", 5*time.Second, func() bool {
		return len(tab.Store.Requests(store.RequestFilter{Session: strPtr("sess-tab")})) > 0
	})
	facts := tab.FactsOf(context.Background(), "sess-tab")
	if facts.CostUSD == nil || *facts.CostUSD <= 0 {
		t.Fatalf("the cost = %+v, want radar's estimate over 0", facts)
	}
	if facts.Provider == nil || *facts.Provider != "Z.ai" {
		t.Fatalf("the provider = %+v, want Z.ai, glm-5.3's provider", facts)
	}
	if facts := tab.FactsOf(context.Background(), "sess-nobody"); facts.CostUSD != nil || facts.Provider != nil {
		t.Fatalf("the facts of an unknown session = %+v, want both nil", facts)
	}
}

// TestAProxyingTabRecordsNothing: while a standalone radar owns the lock, the mount records no
// hook line of its own (the owner's hook does) and answers the facts through the owner.
func TestAProxyingTabRecordsNothing(t *testing.T) {
	env := tempEnv(t)
	removal := 3_600_000
	app, err := server.StartApp(context.Background(), server.AppOptions{
		Env:               env,
		Version:           "standalone",
		IntervalMs:        50, // polls the spool, so the appended route line reaches its store
		InsightsTickMs:    quietMs,
		RemovalIntervalMs: &removal,
		Exit:              func(int) {},
		NotifyRunner:      func(string, []string) {},
	})
	if err != nil {
		t.Fatalf("start the standalone radar: %v", err)
	}
	t.Cleanup(app.Stop)
	appendSpoolRoute(t, env["RADAR_HOME"], "sess-own")
	tab, _, _ := mountMux(t, env, MountOptions{Log: func(string) {}, IntervalMs: quietMs, InsightsTickMs: quietMs})
	if tab.Owns {
		t.Fatal("the mount started its own ingester while the standalone owns the lock")
	}
	if _, ok := tab.RecordHook(context.Background(),
		`{"hook_event_name":"Stop","session_id":"sess-own"}`); ok {
		t.Fatal("the proxying mount recorded a hook line")
	}
	waitFor(t, "the facts through the owner", 5*time.Second, func() bool {
		facts := tab.FactsOf(context.Background(), "sess-own")
		return facts.CostUSD != nil && facts.Provider != nil
	})
}

// TestStopReleasesTheLock: a stopped mount leaves no record behind, so the next starter —
// radar's or huddle's — is not told an owner still runs.
func TestStopReleasesTheLock(t *testing.T) {
	env := tempEnv(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer func() { _ = listener.Close() }()
	port := listener.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	tab := Mount(context.Background(), mux, env["RADAR_HOME"], MountOptions{
		Env: env, Port: port, Version: "test",
		IntervalMs: quietMs, InsightsTickMs: quietMs,
		NotifyRunner: func(string, []string) {},
	})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	tab.Stop()
	if recorded := server.ReadServerInfo(context.Background(), env); recorded != nil {
		t.Fatalf("server.json = %+v after the stop, want it gone", recorded)
	}
	tab.Stop() // idempotent
}

// strPtr lifts one string.
func strPtr(text string) *string { return &text }
