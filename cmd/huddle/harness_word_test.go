package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHarnessWordRoundTripsThroughTheBinary runs the word the user runs: the binary installs
// its MCP server into a temp HOME's Gemini settings, lists it as installed, and removes it —
// the created file gone with it. No real harness directory is ever touched: the environment's
// HOME and XDG_CONFIG_HOME are temp dirs (aloneEnv).
func TestHarnessWordRoundTripsThroughTheBinary(t *testing.T) {
	env := aloneEnv(t)
	home := env["HOME"]
	code, out, errOut := aloneExec(t, env, "harness", "install", "gemini")
	if code != 0 {
		t.Fatalf("harness install gemini = %d, stdout %q, stderr %q", code, out, errOut)
	}
	settings := filepath.Join(home, ".gemini", "settings.json")
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("install wrote no settings: %v", err)
	}
	if !strings.Contains(string(raw), "HUDDLE_HARNESS") {
		t.Errorf("the entry names no harness: %s", raw)
	}
	code, out, errOut = aloneExec(t, env, "harness", "list")
	if code != 0 {
		t.Fatalf("harness list = %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "gemini") || !strings.Contains(out, "installed") {
		t.Errorf("list does not show gemini installed: %q", out)
	}
	code, _, errOut = aloneExec(t, env, "harness", "remove", "gemini")
	if code != 0 {
		t.Fatalf("harness remove gemini = %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("the created settings file is still there: %v", err)
	}
	code, _, errOut = aloneExec(t, env, "harness", "remove", "gemini")
	if code != 4 || !strings.Contains(errOut, "nothing to remove") {
		t.Errorf("a second remove = (%d, %q), want 4 and nothing-to-remove", code, errOut)
	}
	code, _, errOut = aloneExec(t, env, "harness", "install", "nosuch")
	if code != 4 || !strings.Contains(errOut, "unknown harness") {
		t.Errorf("harness install nosuch = (%d, %q), want 4 and unknown-harness", code, errOut)
	}
}

// TestHookExternalEntryRunsSilentAndZero proves the hidden word's wiring through the binary:
// whatever the flags and whatever stdin holds, the answer is exit 0 and silence — a hook
// never blocks the agent that ran it. The mapping itself is the hooks package's tests.
func TestHookExternalEntryRunsSilentAndZero(t *testing.T) {
	env := aloneEnv(t)
	cases := [][]string{
		{"hook", "external", "--harness", "gemini", "--event", "SessionStart"},
		{"hook", "external", "--harness", "notaharness", "--event", "SessionStart"},
		{"hook", "external"},
	}
	for _, args := range cases {
		code, out, errOut := aloneExec(t, env, args...)
		if code != 0 {
			t.Errorf("huddle %v = %d, want 0", args, code)
		}
		if out != "" || errOut != "" {
			t.Errorf("huddle %v printed %q / %q, want silence", args, out, errOut)
		}
	}
}

// TestExternalArgsReadsTheTwoFlags pins the flag read: the value after each flag, either
// order, and a valueless flag left empty.
func TestExternalArgsReadsTheTwoFlags(t *testing.T) {
	name, event := externalArgs([]string{"--harness", "codex", "--event", "PreToolUse"})
	if name != "codex" || event != "PreToolUse" {
		t.Errorf("got (%q, %q), want (codex, PreToolUse)", name, event)
	}
	name, event = externalArgs([]string{"--event", "Stop", "--harness", "crush"})
	if name != "crush" || event != "Stop" {
		t.Errorf("got (%q, %q), want (crush, Stop)", name, event)
	}
	name, event = externalArgs([]string{"--harness"})
	if name != "" || event != "" {
		t.Errorf("got (%q, %q), want both empty", name, event)
	}
}

// TestHarnessWordListShowsTheHooks installs codex and lists it as carrying the hooks, then
// takes both back and lists the harness as merely available again.
func TestHarnessWordListShowsTheHooks(t *testing.T) {
	env := aloneEnv(t)
	home := env["HOME"]
	if code, _, errOut := aloneExec(t, env, "harness", "install", "codex"); code != 0 {
		t.Fatalf("harness install codex = %d, stderr %q", code, errOut)
	}
	code, out, errOut := aloneExec(t, env, "harness", "list")
	if code != 0 {
		t.Fatalf("harness list = %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "installed+hooks") {
		t.Errorf("list does not show codex's hooks: %q", out)
	}
	if code, _, errOut := aloneExec(t, env, "harness", "remove", "codex"); code != 0 {
		t.Fatalf("harness remove codex = %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("the created config is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env["HUDDLE_HOME"], "harness")); !os.IsNotExist(err) {
		t.Errorf("codex has no shim, but a harness directory is still there: %v", err)
	}
}
