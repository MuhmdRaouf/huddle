package app

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

// testVersion is the version the tests pass to Run; the binary passes the one
// it was built with.
const testVersion = "dev"

// usageLine is the usage text an unknown command prints, naming the word the
// huddle binary runs kanban with.
const usageLine = "usage: huddle kanban <command> [flags]; run huddle kanban help for the list"

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
			code := Run(tt.args, &stdout, &stderr, testVersion)
			if code != tt.wantCode {
				t.Errorf("Run(%q) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("Run(%q) stdout = %q, want %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("Run(%q) stderr = %q, want %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

// TestHookWithoutABoardIsSilent proves the word's hook end to end: where no
// board resolves it exits 0 and prints nothing, because a session hook never
// fails a session.
func TestHookWithoutABoardIsSilent(t *testing.T) {
	t.Chdir(t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"hook", "session-start"}, &stdout, &stderr, testVersion); code != 0 {
		t.Fatalf("Run(hook session-start) = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Errorf("hook output = %q / %q, want silence", stdout.String(), stderr.String())
	}
}

// TestRunVersionIsTheOneTheCallerPasses pins the version flow: Run prints the
// version its caller hands it, never one of its own.
func TestRunVersionIsTheOneTheCallerPasses(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--version"}, &stdout, &stderr, "9.9.9"); code != 0 {
		t.Fatalf("Run(--version) = %d, want 0", code)
	}
	if stdout.String() != "kanban 9.9.9\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "kanban 9.9.9\n")
	}
}

// TestHelpPrintsTheCommandList proves help goes through the table and prints
// the cli's command list.
func TestHelpPrintsTheCommandList(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr, testVersion); code != 0 {
		t.Fatalf("Run(help) = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage: kanban <command> [flags]") {
		t.Errorf("stdout = %q, want the command list", stdout.String())
	}
}

// TestEntriesAreTheCLISurface pins the table to the command words the cli
// accepts, read from the cli itself so a command it gains is never left out of
// the word: every board command, the server entries and the help forms.
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
// table, so every key is proven to be reached through Run with the arguments
// after the command word and the version the caller passed.
func TestRunDispatchesThroughTheTable(t *testing.T) {
	original := entries
	t.Cleanup(func() { entries = original })
	for _, key := range keysOf(original) {
		var gotCtx context.Context
		var gotArgs []string
		var gotVersion string
		var gotStdout, gotStderr io.Writer
		entries = map[string]runner{
			key: func(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
				gotCtx, gotArgs, gotStdout, gotStderr, gotVersion = ctx, args, stdout, stderr, version
				io.WriteString(stdout, "out\n")
				io.WriteString(stderr, "err\n")
				return 7
			},
		}
		var stdout, stderr bytes.Buffer
		code := Run([]string{key, "--board", "/tmp/x"}, &stdout, &stderr, testVersion)
		if code != 7 {
			t.Errorf("Run(%q) = %d, want the entry's 7", []string{key}, code)
		}
		if gotCtx == nil {
			t.Errorf("Run(%q) passed a nil context", []string{key})
		}
		if want := []string{"--board", "/tmp/x"}; !reflect.DeepEqual(gotArgs, want) {
			t.Errorf("entry args = %q, want %q", gotArgs, want)
		}
		if gotVersion != testVersion {
			t.Errorf("entry version = %q, want %q", gotVersion, testVersion)
		}
		if gotStdout != io.Writer(&stdout) || gotStderr != io.Writer(&stderr) {
			t.Errorf("Run(%q) did not pass its writers to the entry", []string{key})
		}
		if stdout.String() != "out\n" || stderr.String() != "err\n" {
			t.Errorf("entry output = (%q, %q), want it on Run's writers", stdout.String(), stderr.String())
		}
	}
}

// TestRunMovesALeadingBoardFlag runs the real command line against a throwaway
// board with --board on either side of the command word.
func TestRunMovesALeadingBoardFlag(t *testing.T) {
	board := filepath.Join(t.TempDir(), ".agents", "kanban")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"init", "--board", board}, &stdout, &stderr, testVersion); code != 0 {
		t.Fatalf("init = %d, stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--board", board, "epoch"}, &stdout, &stderr, testVersion); code != 0 {
		t.Fatalf("epoch with a leading --board = %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "1\n" {
		t.Errorf("stdout = %q, want 1", stdout.String())
	}
	stdout.Reset()
	if code := Run([]string{"--board=" + board, "epoch"}, &stdout, &stderr, testVersion); code != 0 {
		t.Fatalf("epoch with a leading --board= = %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != "1\n" {
		t.Errorf("stdout = %q, want 1", stdout.String())
	}
}

// keysOf returns the table's keys in a stable order.
func keysOf(table map[string]runner) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
