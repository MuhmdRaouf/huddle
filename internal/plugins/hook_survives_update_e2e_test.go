package plugins

// A hook outlives the plugin directory it was loaded from. Claude Code keeps CLAUDE_PLUGIN_ROOT
// fixed for a session's life, and an update replaces that cache directory: a hook line that runs
// a file under it fails from then on. The hooks.json line runs the launcher's stable
// $HOME/.agents/huddle-bin/current instead, and dist/run only until the first build exists. The
// plugin is installed into a temp CLAUDE_CONFIG_DIR's cache, the first hook builds the checkout's
// commit through dist/run, and a fake hub on its own port counts the reports the next two hooks
// send: one with the cache directory in place, one after it is deleted.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// updateHub is the fake hub: it answers 200 to every report and keeps the session ids it took.
type updateHub struct {
	mu       sync.Mutex
	sessions []string
}

func (h *updateHub) reports() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.sessions...)
}

// startUpdateHub serves the fake hub on a port this test holds until it ends.
func startUpdateHub(t *testing.T) (*updateHub, string) {
	t.Helper()
	hub := &updateHub{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SessionID string `json:"session_id"`
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/registry/report" &&
			json.NewDecoder(r.Body).Decode(&body) == nil {
			hub.mu.Lock()
			hub.sessions = append(hub.sessions, body.SessionID)
			hub.mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	_ = server.Listener.Close()
	server.Listener = ports.Listen(t)
	server.Start()
	t.Cleanup(server.Close)
	return hub, server.URL
}

// postToolUseLine is the PostToolUse command hooks.json gives Claude Code.
func postToolUseLine(t *testing.T) string {
	t.Helper()
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(repoRoot, "plugin", "hooks", "hooks.json"))), &file); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	groups := file.Hooks["PostToolUse"]
	if len(groups) != 1 || len(groups[0].Hooks) != 1 {
		t.Fatalf("hooks.json gives PostToolUse %d groups, want one line", len(groups))
	}
	return groups[0].Hooks[0].Command
}

// runHookLine runs one hooks.json line through /bin/sh the way Claude Code does, the event on stdin.
func runHookLine(t *testing.T, line, input string, env []string, dir string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", line)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), out.String(), errb.String()
	}
	if err != nil {
		t.Fatalf("run the hook line: %v", err)
	}
	return 0, out.String(), errb.String()
}

// awaitDetachedBuild waits for the build the first hook started: current names a binary, the
// commit dir's lock is gone and no detached run of the copy's launcher is left.
func awaitDetachedBuild(t *testing.T, binHome, launcher string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		_, built := os.Stat(filepath.Join(binHome, "current"))
		locks, _ := filepath.Glob(filepath.Join(binHome, "src", "*", ".huddle.build.lock"))
		if built == nil && len(locks) == 0 && len(detachedRuns(launcher)) == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	logs, _ := filepath.Glob(filepath.Join(binHome, "src", "*", "build.log"))
	for _, log := range logs {
		t.Logf("%s:\n%s", log, readFile(t, log))
	}
	t.Fatalf("the first hook's build left no current under %s within 5 minutes (still running: %v)", binHome, detachedRuns(launcher))
}

// detachedRuns lists the command lines of one launcher's detached builds still running.
func detachedRuns(launcher string) []string {
	out, err := exec.Command("ps", "axo", "command=").Output()
	if err != nil {
		return nil
	}
	var running []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, launcher) && strings.Contains(line, "--detached-fetch") {
			running = append(running, strings.TrimSpace(line))
		}
	}
	return running
}

func TestAHookSurvivesAPluginUpdate(t *testing.T) {
	home := testkit.TempDir(t, "huddle-hook-update-")
	claudeConfig := filepath.Join(home, "claude-config")
	pluginCopy := filepath.Join(claudeConfig, "plugins", "cache", "muhmdraouf", "huddle", "first")
	if err := os.CopyFS(pluginCopy, os.DirFS(filepath.Join(repoRoot, "plugin"))); err != nil {
		t.Fatalf("install the plugin into the temp cache: %v", err)
	}
	source, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	launcher := filepath.Join(pluginCopy, "dist", "run")
	binHome := filepath.Join(home, ".agents", "huddle-bin")
	hub, hubURL := startUpdateHub(t)
	huddleHome := filepath.Join(home, "huddle-home")
	if err := os.MkdirAll(huddleHome, 0o700); err != nil {
		t.Fatal(err)
	}
	// HOME is the temp home, so the line's default $HOME/.agents/huddle-bin is this test's own
	env := isoenv.Environ(t, home,
		"CLAUDE_CONFIG_DIR="+claudeConfig,
		"CLAUDE_PLUGIN_ROOT="+pluginCopy,
		"HUDDLE_SOURCE="+source,
		"HUDDLE_DOWNLOAD_BASE=http://127.0.0.1:1",
		"HUDDLE_HOME="+huddleHome,
		"HUDDLE_URL="+hubURL,
		"HUDDLE_TOKEN=hook-update-token",
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_AUTOSTART=0",
		"RADAR_HOME="+filepath.Join(home, "radar"),
		"RADAR_AUTOSTART=0",
	)
	line := postToolUseLine(t)
	input := `{"hook_event_name":"PostToolUse","session_id":"s-update","tool_name":"Bash","cwd":` +
		`"` + home + `"}`

	t.Run("the first hook, with no current yet, builds through dist/run and exits 0 in silence", func(t *testing.T) {
		code, stdout, stderr := runHookLine(t, line, input, env, home)
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("the first hook = (code %d, stdout %q, stderr %q), want silent exit 0", code, stdout, stderr)
		}
		awaitDetachedBuild(t, binHome, launcher)
	})

	t.Run("a hook with its plugin directory in place reports", func(t *testing.T) {
		code, stdout, stderr := runHookLine(t, line, input, env, home)
		if code != 0 || stdout != "" || stderr != "" {
			t.Fatalf("the hook = (code %d, stdout %q, stderr %q), want silent exit 0", code, stdout, stderr)
		}
		if got := hub.reports(); len(got) != 1 {
			t.Fatalf("the fake hub took %d reports (%v), want 1", len(got), got)
		}
	})

	t.Run("the same hook line after the update deletes its plugin directory still reports", func(t *testing.T) {
		if err := os.RemoveAll(pluginCopy); err != nil {
			t.Fatalf("delete the plugin copy: %v", err)
		}
		code, stdout, stderr := runHookLine(t, line, input, env, home)
		if code != 0 || stdout != "" || stderr != "" {
			t.Errorf("the hook after the update = (code %d, stdout %q, stderr %q), want silent exit 0", code, stdout, stderr)
		}
		got := hub.reports()
		if len(got) != 2 || got[0] != "s-update" || got[1] != "s-update" {
			t.Fatalf("the fake hub took %v, want both hooks' reports for s-update", got)
		}
	})
}
