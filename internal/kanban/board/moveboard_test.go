package board

import (
	"context"
	"strings"
	"testing"
)

// moveFixture is a board with a login card in review, held by w1, with three card
// log lines, and a billing board beside the login one.
func moveFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	mustLines(t, func() ([]string, error) {
		return f.b.CreateBoard(context.Background(), Args{Actor: "lead", ID: "login"})
	})
	mustLines(t, func() ([]string, error) {
		return f.b.CreateBoard(context.Background(), Args{Actor: "lead", ID: "billing"})
	})
	f.card("review", "LOGIN-1",
		"id: LOGIN-1\ntitle: \"t\"\ncluster: login\nclaimed_by: w1\nowner_session: s1",
		"# LOGIN-1\n\nBody.\n\n## Log\n- 2026-10-15T09:00:00+0300 w1: one\n- 2026-10-15T10:00:00+0300 w1: two\n- 2026-10-15T11:00:00+0300 w1: three")
	return f
}

func (f *fixture) moveToBoard(a Args) cmd {
	return func() ([]string, error) { return f.b.MoveToBoard(context.Background(), a) }
}

// A card moved to another board keeps its column, its holder and its owner session, keeps
// its three log lines, and gains the move line as the fourth, with one line on the board LOG.
func TestAMovedCardKeepsColumnOwnerAndLogAndAddsOneLine(t *testing.T) {
	f := moveFixture(t)
	mustLines(t, f.moveToBoard(Args{Actor: "lead", ID: "LOGIN-1", Board: "billing"}))

	text := f.fm("review", "LOGIN-1")
	contains(t, text, "cluster: billing\n", "the card's board")
	contains(t, text, "claimed_by: w1\n", "the holder")
	contains(t, text, "owner_session: s1\n", "the owner session")
	logLines := text[strings.Index(text, "## Log\n")+len("## Log\n"):]
	lines := strings.Split(strings.TrimSpace(logLines), "\n")
	if len(lines) != 4 {
		t.Fatalf("the card log has %d lines, want 4:\n%s", len(lines), logLines)
	}
	for i, old := range []string{": one", ": two", ": three"} {
		if !strings.HasSuffix(lines[i], old) {
			t.Errorf("log line %d = %q, want the old line ending %q", i, lines[i], old)
		}
	}
	if want := "lead: moved to board billing from login"; !strings.HasSuffix(lines[3], want) {
		t.Errorf("the 4th log line = %q, want it to end %q", lines[3], want)
	}
	entry := f.lastLog()
	if entry["event"] != "board-move" || entry["ticket"] != "LOGIN-1" || entry["from"] != "login" || entry["to"] != "billing" {
		t.Errorf("the board LOG line = %v, want a board-move of LOGIN-1 from login to billing", entry)
	}
}

// A move is refused, and writes nothing, for a target that is not a board, an archived source
// or target, the card's own board and an unknown id.
func TestMoveToBoardRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		args  Args
		want  string
	}{
		{"target not registered", func(*fixture) {}, Args{Actor: "lead", ID: "LOGIN-1", Board: "nowhere"}, "create it with kanban board create nowhere"},
		{"archived source", func(f *fixture) {
			mustLines(f.t, func() ([]string, error) {
				return f.b.CloseFeature(context.Background(), Args{Actor: "system", ID: "login"})
			})
		}, Args{Actor: "lead", ID: "LOGIN-1", Board: "billing"}, "Feature closed: login"},
		{"archived target", func(f *fixture) {
			f.card("done", "BILL-1", "id: BILL-1\ncluster: billing")
			mustLines(f.t, func() ([]string, error) {
				return f.b.CloseFeature(context.Background(), Args{Actor: "system", ID: "billing"})
			})
		}, Args{Actor: "lead", ID: "LOGIN-1", Board: "billing"}, "board billing is archived"},
		{"the card's own board", func(*fixture) {}, Args{Actor: "lead", ID: "LOGIN-1", Board: "login"}, "already on board login"},
		{"unknown id", func(*fixture) {}, Args{Actor: "lead", ID: "NOPE-1", Board: "billing"}, "NOPE-1: not on the board"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := moveFixture(t)
			tc.setup(f)
			before, logBefore := f.fm("review", "LOGIN-1"), f.logText()
			contains(t, mustRefuse(t, f.moveToBoard(tc.args)), tc.want, "the refusal")
			if f.fm("review", "LOGIN-1") != before || f.logText() != logBefore {
				t.Error("a refused move wrote to the card or the board LOG")
			}
		})
	}
}
