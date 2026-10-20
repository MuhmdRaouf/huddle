package cli

// huddle's session hook does the providers' work. The provider plugins ship no hooks: huddle's
// merged SessionStart part re-applies every set-up provider's wiring after a plugin update,
// cleans up after a removed one and prints the jobs-awaiting-review line, and its SubagentStop
// part says what a finished agent ran on — one process for all five providers, each under its own
// recover. The scenarios run the real wiring over a temp world with one seam: the host calls (the
// daemon start, the router key, the retire) answer from the world, so no daemon spawns and no
// port is bound.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/app"
	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakes"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	freeports "github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// hookWorld is the temp machine the scenarios run on: one home with Claude Code's config dir, a
// state root per provider, one router home and the plugin tree a fake install carries.
type hookWorld struct {
	root       string
	configDir  string
	routerHome string
	routerPort int
	pluginRoot string
	env        domain.EnvLookup
	// disabledPlugin are the installed plugins Claude Code has turned off: their enabledPlugins
	// entries survive every rewrite of settings.json the scenarios make.
	disabledPlugin []string

	ensureCalls int
}

func newHookWorld(t *testing.T) *hookWorld {
	t.Helper()
	root := testkit.TempDir(t, "providers-hook-")
	w := &hookWorld{
		root:       root,
		configDir:  filepath.Join(root, "home", "claude-config"),
		routerHome: filepath.Join(root, "router-home"),
		routerPort: freeports.Free(t),
		pluginRoot: filepath.Join(root, "installed-plugins"),
	}
	if err := os.MkdirAll(w.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env := domain.EnvLookup{
		"HOME":               filepath.Join(root, "home"),
		"CLAUDE_CONFIG_DIR":  w.configDir,
		"HUDDLE_ROUTER_HOME": w.routerHome,
		"HUDDLE_ROUTER_PORT": strconv.Itoa(w.routerPort),
		"RADAR_HOME":         filepath.Join(root, "radar"),
		"ZAI_API_KEY":        "hook-scenario-key",
		"KIMI_API_KEY":       "hook-scenario-key",
	}
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if !ok {
			t.Fatalf("providers has no %s", name)
		}
		env[provider.EnvPrefix+"_STATE_DIR"] = filepath.Join(root, name+"-state")
	}
	w.env = env
	return w
}

