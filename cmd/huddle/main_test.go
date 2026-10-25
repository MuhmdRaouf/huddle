package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakeclaude"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"

	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// huddleBin is the binary TestMain builds once: the replay suite runs it as a child, with a
// server it starts from the same binary.
var huddleBin string

func TestMain(m *testing.M) {
	// the binaries these tests build and run inherit this, so none reaches a real trust store
	_ = os.Setenv(pki.StoreOffEnv, "1")
	dir, err := os.MkdirTemp("", "huddle-cmd-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	suffix := ""
	huddleBin = filepath.Join(dir, "huddle"+suffix)
	build := exec.Command("go", "build", "-o", huddleBin, "github.com/MuhmdRaouf/huddle/cmd/huddle")
	build.Dir = "../.." // the module root, from this package's directory
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building the huddle binary:", err)
		os.Exit(1)
	}
	// Run runs the suite and removes the fake claude binary's build dir (the provider e2e compiles
	// it on first use); this suite's own build dir goes after it: os.Exit would skip a defer.
	// Code these tests run in process reads os.Environ: the sandbox keeps it off the owner's home.
	unsandbox, err := isoenv.Sandbox()
	if err != nil {
		fmt.Fprintln(os.Stderr, "isoenv:", err)
		os.Exit(1)
	}
	code := fakeclaude.Run(m)
	unsandbox()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestRun(t *testing.T) {
	// hermetic: no real machine Huddle leaks into the dispatch's identity
	t.Setenv("HUDDLE_URL", "")
	t.Setenv("HUDDLE_CHANNEL", "")
	t.Setenv("HUDDLE_AS", "")
	t.Setenv("HUDDLE_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RADAR_HOME", t.TempDir())
	t.Setenv("HUDDLE_HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"--version prints the version and exits 0", []string{"--version"}, 0, "huddle dev\n", ""},
		{"no args runs help, the binary's entries above the CLI's list", nil, 0, "Huddle runs without Claude Code too — the same binary, the same home (~/.config/huddle):\n", ""},
		{"unknown subcommand is an operation the server refuses", []string{"frobnicate"}, 2, "", "huddle: not in a huddle: in a session that is in one, run /huddle:invite and paste its join line here (/huddle:join …); or run /huddle:setup to start one\n"},
		{"a flag and its value before the subcommand", []string{"--as", "boss", "map"}, 2, "", "huddle: not in a huddle: in a session that is in one, run /huddle:invite and paste its join line here (/huddle:join …); or run /huddle:setup to start one\n"},
		{"a lone flag leaves help as the subcommand", []string{"--pretty"}, 0, "Huddle runs without Claude Code too — the same binary, the same home (~/.config/huddle):\n", ""},
		{"-h is a subcommand, not a flag", []string{"-h"}, 0, "Huddle runs without Claude Code too — the same binary, the same home (~/.config/huddle):\n", ""},
		{"hook stop is silent and exits 0", []string{"hook", "stop"}, 0, "", ""},
		{"unknown hook is an operation too", []string{"hook", "bogus"}, 2, "", "huddle: not in a huddle: in a session that is in one, run /huddle:invite and paste its join line here (/huddle:join …); or run /huddle:setup to start one\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if tt.wantStdout != "" && !strings.HasPrefix(stdout.String(), tt.wantStdout) {
				t.Errorf("run(%q) stdout = %q, want it to start with %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("run(%q) stderr = %q, want %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRunVersionComesFromTheBuildFlag(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })
	version = "9.9.9"
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(--version) = %d, want 0", code)
	}
	if stdout.String() != "huddle 9.9.9\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "huddle 9.9.9\n")
	}
}

// TestSplitCommand pins the CLI's argument split: a --flag takes the next word as its value
// unless that is a flag too; the subcommand is the first plain word, "help" when there is none.
func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantCommand string
		wantRest    []string
	}{
		{"no args default to help", nil, "help", nil},
		{"the first plain word is the subcommand", []string{"wait", "--timeout", "5"}, "wait", []string{"--timeout", "5"}},
		{"a flag absorbs the next plain word as its value", []string{"--as", "boss", "map"}, "map", nil},
		{"a flag before another flag takes no value", []string{"--pretty", "--json"}, "help", nil},
		{"the plain word after a flag's flag is that flag's value", []string{"--pretty", "--json", "map"}, "help", nil},
		{"a lone flag leaves no subcommand", []string{"--state", "f"}, "help", nil},
		{"-h is a plain word, not a flag", []string{"-h"}, "-h", nil},
		{"plain words after the subcommand are its arguments", []string{"send", "boss", "hi"}, "send", []string{"boss", "hi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command, rest := splitCommand(tt.args)
			if command != tt.wantCommand {
				t.Errorf("splitCommand(%q) command = %q, want %q", tt.args, command, tt.wantCommand)
			}
			if !reflect.DeepEqual(rest, tt.wantRest) {
				t.Errorf("splitCommand(%q) rest = %q, want %q", tt.args, rest, tt.wantRest)
			}
		})
	}
}

