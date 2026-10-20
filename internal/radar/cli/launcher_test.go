// Black-box tests of the committed launcher (plugin/dist/run) and the command line hooks.json runs —
// the exact files Claude Code starts — as child processes, on a PATH that holds no JavaScript runtime:
// the plugin's only dependency is the huddle binary the launcher itself installs. The radar word rides
// that one launcher: `dist/run radar hook` is the line the merged hooks.json runs. Every world is a
// temp one; only the processes these tests start are stopped.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
)

// repoRoot is the repository root, three levels above this package's directory.
const repoRoot = "../../.."

// huddleBin is the huddle binary TestMain builds once, stamped with the version the root VERSION names —
// the same stamp the release build gives it.
var huddleBin string

// TestMain builds the binary the launcher cases install, so they exercise the exact binary the
// plugin downloads. The build dir is removed before the exit, which a defer would not survive.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "radar-launcher-test-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "radar launcher tests: %v\n", err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "huddle")
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+launcherVersion(nil), "-o", bin, "./cmd/huddle")
	build.Dir = repoRoot
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building ./cmd/huddle: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	huddleBin = bin
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// launcherVersion is the version the root VERSION names — the release the launcher pins, so the binary
// a test installs lands exactly where the launcher looks for it. `t` may be nil.
func launcherVersion(t *testing.T) string {
	if t != nil {
		t.Helper()
	}
	data, err := os.ReadFile(filepath.Join(repoRoot, "VERSION"))
	if err != nil {
		if t != nil {
			t.Fatalf("read VERSION: %v", err)
		}
		fmt.Fprintf(os.Stderr, "radar launcher tests: read VERSION: %v\n", err)
		os.Exit(1)
	}
	version := strings.TrimSpace(string(data))
	if version == "" {
		if t != nil {
			t.Fatal("VERSION is empty")
		}
		fmt.Fprintln(os.Stderr, "radar launcher tests: VERSION is empty")
		os.Exit(1)
	}
	return version
}

// launcherPath is the committed dist/run every hook and command starts.
func launcherPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot, "plugin", "dist", "run")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("read the committed launcher: %v", err)
	}
	return path
}

// hooksCommand is the one command hooks.json runs for an event, radar's part inside it, as Claude
// Code reads it: the launcher's hook line naming the event.
func hooksCommand(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "plugin", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	groups, ok := parsed.Hooks["SessionStart"]
	if !ok || len(groups) == 0 {
		t.Fatal("hooks.json gives SessionStart no command")
	}
	const eventLine = `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" hook session-start`
	for _, group := range groups {
		for _, hook := range group.Hooks {
			if hook.Command == eventLine {
				return hook.Command
			}
		}
	}
	t.Fatal("hooks.json runs no event line on SessionStart")
	return ""
}

// launcherWorld is one temp install: a home for the binary, a state dir, a Claude config dir and
// a PATH holding only the POSIX tools the launcher needs — never a JavaScript runtime, which the
// plugin must not depend on.
type launcherWorld struct {
	binHome string
	state   string
	home    string
	path    string
}

// newLauncherWorld builds a world whose PATH holds the base tools plus any extras (a tool the
// host lacks is skipped, so the suite runs wherever the launcher's own fallbacks run).
func newLauncherWorld(t *testing.T, extras ...string) *launcherWorld {
	t.Helper()
	home := t.TempDir()
	world := &launcherWorld{
		binHome: filepath.Join(home, "huddle-bin"),
		state:   filepath.Join(home, "state"),
		home:    home,
	}
	base := []string{"sh", "uname", "dirname", "mkdir", "rm", "mv", "chmod", "date", "kill", "sleep", "ps"}
	bin := t.TempDir()
	for _, name := range append(append([]string{}, base...), extras...) {
		resolved, err := exec.LookPath(name)
		if err != nil {
			if contains(base, name) {
				t.Fatalf("the launcher's PATH needs %s: %v", name, err)
			}
			continue
		}
		if err := os.Symlink(resolved, filepath.Join(bin, name)); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}
	world.path = bin
	// Registered after both TempDir calls so it runs before either removal: the fetch the wait
	// is for still needs this PATH's mkdir, date and rm while it finishes.
	t.Cleanup(func() { world.waitForDetachedDownloads(t) })
	// Registered last so it runs first: a hook event can start this home's hub (autostart), and a
	// hub left running holds a port of the walk after the test is gone.
	t.Cleanup(func() { world.stopHub(t) })
	return world
}

// stopHub stops the hub this world's home started, if any: the pid in its run directory, and only
// while that process still names this home in its command line.
func (w *launcherWorld) stopHub(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.home, ".config", "huddle", "run", "huddle.pid"))
	if err != nil {
		return
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || pid <= 0 || !process.IsAlive(pid) {
		return
	}
	command, err := exec.Command("ps", "-o", "command=", "-p", strconv.FormatInt(pid, 10)).Output()
	if err != nil || !strings.Contains(string(command), w.home) {
		return
	}
	process.SignalGroup(pid, syscall.SIGTERM)
	deadline := time.Now().Add(10 * time.Second)
	for process.IsAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if process.IsAlive(pid) {
		process.SignalGroup(pid, syscall.SIGKILL)
	}
}

