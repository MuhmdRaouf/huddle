package main

// The hub leg's end to end, over the surfaces a fresh install really runs: the committed
// launcher downloading the binary from a fake release server on a PATH with no JavaScript
// runtime, the hooks.json lines a session lives through, a passkey pairing, a permission allow
// that owes its step-up, a satellite joining from a second home, two linked sessions and the
// radar tab. Every process here is a child of this test, every home a temp one, and nothing of
// the real machine — no keychain, no ~/.config, no release server — is ever reached.

import (
	"bufio"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// strippedPath is the PATH a fresh install lives on: the system directories alone, where no
// JavaScript runtime sits on this machine or in CI.
const strippedPath = "/usr/bin:/bin:/usr/sbin:/sbin"

// pluginSource is the committed plugin dir the copy installs from, from cmd/huddle.
func pluginSource(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	return filepath.Join(root, "plugin")
}

// releaseTarget names the release asset's platform token this test runs on.
func releaseTarget() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

// TestE2EFreshInstallHubSatellitePWA is the whole leg, one step at a time: a step that fails
// stops the ones after it, because every one of them builds on the world the earlier ones made.
func TestE2EFreshInstallHubSatellitePWA(t *testing.T) {
	root := t.TempDir()
	pluginCopy := filepath.Join(root, "plugin")
	binHome := filepath.Join(root, "huddle-bin")
	hubRoot := filepath.Join(root, "hub-home")
	hubUser := filepath.Join(root, "hub-user")
	hubProject := filepath.Join(root, "project")
	satRoot := filepath.Join(root, "sat-home")
	satUser := filepath.Join(root, "sat-user")
	satBin := filepath.Join(root, "sat-bin")
	satProject := filepath.Join(root, "sat-project")
	for _, dir := range []string{
		filepath.Join(hubProject, ".git", "info"), filepath.Join(satProject, ".git", "info"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make the project: %v", err)
		}
	}

	var (
		release  *httptest.Server
		hubBin   string
		hubPort  int
		hubURL   string
		satPort  int
		hubLog   *serveLog
		phone    *webauthntest.SoftKey
		jar      *browserClient
		handle   string
		push     *e2ePush
		pushKeys e2ePushKeys
	)
	parent := t
	step := func(name string, body func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			if parent.Failed() {
				t.Fatalf("an earlier step failed; this one cannot run")
			}
			body(t)
		})
	}

	step("no_js_runtime", func(t *testing.T) {
		t.Setenv("PATH", strippedPath)
		for _, name := range []string{"bun", "node"} {
			if _, err := exec.LookPath(name); err == nil {
				t.Skipf("the host keeps %s under the stripped PATH, so the no-runtime claim cannot be proven here", name)
			}
		}
	})

	step("install", func(t *testing.T) {
		if err := os.CopyFS(pluginCopy, os.DirFS(pluginSource(t))); err != nil {
			t.Fatalf("copy the plugin: %v", err)
		}
		release = fakeReleaseServer(parent, t, huddleBin)
		pinLauncherChecksum(t, filepath.Join(pluginCopy, "dist", "run"), huddleBin)

		env := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL, nil)
		code, stdout, stderr := runSh(t, 90*time.Second, env, hubProject, `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" help`, "")
		if code != 0 {
			t.Fatalf("dist/run help = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		hubBin = filepath.Join(binHome, huddleVersion(t), "huddle")
		info, err := os.Stat(hubBin)
		if err != nil {
			t.Fatalf("the downloaded binary never landed at %s: %v", hubBin, err)
		}
		if info.Mode()&0o100 == 0 {
			t.Fatalf("the downloaded binary at %s is not executable", hubBin)
		}
	})

	step("hub_up", func(t *testing.T) {
		hubPort = pinFreePort(t, hubRoot)
		hubLog = startServe(parent, t, hubBin, serveEnv{
			home: hubRoot, user: hubUser, port: hubPort, token: "hub-root-token",
			data: filepath.Join(hubUser, "data"), pushSweepMS: 500,
		})
		hubURL = fmt.Sprintf("http://127.0.0.1:%d", hubPort)
		until(t, 30*time.Second, func() bool { return healthy(hubURL) }, "the hub to answer /health")
	})

	step("session_start", func(t *testing.T) {
		env := hookEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, hubURL, "hub-root-token", nil)
		for _, command := range hookCommands(t, pluginCopy, "SessionStart") {
			code, stdout, stderr := runSh(t, 30*time.Second, env, hubProject, command,
				`{"hook_event_name":"SessionStart","session_id":"s-one","source":"startup","cwd":`+strconv.Quote(hubProject)+`}`)
			if code != 0 {
				t.Fatalf("%s = %d, stdout %q, stderr %q", command, code, stdout, stderr)
			}
		}
		listed := func() bool { return registryHas(t, hubURL, "hub-root-token", "s-one", "") }
		// a report that outruns the hook's budget on a loaded machine waits in the outbox, and
		// the session's next event replays it: each next prompt is that event, until one lands
		delivered := pollFor(10*time.Second, listed)
		for deadline := time.Now().Add(40 * time.Second); !delivered && time.Now().Before(deadline); {
			for _, command := range hookCommands(t, pluginCopy, "UserPromptSubmit") {
				_, _, _ = runSh(t, 30*time.Second, env, hubProject, command,
					`{"hook_event_name":"UserPromptSubmit","session_id":"s-one","prompt":"go on","cwd":`+strconv.Quote(hubProject)+`}`)
			}
			delivered = pollFor(5*time.Second, listed)
		}
		if !delivered {
			dumpChildDiagnostics(t, hubRoot, binHome)
			t.Logf("=== the hub serve log ===\n%s", hubLog.String())
			t.Fatal("the hub's registry never listed s-one")
		}
	})

	step("pair_passkey", func(t *testing.T) {
		env := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL,
			map[string]string{"HUDDLE_URL": hubURL})
		code, stdout, stderr := runSh(t, 60*time.Second, env, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" pair --name e2e-phone`, "")
		if code != 0 {
			t.Fatalf("huddle pair = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		match := regexpPairLink.FindStringSubmatch(stdout)
		if match == nil {
			t.Fatalf("no pairing link in the pair output: %q", stdout)
		}
		phone = webauthntest.NewSoftKey(t)
		jar = newBrowser(hubURL)

		code, body := jar.post(t, "/api/pair/begin",
			fmt.Sprintf(`{"token":%q,"name":"e2e-phone"}`, match[1]))
		if code != 200 {
			t.Fatalf("pair begin = %d: %s", code, body)
		}
		options, parsed := parseBegin(t, body)
		handle = parsed.UserHandle
		answer := phone.Create(t, options, fmt.Sprintf("http://localhost:%d", hubPort))
		code, body = jar.post(t, "/api/pair/finish",
			fmt.Sprintf(`{"token":%q,"response":%s}`, match[1], answer))
		if code != 200 {
			t.Fatalf("pair finish = %d: %s", code, body)
		}
		if cookie := jar.cookie(t, auth.PairPlainCookie); cookie == "" {
			t.Fatal("the pairing earned no session cookie")
		}
	})

	step("now_list", func(t *testing.T) {
		code, body := jar.get(t, "/api/registry/sessions")
		if code != 200 {
			t.Fatalf("the Now list's sessions = %d: %s", code, body)
		}
		if !strings.Contains(body, `"session_id":"s-one"`) {
			t.Fatalf("the Now list's sessions = %s, want s-one listed", body)
		}
		if code, body = jar.get(t, "/api/needs"); code != 200 {
			t.Fatalf("the Now list's needs = %d: %s", code, body)
		}
	})

	step("push_subscribe", func(t *testing.T) {
		push = newE2EPush(parent, t)
		code, body := jar.get(t, "/api/push/key")
		if code != 200 {
			t.Fatalf("the hub's public key = %d: %s", code, body)
		}
		var key struct {
			PublicKey string `json:"public_key"`
		}
		if err := json.Unmarshal([]byte(body), &key); err != nil {
			t.Fatalf("the public key body %s: %v", body, err)
		}
		point, err := base64.RawURLEncoding.DecodeString(key.PublicKey)
		if err != nil || len(point) != 65 {
			t.Fatalf("the public key %q is not a 65-byte P-256 point", key.PublicKey)
		}
		pushKeys = newE2EPushKeys(t)
		code, body = jar.post(t, "/api/push/subscribe",
			fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q}}`,
				push.srv.URL, pushKeys.p256dh, pushKeys.auth))
		if code != 200 {
			t.Fatalf("the subscribe = %d: %s", code, body)
		}
	})

	step("permission_allow", func(t *testing.T) {
		env := hookEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, hubURL, "hub-root-token", nil)
		permit := ""
		for _, command := range hookCommands(t, pluginCopy, "PermissionRequest") {
			if strings.Contains(command, " hook permit") {
				permit = command
			}
		}
		if permit == "" {
			t.Fatal("hooks.json runs no permit hook")
		}
		stdin := `{"hook_event_name":"PermissionRequest","session_id":"s-one","tool_name":"Bash","tool_input":{"command":"ls"}}`
		asked := startBackground(t, 120*time.Second, env, hubProject, permit, stdin)

		itemID := 0
		until(t, 20*time.Second, func() bool {
			code, body := jar.get(t, "/api/needs")
			if code != 200 {
				return false
			}
			var list struct {
				Items []struct {
					ID      int64  `json:"id"`
					Kind    string `json:"kind"`
					State   string `json:"state"`
					Summary string `json:"summary"`
				} `json:"items"`
			}
			if json.Unmarshal([]byte(body), &list) != nil {
				return false
			}
			for _, item := range list.Items {
				if item.Kind == "permission" && item.State == "open" && strings.Contains(item.Summary, "ls") {
					itemID = int(item.ID)
					return true
				}
			}
			return false
		}, "the permission item to reach the hub's needs-you list")
		if itemID == 0 {
			t.Fatal("no permission item appeared")
		}

		// the item's create wakes the push sweep: the paired phone hears of it as an encrypted
		// message — a real aes128gcm record, no payload in the clear
		until(t, 15*time.Second, func() bool { return push.count() > 0 },
			"the needs-you item to reach the push service")
		first, ok := push.post(0)
		if !ok {
			t.Fatal("the push service kept no message")
		}
		if first.contentEncoding != "aes128gcm" {
			t.Fatalf("the push's content encoding = %q, want aes128gcm", first.contentEncoding)
		}
		// every check below is one chance cannot answer: 86 bytes is the record's header alone
		// (16 salt, 4 record size, 1 key-id length, the 65-byte key), a plaintext payload would
		// parse as JSON, and one in the clear would name its session
		if len(first.body) < 86 {
			t.Fatalf("the push's body is %d bytes, shorter than an aes128gcm header", len(first.body))
		}
		if json.Valid(first.body) {
			t.Fatalf("the push's %d-byte body is JSON, so the payload arrived unencrypted", len(first.body))
		}
		if strings.Contains(string(first.body), `"s-one"`) {
			t.Fatalf("the push's %d-byte body names the session in the clear", len(first.body))
		}

		// a fresh pairing is stepped up for its first five minutes; the window is moved back so
		// the allow really owes its assertion and the ceremony below really runs
		stampStepUpStale(t, hubRoot, jar.cookie(t, auth.PairPlainCookie))

		code, body := jar.post(t, fmt.Sprintf("/api/needs/%d/decide", itemID), `{"decision":"allow"}`)
		if code != 401 || !strings.Contains(body, "step-up required") {
			t.Fatalf("the stale allow = %d %s, want the step-up 401", code, body)
		}
		stepUp(t, jar, phone, hubPort, handle)
		if code, body = jar.post(t, fmt.Sprintf("/api/needs/%d/decide", itemID), `{"decision":"allow"}`); code != 200 {
			t.Fatalf("the allow after the step-up = %d: %s", code, body)
		}

		out := asked()
		if !strings.Contains(out, `"behavior":"allow"`) {
			t.Fatalf("the permit hook printed %q, want an allow", out)
		}
	})

	step("satellite", func(t *testing.T) {
		hubEnv := machineEnv(hubRoot, hubUser, binHome, pluginCopy, hubProject, release.URL,
			map[string]string{"HUDDLE_URL": hubURL})
		code, stdout, stderr := runSh(t, 60*time.Second, hubEnv, hubProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" pair --satellite --name e2e-sat`, "")
		if code != 0 {
			t.Fatalf("huddle pair --satellite = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		match := regexpSatelliteJoin.FindStringSubmatch(stdout)
		if match == nil {
			t.Fatalf("no join-hub line in the output: %q", stdout)
		}
		satEnv := machineEnv(satRoot, satUser, satBin, pluginCopy, satProject, release.URL, nil)
		code, stdout, stderr = runSh(t, 90*time.Second, satEnv, satProject,
			`sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" join-hub `+match[1]+" "+match[2]+" --name e2e-sat", "")
		if code != 0 {
			t.Fatalf("huddle join-hub = %d, stdout %q, stderr %q", code, stdout, stderr)
		}

		satPort = pinFreePort(t, satRoot)
		satLog := startServe(parent, t, filepath.Join(satBin, huddleVersion(t), "huddle"), serveEnv{
			home: satRoot, user: satUser, port: satPort, token: "sat-root-token",
			data: filepath.Join(satUser, "data"),
		})
		satURL := fmt.Sprintf("http://127.0.0.1:%d", satPort)
		until(t, 30*time.Second, func() bool { return healthy(satURL) }, "the satellite to answer /health")

		satHookEnv := hookEnv(satRoot, satUser, satBin, pluginCopy, satProject, satURL, "sat-root-token", nil)
		for _, command := range hookCommands(t, pluginCopy, "SessionStart") {
			code, stdout, stderr := runSh(t, 30*time.Second, satHookEnv, satProject, command,
				`{"hook_event_name":"SessionStart","session_id":"s-two","source":"startup","cwd":`+strconv.Quote(satProject)+`}`)
			if code != 0 {
				t.Fatalf("%s = %d, stdout %q, stderr %q", command, code, stdout, stderr)
			}
		}
		listed := func() bool { return registryHas(t, hubURL, "hub-root-token", "s-two", "e2e-sat") }
		// a report that outruns the hook's 1.9 s budget on a loaded machine waits in the outbox,
		// and the session's next event replays it: each next prompt is that event, until one lands
		delivered := pollFor(10*time.Second, listed)
		for deadline := time.Now().Add(40 * time.Second); !delivered && time.Now().Before(deadline); {
			for _, command := range hookCommands(t, pluginCopy, "UserPromptSubmit") {
				_, _, _ = runSh(t, 30*time.Second, satHookEnv, satProject, command,
					`{"hook_event_name":"UserPromptSubmit","session_id":"s-two","prompt":"go on","cwd":`+strconv.Quote(satProject)+`}`)
			}
			delivered = pollFor(5*time.Second, listed)
		}
		if !delivered {
			dumpChildDiagnostics(t, satRoot, satBin, satUser, satProject)
			t.Logf("=== the satellite's serve log ===\n%s", satLog.String())
			t.Logf("=== the satellite's registry now === %s", registryNow(t, satURL, "sat-root-token"))
			t.Logf("=== the hub's registry now === %s", registryNow(t, hubURL, "hub-root-token"))
			t.Fatal("the hub's registry never listed the satellite's session under e2e-sat")
		}
	})

	step("link", func(t *testing.T) {
		link := func() (int, string) {
			return jar.post(t, "/api/links",
				`{"sessions":["s-one","s-two"],"channel":"e2e","create":true,"level":"digest"}`)
		}
		code, body := link()
		if code == 401 && strings.Contains(body, "step-up required") {
			// the browser's assertion went stale mid-leg: the ceremony earns it again, once
			stepUp(t, jar, phone, hubPort, handle)
			code, body = link()
		}
		if code != 200 {
			t.Fatalf("the link = %d: %s", code, body)
		}
		code, body = jar.get(t, "/api/links/e2e")
		if code != 200 {
			t.Fatalf("the link list = %d: %s", code, body)
		}
		for _, session := range []string{"s-one", "s-two"} {
			if !strings.Contains(body, `"`+session+`"`) {
				t.Fatalf("the link list = %s, want %s tied", body, session)
			}
		}
	})

	step("radar_tab", func(t *testing.T) {
		code, body := jar.get(t, "/api/radar/api/summary")
		if code != 200 || !strings.Contains(body, `"summary":`) {
			t.Fatalf("the radar tab = %d %s, want the overview's JSON", code, body)
		}
	})
}

// regexpPairLink is the pairing link `huddle pair` prints.
var regexpPairLink = regexp.MustCompile(`http://localhost:\d+/pair#(\S+)`)

// regexpSatelliteJoin is the line `huddle pair --satellite` prints for the other machine.
var regexpSatelliteJoin = regexp.MustCompile(`huddle join-hub (\S+) (\S+)`)

// regexpServeAddr is the address line a starting server prints once it listens.
var regexpServeAddr = regexp.MustCompile(`on http://[\d.]+:(\d+)`)

// machineEnv is one machine's whole child environment, built from scratch so nothing of the
// surrounding shell — no PATH, no real home, no real plugin root — leaks into a run.
func machineEnv(root, user, binHome, pluginCopy, project, downloadBase string, extra map[string]string) map[string]string {
	env := map[string]string{
		"PATH":                   strippedPath,
		"HOME":                   user,
		"TMPDIR":                 user,
		"USER":                   os.Getenv("USER"),
		"XDG_CONFIG_HOME":        filepath.Join(user, ".config"),
		"XDG_STATE_HOME":         filepath.Join(user, ".local", "state"),
		"XDG_DATA_HOME":          filepath.Join(user, ".local", "share"),
		"XDG_CACHE_HOME":         filepath.Join(user, ".cache"),
		"CLAUDE_CONFIG_DIR":      filepath.Join(user, ".claude"),
		"RADAR_HOME":             filepath.Join(user, "radar"),
		"HUDDLE_HOME":            root,
		"HUDDLE_KEYS":            "files",
		"HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_NOTIFY_LOG":      filepath.Join(user, "notify.log"),
		"HUDDLE_DOWNLOAD_BASE":   downloadBase,
		"AGENTS_HUDDLE_HOME":     binHome,
		"CLAUDE_PLUGIN_ROOT":     pluginCopy,
		"CLAUDE_PROJECT_DIR":     project,
	}
	for key, value := range extra {
		env[key] = value
	}
	return env
}

// hookEnv is the environment one hook line runs in: the machine's, pointed at its own server.
func hookEnv(root, user, binHome, pluginCopy, project, url, token string, extra map[string]string) map[string]string {
	merged := map[string]string{"HUDDLE_URL": url, "HUDDLE_TOKEN": token}
	for key, value := range extra {
		merged[key] = value
	}
	return machineEnv(root, user, binHome, pluginCopy, project, "http://127.0.0.1:1", merged)
}

// envList renders an env map for a child process.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		if value == "" {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
}

