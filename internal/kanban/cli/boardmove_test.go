package cli

import (
	"strings"
	"testing"
)

// kanban board move ID BOARD changes the card's board and keeps its column; a board that was
// never created exits 1 and a missing positional exits 2.
func TestBoardMoveCommand(t *testing.T) {
	f := newFixture(t)
	f.fullCard("review", "KAN-1")
	if code, _, stderr := f.run("board", "create", "billing", "--actor", "lead"); code != 0 {
		t.Fatalf("board create = %d %q", code, stderr)
	}

	code, stdout, stderr := f.run("board", "move", "KAN-1", "billing", "--actor", "lead")
	if code != 0 || stdout != "KAN-1 moved to board billing from kanban\n" || stderr != "" {
		t.Fatalf("board move = %d %q %q, want exit 0 and the moved line", code, stdout, stderr)
	}
	text := f.ticketText("review", "KAN-1")
	has(t, "the card", text, "cluster: billing\n", "lead: moved to board billing from kanban")

	code, _, stderr = f.run("board", "move", "KAN-1", "nowhere", "--actor", "lead")
	if code != 1 || !strings.Contains(stderr, "create it with kanban board create nowhere") {
		t.Fatalf("board move to an unknown board = %d %q, want exit 1 and the create hint", code, stderr)
	}
	if code, _, stderr = f.run("board", "move", "KAN-1", "--actor", "lead"); code != 2 || !strings.Contains(stderr, "move needs 2 argument(s)") {
		t.Fatalf("board move with no board = %d %q, want exit 2", code, stderr)
	}
}
