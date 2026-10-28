package main

// The agents part end to end on the built binary: `huddle hook session-start` — the one merged
// process Claude Code starts for the event — writes one model agent file per Claude family into
// the temp config dir's agents/ folder, and a second run leaves every byte and mtime alone. A
// config dir whose agents entry is a regular file (the folder cannot exist) still exits 0. Every
// home is a temp one; nothing touches the real ~/.claude, ~/.agents or the daemons' loopback
// ports.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/agents"
)

// agentsEnvE2E lays one hermetic session-start world: temp homes everywhere and CLAUDE_CONFIG_DIR
// at a fresh temp dir whose agents/ folder starts absent.
func agentsEnvE2E(t *testing.T, root, claudeConfig string) map[string]string {
	t.Helper()
	return argvLayer(map[string]string{
		"PATH":                   fakeBrowserShim(t) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME":                   filepath.Join(root, "user"),
		"TMPDIR":                 root,
		"XDG_CONFIG_HOME":        filepath.Join(root, "xdg-config"),
		"XDG_STATE_HOME":         filepath.Join(root, "xdg-state"),
		"XDG_DATA_HOME":          filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":         filepath.Join(root, "xdg-cache"),
		"HUDDLE_KEYS":            "files",
		"RADAR_HOME":             filepath.Join(root, "radar"),
		"HUDDLE_HOME":            filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_CHANNEL":         "",
		"HUDDLE_AS":              "",
		"HUDDLE_URL":             "",
		"HUDDLE_AUTOSTART":       "0",
		"RADAR_AUTOSTART":        "0",
		"CLAUDE_CONFIG_DIR":      claudeConfig,
		"HUDDLE_ROUTER_HOME":     filepath.Join(root, "router-home"),
	}, nil)
}

// agentStats checks every wanted agent file against huddle's render and maps each name to its
// mtime, for the rerun comparison.
func agentStats(t *testing.T, folder string) map[string]string {
	t.Helper()
	models := agents.Newest(agents.ClaudeIDs())
	out := make(map[string]string, len(models))
	for _, model := range models {
		info, err := os.Stat(filepath.Join(folder, model.FileName()))
		if err != nil {
			t.Fatalf("stat %s: %v", model.FileName(), err)
		}
		raw, err := os.ReadFile(filepath.Join(folder, model.FileName()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, agents.Render(model)) {
			t.Errorf("%s = %q, want huddle's render", model.FileName(), raw)
		}
		out[model.FileName()] = info.ModTime().String()
	}
	return out
}

func TestAgentsHookE2E(t *testing.T) {
	root := t.TempDir()
	claudeConfig := filepath.Join(root, "claude-config")
	env := agentsEnvE2E(t, root, claudeConfig)
	folder := filepath.Join(claudeConfig, "agents")
	stdin := `{"source":"startup","session_id":"agents-hook-e2e"}`

	t.Run("a session start writes the model agent files and a rerun touches nothing", func(t *testing.T) {
		code, stdout, stderr := hooksRun(t, env, root, stdin, "hook", "session-start")
		if code != 0 {
			t.Fatalf("huddle hook session-start = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		before := agentStats(t, folder)
		if len(before) != 4 {
			t.Fatalf("agents folder holds %d files (%v), want 4", len(before), before)
		}

		code, stdout, stderr = hooksRun(t, env, root, stdin, "hook", "session-start")
		if code != 0 {
			t.Fatalf("second huddle hook session-start = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
		after := agentStats(t, folder)
		for name, mtimeBefore := range before {
			mtimeAfter, ok := after[name]
			if !ok {
				t.Errorf("%s vanished on the second run", name)
				continue
			}
			if mtimeBefore != mtimeAfter {
				t.Errorf("%s mtime changed on the second run: %s -> %s", name, mtimeBefore, mtimeAfter)
			}
		}
		if len(after) != len(before) {
			t.Errorf("agents folder went from %d files to %d", len(before), len(after))
		}
	})

	t.Run("an agents regular file in the config dir still exits 0", func(t *testing.T) {
		blocked := filepath.Join(root, "claude-config-blocked")
		if err := os.MkdirAll(blocked, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(blocked, "agents"), []byte("not a folder\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		blockedEnv := argvLayer(env, map[string]string{"CLAUDE_CONFIG_DIR": blocked})
		code, stdout, stderr := hooksRun(t, blockedEnv, root, stdin, "hook", "session-start")
		if code != 0 {
			t.Fatalf("huddle hook session-start = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
		}
	})
}
