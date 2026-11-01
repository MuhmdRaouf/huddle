// The checkout: the project and the branch a directory sits in, read from the checkout's own
// files — the .git entry and its HEAD — the way git rev-parse answers them, so a hook names
// them without starting a git process.

package hooks

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// checkout is where a directory sits: Root is the checkout holding it and Branch the branch
// that checkout is on. Key is the repository's main checkout, which every linked worktree of the
// repository shares, and Name is what the project is called.
type checkout struct {
	Root, Branch, Key, Name string
}

// projectOf answers the checkout dir sits in, from the checkout's files alone. The walk up from
// dir stops at the first entry named .git: a directory .git makes its parent the root and the
// key, and the branch comes from <root>/.git/HEAD; a file .git (`gitdir: <path>`, a linked
// worktree) makes its own directory the root and the branch comes from <path>/HEAD, a relative
// <path> resolved against the directory holding the .git file. The worktree's key is the parent of
// its common git directory, <path>/commondir resolved against <path>, or that directory itself
// when it is a bare repository's (its name is not .git); a worktree with no
// readable commondir keys on its own root. With no .git above it, dir is the root and the key and
// names no branch. The name is the "name" in <key>/.agents/huddle/huddle.json, else the key's
// folder. An empty dir reads the hook's working directory; when even that is unknown, nothing is
// named.
func projectOf(dir string) checkout {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return checkout{}
		}
		dir = wd
	}
	found := checkout{Root: dir, Key: dir}
	for at := dir; ; at = filepath.Dir(at) {
		dot := filepath.Join(at, ".git")
		info, err := os.Stat(dot)
		if err == nil {
			found.Root, found.Key = at, at
			if info.IsDir() {
				found.Branch = headBranchOf(filepath.Join(dot, "HEAD"))
			} else if gitdir := worktreeGitdirOf(dot); gitdir != "" {
				found.Branch = headBranchOf(filepath.Join(gitdir, "HEAD"))
				if common := commonDirOf(gitdir); common != "" {
					found.Key = keyOfCommonDir(common)
				}
			}
			break
		}
		if filepath.Dir(at) == at {
			break
		}
	}
	found.Name = projectNameOf(found.Key)
	return found
}

// keyOfCommonDir answers the project a repository's common git directory stands for: the main
// checkout around a .git directory, else the directory itself, a bare repository.
func keyOfCommonDir(common string) string {
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common
}

// projectNameOf answers the "name" in the project's huddle.json, else the project folder's name.
// A file that is missing, torn or names nothing falls back to the folder.
func projectNameOf(key string) string {
	if key == "" {
		return ""
	}
	if raw, err := os.ReadFile(filepath.Join(key, cli.FileRel)); err == nil {
		if value, err := jsfmt.DecodeValue(raw); err == nil {
			if name, _ := value.Get("name").TextOf(); strings.TrimSpace(name) != "" {
				return name
			}
		}
	}
	return filepath.Base(key)
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

// worktreeGitdirOf answers the directory a linked worktree's .git file points at: the
// `gitdir: <path>` line, a relative <path> resolved against the directory that holds the .git
// file. A .git file that names no gitdir answers no path.
func worktreeGitdirOf(dot string) string {
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
	return gitdir
}

// commonDirOf answers the git directory a linked worktree shares with its repository: the path
// in <gitdir>/commondir, usually ../.., resolved against gitdir. A missing or empty commondir
// answers no path.
func commonDirOf(gitdir string) string {
	raw, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	if err != nil {
		return ""
	}
	common := strings.TrimSpace(string(raw))
	if common == "" {
		return ""
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitdir, common)
	}
	return filepath.Clean(common)
}
