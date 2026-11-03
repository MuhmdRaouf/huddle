package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinFixture is a fixture whose world keeps session pins in a map, the way the binary keeps them
// in the hub or a file.
type pinFixture struct {
	*fixture
	pins map[string]string
}

// newPinFixture builds a board with the boards login and billing, in a working tree whose branch
// is branch and whose directory is named dir; session is the session id of the environment.
func newPinFixture(t *testing.T, branch, session string) *pinFixture {
	t.Helper()
	p := &pinFixture{fixture: newFixture(t), pins: map[string]string{}}
	p.env = map[string]string{"KANBAN_BOARD": p.root}
	p.workTree("work", branch)
	if session != "" {
		p.env["HUDDLE_SESSION"] = session
	}
	for _, name := range []string{"login", "billing"} {
		if code, _, stderr := p.run("board", "create", name, "--actor", "lead"); code != 0 {
			t.Fatalf("board create %s = %d %s", name, code, stderr)
		}
	}
	return p
}

// workTree moves the working directory into a git work tree of that directory name, on branch.
func (p *pinFixture) workTree(dir, branch string) {
	p.t.Helper()
	p.cwd = filepath.Join(p.t.TempDir(), dir)
	if err := os.MkdirAll(filepath.Join(p.cwd, ".git"), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.cwd, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pinFixture) run(argv ...string) (int, string, string) {
	p.t.Helper()
	w := p.world()
	w.PinGet = func(_ context.Context, session string) string { return p.pins[session] }
	w.PinSet = func(_ context.Context, session, board string) error {
		if session == "unwritable" {
			return errors.New("the pin cannot be kept")
		}
		p.pins[session] = board
		return nil
	}
	var out, errOut bytes.Buffer
	code := Run(context.Background(), w, argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

// newCard files a card on the board named by --cluster, or on the pin when cluster is empty.
func (p *pinFixture) newCard(id, cluster string) (int, string) {
	argv := []string{"new", id, "--actor", "w1", "--title", "t", "--summary", longSummary}
	if cluster != "" {
		argv = append(argv, "--cluster", cluster)
	}
	code, _, stderr := p.run(argv...)
	return code, stderr
}

func TestBoardUseRefusesABoardThatDoesNotExist(t *testing.T) {
	p := newPinFixture(t, "feat/login", "s1")
	code, _, stderr := p.run("board", "use", "nowhere", "--actor", "w1")
	if code != 1 || !strings.Contains(stderr, "create it with kanban board create nowhere") {
		t.Fatalf("use of an unknown board = %d %q, want exit 1 and the create hint", code, stderr)
	}
	if p.pins["s1"] != "" {
		t.Errorf("the refused use pinned %q", p.pins["s1"])
	}
}

func TestBoardUseNeedsASession(t *testing.T) {
	p := newPinFixture(t, "feat/login", "")
	code, _, stderr := p.run("board", "use", "login", "--actor", "w1")
	if code != 1 || !strings.Contains(stderr, "no session") {
		t.Errorf("use without a session id = %d %q, want exit 1 naming the missing session", code, stderr)
	}
}

func TestBoardUseTakesTheSessionFromClaudeCodeToo(t *testing.T) {
	p := newPinFixture(t, "feat/login", "")
	p.env["CLAUDE_CODE_SESSION_ID"] = "cc-1"
	if code, _, stderr := p.run("board", "use", "login", "--actor", "w1"); code != 0 {
		t.Fatalf("use = %d %q, want exit 0", code, stderr)
	}
	if p.pins["cc-1"] != "login" {
		t.Errorf("pins = %v, want cc-1 pinned to login", p.pins)
	}
}

func TestBoardUseSaysWhenThePinCannotBeKept(t *testing.T) {
	p := newPinFixture(t, "feat/login", "unwritable")
	code, _, stderr := p.run("board", "use", "login", "--actor", "w1")
	if code != 1 || !strings.Contains(stderr, "cannot be kept") {
		t.Errorf("use with an unwritable pin = %d %q, want exit 1 with the cause", code, stderr)
	}
}

func TestBoardSuggestNamesTheBoardOfTheBranchThenOfTheWorkTree(t *testing.T) {
	p := newPinFixture(t, "feat/login", "s1")
	if code, stdout, stderr := p.run("board", "suggest", "--actor", "w1"); code != 0 || stdout != "login\n" {
		t.Fatalf("suggest on feat/login = %d %q %q, want login", code, stdout, stderr)
	}

	// a branch that names no board falls back to the work tree's directory name
	billing := newPinFixture(t, "wip/spike", "s1")
	billing.workTree("billing", "wip/spike")
	if code, stdout, stderr := billing.run("board", "suggest", "--actor", "w1"); code != 0 || stdout != "billing\n" {
		t.Errorf("suggest in the billing work tree = %d %q %q, want billing", code, stdout, stderr)
	}

	none := newPinFixture(t, "fix/typo", "s1")
	if code, _, stderr := none.run("board", "suggest", "--actor", "w1"); code != 1 || !strings.Contains(stderr, "fix/typo") {
		t.Errorf("suggest with no match = %d %q, want exit 1 naming the branch", code, stderr)
	}
}

func TestNewNeedsAClusterOrAPin(t *testing.T) {
	p := newPinFixture(t, "feat/login", "s1")
	code, stderr := p.newCard("LOGIN-1", "")
	if code != 2 || !strings.Contains(stderr, "--cluster") || !strings.Contains(stderr, "kanban board use") {
		t.Fatalf("new with neither = %d %q, want a usage error naming --cluster and kanban board use", code, stderr)
	}
	if code, _, _ := p.run("board", "use", "login", "--actor", "w1"); code != 0 {
		t.Fatal("use failed")
	}
	if code, stderr := p.newCard("LOGIN-1", ""); code != 0 {
		t.Fatalf("new under the pin = %d %q, want exit 0", code, stderr)
	}
	if !strings.Contains(p.ticketText("backlog", "LOGIN-1"), "cluster: login") {
		t.Errorf("the pinned card:\n%s", p.ticketText("backlog", "LOGIN-1"))
	}
}

func TestAnExplicitClusterBeatsThePin(t *testing.T) {
	p := newPinFixture(t, "feat/login", "s1")
	p.pins["s1"] = "login"
	if code, stderr := p.newCard("BILL-1", "billing"); code != 0 {
		t.Fatalf("new --cluster billing = %d %q", code, stderr)
	}
	if !strings.Contains(p.ticketText("backlog", "BILL-1"), "cluster: billing") {
		t.Errorf("the card:\n%s", p.ticketText("backlog", "BILL-1"))
	}
}

func TestAPinToAnArchivedOrMissingBoardIsRefusedByNew(t *testing.T) {
	p := newPinFixture(t, "feat/login", "s1")
	p.pins["s1"] = "gone"
	code, stderr := p.newCard("GONE-1", "")
	if code != 1 || !strings.Contains(stderr, "create it with kanban board create gone") {
		t.Errorf("new under a pin to a missing board = %d %q, want the create hint", code, stderr)
	}
}

func TestListFollowsThePinAndAllIgnoresIt(t *testing.T) {
	p := newPinFixture(t, "feat/login", "s1")
	for id, cluster := range map[string]string{"LOGIN-1": "login", "BILL-1": "billing"} {
		if code, stderr := p.newCard(id, cluster); code != 0 {
			t.Fatalf("new %s = %d %q", id, code, stderr)
		}
	}
	_, stdout, _ := p.run("list")
	if !strings.Contains(stdout, "LOGIN-1") || !strings.Contains(stdout, "BILL-1") {
		t.Fatalf("list before the pin:\n%s\nwant both cards", stdout)
	}
	p.pins["s1"] = "login"
	_, stdout, _ = p.run("list")
	if !strings.Contains(stdout, "LOGIN-1") || strings.Contains(stdout, "BILL-1") {
		t.Errorf("list under the pin:\n%s\nwant only the login card", stdout)
	}
	_, stdout, _ = p.run("list", "--all")
	if !strings.Contains(stdout, "BILL-1") {
		t.Errorf("list --all under the pin:\n%s\nwant every board's cards", stdout)
	}
	_, stdout, _ = p.run("list", "backlog", "--json")
	if strings.Contains(stdout, "BILL-1") {
		t.Errorf("list backlog --json under the pin:\n%s\nwant the pin to scope the json too", stdout)
	}
}

func TestNewStampsTheSessionAsTheCardsOwner(t *testing.T) {
	p := newPinFixture(t, "feat/login", "s1")
	if code, stderr := p.newCard("LOGIN-1", "login"); code != 0 {
		t.Fatalf("new = %d %q", code, stderr)
	}
	if !strings.Contains(p.ticketText("backlog", "LOGIN-1"), "owner_session: s1\n") {
		t.Errorf("the card lacks the session owner:\n%s", p.ticketText("backlog", "LOGIN-1"))
	}
}
