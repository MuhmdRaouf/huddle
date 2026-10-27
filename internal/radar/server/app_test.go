// server.json and the whole-app wiring — bind, publish, stop — plus the three whole-app
// removal cases and the clean-shutdown proof.
package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/alerts"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// quietMs keeps the watcher on its first pass only: no poll lands inside a test.
const quietMs = 3_600_000

// writeText mkdir -p's the parent, then writes with 0600 — the mode every spool/state file
// has.
func writeText(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// getJSON reads one JSON body over real loopback http — the point is that a real client can
// talk to the app.
func getJSON(t *testing.T, port int, path string) map[string]any {
	t.Helper()
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
	if err != nil {
		t.Fatalf("the %s request failed: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode the %s answer: %v", path, err)
	}
	return body
}

// startBlocker holds a foreign socket on a free port, to make binds fail or to reserve one.
// The stop it answers releases the port.
func startBlocker(t *testing.T) (stop func(), port int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind the blocker: %v", err)
	}
	return func() { _ = listener.Close() }, listener.Addr().(*net.TCPAddr).Port
}

// dialRefused says whether the port refuses the connection — the socket is gone.
func dialRefused(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

// seedSpool writes the spool file the watcher's first pass picks up: today's date, fresh
// timestamps. It answers the file's path.
func seedSpool(t *testing.T, state string, lines ...string) string {
	t.Helper()
	today := time.Now().UTC().Format("2006-01-02")
	return writeText(t, filepath.Join(state, "spool", today+".jsonl"), strings.Join(lines, "\n")+"\n")
}

// spoolStart is one SessionStart hook line with a fresh timestamp.
func spoolStart(session, cwd string) string {
	return fmt.Sprintf(`{"ts":%q,"event":"SessionStart","session_id":%q,"cwd":%q}`,
		isoOf(time.Now().UnixMilli()), session, cwd)
}

// spoolPrompt is one UserPromptSubmit hook line with a fresh timestamp.
func spoolPrompt(session, prompt string) string {
	return fmt.Sprintf(`{"ts":%q,"event":"UserPromptSubmit","session_id":%q,"prompt":%q}`,
		isoOf(time.Now().UnixMilli()), session, prompt)
}

// exitRecorder is the exit the tests observe instead of ending the process.
type exitRecorder struct {
	mu    sync.Mutex
	codes []int
}

func (r *exitRecorder) record(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codes = append(r.codes, code)
}

func (r *exitRecorder) snapshot() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.codes...)
}

// logRecorder is the one line a missing history file leaves.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) record(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, line)
}

func (r *logRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// notifyRecorder counts the desktop notifier commands one run asked the OS to show.
type notifyRecorder struct {
	mu    sync.Mutex
	calls int
}

func (r *notifyRecorder) record(string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
}

func (r *notifyRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// mustStart starts the app or fails the test.
func mustStart(t *testing.T, env domain.EnvLookup, options AppOptions) *App {
	t.Helper()
	options.Env = env
	app, err := StartApp(t.Context(), options)
	if err != nil {
		t.Fatalf("start the app: %v", err)
	}
	return app
}

// sessionIDs lists the store's session ids.
func sessionIDs(t *testing.T, app *App) []string {
	t.Helper()
	ids := []string{}
	for _, session := range app.Store.SessionList() {
		ids = append(ids, session.ID)
	}
	return ids
}

// waitForSessions waits for the watcher's first pass to land the given sessions.
func waitForSessions(t *testing.T, app *App, want ...string) {
	t.Helper()
	waitFor(t, fmt.Sprintf("sessions %v in the store", want), 5*time.Second, func() bool {
		return strings.Join(sessionIDs(t, app), ",") == strings.Join(want, ",")
	})
}

// fileMode is a path's permission bits.
func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// exists says whether a path is there.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// msPtr lifts an interval into the override the options take.
func msPtr(ms int) *int { return &ms }

const radarKey = "radar@muhmdraouf"

// registryEntry is one install the way Claude Code records it.
var registryEntry = []any{map[string]any{"scope": "user", "installPath": "/x", "version": "1"}}

// installRegistry writes the registry exactly as Claude Code writes it: the map under
// "plugins", entries as lists.
func installRegistry(t *testing.T, config string, plugins map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 2, "plugins": plugins})
	if err != nil {
		t.Fatalf("marshal the registry: %v", err)
	}
	writeText(t, filepath.Join(config, "plugins", "installed_plugins.json"), string(body))
}

