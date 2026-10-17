package main

// The first session's end to end: a fresh install's SessionStart hook — no channel, no setup,
// no HUDDLE_URL — starts the bundled server on its own, through the committed launcher, with no
// JavaScript runtime on the PATH. Every process here is a child of this test and every home a
// temp one; nothing of the real machine is ever reached.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// TestE2EFirstSessionStartsHuddle walks a fresh install's first session: the launcher installs
// the binary, the session-start hook runs with no channel configured, and the server it starts
// answers /health on the port the home has saved. HUDDLE_AUTOSTART=0 keeps it down (the hooks
// package's cases pin that half).
func TestE2EFirstSessionStartsHuddle(t *testing.T) {
	root := t.TempDir()
	pluginCopy := filepath.Join(root, "plugin")
	binHome := filepath.Join(root, "huddle-bin")
	homeRoot := filepath.Join(root, "home")
	user := filepath.Join(root, "user")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, ".git", "info"), 0o755); err != nil {
		t.Fatalf("make the project: %v", err)
	}
	if err := os.CopyFS(pluginCopy, os.DirFS(pluginSource(t))); err != nil {
		t.Fatalf("copy the plugin: %v", err)
	}
	release := fakeReleaseServer(t, t, huddleBin)
	pinLauncherChecksum(t, filepath.Join(pluginCopy, "dist", "run"), huddleBin)
	port := pinFreePort(t, homeRoot)
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	env := machineEnv(homeRoot, user, binHome, pluginCopy, project, release.URL, nil)

	// where answers the address to poll: the port the home has saved now — the port choice may
	// have moved the server off the pinned one and recorded the port it took — falling back to
	// the pinned url while nothing else is saved
	where := func() string {
		if choice, has := cli.SavedPort(homeRoot); has {
			return cli.Local(choice.Port)
		}
		return url
	}

	// registered before the hooks run, so a failed install or start still ends what they began
	t.Cleanup(func() {
		var serverPid int
		if raw, err := os.ReadFile(filepath.Join(homeRoot, "run", "huddle.pid")); err == nil {
			serverPid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		code, stdout, stderr := runSh(t, 60*time.Second, env, project, `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" down`, "")
		if code != 0 {
			t.Errorf("dist/run down = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		if !pollFor(5*time.Second, func() bool { return !healthy(where()) }) && serverPid > 0 {
			// `down` left the test's own server running: end it the direct way
			_ = syscall.Kill(serverPid, syscall.SIGTERM)
			if !pollFor(3*time.Second, func() bool { return !healthy(where()) }) {
				_ = syscall.Kill(serverPid, syscall.SIGKILL)
				pollFor(2*time.Second, func() bool { return !healthy(where()) })
			}
		}
	})

	// the first command installs the binary the hook's start runs
	code, stdout, stderr := runSh(t, 90*time.Second, env, project, `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" help`, "")
	if code != 0 {
		t.Fatalf("dist/run help = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	for _, command := range hookCommands(t, pluginCopy, "SessionStart") {
		code, stdout, stderr := runSh(t, 30*time.Second, env, project, command,
			`{"hook_event_name":"SessionStart","session_id":"s-first","source":"startup","cwd":`+strconv.Quote(project)+`}`)
		if code != 0 {
			t.Fatalf("%s = %d, stdout %q, stderr %q", command, code, stdout, stderr)
		}
	}
	if !pollFor(20*time.Second, func() bool { return healthy(where()) }) {
		// the temp homes go with the test: say what the failed start left before they do
		for _, path := range []string{
			filepath.Join(homeRoot, "config.toml"),
			filepath.Join(homeRoot, "hooks.log"),
			filepath.Join(homeRoot, "run", "huddle.log"),
			filepath.Join(homeRoot, "run", "huddle.pid"),
			filepath.Join(homeRoot, "run", "huddle.port"),
		} {
			if raw, err := os.ReadFile(path); err == nil {
				t.Logf("=== %s ===\n%s", path, raw)
			} else {
				t.Logf("=== %s === %v", path, err)
			}
		}
		if entries, err := os.ReadDir(filepath.Join(binHome, huddleVersion(t))); err == nil {
			names := []string{}
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Logf("=== %s === %v", filepath.Join(binHome, huddleVersion(t)), names)
		}
		t.Fatalf("the first session did not start huddle at %s within 20 s", where())
	}
}
