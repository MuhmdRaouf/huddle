package main

// The tips part, end to end: the merged user-prompt-submit hook of the real binary asks the hub
// for this session's tips, one tip lands in the session's context, and it lands once.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/insights"
)

func TestTheUserPromptSubmitHookBringsOneTipEndToEnd(t *testing.T) {
	var mu sync.Mutex
	insightsAsks := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Path == "/api/insights" {
			insightsAsks++
		}
		mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/api/insights" {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"tips":[{"id":"t-e2e","sentence":"one narrower run is faster"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(hub.Close)
	home := t.TempDir()
	env := argvLayer(argvBase(t), map[string]string{"HUDDLE_URL": hub.URL, "HUDDLE_HOME": home})
	stdin := `{"session_id":"tips-e2e","hook_event_name":"UserPromptSubmit"}`

	// the switch is off, its default: the hook stays silent and asks the hub for nothing
	code, out, errOut := hooksRun(t, env, t.TempDir(), stdin, "hook", "user-prompt-submit")
	if code != 0 || out != "" || errOut != "" {
		t.Fatalf("with the switch off the hook answered (%d, %q, %q), want silence and 0", code, out, errOut)
	}
	mu.Lock()
	asks := insightsAsks
	mu.Unlock()
	if asks != 0 {
		t.Errorf("with the switch off the hook asked the hub %d times, want none", asks)
	}

	// the switch is on: one tip line in the merged context, and the ledger holds the tip
	if err := insights.WriteSettings(insights.SettingsFile(home), insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
		t.Fatalf("turn the switch on: %v", err)
	}
	code, out, errOut = hooksRun(t, env, t.TempDir(), stdin, "hook", "user-prompt-submit")
	if code != 0 || errOut != "" {
		t.Fatalf("with the switch on the hook answered (%d, %q), want 0 and a quiet stderr", code, errOut)
	}
	var merged struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &merged); err != nil {
		t.Fatalf("hook stdout is not one JSON object: %q (%v)", out, err)
	}
	if got := merged.HookSpecificOutput.AdditionalContext; got != "Huddle tip: one narrower run is faster" {
		t.Errorf("context = %q, want exactly one tip line", got)
	}
	ledger := filepath.Join(home, "seen", "tips-e2e.tips.json")
	raw, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatalf("read the seen ledger: %v", err)
	}
	var seen []string
	if err := json.Unmarshal(raw, &seen); err != nil || len(seen) != 1 || seen[0] != "t-e2e" {
		t.Errorf("the ledger holds %s (%v), want [t-e2e]", raw, err)
	}
	if info, err := os.Stat(ledger); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the ledger sits at %v (%v), want 0600", info, err)
	}

	// the next prompt stays silent: the tip was shown once
	code, out, errOut = hooksRun(t, env, t.TempDir(), stdin, "hook", "user-prompt-submit")
	if code != 0 || out != "" || errOut != "" {
		t.Errorf("the second prompt answered (%d, %q, %q), want silence and 0", code, out, errOut)
	}
}
