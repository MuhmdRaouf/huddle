package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// aloneEnv is the environment of a machine with no Claude session and no plugin: the home and
// every state dir are temp, and no Huddle variable names anything (empty counts as unset).
func aloneEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"PATH":                   os.Getenv("PATH"),
		"HOME":                   t.TempDir(),
		"HUDDLE_HOME":            t.TempDir(),
		"XDG_CONFIG_HOME":        t.TempDir(),
		"XDG_STATE_HOME":         t.TempDir(),
		"XDG_DATA_HOME":          t.TempDir(),
		"RADAR_HOME":             t.TempDir(),
		"CLAUDE_PROJECT_DIR":     t.TempDir(),
		"HUDDLE_URL":             "",
		"HUDDLE_CHANNEL":         "",
		"HUDDLE_AS":              "",
		"HUDDLE_TOKEN":           "",
		"HUDDLE_SESSION":         "",
		"CLAUDE_CODE_SESSION_ID": "",
		"CLAUDE_PID":             "",
		"HUDDLE_DATA":            "",
	}
}

// aloneExec runs the built binary once in env, answering its exit code and both outputs.
func aloneExec(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	return argvRun(t, env, t.TempDir(), args...)
}

// pinFreePort saves a port the OS just handed out in the home's config, so the server a test
// starts keeps that port: with no saved one the port choice walks 18860-18869, where test
// packages running in parallel race for the same port.
func pinFreePort(t *testing.T, homeRoot string) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if _, err := home.SavePort(home.Home{Root: homeRoot}, port, true); err != nil {
		t.Fatalf("save the port in %s: %v", homeRoot, err)
	}
	return port
}

// aloneUpLine reads the address `huddle up` printed.
func aloneUpLine(t *testing.T, out string) (string, int) {
	t.Helper()
	match := regexp.MustCompile(`huddle is up at (http://127\.0\.0\.1:(\d+))`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("up printed %q, want `huddle is up at http://127.0.0.1:<port>`", out)
	}
	port, _ := strconv.Atoi(match[2])
	return match[1], port
}

