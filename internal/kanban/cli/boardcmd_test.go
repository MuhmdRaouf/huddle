package cli

import (
	"strings"
	"testing"
)

// kanban board create registers a board and prints its line; kanban new for a
// board that was never created exits 1 until it is.
func TestBoardCreateCommand(t *testing.T) {
	f := newFixture(t)
	newLogin := []string{"new", "LOGIN-1", "--actor", "lead", "--title", "t",
		"--cluster", "login", "--summary", longSummary}

	code, _, stderr := f.run(newLogin...)
	if code != 1 || !strings.Contains(stderr, "create it with kanban board create login") {
		t.Fatalf("new before create = %d %q, want exit 1 and the create hint", code, stderr)
	}
	if f.exists("backlog", "LOGIN-1") {
		t.Fatal("the refused new wrote a card")
	}

	code, stdout, stderr := f.run("board", "create", "login", "--actor", "lead")
	if code != 0 || stdout != "board login created\n" || stderr != "" {
		t.Fatalf("board create = %d %q %q, want exit 0 and the created line", code, stdout, stderr)
	}
	code, stdout, _ = f.run("board", "create", "login", "--actor", "lead")
	if code != 0 || stdout != "board login already exists\n" {
		t.Fatalf("second board create = %d %q, want exit 0 and the already-exists line", code, stdout)
	}
	code, _, stderr = f.run("board", "create", "Bad_Name", "--actor", "lead")
	if code != 1 || !strings.Contains(stderr, "board name") {
		t.Fatalf("board create Bad_Name = %d %q, want exit 1", code, stderr)
	}
	if code, _, stderr = f.run(newLogin...); code != 0 {
		t.Fatalf("new after create = %d %q, want exit 0", code, stderr)
	}
}

// The board word takes a verb from its table; an unknown or missing verb, a
// missing name and a missing --actor are usage errors (exit 2).
func TestBoardCommandUsageErrors(t *testing.T) {
	f := newFixture(t)
	for _, argv := range [][]string{
		{"board"},
		{"board", "frobnicate", "login", "--actor", "lead"},
		{"board", "create", "--actor", "lead"},
		{"board", "create", "login"},
		{"board", "create", "login", "extra", "--actor", "lead"},
	} {
		if code, _, stderr := f.run(argv...); code != 2 || !strings.Contains(stderr, "kanban board") {
			t.Errorf("%v = %d %q, want exit 2 naming kanban board", argv, code, stderr)
		}
	}
}
