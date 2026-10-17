package board

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func envNone(string) (string, bool) { return "", false }

func envWith(kv map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := kv[key]
		return v, ok
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()

	// --board and $KANBAN_BOARD point anywhere, the walk-up finds the project
	// copy, and a mutation through a resolved board lands in that board.
	t.Run("test_board_option_and_env_point_the_skill_script_at_any_project", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "A-1", "id: A-1\ntitle: \"a\"\ntype: docs")
		elsewhere := t.TempDir()

		b, err := Resolve(f.root, envNone, elsewhere)
		if err != nil || b.Root != f.root {
			t.Fatalf("Resolve(explicit) = %v, %v", b.Root, err)
		}
		if _, err := b.Status(ctx); err != nil {
			t.Fatalf("status through the explicit board: %v", err)
		}

		b, err = Resolve("", envWith(map[string]string{"KANBAN_BOARD": f.root}), elsewhere)
		if err != nil || b.Root != f.root {
			t.Fatalf("Resolve(env) = %v, %v", b.Root, err)
		}

		project := t.TempDir()
		walked := newFixtureAt(t, filepath.Join(project, ".agents", "kanban"))
		walked.card("ready", "A-1", "id: A-1\ntitle: \"a\"\ntype: docs")
		deep := filepath.Join(project, "a", "b", "c")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		b, err = Resolve("", envNone, deep)
		if err != nil || b.Root != filepath.Join(project, ".agents", "kanban") {
			t.Fatalf("Resolve(walk up) = %v, %v", b.Root, err)
		}

		// a mutation through a resolved board lands in that board
		if _, err := b.Pull(ctx, Args{Actor: "w1"}); err != nil {
			t.Fatalf("pull through the resolved board: %v", err)
		}
		if !walked.exists("in-progress", "A-1") {
			t.Error("the mutation did not land in the walked-up board")
		}
	})

	// An explicit board works from a directory with no board of its own. The
	// argv-order and --help halves are the cli suite's.
	t.Run("test_board_option_works_before_and_after_the_subcommand", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "A-1", "id: A-1\ntitle: \"a\"\ntype: docs\npriority: P0")
		elsewhere := t.TempDir()
		b, err := Resolve(f.root, envNone, elsewhere)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		cards, err := b.List(ctx, "")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(cards) != 1 || cards[0].ID != "A-1" {
			t.Fatalf("list = %+v", cards)
		}
		if _, err := b.Pull(ctx, Args{Actor: "w1"}); err != nil {
			t.Fatalf("pull: %v", err)
		}
		if !f.exists("in-progress", "A-1") {
			t.Error("the mutation did not land in the explicit board")
		}
	})

	t.Run("test_missing_board_is_a_clear_error_not_a_traceback", func(t *testing.T) {
		if _, err := Resolve("", envNone, t.TempDir()); !errors.Is(err, ErrNoBoard) {
			t.Errorf("Resolve = %v, want ErrNoBoard", err)
		}
	})
}

func TestResolveWalkUp(t *testing.T) {
	newProject := func(t *testing.T) string {
		root := t.TempDir()
		board := filepath.Join(root, ".agents", "kanban")
		for _, col := range Columns {
			if err := os.MkdirAll(filepath.Join(board, col), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	t.Run("finds the board above the working directory", func(t *testing.T) {
		root := newProject(t)
		deep := filepath.Join(root, "a", "b", "c")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		b, err := Resolve("", envNone, deep)
		if err != nil || b.Root != filepath.Join(root, ".agents", "kanban") {
			t.Errorf("Resolve = %v, %v", b.Root, err)
		}
	})

	t.Run("stops at the git root", func(t *testing.T) {
		outer := newProject(t)
		inner := filepath.Join(outer, "repo")
		if err := os.MkdirAll(filepath.Join(inner, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve("", envNone, inner); !errors.Is(err, ErrNoBoard) {
			t.Errorf("Resolve climbed past .git: %v", err)
		}
	})

	t.Run("explicit wins over env and cwd", func(t *testing.T) {
		root := newProject(t)
		other := newProject(t)
		b, err := Resolve(other, envWith(map[string]string{"KANBAN_BOARD": root}), root)
		if err != nil || b.Root != other {
			t.Errorf("Resolve = %v, %v", b.Root, err)
		}
		b, err = Resolve("", envWith(map[string]string{"KANBAN_BOARD": other}), root)
		if err != nil || b.Root != other {
			t.Errorf("Resolve(env) = %v, %v", b.Root, err)
		}
	})
}
