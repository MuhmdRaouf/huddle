package main

// The provider-plugin e2e: a provider launcher as Claude Code runs it, `sh <plugin>/dist/run
// <bundle>.js [args…]`, against the huddle binary the launcher finds under AGENTS_HUDDLE_HOME —
// the same binary, so a provider plugin's CLI word is `huddle provider <p> cli`. Every home and
// config dir is the test's own; the hook's detached download points at a dead local address, so
// nothing here touches the network or the real ~/.claude, ~/.agents or ~/.config.

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// TestProviderPluginLauncherE2E drives the zai plugin's launcher end to end.
func TestProviderPluginLauncherE2E(t *testing.T) {
	release := freshReleaseOf(t)
	pluginDir := filepath.Join(freshRoot, "plugins", "zai-plugin-cc")
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

	t.Run("the ensure bundle as a hook exits 0 at once and prints nothing with no binary", func(t *testing.T) {
		home := t.TempDir()
		env := providerLauncherEnv(t, home)
		// The hook starts one detached download; the dead local base keeps it off the network.
		env = append(env, "HUDDLE_DOWNLOAD_BASE=http://127.0.0.1:1")

		started := time.Now()
		output, code, err := providerLauncherRun(t, launcher, env, "zai-ensure.js", "--hook")
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
		// The detached fetch fails at once against the dead base; wait for its lock so the temp
		// home's removal never races a fetcher still writing under it.
		lock := filepath.Join(home, "huddle-bin", release.version, ".huddle.fetch.lock")
		deadline := time.Now().Add(10 * time.Second)
		for _, err := os.Stat(lock); err == nil && time.Now().Before(deadline); _, err = os.Stat(lock) {
			time.Sleep(50 * time.Millisecond)
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
