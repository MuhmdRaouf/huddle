// The plugin registry watch and the state-tree removal cases. The flight tracker and
// whole-app cases live with the server packages they test.
package lifecycle_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/lifecycle"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// testEnv is a whole temp world: HOME, RADAR_HOME, CLAUDE_CONFIG_DIR and ZAI_STATE_DIR all
// point into one throwaway directory, so no test can read or write anything real — not the
// user's transcripts, not a live server.json. Built from scratch (never copied from the
// process environment) so a stray variable set in the surrounding shell cannot leak a real
// path in.
type testEnv struct {
	env    domain.EnvLookup
	home   string
	state  string
	config string
	zai    string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{
		env: domain.EnvLookup{
			"HOME":              home,
			"RADAR_HOME":        filepath.Join(home, "state"),
			"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
			"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
		},
		home:   home,
		state:  filepath.Join(home, "state"),
		config: filepath.Join(home, "claude"),
		zai:    filepath.Join(home, "zai"),
	}
}

// writeText mkdir -p's the parent, then writes with 0600 — the mode every spool/state file
// has.
func writeText(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

const registryKey = "radar@muhmdraouf"

// registryEntry is one install the way Claude Code records it.
var registryEntry = []any{map[string]any{"scope": "user", "installPath": "/x", "version": "1"}}

// install writes the registry exactly as Claude Code writes it: the map under "plugins",
// entries as lists.
func install(t *testing.T, config string, plugins map[string]any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"version": 2, "plugins": plugins})
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	writeText(t, filepath.Join(config, "plugins", "installed_plugins.json"), string(body))
}

func installed(t *testing.T, config string) {
	t.Helper()
	install(t, config, map[string]any{registryKey: registryEntry})
}

func uninstalledRegistry(t *testing.T, config string) {
	t.Helper()
	install(t, config, map[string]any{"other@market": registryEntry}) // others remain, ours is gone
}

func userSettings(t *testing.T, config string, enabledPlugins map[string]bool) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"enabledPlugins": enabledPlugins})
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	writeText(t, filepath.Join(config, "settings.json"), string(body))
}

// project makes a project dir whose .claude/settings.json / settings.local.json say what it
// says.
func project(t *testing.T, files map[string]map[string]bool) string {
	t.Helper()
	dir := t.TempDir()
	for name, enabledPlugins := range files {
		body, err := json.Marshal(map[string]any{"enabledPlugins": enabledPlugins})
		if err != nil {
			t.Fatalf("marshal project settings: %v", err)
		}
		writeText(t, filepath.Join(dir, ".claude", name), string(body))
	}
	return dir
}

// presence reads the registry once, with the given known projects.
func presence(t *testing.T, env domain.EnvLookup, projectDirs ...string) lifecycle.Presence {
	t.Helper()
	dirs := projectDirs
	return lifecycle.PluginPresence(t.Context(), lifecycle.PresenceOptions{
		Env:         env,
		Plugin:      "radar",
		ProjectDirs: func() []string { return dirs },
	})
}

