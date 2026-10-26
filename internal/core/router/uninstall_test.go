// The plugin-presence tests and the settings undo the removal runs: what setup recorded,
// adapters undo, byte for byte, and how Claude Code's own bookkeeping tells a removed plugin
// from an installed one.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// undoRouterURL is the address a test setup points Claude Code at.
var undoRouterURL = fmt.Sprintf("http://127.0.0.1:%d", domain.HuddleRouterPort)

// presenceConfig is a Claude Code config dir with a plugins dir, as the presence tests shape it.
func presenceConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// installPlugins writes installed_plugins.json.
func installPlugins(t *testing.T, dir string, plugins string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "plugins", "installed_plugins.json"), []byte(plugins), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPluginPresence(t *testing.T) {
	t.Parallel()
	t.Run("is unknown while installed_plugins.json is missing or half-written, absent once the plugin is gone", func(t *testing.T) {
		dir := presenceConfig(t)
		if got := PluginPresence("zai", dir, nil, nil); got != PresenceUnknown {
			t.Fatalf("no file: %s, want unknown", got)
		}
		installPlugins(t, dir, `{"version": 2, "plug`)
		if got := PluginPresence("zai", dir, nil, nil); got != PresenceUnknown {
			t.Fatalf("half-written: %s, want unknown", got)
		}
		installPlugins(t, dir, `{"version":2,"plugins":{"kimi-plugin-cc@m":[{"scope":"user"}]}}`)
		if got := PluginPresence("zai", dir, nil, nil); got != PresenceAbsent {
			t.Fatalf("plugin gone: %s, want absent", got)
		}
	})

	t.Run("is present when installed and not turned off, in the v2 and the flat shape", func(t *testing.T) {
		dir := presenceConfig(t)
		installPlugins(t, dir, `{"version":2,"plugins":{"zai-plugin-cc@muhmdraouf":[{"scope":"user"}]}}`)
		if got := PluginPresence("zai", dir, nil, nil); got != PresencePresent {
			t.Fatalf("v2 shape: %s, want present", got)
		}
		installPlugins(t, dir, `{"zai-plugin-cc@muhmdraouf":"not a list"}`)
		if got := PluginPresence("zai", dir, nil, nil); got != PresencePresent {
			t.Fatalf("flat shape: %s, want present", got)
		}
	})

	t.Run("is absent when the data dir's marker is gone (uninstall deletes it)", func(t *testing.T) {
		dir := presenceConfig(t)
		installPlugins(t, dir, `{"version":2,"plugins":{"zai-plugin-cc@m":[{"scope":"user"}]}}`)
		marker := filepath.Join(dir, "installed")
		if got := PluginPresence("zai", dir, &marker, nil); got != PresenceAbsent {
			t.Fatalf("marker gone: %s, want absent", got)
		}
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := PluginPresence("zai", dir, &marker, nil); got != PresencePresent {
			t.Fatalf("marker back: %s, want present", got)
		}
	})

	t.Run("is disabled only when every scope it is installed in says false", func(t *testing.T) {
		dir := presenceConfig(t)
		project := t.TempDir()
		if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o755); err != nil {
			t.Fatal(err)
		}
		installPlugins(t, dir, fmt.Sprintf(
			`{"version":2,"plugins":{"zai-plugin-cc@m":[{"scope":"user"},{"scope":"project","projectPath":%q},{"scope":"local","projectPath":%q},null]}}`,
			project, project))
		off := `{"enabledPlugins":{"zai-plugin-cc@m":false}}`
		for _, path := range []string{
			filepath.Join(dir, "settings.json"),
			filepath.Join(project, ".claude", "settings.json"),
			filepath.Join(project, ".claude", "settings.local.json"),
		} {
			if err := os.WriteFile(path, []byte(off), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// The null entry counts as a user install, whose settings say false.
		if got := PluginPresence("zai", dir, nil, nil); got != PresenceDisabled {
			t.Fatalf("every scope off: %s, want disabled", got)
		}
		if err := os.WriteFile(filepath.Join(project, ".claude", "settings.local.json"),
			[]byte(`{"enabledPlugins":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := PluginPresence("zai", dir, nil, nil); got != PresencePresent {
			t.Fatalf("one scope back on: %s, want present", got)
		}
	})
}

func undoSettings(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// undoSetUp runs setup on the settings file and returns what it recorded.
func undoSetUp(t *testing.T, path string) adapters.SettingsDelta {
	t.Helper()
	var delta adapters.SettingsDelta
	_, err := adapters.ApplyProviderSettings(context.Background(), path, routerProvider, undoRouterURL,
		adapters.ApplyProviderOptions{
			Record: func(recorded adapters.SettingsDelta) { delta = recorded },
		})
	if err != nil {
		t.Fatal(err)
	}
	return delta
}

func TestUninstallSettingsUndo(t *testing.T) {
	t.Parallel()
	t.Run("setup then uninstall leaves settings.json byte-identical, even in the user's own formatting", func(t *testing.T) {
		original := "{\n    \"theme\": \"dark\",\n    \"permissions\": { \"allow\": [] }\n}\n"
		path := undoSettings(t, original)
		delta := undoSetUp(t, path)
		backup, err := os.ReadFile(adapters.SettingsBackupPath(path, "zai"))
		if err != nil || string(backup) != original {
			t.Fatalf("the backup = %q %v, want the original", backup, err)
		}
		if _, err := adapters.UndoProviderSettings(context.Background(), path, "zai",
			MergeSettingsUndo(nil, &delta), undoRouterURL, nil, adapters.ProxyOurs{}); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != original {
			t.Fatalf("settings after = %q %v, want the original bytes", after, err)
		}
	})

	t.Run("proxy mode: setup writes the three keys and uninstall leaves settings.json byte-identical", func(t *testing.T) {
		original := "{\n    \"theme\": \"dark\",\n    \"permissions\": { \"allow\": [] }\n}\n"
		path := undoSettings(t, original)
		caDir := domain.NewStateLayout(filepath.Join(t.TempDir(), "zai")).CA
		var delta adapters.SettingsDelta
		_, err := adapters.ApplyProviderSettings(context.Background(), path, routerProvider, undoRouterURL,
			adapters.ApplyProviderOptions{
				CADir:  caDir,
				Record: func(recorded adapters.SettingsDelta) { delta = recorded },
			})
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Env map[string]string `json:"env"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			t.Fatal(err)
		}
		if env.Env["HTTPS_PROXY"] != undoRouterURL || env.Env["NO_PROXY"] != "localhost,127.0.0.1" {
			t.Fatalf("env = %v, want the proxy keys", env.Env)
		}
		if _, err := os.ReadFile(env.Env["NODE_EXTRA_CA_CERTS"]); err != nil {
			t.Fatalf("read the CA file the merge wrote: %v", err)
		}
		if delta.ProxyBefore != nil || delta.CABefore != nil || delta.NoProxyBefore != nil || !delta.CreatedProxy {
			t.Fatalf("delta = %+v, want the proxy keys recorded as created", delta)
		}

		if _, err := adapters.UndoProviderSettings(context.Background(), path, "zai",
			MergeSettingsUndo(nil, &delta), undoRouterURL, nil, adapters.ProxyOurs{}); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != original {
			t.Fatalf("settings after = %q %v, want the original bytes", after, err)
		}
	})

	t.Run("reverts only this plugin's keys when the user edited another key in between", func(t *testing.T) {
		path := undoSettings(t, "{\n  \"theme\": \"dark\",\n  \"availableModels\": [\n    \"sonnet\"\n  ]\n}\n")
		delta := undoSetUp(t, path)
		edited := map[string]any{}
		if err := json.Unmarshal(mustRead(t, path), &edited); err != nil {
			t.Fatal(err)
		}
		edited["theme"] = "light"
		if err := os.WriteFile(path, mustMarshal(t, edited), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := adapters.UndoProviderSettings(context.Background(), path, "zai",
			MergeSettingsUndo(nil, &delta), undoRouterURL, nil, adapters.ProxyOurs{}); err != nil {
			t.Fatal(err)
		}
		var after map[string]any
		if err := json.Unmarshal(mustRead(t, path), &after); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"theme": "light", "availableModels": []any{"sonnet"}}
		if !reflect.DeepEqual(after, want) {
			t.Fatalf("settings after = %+v, want %+v", after, want)
		}
	})

	t.Run("leaves a base URL the user changed meanwhile alone", func(t *testing.T) {
		path := undoSettings(t, "{}\n")
		delta := undoSetUp(t, path)
		edited := map[string]any{}
		if err := json.Unmarshal(mustRead(t, path), &edited); err != nil {
			t.Fatal(err)
		}
		env := map[string]any{"ANTHROPIC_BASE_URL": "https://my-proxy.example"}
		edited["env"] = env
		if err := os.WriteFile(path, mustMarshal(t, edited), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := adapters.UndoProviderSettings(context.Background(), path, "zai",
			MergeSettingsUndo(nil, &delta), undoRouterURL, nil, adapters.ProxyOurs{}); err != nil {
			t.Fatal(err)
		}
		var after map[string]any
		if err := json.Unmarshal(mustRead(t, path), &after); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://my-proxy.example"}}
		if !reflect.DeepEqual(after, want) {
			t.Fatalf("settings after = %+v, want %+v", after, want)
		}
	})

	t.Run("puts the base URL it replaced back, and takes one of the router's out", func(t *testing.T) {
		// Setup moved the canonical address's other spelling onto the router and recorded it; the
		// before value names the router, so uninstall removes the key (the env this undo empties
		// goes with it — the last provider is leaving).
		path := undoSettings(t, string(mustMarshal(t, map[string]any{"env": map[string]any{
			"ANTHROPIC_BASE_URL": fmt.Sprintf("http://localhost:%d", domain.HuddleRouterPort),
		}}))+"\n")
		delta := undoSetUp(t, path)
		if delta.BaseURLBefore == nil {
			t.Fatalf("baseUrlBefore = %+v, want the moved spelling", delta.BaseURLBefore)
		}
		if _, err := adapters.UndoProviderSettings(context.Background(), path, "zai",
			MergeSettingsUndo(nil, &delta), undoRouterURL, nil, adapters.ProxyOurs{}); err != nil {
			t.Fatal(err)
		}
		var after map[string]any
		if err := json.Unmarshal(mustRead(t, path), &after); err != nil {
			t.Fatal(err)
		}
		if want := map[string]any{}; !reflect.DeepEqual(after, want) {
			t.Fatalf("settings after = %+v, want %+v", after, want)
		}

		// A before value that names no router of ours goes back.
		restored := undoSettings(t, string(mustMarshal(t, map[string]any{"env": map[string]any{
			"ANTHROPIC_BASE_URL": undoRouterURL,
		}}))+"\n")
		before := adapters.SettingsDelta{BaseURLBefore: strPtrLocal("https://my-proxy.example")}
		if _, err := adapters.UndoProviderSettings(context.Background(), restored, "zai",
			MergeSettingsUndo(nil, &before), undoRouterURL, nil, adapters.ProxyOurs{}); err != nil {
			t.Fatal(err)
		}
		var afterRestore map[string]any
		if err := json.Unmarshal(mustRead(t, restored), &afterRestore); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"env": map[string]any{"ANTHROPIC_BASE_URL": "https://my-proxy.example"}}
		if !reflect.DeepEqual(afterRestore, want) {
			t.Fatalf("settings after = %+v, want %+v", afterRestore, want)
		}
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// ── the worker entry ────────────────────────────────────────────────────────────────────────────────────────────────

// workerMessages collects the IPC lines the worker sends, and waits for one to arrive.
