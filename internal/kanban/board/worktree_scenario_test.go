package board

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestWorktreeBoardScenarios(t *testing.T) {
	// the repositories outlive each Given step, so they live in the test's own temp dir
	keep := t
	bdd.Feature(t, "a worktree session finds its repository's board", func(f *bdd.F) {
		f.Scenario("a worktree with no board lists the main checkout's cards", func(s *bdd.S) {
			var main, wt string
			var got Board
			s.Given("a repository whose main checkout holds a board with one ready card", func(t *testing.T) {
				main, wt = gitWorktree(keep)
				newFixtureAt(keep, filepath.Join(main, ".agents", "kanban")).
					card("ready", "WT-1", "id: WT-1\ntitle: \"from main\"\ntype: docs")
			})
			s.When("a session in a worktree of it resolves its board", func(t *testing.T) {
				b, err := Resolve("", envNone, wt)
				if err != nil {
					t.Fatalf("Resolve(worktree) = %v", err)
				}
				got = b
			})
			s.Then("it answers the main checkout's board", func(t *testing.T) {
				if want := filepath.Join(main, ".agents", "kanban"); got.Root != want {
					t.Fatalf("board = %q, want %q", got.Root, want)
				}
			})
			s.And("its list holds the main checkout's card", func(t *testing.T) {
				cards, err := got.List(context.Background(), "")
				if err != nil || len(cards) != 1 || cards[0].ID != "WT-1" {
					t.Fatalf("list = %+v, %v, want WT-1", cards, err)
				}
			})
		})

		f.Scenario("a worktree with its own board keeps it", func(s *bdd.S) {
			var wt string
			s.Given("a repository and a worktree each holding a board", func(t *testing.T) {
				var main string
				main, wt = gitWorktree(keep)
				newFixtureAt(keep, filepath.Join(main, ".agents", "kanban"))
				newFixtureAt(keep, filepath.Join(wt, ".agents", "kanban"))
			})
			s.Then("a session in the worktree resolves the worktree's board", func(t *testing.T) {
				b, err := Resolve("", envNone, wt)
				if want := filepath.Join(wt, ".agents", "kanban"); err != nil || b.Root != want {
					t.Fatalf("Resolve(worktree) = %q, %v, want %q", b.Root, err, want)
				}
			})
		})

		f.Scenario("a worktree of a repository with no board has none", func(s *bdd.S) {
			var wt string
			s.Given("a repository with no board and a worktree of it", func(t *testing.T) {
				_, wt = gitWorktree(keep)
				if err := os.MkdirAll(filepath.Join(wt, "src"), 0o755); err != nil {
					t.Fatal(err)
				}
			})
			s.Then("resolving from the worktree answers no board", func(t *testing.T) {
				if _, err := Resolve("", envNone, filepath.Join(wt, "src")); !errors.Is(err, ErrNoBoard) {
					t.Fatalf("Resolve = %v, want ErrNoBoard", err)
				}
			})
		})
	})
}