// runSh runs one command line through /bin/sh the way Claude Code runs a hook's line, with
// stdin fed to it, and answers its exit code and both outputs.
func runSh(t *testing.T, timeout time.Duration, env map[string]string, dir, command, stdin string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = envList(env)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %q: %v", command, err)
		}
		code = exit.ExitCode()
	}
	if ctx.Err() != nil {
		t.Fatalf("run %q did not finish within %s (stdout %q, stderr %q)", command, timeout, stdout.String(), stderr.String())
	}
	return code, stdout.String(), stderr.String()
}

// startBackground starts one long-running hook line and answers a function that waits for its
// exit and hands back everything it printed.
func startBackground(t *testing.T, timeout time.Duration, env map[string]string, dir, command, stdin string) func() string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = envList(env)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout strings.Builder
	cmd.Stdout = &stdout
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %q: %v", command, err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return func() string {
		err := cmd.Wait()
		if err != nil {
			t.Fatalf("%s exited %v (stdout %q, stderr %q)", command, err, stdout.String(), stderr.String())
		}
		return stdout.String()
	}
}

// serveEnv is one server child this test starts.
type serveEnv struct {
	home  string
	user  string
	port  int
	token string
	data  string
	// pushSweepMS shrinks the push sweep's 30 s floor for a leg that cannot wait a tick; the
	// create of an item wakes the sweep anyway, this is only the backstop
	pushSweepMS int
	// extra layers variables over the child's environment a leg needs beyond the machine's
	// own: a proxy and a certificate file for a server that reaches a hub over TLS
	extra map[string]string
}

