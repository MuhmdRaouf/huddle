package cli

import (
	"fmt"
	"os"
	"path/filepath"
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
// compact SessionStart document carrying the board line. A link is the board's
// page in the huddle hub; without one the line says where the board will be.
func hookAnswer(t *testing.T, open, inProgress, inReview int, link string) string {
	t.Helper()
	board := fmt.Sprintf(".agents/kanban (%d open, %d in progress, %d in review)", open, inProgress, inReview)
	line := "Kanban board: " + board + ". Open it in the huddle hub once it runs (/huddle:setup)."
	if link != "" {
		line = "Kanban board: " + board + " in the huddle hub at " + link + "."
	}
	line += " Use the kanban skill to work it; move tickets only with the kanban command."
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
				if len(s.fetches) != 0 {
					t.Errorf("fetches = %v, want none", s.fetches)
				}
			},
		},
		{
			name: "a board answers one context line with the counts and the hub's link",
			prepare: func(t *testing.T, s *serverFixture) {
				fillBoard(s)
				// a subdirectory: resolution walks up from the project dir
				s.env["CLAUDE_PROJECT_DIR"] = filepath.Join(s.cwd, "pkg")
				s.hubLinks["*"] = "http://127.0.0.1:42101/#/kanban/board-key"
			},
			argv:   []string{"hook", "session-start"},
			code:   0,
			stdout: hookAnswer(t, 8, 1, 2, "http://127.0.0.1:42101/#/kanban/board-key"),
			check: func(t *testing.T, s *serverFixture) {
				if len(s.ensures) != 0 {
					t.Errorf("the hook asked to start a hub: %v", s.ensures)
				}
				if len(s.browsers) != 0 {
					t.Errorf("the hook opened a browser: %v", s.browsers)
				}
			},
		},
		{
			name: "with no hub, the line names the counts and starts no server",
			prepare: func(t *testing.T, s *serverFixture) {
				fillBoard(s)
				s.env["CLAUDE_PROJECT_DIR"] = s.cwd
			},
			argv:   []string{"hook", "session-start"},
			code:   0,
			stdout: hookAnswer(t, 8, 1, 2, ""),
			check: func(t *testing.T, s *serverFixture) {
				if len(s.ensures) != 0 {
					t.Errorf("ensures = %v, want none: the hook starts nothing", s.ensures)
				}
			},
		},
		{
			name: "an empty board answers zero counts",
			prepare: func(t *testing.T, s *serverFixture) {
				s.env["CLAUDE_PROJECT_DIR"] = s.cwd
			},
			argv:   []string{"hook", "session-start"},
			code:   0,
			stdout: hookAnswer(t, 0, 0, 0, ""),
		},
		{
			name: "an unopenable board is silence, not a failed session",
			prepare: func(t *testing.T, s *serverFixture) {
				s.env["CLAUDE_PROJECT_DIR"] = s.cwd
				// a kanban.db that opens for no store: the hook loses one
				// line, the session keeps its start
				if err := os.WriteFile(filepath.Join(s.root, "kanban.db"), []byte("not a database"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			argv: []string{"hook", "session-start"},
			code: 0,
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
	s.env["CLAUDE_PROJECT_DIR"] = s.cwd
	s.env["CLAUDE_ENV_FILE"] = filepath.Join(dir, "env")
	if code, _, _ := s.run("hook", "session-start"); code != 0 {
		t.Fatalf("hook exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "env")); !os.IsNotExist(err) {
		t.Error("the env file exists without a plugin root")
	}
}
