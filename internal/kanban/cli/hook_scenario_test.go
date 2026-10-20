package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/kanbantab"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// The hook's line points at the huddle hub's board: the hub serves every board
// in process, so a session start starts nothing and names the link instead.

// hubDocument decodes the hook's one answer.
func hubDocument(t *testing.T, stdout string) string {
	t.Helper()
	var doc struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decode the hook's stdout %q: %v", stdout, err)
	}
	if doc.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", doc.HookSpecificOutput.HookEventName)
	}
	return doc.HookSpecificOutput.AdditionalContext
}

// gitScenarioRepo turns one existing directory into a git repository with an
// origin remote, its git reads hermetic.
func gitScenarioRepo(t *testing.T, dir, origin string) {
	t.Helper()
	run := func(args ...string) {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", dir)
	run("-C", dir, "remote", "add", "origin", origin)
}

// tabBoards mounts the huddle tab over one repo and answers the board keys it
// lists, the way the hub's /api/kanban/boards does.
func tabBoards(t *testing.T, repos ...string) []string {
	t.Helper()
	tab := kanbantab.Mount(context.Background(), kanbantab.Options{
		Roots:   func(context.Context) []string { return repos },
		Version: "test",
	})
	rec := httptest.NewRecorder()
	tab.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, kanbantab.Prefix+"/boards", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the tab's /boards = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var listed struct {
		Boards []struct {
			Key string `json:"key"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	keys := make([]string, 0, len(listed.Boards))
	for _, b := range listed.Boards {
		keys = append(keys, b.Key)
	}
	return keys
}

func TestASessionStartPointsAtTheHubsBoard(t *testing.T) {
	bdd.Feature(t, "a session start points at the hub's board", func(f *bdd.F) {
		f.Scenario("with the hub running, the line links the board in the hub and no server is started", func(s *bdd.S) {
			var (
				fix    *serverFixture
				link   string
				stdout string
			)
			s.Given("a temp project with a board and a hub answering the board's link", func(t *testing.T) {
				fix = newServerFixture(s.T)
				fillBoard(fix)
				fix.env["CLAUDE_PROJECT_DIR"] = fix.cwd
				link = "http://127.0.0.1:42101/#/kanban/board-key"
				fix.hubLinks["*"] = link
			})
			s.When("the hook runs", func(t *testing.T) {
				code, out, errOut := fix.run("hook", "session-start")
				if code != 0 || errOut != "" {
					t.Fatalf("the hook = %d, stderr %q, want exit 0 in silence on stderr", code, errOut)
				}
				stdout = out
			})
			s.Then("the line carries the hub's link", func(t *testing.T) {
				line := hubDocument(t, stdout)
				if !strings.Contains(line, " in the huddle hub at "+link+". ") {
					t.Errorf("the line = %q, want it to link %s", line, link)
				}
				if !strings.Contains(line, "(8 open, 1 in progress, 2 in review)") {
					t.Errorf("the line = %q, want the board's counts", line)
				}
			})
			s.And("no server was started", func(t *testing.T) {
				if len(fix.spawns) != 0 {
					t.Errorf("spawns = %v, want none", fix.spawns)
				}
				if len(fix.browsers) != 0 {
					t.Errorf("the hook opened a browser: %v", fix.browsers)
				}
			})
		})

		f.Scenario("with no hub, the line says where it will be and no server is started", func(s *bdd.S) {
			var (
				fix    *serverFixture
				stdout string
			)
			s.Given("a temp project with a board and no hub answering", func(t *testing.T) {
				fix = newServerFixture(s.T)
				fillBoard(fix)
				fix.env["CLAUDE_PROJECT_DIR"] = fix.cwd
			})
			s.When("the hook runs", func(t *testing.T) {
				code, out, errOut := fix.run("hook", "session-start")
				if code != 0 || errOut != "" {
					t.Fatalf("the hook = %d, stderr %q, want exit 0 in silence on stderr", code, errOut)
				}
				stdout = out
			})
			s.Then("the line names the counts and the hub that is not running", func(t *testing.T) {
				line := hubDocument(t, stdout)
				if !strings.Contains(line, "(8 open, 1 in progress, 2 in review)") {
					t.Errorf("the line = %q, want the board's counts", line)
				}
				if !strings.Contains(line, "Open it in the huddle hub once it runs (/huddle:setup).") {
					t.Errorf("the line = %q, want the way to the hub", line)
				}
				if strings.Contains(line, "http://") {
					t.Errorf("the line = %q, want no link while no hub answers", line)
				}
			})
			s.And("no server was started", func(t *testing.T) {
				if len(fix.spawns) != 0 {
					t.Errorf("spawns = %v, want none", fix.spawns)
				}
			})
		})

		f.Scenario("the hook's key is the key the hub lists the board under", func(s *bdd.S) {
			var (
				fix     *serverFixture
				tabKeys []string
				asked   string
			)
			s.T.Setenv("HOME", s.T.TempDir())
			s.T.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
			s.T.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			s.Given("a temp repo with an origin remote and a board, and the tab mounted over it", func(t *testing.T) {
				fix = newServerFixture(s.T)
				gitScenarioRepo(t, fix.cwd, "git@github.com:Example/demo.git")
				fillBoard(fix)
				fix.env["CLAUDE_PROJECT_DIR"] = fix.cwd
				fix.hubLinks["*"] = "http://127.0.0.1:42101/#/kanban/asked"
				tabKeys = tabBoards(t, fix.cwd)
				if len(tabKeys) != 1 {
					t.Fatalf("the tab listed %v, want the repo's one board", tabKeys)
				}
			})
			s.When("the hook runs", func(t *testing.T) {
				code, _, errOut := fix.run("hook", "session-start")
				if code != 0 || errOut != "" {
					t.Fatalf("the hook = %d, stderr %q", code, errOut)
				}
				if len(fix.hubAsked) != 1 {
					t.Fatalf("the hub was asked %v, want one key", fix.hubAsked)
				}
				asked = fix.hubAsked[0]
			})
			s.Then("the key asked is the key the tab lists", func(t *testing.T) {
				if asked != tabKeys[0] {
					t.Errorf("the hook asked for %q, the hub lists %q", asked, tabKeys[0])
				}
			})
		})
	})
}
