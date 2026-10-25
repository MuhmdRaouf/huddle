package hooks

// checkoutOf reads the project and the branch from the checkout's own files: a main checkout, a
// linked worktree and a detached HEAD, built by writing files alone, no git binary.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckoutOfReadsProjectAndBranchFromTheFiles(t *testing.T) {
	t.Run("main checkout", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/feat/x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "sub", "dir"), 0o755); err != nil {
			t.Fatal(err)
		}
		project, branch := checkoutOf(filepath.Join(root, "sub", "dir"))
		if project != root {
			t.Errorf("project = %q, want the checkout %q", project, root)
		}
		if branch != "feat/x" {
			t.Errorf("branch = %q, want feat/x", branch)
		}
	})
	t.Run("linked worktree", func(t *testing.T) {
		main, wt := t.TempDir(), t.TempDir()
		if err := os.MkdirAll(filepath.Join(main, ".git", "worktrees", "wt1"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(main, ".git", "worktrees", "wt1", "HEAD"),
			[]byte("ref: refs/heads/wt/one\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, ".git"),
			[]byte("gitdir: "+filepath.Join(main, ".git", "worktrees", "wt1")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		pkg := filepath.Join(wt, "pkg")
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		project, branch := checkoutOf(pkg)
		if project != wt {
			t.Errorf("project = %q, want the worktree itself %q, the way git answers inside it", project, wt)
		}
		if branch != "wt/one" {
			t.Errorf("branch = %q, want wt/one from the gitdir's HEAD", branch)
		}
	})
	t.Run("detached head", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"),
			[]byte("0123456789abcdef0123456789abcdef01234567\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		project, branch := checkoutOf(root)
		if project != root {
			t.Errorf("project = %q, want the checkout %q", project, root)
		}
		if branch != "HEAD" {
			t.Errorf("branch = %q, want HEAD, the word git prints when detached", branch)
		}
	})
}
