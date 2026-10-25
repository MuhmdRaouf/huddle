package outside

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const pluginKey = "huddle@muhmdraouf"

// installedEntry is one registry entry, the shape Claude Code writes.
const installedEntry = `[{"scope":"user","installPath":"/x","version":"1"}]`

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// world is a temp Claude config dir with its plugins dir, and the env that points at it.
func world(t *testing.T) (string, map[string]string) {
	t.Helper()
	config := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config, "plugins"), 0o700); err != nil {
		t.Fatalf("mkdir plugins: %v", err)
	}
	return config, map[string]string{"HOME": config, "CLAUDE_CONFIG_DIR": config}
}

// install writes the installed-plugins registry with the given plugins table.
func install(t *testing.T, config, plugins string) {
	t.Helper()
	writeFile(t, filepath.Join(config, "plugins", "installed_plugins.json"), `{"version":2,"plugins":`+plugins+`}`)
}

func registryFile(config string) string {
	return filepath.Join(config, "plugins", "installed_plugins.json")
}

func settingsFile(config string) string {
	return filepath.Join(config, "settings.json")
}

func enabled(body string) string {
	return `{"enabledPlugins":{"` + pluginKey + `":` + body + `}}`
}

func TestPresence(t *testing.T) {
	presence := func(env map[string]string, projects ...string) Presence {
		return PluginPresence(context.Background(), env, "huddle", projects)
	}

	t.Run("presence: unknown while the registry is missing, half-written, or a foreign shape", func(t *testing.T) {
		config, env := world(t)
		if got := presence(env); got != PresenceUnknown { // no file yet
			t.Errorf("no registry: presence = %q, want unknown", got)
		}
		writeFile(t, registryFile(config), "{half-written")
		if got := presence(env); got != PresenceUnknown { // a torn write
			t.Errorf("torn registry: presence = %q, want unknown", got)
		}
		install(t, config, `{"`+pluginKey+`":"not a list"}`)
		if got := presence(env); got != PresenceUnknown { // not the shape Claude Code writes
			t.Errorf("registry without lists: presence = %q, want unknown", got)
		}
	})

	t.Run("presence: uninstalled when no key names the plugin, under any marketplace", func(t *testing.T) {
		config, env := world(t)
		install(t, config, `{"`+pluginKey+`":`+installedEntry+`}`)
		install(t, config, `{"other@market":`+installedEntry+`}`)
		if got := presence(env); got != PresenceUninstalled {
			t.Errorf("another plugin installed: presence = %q, want uninstalled", got)
		}
		install(t, config, `{}`)
		if got := presence(env); got != PresenceUninstalled { // nothing installed at all
			t.Errorf("empty registry: presence = %q, want uninstalled", got)
		}
		install(t, config, `{"huddle@fork":`+installedEntry+`}`)
		if got := presence(env); got != PresencePresent { // a fork counts
			t.Errorf("a fork of the plugin: presence = %q, want present", got)
		}
	})

	t.Run("presence: the user scope disables, a known project re-enables, unreadable settings wait", func(t *testing.T) {
		config, env := world(t)
		install(t, config, `{"`+pluginKey+`":`+installedEntry+`}`)
		if got := presence(env); got != PresencePresent { // no settings at all
			t.Errorf("no settings: presence = %q, want present", got)
		}
		writeFile(t, settingsFile(config), "{oops")
		if got := presence(env); got != PresenceUnknown { // unreadable settings
			t.Errorf("torn settings: presence = %q, want unknown", got)
		}
		writeFile(t, settingsFile(config), enabled("true"))
		if got := presence(env); got != PresencePresent {
			t.Errorf("user scope on: presence = %q, want present", got)
		}
		writeFile(t, settingsFile(config), enabled("false"))
		if got := presence(env); got != PresenceDisabled {
			t.Errorf("user scope off: presence = %q, want disabled", got)
		}
		project := t.TempDir()
		if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		if got := presence(env, project); got != PresenceDisabled { // the project says nothing
			t.Errorf("project without settings: presence = %q, want disabled", got)
		}
		writeFile(t, filepath.Join(project, ".claude", "settings.json"), enabled("false"))
		if got := presence(env, project); got != PresenceDisabled {
			t.Errorf("project scope off: presence = %q, want disabled", got)
		}
		writeFile(t, filepath.Join(project, ".claude", "settings.local.json"), enabled("true"))
		if got := presence(env, project); got != PresencePresent { // the local scope says yes
			t.Errorf("local scope on: presence = %q, want present", got)
		}
	})
}

