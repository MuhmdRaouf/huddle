package main

// The development-channel e2e: an installed provider launcher with no release to download builds the
// huddle binary of the marketplace checkout's own commit and runs the provider CLI on it. The
// repository's tracked files stand in for the checkout Claude Code clones under
// <CLAUDE_CONFIG_DIR>/plugins/marketplaces/muhmdraouf; the plugin dir is a copy outside that tree,
// the download base a dead local address and every home the test's own — the source build is the only
// way to a binary. Skipped where Go is not on PATH.

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// devGoCacheEnv pins the build and module caches to the ones the ambient toolchain already filled:
// the launcher's go build runs under a temp HOME, and a cache-less build would re-fetch the module
// tree instead of reusing what the suite compiled.
func devGoCacheEnv(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE").Output()
	if err != nil {
		t.Fatalf("go env GOCACHE GOMODCACHE: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("go env GOCACHE GOMODCACHE printed %d lines, want 2", len(lines))
	}
	env := []string{}
	if lines[0] != "" && lines[0] != "off" {
		env = append(env, "GOCACHE="+lines[0])
	}
	if lines[1] != "" {
		env = append(env, "GOMODCACHE="+lines[1])
	}
	return env
}

// devGit runs one git word in dir hermetically: a temp home, no global or system config, a fixed
// identity, no signing — the checkout is the test's own.
func devGit(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "commit.gpgsign=false",
		"-c", "user.email=dev@example.com",
		"-c", "user.name=dev",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = isoenv.Environ(t, home, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// devMarketplace copies the repository's tracked files into the home's marketplace path and commits
// them — the checkout Claude Code keeps for a marketplace it added — and answers the short commit
// the launcher's source step names.
func devMarketplace(t *testing.T, home string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", freshRoot, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	root := filepath.Join(home, "claude-config", "plugins", "marketplaces", "muhmdraouf")
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" {
			continue
		}
		target := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("make %s: %v", filepath.Dir(target), err)
		}
		if err := copyFile(filepath.Join(freshRoot, rel), target); err != nil {
			t.Fatalf("copy %s: %v", rel, err)
		}
	}
	devGit(t, home, root, "init", "-q")
	devGit(t, home, root, "add", "-A")
	devGit(t, home, root, "commit", "-q", "-m", "dev channel")
	return strings.TrimSpace(devGit(t, home, root, "rev-parse", "--short=12", "HEAD"))
}

// devCopyPlugin copies one provider plugin's dir to a temp one outside the marketplace tree, modes
// intact: the installed copy Claude Code runs from its cache, with no Go sources beside it.
func devCopyPlugin(t *testing.T, provider string) string {
	t.Helper()
	src := filepath.Join(freshRoot, "plugins", provider+"-plugin-cc")
	dst := filepath.Join(t.TempDir(), "plugins", "cache", provider+"-plugin-cc")
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target)
	})
	if err != nil {
		t.Fatalf("copy the %s plugin: %v", provider, err)
	}
	return dst
}

// TestDevChannelE2E runs the development channel as an installed plugin lives it: no release to
// download, the marketplace checkout under CLAUDE_CONFIG_DIR, Go on PATH — the launcher builds the
// checkout's commit once, runs the provider CLI on it, and the next run reuses that build.
func TestDevChannelE2E(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH: the development channel has no way to build the binary")
	}
	home := t.TempDir()
	commit := devMarketplace(t, home)
	pluginDir := devCopyPlugin(t, "zai")
	launcher := filepath.Join(pluginDir, "dist", "run")
	env := append(providerLauncherEnv(t, home), "HUDDLE_DOWNLOAD_BASE=http://127.0.0.1:1")
	env = append(env, devGoCacheEnv(t)...)

	direct := exec.Command(huddleBin, "provider", "zai", "cli", "--help")
	direct.Env = env
	want, err := direct.Output()
	if err != nil {
		t.Fatalf("huddle provider zai cli --help: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("huddle provider zai cli --help printed nothing")
	}

	bin := filepath.Join(home, "huddle-bin", "src", commit, "huddle")

	t.Run("the first run builds the marketplace checkout's commit and runs it", func(t *testing.T) {
		if _, err := os.Stat(bin); err == nil {
			t.Fatal("the commit's binary exists before the first run; the channel is not fresh")
		}
		got, code, err := providerLauncherRun(t, launcher, env, "zai.js", "--help")
		if err != nil {
			t.Fatalf("run the launcher: %v", err)
		}
		if code != 0 {
			t.Fatalf("sh dist/run zai.js --help exited %d:\n%s", code, got)
		}
		if string(got) != string(want) {
			t.Fatalf("the launcher's output is not the provider cli's:\ngot  %q\nwant %q", got, want)
		}
		info, err := os.Stat(bin)
		if err != nil {
			t.Fatalf("the launcher never built %s: %v", bin, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Errorf("the built binary at %s is not executable", bin)
		}
	})

	t.Run("the next run reuses the build within 2 s", func(t *testing.T) {
		before, err := os.Stat(bin)
		if err != nil {
			t.Fatalf("stat the built binary: %v", err)
		}
		started := time.Now()
		got, code, err := providerLauncherRun(t, launcher, env, "zai.js", "--help")
		if err != nil {
			t.Fatalf("run the launcher: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("the reuse run took %s, want under 2 s", elapsed)
		}
		if code != 0 {
			t.Fatalf("the reuse run exited %d:\n%s", code, got)
		}
		if string(got) != string(want) {
			t.Fatalf("the reuse run's output is not the provider cli's:\ngot  %q\nwant %q", got, want)
		}
		after, err := os.Stat(bin)
		if err != nil {
			t.Fatalf("stat the built binary: %v", err)
		}
		if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
			t.Error("the reuse run rebuilt the binary instead of running the commit's build")
		}
	})
}
