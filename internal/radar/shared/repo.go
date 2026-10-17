package shared

import (
	"path"
	"regexp"
	"strings"
)

// RepoFS is the slice of the filesystem RepoOf may look at: enough to tell a `.git`
// directory from a `.git` file. ReadFile reports ok false for a missing file and for a
// `.git` directory, which never reads as text.
type RepoFS interface {
	Exists(path string) bool
	ReadFile(path string) (content string, ok bool)
}

// homePrefix is the home-directory head a path shows without.
var homePrefix = regexp.MustCompile(`^/(?:Users|home)/[^/]+`)

// HomeShort shows a path from the home directory: /Users/raouf/work → ~/work,
// /home/u/x → ~/x; any other path stands.
func HomeShort(p string) string {
	return homePrefix.ReplaceAllString(p, "~")
}

// RepoName is the name a repo path stands for: its last non-empty segment
// ("claude-code-plugins", "app"). A name never shows as a path — the full path stays
// where the workspace is shown, in tooltips.
func RepoName(p string) string {
	base := ""
	for _, part := range strings.Split(p, "/") {
		if part != "" {
			base = part
		}
	}
	if base == "" {
		return p
	}
	return base
}

// RepoOf returns the repo root cwd sits in, or ok false when no parent holds a `.git`.
// A linked worktree's `.git` is a file pointing into the main checkout, so a worktree
// folds into the repo it works on.
func RepoOf(cwd string, fs RepoFS) (string, bool) {
	dir := cwd
	for {
		gitPath := path.Join(dir, ".git")
		if fs.Exists(gitPath) {
			content, _ := fs.ReadFile(gitPath)
			if repo, ok := worktreeRepoOf(dir, content); ok {
				return repo, true
			}
			return dir, true
		}
		parent := path.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// worktreePointer is a linked worktree's `.git` file: "gitdir: <x>/.git/worktrees/<name>".
var worktreePointer = regexp.MustCompile(`^gitdir:` + jsSpaceClass + `*(\S+)` + jsSpaceClass + `*$`)

// worktreeRepoOf reads the main checkout a worktree's .git pointer names; any other content
// reads as no pointer. A relative gitdir resolves against the directory holding the .git
// file.
func worktreeRepoOf(dir, content string) (string, bool) {
	match := worktreePointer.FindStringSubmatch(strings.TrimFunc(content, isJSSpace))
	if match == nil {
		return "", false
	}
	raw := match[1]
	var gitdir string
	if path.IsAbs(raw) {
		gitdir = path.Clean(raw)
	} else {
		gitdir = path.Join(dir, raw)
	}
	at := strings.LastIndex(gitdir, "/.git/worktrees/")
	if at <= 0 {
		return "", false
	}
	return gitdir[:at], true
}