func TestPluginPresence(t *testing.T) {
	t.Run("reads unknown while the registry is missing, half-written, or not the shape Claude Code writes", func(t *testing.T) {
		e := makeEnv(t)
		if got := presence(t, e.env); got != lifecycle.PresenceUnknown {
			t.Fatalf("presence = %q, want unknown (not there yet)", got)
		}
		registry := filepath.Join(e.config, "plugins", "installed_plugins.json")
		if err := os.MkdirAll(registry, 0o700); err != nil { // a file nothing can read
			t.Fatalf("mkdir %s: %v", registry, err)
		}
		if got := presence(t, e.env); got != lifecycle.PresenceUnknown {
			t.Fatalf("presence = %q, want unknown (unreadable)", got)
		}
		if err := os.Remove(registry); err != nil {
			t.Fatalf("remove %s: %v", registry, err)
		}
		writeText(t, registry, "{half-written")
		if got := presence(t, e.env); got != lifecycle.PresenceUnknown {
			t.Fatalf("presence = %q, want unknown (torn write)", got)
		}
		install(t, e.config, map[string]any{registryKey: "not a list"})
		if got := presence(t, e.env); got != lifecycle.PresenceUnknown {
			t.Fatalf("presence = %q, want unknown (foreign shape)", got)
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if got := lifecycle.PluginPresence(canceled, lifecycle.PresenceOptions{Env: e.env, Plugin: "radar"}); got != lifecycle.PresenceUnknown {
			t.Fatalf("presence with a canceled context = %q, want unknown", got)
		}
	})

	t.Run("reads uninstalled when no key names the plugin, under any marketplace", func(t *testing.T) {
		e := makeEnv(t)
		installed(t, e.config)
		uninstalledRegistry(t, e.config)
		if got := presence(t, e.env); got != lifecycle.PresenceUninstalled {
			t.Fatalf("presence = %q, want uninstalled", got)
		}
		install(t, e.config, map[string]any{}) // nothing installed at all
		if got := presence(t, e.env); got != lifecycle.PresenceUninstalled {
			t.Fatalf("presence = %q, want uninstalled", got)
		}
		install(t, e.config, map[string]any{"radar@fork": registryEntry})
		if got := presence(t, e.env); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present (a fork counts)", got)
		}
		install(t, e.config, map[string]any{registryKey: registryEntry, "bare-key": registryEntry})
		if got := presence(t, e.env); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present (a key without @ is no plugin@marketplace key)", got)
		}
	})

	t.Run("lets the user scope switch the plugin off, and a known project switch it back on", func(t *testing.T) {
		e := makeEnv(t)
		installed(t, e.config)
		if got := presence(t, e.env); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present (no settings at all)", got)
		}
		writeText(t, filepath.Join(e.config, "settings.json"), "{oops")
		if got := presence(t, e.env); got != lifecycle.PresenceUnknown {
			t.Fatalf("presence = %q, want unknown (unreadable)", got)
		}
		userSettings(t, e.config, map[string]bool{registryKey: true})
		if got := presence(t, e.env); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present", got)
		}
		writeText(t, filepath.Join(e.config, "settings.json"), `{"other":1}`) // no enabledPlugins map
		if got := presence(t, e.env); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present (no enabledPlugins map)", got)
		}
		userSettings(t, e.config, map[string]bool{"other@market": false}) // ours is not named there
		if got := presence(t, e.env); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present (our key is not named)", got)
		}
		writeText(t, filepath.Join(e.config, "settings.json"), `{"enabledPlugins":{"radar@muhmdraouf":"yes"}}`) // not a boolean
		if got := presence(t, e.env); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present (only strictly false switches off)", got)
		}
		userSettings(t, e.config, map[string]bool{registryKey: false})
		if got := presence(t, e.env); got != lifecycle.PresenceDisabled {
			t.Fatalf("presence = %q, want disabled", got)
		}
		if got := lifecycle.PluginPresence(t.Context(), lifecycle.PresenceOptions{Env: e.env, Plugin: "radar"}); got != lifecycle.PresenceDisabled {
			t.Fatalf("presence with no project dirs = %q, want disabled", got)
		}
		quiet := project(t, map[string]map[string]bool{"settings.json": {registryKey: false}})
		if got := presence(t, e.env, quiet); got != lifecycle.PresenceDisabled {
			t.Fatalf("presence = %q, want disabled", got)
		}
		plain := t.TempDir()
		writeText(t, filepath.Join(plain, ".claude", "settings.json"), `{"model":"big"}`) // carries no enabledPlugins map
		if got := presence(t, e.env, plain); got != lifecycle.PresenceDisabled {
			t.Fatalf("presence = %q, want disabled (the project says nothing about plugins)", got)
		}
		torn := t.TempDir()
		writeText(t, filepath.Join(torn, ".claude", "settings.json"), "{torn")
		writeText(t, filepath.Join(torn, ".claude", "settings.local.json"), `{"enabledPlugins":{"radar@muhmdraouf":true}}`)
		if got := presence(t, e.env, torn); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present (the torn file is skipped, the local one re-enables)", got)
		}
		enabling := project(t, map[string]map[string]bool{"settings.json": {registryKey: true}})
		if got := presence(t, e.env, quiet, enabling); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present", got)
		}
		local := project(t, map[string]map[string]bool{"settings.local.json": {registryKey: true}})
		if got := presence(t, e.env, local); got != lifecycle.PresencePresent {
			t.Fatalf("presence = %q, want present", got)
		}
	})
}

// watchUnderWatch builds a watch whose interval never fires, so only the manual ticks act.
func watchUnderTest(t *testing.T, env domain.EnvLookup, removed *[]string) *lifecycle.RemovalWatch {
	t.Helper()
	anHour := 3_600_000
	watch := lifecycle.WatchRemoval(t.Context(), lifecycle.WatchOptions{
		Env:         env,
		Plugin:      "radar",
		ProjectDirs: func() []string { return nil },
		IntervalMs:  &anHour,
		OnRemove: func(ctx context.Context, kind lifecycle.RemovalKind) {
			*removed = append(*removed, string(kind))
		},
	})
	t.Cleanup(watch.Stop)
	return watch
}