func installedRadar(t *testing.T, config string) {
	t.Helper()
	installRegistry(t, config, map[string]any{radarKey: registryEntry})
}

func uninstalledRegistry(t *testing.T, config string) {
	t.Helper()
	installRegistry(t, config, map[string]any{"other@market": registryEntry}) // others remain, ours is gone
}

func radarOffInUserSettings(t *testing.T, config string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"enabledPlugins": map[string]bool{radarKey: false}})
	if err != nil {
		t.Fatalf("marshal the settings: %v", err)
	}
	writeText(t, filepath.Join(config, "settings.json"), string(body))
}

func TestServerJson(t *testing.T) {
	t.Run("round-trips through a 0600 file, creating the state dir on the way", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		if exists(world.state) {
			t.Fatalf("a first-ever start has no state dir yet")
		}
		if !WriteServerInfo(ctx, world.env, ServerInfo{Pid: 4242, Port: 8787, URL: "http://127.0.0.1:8787", StartedAt: 42}) {
			t.Fatalf("the first write succeeds")
		}
		if mode := fileMode(t, world.state); mode != 0o700 {
			t.Fatalf("the state dir is 0700, got %#o", mode)
		}
		if mode := fileMode(t, paths.ServerInfoPath(ctx, world.env)); mode != 0o600 {
			t.Fatalf("server.json is 0600, got %#o", mode)
		}
		got := ReadServerInfo(ctx, world.env)
		if got == nil {
			t.Fatalf("server.json reads back")
		}
		want := ServerInfo{Pid: 4242, Port: 8787, URL: "http://127.0.0.1:8787", StartedAt: 42}
		if *got != want {
			t.Fatalf("server.json round-trips: got %+v, want %+v", *got, want)
		}
	})

	t.Run("reads nothing from malformed or mistyped files", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		writeText(t, paths.ServerInfoPath(ctx, world.env), "{oops")
		if got := ReadServerInfo(ctx, world.env); got != nil {
			t.Fatalf("a torn file reads as nothing, got %+v", *got)
		}
		writeText(t, paths.ServerInfoPath(ctx, world.env), `{"pid":"1","port":2}`) // pid as a string
		if got := ReadServerInfo(ctx, world.env); got != nil {
			t.Fatalf("a string pid reads as nothing, got %+v", *got)
		}
		writeText(t, paths.ServerInfoPath(ctx, world.env), `{"pid":1}`) // no port
		if got := ReadServerInfo(ctx, world.env); got != nil {
			t.Fatalf("a file without a port reads as nothing, got %+v", *got)
		}
	})

	t.Run("synthesizes the url and zeroes startedAt for a minimal but valid file", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		writeText(t, paths.ServerInfoPath(ctx, world.env), `{"pid":9,"port":9001}`)
		got := ReadServerInfo(ctx, world.env)
		if got == nil {
			t.Fatalf("the minimal file reads")
		}
		want := ServerInfo{Pid: 9, Port: 9001, URL: "http://127.0.0.1:9001", StartedAt: 0}
		if *got != want {
			t.Fatalf("the minimal file reads back filled in: got %+v, want %+v", *got, want)
		}
	})

	t.Run("removes idempotently, present or not", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		RemoveServerInfo(ctx, world.env) // nothing there: the common case after a crash
		RemoveServerInfo(ctx, world.env)
		if exists(paths.ServerInfoPath(ctx, world.env)) {
			t.Fatalf("nothing was there to leave")
		}
		if !WriteServerInfo(ctx, world.env, ServerInfo{Pid: 1, Port: 1, URL: "u", StartedAt: 0}) {
			t.Fatalf("the write succeeds")
		}
		RemoveServerInfo(ctx, world.env)
		if exists(paths.ServerInfoPath(ctx, world.env)) {
			t.Fatalf("the written file goes")
		}
	})

	t.Run("reports a failed write against an unwritable file, leaving the old contents intact", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		path := paths.ServerInfoPath(ctx, world.env)
		if !WriteServerInfo(ctx, world.env, ServerInfo{Pid: 1, Port: 1111, URL: "http://127.0.0.1:1111", StartedAt: 1}) {
			t.Fatalf("the first write succeeds")
		}
		if err := os.Chmod(path, 0o400); err != nil { // a write on a read-only file fails where it stands
			t.Fatalf("chmod the file read-only: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		if WriteServerInfo(ctx, world.env, ServerInfo{Pid: 2, Port: 2222, URL: "http://127.0.0.1:2222", StartedAt: 2}) {
			t.Fatalf("the second write reports its failure")
		}
		if got := ReadServerInfo(ctx, world.env); got == nil || got.Pid != 1 {
			t.Fatalf("the open failed before it could truncate anything: got %+v", got)
		}
	})
}

