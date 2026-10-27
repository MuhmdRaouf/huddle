package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/insights"
)

// spinnerSettings is the part of Claude Code's settings.json the spinner tests read: huddle's
// member and one unrelated key that must survive it.
type spinnerSettings struct {
	Unrelated           string `json:"unrelated"`
	SpinnerTipsOverride *struct {
		TipsFile string `json:"tipsFile"`
	} `json:"spinnerTipsOverride"`
}

// readSpinnerSettings decodes the settings file at path, failing the test when it does not parse.
func readSpinnerSettings(t *testing.T, path string) spinnerSettings {
	t.Helper()
	var decoded spinnerSettings
	if err := json.Unmarshal([]byte(readFile(t, path)), &decoded); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return decoded
}

// spinnerWorld is one test machine: a temp user home, huddle home and Claude config dir with the
// seeded settings.json, none of the real session's environment. The env argument overrides the
// three homes; an empty value unsets the variable, the way world reads it.
func spinnerWorld(t *testing.T, env map[string]string) (*World, string) {
	t.Helper()
	vars := map[string]string{
		"HOME":              t.TempDir(),
		"HUDDLE_HOME":       t.TempDir(),
		"CLAUDE_CONFIG_DIR": t.TempDir(),
	}
	for key, value := range env {
		vars[key] = value
	}
	w := world(t, t.TempDir(), vars)
	path := filepath.Join(w.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
	writeFile(t, path, "{\n  \"unrelated\": \"keep\"\n}\n")
	return w, path
}

// TestEnsureSpinnerTipsAddsTheEntry: the ensure adds one spinnerTipsOverride member naming the
// owner's spinner-tips file and leaves every other byte where it was, and a second ensure finds
// the entry already ours and writes nothing.
func TestEnsureSpinnerTipsAddsTheEntry(t *testing.T) {
	w, path := spinnerWorld(t, nil)
	home := w.Getenv("HOME")
	ctx := context.Background()

	if err := EnsureSpinnerTips(ctx, w); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	decoded := readSpinnerSettings(t, path)
	want := insights.SpinnerTipsFile(home)
	if decoded.SpinnerTipsOverride == nil || decoded.SpinnerTipsOverride.TipsFile != want {
		t.Fatalf("settings after ensure hold %s, want spinnerTipsOverride with tipsFile %q", readFile(t, path), want)
	}
	if decoded.Unrelated != "keep" {
		t.Errorf("unrelated = %q, want keep", decoded.Unrelated)
	}

	before := readFile(t, path)
	if err := EnsureSpinnerTips(ctx, w); err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("the second ensure rewrote the file:\nbefore %s\nafter  %s", before, after)
	}
}

// TestEnsureSpinnerTipsWithoutAHomeDoesNothing: no user home is nothing to act on — the ensure
// answers nil and the settings file stays byte for byte.
func TestEnsureSpinnerTipsWithoutAHomeDoesNothing(t *testing.T) {
	w, path := spinnerWorld(t, map[string]string{"HOME": ""})
	before := readFile(t, path)

	if err := EnsureSpinnerTips(context.Background(), w); err != nil {
		t.Fatalf("ensure with no HOME = %v, want nil", err)
	}
	if after := readFile(t, path); after != before {
		t.Errorf("the ensure rewrote the file:\nbefore %s\nafter  %s", before, after)
	}
}

// TestEnsureSpinnerTipsRefusesAMalformedSettings: a settings.json that does not parse is refused
// before anything is written, and the file stays byte for byte.
func TestEnsureSpinnerTipsRefusesAMalformedSettings(t *testing.T) {
	w, path := spinnerWorld(t, nil)
	broken := "{\n  \"unrelated\":\n}\n"
	writeFile(t, path, broken)

	if err := EnsureSpinnerTips(context.Background(), w); err == nil {
		t.Fatal("ensure on a malformed settings.json = nil, want an error")
	}
	if after := readFile(t, path); after != broken {
		t.Errorf("the malformed file changed:\n%s", after)
	}
}