// serveLog collects one server child's stdout from any goroutine.
type serveLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *serveLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *serveLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// startServe spawns the binary's serve entry on the port given and waits for its address line.
// The process outlives the step that started it, so the caller's parent owns its stop: a signal
// first, so a coverage run keeps its counters, then the hard end. Its stdout lands in the log
// handed back, so a failed step can say what the server said.
func startServe(parent, t *testing.T, bin string, cfg serveEnv) *serveLog {
	t.Helper()
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("the binary to serve is missing: %v", err)
	}
	env := map[string]string{
		"PATH":              strippedPath,
		"HOME":              cfg.user,
		"TMPDIR":            cfg.user,
		"USER":              os.Getenv("USER"),
		"XDG_CONFIG_HOME":   filepath.Join(cfg.user, ".config"),
		"XDG_STATE_HOME":    filepath.Join(cfg.user, ".local", "state"),
		"XDG_DATA_HOME":     filepath.Join(cfg.user, ".local", "share"),
		"XDG_CACHE_HOME":    filepath.Join(cfg.user, ".cache"),
		"CLAUDE_CONFIG_DIR": filepath.Join(cfg.user, ".claude"),
		"RADAR_HOME":        filepath.Join(cfg.user, "radar"),
		"HUDDLE_HOME":       cfg.home,
		"HUDDLE_KEYS":       "files",
		"HUDDLE_DATA":       cfg.data,
		"HUDDLE_TOKEN":      cfg.token,
		"HUDDLE_NOTIFY_LOG": filepath.Join(cfg.user, "notify.log"),
		"PORT":              strconv.Itoa(cfg.port),
	}
	if cfg.pushSweepMS > 0 {
		env["HUDDLE_PUSH_SWEEP_MS"] = strconv.Itoa(cfg.pushSweepMS)
	}
	for key, value := range cfg.extra {
		env[key] = value
	}
	cmd := exec.Command(bin, "serve")
	cmd.Env = envList(env)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the server: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	log := &serveLog{}
	doneReading := make(chan struct{})
	go func() {
		defer close(doneReading)
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			log.Write(append([]byte(lines.Text()), '\n'))
		}
	}()
	parent.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		<-doneReading
	})
	said := ""
	deadline := time.Now().Add(15 * time.Second)
	for {
		if match := regexpServeAddr.FindStringSubmatch(log.String()); match != nil {
			said = match[1]
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if said != "" && said != strconv.Itoa(cfg.port) {
		t.Fatalf("the server took port %s, want the pinned %d", said, cfg.port)
	}
	return log
}

// hookCommands reads one event's launcher lines out of the copied plugin's hooks.json, in the
// order Claude Code runs them.
func hookCommands(t *testing.T, pluginCopy, event string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pluginCopy, "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	var out []string
	for _, group := range file.Hooks[event] {
		for _, hook := range group.Hooks {
			out = append(out, hook.Command)
		}
	}
	if len(out) == 0 {
		t.Fatalf("hooks.json runs no %s", event)
	}
	return out
}

// huddleVersion is the version the committed launcher downloads.
func huddleVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

// fakeReleaseServer serves the built binary at the release path the launcher fetches. It lives
// as long as the whole test: a satellite's own download still needs it steps later, so it is
// the caller's parent that owns the close.
func fakeReleaseServer(parent, t *testing.T, bin string) *httptest.Server {
	t.Helper()
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read the built binary: %v", err)
	}
	asset := "/v" + huddleVersion(t) + "/huddle-" + releaseTarget()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != asset {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/octet-stream")
		_, _ = w.Write(data)
	}))
	parent.Cleanup(srv.Close)
	return srv
}

