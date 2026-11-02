package adapters

import (
	"context"
	"path/filepath"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
)

// gitRequireTop refuses dir unless it is the top of its own repository (or of a linked worktree).
// Git walks up from a folder with no repository of its own to the repository around it, so a write
// there would land in that other repository: the user's own branch. A mismatch and a failed lookup
// both answer not_a_repo, and the caller writes nothing.
func gitRequireTop(ctx context.Context, dir string) *ports.GitError {
	notARepo := &ports.GitError{Kind: ports.GitErrorNotARepo, Path: dir}
	top, err := GitValue(ctx, dir, []string{"rev-parse", "--show-toplevel"}, GitRunOptions{})
	if err != nil {
		return notARepo
	}
	// macOS reports /private/var for a folder named /var.
	realTop, topErr := filepath.EvalSymlinks(top)
	realDir, dirErr := filepath.EvalSymlinks(dir)
	if topErr != nil || dirErr != nil || realTop != realDir {
		return notARepo
	}
	return nil
}
