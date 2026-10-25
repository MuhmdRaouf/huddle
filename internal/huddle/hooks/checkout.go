// The checkout: the project and the branch a directory sits in, read from the checkout's own
// files — the .git entry and its HEAD — the way git rev-parse answers them, so a hook names
// them without starting a git process.

package hooks

import (
	"os"
	"path/filepath"
	"strings"
)

// checkoutOf answers the project and the branch of the checkout dir sits in, from the
// checkout's files alone. The walk up from dir stops at the first entry named .git: a
// directory .git makes its parent the project and reads the branch from <project>/.git/HEAD; a
// file .git (`gitdir: <path>`, a linked worktree) makes its own directory the project and reads
// the branch from <path>/HEAD, a relative path resolved against the directory holding the .git
// file. With no .git above it, dir is the project and names no branch. An empty dir reads
// the hook's working directory; when even that is unknown, nothing is named.
func checkoutOf(dir string) (project, branch string) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", ""
		}
		dir = wd
	}
	for at := dir; ; at = filepath.Dir(at) {
		dot := filepath.Join(at, ".git")
		info, err := os.Stat(dot)
		if err == nil {
			if info.IsDir() {
				return at, headBranchOf(filepath.Join(dot, "HEAD"))
			}
			return at, headBranchOf(worktreeHeadOf(dot))
		}
		if filepath.Dir(at) == at {
			return dir, ""
		}
	}
}

// headBranchOf answers the branch a HEAD file names: `ref: refs/heads/<name>` gives <name>; a
// bare commit hash is a detached HEAD, and the answer is HEAD, the word git itself prints for
// it. A HEAD that cannot be read names no branch.
func headBranchOf(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "ref: refs/heads/"); ok {
		return ref
	}
	return "HEAD"
}

// worktreeHeadOf answers the path of the HEAD file a linked worktree's .git file points at: the
// `gitdir: <path>` line joined with HEAD, a relative <path> resolved against the directory that
// holds the .git file. A .git file that names no gitdir answers no path.
func worktreeHeadOf(dot string) string {
	raw, err := os.ReadFile(dot)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(dot), gitdir)
	}
	return filepath.Join(gitdir, "HEAD")
}
