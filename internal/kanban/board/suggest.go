package board

import (
	"os"
	"path/filepath"
	"strings"
)

// SuggestBoard answers the board a branch names: the branch itself when it is a board, else the
// last segment of its path (feat/login names login). Names fold the way boards do.
func SuggestBoard(branch string, boards []string) (string, bool) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", false
	}
	last := branch
	if i := strings.LastIndex(branch, "/"); i >= 0 {
		last = branch[i+1:]
	}
	for _, candidate := range []string{branch, last} {
		name := boardName(candidate)
		for _, board := range boards {
			if board == name {
				return board, true
			}
		}
	}
	return "", false
}

// SuggestBoardAt answers the board the git work tree around dir names: its branch's board, else
// the board named like the work tree's directory.
func SuggestBoardAt(dir string, boards []string) (string, bool) {
	branch, tree := workTree(dir)
	if name, ok := SuggestBoard(branch, boards); ok {
		return name, true
	}
	return SuggestBoard(tree, boards)
}

// WorkingBranch answers the branch checked out in the git work tree around dir, "" on a detached
// head or outside a repository.
func WorkingBranch(dir string) string {
	branch, _ := workTree(dir)
	return branch
}

// workTree answers the branch checked out in the work tree around dir and that work tree's
// directory name.
func workTree(dir string) (branch, name string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	for d := abs; ; d = filepath.Dir(d) {
		gitDir, found, ok := gitDirIn(d)
		if found {
			if !ok {
				return "", ""
			}
			head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return "", filepath.Base(d)
			}
			ref, onBranch := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
			if !onBranch {
				return "", filepath.Base(d)
			}
			return ref, filepath.Base(d)
		}
		if filepath.Dir(d) == d {
			return "", ""
		}
	}
}