// writePluginTree lays out one installed provider plugin: the dist bundle the wiring names and
// the agent files whose model: lines setup rewrites.
func (w *hookWorld) writePluginTree(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(w.pluginRoot, name+"-plugin-cc")
	agents := filepath.Join(dir, "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(dir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	provider, ok := providers.ByName(name)
	if !ok {
		t.Fatalf("providers has no %s", name)
	}
	for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
		agent := provider.Agents[tier]
		body := "---\nname: " + agent + "\nmodel: sonnet\n---\n\nYou are a " + agent + " worker.\n"
		if err := os.WriteFile(filepath.Join(agents, agent+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dist, name+".js"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// installPlugins writes installed_plugins.json: every named plugin installed, the disabled ones
// turned off in settings.json's enabledPlugins the way Claude Code records it.
func (w *hookWorld) installPlugins(t *testing.T, enabled, disabled []string) {
	t.Helper()
	w.disabledPlugin = disabled
	plugins := map[string]any{}
	installed := func(name string) []map[string]any {
		return []map[string]any{{
			"installPath": filepath.Join(w.pluginRoot, name+"-plugin-cc"),
			"scope":       "user",
		}}
	}
	for _, name := range enabled {
		plugins[name+"-plugin-cc@muhmdraouf"] = installed(name)
	}
	for _, name := range disabled {
		plugins[name+"-plugin-cc@muhmdraouf"] = installed(name)
	}
	raw, err := json.Marshal(map[string]any{"plugins": plugins})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.configDir, "plugins")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "installed_plugins.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	w.writeSettings(t, nil)
}

// writeSettings lays settings.json down: `extra` under env, the enabledPlugins entries of the
// disabled plugins kept whatever the scenario seeds.
func (w *hookWorld) writeSettings(t *testing.T, extra map[string]any) {
	t.Helper()
	document := map[string]any{}
	if len(extra) > 0 {
		document["env"] = extra
	}
	if len(w.disabledPlugin) > 0 {
		enabled := map[string]any{}
		for _, name := range w.disabledPlugin {
			enabled[name+"-plugin-cc@muhmdraouf"] = false
		}
		document["enabledPlugins"] = enabled
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.configDir, "settings.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// settingsText is settings.json as it stands.
func (w *hookWorld) settingsText(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.configDir, "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	return string(raw)
}

// settingsModels lists the model ids settings.json's /model picker carries.
func (w *hookWorld) settingsModels(t *testing.T) []string {
	t.Helper()
	var parsed struct {
		ModelPicker struct {
			Options []struct {
				Model string `json:"model"`
			} `json:"options"`
		} `json:"modelPicker"`
	}
	if err := json.Unmarshal([]byte(w.settingsText(t)), &parsed); err != nil {
		t.Fatalf("parse settings.json: %v", err)
	}
	ids := make([]string, 0, len(parsed.ModelPicker.Options))
	for _, option := range parsed.ModelPicker.Options {
		ids = append(ids, option.Model)
	}
	return ids
}

// enabledNames is what providers.json records.
func (w *hookWorld) enabledNames(t *testing.T) []string {
	t.Helper()
	names, err := router.ReadEnabled(w.env)
	if err != nil {
		t.Fatalf("read providers.json: %v", err)
	}
	return names
}

// setUp marks one provider set up the way setup leaves the machine: recorded in providers.json,
// its setup-done mark written.
func (w *hookWorld) setUp(t *testing.T, name string) {
	t.Helper()
	provider, ok := providers.ByName(name)
	if !ok {
		t.Fatalf("providers has no %s", name)
	}
	if err := router.SetEnabled(w.env, name, true); err != nil {
		t.Fatalf("record %s in providers.json: %v", name, err)
	}
	stateRoot := w.env[provider.EnvPrefix+"_STATE_DIR"]
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		t.Fatalf("make the state root: %v", err)
	}
	RecordSetupDone(stateRoot)
}

// wiring is the production wiring with the host calls faked: the daemon start, the router key and
// the retire answer from the world, so nothing spawns and no port is bound. A store the caller
// holds replaces the real one, so a scenario can seed jobs.
func (w *hookWorld) wiring(store ports.JobStore) providerWiring {
	return func(ctx context.Context, provider domain.Provider, env domain.EnvLookup, out ports.Output) app.Deps {
		deps := wireProviderDeps(ctx, provider, env, out)
		deps.Host.EnsureRouter = func(context.Context) (string, error) {
			w.ensureCalls++
			return fmt.Sprintf("huddle-router: http://127.0.0.1:%d (%s)", w.routerPort, provider.Name), nil
		}
		deps.Host.RouterKey = func(context.Context) (string, error) {
			return provider.EnvPrefix + "_API_KEY", nil
		}
		deps.Host.RetireRouter = func(ctx context.Context) (string, error) {
			if err := router.SetEnabled(env, provider.Name, false); err != nil {
				return "", err
			}
			return "huddle-router: stopped routing " + provider.Display, nil
		}
		if store != nil {
			deps.Store = store
		}
		return deps
	}
}

// sessionStartText runs the merged session-start part over the world and joins its outputs.
func (w *hookWorld) sessionStartText(t *testing.T, store ports.JobStore) string {
	t.Helper()
	return joinOutputs(providersSessionStart(context.Background(), w.env, w.wiring(store)))
}

// subagentStopText runs the merged subagent-stop part over the world and joins its outputs.
func (w *hookWorld) subagentStopText(t *testing.T, payload []byte) string {
	t.Helper()
	return joinOutputs(providersSubagentStop(context.Background(), w.env, payload, w.wiring(nil)))
}

// joinOutputs is every output's text, in order.
func joinOutputs(outs [][]byte) string {
	var joined strings.Builder
	for _, out := range outs {
		joined.Write(out)
	}
	return joined.String()
}

// agentModelOf is one provider plugin's main agent model: line.
func (w *hookWorld) agentModelOf(t *testing.T, name string) string {
	t.Helper()
	provider, ok := providers.ByName(name)
	if !ok {
		t.Fatalf("providers has no %s", name)
	}
	file := filepath.Join(w.pluginRoot, name+"-plugin-cc", "agents", provider.Agents[domain.ModelTierMain]+".md")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read the main agent: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if model, ok := strings.CutPrefix(line, "model: "); ok {
			return strings.TrimSpace(model)
		}
	}
	return ""
}

func TestHuddlesSessionHookDoesTheProvidersWork(t *testing.T) {
	ctx := context.Background()

	t.Run("a session start re-applies zai's /model entries after a plugin update removed them",
		func(t *testing.T) {
			w := newHookWorld(t)
			w.writePluginTree(t, "zai")
			w.installPlugins(t, []string{"zai"}, nil)
			w.setUp(t, "zai")
			w.writeSettings(t, map[string]any{"unrelated": "keep"})

			out := w.sessionStartText(t, nil)

			provider, _ := providers.ByName("zai")
			models := strings.Join(w.settingsModels(t), ",")
			for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
				if !strings.Contains(models, provider.Catalog[tier].ID) {
					t.Errorf("the /model entries are %v, want %s back", models, provider.Catalog[tier].ID)
				}
			}
			if got := w.enabledNames(t); len(got) != 1 || got[0] != "zai" {
				t.Errorf("providers.json = %v, want [zai]", got)
			}
			if model := w.agentModelOf(t, "zai"); model != provider.Catalog[domain.ModelTierMain].ID {
				t.Errorf("the main agent runs on %q, want %s", model, provider.Catalog[domain.ModelTierMain].ID)
			}
			if strings.Contains(out, "unrelated") {
				t.Errorf("the hook spoke for the settings it kept: %q", out)
			}
		})

	t.Run("a session start prints one line when zai jobs await review", func(t *testing.T) {
		w := newHookWorld(t)
		w.writePluginTree(t, "zai")
		w.installPlugins(t, []string{"zai"}, nil)
		w.setUp(t, "zai")
		w.writeSettings(t, nil)
		// One job of this repository awaits review; the store is the seam the scenario seeds.
		store := fakes.NewMemoryStore(testkit.TempDir(t, "providers-hook-jobs-"))
		store.Put(testkit.AJob(func(job *domain.Job) {
			job.State = domain.JobStateAwaitingReview
			job.Workspace.RepoRoot = w.repoRoot(t)
		}))

		out := w.sessionStartText(t, store)

		if !strings.Contains(out, "/zai:review") {
			t.Errorf("the hook said %q, want the jobs-awaiting-review line", out)
		}
	})

	t.Run("a removed kimi plugin's settings are cleaned up at the next session start", func(t *testing.T) {
		w := newHookWorld(t)
		kimi, _ := providers.ByName("kimi")
		w.writePluginTree(t, "kimi")
		// The plugin is installed but disabled: Claude Code records both facts.
		w.installPlugins(t, nil, []string{"kimi"})
		w.setUp(t, "kimi")
		options := []map[string]any{}
		for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
			options = append(options, map[string]any{"model": kimi.Catalog[tier].ID, "label": kimi.Catalog[tier].Label})
		}
		w.writeSettings(t, map[string]any{
			"modelPicker":        map[string]any{"options": options},
			"ANTHROPIC_BASE_URL": fmt.Sprintf("http://127.0.0.1:%d", w.routerPort),
		})

		out := w.sessionStartText(t, nil)

		models := strings.Join(w.settingsModels(t), ",")
		for _, tier := range []domain.ModelTier{domain.ModelTierMain, domain.ModelTierFlash} {
			if id := kimi.Catalog[tier].ID; strings.Contains(models, id) {
				t.Errorf("the /model entries are %v, %s is still among them", models, id)
			}
		}
		if got := w.enabledNames(t); len(got) != 0 {
			t.Errorf("providers.json = %v, want kimi off", got)
		}
		if out != "" {
			t.Errorf("the cleanup spoke: %q", out)
		}
	})

	t.Run("a provider that was never set up does nothing", func(t *testing.T) {
		w := newHookWorld(t)
		w.writePluginTree(t, "zai")
		w.installPlugins(t, []string{"zai"}, nil)
		w.writeSettings(t, map[string]any{"unrelated": "keep"})
		before := w.settingsText(t)

		out := w.sessionStartText(t, nil)

		if out != "" {
			t.Errorf("the hook spoke: %q", out)
		}
		if after := w.settingsText(t); after != before {
			t.Errorf("settings.json changed:\nbefore %s\nafter  %s", before, after)
		}
		if w.ensureCalls != 0 {
			t.Errorf("the daemon was started %d times, want never", w.ensureCalls)
		}
	})

	t.Run("a subagent stop says which model a zai agent ran on", func(t *testing.T) {
		w := newHookWorld(t)
		w.writePluginTree(t, "zai")
		w.installPlugins(t, []string{"zai"}, nil)
		w.setUp(t, "zai")
		w.writeSettings(t, nil)
		transcript := filepath.Join(w.root, "agent.jsonl")
		run := `{"type":"assistant","message":{"id":"m1","model":"glm-5.3","usage":{"input_tokens":10,"output_tokens":10},"content":[]}}`
		if err := os.WriteFile(transcript, []byte(run+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		payload := `{"agent_type":"zai:glm-5.3","agent_transcript_path":` + strconv.Quote(transcript) + `}`

		out := w.subagentStopText(t, []byte(payload))

		if !strings.Contains(out, "zai:glm-5.3 ran on glm-5.3 via the zai router") {
			t.Errorf("the usage part said %q", out)
		}
	})

	t.Run("one provider's failure never stops the next", func(t *testing.T) {
		w := newHookWorld(t)
		w.writePluginTree(t, "zai")
		w.writePluginTree(t, "kimi")
		w.installPlugins(t, []string{"zai", "kimi"}, nil)
		w.setUp(t, "zai")
		w.setUp(t, "kimi")
		w.writeSettings(t, nil)
		// kimi's pass blows up; zai's, which runs before it, must still have applied.
		wiring := func(ctx context.Context, provider domain.Provider, env domain.EnvLookup, out ports.Output) app.Deps {
			if provider.Name == "kimi" {
				panic("kimi's hook work blew up")
			}
			return w.wiring(nil)(ctx, provider, env, out)
		}
		outs := providersSessionStart(ctx, w.env, wiring)

		if models := w.settingsModels(t); len(models) == 0 {
			t.Error("zai's /model entries never came back beside the failing provider")
		}
		if joinOutputs(outs) != "" {
			t.Errorf("the surviving provider spoke: %q", joinOutputs(outs))
		}
	})
}

// repoRoot is the repository the scenario's jobs belong to: the one the hook's own board scope
// resolves the session's working directory into.
func (w *hookWorld) repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("the working directory: %v", err)
	}
	provider, _ := providers.ByName("zai")
	deps := wireProviderDeps(context.Background(), provider, w.env, setupSilent{})
	root, err := deps.Git.Root(context.Background(), cwd)
	if err != nil {
		t.Fatalf("resolve the scenario's repository: %v", err)
	}
	return root
}
