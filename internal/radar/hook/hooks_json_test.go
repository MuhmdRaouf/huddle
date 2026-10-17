// The hooks.json cases. hooks.json is
// load-bearing: Claude Code runs it in every session, and a malformed file (hook objects
// listed directly under an event instead of matcher groups) makes the plugin's hooks silently
// not run — or worse, a bad entry can break the session. The one file carries all three parts'
// hooks; these cases hold radar's share of it.
package hook_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// hooksFile is the shape of plugin/hooks/hooks.json.
type hooksFile struct {
	Hooks map[string][]hooksGroup `json:"hooks"`
}

type hooksGroup struct {
	Matcher string           `json:"matcher"`
	Hooks   []hooksEntryJSON `json:"hooks"`
}

type hooksEntryJSON struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Timeout *float64 `json:"timeout"`
}

// loadHooksJSON reads the plugin's hooks.json, the file Claude Code loads.
func loadHooksJSON(t *testing.T) hooksFile {
	t.Helper()
	path := filepath.Join("..", "..", "..", "plugin", "hooks", "hooks.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading hooks.json: %v", err)
	}
	var file hooksFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parsing hooks.json: %v", err)
	}
	return file
}

// radarHooks answers the one group per event whose command is radar's launcher line.
func radarHooks(t *testing.T) map[string]hooksGroup {
	t.Helper()
	out := map[string]hooksGroup{}
	for event, groups := range loadHooksJSON(t).Hooks {
		for _, group := range groups {
			for _, entry := range group.Hooks {
				if entry.Command == launcherCommand {
					if _, again := out[event]; again {
						t.Fatalf("%s runs radar's hook twice", event)
					}
					out[event] = group
				}
			}
		}
	}
	return out
}

// hookTimeoutMs is the timeout hooks.json gives radar's hook for an event, in ms: Claude Code's
// budget for it.
func hookTimeoutMs(t *testing.T, event string) int {
	t.Helper()
	group, ok := radarHooks(t)[event]
	if !ok || len(group.Hooks) == 0 || group.Hooks[0].Timeout == nil {
		t.Fatalf("hooks.json gives %s no radar hook timeout", event)
	}
	return int(*group.Hooks[0].Timeout * 1000)
}

// launcherCommand is the one command every radar hook runs: the committed launcher's radar word,
// which starts the huddle binary (downloading it on first use) and never blocks or fails Claude
// Code.
const launcherCommand = `sh "${CLAUDE_PLUGIN_ROOT}/dist/run" radar hook`

func TestHooksJSON(t *testing.T) {
	// Every event Claude Code fires that radar observes.
	expectedEvents := []string{
		"SessionStart", "SessionEnd", "UserPromptSubmit", "SubagentStart", "SubagentStop",
		"Stop", "PreCompact", "Notification",
	}
	// Events fired per tool call: a hook on them would start a process for every tool Claude
	// runs.
	toolEvents := []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"}

	t.Run("covers every radar event exactly once", func(t *testing.T) {
		mine := radarHooks(t)
		var missing []string
		for _, event := range expectedEvents {
			if _, ok := mine[event]; !ok {
				missing = append(missing, event)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("hooks.json gives radar no hook on %v", missing)
		}
		if got := len(mine); got != len(expectedEvents) {
			t.Fatalf("radar hooks %d events, want %d", got, len(expectedEvents))
		}
	})

	t.Run("lists matcher groups of command hooks for every event, never bare hook objects", func(t *testing.T) {
		for event, groups := range loadHooksJSON(t).Hooks {
			if len(groups) == 0 {
				t.Errorf("%s: at least one group", event)
			}
			for _, group := range groups {
				if group.Hooks == nil {
					t.Errorf("%s: group without hooks[]", event)
					continue
				}
				for _, entry := range group.Hooks {
					if entry.Type != "command" {
						t.Errorf("%s: hook type = %q, want command", event, entry.Type)
					}
					if entry.Command == "" {
						t.Errorf("%s: hook needs a command", event)
					}
					if entry.Timeout == nil {
						t.Errorf("%s: timeout = %v, want a number", event, entry.Timeout)
					}
				}
			}
		}
	})

	t.Run("keeps every radar hook inside a 10 s budget", func(t *testing.T) {
		for event, group := range radarHooks(t) {
			for _, entry := range group.Hooks {
				if entry.Timeout == nil || *entry.Timeout > 10 {
					t.Errorf("%s: radar's timeout = %v, want a number <= 10", event, entry.Timeout)
				}
			}
		}
	})

	t.Run("never runs radar per tool call: tool calls come from the transcripts", func(t *testing.T) {
		mine := radarHooks(t)
		for _, event := range toolEvents {
			if _, ok := mine[event]; ok {
				t.Errorf("%s: a radar hook on a tool event starts a process per tool call", event)
			}
		}
	})

	t.Run("runs the launcher's radar word", func(t *testing.T) {
		for event, group := range radarHooks(t) {
			for _, entry := range group.Hooks {
				if entry.Command != launcherCommand {
					t.Errorf("%s: command = %q, want the launcher's radar word", event, entry.Command)
				}
			}
		}
	})
}
