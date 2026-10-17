package shared

import "testing"

// fakeFS is a fake fs from path → content; a path present with ok false is a directory.
type fakeFS map[string]fakeEntry

type fakeEntry struct {
	content string
	isDir   bool
}

func dir() fakeEntry                { return fakeEntry{isDir: true} }
func file(content string) fakeEntry { return fakeEntry{content: content} }

func (f fakeFS) Exists(path string) bool {
	_, ok := f[path]
	return ok
}

func (f fakeFS) ReadFile(path string) (string, bool) {
	entry, ok := f[path]
	if !ok || entry.isDir {
		return "", false
	}
	return entry.content, true
}

func TestRepoOf(t *testing.T) {
	t.Run("names the directory holding a .git directory, near or far up", func(t *testing.T) {
		fs := fakeFS{"/w/app/.git": dir(), "/w/app/src/a.ts": file("")}
		for cwd, want := range map[string]string{
			"/w/app":     "/w/app",
			"/w/app/src": "/w/app",
		} {
			if got, ok := RepoOf(cwd, fs); !ok || got != want {
				t.Errorf("RepoOf(%q) = %q, %v; want %q, ok", cwd, got, ok, want)
			}
		}
	})

	t.Run("folds a linked worktree into the checkout its .git file points at", func(t *testing.T) {
		fs := fakeFS{"/w/wt/.git": file("gitdir: /w/main/.git/worktrees/wt\n")}
		for cwd, want := range map[string]string{
			"/w/wt":               "/w/main",
			"/w/wt/plugins/radar": "/w/main",
		} {
			if got, ok := RepoOf(cwd, fs); !ok || got != want {
				t.Errorf("RepoOf(%q) = %q, %v; want %q, ok", cwd, got, ok, want)
			}
		}
	})

	t.Run("resolves a relative gitdir against the directory holding the .git file", func(t *testing.T) {
		fs := fakeFS{"/w/wt/.git": file("gitdir: ../main/.git/worktrees/wt")}
		if got, ok := RepoOf("/w/wt", fs); !ok || got != "/w/main" {
			t.Errorf("RepoOf(\"/w/wt\") = %q, %v; want \"/w/main\", ok", got, ok)
		}
	})

	t.Run("keeps the checkout itself when the .git file is not a worktree pointer", func(t *testing.T) {
		fs := fakeFS{"/w/app/.git": file("gitdir: /somewhere/else")}
		if got, ok := RepoOf("/w/app", fs); !ok || got != "/w/app" {
			t.Errorf("RepoOf(\"/w/app\") = %q, %v; want \"/w/app\", ok", got, ok)
		}
	})

	t.Run("returns null when no parent holds a .git", func(t *testing.T) {
		for _, cwd := range []string{"/w/app/src", "/"} {
			if got, ok := RepoOf(cwd, fakeFS{}); ok {
				t.Errorf("RepoOf(%q) = %q, want not ok", cwd, got)
			}
		}
	})
}

func TestHomeShort(t *testing.T) {
	t.Run("shows a home-rooted path from the tilde, others as they are", func(t *testing.T) {
		cases := []struct{ path, want string }{
			{"/Users/raouf/work", "~/work"},
			{"/home/u/x", "~/x"},
			{"/opt/app", "/opt/app"},
			{"relative/path", "relative/path"},
		}
		for _, tt := range cases {
			if got := HomeShort(tt.path); got != tt.want {
				t.Errorf("HomeShort(%q) = %q, want %q", tt.path, got, tt.want)
			}
		}
	})
}

func TestRepoName(t *testing.T) {
	t.Run("is the last non-empty segment, the whole path when there is none", func(t *testing.T) {
		cases := []struct{ path, want string }{
			{"/Users/raouf/work/claude-code-plugins", "claude-code-plugins"},
			{"/a/b/app/", "app"},
			{"app", "app"},
			{"/", "/"},
			{`a\b\c`, `a\b\c`},
		}
		for _, tt := range cases {
			if got := RepoName(tt.path); got != tt.want {
				t.Errorf("RepoName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		}
	})
}