// pinLauncherChecksum rewrites the copied launcher's checksum for this platform to the binary
// the fake release server serves: the copy is what a release builds, so the download is
// verified the way the real one is.
func pinLauncherChecksum(t *testing.T, launcher, bin string) {
	t.Helper()
	raw, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatalf("read the copied launcher: %v", err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("read the built binary: %v", err)
	}
	sum := sha256.Sum256(data)
	want := "  " + releaseTarget() + ") want=" + hex.EncodeToString(sum[:]) + " ;;"
	lines := strings.Split(string(raw), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, "  "+releaseTarget()+") want=") {
			lines[i] = want
			found = true
		}
	}
	if !found {
		t.Fatalf("the launcher names no checksum for %s", releaseTarget())
	}
	if err := os.WriteFile(launcher, []byte(strings.Join(lines, "\n")), 0o755); err != nil {
		t.Fatalf("write the copied launcher: %v", err)
	}
}

// e2ePushPost is one message the fake push service took: the content encoding it arrived
// under, and its encrypted body.
type e2ePushPost struct {
	contentEncoding string
	body            []byte
}

// e2ePush is the push service a paired phone subscribes to: one loopback endpoint that records
// every message it is handed and answers 201, so the leg can prove a push left the hub.
type e2ePush struct {
	srv   *httptest.Server
	mu    sync.Mutex
	posts []e2ePushPost
}

