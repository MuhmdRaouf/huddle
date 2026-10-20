package board

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// OriginOf answers a repository's origin URL from git's own config: one read
// bounded at a second, empty on any error, so a repo without an origin — or a
// git that never answers — keeps the path key and no caller waits on it.
func OriginOf(ctx context.Context, repoRoot string) string {
	callCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, err := exec.CommandContext(callCtx, "git", "-C", repoRoot, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RepoRootOf answers the dir a board's repo lives in: the parent of .agents.
func RepoRootOf(boardRoot string) string {
	return filepath.Dir(filepath.Dir(boardRoot))
}
