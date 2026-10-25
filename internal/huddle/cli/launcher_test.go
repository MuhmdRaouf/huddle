// Black-box tests of the committed launcher (plugin/dist/run) and the lines the
// plugin's own files run — hooks.json, .mcp.json and the commands' CLI — as child processes, on
// a PATH that holds no JavaScript runtime: the plugin's only dependency is the huddle binary the
// launcher itself installs. Every world is a temp one; only the processes these tests start are
// stopped.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// launcherRepoRoot is the repository root, two levels above this package's directory.
const launcherRepoRoot = "../../.."

// launcherPluginRoot is the committed plugin dir Claude Code points CLAUDE_PLUGIN_ROOT at.
func launcherPluginRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(launcherRepoRoot, "plugin"))
	if err != nil {
		t.Fatalf("resolve the plugin root: %v", err)
	}
	return root
}

// launcherVersion is the version the root VERSION names — the release the launcher pins,
// so the binary a test installs lands exactly where the launcher looks for it.
func launcherVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(launcherRepoRoot, "VERSION"))
	if err != nil {
		t.Fatalf("read VERSION: %v", err)
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		t.Fatal("VERSION is empty")
	}
	return version
}

// launcherHook is one group's line as hooks.json states it; an event may hold several.
type launcherHook struct {
	Event   string
	Name    string // the hook's name, as the launcher line's `hook <name>` spells it
	Matcher string // "" when the group names none
	Command string
	Timeout int
}