// newE2EPush starts the fake push service on a free loopback port. The service outlives the
// step that started it — the push it records lands in a later step — so the test's parent owns
// its close, like the release server's.
func newE2EPush(parent, t *testing.T) *e2ePush {
	t.Helper()
	service := &e2ePush{}
	service.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		service.mu.Lock()
		service.posts = append(service.posts,
			e2ePushPost{contentEncoding: r.Header.Get("content-encoding"), body: body})
		service.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	parent.Cleanup(service.srv.Close)
	return service
}

// count answers how many messages the service took.
func (p *e2ePush) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.posts)
}

// post answers the message at i, and whether there was one.
func (p *e2ePush) post(i int) (e2ePushPost, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.posts) {
		return e2ePushPost{}, false
	}
	return p.posts[i], true
}

// e2ePushKeys is one subscription's public keys as the browser generated them: what its
// subscribe call carries, and all the hub ever learns of them.
type e2ePushKeys struct {
	p256dh string
	auth   string
}

// newE2EPushKeys draws one subscription's keys the way a browser does: a P-256 pair and a
// 16-byte auth secret, both base64url.
func newE2EPushKeys(t *testing.T) e2ePushKeys {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("draw the browser's key: %v", err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatalf("draw the auth secret: %v", err)
	}
	return e2ePushKeys{
		p256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		auth:   base64.RawURLEncoding.EncodeToString(auth),
	}
}