// aloneStop ends a server this test started: TERM, then KILL once the wait runs out.
func aloneStop(pid int) {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// aloneHealthy answers whether a server answers at url.
func aloneHealthy(url string) bool {
	resp, err := http.Get(url + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// TestAloneUpStatusOpenDown is the card's e2e: a temp home and no plugin start, describe, open
// and stop the server through the binary alone.
func TestAloneUpStatusOpenDown(t *testing.T) {
	env := aloneEnv(t)
	pinFreePort(t, env["HUDDLE_HOME"])

	code, out, errOut := aloneExec(t, env, "up")
	if code != 0 || errOut != "" {
		t.Fatalf("up = %d %q %q, want 0 and silence", code, out, errOut)
	}
	url, port := aloneUpLine(t, out)
	if !aloneHealthy(url) {
		t.Fatalf("nothing answers at %s after up", url)
	}
	raw, err := os.ReadFile(filepath.Join(env["HUDDLE_HOME"], "run", "huddle.pid"))
	if err != nil {
		t.Fatalf("no pid file: %v", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if pid <= 0 {
		t.Fatalf("pid file %q", raw)
	}
	t.Cleanup(func() { aloneStop(pid) })

	code, out, errOut = aloneExec(t, env, "status")
	if code != 0 || errOut != "" {
		t.Fatalf("status = %d %q %q, want 0 and silence", code, out, errOut)
	}
	facts := []string{
		"huddle is up at " + url,
		"role solo",
		"port " + strconv.Itoa(port),
		"sessions " + regexp.MustCompile(`sessions (\d+)`).FindStringSubmatch(out)[1],
		"pid " + strconv.Itoa(pid),
	}
	for _, fact := range facts {
		if !strings.Contains(out, fact) {
			t.Errorf("status said %q, want the fact %q", out, fact)
		}
	}

	code, out, errOut = aloneExec(t, env, "open", "--print")
	if code != 0 || errOut != "" {
		t.Fatalf("open --print = %d %q %q, want 0 and silence", code, out, errOut)
	}
	if match := regexp.MustCompile(`^http://127\.0\.0\.1:` + strconv.Itoa(port) + `/\?code=\S+\n$`).FindString(out); match == "" {
		t.Errorf("open --print printed %q, want the sign-in link at port %d", out, port)
	}

	code, out, errOut = aloneExec(t, env, "down")
	if code != 0 || errOut != "" {
		t.Fatalf("down = %d %q %q, want 0 and silence", code, out, errOut)
	}
	closed := time.Now().Add(2 * time.Second)
	for aloneHealthy(url) && time.Now().Before(closed) {
		time.Sleep(50 * time.Millisecond)
	}
	if aloneHealthy(url) {
		t.Fatalf("the server still answers at %s 2 s after down", url)
	}

	code, _, errOut = aloneExec(t, env, "status")
	if code != 6 {
		t.Errorf("status after down = %d, want 6", code)
	}
	if errOut != "huddle is not running\n" {
		t.Errorf("status after down said %q, want `huddle is not running`", errOut)
	}
}

// TestAloneStatusIsSixWhenNothingRuns pins the not-running answer: exit 6, one line.
func TestAloneStatusIsSixWhenNothingRuns(t *testing.T) {
	env := aloneEnv(t)
	code, out, errOut := aloneExec(t, env, "status")
	if code != 6 || out != "" {
		t.Errorf("status = (%d, %q), want 6 and a silent stdout", code, out)
	}
	if errOut != "huddle is not running\n" {
		t.Errorf("status said %q, want `huddle is not running`", errOut)
	}
}

// TestAloneUpRefusesABadPort keeps the CLI's refusal: a --port that is not a port number is a
// usage error, wherever the flag stands.
func TestAloneUpRefusesABadPort(t *testing.T) {
	env := aloneEnv(t)
	for _, args := range [][]string{{"up", "--port", "99999"}, {"--port", "99999", "up"}} {
		code, out, errOut := aloneExec(t, env, args...)
		if code != 2 || out != "" || errOut != "huddle: --port 99999: a port number\n" {
			t.Errorf("huddle %v = (%d, %q, %q), want 2 and the usage line", args, code, out, errOut)
		}
	}
}

// TestAloneHelpListsEveryEntry pins `huddle help`: one line per entry of both tables, and the
// channel CLI's list still below them.
func TestAloneHelpListsEveryEntry(t *testing.T) {
	env := aloneEnv(t)
	code, out, errOut := aloneExec(t, env, "help")
	if code != 0 || errOut != "" {
		t.Fatalf("help = %d %q %q, want 0 and silence", code, out, errOut)
	}
	for key := range entries {
		word := strings.Split(key, " ")[0]
		if !strings.Contains(out, "huddle "+word) {
			t.Errorf("help does not list the entry %q", key)
		}
	}
	for key := range aloneWords {
		if key == "-h" {
			continue // a synonym of help, spelled on help's own line
		}
		if !strings.Contains(out, "huddle "+key) {
			t.Errorf("help does not list the alone word %q", key)
		}
	}
	if !strings.Contains(out, "Background subagents have Bash") {
		t.Errorf("help lost the channel CLI's list")
	}
}

// TestTheSessionKeepsItsOwnWords pins the split: a session (a channel identity) keeps the CLI's
// words — its `status` is the channel's, not the server's.
func TestTheSessionKeepsItsOwnWords(t *testing.T) {
	env := aloneEnv(t)
	env["HUDDLE_CHANNEL"] = "team"
	env["HUDDLE_AS"] = "me"
	code, _, errOut := aloneExec(t, env, "status")
	if code != 5 || !strings.HasPrefix(errOut, "huddle: Huddle has no port here yet") {
		t.Errorf("a session's status = (%d, %q), want the CLI's 5 with its own message", code, errOut)
	}
}

// TestBrowserOpenRunsTheSystemOpener proves the opener choice per platform with a PATH shim, so
// no real browser opens.
func TestBrowserOpenRunsTheSystemOpener(t *testing.T) {
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	shim := t.TempDir()
	saw := filepath.Join(shim, "opened")
	script := filepath.Join(shim, opener)
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" > \""+saw+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim)
	if err := browserOpen(context.Background(), "http://127.0.0.1:199/x"); err != nil {
		t.Fatalf("browserOpen: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if raw, err := os.ReadFile(saw); err == nil {
			if string(raw) != "http://127.0.0.1:199/x\n" {
				t.Fatalf("the opener saw %q", raw)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the opener never ran")
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Setenv("PATH", "")
	if err := browserOpen(context.Background(), "http://127.0.0.1:199/x"); err == nil {
		t.Fatal("browserOpen with no opener on PATH answered nil, want the failure the link print falls back to")
	}
}

// TestAlonePairNeedsAServer pins the word's refusal: with nothing to point at (no server, no
// saved port, no hub name) it names what to run instead of printing a link nothing answers.
func TestAlonePairNeedsAServer(t *testing.T) {
	env := aloneEnv(t)
	code, out, errOut := aloneExec(t, env, "pair")
	if code != 6 || out != "" || !strings.Contains(errOut, "huddle up") {
		t.Errorf("pair = (%d, %q, %q), want 6 and the line that names huddle up", code, out, errOut)
	}
}

// TestAloneWordsAreTheirOwnTable pins the binary's own words: every one the alone table holds is
// dispatched by it, and none of them collides with a plugin entry.
func TestAloneWordsAreTheirOwnTable(t *testing.T) {
	want := []string{"cert", "devices", "down", "help", "-h", "open", "pair", "role", "service", "status", "trust", "untrust", "up"}
	if len(aloneWords) != len(want) {
		t.Fatalf("aloneWords has %v, want exactly %v", keysOfAlone(), want)
	}
	for _, word := range want {
		if _, ok := aloneWords[word]; !ok {
			t.Errorf("aloneWords[%q] is missing", word)
		}
		if _, clash := entries[word]; clash {
			t.Errorf("%q is in both tables", word)
		}
	}
}

// keysOfAlone lists the alone table's keys in a stable order.
func keysOfAlone() []string {
	keys := make([]string, 0, len(aloneWords))
	for key := range aloneWords {
		keys = append(keys, key)
	}
	return keys
}

// TestAloneEntryRunsFromRun proves the dispatch reaches the alone table through run with the
// whole argv, so a flag before the word keeps its meaning.
func TestAloneEntryRunsFromRun(t *testing.T) {
	t.Setenv("HUDDLE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("HUDDLE_URL", "")
	t.Setenv("HUDDLE_CHANNEL", "")
	t.Setenv("HUDDLE_AS", "")
	var gotArgv []string
	original := aloneWords["pair"]
	t.Cleanup(func() { aloneWords["pair"] = original })
	aloneWords["pair"] = func(ctx context.Context, w *cli.World, argv []string, stdout, stderr io.Writer) int {
		gotArgv = argv
		return 0
	}
	var stdout, stderr strings.Builder
	if code := run([]string{"pair", "--print"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run = %d", code)
	}
	if want := []string{"pair", "--print"}; !equalStrings(gotArgv, want) {
		t.Errorf("the alone entry saw %v, want the whole argv %v", gotArgv, want)
	}
}

// equalStrings answers whether two slices hold the same strings.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// serviceTool is the platform's service manager and the lines a load and an unload record, plus
// the unit file's place in the alone test's temp environment.
func serviceTool(env map[string]string) (tool, load, unload, unit string) {
	tool, load, unload = "systemctl", "--user enable --now huddle", "--user disable --now huddle"
	unit = filepath.Join(env["XDG_CONFIG_HOME"], "systemd", "user", "huddle.service")
	if runtime.GOOS == "darwin" {
		tool = "launchctl"
		unit = filepath.Join(env["HOME"], "Library", "LaunchAgents", "me.raouf.huddle.plist")
		load = fmt.Sprintf("bootstrap gui/%d %s", os.Getuid(), unit)
		unload = fmt.Sprintf("bootout gui/%d/me.raouf.huddle", os.Getuid())
	}
	return tool, load, unload, unit
}

// TestAloneServiceInstallsAndUninstalls is the service word's e2e: a PATH shim stands in for the
// system service manager, the unit file and the ledger land in the temp home, and both directions
// are idempotent.
func TestAloneServiceInstallsAndUninstalls(t *testing.T) {
	env := aloneEnv(t)
	tool, load, unload, unit := serviceTool(env)
	shim := t.TempDir()
	saw := filepath.Join(shim, "argv")
	if err := os.WriteFile(filepath.Join(shim, tool), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+saw+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env["PATH"] = shim

	for _, args := range [][]string{{"service"}, {"service", "bogus"}} {
		code, out, errOut := aloneExec(t, env, args...)
		if code != 2 || out != "" || errOut != "usage: huddle service install | uninstall\n" {
			t.Errorf("huddle %v = (%d, %q, %q), want 2 and the usage line", args, code, out, errOut)
		}
	}

	code, out, errOut := aloneExec(t, env, "service", "install")
	if code != 0 || errOut != "" {
		t.Fatalf("service install = %d %q %q, want 0 and silence", code, out, errOut)
	}
	if out != "huddle service installed: "+unit+"\n" {
		t.Errorf("service install said %q, want the unit's path", out)
	}
	body, err := os.ReadFile(unit)
	if err != nil {
		t.Fatalf("no unit file at %s: %v", unit, err)
	}
	if !strings.Contains(string(body), "<string>serve</string>") && !strings.Contains(string(body), " serve ") {
		t.Errorf("the unit does not run serve:\n%s", body)
	}
	if !strings.Contains(string(body), "--foreground") {
		t.Errorf("the unit does not run serve --foreground:\n%s", body)
	}
	// the unit names this very binary, so the service runs the huddle that installed it
	binOfUnit := regexp.MustCompile(`<string>(\S*/huddle)</string>`).FindStringSubmatch(string(body))
	if binOfUnit == nil {
		binOfUnit = regexp.MustCompile(`(?m)^ExecStart=(\S*/huddle) `).FindStringSubmatch(string(body))
	}
	if binOfUnit == nil {
		t.Fatalf("the unit names no binary:\n%s", body)
	}
	gotBin, wantBin := binOfUnit[1], huddleBin
	if resolved, err := filepath.EvalSymlinks(gotBin); err == nil {
		gotBin = resolved
	}
	if resolved, err := filepath.EvalSymlinks(wantBin); err == nil {
		wantBin = resolved
	}
	if gotBin != wantBin {
		t.Errorf("the unit runs %q, want the binary %q", gotBin, wantBin)
	}
	// the service's log lands in the home's run directory, so the install lays it out
	if _, err := os.Stat(filepath.Join(env["HUDDLE_HOME"], "run")); err != nil {
		t.Errorf("install left the home without its run directory: %v", err)
	}
	ledger := filepath.Join(env["HUDDLE_HOME"], "service.json")
	if raw, err := os.ReadFile(ledger); err != nil || !strings.Contains(string(raw), `"unit": "`+unit+`"`) {
		t.Errorf("the ledger = (%q, %v), want the unit %q recorded", raw, err, unit)
	}
	if raw, err := os.ReadFile(saw); err != nil || !strings.Contains(string(raw), load+"\n") {
		t.Errorf("the load the shim saw = (%q, %v), want %q", raw, err, load)
	}

	// a second install is a success, not an error
	if code, _, errOut := aloneExec(t, env, "service", "install"); code != 0 || errOut != "" {
		t.Errorf("the second install = (%d, %q), want a success", code, errOut)
	}

	code, out, errOut = aloneExec(t, env, "service", "uninstall")
	if code != 0 || errOut != "" {
		t.Fatalf("service uninstall = %d %q %q, want 0 and silence", code, out, errOut)
	}
	if out != "huddle service removed\n" {
		t.Errorf("service uninstall said %q", out)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("the unit file stayed behind: %v", err)
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Errorf("the ledger stayed behind: %v", err)
	}
	if raw, err := os.ReadFile(saw); err != nil || !strings.Contains(string(raw), unload+"\n") {
		t.Errorf("the unload the shim saw = (%q, %v), want %q", raw, err, unload)
	}

	// a second uninstall on nothing installed is a success too
	if code, _, errOut := aloneExec(t, env, "service", "uninstall"); code != 0 || errOut != "" {
		t.Errorf("the second uninstall = (%d, %q), want a success", code, errOut)
	}
}
