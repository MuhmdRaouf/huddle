package hooks

// projectOf reads the project, its key and the branch from the checkout's own files: a main checkout, a
// linked worktree and a detached HEAD, built by writing files alone, no git binary.

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

func TestProjectOfReadsRootAndBranchFromTheFiles(t *testing.T) {
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
		got := projectOf(filepath.Join(root, "sub", "dir"))
		if got.Root != root {
			t.Errorf("root = %q, want the checkout %q", got.Root, root)
		}
		if got.Branch != "feat/x" {
			t.Errorf("branch = %q, want feat/x", got.Branch)
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
		got := projectOf(pkg)
		if got.Root != wt {
			t.Errorf("root = %q, want the worktree itself %q, the way git answers inside it", got.Root, wt)
		}
		if got.Branch != "wt/one" {
			t.Errorf("branch = %q, want wt/one from the gitdir's HEAD", got.Branch)
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
		got := projectOf(root)
		if got.Root != root {
			t.Errorf("root = %q, want the checkout %q", got.Root, root)
		}
		if got.Branch != "HEAD" {
			t.Errorf("branch = %q, want HEAD, the word git prints when detached", got.Branch)
		}
	})
}

// gitIn runs one git command in dir under a temp home with a fixed identity and no global config.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = isoenv.Environ(t, t.TempDir(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// realPath resolves symlinks: temp dirs sit under /private on macOS.
func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func writeProjectName(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "huddle")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "huddle.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProjectOfGroupsAWorktreeUnderItsMainCheckout(t *testing.T) {
	t.Run("a worktree's project key is its main checkout", func(t *testing.T) {
		base := realPath(t, t.TempDir())
		main, wt := filepath.Join(base, "main"), filepath.Join(base, "wt")
		gitIn(t, base, "init", "-q", "main")
		gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "first")
		gitIn(t, main, "worktree", "add", "-q", "../wt", "-b", "feat/x")
		sub := filepath.Join(wt, "sub")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		got := projectOf(sub)
		if realPath(t, got.Key) != main {
			t.Errorf("key = %q, want the main checkout %q", got.Key, main)
		}
		if realPath(t, got.Root) != wt {
			t.Errorf("root = %q, want the worktree %q", got.Root, wt)
		}
		if got.Branch != "feat/x" {
			t.Errorf("branch = %q, want feat/x", got.Branch)
		}
		if got.Name != "main" {
			t.Errorf("name = %q, want the main checkout's folder name", got.Name)
		}
	})
	t.Run("a plain checkout is its own key", func(t *testing.T) {
		base := realPath(t, t.TempDir())
		gitIn(t, base, "init", "-q", "plain")
		plain := filepath.Join(base, "plain")
		got := projectOf(plain)
		if got.Key != plain || got.Root != plain || got.Name != "plain" {
			t.Errorf("got key %q root %q name %q, want %q three times over", got.Key, got.Root, got.Name, plain)
		}
	})
	t.Run("a folder with no git is its own key, named by its base", func(t *testing.T) {
		dir := filepath.Join(realPath(t, t.TempDir()), "loose")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		got := projectOf(dir)
		if got.Key != dir || got.Name != "loose" || got.Branch != "" {
			t.Errorf("got key %q name %q branch %q, want %q, loose and no branch", got.Key, got.Name, got.Branch, dir)
		}
	})
	t.Run("a worktree of a bare repository keys on the bare directory", func(t *testing.T) {
		base := realPath(t, t.TempDir())
		src, bare, wt := filepath.Join(base, "src"), filepath.Join(base, "shop.git"), filepath.Join(base, "wt")
		gitIn(t, base, "init", "-q", "src")
		gitIn(t, src, "commit", "-q", "--allow-empty", "-m", "first")
		gitIn(t, base, "clone", "-q", "--bare", "src", "shop.git")
		gitIn(t, bare, "worktree", "add", "-q", wt, "-b", "feat/y")
		got := projectOf(wt)
		if realPath(t, got.Key) != bare {
			t.Errorf("key = %q, want the bare repository %q, not its parent folder", got.Key, bare)
		}
		if got.Name != "shop.git" {
			t.Errorf("name = %q, want shop.git", got.Name)
		}
	})
	t.Run("a worktree whose commondir is missing keys on its own root", func(t *testing.T) {
		main, wt := t.TempDir(), t.TempDir()
		gitdir := filepath.Join(main, ".git", "worktrees", "wt1")
		if err := os.MkdirAll(gitdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := projectOf(wt); got.Key != wt {
			t.Errorf("key = %q, want the worktree root %q", got.Key, wt)
		}
	})
}

func TestProjectOfNamesTheProjectFromHuddleJSON(t *testing.T) {
	t.Run("huddle.json name wins over the folder", func(t *testing.T) {
		base := realPath(t, t.TempDir())
		main := filepath.Join(base, "main")
		gitIn(t, base, "init", "-q", "main")
		gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "first")
		gitIn(t, main, "worktree", "add", "-q", "../wt", "-b", "feat/x")
		writeProjectName(t, main, `{"channel":"shop","name":"homelab"}`)
		if got := projectOf(filepath.Join(base, "wt")); got.Name != "homelab" {
			t.Errorf("name = %q, want homelab from the main checkout's huddle.json", got.Name)
		}
	})
	t.Run("an empty name falls back to the folder", func(t *testing.T) {
		root := filepath.Join(realPath(t, t.TempDir()), "shop")
		writeProjectName(t, root, `{"name":""}`)
		if got := projectOf(root); got.Name != "shop" {
			t.Errorf("name = %q, want shop", got.Name)
		}
	})
	t.Run("an unreadable file falls back to the folder", func(t *testing.T) {
		root := filepath.Join(realPath(t, t.TempDir()), "shop")
		writeProjectName(t, root, `{"name":`)
		if got := projectOf(root); got.Name != "shop" {
			t.Errorf("name = %q, want shop", got.Name)
		}
	})
}
