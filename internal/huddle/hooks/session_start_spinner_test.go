package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/insights"
)

// spinnerHookSettings is the part of Claude Code's settings.json this test reads.
type spinnerHookSettings struct {
	SpinnerTipsOverride *struct {
		TipsFile string `json:"tipsFile"`
	} `json:"spinnerTipsOverride"`
}

// TestSessionStartInstallsTheSpinnerTips: a session start outside any channel still adds
// huddle's spinnerTipsOverride member to Claude Code's settings.json, naming the owner's
// spinner-tips file, and says nothing; a second start writes nothing.
func TestSessionStartInstallsTheSpinnerTips(t *testing.T) {
	home, config := t.TempDir(), t.TempDir()
	env := map[string]string{
		"HOME":              home,
		"CLAUDE_CONFIG_DIR": config,
		"RADAR_HOME":        t.TempDir(),
		"HUDDLE_CHANNEL":    "",
		"HUDDLE_AS":         "",
	}
	settings := filepath.Join(config, "settings.json")

	stdout, _, _ := runHook(t, "session-start", `{"source":"startup"}`, env)
	if stdout != "" {
		t.Errorf("session-start printed %q, want silence", stdout)
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read %s: %v", settings, err)
	}
	var decoded spinnerHookSettings
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v", settings, err)
	}
	want := insights.SpinnerTipsFile(home)
	if decoded.SpinnerTipsOverride == nil || decoded.SpinnerTipsOverride.TipsFile != want {
		t.Fatalf("settings after the start = %s, want spinnerTipsOverride with tipsFile %q", raw, want)
	}

	if stdout, _, _ := runHook(t, "session-start", `{"source":"startup"}`, env); stdout != "" {
		t.Errorf("the second start printed %q, want silence", stdout)
	}
	again, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("read %s again: %v", settings, err)
	}
	if string(again) != string(raw) {
		t.Errorf("the second start rewrote the file:\nbefore %s\nafter  %s", raw, again)
	}
}