// TestEntriesAreThePluginEntryPoints pins the table to the entry points the plugin runs itself:
// the MCP server, the HTTP server, the two decision hooks (their own lines) and one hook entry
// per event hooks.json serves. Every CLI word — a subcommand or an operation by name — goes to
// the CLI, which reads one argv.
func TestEntriesAreThePluginEntryPoints(t *testing.T) {
	want := []string{
		// The MCP server and the HTTP server.
		"mcp", "serve",
		// The two decision hooks, still their own lines.
		"hook approve", "hook permit",
		// One entry per event, one process per event.
		"hook session-start", "hook user-prompt-submit", "hook pre-tool-use", "hook post-tool-use",
		"hook notification", "hook permission-request", "hook stop", "hook subagent-start",
		"hook subagent-stop", "hook session-end", "hook pre-compact", "hook task-created",
		"hook teammate-idle",
	}
	if len(entries) != len(want) {
		t.Fatalf("entries has %d keys (%v), want %d", len(entries), keysOf(entries), len(want))
	}
	for _, key := range want {
		if _, ok := entries[key]; !ok {
			t.Errorf("entries[%q] is missing", key)
		}
	}
}

// TestTheHookEntriesStaySilent checks every hook entry hooks.json runs: silent and exit 0
// outside a huddle, because a hook never fails the session it runs in. The merged entries also
// run radar's and kanban's parts, whose homes point at temp dirs like every test's.
func TestTheHookEntriesStaySilent(t *testing.T) {
	t.Setenv("HUDDLE_URL", "")
	t.Setenv("HUDDLE_CHANNEL", "")
	t.Setenv("HUDDLE_AS", "")
	t.Setenv("HUDDLE_AUTOSTART", "0")
	t.Setenv("RADAR_AUTOSTART", "0")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RADAR_HOME", t.TempDir())
	t.Setenv("HUDDLE_HOME", t.TempDir())
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, key := range hookKeys() {
		t.Run(key, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(strings.Split(key, " "), &stdout, &stderr)
			if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("%s = (%d, %q, %q), want (0, silent, silent)", key, code, stdout.String(), stderr.String())
			}
		})
	}
}

// hookKeys lists the hook entries hooks.json runs.
func hookKeys() []string {
	return []string{
		"hook approve", "hook permit",
		"hook session-start", "hook user-prompt-submit", "hook pre-tool-use", "hook post-tool-use",
		"hook notification", "hook permission-request", "hook stop", "hook subagent-start",
		"hook subagent-stop", "hook session-end", "hook pre-compact",
	}
}

// TestRunDispatchesThroughTheTable swaps the table for a recording one-key table, so every key
// is proven to be reached through run with the arguments after the subcommand name.
func TestRunDispatchesThroughTheTable(t *testing.T) {
	original := entries
	t.Cleanup(func() { entries = original })
	for _, key := range keysOf(original) {
		var gotCtx context.Context
		var gotArgs []string
		var gotStdout, gotStderr io.Writer
		entries = map[string]entry{
			key: func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
				gotCtx, gotArgs, gotStdout, gotStderr = ctx, args, stdout, stderr
				io.WriteString(stdout, "out\n")
				io.WriteString(stderr, "err\n")
				return 7
			},
		}
		var stdout, stderr bytes.Buffer
		argv := append(strings.Split(key, " "), "--state", "f")
		code := run(argv, &stdout, &stderr)
		if code != 7 {
			t.Errorf("run(%q) = %d, want the entry's 7", argv, code)
		}
		if gotCtx == nil {
			t.Errorf("run(%q) passed a nil context", argv)
		}
		if want := []string{"--state", "f"}; !reflect.DeepEqual(gotArgs, want) {
			t.Errorf("entry args = %q, want %q", gotArgs, want)
		}
		if gotStdout != io.Writer(&stdout) || gotStderr != io.Writer(&stderr) {
			t.Errorf("run(%q) did not pass its writers to the entry", argv)
		}
		if stdout.String() != "out\n" || stderr.String() != "err\n" {
			t.Errorf("entry output = (%q, %q), want it on run's writers", stdout.String(), stderr.String())
		}
	}
}