// TestTheAppPrunesTheLedgerAtStart: the app runs the ledger's prune beat, so its start cuts
// the ledger file back to the TTL. A row 100 days old goes at the start; a 1-day-old one
// stays.
func TestTheAppPrunesTheLedgerAtStart(t *testing.T) {
	world := makeEnv(t)
	ctx := t.Context()
	ledPath := paths.LedgerPath(ctx, world.env)
	now := time.Now()
	seed, err := ledger.Open(ledPath)
	if err != nil {
		t.Fatalf("open the seed ledger: %v", err)
	}
	rows := []ledger.Row{
		{ID: "req_100", StartedAt: now.Add(-100 * 24 * time.Hour).UnixMilli()},
		{ID: "req_1", StartedAt: now.Add(-24 * time.Hour).UnixMilli()},
	}
	for _, row := range rows {
		if err := seed.Upsert(ctx, row); err != nil {
			t.Fatalf("upsert %s: %v", row.ID, err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close the seed ledger: %v", err)
	}

	app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
	app.Stop()

	again, err := ledger.Open(ledPath)
	if err != nil {
		t.Fatalf("reopen the ledger: %v", err)
	}
	t.Cleanup(func() { _ = again.Close() })
	if row, err := again.Get(ctx, "req_100"); err != nil || row != nil {
		t.Fatalf("the 100-day row = %+v, %v; want it gone at the start", row, err)
	}
	if row, err := again.Get(ctx, "req_1"); err != nil || row == nil {
		t.Fatalf("the 1-day row = %+v, %v; want it kept", row, err)
	}
}

func TestStartApp(t *testing.T) {
	t.Run("runs without history when the history file cannot be opened, logging one line", func(t *testing.T) {
		world := makeEnv(t)
		logs := &logRecorder{}
		// a directory where the database file must go: the open refuses it
		if err := os.MkdirAll(filepath.Join(world.state, "history.db"), 0o700); err != nil {
			t.Fatalf("block the history file: %v", err)
		}
		seedSpool(t, world.state, spoolStart("s1", "/w/app"))
		app := mustStart(t, world.env, AppOptions{
			Version: "test", SinceMs: dayMs, IntervalMs: quietMs, Log: logs.record,
		})
		if app.Writer != nil {
			t.Fatalf("the writer is nil without history")
		}
		lines := logs.snapshot()
		if len(lines) != 1 || !strings.Contains(lines[0], "history unavailable") {
			t.Fatalf("one history-unavailable line, got %q", lines)
		}
		waitForSessions(t, app, "s1")
		if health := getJSON(t, app.Port, "/api/health"); health["ok"] != true || health["sessions"] != float64(1) {
			t.Fatalf("ingest went on as usual: %v", health) // ingest went on as usual
		}
		app.Stop()
	})

	t.Run("binds loopback, publishes its own pid and answers real http before it resolves", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		seedSpool(t, world.state, spoolStart("s1", "/w/app"), spoolPrompt("s1", "hello"))
		app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
		if app.Port < 10_000 || app.Port >= 65_536 {
			t.Fatalf("the port is a five-digit one, got %d", app.Port)
		}
		if want := fmt.Sprintf("http://127.0.0.1:%d", app.Port); app.URL != want {
			t.Fatalf("the url names the port: got %s, want %s", app.URL, want)
		}
		waitForSessions(t, app, "s1") // the first pass ran
		if mode := fileMode(t, paths.ServerInfoPath(ctx, world.env)); mode != 0o600 {
			t.Fatalf("server.json is 0600, got %#o", mode)
		}
		info := ReadServerInfo(ctx, world.env)
		if info == nil || info.Pid != int64(os.Getpid()) || info.Port != app.Port || info.URL != app.URL || info.StartedAt <= 0 {
			t.Fatalf("server.json names this process: got %+v", info) // the app writes server.json itself
		}
		health := getJSON(t, app.Port, "/api/health")
		if health["ok"] != true || health["version"] != "test" || health["port"] != float64(app.Port) || health["sessions"] != float64(1) {
			t.Fatalf("health answers over real http: %v", health)
		}
		app.Stop()
		if exists(paths.ServerInfoPath(ctx, world.env)) {
			t.Fatalf("stop removes server.json")
		}
		if !dialRefused(app.Port) {
			t.Fatalf("the socket is gone")
		}
	})

	t.Run("keeps its port across a stop: the next start takes the saved one, a taken one moves and says so", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		first := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
		port := first.Port
		first.Stop()
		if saved, ok := paths.SavedPort(ctx, world.env); !ok || saved != port {
			t.Fatalf("the port is kept after the stop, unlike server.json: got %d ok=%v", saved, ok)
		}
		again := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
		if again.Port != port {
			t.Fatalf("the next start takes the saved port: got %d, want %d", again.Port, port)
		}
		if again.MovedFrom != 0 {
			t.Fatalf("a free saved port never moves: movedFrom %d", again.MovedFrom)
		}
		again.Stop()
		stopBlocker, taken := startBlocker(t)
		paths.SavePort(ctx, world.env, taken)
		moved := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
		if moved.Port == taken {
			t.Fatalf("a taken port is not bound again")
		}
		if moved.MovedFrom != taken {
			t.Fatalf("the move names the port it could not have: got %d, want %d", moved.MovedFrom, taken)
		}
		if saved, ok := paths.SavedPort(ctx, world.env); !ok || saved != moved.Port {
			t.Fatalf("the saved port follows the move: got %d ok=%v", saved, ok)
		}
		moved.Stop()
		stopBlocker()
	})

	t.Run("binds exactly a pinned --port and records it", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		stopBlocker, port := startBlocker(t) // reserve, then vacate
		stopBlocker()
		app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, Port: port, IntervalMs: quietMs})
		if app.Port != port {
			t.Fatalf("the pinned port is bound: got %d, want %d", app.Port, port)
		}
		if info := ReadServerInfo(ctx, world.env); info == nil || info.Port != port {
			t.Fatalf("server.json records the pinned port: got %+v", info)
		}
		app.Stop()
	})

	t.Run("fails a pinned port that is busy, without publishing anything", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		stopBlocker, port := startBlocker(t)
		t.Cleanup(stopBlocker)
		_, err := StartApp(ctx, AppOptions{
			Env: world.env, Version: "test", SinceMs: dayMs, Port: port, IntervalMs: quietMs,
		})
		if err == nil || !strings.Contains(err.Error(), AddrInUse) {
			t.Fatalf("one attempt, the error surfaces as-is: got %v", err)
		}
		if exists(paths.ServerInfoPath(ctx, world.env)) {
			t.Fatalf("no url to a server that never bound")
		}
	})

	t.Run("gives up after 50 hopeless random draws and publishes nothing", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		stopBlocker, port := startBlocker(t)
		t.Cleanup(stopBlocker)
		draws := 0
		_, err := StartApp(ctx, AppOptions{
			Env:     world.env,
			Version: "test",
			SinceMs: dayMs,
			Random: func(int, int) int {
				draws++
				return port // every draw lands on the busy port, EADDRINUSE being retryable
			},
			IntervalMs: quietMs,
		})
		if err == nil || !strings.Contains(err.Error(), "no free port after 50 random draws") {
			t.Fatalf("the spent budget surfaces: got %v", err)
		}
		if draws != 50 {
			t.Fatalf("exactly 50 draws, got %d", draws)
		}
		if exists(paths.ServerInfoPath(ctx, world.env)) {
			t.Fatalf("nothing was published")
		}
	})
}

