package hooks

// The report hook's corners: an event without its session, a world with no home and no outbox,
// a machine name the config cannot give, a project that falls back to the working directory,
// and the outbox's own refusals. The process walk reads the kernel's own answers for pids this
// test owns.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

func TestTheReportHookSkipsAnEventWithoutItsSession(t *testing.T) {
	reg, url := startRegistry(t)
	home := reportHome(t)
	input := fmt.Sprintf(`{"hook_event_name":"Stop","cwd":%q}`, t.TempDir())
	stdout, _ := runReportHook(t, input, home, map[string]string{"HUDDLE_URL": url})
	if stdout != "" {
		t.Errorf("the hook printed %q, want silence", stdout)
	}
	if took := reg.took(); len(took) != 0 {
		t.Errorf("the registry took %d POSTs for a session-less event, want none", len(took))
	}
	if raw, err := os.ReadFile(filepath.Join(home, "outbox.jsonl")); err == nil && len(raw) != 0 {
		t.Errorf("the outbox grew (%s), want it untouched", raw)
	}
}

func TestTheReportHookWithNoHomeNamesNoMachineAndKeepsNoOutbox(t *testing.T) {
	reg, url := startRegistry(t)
	home := reportHome(t)
	// every variable a home resolves from is empty: the hook cannot name a machine or keep an
	// outbox, and the report still lands
	stdout, _ := runReportHook(t, reportFixture(t, "stop", writeTranscript(t), t.TempDir()), home,
		map[string]string{"HUDDLE_URL": url, "HUDDLE_HOME": "", "XDG_CONFIG_HOME": "", "HOME": ""})
	if stdout != "" {
		t.Errorf("the hook printed %q, want silence", stdout)
	}
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want the one report", len(took))
	}
	if machine := field(took[0].Body, "machine"); machine != "" {
		t.Errorf("machine = %q, want it empty with no home to read", machine)
	}
}

