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
