package main

// The providers' hook work end to end on the built binary: after zai's setup, a `huddle hook
// session-start` run — the one merged process Claude Code starts for the event — re-applies the
// /model entries a plugin update took out, in exactly one JSON object on stdout. The daemon is
// this test's child and dies in the cleanup; every home is a temp one, the fake open/xdg-open on
// PATH keep any key page shut, and nothing touches the real ~/.claude, ~/.agents or ports
// 18786-18879.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakeclaude"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// hooksFake is one recording fake upstream the e2e points a lane or Anthropic at.
type hooksFake struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   int
}

func newHooksFake(t *testing.T, name string) *hooksFake {
	t.Helper()
	fake := &hooksFake{}
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

// hooksRun feeds the binary one hook invocation with stdin and answers its exit code and outputs.
func hooksRun(t *testing.T, env map[string]string, dir string, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(huddleBin, args...)
	cmd.Env = argvEntries(env)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run huddle %v: %v", args, err)
		}
		return exit.ExitCode(), out.String(), errOut.String()
	}
	return 0, out.String(), errOut.String()
}

// hooksModelIDs is settings.json's /model picker's model ids.
func hooksModelIDs(t *testing.T, settingsPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var parsed struct {
		ModelPicker struct {
			Options []struct {
				Model string `json:"model"`
			} `json:"options"`
		} `json:"modelPicker"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	ids := make([]string, 0, len(parsed.ModelPicker.Options))
	for _, option := range parsed.ModelPicker.Options {
		ids = append(ids, option.Model)
	}
	return ids
}

// hooksDropModels rewrites settings.json without the named model ids.
func hooksDropModels(t *testing.T, settingsPath string, drop map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	picker, _ := parsed["modelPicker"].(map[string]any)
	if picker == nil {
		t.Fatal("settings.json carries no modelPicker to strip")
	}
	options, _ := picker["options"].([]any)
	kept := make([]any, 0, len(options))
	for _, option := range options {
		entry, _ := option.(map[string]any)
		model, _ := entry["model"].(string)
		if !drop[model] {
			kept = append(kept, option)
		}
	}
	picker["options"] = kept
	raw, err = json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// hooksSeedAwaitingReview lays one awaiting-review job into the provider's job store, the file
// the store reads, so the board hook has a job to name.
func hooksSeedAwaitingReview(t *testing.T, stateRoot string) {
	t.Helper()
	record := map[string]any{
		"id":    "j1",
		"brief": map[string]any{"title": "Fix the flake", "model": "main"},
		"state": "awaiting_review",
		"workspace": map[string]any{
			"repoRoot": "/repo",
			"baseSha":  "0000000000000000000000000000000000000000",
		},
		"attempts":  []any{},
		"createdAt": "2026-10-20T00:00:00.000Z",
		"updatedAt": "2026-10-20T00:00:00.000Z",
		"version":   1,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stateRoot, "jobs", "j1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHooksProvidersE2E(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "user")
	routerHome := filepath.Join(root, "router-home")
	claudeConfig := filepath.Join(home, "claude-config")
	fake := fakeclaude.Build(t)
	anthropic := newHooksFake(t, "anthropic")
	zaiUp := newHooksFake(t, "zai")
	routerPort := ports.Free(t)
	pluginDir := fakePluginDir(t)
	shim := fakeBrowserShim(t)

	// Claude Code's own bookkeeping: zai installed at the fake plugin dir, the path its agents
	// and dist bundle sit under.
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
		"XDG_STATE_HOME":              filepath.Join(root, "xdg-state"),
		"XDG_DATA_HOME":               filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":              filepath.Join(root, "xdg-cache"),
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
		"ZAI_API_KEY":                 "hooks-providers-e2e-key",
		"ZAI_STATE_DIR":               filepath.Join(root, "zai-state"),
		"ZAI_CLAUDE_BIN":              fake,
	}, nil)
	settingsPath := filepath.Join(claudeConfig, "settings.json")
	if err := os.WriteFile(settingsPath, []byte("{\n  \"unrelated\": \"keep\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := argvRun(t, env, root, "provider", "zai", "cli", "setup")
	if code != 0 {
		t.Fatalf("huddle provider zai cli setup = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// From here on a daemon runs: this test's child, ended in the cleanup below.
	raw, err = os.ReadFile(filepath.Join(routerHome, "run", "router.json"))
	if err != nil {
		t.Fatalf("read the daemon record: %v", err)
	}
	var record struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatalf("parse the daemon record: %v", err)
	}
	if record.PID <= 0 {
		t.Fatal("no daemon pid after zai setup")
	}
	huddleURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)
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
		_ = syscall.Kill(record.PID, syscall.SIGTERM)
		if !pollFor(5*time.Second, func() bool { return !healthy() }) {
			_ = syscall.Kill(record.PID, syscall.SIGKILL)
			pollFor(2*time.Second, func() bool { return !healthy() })
		}
	})
	if !pollFor(5*time.Second, healthy) {
		t.Fatal("the daemon never came up")
	}

	// Sanity: setup put zai's models into /model before the update takes them out.
	provider, ok := providers.ByName("zai")
	if !ok {
		t.Fatal("providers has no zai")
	}

	t.Run("a session start re-applies the /model entries a plugin update removed", func(t *testing.T) {
		before := hooksModelIDs(t, settingsPath)
		if len(before) == 0 {
			t.Fatal("zai's setup put no /model entries in")
		}
		// One job awaits review, so the providers' part has its line to say and the merged
		// process answers with it inside the one JSON object.
		hooksSeedAwaitingReview(t, filepath.Join(root, "zai-state"))
		hooksDropModels(t, settingsPath, map[string]bool{
			provider.Catalog[domain.ModelTierMain].ID: true,
		})

		code, stdout, stderr := hooksRun(t, env, root,
			`{"source":"startup","session_id":"hooks-providers-e2e"}`, "hook", "session-start")
		if code != 0 {
			t.Fatalf("huddle hook session-start = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}

		// Exactly one JSON object: one value decodes, and the stream ends with it.
		decoder := json.NewDecoder(strings.NewReader(stdout))
		var merged map[string]any
		if err := decoder.Decode(&merged); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\nstdout:\n%s", err, stdout)
		}
		var trailing any
		if err := decoder.Decode(&trailing); err == nil {
			t.Fatalf("stdout carries more than one JSON value:\n%s", stdout)
		}
		specific, ok := merged["hookSpecificOutput"].(map[string]any)
		if !ok {
			t.Fatalf("the merged object %v names no hookSpecificOutput", merged)
		}
		if context, _ := specific["additionalContext"].(string); !strings.Contains(context, "/zai:review") {
			t.Errorf("the context %q never names the awaiting review", context)
		}

		ids := hooksModelIDs(t, settingsPath)
		joined := strings.Join(ids, ",")
		for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
			if !strings.Contains(joined, provider.Catalog[tier].ID) {
				t.Errorf("the /model entries are %v, want %s back", ids, provider.Catalog[tier].ID)
			}
		}
	})
}
