package board

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// originWait is how long git may run; originReap is how long the read waits
// after the kill for a process git left behind to let go of the output pipe.
const (
	originWait = time.Second
	originReap = 100 * time.Millisecond
)

// OriginOf answers a repository's origin URL, the value of
// `git -C repoRoot config --get remote.origin.url`, empty when the repository
// has none. The repository's own config file answers it whenever it can, with
// no process and no timing, so every caller gets the same board key; a shape
// the file read leaves out goes to git (see originFromGit).
func OriginOf(ctx context.Context, repoRoot string) string {
	if url, ok := originFromConfigFile(repoRoot); ok {
		return url
	}
	return originFromGit(ctx, repoRoot)
}

// originFromGit asks git: one read that ends by originWait plus originReap,
// empty on any error, so a git that never answers — a child of it holding the
// pipe included — keeps the path key and no caller waits on it.
func originFromGit(ctx context.Context, repoRoot string) string {
	callCtx, cancel := context.WithTimeout(ctx, originWait)
	defer cancel()
	cmd := exec.CommandContext(callCtx, "git", "-C", repoRoot, "config", "--get", "remote.origin.url")
	cmd.WaitDelay = originReap
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RepoRootOf answers the dir a board's repo lives in: the parent of .agents.
func RepoRootOf(boardRoot string) string {
	return filepath.Dir(filepath.Dir(boardRoot))
}
