package main

// The settings end to end on the built binary: every provider's setup points Claude Code at the
// one huddle-router, records itself in providers.json and keeps the daemon running; removing the
// providers one by one keeps the proxy while another is enabled and restores settings.json byte
// for byte after the last one. Every process here is this test's child (the daemon included),
// every home a temp one, and the fake open and xdg-open on PATH keep any key page shut.

import (
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
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakeclaude"
)

// settingsFake is one recording fake upstream the e2e points a lane or Anthropic at.
type settingsFake struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   int
}

func newSettingsFake(t *testing.T, name string) *settingsFake {
	t.Helper()
	fake := &settingsFake{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.seen++
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+name+"-1\n\n")
		_, _ = io.WriteString(w, "data: "+name+"-2\n\n")
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *settingsFake) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

// fakePluginDir lays out the plugin tree setup rewrites: the dist bundles it names and the agent
// files whose model: lines it sets back.
func fakePluginDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(root, "agents")
	if err := os.MkdirAll(agents, 0o700); err != nil {
		t.Fatal(err)
	}
	names := []string{"glm-5.3", "glm-5.3-flash", "kimi-k3", "kimi-k2.6", "omp", "opencode", "pi", "zcode"}
	for _, name := range names {
		body := "---\nname: " + name + "\nmodel: sonnet\n---\nBody.\n"
		if err := os.WriteFile(filepath.Join(agents, name+".md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"zai", "kimi"} {
		if err := os.WriteFile(filepath.Join(root, "dist", name+".js"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fakeBrowserShim puts open and xdg-open scripts first on PATH: any key page they open is
// recorded, never shown.
func fakeBrowserShim(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"open", "xdg-open"} {
		calls := filepath.Join(dir, name+"-was-called")
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + calls + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRouterSettingsE2E(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "user")
	routerHome := filepath.Join(root, "router-home")
	claudeConfig := filepath.Join(home, "claude-config")
	fake := fakeclaude.Build(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	kimiUp := newSettingsFake(t, "kimi")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	routerPort := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	shim := fakeBrowserShim(t)
	env := argvLayer(map[string]string{
		"PATH":                        shim + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME":                        home,
		"TMPDIR":                      root,
		"XDG_CONFIG_HOME":             filepath.Join(root, "xdg-config"),
		"XDG_STATE_HOME":              filepath.Join(root, "xdg-state"),
		"RADAR_HOME":                  filepath.Join(root, "radar"),
		"HUDDLE_HOME":                 filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED":      "1",
		"CLAUDE_CONFIG_DIR":           claudeConfig,
		"HUDDLE_ROUTER_HOME":          routerHome,
		"HUDDLE_ROUTER_PORT":          strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL": anthropic.server.URL,
		"ZAI_ROUTER_URL":              zaiUp.server.URL,
		"KIMI_ROUTER_URL":             kimiUp.server.URL,
		"ZAI_API_KEY":                 "zai-settings-e2e-key",
		"KIMI_API_KEY":                "kimi-settings-e2e-key",
		"ZAI_STATE_DIR":               filepath.Join(root, "zai-state"),
		"KIMI_STATE_DIR":              filepath.Join(root, "kimi-state"),
		"ZAI_CLAUDE_BIN":              fake,
		"KIMI_CLAUDE_BIN":             fake,
		"CLAUDE_PLUGIN_ROOT":          fakePluginDir(t),
	}, nil)
	settingsPath := filepath.Join(claudeConfig, "settings.json")
	original := "{\n  \"unrelated\": \"keep\"\n}\n"
	writeSettings := func(t *testing.T) {
		t.Helper()
		if err := os.MkdirAll(claudeConfig, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(settingsPath, []byte(original), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	readSettings := func(t *testing.T) string {
		t.Helper()
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("read settings.json: %v", err)
		}
		return string(data)
	}
	envValue := func(t *testing.T, key string) string {
		t.Helper()
		var parsed struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal([]byte(readSettings(t)), &parsed); err != nil {
			t.Fatalf("parse settings.json: %v", err)
		}
		return parsed.Env[key]
	}
	enabled := func(t *testing.T) []string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(routerHome, "providers.json"))
		if err != nil {
			t.Fatalf("read providers.json: %v", err)
		}
		var parsed struct {
			Providers []string `json:"providers"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("parse providers.json: %v", err)
		}
		return parsed.Providers
	}
	runCLI := func(t *testing.T, provider string, args ...string) string {
		t.Helper()
		code, out, errOut := argvRun(t, env, root, append([]string{"provider", provider, "cli"}, args...)...)
		if code != 0 {
			t.Fatalf("huddle provider %s cli %s = %d\nstdout:\n%s\nstderr:\n%s", provider, strings.Join(args, " "), code, out, errOut)
		}
		return out
	}
	huddleURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)

	writeSettings(t)
	runCLI(t, "zai", "setup")

	// From here on a daemon runs: this test's child, ended in the cleanup below.
	daemonPID := 0
	if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
		var record struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(raw, &record) == nil {
			daemonPID = record.PID
		}
	}
	if daemonPID <= 0 {
		t.Fatal("no daemon record after zai setup")
	}
	healthy := func() bool {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(huddleURL + "/huddle-router/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	}
	t.Cleanup(func() {
		_ = syscall.Kill(daemonPID, syscall.SIGTERM)
		if !pollFor(5*time.Second, func() bool { return !healthy() }) {
			_ = syscall.Kill(daemonPID, syscall.SIGKILL)
			pollFor(2*time.Second, func() bool { return !healthy() })
		}
	})
	if !pollFor(5*time.Second, healthy) {
		t.Fatal("the daemon never came up")
	}

	t.Run("zai setup points the settings at huddle-router and records itself", func(t *testing.T) {
		if got := envValue(t, "HTTPS_PROXY"); got != huddleURL {
			t.Errorf("HTTPS_PROXY = %q, want %q", got, huddleURL)
		}
		if got := enabled(t); len(got) != 1 || got[0] != "zai" {
			t.Errorf("providers.json = %v, want [zai]", got)
		}
	})

	t.Run("kimi setup keeps the proxy and joins providers.json", func(t *testing.T) {
		proxyBefore := envValue(t, "HTTPS_PROXY")
		runCLI(t, "kimi", "setup")
		if got := envValue(t, "HTTPS_PROXY"); got != proxyBefore {
			t.Errorf("HTTPS_PROXY = %q, want the kept %q", got, proxyBefore)
		}
		if got := enabled(t); len(got) != 2 || got[0] != "zai" || got[1] != "kimi" {
			t.Errorf("providers.json = %v, want [zai kimi]", got)
		}
	})

	t.Run("a glm-5.3 request through the proxy port reaches the fake zai upstream", func(t *testing.T) {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(huddleURL+"/v1/messages", "application/json",
			strings.NewReader(`{"model":"glm-5.3","messages":[]}`))
		if err != nil {
			t.Fatalf("post glm-5.3: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("glm-5.3 = %d %q", resp.StatusCode, body)
		}
		if seen := zaiUp.requests(); seen != 1 {
			t.Errorf("the zai upstream saw %d requests, want 1", seen)
		}
	})

	t.Run("removing zai keeps the proxy while kimi is enabled", func(t *testing.T) {
		runCLI(t, "zai", "setup", "--remove")
		if got := envValue(t, "HTTPS_PROXY"); got != huddleURL {
			t.Errorf("HTTPS_PROXY = %q, want the shared %q", got, huddleURL)
		}
		if got := enabled(t); len(got) != 1 || got[0] != "kimi" {
			t.Errorf("providers.json = %v, want [kimi]", got)
		}
	})

	t.Run("removing kimi restores settings.json byte for byte", func(t *testing.T) {
		runCLI(t, "kimi", "setup", "--remove")
		if got := readSettings(t); got != original {
			t.Errorf("settings.json = %q, want the original %q", got, original)
		}
		if got := enabled(t); len(got) != 0 {
			t.Errorf("providers.json = %v, want none", got)
		}
	})
}