func TestWatchRemoval(t *testing.T) {
	t.Run("acts once, after two consecutive checks agree; present or unknown in between resets", func(t *testing.T) {
		e := makeEnv(t)
		installed(t, e.config)
		removed := []string{}
		watch := watchUnderTest(t, e.env, &removed)

		watch.Tick(t.Context()) // present
		if len(removed) != 0 {
			t.Fatalf("removed = %v, want none", removed)
		}
		writeText(t, filepath.Join(e.config, "plugins", "installed_plugins.json"), "{torn") // unknown, resets
		watch.Tick(t.Context())
		uninstalledRegistry(t, e.config) // first sighting
		watch.Tick(t.Context())
		if len(removed) != 0 {
			t.Fatalf("removed = %v, want none", removed)
		}
		writeText(t, filepath.Join(e.config, "plugins", "installed_plugins.json"), "{torn") // unknown again, resets
		watch.Tick(t.Context())
		uninstalledRegistry(t, e.config)
		watch.Tick(t.Context()) // first sighting after the reset
		watch.Tick(t.Context()) // agrees — acts
		if !slices.Equal(removed, []string{"uninstalled"}) {
			t.Fatalf("removed = %v, want [uninstalled]", removed)
		}
		watch.Tick(t.Context()) // the watch is done
		if !slices.Equal(removed, []string{"uninstalled"}) {
			t.Fatalf("removed = %v, want [uninstalled] once", removed)
		}
	})

	t.Run("does not add a disable and an uninstall up: two of a kind act, mixed readings do not", func(t *testing.T) {
		e := makeEnv(t)
		installed(t, e.config)
		userSettings(t, e.config, map[string]bool{registryKey: false})
		removed := []string{}
		watch := watchUnderTest(t, e.env, &removed)

		watch.Tick(t.Context()) // disabled, once
		uninstalledRegistry(t, e.config)
		watch.Tick(t.Context()) // disagrees with the last sighting: start over
		if len(removed) != 0 {
			t.Fatalf("removed = %v, want none", removed)
		}
		watch.Tick(t.Context()) // twice the same
		if !slices.Equal(removed, []string{"uninstalled"}) {
			t.Fatalf("removed = %v, want [uninstalled]", removed)
		}
		watch.Stop()
	})

	t.Run("checks on the interval, not only on demand", func(t *testing.T) {
		e := makeEnv(t)
		uninstalledRegistry(t, e.config)
		fired := make(chan lifecycle.RemovalKind, 1)
		aMillisecond := 1
		watch := lifecycle.WatchRemoval(t.Context(), lifecycle.WatchOptions{
			Env:         e.env,
			Plugin:      "radar",
			ProjectDirs: func() []string { return nil },
			IntervalMs:  &aMillisecond,
			OnRemove: func(ctx context.Context, kind lifecycle.RemovalKind) {
				fired <- kind
			},
		})
		t.Cleanup(watch.Stop)
		select {
		case kind := <-fired:
			if kind != lifecycle.RemovalUninstalled {
				t.Fatalf("removal kind = %q, want uninstalled", kind)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the interval never fired the removal")
		}
	})
}

func TestRemoveStateTree(t *testing.T) {
	t.Run("takes the whole state dir with it", func(t *testing.T) {
		e := makeEnv(t)
		if !paths.EnsureStateDirs(t.Context(), e.env) {
			t.Fatal("EnsureStateDirs = false, want true")
		}
		writeText(t, filepath.Join(e.state, "spool", "today.jsonl"), "{}\n")
		if !lifecycle.RemoveStateTree(t.Context(), e.env) {
			t.Fatal("RemoveStateTree = false, want true")
		}
		if _, err := os.Stat(e.state); !os.IsNotExist(err) {
			t.Fatalf("stat %s after the removal = %v, want not there", e.state, err)
		}
	})

	t.Run("never deletes a directory without radar's marker, whatever RADAR_HOME names", func(t *testing.T) {
		e := makeEnv(t)
		foreign := filepath.Join(e.home, "projects")
		writeText(t, filepath.Join(foreign, "spool", "today.jsonl"), "{}\n")
		writeText(t, filepath.Join(foreign, "thesis.md"), "years of work\n")
		pointed := domain.EnvLookup{}
		for key, value := range e.env {
			pointed[key] = value
		}
		pointed["RADAR_HOME"] = foreign
		if !paths.EnsureStateDirs(t.Context(), pointed) {
			t.Fatal("EnsureStateDirs = false, want true")
		}
		if lifecycle.RemoveStateTree(t.Context(), pointed) {
			t.Fatal("RemoveStateTree = true, want false")
		}
		if _, err := os.Stat(filepath.Join(foreign, "thesis.md")); err != nil {
			t.Fatalf("thesis.md after the refusal = %v, want still there", err)
		}
	})

	t.Run("reports a failure without throwing", func(t *testing.T) {
		home := t.TempDir()
		env := domain.EnvLookup{"HOME": home, "RADAR_HOME": filepath.Join(home, "guard", "state")}
		if !paths.EnsureStateDirs(t.Context(), env) {
			t.Fatal("EnsureStateDirs = false, want true")
		}
		writeText(t, filepath.Join(home, "guard", "state", "spool", "today.jsonl"), "{}\n")
		guard := filepath.Join(home, "guard")
		if err := os.Chmod(guard, 0o500); err != nil { // nothing may be unlinked below it
			t.Fatalf("chmod %s: %v", guard, err)
		}
		t.Cleanup(func() { _ = os.Chmod(guard, 0o700) })
		if lifecycle.RemoveStateTree(t.Context(), env) {
			t.Fatal("RemoveStateTree = true, want false")
		}
	})
}

func TestRemovalIntervalMs(t *testing.T) {
	t.Run("takes the override, then the env, then the default", func(t *testing.T) {
		e := makeEnv(t)
		override := 25
		if got := lifecycle.RemovalIntervalMs(e.env, &override); got != 25 {
			t.Fatalf("RemovalIntervalMs(override 25) = %d, want 25", got)
		}
		if got := lifecycle.RemovalIntervalMs(e.env, nil); got != lifecycle.DefaultRemovalIntervalMs {
			t.Fatalf("RemovalIntervalMs(no override) = %d, want %d", got, lifecycle.DefaultRemovalIntervalMs)
		}
		env := func(value string) domain.EnvLookup {
			copied := domain.EnvLookup{}
			for key, v := range e.env {
				copied[key] = v
			}
			copied["RADAR_REMOVAL_MS"] = value
			return copied
		}
		if got := lifecycle.RemovalIntervalMs(env("250"), nil); got != 250 {
			t.Fatalf(`RemovalIntervalMs(RADAR_REMOVAL_MS="250") = %d, want 250`, got)
		}
		if got := lifecycle.RemovalIntervalMs(env(""), nil); got != lifecycle.DefaultRemovalIntervalMs {
			t.Fatalf(`RemovalIntervalMs(RADAR_REMOVAL_MS="") = %d, want %d`, got, lifecycle.DefaultRemovalIntervalMs)
		}
		if got := lifecycle.RemovalIntervalMs(env("soon"), nil); got != lifecycle.DefaultRemovalIntervalMs {
			t.Fatalf(`RemovalIntervalMs(RADAR_REMOVAL_MS="soon") = %d, want %d`, got, lifecycle.DefaultRemovalIntervalMs)
		}
		if got := lifecycle.RemovalIntervalMs(env("0"), nil); got != lifecycle.DefaultRemovalIntervalMs {
			t.Fatalf(`RemovalIntervalMs(RADAR_REMOVAL_MS="0") = %d, want %d`, got, lifecycle.DefaultRemovalIntervalMs)
		}
		huge := strings.Repeat("9", 20)
		if got := lifecycle.RemovalIntervalMs(env(huge), nil); got <= lifecycle.DefaultRemovalIntervalMs {
			t.Fatalf("RemovalIntervalMs(RADAR_REMOVAL_MS=9…20 digits) = %d, want a far-future interval, not the default", got)
		}
		if got := lifecycle.RemovalIntervalMs(env("-"+huge), nil); got != lifecycle.DefaultRemovalIntervalMs {
			t.Fatalf("RemovalIntervalMs(RADAR_REMOVAL_MS=-9…20 digits) = %d, want %d", got, lifecycle.DefaultRemovalIntervalMs)
		}
		beyondNumber := strings.Repeat("9", 400) // Infinity in JavaScript: not a finite interval
		if got := lifecycle.RemovalIntervalMs(env(beyondNumber), nil); got != lifecycle.DefaultRemovalIntervalMs {
			t.Fatalf("RemovalIntervalMs(RADAR_REMOVAL_MS=9…400 digits) = %d, want %d", got, lifecycle.DefaultRemovalIntervalMs)
		}
	})
}
