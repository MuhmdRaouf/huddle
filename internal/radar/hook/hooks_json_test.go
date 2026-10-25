// The hooks.json cases. hooks.json is
// load-bearing: Claude Code runs it in every session, and a malformed file (hook objects
// listed directly under an event instead of matcher groups) makes the plugin's hooks silently
// not run — or worse, a bad entry can break the session. One line per event runs one process
// for all three parts, radar's among them; these cases hold radar's share of it.
package hook_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// launcherLine is one event's hook line: the launcher's stable current binary, else the committed
// launcher, which builds or downloads that binary on first use and never blocks or fails Claude
// Code. The word names the event, and radar's hook is one part of that event's process.
func launcherLine(word string) string {
	return `h="${AGENTS_HUDDLE_HOME:-$HOME/.agents/huddle-bin}/current"; [ -x "$h" ] && exec "$h" hook ` + word +
		`; exec sh "${CLAUDE_PLUGIN_ROOT}/dist/run" hook ` + word
}

// eventWord is the Claude Code event's word in the launcher line: SessionStart -> session-start.
func eventWord(event string) string {
	var b strings.Builder
	for i, r := range event {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hookTimeoutMs is the timeout hooks.json grants the named event's line, in ms: Claude Code's
// budget for the whole event, radar's part inside it.
func hookTimeoutMs(t *testing.T, event string) int {
	t.Helper()
	group, ok := eventGroups(t)[event]
	if !ok || len(group.Hooks) == 0 || group.Hooks[0].Timeout == nil {
		t.Fatalf("hooks.json gives %s no hook timeout", event)
	}
	return int(*group.Hooks[0].Timeout * 1000)
}

// eventGroups answers the one group per event whose command is the event's launcher line.
func eventGroups(t *testing.T) map[string]hooksGroup {
	t.Helper()
	out := map[string]hooksGroup{}
	for event, groups := range loadHooksJSON(t).Hooks {
		word := eventWord(event)
		for _, group := range groups {
			for _, entry := range group.Hooks {
				if entry.Command == launcherLine(word) {
					if _, again := out[event]; again {
						t.Fatalf("%s runs the event line twice", event)
					}
					out[event] = group
				}
			}
		}
	}
	return out
}

func TestHooksJSON(t *testing.T) {
	// Every event Claude Code fires that radar observes.
	expectedEvents := []string{
		"SessionStart", "SessionEnd", "UserPromptSubmit", "SubagentStart", "SubagentStop",
		"Stop", "PreCompact", "Notification",
	}

	t.Run("covers every radar event exactly once", func(t *testing.T) {
		mine := eventGroups(t)
		var missing []string
		for _, event := range expectedEvents {
			if _, ok := mine[event]; !ok {
				missing = append(missing, event)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("hooks.json gives %v no event line", missing)
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

	t.Run("keeps every event line inside a 15 s budget", func(t *testing.T) {
		for event, group := range eventGroups(t) {
			for _, entry := range group.Hooks {
				if entry.Timeout == nil || *entry.Timeout > 15 {
					t.Errorf("%s: timeout = %v, want a number <= 15", event, entry.Timeout)
				}
			}
		}
	})

	t.Run("runs the launcher's hook word naming the event", func(t *testing.T) {
		for event, group := range eventGroups(t) {
			for _, entry := range group.Hooks {
				if entry.Command != launcherLine(eventWord(event)) {
					t.Errorf("%s: command = %q, want the launcher's %s line", event, entry.Command, eventWord(event))
				}
			}
		}
	})
}