func TestWatchRemoval(t *testing.T) {
	t.Run("watchRemoval: two consecutive agreeing checks act once; present and unknown reset", func(t *testing.T) {
		config, env := world(t)
		install(t, config, `{"`+pluginKey+`":`+installedEntry+`}`)
		var gone []string
		watch := WatchRemoval(RemovalOptions{
			Env:      env,
			Plugin:   "huddle",
			Projects: func() []string { return nil },
			Interval: time.Hour,
			Gone:     func(kind Gone) { gone = append(gone, string(kind)) },
		})
		defer watch.Stop()
		watch.Tick(context.Background()) // present: nothing
		if len(gone) != 0 {
			t.Fatalf("gone = %v while present, want none", gone)
		}
		writeFile(t, registryFile(config), "{torn")
		watch.Tick(context.Background()) // unknown: nothing, and a reset
		if len(gone) != 0 {
			t.Fatalf("gone = %v while unknown, want none", gone)
		}
		install(t, config, `{"other@market":`+installedEntry+`}`)
		watch.Tick(context.Background()) // uninstalled, first sighting
		if len(gone) != 0 {
			t.Fatalf("gone = %v after one sighting, want none", gone)
		}
		writeFile(t, registryFile(config), "{torn")
		watch.Tick(context.Background()) // unknown again: reset
		install(t, config, `{"other@market":`+installedEntry+`}`)
		watch.Tick(context.Background()) // first sighting after the reset
		if len(gone) != 0 {
			t.Fatalf("gone = %v after the first sighting past a reset, want none", gone)
		}
		watch.Tick(context.Background()) // agrees: gone
		if strings.Join(gone, ",") != "uninstalled" {
			t.Fatalf("gone = %v, want [uninstalled]", gone)
		}
		watch.Tick(context.Background()) // the watch is done
		if strings.Join(gone, ",") != "uninstalled" {
			t.Fatalf("gone = %v after the watch fired, want it to act once", gone)
		}
	})

	t.Run("watchRemoval: a disable and an uninstall do not add up; two of a kind do", func(t *testing.T) {
		config, env := world(t)
		install(t, config, `{"`+pluginKey+`":`+installedEntry+`}`)
		writeFile(t, settingsFile(config), enabled("false"))
		var gone []string
		watch := WatchRemoval(RemovalOptions{
			Env:    env,
			Plugin: "huddle",
			Gone:   func(kind Gone) { gone = append(gone, string(kind)) },
		})
		defer watch.Stop()
		watch.Tick(context.Background()) // disabled, once
		if len(gone) != 0 {
			t.Fatalf("gone = %v after one disabled sighting, want none", gone)
		}
		install(t, config, `{"other@market":`+installedEntry+`}`) // now uninstalled
		watch.Tick(context.Background())                          // disagrees with the last sighting
		if len(gone) != 0 {
			t.Fatalf("gone = %v after a disagreeing sighting, want none", gone)
		}
		watch.Tick(context.Background()) // twice the same
		if strings.Join(gone, ",") != "uninstalled" {
			t.Fatalf("gone = %v, want [uninstalled]", gone)
		}
		watch.Stop()
	})
}