// launcherHooks reads every hook line out of the committed hooks.json. One line per event runs
// one process for all three parts; approve and permit keep their own lines for their matchers.
func launcherHooks(t *testing.T) []launcherHook {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(launcherPluginRoot(t), "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var file struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	events := []string{}
	for event := range file.Hooks {
		events = append(events, event)
	}
	// a deterministic order: the events a session sees, first to last
	order := map[string]int{
		"SessionStart": 0, "UserPromptSubmit": 1, "PreToolUse": 2, "PostToolUse": 3, "Notification": 4,
		"PermissionRequest": 5, "Stop": 6, "SubagentStart": 7, "SubagentStop": 8, "SessionEnd": 9,
		"PreCompact": 10, "TaskCreated": 11, "TeammateIdle": 12,
	}
	for i := 0; i < len(events); i++ {
		for j := i + 1; j < len(events); j++ {
			if order[events[j]] < order[events[i]] {
				events[i], events[j] = events[j], events[i]
			}
		}
	}
	out := make([]launcherHook, 0, len(events))
	for _, event := range events {
		for _, group := range file.Hooks[event] {
			if len(group.Hooks) != 1 {
				t.Fatalf("%s has a group holding %d hooks; want one hook per group", event, len(group.Hooks))
			}
			name := ""
			if m := regexp.MustCompile(` hook ([a-z-]+)$`).FindStringSubmatch(group.Hooks[0].Command); m != nil {
				name = m[1]
			}
			out = append(out, launcherHook{
				Event:   event,
				Name:    name,
				Matcher: group.Matcher,
				Command: group.Hooks[0].Command,
				Timeout: group.Hooks[0].Timeout,
			})
		}
	}
	return out
}

// launcherLine is the command one hook name owes: the committed launcher, which starts the
// huddle binary (downloading it on first use) and never blocks or fails Claude Code.
func launcherLine(name string) string {
	return fmt.Sprintf(`sh "${CLAUDE_PLUGIN_ROOT}/dist/run" hook %s`, name)
}

// TestHooksJSONRunsTheLauncher pins hooks.json whole: one launcher line per event, naming the
// event, with the old matchers and the largest old timeout, one hook per group. PreToolUse keeps
// the approve line for Bash and PermissionRequest the permit line: the two that answer Claude
// Code, not only observe it.
func TestHooksJSONRunsTheLauncher(t *testing.T) {
	want := []launcherHook{
		{Event: "SessionStart", Matcher: "startup|resume|clear|compact", Name: "session-start", Command: launcherLine("session-start"), Timeout: 15},
		{Event: "UserPromptSubmit", Name: "user-prompt-submit", Command: launcherLine("user-prompt-submit"), Timeout: 5},
		{Event: "PreToolUse", Matcher: "Bash", Name: "approve", Command: launcherLine("approve"), Timeout: 5},
		{Event: "PreToolUse", Name: "pre-tool-use", Command: launcherLine("pre-tool-use"), Timeout: 5},
		{Event: "PostToolUse", Name: "post-tool-use", Command: launcherLine("post-tool-use"), Timeout: 5},
		{Event: "Notification", Name: "notification", Command: launcherLine("notification"), Timeout: 5},
		{Event: "PermissionRequest", Name: "permit", Command: launcherLine("permit"), Timeout: 600},
		{Event: "PermissionRequest", Name: "permission-request", Command: launcherLine("permission-request"), Timeout: 5},
		{Event: "Stop", Name: "stop", Command: launcherLine("stop"), Timeout: 15},
		{Event: "SubagentStart", Name: "subagent-start", Command: launcherLine("subagent-start"), Timeout: 5},
		{Event: "SubagentStop", Name: "subagent-stop", Command: launcherLine("subagent-stop"), Timeout: 7},
		{Event: "SessionEnd", Name: "session-end", Command: launcherLine("session-end"), Timeout: 5},
		{Event: "PreCompact", Name: "pre-compact", Command: launcherLine("pre-compact"), Timeout: 5},
		{Event: "TaskCreated", Name: "task-created", Command: launcherLine("task-created"), Timeout: 5},
		{Event: "TeammateIdle", Name: "teammate-idle", Command: launcherLine("teammate-idle"), Timeout: 5},
	}
	got := launcherHooks(t)
	if len(got) != len(want) {
		t.Fatalf("hooks.json runs %d hooks (%v), want %d", len(got), launcherEventNames(got), len(want))
	}
	for i, hook := range got {
		expected := want[i]
		if hook.Event != expected.Event {
			t.Errorf("hook %d = %s, want %s", i, hook.Event, expected.Event)
			continue
		}
		if hook.Command != expected.Command || hook.Name != expected.Name {
			t.Errorf("%s: command = %q, want the launcher line %q", hook.Event, hook.Command, expected.Command)
		}
		if hook.Matcher != expected.Matcher {
			t.Errorf("%s: matcher = %q, want %q", hook.Event, hook.Matcher, expected.Matcher)
		}
		if hook.Timeout != expected.Timeout {
			t.Errorf("%s: timeout = %d, want %d", hook.Event, hook.Timeout, expected.Timeout)
		}
	}
}

// launcherEventNames lists the events, for failure messages.
func launcherEventNames(hooks []launcherHook) []string {
	names := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		names = append(names, hook.Event)
	}
	return names
}

