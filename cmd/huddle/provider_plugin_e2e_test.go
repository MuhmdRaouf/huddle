package main

// The provider-plugin e2e: a provider launcher as Claude Code runs it, `sh <plugin>/dist/run
// <bundle>.js [args…]`, against the huddle binary the launcher finds under AGENTS_HUDDLE_HOME —
// the same binary, so a provider plugin's CLI word is `huddle provider <p> cli`. Every home and
// config dir is the test's own; the hook's detached download points at a dead local address, so
// nothing here touches the network or the real ~/.claude, ~/.agents or ~/.config.

import (
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// providerLauncherEnv is the environment one launcher run needs: a temp home and Claude config
// dir, and the huddle binary home the launcher looks its pinned version's binary in.
func providerLauncherEnv(t *testing.T, home string) []string {
	t.Helper()
	return append(
		os.Environ(),
		"HOME="+home,
		"CLAUDE_CONFIG_DIR="+filepath.Join(home, "claude-config"),
		"AGENTS_HUDDLE_HOME="+filepath.Join(home, "huddle-bin"),
	)
}

// providerLauncherRun runs the committed launcher as Claude Code would and returns its output,
// exit code and the error a start failure is.
func providerLauncherRun(t *testing.T, launcher string, env []string, args ...string) (output []byte, code int, err error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{launcher}, args...)...)
	cmd.Env = env
	output, err = cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return output, exit.ExitCode(), nil
	}
	return output, 0, err
}

// providerHookCleanup watches the hook's detached worker out before the temp dir goes: registered
// after t.TempDir(), cleanups run last-in first-out, so this lands ahead of the temp dir's own
// removal. The fetcher's first act on the tree is the version dir, so the wait starts there —
// a lock check alone can pass while the worker is still forking, and its mkdir would then rebuild
// the tree after the removal. From there it waits until no lock and no process names the temp
// home, up to 15 s, and kills whatever is left: a worker writing after the removal only rebuilds
// the dir it writes into.
func providerHookCleanup(t *testing.T, home, version string) {
	t.Helper()
	t.Cleanup(func() {
		binHome := filepath.Join(home, "huddle-bin")
		deadline := time.Now().Add(15 * time.Second)
		for {
			_, err := os.Stat(filepath.Join(binHome, version))
			if err == nil || !time.Now().Before(deadline) {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		for providerLocksLive(binHome) || len(providerWorkerPids(home)) > 0 {
			if !time.Now().Before(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, pid := range providerWorkerPids(home) {
			_ = exec.Command("kill", pid).Run()
		}
	})
}

// providerWorkerPids names the live processes whose command line names the temp home.
func providerWorkerPids(home string) []string {
	out, err := exec.Command("pgrep", "-f", home).Output()
	if err != nil {
		return nil // nothing matched, or pgrep is missing: nothing to wait for or kill
	}
	pids := []string{}
	for _, field := range strings.Fields(string(out)) {
		if _, err := strconv.Atoi(field); err == nil {
			pids = append(pids, field)
		}
	}
	return pids
}

// providerLocksLive reports whether a detached fetch or build still holds one of its locks under
// the binary home.
func providerLocksLive(binHome string) bool {
	live := false
	_ = filepath.WalkDir(binHome, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil // gone already, or unreadable: no lock to wait for here
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Name() == ".huddle.fetch.lock" || entry.Name() == ".huddle.build.lock" {
			live = true
			return fs.SkipAll
		}
		return nil
	})
	return live
}

// TestProviderPluginLauncherE2E drives the zai plugin's launcher end to end: the launcher a copy
// outside the repository, as an installed plugin runs it, so the source step finds no checkout and
// the release path — the installed binary, or the dead download base — is what the launcher meets.
func TestProviderPluginLauncherE2E(t *testing.T) {
	release := freshReleaseOf(t)
	pluginDir := devCopyPlugin(t, "zai")
	launcher := filepath.Join(pluginDir, "dist", "run")

	t.Run("the cli bundle runs the huddle binary's provider cli word", func(t *testing.T) {
		home := t.TempDir()
		binDir := filepath.Join(home, "huddle-bin", release.version)
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("make the binary home: %v", err)
		}
		bin := filepath.Join(binDir, "huddle")
		if err := copyFile(huddleBin, bin); err != nil {
			t.Fatalf("install the huddle binary: %v", err)
		}
		if err := os.Chmod(bin, 0o755); err != nil {
			t.Fatalf("chmod the binary: %v", err)
		}
		env := providerLauncherEnv(t, home)

		direct := exec.Command(huddleBin, "provider", "zai", "cli", "--help")
		direct.Env = env
		want, err := direct.Output()
		if err != nil {
			t.Fatalf("huddle provider zai cli --help: %v", err)
		}
		if len(want) == 0 {
			t.Fatal("huddle provider zai cli --help printed nothing")
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
	})

	t.Run("a hook line exits 0 at once and prints nothing with no binary", func(t *testing.T) {
		home := t.TempDir()
		// Registered after t.TempDir(), so the detached worker is waited out before the temp dir
		// is removed.
		providerHookCleanup(t, home, release.version)
		env := providerLauncherEnv(t, home)
		// The hook starts one detached download; the dead local base keeps it off the network.
		env = append(env, "HUDDLE_DOWNLOAD_BASE=http://127.0.0.1:1")
		// The source step only builds a checkout whose git names a commit; this empty dir is none,
		// so the hook can never start a build of the working tree.
		source := filepath.Join(home, "source")
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatalf("make the empty source dir: %v", err)
		}
		env = append(env, "HUDDLE_SOURCE="+source)

		started := time.Now()
		output, code, err := providerLauncherRun(t, launcher, env, "zai.js", "setup", "--hook")
		if err != nil {
			t.Fatalf("run the launcher: %v", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("the hook took %s, want under 1 s", elapsed)
		}
		if code != 0 {
			t.Fatalf("the hook exited %d:\n%s", code, output)
		}
		if len(output) != 0 {
			t.Fatalf("the hook printed %q, want nothing", output)
		}
	})
}

// copyFile copies one file's bytes and mode.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	info, err := in.Stat()
	if err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode())
}