// TestServeEntryRunsTheServerUntilItsContextEnds pins the serve entry: the server this binary
// runs in the foreground (the one up starts detached) comes up on a free port and answers.
func TestServeEntryRunsTheServerUntilItsContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	homeDir := filepath.Join(t.TempDir(), "home")
	t.Setenv("HUDDLE_HOME", homeDir)
	pinFreePort(t, homeDir)
	t.Setenv("HUDDLE_DATA", filepath.Join(t.TempDir(), "data"))
	var stdout, stderr bytes.Buffer
	if code := entries["serve"](ctx, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("serve = %d, %q %q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Huddle dev on http://127.0.0.1:") {
		t.Errorf("serve printed %q", stdout.String())
	}
}

// TestServeEntryFailsWhenThePortIsBeyondEveryOS pins the serve entry's failure shape: a server
// that cannot take its port says why on stderr and exits 1 (70000 is past every OS's port
// range, so no listener anywhere takes it).
func TestServeEntryFailsWhenThePortIsBeyondEveryOS(t *testing.T) {
	t.Setenv("HUDDLE_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("HUDDLE_DATA", filepath.Join(t.TempDir(), "data"))
	t.Setenv("PORT", "70000")
	var stdout, stderr bytes.Buffer
	if code := entries["serve"](context.Background(), nil, &stdout, &stderr); code != 1 {
		t.Fatalf("serve = %d %q %q, want 1", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "huddle: ") {
		t.Errorf("serve printed (%q, %q), want the reason on stderr only", stdout.String(), stderr.String())
	}
}

// keysOf returns the table's keys in a stable order.
func keysOf(table map[string]entry) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// recordedCase is one argv shape's recorded answer: what the CLI prints and exits with in
// the leg's world (no server, a dead url, or a live one), pinned by testdata/argv-shapes.json.
type recordedCase struct {
	Name   string   `json:"name"`
	Leg    string   `json:"leg"`
	Args   []string `json:"args"`
	Code   int      `json:"code"`
	Stdout string   `json:"stdout"`
	Stderr string   `json:"stderr"`
}

// argvBase is one throwaway world for a whole replay: none of the real machine's Huddle or
// Claude variables, temp homes, one state dir the joining session's credential lands in.
func argvBase(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"PATH":                   os.Getenv("PATH"),
		"HOME":                   t.TempDir(),
		"XDG_STATE_HOME":         t.TempDir(),
		"RADAR_HOME":             t.TempDir(),
		"HUDDLE_HOME":            t.TempDir(),
		"CLAUDE_PROJECT_DIR":     t.TempDir(),
		"HUDDLE_NO_PROJECT_CRED": "1",
	}
}

// argvLayer layers extra variables over a base.
func argvLayer(base map[string]string, extra map[string]string) map[string]string {
	t := map[string]string{}
	for key, value := range base {
		t[key] = value
	}
	for key, value := range extra {
		t[key] = value
	}
	return t
}

// argvEntries renders an env map for a child process.
func argvEntries(env map[string]string) []string {
	entries := make([]string, 0, len(env))
	for key, value := range env {
		entries = append(entries, key+"="+value)
	}
	return entries
}

// argvRun runs the binary once in env and dir, answering its exit code and both outputs.
func argvRun(t *testing.T, env map[string]string, dir string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(huddleBin, args...)
	cmd.Env = argvEntries(env)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if exited, ok := err.(*exec.ExitError); ok {
			return exited.ExitCode(), out.String(), errOut.String()
		}
		t.Fatalf("run huddle %v: %v", args, err)
	}
	return 0, out.String(), errOut.String()
}

