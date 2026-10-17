package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The session hook: one JSON line for a project with a board, silence for
// everything else. Every effect is the server fixture's recorded one; no row
// binds a port, starts a process or opens a browser.

// fillBoard writes one ticket per column and three in done, so the counts the
// hook line names stay readable: open counts every column but done.
func fillBoard(s *serverFixture) {
	s.fullCard("backlog", "B-1")
	s.fullCard("ready", "R-1")
	s.fullCard("ready", "R-2")
	s.fullCard("in-progress", "W-1")
	s.fullCard("review", "V-1")
	s.fullCard("review", "V-2")
	s.fullCard("testing", "T-1")
	s.fullCard("blocked", "K-1")
	s.fullCard("done", "D-1")
	s.fullCard("done", "D-2")
	s.fullCard("done", "D-3")
}

// hookAnswer is the whole stdout the hook owes for the given counts: one
// compact SessionStart document carrying the board line.
func hookAnswer(t *testing.T, open, inProgress, inReview int) string {
	t.Helper()
	line := fmt.Sprintf(
		"Kanban board: .agents/kanban (%d open, %d in progress, %d in review) live at %s. "+
			"Use the kanban skill to work it; move tickets only with the kanban command.",
		open, inProgress, inReview, "http://127.0.0.1:45678")
	return fmt.Sprintf(`{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":%s}}`+"\n",
		jsonString(t, line))
}

func TestHookSessionStart(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T, s *serverFixture)
		argv    []string
		code    int
		stdout  string
		stderr  string
		check   func(t *testing.T, s *serverFixture)
	}{
		{
			name: "a project with no board is silence",
			prepare: func(t *testing.T, s *serverFixture) {
				// the fixture's own cwd holds a board; the project dir Claude
				// Code names wins, and it has none
				s.env["CLAUDE_PROJECT_DIR"] = t.TempDir()
			},
			argv: []string{"hook", "session-start"},
			code: 0,
			check: func(t *testing.T, s *serverFixture) {
				if len(s.fetches) != 0 || len(s.spawns) != 0 {
					t.Errorf("fetches = %v, spawns = %v, want none", s.fetches, s.spawns)
				}
			},
		},
		{
			name: "a board answers one context line with the counts and the server url",
			prepare: func(t *testing.T, s *serverFixture) {
				s.recorded()
				fillBoard(s)
				// a subdirectory: resolution walks up from the project dir
				s.env["CLAUDE_PROJECT_DIR"] = filepath.Join(s.cwd, "pkg")
			},
			argv:   []string{"hook", "session-start"},
			code:   0,
			stdout: hookAnswer(t, 8, 1, 2),
			check: func(t *testing.T, s *serverFixture) {
				if len(s.spawns) != 0 {
					t.Errorf("a healthy server was spawned again: %v", s.spawns)
				}
				if len(s.browsers) != 0 {
					t.Errorf("the hook opened a browser: %v", s.browsers)
				}
			},
		},
		{
			name: "a board with no server starts one, then answers the same line",
			prepare: func(t *testing.T, s *serverFixture) {
				fillBoard(s)
				s.env["CLAUDE_PROJECT_DIR"] = s.cwd
			},
			argv:   []string{"hook", "session-start"},
			code:   0,
			stdout: hookAnswer(t, 8, 1, 2),
			check: func(t *testing.T, s *serverFixture) {
				if len(s.spawns) != 1 {
					t.Fatalf("spawns = %v, want one serve", s.spawns)
				}
				joined := strings.Join(s.spawns[0], " ")
				if !strings.Contains(joined, "serve") || !strings.Contains(joined, s.boardAbs) {
					t.Errorf("spawn args = %q, want serve for the board", s.spawns[0])
				}
			},
		},
		{
			name: "an empty board answers zero counts",
			prepare: func(t *testing.T, s *serverFixture) {
				s.recorded()
				s.env["CLAUDE_PROJECT_DIR"] = s.cwd
			},
			argv:   []string{"hook", "session-start"},
			code:   0,
			stdout: hookAnswer(t, 0, 0, 0),
		},
		{
			name: "a start that never answers is silence, not a failed session",
			prepare: func(t *testing.T, s *serverFixture) {
				s.healthOK(false)
				s.env["CLAUDE_PROJECT_DIR"] = s.cwd
			},
			argv: []string{"hook", "session-start"},
			code: 0,
			check: func(t *testing.T, s *serverFixture) {
				if len(s.spawns) != 1 {
					t.Errorf("spawns = %v, want one attempt", s.spawns)
				}
			},
		},
		{
			name:   "a wrong hook word is a usage error",
			argv:   []string{"hook", "cleanup"},
			code:   2,
			stderr: "kanban hook: cleanup is not a hook word; session-start is\nrun kanban hook --help\n",
		},
		{
			name:   "the hook word itself is required",
			argv:   []string{"hook"},
			code:   2,
			stderr: "kanban hook: session-start is required\nrun kanban hook --help\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServerFixture(t)
			if tt.prepare != nil {
				tt.prepare(t, s)
			}
			code, stdout, stderr := s.run(tt.argv...)
			if code != tt.code {
				t.Errorf("exit = %d, want %d (stdout %q, stderr %q)", code, tt.code, stdout, stderr)
			}
			if tt.stdout != "" && stdout != tt.stdout {
				t.Errorf("stdout = %q, want %q", stdout, tt.stdout)
			}
			if tt.stdout == "" && stdout != "" {
				t.Errorf("stdout = %q, want silence", stdout)
			}
			if tt.stderr != "" && stderr != tt.stderr {
				t.Errorf("stderr = %q, want %q", stderr, tt.stderr)
			}
			if tt.stderr == "" && stderr != "" {
				t.Errorf("stderr = %q, want silence", stderr)
			}
			if tt.check != nil {
				tt.check(t, s)
			}
		})
	}
}

// TestHookPutsThePluginBinOnPathOnce: the hook appends the plugin's bin to the
// session's env file once, however many times it runs, so `kanban` works in
// Bash and subagents.
func TestHookPutsThePluginBinOnPathOnce(t *testing.T) {
	dir := t.TempDir()
	s := newServerFixture(t)
	s.recorded()
	s.env["CLAUDE_PROJECT_DIR"] = s.cwd
	s.env["CLAUDE_ENV_FILE"] = filepath.Join(dir, "env")
	s.env["CLAUDE_PLUGIN_ROOT"] = filepath.Join(dir, "plugin")
	for range 2 {
		code, stdout, stderr := s.run("hook", "session-start")
		if code != 0 || stdout == "" || stderr != "" {
			t.Fatalf("hook = %d, stdout %q, stderr %q", code, stdout, stderr)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	want := "export PATH=\"" + filepath.Join(dir, "plugin", "bin") + ":$PATH\"\n"
	if string(raw) != want {
		t.Errorf("env file = %q, want %q", string(raw), want)
	}
}

// TestHookWithoutAPluginRootWritesNoEnvFile: the PATH line needs the plugin
// root Claude Code sets; without it nothing is written.
func TestHookWithoutAPluginRootWritesNoEnvFile(t *testing.T) {
	dir := t.TempDir()
	s := newServerFixture(t)
	s.recorded()
	s.env["CLAUDE_PROJECT_DIR"] = s.cwd
	s.env["CLAUDE_ENV_FILE"] = filepath.Join(dir, "env")
	if code, _, _ := s.run("hook", "session-start"); code != 0 {
		t.Fatalf("hook exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "env")); !os.IsNotExist(err) {
		t.Error("the env file exists without a plugin root")
	}
}