// browserClient is the paired device's browser: one cookie jar over the hub, every dangerous
// act carried by the cookie alone.
type browserClient struct {
	base   string
	client *http.Client
}

func newBrowser(base string) *browserClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return &browserClient{base: base, client: &http.Client{Jar: jar, Timeout: 45 * time.Second}}
}

// cookie answers one cookie's value the jar holds for the hub.
func (b *browserClient) cookie(t *testing.T, name string) string {
	t.Helper()
	parsed, err := url.Parse(b.base)
	if err != nil {
		t.Fatalf("parse %s: %v", b.base, err)
	}
	for _, cookie := range b.client.Jar.Cookies(parsed) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func (b *browserClient) get(t *testing.T, path string) (int, string) {
	t.Helper()
	return b.do(t, http.MethodGet, path, "")
}

func (b *browserClient) post(t *testing.T, path, body string) (int, string) {
	t.Helper()
	return b.do(t, http.MethodPost, path, body)
}

func (b *browserClient) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, b.base+path, reader)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if body != "" {
		req.Header.Set("content-type", "application/json")
		req.Header.Set(server.ActionHeader, strings.SplitN(path, "?", 2)[0])
	}
	resp, err := b.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// parseBegin reads one begin answer: the options the soft key answers and the fields beside
// them the later ceremonies need.
func parseBegin(t *testing.T, body string) (json.RawMessage, struct {
	Challenge  string `json:"challenge"`
	UserHandle string `json:"userHandle"`
}) {
	t.Helper()
	var envelope struct {
		Options json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("begin body %s: %v", body, err)
	}
	var options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			User      struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(envelope.Options, &options); err != nil {
		t.Fatalf("begin options %s: %v", envelope.Options, err)
	}
	parsed := struct {
		Challenge  string `json:"challenge"`
		UserHandle string `json:"userHandle"`
	}{options.PublicKey.Challenge, options.PublicKey.User.ID}
	if parsed.Challenge == "" || parsed.UserHandle == "" {
		t.Fatalf("begin options are short: %s", body)
	}
	return envelope.Options, parsed
}