// TestTheMcpJsonStartsTheBridge pins .mcp.json: Claude Code spawns the plugin's bridge through
// sh, and the bridge is the launcher's mcp entry on the huddle binary.
func TestTheMcpJsonStartsTheBridge(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(launcherPluginRoot(t), ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var file struct {
		McpServers struct {
			Huddle struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"huddle"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	server := file.McpServers.Huddle
	if server.Command != "sh" || len(server.Args) != 1 || server.Args[0] != "${CLAUDE_PLUGIN_ROOT}/bin/huddle-mcp" {
		t.Errorf(".mcp.json starts %q %q, want sh with the plugin's bin/huddle-mcp", server.Command, server.Args)
	}
}

// TestTheThreeCommandsRunTheCLI pins huddle's command files: the three among the merged plugin's
// ten (the hub's and kanban's carry their own words and are pinned by the plugin's own suite), each
// running the plugin's CLI and showing its join and dashboard lines verbatim, with no wording that
// hides a token or a link from the user it is for.
func TestTheThreeCommandsRunTheCLI(t *testing.T) {
	commands := filepath.Join(launcherPluginRoot(t), "commands")
	entries, err := os.ReadDir(commands)
	if err != nil {
		t.Fatalf("read the commands dir: %v", err)
	}
	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	for _, name := range []string{"invite.md", "join.md", "setup.md"} {
		if !names[name] {
			t.Errorf("the commands hold no %s (/huddle:%s is gone)", name, strings.TrimSuffix(name, ".md"))
		}
	}
	for _, name := range []string{"setup.md", "invite.md", "join.md"} {
		raw, err := os.ReadFile(filepath.Join(commands, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		md := string(raw)
		if !regexp.MustCompile(`(?s)^---\ndescription: .+\n(argument-hint: .+\n)?allowed-tools: Bash\n---\n`).MatchString(md) {
			t.Errorf("%s: the frontmatter is not description, argument-hint and allowed-tools: Bash", name)
		}
		if !strings.Contains(md, `"${CLAUDE_PLUGIN_ROOT}/bin/huddle"`) {
			t.Errorf("%s: does not run the plugin's CLI", name)
		}
		if !strings.Contains(md, "verbatim") {
			t.Errorf("%s: does not show the CLI's lines verbatim", name)
		}
		if regexp.MustCompile(`(?i)a secret|not to claude|claude never sees|never reaches you|only you see|shows to the user`).MatchString(md) {
			t.Errorf("%s: words a token or a link as hidden from its own user", name)
		}
	}
}

// sortStrings sorts names in place, the default string order.
func sortStrings(names []string) {
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
}

// launcherWorld is one temp install: a home for the binary, a home for the user, a project and
// a PATH holding only the POSIX tools the launcher and the binary need — never a JavaScript
// runtime, which the plugin must not depend on.
type launcherWorld struct {
	binHome    string
	home       string
	project    string
	path       string
	pluginRoot string
}

// newLauncherWorld builds a world whose PATH holds the base tools plus any extras (a tool the
// host lacks is skipped, so the suite runs wherever the launcher's own fallbacks run).
func newLauncherWorld(t *testing.T, extras ...string) *launcherWorld {
	t.Helper()
	home := t.TempDir()
	world := &launcherWorld{
		binHome:    filepath.Join(home, "huddle-bin"),
		home:       home,
		project:    filepath.Join(home, "fresh-install"),
		pluginRoot: launcherPluginRoot(t),
	}
	if err := os.MkdirAll(filepath.Join(world.project, ".git", "info"), 0o755); err != nil {
		t.Fatalf("make the project: %v", err)
	}
	// the launched binary resolves its home from this world's env (no HUDDLE_HOME, so
	// $XDG_CONFIG_HOME/huddle): a saved port keeps every start it does off the shared
	// 18860-18869 walk
	pinFreePort(t, filepath.Join(home, ".config", "huddle"))
	// the binary's process lookups read ps; lsof only backs one fallback of them
	base := []string{"sh", "uname", "dirname", "mkdir", "rm", "mv", "chmod", "date", "kill", "sleep", "ps"}
	bin := t.TempDir()
	for _, name := range append(append([]string{}, base...), extras...) {
		resolved, err := exec.LookPath(name)
		if err != nil {
			if launcherContains(base, name) {
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
	return world
}

// launcherContains answers whether names holds one name.
func launcherContains(names []string, name string) bool {
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
// neither its lock nor its partial file yet: its process is watched too. Nothing is signalled;
// the wait ends at 10 s whatever the fetches do.
func (w *launcherWorld) waitForDetachedDownloads(t *testing.T) {
	t.Helper()
	launcher := filepath.Join(w.pluginRoot, "dist", "run")
	deadline := time.Now().Add(10 * time.Second)
	for {
		leftovers := launcherFetchLeftovers(t, w.binHome)
		running := launcherFetchProcesses(launcher)
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

// launcherFetchLeftovers answers the files a detached fetch leaves under one bin home:
// .huddle.fetch.lock while it runs and .huddle.<pid>.download mid-transfer.
func launcherFetchLeftovers(t *testing.T, binHome string) []string {
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

// launcherFetchProcesses answers the command lines of one launcher's detached fetches still
// running. The launcher path is this checkout's, so only fetches this world's own hook runs
// started can match; a machine without ps falls back to the file wait.
func launcherFetchProcesses(launcher string) []string {
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

// env is the child's whole environment, built from scratch so nothing of the surrounding shell
// — no PATH, no real home, no real plugin root — leaks into the run.
func (w *launcherWorld) env(extra ...string) []string {
	vars := []string{
		"HOME=" + w.home,
		"PATH=" + w.path,
		"TMPDIR=" + w.home,
		"XDG_CONFIG_HOME=" + filepath.Join(w.home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(w.home, ".local", "state"),
		"XDG_DATA_HOME=" + filepath.Join(w.home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(w.home, ".cache"),
		"RADAR_HOME=" + filepath.Join(w.home, "radar"),
		"AGENTS_HUDDLE_HOME=" + w.binHome,
		// never the real release URL: a closed port answers nothing, and a test that wants the
		// download path overrides it with its own server
		"HUDDLE_DOWNLOAD_BASE=http://127.0.0.1:1",
		// a notification lands in a file, never on this desktop
		"HUDDLE_NOTIFY_LOG=" + filepath.Join(w.home, "notify.log"),
		"CLAUDE_PLUGIN_ROOT=" + w.pluginRoot,
		"CLAUDE_PROJECT_DIR=" + w.project,
	}
	if user := os.Getenv("USER"); user != "" {
		vars = append(vars, "USER="+user)
	}
	return append(vars, extra...)
}

// run runs one command line through /bin/sh the way Claude Code runs a hook's or a command's
// line, with stdin fed to it, and answers its exit code and both outputs.
func (w *launcherWorld) run(t *testing.T, timeout time.Duration, command, stdin string, env []string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = w.project
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
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

// hookInput is the JSON Claude Code feeds one hook.
func hookInput(event string, extra string) string {
	body := fmt.Sprintf(`"hook_event_name":%q`, event)
	if extra != "" {
		body += "," + extra
	}
	return "{" + body + "}"
}

// hook runs one event's hooks.json line with the input Claude Code gives it.
func (w *launcherWorld) hook(t *testing.T, event string, extraInput string, extraEnv ...string) (int, string, string) {
	t.Helper()
	for _, hook := range launcherHooks(t) {
		if hook.Event != event {
			continue
		}
		return w.run(t, time.Duration(hook.Timeout)*time.Second+15*time.Second,
			hook.Command, hookInput(event, extraInput), w.env(extraEnv...))
	}
	t.Fatalf("hooks.json runs no %s", event)
	return 0, "", ""
}

// cli runs the CLI line the command files name, as the Bash tool does.
func (w *launcherWorld) cli(t *testing.T, args string, extraEnv ...string) (int, string, string) {
	t.Helper()
	return w.run(t, 60*time.Second, `sh "${CLAUDE_PLUGIN_ROOT}/bin/huddle" `+args, "",
		w.env(append([]string{"CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=e2e-1", "CLAUDE_PID=40001"}, extraEnv...)...))
}

// stopRecordedServer stops the server the project's pid file names — only the process a test's
// own setup started — and answers whether one was running.
func (w *launcherWorld) stopRecordedServer(t *testing.T) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.home, ".config", "huddle", "run", "huddle.pid"))
	if err != nil {
		return // nothing recorded: nothing to stop
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 || !processAlive(pid) {
		return
	}
	stopProcess(pid)
}

// TestTheHooksJsonCommandsExitZeroInSilenceWithNoBinaryAndNoRuntime: installed but nothing
// downloaded yet, with no JavaScript runtime on the PATH either, every hook owes Claude Code
// silence and exit 0 — a hook never blocks or fails the session it runs in.
func TestTheHooksJsonCommandsExitZeroInSilenceWithNoBinaryAndNoRuntime(t *testing.T) {
	world := newLauncherWorld(t) // no curl either: the detached fetch cannot hang on the closed port
	for _, hook := range launcherHooks(t) {
		name := hook.Name
		if name == "" {
			name = hook.Command
		}
		t.Run(hook.Event+" "+name, func(t *testing.T) {
			code, stdout, stderr := world.run(t, 30*time.Second, hook.Command, hookInput(hook.Event, `"session_id":"s1"`), world.env())
			if code != 0 || stdout != "" || stderr != "" {
				t.Errorf("the hook owes silence and exit 0, got code %d, stdout %q, stderr %q", code, stdout, stderr)
			}
		})
	}
}

// launcherMcp is the MCP server as .mcp.json starts it, driven over stdio.
type launcherMcp struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	mu    sync.Mutex
	lines []map[string]any
	dead  chan struct{}
}

// startLauncherMcp spawns the bridge as Claude Code does and reads its stdout lines.
func (w *launcherWorld) startLauncherMcp(t *testing.T, extraEnv ...string) *launcherMcp {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(launcherPluginRoot(t), ".mcp.json"))
	if err != nil {
		t.Fatalf("read .mcp.json: %v", err)
	}
	var file struct {
		McpServers struct {
			Huddle struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"huddle"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse .mcp.json: %v", err)
	}
	server := file.McpServers.Huddle
	args := make([]string, 0, len(server.Args))
	for _, arg := range server.Args {
		args = append(args, strings.ReplaceAll(arg, "${CLAUDE_PLUGIN_ROOT}", launcherPluginRoot(t)))
	}
	cmd := exec.Command(server.Command, args...)
	cmd.Dir = w.project
	cmd.Env = w.env(extraEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("pipe the bridge: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the bridge: %v", err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the bridge: %v", err)
	}
	bridge := &launcherMcp{cmd: cmd, in: stdin, dead: make(chan struct{})}
	go func() {
		defer close(bridge.dead)
		defer func() { _ = stdin.Close() }()
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			for _, piece := range strings.Split(strings.TrimSuffix(line, "\n"), "\n") {
				if piece == "" {
					continue
				}
				var decoded map[string]any
				if json.Unmarshal([]byte(piece), &decoded) != nil {
					continue
				}
				bridge.mu.Lock()
				bridge.lines = append(bridge.lines, decoded)
				bridge.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(bridge.stop)
	return bridge
}

// send writes one JSON-RPC line to the bridge.
func (b *launcherMcp) send(t *testing.T, line string) {
	t.Helper()
	if _, err := fmt.Fprintln(b.in, line); err != nil {
		t.Fatalf("write to the bridge: %v", err)
	}
}

// answerOf waits for the answer to one id.
func (b *launcherMcp) answerOf(t *testing.T, id float64, within time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		for _, line := range b.lines {
			if number, ok := line["id"].(float64); ok && number == id {
				b.mu.Unlock()
				return line
			}
		}
		b.mu.Unlock()
		select {
		case <-b.dead:
			t.Fatal("the bridge exited before it answered")
		case <-time.After(25 * time.Millisecond):
		}
	}
	t.Fatalf("the bridge never answered id %.0f", id)
	return nil
}

// stop ends the bridge this test started.
func (b *launcherMcp) stop() {
	_ = b.in.Close()
	_ = b.cmd.Process.Kill()
	_, _ = b.cmd.Process.Wait()
}

// TestLauncherFreshInstall is the fresh-install leg: the committed launcher on a PATH holding
// no JavaScript runtime, with the huddle binary preinstalled the way its own download leaves it.
// Before any setup every hook is silent and the MCP bridge holds two tools; /huddle:setup's one
// CLI call starts the server and joins the project; the next session start carries the channel
// and puts the CLI on the session's PATH; the bridge answers one tool call as the joined
// project; huddle down stops the server and nothing is left behind.
func TestLauncherFreshInstall(t *testing.T) {
	world := newLauncherWorld(t, "lsof")
	world.installBin(t)
	t.Cleanup(func() { world.stopRecordedServer(t) })

	t.Run("installed, nothing set up: every hook is silent", func(t *testing.T) {
		for _, hook := range launcherHooks(t) {
			extra := `"session_id":"e2e-1"`
			if hook.Event == "SessionStart" {
				extra += `,"source":"startup"`
			}
			code, stdout, stderr := world.run(t, time.Duration(hook.Timeout)*time.Second+15*time.Second,
				hook.Command, hookInput(hook.Event, extra), world.env())
			if code != 0 || stdout != "" || stderr != "" {
				t.Errorf("%s owes silence and exit 0 before setup, got code %d, stdout %q, stderr %q",
					hook.Event, code, stdout, stderr)
			}
		}
	})

	t.Run("the MCP bridge answers before setup with the two tools a session outside a huddle holds", func(t *testing.T) {
		bridge := world.startLauncherMcp(t, "CLAUDE_PID=40001")
		bridge.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"0"}}}`)
		bridge.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		bridge.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		bridge.send(t, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		init := bridge.answerOf(t, 1, 30*time.Second)
		if name := jsonPath(t, init, "result", "serverInfo", "name"); name != "huddle" {
			t.Errorf("initialize names the server %q, want huddle", name)
		}
		list := bridge.answerOf(t, 2, 30*time.Second)
		tools, ok := jsonPathAny(t, list, "result", "tools").([]any)
		if !ok || len(tools) != 2 {
			t.Fatalf("tools/list before setup holds %v, want the two tools of a session outside a huddle", list)
		}
		names := []string{}
		for _, tool := range tools {
			if def, ok := tool.(map[string]any); ok {
				names = append(names, fmt.Sprintf("%v", def["name"]))
			}
		}
		sortStrings(names)
		if strings.Join(names, ",") != "join,status" {
			t.Errorf("the tools before setup are %v, want status and join", names)
		}
		status := bridge.answerOf(t, 3, 30*time.Second)
		if text := jsonPath(t, status, "result", "content"); !strings.Contains(text, "/huddle:setup") {
			t.Errorf("status says %q, want it to name /huddle:setup", text)
		}
	})

	var url string
	t.Run("huddle setup --start starts the server and joins this project", func(t *testing.T) {
		code, stdout, stderr := world.cli(t, "setup --start")
		if code != 0 {
			t.Fatalf("setup --start = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		join := regexp.MustCompile(`(?m)^join: +/huddle:join (127\.0\.0\.1:\d+) --token (\S+)`).FindStringSubmatch(stdout)
		if join == nil {
			t.Fatalf("setup printed no join line: %q", stdout)
		}
		url = "http://" + join[1]
		if !regexp.MustCompile(`(?m)^dashboard: http://127\.0\.0\.1:\d+/\?code=\S+`).MatchString(stdout) {
			t.Errorf("setup printed no dashboard link: %q", stdout)
		}
		raw, err := os.ReadFile(filepath.Join(world.project, ".agents", "huddle", "huddle.json"))
		if err != nil {
			t.Fatalf("read the project's huddle.json: %v", err)
		}
		var cfg struct {
			Channel   string `json:"channel"`
			As        string `json:"as"`
			Autostart bool   `json:"autostart"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("parse huddle.json: %v", err)
		}
		if cfg.Channel != "fresh-install" || cfg.As != "fresh-install" || !cfg.Autostart {
			t.Errorf("huddle.json = %s, want the channel and session named after the project, autostart on", raw)
		}
		exclude, err := os.ReadFile(filepath.Join(world.project, ".git", "info", "exclude"))
		if err != nil || !regexp.MustCompile(`(?m)^\.agents/huddle/$`).MatchString(string(exclude)) {
			t.Errorf("the project's git exclude holds %q, want .agents/huddle/ kept out of git", exclude)
		}
		until(t, 30*time.Second, func() bool { return e2eHealthy(url) }, "the server to answer /health")
	})

	t.Run("the next session start carries the channel and puts the CLI on the session's PATH", func(t *testing.T) {
		envFile := filepath.Join(world.home, "claude-env")
		code, stdout, stderr := world.hook(t, "SessionStart", `"session_id":"e2e-1","source":"startup"`, "CLAUDE_ENV_FILE="+envFile)
		if code != 0 {
			t.Fatalf("session-start = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		context, systemMessages := sessionStartOutput(t, stdout)
		if !strings.Contains(context, `You are in Huddle channel "fresh-install" as "fresh-install"`) {
			t.Errorf("the context is %q, want it to name the channel and the session", context)
		}
		if len(systemMessages) > 0 {
			t.Errorf("session-start printed systemMessages (%v); its lines come from the CLI, not a hook", systemMessages)
		}
		// the launcher passes CLAUDE_PLUGIN_ROOT and CLAUDE_ENV_FILE through: the PATH line names
		// the committed plugin's bin/, once per session
		raw, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatalf("read the env file: %v", err)
		}
		want := `export PATH="` + filepath.Join(launcherPluginRoot(t), "bin") + `:$PATH"` + "\n"
		if string(raw) != want {
			t.Errorf("the env file holds %q, want exactly %q", raw, want)
		}
	})

	t.Run("the bridge answers one tool call as the joined project", func(t *testing.T) {
		bridge := world.startLauncherMcp(t, "CLAUDE_PID=40001")
		bridge.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"0"}}}`)
		bridge.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
		bridge.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"status","arguments":{}}}`)
		bridge.answerOf(t, 1, 30*time.Second)
		status := bridge.answerOf(t, 2, 30*time.Second)
		if text := jsonPath(t, status, "result", "content"); !strings.Contains(text, "you are fresh-install") {
			t.Errorf("status says %q, want it to name the joined session", text)
		}
	})

	t.Run("after the server dies unlogged, the next session start brings it back", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(world.home, ".config", "huddle", "run", "huddle.pid"))
		if err != nil {
			t.Fatalf("read the pid file: %v", err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 0 || !processAlive(pid) {
			t.Fatalf("the pid file names %q, want the server this test started", raw)
		}
		if err := signalKill(pid); err != nil { // only the process this test's own setup started
			t.Fatalf("kill the server: %v", err)
		}
		until(t, 10*time.Second, func() bool { return !processAlive(pid) }, "the killed server to die")

		code, stdout, stderr := world.hook(t, "SessionStart", `"session_id":"e2e-1","source":"startup"`)
		if code != 0 {
			t.Fatalf("session-start after the crash = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		context, _ := sessionStartOutput(t, stdout)
		if !strings.Contains(context, `You are in Huddle channel "fresh-install"`) {
			t.Errorf("the context after the crash is %q, want the member remembered and the channel named", context)
		}
		until(t, 30*time.Second, func() bool { return e2eHealthy(url) }, "the restarted server to answer /health")
	})

	t.Run("huddle down stops the server", func(t *testing.T) {
		code, stdout, stderr := world.cli(t, "down")
		if code != 0 {
			t.Fatalf("down = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
		until(t, 10*time.Second, func() bool { return !e2eHealthy(url) }, "the server to stop answering")
	})
}

// sessionStartOutput reads a session-start line's stdout: one JSON line per part with something
// to tell Claude (the merged process concatenates its parts' output). It answers every part's
// additionalContext joined, and every systemMessage any part printed.
func sessionStartOutput(t *testing.T, stdout string) (context string, systemMessages []string) {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var out struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
			SystemMessage any `json:"systemMessage"`
		}
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			t.Fatalf("session-start printed %q, want one JSON line per part", stdout)
		}
		if out.HookSpecificOutput.AdditionalContext != "" {
			if context != "" {
				context += "\n\n"
			}
			context += out.HookSpecificOutput.AdditionalContext
		}
		if out.SystemMessage != nil {
			systemMessages = append(systemMessages, fmt.Sprintf("%v", out.SystemMessage))
		}
	}
	return context, systemMessages
}

// jsonPath walks one decoded answer to a text field, rendered as the message it holds.
func jsonPath(t *testing.T, value map[string]any, path ...string) string {
	t.Helper()
	here := jsonPathAny(t, value, path...)
	switch typed := here.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// jsonPathAny walks one decoded answer along path and answers the value there.
func jsonPathAny(t *testing.T, value map[string]any, path ...string) any {
	t.Helper()
	var here any = value
	for _, step := range path {
		object, ok := here.(map[string]any)
		if !ok {
			t.Fatalf("walk %v: %v is not an object", path, here)
		}
		here, ok = object[step]
		if !ok {
			return nil
		}
	}
	return here
}