// contains answers whether names holds one name.
func contains(names []string, name string) bool {
	for _, item := range names {
		if item == name {
			return true
		}
	}
	return false
}

// waitForDetachedDownloads blocks until every download this world's hook runs started has
// finished, so the temp home's removal never races a fetch still writing inside it. The hook
// exits before its detached fetch writes anything, so a fetch forked a moment ago holds
// neither its lock nor its partial file yet: its process is watched too, under both spellings
// the process table shows — absolute for the hook line's ${CLAUDE_PLUGIN_ROOT}, relative for a
// direct launcherRun. Nothing is signalled; the wait ends at 10 s whatever the fetches do.
func (w *launcherWorld) waitForDetachedDownloads(t *testing.T) {
	t.Helper()
	relative := launcherPath(t)
	absolute, err := filepath.Abs(relative)
	if err != nil {
		t.Fatalf("resolve the launcher path: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		leftovers := fetchLeftovers(t, w.binHome)
		running := append(fetchProcesses(absolute), fetchProcesses(relative)...)
		if len(leftovers) == 0 && len(running) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("a detached download never finished within 10 s: files %v, processes %v", leftovers, running)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// fetchLeftovers answers the files a detached fetch leaves under one bin home:
// .huddle.fetch.lock while it runs and .huddle.<pid>.download mid-transfer.
func fetchLeftovers(t *testing.T, binHome string) []string {
	t.Helper()
	leftovers := []string{}
	err := filepath.WalkDir(binHome, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // no fetch ever ran in this world
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".fetch.lock") || strings.HasSuffix(name, ".download") ||
			strings.HasSuffix(name, ".tmp") {
			leftovers = append(leftovers, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", binHome, err)
	}
	return leftovers
}

// fetchProcesses answers the command lines of one launcher's detached fetches still running.
// The launcher path is this checkout's, so only fetches this world's own hook runs started can
// match; a machine without ps falls back to the file wait.
func fetchProcesses(launcher string) []string {
	out, err := exec.Command("ps", "axo", "command=").Output()
	if err != nil {
		return nil
	}
	running := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, launcher) && strings.Contains(line, "--detached-fetch") {
			running = append(running, strings.TrimSpace(line))
		}
	}
	return running
}

// installBin puts the built binary where the launcher looks for it: <home>/<version>/huddle.
func (w *launcherWorld) installBin(t *testing.T) {
	t.Helper()
	dir := filepath.Join(w.binHome, launcherVersion(t))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	target := filepath.Join(dir, "huddle")
	data, err := os.ReadFile(huddleBin)
	if err != nil {
		t.Fatalf("read the built binary: %v", err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatalf("install the binary: %v", err)
	}
}

// env is the child's whole environment, built from scratch so nothing from the surrounding
// shell — no PATH, no real home — leaks into the run.
func (w *launcherWorld) env(extra ...string) []string {
	return append([]string{
		"HOME=" + w.home,
		"PATH=" + w.path,
		"RADAR_HOME=" + w.state,
		"CLAUDE_CONFIG_DIR=" + filepath.Join(w.home, "claude"),
		"ZAI_STATE_DIR=" + filepath.Join(w.home, "zai"),
		"AGENTS_HUDDLE_HOME=" + w.binHome,
		// never the real release URL: a closed port answers nothing, and a test that wants the
		// download path overrides it with its own server
		"HUDDLE_DOWNLOAD_BASE=http://127.0.0.1:1",
	}, extra...)
}

// run runs one command line through /bin/sh the way Claude Code runs a hook's command, with
// stdin fed to it, and answers its exit code and both outputs.
func (w *launcherWorld) run(t *testing.T, timeout time.Duration, command, stdin string, env []string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
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

// launcherRun runs the launcher itself with one entry and its arguments.
func (w *launcherWorld) launcherRun(t *testing.T, timeout time.Duration, stdin, entry string, env []string) (int, string, string) {
	t.Helper()
	return w.run(t, timeout, fmt.Sprintf(`sh %q %s`, launcherPath(t), entry), stdin, env)
}

// expectSilentSuccess is the hook's contract: exit 0, nothing on either stream.
func expectSilentSuccess(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code != 0 || stdout != "" || stderr != "" {
		t.Errorf("the run owes silence and exit 0, got code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// spoolLines is every line the spool collected so far, oldest first.
func (w *launcherWorld) spoolLines(t *testing.T) []map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(w.state, "spool", "*.jsonl"))
	if err != nil {
		t.Fatalf("read the spool dir: %v", err)
	}
	lines := []map[string]any{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("parse the spool line %q: %v", line, err)
			}
			lines = append(lines, record)
		}
	}
	return lines
}

// lastSpoolLine is the newest spool record, with the fields a hook owes it.
func (w *launcherWorld) lastSpoolLine(t *testing.T) map[string]any {
	t.Helper()
	lines := w.spoolLines(t)
	if len(lines) == 0 {
		t.Fatal("the spool holds no line")
	}
	return lines[len(lines)-1]
}

// pluginRoot is the committed plugin dir Claude Code points CLAUDE_PLUGIN_ROOT at.
func pluginRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(repoRoot, "plugin"))
	if err != nil {
		t.Fatalf("resolve the plugin root: %v", err)
	}
	return root
}

// TestHooksJsonCommand runs the command hooks.json runs for an event, radar's part inside it, the
// way Claude Code runs it: through /bin/sh, with the plugin root Claude Code sets and only the
// POSIX tools on PATH. A hook never blocks or fails Claude Code, so every failure mode owes silence
// and exit 0; a healthy one records its event.
func TestHooksJsonCommand(t *testing.T) {
	command := hooksCommand(t)
	plugin := pluginRoot(t)

	t.Run("exits 0 in silence when no binary is installed and none can be downloaded", func(t *testing.T) {
		world := newLauncherWorld(t) // no curl, and the download base is a closed port
		code, stdout, stderr := world.run(t, 20*time.Second, command, `{"hook_event_name":"Stop","session_id":"s1"}`,
			world.env("CLAUDE_PLUGIN_ROOT="+plugin))
		expectSilentSuccess(t, code, stdout, stderr)
		if lines := world.spoolLines(t); len(lines) != 0 {
			t.Errorf("nothing was installed, yet the spool holds %d lines", len(lines))
		}
	})

	t.Run("records the event once the binary is installed", func(t *testing.T) {
		world := newLauncherWorld(t)
		world.installBin(t)
		code, stdout, stderr := world.run(t, 20*time.Second, command, `{"hook_event_name":"Stop","session_id":"s1"}`,
			world.env("CLAUDE_PLUGIN_ROOT="+plugin))
		expectSilentSuccess(t, code, stdout, stderr)
		last := world.lastSpoolLine(t)
		if last["event"] != "Stop" || last["session_id"] != "s1" {
			t.Errorf("the spool line is %v, want the Stop event of session s1", last)
		}
	})

	t.Run("exits 0 in silence when the download is missing", func(t *testing.T) {
		world := newLauncherWorld(t, "curl")
		missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}))
		t.Cleanup(missing.Close)
		code, stdout, stderr := world.run(t, 20*time.Second, command, `{"hook_event_name":"Stop","session_id":"s1"}`,
			world.env("CLAUDE_PLUGIN_ROOT="+plugin, "HUDDLE_DOWNLOAD_BASE="+missing.URL))
		expectSilentSuccess(t, code, stdout, stderr)
	})
}

