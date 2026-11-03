package cli

import (
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// kanban status is a read the board already serves: it archives a board whose
// cards have all been done for 24 hours, so the counts leave it out.
func TestStatusArchivesABoardDoneFor24Hours(t *testing.T) {
	f := newFixture(t)
	finished := fixedNow.Add(-24 * time.Hour).Format("2006-01-02T15:04:05-0700")
	f.card("done", "OLD-1", "id: OLD-1\ncluster: kanban\nfinished_at: "+finished)

	code, out, errOut := f.run("status")
	if code != 0 {
		t.Fatalf("status = %d %s", code, errOut)
	}
	has(t, "status", out, "done: 0")
	hasNot(t, "status", out, "done: 1")

	b, closeBoard, err := board.Open(t.Context(), board.Board{Root: f.root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeBoard() }()
	closed := b.ClosedFeatures()
	if len(closed) != 1 || closed[0].Name != "kanban" || closed[0].Actor != "system" {
		t.Fatalf("ClosedFeatures = %+v, want kanban closed by system", closed)
	}
}
