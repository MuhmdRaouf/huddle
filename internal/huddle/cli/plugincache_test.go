package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakePluginCache writes Claude Code's installed_plugins.json under configDir, naming huddle's
// install at commit sha and a provider plugin at another commit.
func fakePluginCache(t *testing.T, configDir, sha string) {
	t.Helper()
	dir := filepath.Join(configDir, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":2,"plugins":{
		"zai-plugin-cc@muhmdraouf":[{"scope":"user","installPath":"` + dir + `/cache/muhmdraouf/zai-plugin-cc/aaaaaaaaaaaa",
			"version":"aaaaaaaaaaaa","installedAt":"2026-10-24T10:00:00.000Z","lastUpdated":"2026-10-24T10:00:00.000Z",
			"gitCommitSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}],
		"huddle@muhmdraouf":[{"scope":"user","installPath":"` + dir + `/cache/muhmdraouf/huddle/` + sha[:12] + `",
			"version":"` + sha[:12] + `","installedAt":"2026-10-24T10:00:00.000Z","lastUpdated":"2026-10-24T10:00:00.000Z",
			"gitCommitSha":"` + sha + `"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeLauncherCurrent points the launcher home's current link at src/<commit>/huddle, the way a
// source build repoints it.
func fakeLauncherCurrent(t *testing.T, launcherHome, commit string) {
	t.Helper()
	bin := filepath.Join(launcherHome, "src", commit, "huddle")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("src", commit, "huddle"), filepath.Join(launcherHome, "current")); err != nil {
		t.Fatal(err)
	}
}

// TestStatusNamesThePluginCacheAndBinaryCommits pins the status line about the deploy: the
// commit Claude Code's plugin cache holds beside the commit the launcher's current binary was
// built from, marked when the cache is behind.
func TestStatusNamesThePluginCacheAndBinaryCommits(t *testing.T) {
	const cacheSha = "052c8e49c9ff0123456789abcdef0123456789ab"
	cases := []struct {
		name   string
		binary string
		want   string
	}{
		{"behind", "38e9e40e321e", "plugin cache 052c8e49c9ff · binary 38e9e40e321e (cache behind)"},
		{"same", "052c8e49c9ff", "plugin cache 052c8e49c9ff · binary 052c8e49c9ff (same)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, launcher := t.TempDir(), t.TempDir()
			fakePluginCache(t, config, cacheSha)
			fakeLauncherCurrent(t, launcher, tc.binary)
			w := world(t, t.TempDir(), map[string]string{
				"CLAUDE_CONFIG_DIR":  config,
				"AGENTS_HUDDLE_HOME": launcher,
			})
			if got := PluginCacheLine(context.Background(), w); got != tc.want {
				t.Errorf("PluginCacheLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestStatusFindsTheCacheUnderHome pins the defaults: with neither variable set the cache is
// read from $HOME/.claude and the binary from $HOME/.agents/huddle-bin.
func TestStatusFindsTheCacheUnderHome(t *testing.T) {
	home := t.TempDir()
	fakePluginCache(t, filepath.Join(home, ".claude"), "052c8e49c9ff0123456789abcdef0123456789ab")
	launcher := filepath.Join(home, ".agents", "huddle-bin")
	if err := os.MkdirAll(launcher, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeLauncherCurrent(t, launcher, "38e9e40e321e")
	w := world(t, t.TempDir(), map[string]string{"HOME": home})
	want := "plugin cache 052c8e49c9ff · binary 38e9e40e321e (cache behind)"
	if got := PluginCacheLine(context.Background(), w); got != want {
		t.Errorf("PluginCacheLine = %q, want %q", got, want)
	}
}

// TestStatusSaysNothingWithoutAPluginInstall pins the quiet case: a machine with no plugin
// install (no file, or no huddle entry in it) prints no cache line at all.
func TestStatusSaysNothingWithoutAPluginInstall(t *testing.T) {
	launcher := t.TempDir()
	fakeLauncherCurrent(t, launcher, "38e9e40e321e")

	empty := t.TempDir()
	w := world(t, t.TempDir(), map[string]string{"CLAUDE_CONFIG_DIR": empty, "AGENTS_HUDDLE_HOME": launcher})
	if got := PluginCacheLine(context.Background(), w); got != "" {
		t.Errorf("no installed_plugins.json: PluginCacheLine = %q, want nothing", got)
	}

	other := t.TempDir()
	if err := os.MkdirAll(filepath.Join(other, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "plugins", "installed_plugins.json"),
		[]byte(`{"version":2,"plugins":{"zai-plugin-cc@muhmdraouf":[{"scope":"user","gitCommitSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w = world(t, t.TempDir(), map[string]string{"CLAUDE_CONFIG_DIR": other, "AGENTS_HUDDLE_HOME": launcher})
	if got := PluginCacheLine(context.Background(), w); got != "" {
		t.Errorf("no huddle entry: PluginCacheLine = %q, want nothing", got)
	}
}

// TestStatusNamesAnUnknownBinary pins the case the launcher's current link does not name a
// source build: the cache commit still prints, the binary reads unknown, and no marker guesses.
func TestStatusNamesAnUnknownBinary(t *testing.T) {
	config := t.TempDir()
	fakePluginCache(t, config, "052c8e49c9ff0123456789abcdef0123456789ab")
	w := world(t, t.TempDir(), map[string]string{"CLAUDE_CONFIG_DIR": config, "AGENTS_HUDDLE_HOME": t.TempDir()})
	want := "plugin cache 052c8e49c9ff · binary unknown"
	if got := PluginCacheLine(context.Background(), w); got != want {
		t.Errorf("PluginCacheLine = %q, want %q", got, want)
	}
}
