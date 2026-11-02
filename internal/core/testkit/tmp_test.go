package testkit

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// refusingTB is a testing.TB whose temp root is fixed and whose Fatalf records its message and
// stops the calling goroutine, so a test can watch TempDir refuse.
type refusingTB struct {
	testing.TB

	root  string
	fatal string
}

func (r *refusingTB) TempDir() string { return r.root }

func (r *refusingTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// fatalOf runs fn on its own goroutine and returns the message the fake's Fatalf recorded.
func fatalOf(fake *refusingTB, fn func(testing.TB)) string {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(fake)
	}()
	<-done
	return fake.fatal
}

func TestTempDirRunsInParallelTests(t *testing.T) {
	for _, name := range []string{"first", "second", "third"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := TempDir(t, "parallel-")
			if _, err := os.Stat(dir); err != nil {
				t.Fatalf("temp dir %s: %v", dir, err)
			}
		})
	}
}

func TestTempDirRefusesARootInsideAGitWorkTree(t *testing.T) {
	t.Run("a .git folder above the temp root", func(t *testing.T) {
		top := t.TempDir()
		if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(top, "scratch", "tmp", "TestX", "001")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		fake := &refusingTB{TB: t, root: root}

		message := fatalOf(fake, func(tb testing.TB) { TempDir(tb, "inside-") })

		if !strings.Contains(message, "TMPDIR") || !strings.Contains(message, resolved(t, top)) {
			t.Fatalf("message = %q, want it to name TMPDIR and the work tree %s", message, top)
		}
	})

	t.Run("a .git file above the temp root, as a linked worktree has", func(t *testing.T) {
		top := t.TempDir()
		if err := os.WriteFile(filepath.Join(top, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		fake := &refusingTB{TB: t, root: filepath.Join(top, "tmp", "001")}

		message := fatalOf(fake, func(tb testing.TB) { TempDir(tb, "inside-") })

		if message == "" {
			t.Fatal("TempDir accepted a root inside a linked worktree")
		}
	})
}

func TestWorkTreeAbove(t *testing.T) {
	t.Run("finds the nearest folder holding .git", func(t *testing.T) {
		top := t.TempDir()
		if err := os.Mkdir(filepath.Join(top, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		deep := filepath.Join(top, "a", "b")
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}

		got, ok := workTreeAbove(deep)

		if !ok || got != resolved(t, top) {
			t.Fatalf("workTreeAbove(%s) = %q, %v, want %q, true", deep, got, ok, resolved(t, top))
		}
	})

	t.Run("answers false outside any work tree", func(t *testing.T) {
		if top, ok := workTreeAbove(t.TempDir()); ok {
			t.Fatalf("workTreeAbove(temp dir) = %q, true, want false", top)
		}
	})
}

// TestARepositoryMadeInATempDirIsFoundFromInsideIt: the refusal stops a TMPDIR inside a checkout,
// and leaves a test's own repository alone.
func TestARepositoryMadeInATempDirIsFoundFromInsideIt(t *testing.T) {
	IsolateGitConfig(t)
	repo := MakeRepo(t)
	folder := filepath.Join(repo.Dir, "deep", "folder")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}

	top, err := Git(folder, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatalf("git from a folder inside the temp repo = %v, want its top", err)
	}
	if got, want := strings.TrimSpace(top), repo.Dir; got != want {
		t.Fatalf("top = %q, want %q", got, want)
	}
}

// resolved is dir after symlinks, the form workTreeAbove reports.
func resolved(t *testing.T, dir string) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