func TestTheReportHookReadsTheMachineFromTheConfigAlone(t *testing.T) {
	reg, url := startRegistry(t)
	home := t.TempDir()
	// a config that cannot be read at all (a directory where the file belongs) leaves the
	// machine unnamed; a torn file would fall back to the machine's own name
	if err := os.MkdirAll(filepath.Join(home, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	runReportHook(t, reportFixture(t, "stop", writeTranscript(t), t.TempDir()), home,
		map[string]string{"HUDDLE_URL": url})
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	if machine := field(took[0].Body, "machine"); machine != "" {
		t.Errorf("machine = %q, want it empty when the config cannot be read", machine)
	}
}

func TestTheReportHookWithoutACwdStillNamesItsProject(t *testing.T) {
	reg, url := startRegistry(t)
	home := reportHome(t)
	// no cwd in the input: the hook asks its own working directory, and the report carries a
	// project either way — git's toplevel when git answers inside its budget, the directory
	// itself when it does not
	runReportHook(t, `{"session_id":"s-wd","hook_event_name":"Stop","transcript_path":""}`, home,
		map[string]string{"HUDDLE_URL": url})
	took := reg.took()
	if len(took) != 1 {
		t.Fatalf("the registry took %d POSTs, want 1", len(took))
	}
	if project := field(took[0].Body, "project"); project == "" {
		t.Error("project = empty, want the hook's working directory or its repository")
	}
}

func TestSessionPidEndsAtAnUnreadableParentAndAtTwelveShells(t *testing.T) {
	// a chain whose parent cannot be read answers no session
	command := func(int) (string, bool) { return "zsh -i", true }
	parent := func(int) (int, bool) { return 0, false }
	if got := sessionPid(30, parent, command); got != 0 {
		t.Errorf("sessionPid of a shell with no readable parent = %d, want 0", got)
	}
	// twelve shells in a row: the walk gives up rather than run for ever
	count := 0
	climbing := func(pid int) (int, bool) { count++; return pid + 1, true }
	if got := sessionPid(1, climbing, command); got != 0 {
		t.Errorf("sessionPid of twelve shells = %d, want 0", got)
	}
	if count != 12 {
		t.Errorf("the walk climbed %d shells, want 12 and no more", count)
	}
	// a command line with no words at all is hook chain too
	if !hookChainLine("   ") {
		t.Error(`hookChainLine("   ") = false, want it treated as the chain`)
	}
}

func TestTheRealProcessWalkNamesTheParentAndRefusesTheUnknown(t *testing.T) {
	command, ok := procCommandOf(os.Getpid())
	if !ok || !strings.Contains(command, os.Args[0]) {
		t.Errorf("procCommandOf(this process) = (%q, %v), want this test binary", command, ok)
	}
	if _, ok := procCommandOf(1 << 30); ok {
		t.Error("procCommandOf of a pid nothing owns answered ok")
	}
	parent, ok := procParentOf(os.Getppid())
	if !ok || parent <= 0 {
		t.Errorf("procParentOf(this process's parent) = (%d, %v), want a parent", parent, ok)
	}
	// the first process has no parent: the walk answers not ok rather than a number
	if got, ok := procParentOf(1); ok || got != 0 {
		t.Errorf("procParentOf(1) = (%d, %v), want 0 not ok", got, ok)
	}
	if _, ok := procParentOf(1 << 30); ok {
		t.Error("procParentOf of a pid nothing owns answered ok")
	}
}

func TestEnvironOfAWorldWithoutItsOwnReadsTheProcess(t *testing.T) {
	if len(environOf(nil)) == 0 {
		t.Error("environOf(nil) = empty, want the process's environment")
	}
	if len(environOf(&cli.World{})) == 0 {
		t.Error("environOf of a world without Environ = empty, want the process's environment")
	}
}

func TestTheOutboxRefusesTheImpossiblePlaces(t *testing.T) {
	// a place under a file cannot be made
	underFile := filepath.Join(t.TempDir(), "a file")
	if err := os.WriteFile(underFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	appendOutbox(context.Background(), nil, filepath.Join(underFile, "outbox.jsonl"), "line")
	writeOutbox(filepath.Join(underFile, "outbox.jsonl"), []string{"line"})
	if body, err := os.ReadFile(underFile); err != nil || string(body) != "x" {
		t.Fatalf("the file was replaced by a directory (%q, %v), want it untouched", body, err)
	}
	// an outbox that is a directory cannot be appended to
	dirBox := filepath.Join(t.TempDir(), "outbox.jsonl")
	if err := os.MkdirAll(dirBox, 0o700); err != nil {
		t.Fatal(err)
	}
	appendOutbox(context.Background(), nil, dirBox, "line")
	// a replacement whose temp file cannot be written leaves the old lines standing
	home := t.TempDir()
	outbox := filepath.Join(home, "outbox.jsonl")
	if err := os.WriteFile(outbox, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outbox+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outbox + ".tmp") })
	writeOutbox(outbox, []string{"new"})
	if raw, err := os.ReadFile(outbox); err != nil || string(raw) != "kept\n" {
		t.Errorf("the outbox = (%q, %v), want the old line kept", raw, err)
	}
	// a replacement that cannot land removes its temp file and keeps the target
	target := filepath.Join(t.TempDir(), "outbox.jsonl")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "inside"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeOutbox(target, []string{"new"})
	if _, err := os.Stat(target + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("the temp file stayed: %v", err)
	}
}

func TestTheOutboxPiecesAnswerNothingForNothing(t *testing.T) {
	if lines := readOutbox(""); lines != nil {
		t.Errorf("readOutbox of no path = %v, want nil", lines)
	}
	writeOutbox("", []string{"line"})
	appendOutbox(context.Background(), nil, "", "line")
	if lines := readOutbox(filepath.Join(t.TempDir(), "missing.jsonl")); len(lines) != 0 {
		t.Errorf("readOutbox of a missing file = %v, want none", lines)
	}
	capOutbox(context.Background(), nil, filepath.Join(t.TempDir(), "missing.jsonl"))
}

func TestTheOutboxRoundTripsItsLines(t *testing.T) {
	home := t.TempDir()
	outbox := filepath.Join(home, "outbox.jsonl")
	appendOutbox(context.Background(), nil, outbox, "first")
	appendOutbox(context.Background(), nil, outbox, "second")
	lines := readOutbox(outbox)
	if len(lines) != 2 || lines[0] != "first" || lines[1] != "second" {
		t.Fatalf("readOutbox = %v, want the two lines in order", lines)
	}
	writeOutbox(outbox, nil)
	if raw, err := os.ReadFile(outbox); err != nil || len(raw) != 0 {
		t.Errorf("the outbox after an empty replace = (%q, %v), want it empty", raw, err)
	}
}
