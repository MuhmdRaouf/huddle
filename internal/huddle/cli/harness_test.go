package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// harnessWorld is one machine for the harness word: an empty PATH (no harness is installed
// here unless the test puts one there), temp homes for everything the word touches.
func harnessWorld(t *testing.T) (*World, string) {
	t.Helper()
	hud := t.TempDir()
	home := t.TempDir()
	w := world(t, t.TempDir(), map[string]string{
		"HOME":            home,
		"HUDDLE_HOME":     hud,
		"XDG_CONFIG_HOME": t.TempDir(),
		"PATH":            t.TempDir(),
	})
	return w, hud
}

// TestHarnessWordInstallsListsAndRemoves walks the word the way a user does: install wires the
// MCP server in, list says so, remove takes it back out.
func TestHarnessWordInstallsListsAndRemoves(t *testing.T) {
	w, hud := harnessWorld(t)
	home := w.Getenv("HOME")
	var stdout, stderr bytes.Buffer
	if code := Huddle(context.Background(), w, []string{"harness", "install", "gemini"}, &stdout, &stderr); code != 0 {
		t.Fatalf("harness install gemini = %d, stderr %q", code, stderr.String())
	}
	settings := filepath.Join(home, ".gemini", "settings.json")
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("install wrote no settings: %v", err)
	}
	bin, err := filepath.EvalSymlinks(huddleBin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), bin) {
		t.Errorf("the entry does not run this binary %s: %s", bin, raw)
	}
	if !strings.Contains(string(raw), `"HUDDLE_HARNESS":"gemini"`) && !strings.Contains(string(raw), "HUDDLE_HARNESS") {
		t.Errorf("the entry names no harness: %s", raw)
	}
	stdout.Reset()
	if code := Huddle(context.Background(), w, []string{"harness", "list"}, &stdout, &stderr); code != 0 {
		t.Fatalf("harness list = %d, stderr %q", code, stderr.String())
	}
	installed := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, "gemini") && strings.Contains(line, "installed") {
			installed = true
		}
	}
	if !installed {
		t.Errorf("list does not show gemini installed: %q", stdout.String())
	}
	stdout.Reset()
	if code := Huddle(context.Background(), w, []string{"harness", "remove", "gemini"}, &stdout, &stderr); code != 0 {
		t.Fatalf("harness remove gemini = %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("the created settings file is still there: %v", err)
	}
	// the ledger forgets the harness it no longer holds
	ledgerRaw, err := os.ReadFile(filepath.Join(hud, "harness-ledger.json"))
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if strings.Contains(string(ledgerRaw), "gemini") {
		t.Errorf("the ledger still records gemini: %s", ledgerRaw)
	}
}

// TestHarnessWordAnswersUsageAndUnknownNames pins the word's own refusals: no name, a bad
// subcommand, a name no harness answers to, and a remove with nothing of ours in the file.
func TestHarnessWordAnswersUsageAndUnknownNames(t *testing.T) {
	w, _ := harnessWorld(t)
	cases := []struct {
		argv []string
		code int
		want string
	}{
		{[]string{"harness", "install"}, 2, "harness install <name>"},
		{[]string{"harness", "frobnicate"}, 2, "harness list | install"},
		{[]string{"harness", "install", "nosuch"}, 4, "unknown harness"},
		{[]string{"harness", "remove", "nosuch"}, 4, "unknown harness"},
		{[]string{"harness", "remove", "crush"}, 4, "nothing to remove"},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		code := Huddle(context.Background(), w, tc.argv, &stdout, &stderr)
		if code != tc.code || !strings.Contains(stderr.String(), tc.want) {
			t.Errorf("huddle %v = (%d, %q), want %d and %q in stderr", tc.argv, code, stderr.String(), tc.code, tc.want)
		}
	}
}

// TestHarnessWordKeepsAUserEditedEntry pins the remove the card cares about through the CLI:
// an entry the user changed after install stays, and the command says so.
func TestHarnessWordKeepsAUserEditedEntry(t *testing.T) {
	w, _ := harnessWorld(t)
	home := w.Getenv("HOME")
	var stdout, stderr bytes.Buffer
	if code := Huddle(context.Background(), w, []string{"harness", "install", "gemini"}, &stdout, &stderr); code != 0 {
		t.Fatalf("install = %d, stderr %q", code, stderr.String())
	}
	settings := filepath.Join(home, ".gemini", "settings.json")
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	// the user's hand: a different binary runs the server now
	bin, err := filepath.EvalSymlinks(huddleBin)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), bin, "/elsewhere/huddle", 1)
	if edited == string(raw) {
		t.Fatalf("the edit changed nothing: %s", raw)
	}
	if err := os.WriteFile(settings, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := Huddle(context.Background(), w, []string{"harness", "remove", "gemini"}, &stdout, &stderr); code != 0 {
		t.Fatalf("remove = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "left in place") {
		t.Errorf("remove does not say the entry stayed: %q", stdout.String())
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != edited {
		t.Errorf("remove touched the edited entry:\nwant %q\ngot  %q", edited, after)
	}
}
