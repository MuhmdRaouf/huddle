package board

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// paritySummary clears the summary bar.
const paritySummary = "Puts the board's files behind one interface with a folder and a database " +
	"implementation, so a root holding kanban.db reads and writes the database. " +
	"Done when the parity test proves identical outputs and texts."

func TestADbBoardMatchesAFolderBoard(t *testing.T) {
	ctx := context.Background()

	// The folder board: today's board, built by init.
	folderRoot := filepath.Join(t.TempDir(), "board")
	if _, err := Init(ctx, folderRoot, "", fixedNow); err != nil {
		t.Fatalf("init the folder board: %v", err)
	}
	folder := Board{Root: folderRoot, Now: func() time.Time { return fixedNow }}

	// The db board: the same root shape with a kanban.db in it.
	dbRoot := filepath.Join(t.TempDir(), "board")
	st, err := store.Open(ctx, filepath.Join(dbRoot, "kanban.db"))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db, closeBoard, err := Open(ctx, Board{Root: dbRoot, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("open the db board: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })

	// bodyFile lays the same card body outside each board root, the way a user's
	// --body-file sits outside the board.
	bodyFile := func(root, id string) string {
		t.Helper()
		file := filepath.Join(filepath.Dir(root), id+"-body.md")
		if err := os.WriteFile(file, []byte(fullBody(id)), 0o644); err != nil {
			t.Fatal(err)
		}
		return file
	}

	steps := []struct {
		name string
		run  func(b Board) ([]string, error)
	}{
		{"new A", func(b Board) ([]string, error) {
			return b.New(ctx, NewTicket{
				Actor: "dev", ID: "A", Title: "First card", Cluster: "fix",
				Type: "fix", Priority: "P1", Est: "S", Summary: paritySummary,
				BodyFile: bodyFile(b.Root, "A"),
			})
		}},
		{"new B", func(b Board) ([]string, error) {
			return b.New(ctx, NewTicket{
				Actor: "dev", ID: "B", Title: "Second card", Cluster: "fix",
				Type: "fix", Priority: "P2", Est: "M", Summary: paritySummary,
				DependsOn: []string{"A"}, BodyFile: bodyFile(b.Root, "B"),
			})
		}},
		{"promote A", func(b Board) ([]string, error) {
			return b.Promote(ctx, Args{Actor: "dev", ID: "A"})
		}},
		{"pull A", func(b Board) ([]string, error) {
			return b.Pull(ctx, Args{Actor: "dev", ID: "A", Branch: "fix/a", Worktree: "wt-a"})
		}},
		{"review A", func(b Board) ([]string, error) {
			return b.Review(ctx, Args{Actor: "dev", ID: "A", Note: "ready for review"})
		}},
		{"reject A", func(b Board) ([]string, error) {
			return b.Reject(ctx, Args{Actor: "rev", ID: "A", Note: "tighten the wording"})
		}},
		{"pull A again", func(b Board) ([]string, error) {
			return b.Pull(ctx, Args{Actor: "dev", ID: "A"})
		}},
		{"review A again", func(b Board) ([]string, error) {
			return b.Review(ctx, Args{Actor: "dev", ID: "A", Note: "second pass"})
		}},
		{"test A", func(b Board) ([]string, error) {
			return b.Test(ctx, Args{Actor: "rev", ID: "A", Note: "go test ./... exit 0; 12 passed"})
		}},
		{"done A", func(b Board) ([]string, error) {
			return b.Done(ctx, Args{Actor: "rev", ID: "A", Note: "verified green",
				Checks: "go test ./... exit 0", Merge: "pr merged"})
		}},
		{"block B", func(b Board) ([]string, error) {
			return b.Block(ctx, Args{Actor: "dev", ID: "B", Note: "waiting on the spec",
				On: []string{"upstream"}, Until: "the spec lands"})
		}},
		{"unblock B", func(b Board) ([]string, error) {
			return b.Unblock(ctx, Args{Actor: "dev", ID: "B"})
		}},
		{"stop-line", func(b Board) ([]string, error) {
			return b.StopLine(ctx, Args{Actor: "lead", Note: "broken main"})
		}},
		{"resume-line", func(b Board) ([]string, error) {
			return b.ResumeLine(ctx, Args{Actor: "lead"})
		}},
		{"set B", func(b Board) ([]string, error) {
			return b.Set(ctx, "dev", "B", []string{"title=Second card, retitled"})
		}},
		{"log B", func(b Board) ([]string, error) {
			return b.Log(ctx, Args{Actor: "dev", ID: "B", Note: "picked up again"})
		}},
		{"show A", func(b Board) ([]string, error) {
			shown, err := b.Show(ctx, "A")
			return shownLines(shown), err
		}},
		{"show B", func(b Board) ([]string, error) {
			shown, err := b.Show(ctx, "B")
			return shownLines(shown), err
		}},
		{"list", func(b Board) ([]string, error) {
			cards, err := b.List(ctx, "")
			return cardLines(cards), err
		}},
		{"status", func(b Board) ([]string, error) {
			st, err := b.Status(ctx)
			return []string{fmt.Sprintf("%+v", st)}, err
		}},
		{"check A", func(b Board) ([]string, error) {
			return b.Check(ctx, "A")
		}},
		{"check B", func(b Board) ([]string, error) {
			return b.Check(ctx, "B")
		}},
		{"feature close fix", func(b Board) ([]string, error) {
			return b.CloseFeature(ctx, Args{Actor: "lead", ID: "fix"})
		}},
		{"closed features", func(b Board) ([]string, error) {
			return closedFeatureLines(b), nil
		}},
		{"feature reopen fix", func(b Board) ([]string, error) {
			return b.ReopenFeature(ctx, Args{Actor: "lead", ID: "fix"})
		}},
		{"closed features after reopen", func(b Board) ([]string, error) {
			return closedFeatureLines(b), nil
		}},
	}

	folderLines := drive(t, folder, steps)
	dbLines := drive(t, db, steps)
	for _, step := range steps {
		want, ok := folderLines[step.name]
		if !ok {
			t.Fatalf("the folder board recorded nothing for %s", step.name)
		}
		got := dbLines[step.name]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n db: %q\nfolder: %q", step.name, got, want)
		}
	}

	// Every ticket's final text is the same, whole.
	for _, id := range []string{"A", "B"} {
		folderShown, err := folder.Show(ctx, id)
		if err != nil {
			t.Fatalf("show %s on the folder board: %v", id, err)
		}
		dbShown, err := db.Show(ctx, id)
		if err != nil {
			t.Fatalf("show %s on the db board: %v", id, err)
		}
		folderShown.Path = "<root>"
		dbShown.Path = "<root>"
		folderShown.Card.UpdatedAt = ""
		dbShown.Card.UpdatedAt = ""
		if !reflect.DeepEqual(folderShown, dbShown) {
			t.Errorf("ticket %s:\n db: %+v\nfolder: %+v", id, dbShown, folderShown)
		}
	}

	// The board's own files carry the same bytes.
	for _, name := range []string{"LOG", "EPOCH", "STOP-LINE", "FEATURES"} {
		want := boardFile(t, folder, name)
		got := boardFile(t, db, name)
		if got != want {
			t.Errorf("%s:\n db: %q\nfolder: %q", name, got, want)
		}
	}
}

// drive runs the whole sequence on one board and answers each step's lines with
// the board's root replaced by <root>; a refusal counts as its step's lines.
func drive(t *testing.T, b Board, steps []struct {
	name string
	run  func(b Board) ([]string, error)
}) map[string][]string {
	t.Helper()
	out := make(map[string][]string, len(steps))
	for _, step := range steps {
		lines, err := step.run(b)
		if err != nil {
			var refused *Refused
			if !errors.As(err, &refused) {
				t.Fatalf("%s on %s: %v", step.name, b.Root, err)
			}
			lines = append(lines, refused.Message)
		}
		for _, line := range lines {
			out[step.name] = append(out[step.name], strings.ReplaceAll(line, b.Root, "<root>"))
		}
	}
	return out
}

// shownLines renders one Show answer: the text, the path and the card view,
// with the mtime a folder card carries and the updated_at a db card carries
// blanked.
func shownLines(shown Shown) []string {
	shown.Card.UpdatedAt = ""
	return []string{shown.Text, shown.Path, fmt.Sprintf("%+v", shown.Card)}
}

// cardLines renders the list answer the same way.
func cardLines(cards []Card) []string {
	lines := make([]string, 0, len(cards))
	for _, c := range cards {
		c.UpdatedAt = ""
		lines = append(lines, fmt.Sprintf("%+v", c))
	}
	return lines
}

// closedFeatureLines renders the board's closed features as comparable lines,
// led by the count so an empty board still answers one.
func closedFeatureLines(b Board) []string {
	closed := b.ClosedFeatures()
	lines := make([]string, 0, len(closed)+1)
	lines = append(lines, fmt.Sprintf("%d closed", len(closed)))
	for _, f := range closed {
		lines = append(lines, fmt.Sprintf("%s %s %s", f.Name, f.Actor, f.ClosedAt))
	}
	return lines
}

// boardFile reads one of the board's own files through its seam; a file that is
// missing answers the same marker on both boards.
func boardFile(t *testing.T, b Board, name string) string {
	t.Helper()
	data, err := b.fs().readFile(filepath.Join(b.Root, name))
	if err != nil {
		return "<missing>"
	}
	return string(data)
}
