package main

// The 0.0.1 migration end to end on the built binary: a machine laid out like a 0.0.1 install
// (zai's state root, a ledger naming the old router, a settings.json whose proxy names 18787) is
// moved onto huddle-router by one `huddle router start` plus the session-start hook Claude Code
// already runs — providers.json records zai, the settings move to the daemon's port, the ledger
// names the daemon, and the old router's files go. The daemon is this test's child and dies in
// the cleanup; every home is a temp one and nothing touches the real ~/.claude, ~/.agents or any
// of the routers' reserved ports.

import (
	"encoding/json"
	"fmt"
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
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// readFile is a small file's text, failing the test when it cannot be read.
func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// readProxySettings is settings.json's env object as a map, ok false when the file or its env
// object cannot be read.
func readProxySettings(t *testing.T, path string) (map[string]string, bool) {
	t.Helper()
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return parsed.Env, parsed.Env != nil
}

// migrateSeen counts the requests one fake upstream saw.
type migrateSeen struct {
	server *httptest.Server
	mu     sync.Mutex
	n      int
}

func newMigrateFake(t *testing.T, name string) *migrateSeen {
	t.Helper()
	fake := &migrateSeen{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.n++
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s-1\n\n", name)
		_, _ = fmt.Fprintf(w, "data: %s-2\n\n", name)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *migrateSeen) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// migrateGonePid is a pid no live process can hold.
const migrateGonePid = 999999999

func TestMigrate001E2E(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "user")
	routerHome := filepath.Join(root, "router-home")
	claudeConfig := filepath.Join(home, "claude-config")
	state := filepath.Join(root, "zai-state")
	fake := fakeclaude.Build(t)
	anthropic := newMigrateFake(t, "anthropic")
	zaiUp := newMigrateFake(t, "zai")
	routerPort := ports.Free(t)
	pluginDir := fakePluginDir(t)
	shim := fakeBrowserShim(t)

	// Claude Code's own bookkeeping: zai installed and enabled at the fake plugin dir.
	installed := map[string]any{
		"plugins": map[string]any{
			"zai-plugin-cc@muhmdraouf": []map[string]any{
				{"installPath": pluginDir, "scope": "user"},
			},
		},
	}
	raw, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(claudeConfig, "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeConfig, "plugins", "installed_plugins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	env := argvLayer(map[string]string{
		"PATH":                        shim + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME":                        home,
		"TMPDIR":                      root,
		"XDG_CONFIG_HOME":             filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME":               filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":              filepath.Join(root, "xdg-cache"),
		"XDG_STATE_HOME":              filepath.Join(root, "xdg-state"),
		"HUDDLE_KEYS":                 "files",
		"RADAR_HOME":                  filepath.Join(root, "radar"),
		"HUDDLE_HOME":                 filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED":      "1",
		"HUDDLE_CHANNEL":              "",
		"HUDDLE_AS":                   "",
		"HUDDLE_URL":                  "",
		"HUDDLE_AUTOSTART":            "0",
		"RADAR_AUTOSTART":             "0",
		"CLAUDE_CONFIG_DIR":           claudeConfig,
		"CLAUDE_PLUGIN_ROOT":          pluginDir,
		"CLAUDE_PLUGIN_DATA":          filepath.Join(root, "zai-plugin-data"),
		"HUDDLE_ROUTER_HOME":          routerHome,
		"HUDDLE_ROUTER_PORT":          strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL": anthropic.server.URL,
		"ZAI_ROUTER_URL":              zaiUp.server.URL,
		"ZAI_API_KEY":                 "migrate-e2e-key",
		"ZAI_STATE_DIR":               state,
		"ZAI_CLAUDE_BIN":              fake,
	}, nil)

	// The 0.0.1 zai state, laid out the way a real 0.0.1 machine's ~/.agents/zai looks: the
	// setup mark, the dead router's pid record, the binary copy, the ledger naming 18787, two
	// watch guards, one job file.
	write001 := func(rel, body string) {
		t.Helper()
		path := filepath.Join(state, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write001("setup-done", "")
	write001("router.pid", fmt.Sprintf(`{"pid":%d,"port":18787,"startedAt":"2026-01-01T00:00:00Z",`+
		`"node":%q,"version":"abc123def456","frontVersion":3,"mode":"router","token":"old-token"}`,
		migrateGonePid, filepath.Join(state, "router", "plugin-core")))
	write001("router/plugin-core", "the 0.0.1 binary copy")
	write001("ledger.json", fmt.Sprintf(`{
  "version": 1,
  "plugin": "zai",
  "routerUrl": "http://127.0.0.1:18787",
  "settingsPath": "%s",
  "settings": {
    "proxyBefore": null,
    "optionsAdded": ["glm-5.3"],
    "availableAppended": [],
    "effortsAdded": {}
  },
  "keystore": {"created": false},
  "routerFiles": ["%s/router"],
  "marker": "%s",
  "agents": {"glm": "sonnet"},
  "agentsDir": "%s",
  "stateRoot": "%s"
}`,
		filepath.Join(claudeConfig, "settings.json"), state,
		filepath.Join(root, "zai-plugin-data", "installed"), pluginDir, state))
	write001("watch."+strconv.Itoa(migrateGonePid-1), strconv.Itoa(migrateGonePid-1)+"\n")
	write001("watch."+strconv.Itoa(migrateGonePid-2), strconv.Itoa(migrateGonePid-2)+"\n")
	write001("jobs/j-1.json", `{"id":"j-1","brief":"keep me"}`)
	if err := os.MkdirAll(filepath.Join(root, "zai-plugin-data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "zai-plugin-data", "installed"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The settings a 0.0.1 setup left: the proxy naming zai's old router.
	settingsPath := filepath.Join(claudeConfig, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{
  "env": {
    "HTTPS_PROXY": "http://127.0.0.1:18787",
    "NO_PROXY": "localhost,127.0.0.1"
  },
  "unrelated": "keep"
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	huddleURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)
	jobBefore, err := os.ReadFile(filepath.Join(state, "jobs", "j-1.json"))
	if err != nil {
		t.Fatal(err)
	}

	// The first start: no provider is recorded yet, so nothing spawns and the word says so.
	code, out, errOut := argvRun(t, env, root, "router", "start")
	if code != 0 {
		t.Fatalf("huddle router start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out+errOut, "no provider is set up") {
		t.Errorf("router start said %q, want the no-provider line", out+errOut)
	}

	// The session start hook Claude Code runs: it records zai, starts the daemon, moves the
	// settings — and the daemon's migration retires the old setup's leftovers.
	code, out, errOut = hooksRun(t, env, root, `{"source":"startup","session_id":"migrate-e2e"}`,
		"hook", "session-start")
	if code != 0 {
		t.Fatalf("huddle hook session-start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// From here on a daemon runs: this test's grandchild, ended in the cleanup below.
	record := filepath.Join(routerHome, "run", "router.json")
	var daemon struct {
		PID int `json:"pid"`
	}
	if !pollFor(5*time.Second, func() bool {
		raw, err := os.ReadFile(record)
		if err != nil {
			return false
		}
		return json.Unmarshal(raw, &daemon) == nil && daemon.PID > 0
	}) {
		t.Fatalf("no daemon record at %s (hook stdout: %s, stderr: %s)", record, out, errOut)
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
		_ = syscall.Kill(daemon.PID, syscall.SIGTERM)
		if !pollFor(5*time.Second, func() bool { return !healthy() }) {
			_ = syscall.Kill(daemon.PID, syscall.SIGKILL)
			pollFor(2*time.Second, func() bool { return !healthy() })
		}
	})
	if !pollFor(5*time.Second, healthy) {
		t.Fatalf("the daemon never came up; its log:\n%s", readFile(t, filepath.Join(routerHome, "router.log")))
	}

	// The migrated state, everything the card promises, within the daemon's first seconds.
	migrated := func() bool {
		raw, err := os.ReadFile(filepath.Join(routerHome, "providers.json"))
		if err != nil || !strings.Contains(string(raw), `"zai"`) {
			return false
		}
		parsed, ok := readProxySettings(t, settingsPath)
		if !ok || parsed["HTTPS_PROXY"] != huddleURL {
			return false
		}
		ledger, err := os.ReadFile(filepath.Join(state, "ledger.json"))
		if err != nil || !strings.Contains(string(ledger), huddleURL) {
			return false
		}
		if _, err := os.Stat(filepath.Join(state, "router", "plugin-core")); !os.IsNotExist(err) {
			return false
		}
		for _, name := range []string{
			"watch." + strconv.Itoa(migrateGonePid-1),
			"watch." + strconv.Itoa(migrateGonePid-2),
		} {
			if _, err := os.Stat(filepath.Join(state, name)); !os.IsNotExist(err) {
				return false
			}
		}
		return true
	}
	if !pollFor(10*time.Second, migrated) {
		t.Fatalf("the migration never finished; providers.json:\n%s\nledger:\n%s\nsettings:\n%s\nrouter log:\n%s",
			readFile(t, filepath.Join(routerHome, "providers.json")),
			readFile(t, filepath.Join(state, "ledger.json")),
			readFile(t, settingsPath),
			readFile(t, filepath.Join(routerHome, "router.log")))
	}

	// The daemon answers a glm request with zai's lane, and the job file is untouched.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(huddleURL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"glm-5.3","messages":[]}`))
	if err != nil {
		t.Fatalf("post glm-5.3: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("glm-5.3 = %d", resp.StatusCode)
	}
	if seen := zaiUp.requests(); seen != 1 {
		t.Errorf("the zai upstream saw %d requests, want 1", seen)
	}
	jobAfter, err := os.ReadFile(filepath.Join(state, "jobs", "j-1.json"))
	if err != nil || string(jobAfter) != string(jobBefore) {
		t.Errorf("jobs/j-1.json changed: %q (%v)", jobAfter, err)
	}
}