func TestTheAppRemovesItself(t *testing.T) {
	t.Run("stops, deletes its state dir and exits 0 when two checks agree the plugin is uninstalled", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		installedRadar(t, world.config)
		codes := &exitRecorder{}
		app := mustStart(t, world.env, AppOptions{
			Version: "test", SinceMs: quietMs, IntervalMs: quietMs,
			RemovalIntervalMs: msPtr(15), Exit: codes.record,
		})
		if sessions := app.Store.SessionList(); len(sessions) != 0 {
			t.Fatalf("up and serving with no sessions, got %d", len(sessions))
		}
		writeText(t, filepath.Join(world.state, "spool", "today.jsonl"), "{}\n") // something to take with us
		uninstalledRegistry(t, world.config)
		waitFor(t, "exit after removal", 5*time.Second, func() bool { return len(codes.snapshot()) > 0 })
		if got := codes.snapshot(); len(got) != 1 || got[0] != 0 {
			t.Fatalf("one clean exit, got %v", got)
		}
		if exists(world.state) {
			t.Fatalf("the whole state dir went")
		}
		if exists(paths.ServerInfoPath(ctx, world.env)) {
			t.Fatalf("server.json went with it")
		}
		if !dialRefused(app.Port) {
			t.Fatalf("not accepting anything")
		}
		app.Stop() // already stopped: an idempotent second stop
	})

	t.Run("stops and exits 0 on disable, but keeps the state dir for the reinstall", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		installedRadar(t, world.config)
		codes := &exitRecorder{}
		app := mustStart(t, world.env, AppOptions{
			Version: "test", SinceMs: quietMs, IntervalMs: quietMs,
			RemovalIntervalMs: msPtr(15), Exit: codes.record,
		})
		spool := writeText(t, filepath.Join(world.state, "spool", "today.jsonl"), "{}\n") // the history that stays
		radarOffInUserSettings(t, world.config)
		waitFor(t, "exit after disable", 5*time.Second, func() bool { return len(codes.snapshot()) > 0 })
		if got := codes.snapshot(); len(got) != 1 || got[0] != 0 {
			t.Fatalf("one clean exit, got %v", got)
		}
		if !exists(spool) {
			t.Fatalf("the state dir stayed")
		}
		if exists(paths.ServerInfoPath(ctx, world.env)) {
			t.Fatalf("the server's own file went")
		}
		if !dialRefused(app.Port) {
			t.Fatalf("not accepting anything")
		}
		app.Stop()
	})

	t.Run("keeps serving through a torn registry and a torn settings file", func(t *testing.T) {
		world := makeEnv(t)
		installedRadar(t, world.config)
		codes := &exitRecorder{}
		app := mustStart(t, world.env, AppOptions{
			Version: "test", SinceMs: quietMs, IntervalMs: quietMs,
			RemovalIntervalMs: msPtr(15), Exit: codes.record,
		})
		writeText(t, filepath.Join(world.config, "plugins", "installed_plugins.json"), "{torn")
		writeText(t, filepath.Join(world.config, "settings.json"), "{torn")
		time.Sleep(120 * time.Millisecond) // several checks, all unknown
		if got := codes.snapshot(); len(got) != 0 {
			t.Fatalf("an unknown reading never acts, got %v", got)
		}
		installedRadar(t, world.config)    // back to installed and enabled
		time.Sleep(120 * time.Millisecond) // still installed: nothing happens
		if got := codes.snapshot(); len(got) != 0 {
			t.Fatalf("an installed plugin never acts, got %v", got)
		}
		if dialRefused(app.Port) {
			t.Fatalf("still serving")
		}
		app.Stop()
	})
}

