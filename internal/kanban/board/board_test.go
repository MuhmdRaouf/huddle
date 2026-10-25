package board

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/store"
)

// openRoot builds an initialised board at its own temp root.
func openRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agents", "kanban")
	if _, err := Init(context.Background(), root, "", fixedNow); err != nil {
		t.Fatalf("init: %v", err)
	}
	return root
}

func TestOpenLeavesAFolderBoardAlone(t *testing.T) {
	ctx := context.Background()
	root := openRoot(t)
	b, closeBoard, err := Open(ctx, Board{Root: root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if b.Store != nil {
		t.Fatal("a folder board opened with a store")
	}
	if b.Root != root {
		t.Fatalf("root = %q, want %q", b.Root, root)
	}
	if err := closeBoard(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "kanban.db")); !os.IsNotExist(err) {
		t.Fatalf("stat kanban.db = %v, want no database file", err)
	}
	if cards, err := b.List(ctx, ""); err != nil || len(cards) != 0 {
		t.Fatalf("List = %v, %v; want an empty board", cards, err)
	}
}

func TestOpenUsesKanbanDbWhenPresent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(root, "kanban.db"))
	if err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Put(ctx, store.Ticket{ID: "KBN-1", Column: "backlog", Text: "seeded", UpdatedAt: dbNow.Format(time.RFC3339)}); err != nil {
		t.Fatalf("seed the ticket: %v", err)
	}

	b, closeBoard, err := Open(ctx, Board{Root: root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })
	if b.Store == nil {
		t.Fatal("a root holding kanban.db opened without its store")
	}
	if _, col, ok := b.Find("KBN-1"); !ok || col != "backlog" {
		t.Fatalf("Find KBN-1 = %q, %v; want it in backlog", col, ok)
	}
	_, err = b.New(ctx, NewTicket{
		Actor: "dev", ID: "KBN-2", Title: "Second card", Cluster: "fix",
		Type: "fix", Priority: "P1", Est: "S", Summary: longSummary,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	row, err := st.Get(ctx, "KBN-2")
	if err != nil {
		t.Fatalf("get the new card's row: %v", err)
	}
	if row.Column != "backlog" {
		t.Fatalf("the new card's column = %q, want backlog", row.Column)
	}
	if _, err := os.Stat(filepath.Join(root, "backlog", "KBN-2.md")); !os.IsNotExist(err) {
		t.Fatalf("stat the new card's file = %v, want no file on disk", err)
	}
	if err := closeBoard(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestOpenRefusesACorruptDatabase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "kanban.db"), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(context.Background(), Board{Root: root}); err == nil {
		t.Fatal("open over a file that is no database passed")
	}
}

// gitWorktree builds a repository at <root>/main with one commit and adds a
// worktree of it at <root>/wt, its git runs hermetic. It answers both paths,
// symlinks resolved.
func gitWorktree(t *testing.T) (main, wt string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main, wt = filepath.Join(root, "main"), filepath.Join(root, "wt")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = []string{
			"HOME=" + root,
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q", main)
	git("-C", main, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	git("-C", main, "worktree", "add", "-q", wt)
	return main, wt
}

func TestResolveFollowsAWorktreeToItsRepositorysBoard(t *testing.T) {
	main, wt := gitWorktree(t)
	want := filepath.Join(main, ".agents", "kanban")
	newFixtureAt(t, want)
	deep := filepath.Join(wt, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{wt, deep} {
		b, err := Resolve("", envNone, cwd)
		if err != nil || b.Root != want {
			t.Fatalf("Resolve(%s) = %q, %v, want %q", cwd, b.Root, err, want)
		}
	}
}

func TestAWorktreesOwnBoardWins(t *testing.T) {
	main, wt := gitWorktree(t)
	newFixtureAt(t, filepath.Join(main, ".agents", "kanban"))
	own := filepath.Join(wt, ".agents", "kanban")
	newFixtureAt(t, own)
	b, err := Resolve("", envNone, wt)
	if err != nil || b.Root != own {
		t.Fatalf("Resolve(worktree) = %q, %v, want its own board %q", b.Root, err, own)
	}
}

func TestResolveTreatsADanglingGitFileAsNoRepository(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+filepath.Join(dir, "gone")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("", envNone, dir); !errors.Is(err, ErrNoBoard) {
		t.Fatalf("Resolve(dangling .git) = %v, want ErrNoBoard", err)
	}
}
