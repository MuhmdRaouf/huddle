package board

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// gitEnvOverrides are the variables that change which repository or which
// config `git config --get` reads; GIT_CONFIG_COUNT carries its own
// GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n.
func gitEnvOverrides() []string {
	return []string{"GIT_DIR", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS"}
}

// localConfigPath answers the config file git reads for the repository that
// holds dir: the nearest .git above it is either the git directory or a file
// "gitdir: <path>" naming it (a worktree or a submodule), and a git directory
// with a commondir file (a linked worktree) shares the config of the
// directory that file names. It answers false for every case git decides
// better than a file read: no repository found, an environment variable that
// moves the repository or the config (gitEnvOverrides), a .git that is not a
// git directory, or a per-worktree config. It does not model git's
// dubious-ownership refusal or GIT_CEILING_DIRECTORIES.
func localConfigPath(dir string) (string, bool) {
	for _, name := range gitEnvOverrides() {
		if os.Getenv(name) != "" {
			return "", false
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for d := abs; ; d = filepath.Dir(d) {
		gitDir, found, ok := gitDirIn(d)
		if found {
			if !ok {
				return "", false
			}
			return configOf(gitDir)
		}
		if filepath.Dir(d) == d {
			return "", false
		}
	}
}

// gitDirIn answers the git directory d's .git names. found is false when d
// has no .git; ok is false when it has one that is not a usable git directory.
func gitDirIn(d string) (gitDir string, found, ok bool) {
	dotGit := filepath.Join(d, ".git")
	info, err := os.Stat(dotGit)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, false
	}
	if err != nil {
		return "", true, false
	}
	if info.IsDir() {
		gitDir = dotGit
	} else {
		data, err := os.ReadFile(dotGit)
		if err != nil {
			return "", true, false
		}
		rest, isGitdir := strings.CutPrefix(string(data), "gitdir:")
		if !isGitdir {
			return "", true, false
		}
		gitDir = resolveFrom(d, strings.TrimSpace(rest))
	}
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err != nil {
		return "", true, false
	}
	return gitDir, true, true
}

// configOf answers the shared config of a git directory.
func configOf(gitDir string) (string, bool) {
	if _, err := os.Stat(filepath.Join(gitDir, "config.worktree")); err == nil {
		return "", false
	}
	common := gitDir
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	switch {
	case err == nil:
		common = resolveFrom(gitDir, strings.TrimSpace(string(data)))
	case !errors.Is(err, fs.ErrNotExist):
		return "", false
	}
	return filepath.Join(common, "config"), true
}

func resolveFrom(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}
