package cli

// The hub path: the flow commands the huddle tab serves go to World.HubFlow
// first, its answer printed the way the direct path prints its own, and a hub
// that declines leaves the board's own files to answer.

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// hermeticGit points git's config reads at nothing: the key computation runs
// git against the test's own repos only.
func hermeticGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// hubCapture is a World.HubFlow fake: it records the one call it takes and
// answers what the test set.
type hubCapture struct {
	key     string
	req     board.FlowRequest
	calls   int
	lines   []string
	refusal string
	handled bool
}

func (h *hubCapture) flow(_ context.Context, key string, req board.FlowRequest) ([]string, string, bool) {
	h.calls++
	h.key, h.req = key, req
	return h.lines, h.refusal, h.handled
}

// runWorld runs one command line under a world of the caller's and answers the
// exit code, stdout and stderr.
func runWorld(t *testing.T, w World, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), w, argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestTheHubAnswersAFlowCommand(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	f.fullCard("ready", "FLW-1")
	hub := &hubCapture{lines: []string{"kanban: pulled FLW-1"}, handled: true}
	w := f.world()
	w.HubFlow = hub.flow
	code, out, errOut := runWorld(t, w, "pull", "--actor", "w1", "--id", "FLW-1")
	if code != 0 || errOut != "" {
		t.Fatalf("pull through the hub = %d, out %q, err %q, want 0 with no stderr", code, out, errOut)
	}
	equals(t, "the hub's lines", out, "kanban: pulled FLW-1\n")
	if hub.calls != 1 {
		t.Fatalf("the hub saw %d calls, want 1", hub.calls)
	}
	equals(t, "the key the hub got", hub.key, server.KeyOf(f.root))
	want := board.FlowRequest{Command: "pull", Actor: "w1", ID: "FLW-1"}
	if !reflect.DeepEqual(hub.req, want) {
		t.Errorf("the request the hub got = %+v, want %+v", hub.req, want)
	}
	// the direct path never ran: the local card stays where it was
	if f.exists("in-progress", "FLW-1") || !f.exists("ready", "FLW-1") {
		t.Error("the local card moved; the hub's answer should have ended the command")
	}
}

func TestTheHubsRefusalPrintsLikeTheDirectPaths(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	directCode, _, directErr := f.run("pull", "--actor", "w1", "--id", "NOPE")
	if directCode != 1 || directErr == "" {
		t.Fatalf("the direct refusal = %d %q, want 1 with a message", directCode, directErr)
	}
	hub := &hubCapture{refusal: strings.TrimRight(directErr, "\n"), handled: true}
	w := f.world()
	w.HubFlow = hub.flow
	code, out, errOut := runWorld(t, w, "pull", "--actor", "w1", "--id", "NOPE")
	if code != directCode {
		t.Fatalf("the hub's refusal exits %d, want %d", code, directCode)
	}
	equals(t, "the hub refusal's stderr", errOut, directErr)
	if out != "" {
		t.Errorf("the hub's refusal printed %q on stdout, want nothing", out)
	}
}

func TestADecliningHubLeavesTheDirectPath(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	f.fullCard("ready", "FLW-1")
	hub := &hubCapture{} // answers nothing
	w := f.world()
	w.HubFlow = hub.flow
	code, out, errOut := runWorld(t, w, "pull", "--actor", "w1", "--id", "FLW-1")
	if code != 0 || errOut != "" {
		t.Fatalf("pull after a decline = %d, out %q, err %q, want 0", code, out, errOut)
	}
	has(t, "the direct pull", out, "in-progress")
	if !f.exists("in-progress", "FLW-1") || f.exists("ready", "FLW-1") {
		t.Error("the card did not move on the local board")
	}
	if hub.calls != 1 {
		t.Fatalf("the hub saw %d calls, want 1", hub.calls)
	}
}

func TestTheHubSeesOnlyTheFlowCommands(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	hub := &hubCapture{lines: []string{"from the hub"}, handled: true}
	w := f.world()
	w.HubFlow = hub.flow
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte(fullBody("NEW-1")), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runWorld(t, w, "new", "NEW-1", "--actor", "lead",
		"--title", "one ticket", "--cluster", "kanban", "--summary", longSummary,
		"--body-file", body)
	if code != 0 || !f.exists("backlog", "NEW-1") {
		t.Fatalf("new = %d, want 0 with the ticket in backlog", code)
	}
	hasNot(t, "new's output", out, "from the hub")
	code, out, _ = runWorld(t, w, "list")
	if code != 0 || !strings.Contains(out, "NEW-1") {
		t.Fatalf("list = %d %q, want the new ticket", code, out)
	}
	hasNot(t, "list's output", out, "from the hub")
	if hub.calls != 0 {
		t.Fatalf("the hub saw %d calls, want none: new writes a whole ticket and list only reads", hub.calls)
	}
}