func TestAppStopsClean(t *testing.T) {
	t.Run("stops with nothing left running: every goroutine it started ends with Stop", func(t *testing.T) {
		world := makeEnv(t)
		before := runtime.NumGoroutine()
		app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: quietMs, IntervalMs: quietMs})
		app.Stop()
		waitFor(t, "the app's goroutines to end", 2*time.Second, func() bool {
			return runtime.NumGoroutine() <= before
		})
	})
}

func TestAppHoldsNotificationsWhileCatchingUp(t *testing.T) {
	t.Run("no desktop notification and no notified.json until the first ingest pass drains", func(t *testing.T) {
		world := makeEnv(t)
		ctx := t.Context()
		// one routed glm-5.3 call that overspends a $1 monthly budget: an alert worth a
		// notification from the moment its line lands
		route := fmt.Sprintf(`{"ts":%q,"event":"route","session_id":"s0","agent_id":"main",`+
			`"plugin":"zai","model":"glm-5.3","upstream":"https://api.z.ai/api/anthropic",`+
			`"route":"provider","status":200,"latency_ms":900,`+
			`"usage":{"input_tokens":1000000,"output_tokens":1000000}}`, isoOf(time.Now().UnixMilli()))
		// a backlog long enough that the first pass is still draining once the app is up
		lines := []string{route}
		for i := 0; i < 50_000; i++ {
			lines = append(lines, fmt.Sprintf(`{"ts":%q,"event":"UserPromptSubmit","session_id":%q,"prompt":"filling the backlog"}`,
				isoOf(time.Now().UnixMilli()), fmt.Sprintf("s%d", i%40)))
		}
		seedSpool(t, world.state, lines...)
		writeText(t, filepath.Join(world.state, "budgets.json"),
			`{"version":1,"budgets":[{"id":"zai-month","scope":"provider:zai","period":"month","limitUsd":1,"action":"warn"}]}`)
		notified := &notifyRecorder{}
		app := mustStart(t, world.env, AppOptions{
			Version: "test", SinceMs: dayMs, IntervalMs: quietMs,
			InsightsTickMs: 50, NotifyRunner: notified.record,
		})
		defer app.Stop()
		notifiedPath := alerts.NotifiedPath(ctx, world.env)

		// the overspent budget answers in the alerts API while the backlog still drains: the
		// hold is what stops the notification, not a missing alert
		waitFor(t, "the budget alert while the first pass drains", 10*time.Second, func() bool {
			if !app.Watcher.CatchingUp() {
				return false
			}
			answer := getJSON(t, app.Port, "/api/alerts")
			list, _ := answer["alerts"].([]any)
			return len(list) > 0
		})
		for app.Watcher.CatchingUp() {
			if exists(notifiedPath) {
				t.Fatalf("notified.json appeared before the first pass drained")
			}
			if notified.count() != 0 {
				t.Fatalf("a desktop notification went out before the first pass drained")
			}
			time.Sleep(20 * time.Millisecond)
		}
		waitFor(t, "the held notification once the backlog drained", 10*time.Second, func() bool {
			body, err := os.ReadFile(notifiedPath)
			return err == nil && strings.Contains(string(body), "budget:zai-month")
		})
	})
}

func TestProjectDirsOf(t *testing.T) {
	t.Run("lists each project a tracked session ran in once, newest session first", func(t *testing.T) {
		st := store.CreateStore(time.Now)
		// one millisecond apart: sessions started in the same millisecond have no order
		at := time.Now().UnixMilli()
		session := func(id, cwd string) shared.SpoolLine {
			at++
			line := shared.SpoolLine{Ts: isoOf(at), Event: "SessionStart", SessionID: &id}
			if cwd != "" {
				line.Cwd = &cwd
			}
			return line
		}
		st.AddSpoolLine(session("s1", "/w/app"))
		st.AddSpoolLine(session("s2", "/w/app")) // the same project twice: one entry
		st.AddSpoolLine(session("s3", ""))       // no cwd: no project
		st.AddSpoolLine(session("s4", "/w/other"))
		got := projectDirsOf(st)
		want := []string{"/w/other", "/w/app"}
		if !slices.Equal(got, want) {
			t.Fatalf("the projects this server tracked: got %v, want %v", got, want)
		}
	})
}