// stepUp runs one step-up ceremony for the browser its cookie names, the way the app earns its
// five minutes.
func stepUp(t *testing.T, jar *browserClient, phone *webauthntest.SoftKey, port int, handle string) {
	t.Helper()
	code, body := jar.post(t, "/api/stepup/begin", `{}`)
	if code != 200 {
		t.Fatalf("stepup begin = %d: %s", code, body)
	}
	var begin struct {
		Options struct {
			PublicKey struct {
				Challenge string `json:"challenge"`
				RPID      string `json:"rpId"`
			} `json:"publicKey"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(body), &begin); err != nil {
		t.Fatalf("stepup begin body %s: %v", body, err)
	}
	if begin.Options.PublicKey.Challenge == "" || begin.Options.PublicKey.RPID == "" {
		t.Fatalf("stepup begin options are short: %s", body)
	}
	answer := phone.Get(t, begin.Options.PublicKey.Challenge, begin.Options.PublicKey.RPID,
		fmt.Sprintf("http://localhost:%d", port), handle)
	if code, body = jar.post(t, "/api/stepup/finish", fmt.Sprintf(`{"response":%s}`, answer)); code != 200 {
		t.Fatalf("stepup finish = %d: %s", code, body)
	}
}

// stampStepUpStale moves one device session's assertion moment back past the window, so a
// dangerous act really owes its passkey.
func stampStepUpStale(t *testing.T, root, cookie string) {
	t.Helper()
	db, err := store.SQLite(context.Background(), home.Home{Root: root}.HubDB())
	if err != nil {
		t.Fatalf("open the hub's hub.db: %v", err)
	}
	defer db.Close()
	stale := time.Now().UnixMilli() - 6*60*1000
	if err := db.Run(context.Background(),
		"UPDATE device_sessions SET stepup_at = ? WHERE digest = ?", stale, auth.PairDigest(cookie)); err != nil {
		t.Fatalf("move the assertion moment back: %v", err)
	}
}

// registryNow answers the registry listing at url as one string, for diagnostics.
func registryNow(t *testing.T, url, token string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/api/registry/sessions", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("x-huddle-token", token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return fmt.Sprintf("unreachable: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("HTTP %d %s", resp.StatusCode, raw)
}

// registryHas answers whether the registry at url lists one session, under one machine when
// one is named.
func registryHas(t *testing.T, url, token, session, machine string) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/api/registry/sessions", nil)
	if err != nil {
		t.Fatalf("build the registry read: %v", err)
	}
	req.Header.Set("x-huddle-token", token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return false
	}
	var list struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Machine   string `json:"machine"`
		} `json:"sessions"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return false
	}
	for _, row := range list.Sessions {
		if row.SessionID == session && (machine == "" || row.Machine == machine) {
			return true
		}
	}
	return false
}

// dumpChildDiagnostics prints what a hook child left behind, for a step that timed out.
func dumpChildDiagnostics(t *testing.T, root, binHome string, more ...string) {
	t.Helper()
	// a hook logs to the home it resolves, else its temp dir: every outbox and hook log under
	// the machine's dirs is printed, wherever the child put it
	for _, dir := range append([]string{root}, more...) {
		_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			if name := entry.Name(); name == "outbox.jsonl" || name == "hooks.log" {
				if raw, err := os.ReadFile(path); err == nil {
					t.Logf("=== %s ===\n%s", path, raw)
				}
			}
			return nil
		})
	}
	if entries, err := os.ReadDir(binHome); err == nil {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Logf("=== %s === %v", binHome, names)
	}
}

// healthy answers whether a Huddle answers at url.
func healthy(url string) bool {
	resp, err := http.Get(url + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// until polls f until it answers true or the deadline passes.
func until(t *testing.T, within time.Duration, f func() bool, what string) {
	t.Helper()
	if !pollFor(within, f) {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// pollFor answers whether f answered true within the deadline.
func pollFor(within time.Duration, f func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if f() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}
