package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/cli"
)

// usageLine is the usage text the CLI prints for an unknown command.
const usageLine = "usage: kanban <command> [flags]; run kanban help for the list"

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"--version prints the version and exits 0", []string{"--version"}, 0, "kanban dev\n", ""},
		{"no args is an unknown command", nil, 2, "", "kanban: unknown command: (none)\n" + usageLine + "\n"},
		{"unknown subcommand", []string{"frobnicate"}, 2, "", "kanban: unknown command: frobnicate\n" + usageLine + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("run(%q) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("run(%q) stdout = %q, want %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("run(%q) stderr = %q, want %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

// TestHookWithoutABoardIsSilent proves the binary's hook end to end: where no
// board resolves it exits 0 and prints nothing, because a session hook never
// fails a session.
func TestHookWithoutABoardIsSilent(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"hook", "session-start"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(hook session-start) = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Errorf("hook output = %q / %q, want silence", stdout.String(), stderr.String())
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
	if stdout.String() != "kanban 9.9.9\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "kanban 9.9.9\n")
	}
}

// TestHelpPrintsTheCommandList proves help goes through the table and prints
// the cli's command list.
func TestHelpPrintsTheCommandList(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(help) = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage: kanban <command> [flags]") {
		t.Errorf("stdout = %q, want the command list", stdout.String())
	}
}

// TestEntriesAreTheCLISurface pins the table to the command words the cli
// accepts, read from the cli itself so a command it gains is never left out of
// the binary: every board command, the server entries and the help forms.
func TestEntriesAreTheCLISurface(t *testing.T) {
	want := append(cli.Words(), "help", "--help", "-h")
	for _, key := range want {
		if _, ok := entries[key]; !ok {
			t.Errorf("entries[%q] is missing", key)
		}
	}
	if len(entries) != len(want) {
		t.Errorf("entries has %d keys (%v), want %d (%v)", len(entries), keysOf(entries), len(want), want)
	}
}

// TestRunDispatchesThroughTheTable swaps the table for a recording one-key
// table, so every key is proven to be reached through run with the arguments
// after the command word.
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
		code := run([]string{key, "--board", "/tmp/x"}, &stdout, &stderr)
		if code != 7 {
			t.Errorf("run(%q) = %d, want the entry's 7", []string{key}, code)
		}
		if gotCtx == nil {
			t.Errorf("run(%q) passed a nil context", []string{key})
		}
		if want := []string{"--board", "/tmp/x"}; !reflect.DeepEqual(gotArgs, want) {
			t.Errorf("entry args = %q, want %q", gotArgs, want)
		}
		if gotStdout != io.Writer(&stdout) || gotStderr != io.Writer(&stderr) {
			t.Errorf("run(%q) did not pass its writers to the entry", []string{key})
		}
		if stdout.String() != "out\n" || stderr.String() != "err\n" {
			t.Errorf("entry output = (%q, %q), want it on run's writers", stdout.String(), stderr.String())
		}
	}
}

// TestRunMovesALeadingBoardFlag runs the real command line against a throwaway
// board with --board on either side of the command word.
func TestRunMovesALeadingBoardFlag(t *testing.T) {
	board := filepath.Join(t.TempDir(), ".agents", "kanban")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"init", "--board", board}, &stdout, &stderr); code != 0 {
		t.Fatalf("init = %d, stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--board", board, "epoch"}, &stdout, &stderr); code != 0 {
		t.Fatalf("epoch with a leading --board = %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "1\n" {
		t.Errorf("stdout = %q, want 1", stdout.String())
	}
	stdout.Reset()
	if code := run([]string{"--board=" + board, "epoch"}, &stdout, &stderr); code != 0 {
		t.Fatalf("epoch with a leading --board= = %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "1\n" {
		t.Errorf("stdout = %q, want 1", stdout.String())
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