func TestTheKeyTheHubGetsIsTheTabsKey(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	if out, err := exec.Command("git", "init", f.cwd).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	remote := "git@github.com:MuhmdRaouf/shared.git"
	if out, err := exec.Command("git", "-C", f.cwd, "remote", "add", "origin", remote).CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	hub := &hubCapture{lines: []string{"ok"}, handled: true}
	w := f.world()
	w.HubFlow = hub.flow
	if code, _, errOut := runWorld(t, w, "log", "--actor", "w1", "--note", "n", "FLW-1"); code != 0 || errOut != "" {
		t.Fatalf("log through the hub = %d %q, want 0", code, errOut)
	}
	// the tab keys the same board by its repository's origin, so both clones of
	// one repository name one board
	equals(t, "the key the hub got", hub.key, board.RepoKey(remote, server.KeyOf(f.root)))
}

func TestEveryFlowCommandBuildsItsRequest(t *testing.T) {
	hermeticGit(t)
	f := newFixture(t)
	tests := []struct {
		name string
		argv []string
		want board.FlowRequest
	}{
		{"promote", []string{"promote", "--actor", "w1", "PRO-1"},
			board.FlowRequest{Command: "promote", Actor: "w1", ID: "PRO-1"}},
		{"promote --force", []string{"promote", "--actor", "w1", "--force", "PRO-1"},
			board.FlowRequest{Command: "promote", Actor: "w1", ID: "PRO-1", Force: true}},
		{"pull", []string{"pull", "--actor", "w1", "--id", "FLW-1"},
			board.FlowRequest{Command: "pull", Actor: "w1", ID: "FLW-1"}},
		{"pull with the branch flags", []string{"pull", "--actor", "w1", "--id", "FLW-1",
			"--branch", "card/x", "--worktree", "/wt/x", "--force"},
			board.FlowRequest{Command: "pull", Actor: "w1", ID: "FLW-1",
				Branch: "card/x", Worktree: "/wt/x", Force: true}},
		{"review", []string{"review", "--actor", "w1", "--note", "look", "REV-1"},
			board.FlowRequest{Command: "review", Actor: "w1", ID: "REV-1", Note: "look"}},
		{"reject", []string{"reject", "--actor", "lead", "--note", "not yet", "REV-1"},
			board.FlowRequest{Command: "reject", Actor: "lead", ID: "REV-1", Note: "not yet"}},
		{"unblock", []string{"unblock", "--actor", "w1", "--note", "free", "BLO-1"},
			board.FlowRequest{Command: "unblock", Actor: "w1", ID: "BLO-1", Note: "free"}},
		{"test", []string{"test", "--actor", "v1", "--note", "go test 0", "TST-1"},
			board.FlowRequest{Command: "test", Actor: "v1", ID: "TST-1", Note: "go test 0"}},
		{"done", []string{"done", "--actor", "lead", "--note", "landed", "--checks", "all 0",
			"--merge", "abc123", "DON-1"},
			board.FlowRequest{Command: "done", Actor: "lead", ID: "DON-1",
				Note: "landed", Checks: "all 0", Merge: "abc123"}},
		{"block", []string{"block", "--actor", "w1", "--note", "waiting", "--on", "OTH-1",
			"--on", "the owner", "--until", "OTH-1 done", "BLO-1"},
			board.FlowRequest{Command: "block", Actor: "w1", ID: "BLO-1", Note: "waiting",
				On: []string{"OTH-1", "the owner"}, Until: "OTH-1 done"}},
		{"move", []string{"move", "--actor", "lead", "--note", "repair", "MOV-1", "review"},
			board.FlowRequest{Command: "move", Actor: "lead", ID: "MOV-1", Note: "repair",
				Column: "review"}},
		{"log", []string{"log", "--actor", "w1", "--note", "found it", "LOG-1"},
			board.FlowRequest{Command: "log", Actor: "w1", ID: "LOG-1", Note: "found it"}},
		{"set", []string{"set", "--actor", "w1", "SET-1", "title=New", "est=S"},
			board.FlowRequest{Command: "set", Actor: "w1", ID: "SET-1",
				Pairs: []string{"title=New", "est=S"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := &hubCapture{lines: []string{"ok"}, handled: true}
			w := f.world()
			w.HubFlow = hub.flow
			code, _, errOut := runWorld(t, w, tt.argv...)
			if code != 0 || errOut != "" {
				t.Fatalf("%s through the hub = %d %q, want 0", tt.name, code, errOut)
			}
			if hub.calls != 1 {
				t.Fatalf("the hub saw %d calls, want 1", hub.calls)
			}
			if !reflect.DeepEqual(hub.req, tt.want) {
				t.Errorf("the request the hub got = %+v, want %+v", hub.req, tt.want)
			}
		})
	}
}