// TestLauncherFreshInstall is the fresh-install leg: the committed launcher on a PATH holding no
// JavaScript runtime, with the binary preinstalled the way its own download leaves it — one
// SessionStart hook through the radar word, the words pointing at the huddle hub, and nothing
// left behind: no dashboard of radar's own is ever started.
func TestLauncherFreshInstall(t *testing.T) {
	world := newLauncherWorld(t, "curl", "shasum", "openssl")
	world.installBin(t)
	env := world.env()

	code, stdout, stderr := world.launcherRun(t, 20*time.Second, `{"hook_event_name":"SessionStart","session_id":"e2e-1"}`, "radar hook", env)
	expectSilentSuccess(t, code, stdout, stderr)
	if last := world.lastSpoolLine(t); last["event"] != "SessionStart" || last["session_id"] != "e2e-1" {
		t.Errorf("the spool line is %v, want the SessionStart event of session e2e-1", last)
	}

	// a fresh install has no set-up huddle home: start says so and starts nothing
	code, stdout, stderr = world.launcherRun(t, 30*time.Second, "", "radar start", env)
	if code != 6 {
		t.Fatalf("radar start = %d, stdout %q, stderr %q, want 6", code, stdout, stderr)
	}
	if want := "radar: the huddle hub is not set up; run /huddle:setup"; strings.TrimSpace(stderr) != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}

	code, stdout, stderr = world.launcherRun(t, 20*time.Second, "", "radar stop", env)
	if code != 0 || strings.TrimSpace(stdout) != "radar: runs inside the huddle hub; /huddle:stop stops it" {
		t.Fatalf("radar stop = %d, stdout %q, stderr %q, want 0 and the hub line", code, stdout, stderr)
	}

	// nothing of radar's own ever listened: no server record, nothing to stop
	if _, err := os.Stat(filepath.Join(world.state, "server.json")); !os.IsNotExist(err) {
		t.Errorf("a standalone dashboard left server.json behind (err %v)", err)
	}
	if procs := fetchProcesses(launcherPath(t)); len(procs) != 0 {
		t.Errorf("the launcher left %v running", procs)
	}
}