func TestRunFiles(t *testing.T) {
	t.Run("the run files leave; the channels, the config and the note stay", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(home, "run", "huddle.pid"), "123")
		writeFile(t, filepath.Join(home, "run", "huddle.port"), "18860")
		writeFile(t, filepath.Join(home, "run", "huddle.log"), "log")
		writeFile(t, filepath.Join(home, "hooks.log"), "x")
		writeFile(t, filepath.Join(home, "stop-raised.json"), "{}")
		if err := os.MkdirAll(filepath.Join(home, "seen"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(home, "seen", "s1.json"), "{}")
		writeFile(t, filepath.Join(home, "config.toml"), "port = 18860\n")
		if err := os.MkdirAll(filepath.Join(home, "data", "channels"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(home, "data", "channels", "shop.db"), "sqlite")
		RemoveRunFiles(home)
		for _, name := range []string{"run", "hooks.log", "stop-raised.json", "seen"} {
			if _, err := os.Stat(filepath.Join(home, name)); err == nil {
				t.Errorf("%s is still there, want it gone", name)
			} else if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("stat %s: %v", name, err)
			}
		}
		for _, name := range []string{"config.toml", filepath.Join("data", "channels", "shop.db")} {
			if _, err := os.Stat(filepath.Join(home, name)); err != nil {
				t.Errorf("%s left with the run files: %v", name, err)
			}
		}
		at := time.Date(2026, 10, 15, 3, 13, 50, 500000000, time.UTC)
		if err := WriteLeftBehind(home, filepath.Join(home, "data"), at); err != nil {
			t.Fatalf("WriteLeftBehind: %v", err)
		}
		note, err := os.ReadFile(LeftBehindPath(home))
		if err != nil {
			t.Fatalf("read the note: %v", err)
		}
		for _, want := range []string{
			filepath.Join(home, "data", "channels"),
			"rm -rf",
			"the config",
			"2026-10-15T03:13:50.500Z",
		} {
			if !strings.Contains(string(note), want) {
				t.Errorf("the note lacks %q:\n%s", want, note)
			}
		}
	})
}

func TestPresenceShapes(t *testing.T) {
	t.Run("the registry as the whole file, without the plugins key", func(t *testing.T) {
		config, env := world(t)
		writeFile(t, registryFile(config), `{"huddle@x":`+installedEntry+`}`)
		if got := PluginPresence(context.Background(), env, "huddle", nil); got != PresencePresent {
			t.Errorf("presence = %q, want present", got)
		}
		writeFile(t, registryFile(config), `{"other@y":`+installedEntry+`}`)
		if got := PluginPresence(context.Background(), env, "huddle", nil); got != PresenceUninstalled {
			t.Errorf("presence = %q, want uninstalled", got)
		}
	})

	t.Run("a registry that cannot be read is unknown, not missing", func(t *testing.T) {
		config, env := world(t)
		// a directory where the file should be reads as a failure that is not ENOENT
		if err := os.Mkdir(registryFile(config), 0o700); err != nil {
			t.Fatal(err)
		}
		if got := PluginPresence(context.Background(), env, "huddle", nil); got != PresenceUnknown {
			t.Errorf("presence = %q, want unknown", got)
		}
	})

	t.Run("a project settings file that is not an object says nothing", func(t *testing.T) {
		config, env := world(t)
		install(t, config, `{"`+pluginKey+`":`+installedEntry+`}`)
		writeFile(t, settingsFile(config), enabled("false"))
		project := t.TempDir()
		if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(project, ".claude", "settings.json"), "[1]")
		if got := PluginPresence(context.Background(), env, "huddle", []string{project}); got != PresenceDisabled {
			t.Errorf("presence = %q, want disabled", got)
		}
	})

	t.Run("the config dir is $CLAUDE_CONFIG_DIR, else ~/.claude", func(t *testing.T) {
		for _, c := range []struct {
			env  map[string]string
			want string
		}{
			{map[string]string{"CLAUDE_CONFIG_DIR": "/c"}, "/c"},
			{map[string]string{"HOME": "/h"}, "/h/.claude"},
		} {
			if got := ClaudeConfigDir(c.env); got != c.want {
				t.Errorf("ClaudeConfigDir(%v) = %q, want %q", c.env, got, c.want)
			}
		}
	})

	t.Run("a note that cannot be written says so", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "a-file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteLeftBehind(filepath.Join(blocker, "under"), "data", time.Now()); err == nil {
			t.Error("WriteLeftBehind under a file: want an error")
		}
	})
}