// argvHealthy answers whether a Huddle answers at url.
func argvHealthy(url string) bool {
	resp, err := http.Get(url + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// argvServer starts the server from this binary on a free port, then stands the recorded live
// leg's world on it: the creator's invite, and alpha joined with it holding its own credential.
func argvServer(t *testing.T, base map[string]string, dir string) string {
	t.Helper()
	child := argvLayer(base, map[string]string{
		"PORT":         "0",
		"HUDDLE_DATA":  filepath.Join(t.TempDir(), "data"),
		"HUDDLE_HOME":  t.TempDir(),
		"HUDDLE_TOKEN": "fake-root-token",
	})
	cmd := exec.Command(huddleBin, "serve")
	cmd.Env = argvEntries(child)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the server: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	lines := make(chan string, 16)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	port := 0
	for line := range lines {
		if match := regexp.MustCompile(`on http://[\d.]+:(\d+)`).FindStringSubmatch(line); match != nil {
			port, _ = strconv.Atoi(match[1])
			break
		}
	}
	if port == 0 {
		t.Fatal("the server never said its port")
	}
	go func() {
		for range lines {
		}
	}() // the rest of the log goes nowhere
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if argvHealthy(url) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !argvHealthy(url) {
		t.Fatal("the server never answered /health")
	}
	owner := argvLayer(base, map[string]string{
		"HUDDLE_URL":     url,
		"HUDDLE_CHANNEL": "argv",
		"HUDDLE_AS":      "owner",
		"HUDDLE_TOKEN":   "fake-root-token",
	})
	code, out, errOut := argvRun(t, owner, dir, "token", "create", "--print-join-command")
	if code != 0 || errOut != "" {
		t.Fatalf("token create: %d %q", code, errOut)
	}
	match := regexp.MustCompile(`/huddle:join (\S+) --token (\S+)`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no join line in %q", out)
	}
	joiner := argvLayer(base, map[string]string{"HUDDLE_CHANNEL": "argv", "HUDDLE_SESSION": "alpha"})
	code, _, errOut = argvRun(t, joiner, dir, "join", match[1], "--token", match[2], "--as", "alpha", "--channel", "argv")
	if code != 0 {
		t.Fatalf("alpha joins: %d %q", code, errOut)
	}
	return url
}

// TestRunReplaysTheRecordedArgvAnswers runs the recorded argv shapes
// (testdata/argv-shapes.json) against this binary, byte for byte: a flag placed before the
// subcommand keeps the meaning it carries, wherever it stands.
func TestRunReplaysTheRecordedArgvAnswers(t *testing.T) {
	raw, err := os.ReadFile("testdata/argv-shapes.json")
	if err != nil {
		t.Fatalf("read the recorded answers: %v", err)
	}
	var recorded struct {
		Cases []recordedCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatalf("parse the recorded answers: %v", err)
	}
	if len(recorded.Cases) == 0 {
		t.Fatal("no recorded cases")
	}
	base := argvBase(t)
	dir := t.TempDir()
	live := ""
	legs := map[string]map[string]string{
		"none": base,
		"dead": argvLayer(base, map[string]string{"HUDDLE_URL": "http://127.0.0.1:1"}),
	}
	for _, c := range recorded.Cases {
		if c.Leg == "live" && live == "" {
			live = argvServer(t, base, dir)
		}
	}
	if live != "" {
		legs["live"] = argvLayer(base, map[string]string{
			"HUDDLE_URL":     live,
			"HUDDLE_CHANNEL": "argv",
			"HUDDLE_AS":      "alpha",
			"HUDDLE_SESSION": "alpha",
		})
	}
	for _, c := range recorded.Cases {
		t.Run(c.Name, func(t *testing.T) {
			env, ok := legs[c.Leg]
			if !ok {
				t.Fatalf("no leg %q", c.Leg)
			}
			code, out, errOut := argvRun(t, env, dir, c.Args...)
			if code != c.Code {
				t.Errorf("huddle %v = %d, want the recorded %d", c.Args, code, c.Code)
			}
			if out != c.Stdout {
				t.Errorf("huddle %v stdout = %q, want the recorded %q", c.Args, out, c.Stdout)
			}
			if errOut != c.Stderr {
				t.Errorf("huddle %v stderr = %q, want the recorded %q", c.Args, errOut, c.Stderr)
			}
		})
	}
}

// TestEveryEntryImportsTheLegacyHomesFirst pins the funnel: a hook entry (any entry) brings the
// project's legacy home into the user's home before it runs, records it in imports.json, and
// stays silent — the notice waits for the next session start.
func TestEveryEntryImportsTheLegacyHomesFirst(t *testing.T) {
	t.Setenv("HUDDLE_URL", "")
	t.Setenv("HUDDLE_CHANNEL", "")
	t.Setenv("HUDDLE_AS", "")
	t.Setenv("HUDDLE_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HUDDLE_HOME", home)
	repo := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", repo)
	source := filepath.Join(repo, ".agents", "huddle")
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(source, "data", "channels", "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Run(ctx, "INSERT INTO tasks (id, title, created_at) VALUES (?,?,?)", "t1", "the legacy task", "2026-10-16T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook", "stop"}, &stdout, &stderr); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("hook stop = (%d, %q, %q), want silent and 0", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "channels", "main.db")); err != nil {
		t.Fatalf("the legacy channel did not move in: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "imports.json"))
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if !strings.Contains(string(raw), `"source": `+strconv.Quote(source)) {
		t.Errorf("the ledger = %s, want it to record %s", raw, source)
	}
	if _, err := os.Stat(filepath.Join(source, "data", "channels", "main.db")); err != nil {
		t.Errorf("the legacy folder lost its channel: %v", err)
	}
}

// TestServeEntryRefreshesAStaleService pins the entry's side duty: a home whose service names
// another binary would have it re-rendered and reloaded before the server starts — through a
// PATH shim, so no real service manager is reached — but this process's own executable is a Go
// test binary, and a reload would start it as `serve --foreground`: the entry keeps the old file
// and the old record, never says a word, and its own work still succeeds.
func TestServeEntryRefreshesAStaleService(t *testing.T) {
	homeDir := filepath.Join(t.TempDir(), "home")
	userHome := t.TempDir()
	xdg := t.TempDir()
	t.Setenv("HUDDLE_HOME", homeDir)
	t.Setenv("HUDDLE_DATA", filepath.Join(t.TempDir(), "data"))
	t.Setenv("HOME", userHome)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	env := map[string]string{"HOME": userHome, "XDG_CONFIG_HOME": xdg}
	tool, _, _, unit := serviceTool(env)
	reloadWord := "daemon-reload"
	render := func(bin string) []byte { return outside.RenderSystemdUnit(bin) }
	if runtime.GOOS == "darwin" {
		reloadWord = "bootstrap gui/"
		render = func(bin string) []byte { return outside.RenderLaunchdPlist(bin, home.Home{Root: homeDir}.LogFile()) }
	}
	shim := t.TempDir()
	saw := filepath.Join(shim, "argv")
	failing := t.TempDir()
	if err := os.WriteFile(filepath.Join(shim, tool), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+saw+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failing, tool), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := func(t *testing.T) {
		t.Helper()
		if err := (home.Home{Root: homeDir}).Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		pinFreePort(t, homeDir)
		if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(unit, render("/opt/old/huddle"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(homeDir, "service.json"), []byte("{\n  \"unit\": "+strconv.Quote(unit)+
			",\n  \"bin\": \"/opt/old/huddle\",\n  \"at\": \"2026-10-16T12:00:00.000Z\"\n}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("a stale service is left alone when the running binary is a test binary", func(t *testing.T) {
		stale(t)
		t.Setenv("PATH", shim)
		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		var stdout, stderr bytes.Buffer
		if code := entries["serve"](ctx, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("serve = %d %q %q", code, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "Huddle dev on http://127.0.0.1:") {
			t.Errorf("serve printed %q", stdout.String())
		}
		body, err := os.ReadFile(unit)
		if err != nil {
			t.Fatal(err)
		}
		// the reload would start this test binary as `serve --foreground`: the entry keeps the
		// old file and the old record, and the manager never hears of it
		if !strings.Contains(string(body), "/opt/old/huddle") {
			t.Errorf("the unit was re-rendered for a test binary:\n%s", body)
		}
		if raw, err := os.ReadFile(filepath.Join(homeDir, "service.json")); err != nil || !strings.Contains(string(raw), "/opt/old/huddle") {
			t.Errorf("the ledger = (%q, %v), want the old binary kept", raw, err)
		}
		if raw, err := os.ReadFile(saw); err == nil && strings.Contains(string(raw), reloadWord) {
			t.Errorf("the shim saw %q, want no reload for a test binary", raw)
		}
	})

	t.Run("the refusal stays silent and keeps the old file", func(t *testing.T) {
		stale(t)
		t.Setenv("PATH", failing)
		ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
		defer cancel()
		var stdout, stderr bytes.Buffer
		if code := entries["serve"](ctx, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("serve = %d %q %q, want 0: an entry's side duty never fails it", code, stdout.String(), stderr.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("the refused refresh said %q, want silence", stderr.String())
		}
		body, err := os.ReadFile(unit)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "/opt/old/huddle") {
			t.Errorf("the old file was not kept:\n%s", body)
		}
		if raw, err := os.ReadFile(filepath.Join(homeDir, "service.json")); err != nil || !strings.Contains(string(raw), "/opt/old/huddle") {
			t.Errorf("the ledger = (%q, %v), want the old binary kept", raw, err)
		}
	})
}
