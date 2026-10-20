package adapters

// Feature: one router in Claude Code's settings. Every provider's setup points the settings at
// huddle-router and adds its models; removing one provider keeps the proxy while another provider
// is still enabled, and removing the last one restores the file byte for byte. The scenarios run
// the real merge and undo against a temp settings.json.

import (
	"os"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// huddleSettingsURL is the one router address the settings point at.
var huddleSettingsURL = domain.HuddleRouterURL(domain.EnvLookup{})

// applyProvider applies one provider against huddle-router and answers the delta it recorded.
// caDir is the one CA dir every provider's setup names (the router's own, in production).
func applyProvider(t *testing.T, path string, provider domain.Provider, caDir string, options ApplyProviderOptions) SettingsDelta {
	t.Helper()
	options.CADir = caDir
	var delta SettingsDelta
	options.Record = func(recorded SettingsDelta) { delta = recorded }
	if _, err := ApplyProviderSettings(t.Context(), path, provider, huddleSettingsURL, options); err != nil {
		t.Fatalf("apply %s: %v", provider.Name, err)
	}
	return delta
}

// undoProvider undoes one provider's recorded delta, naming the providers still enabled.
func undoProvider(t *testing.T, path string, provider domain.Provider, delta SettingsDelta, othersEnabled []string) []string {
	t.Helper()
	lines, err := UndoProviderSettings(t.Context(), path, provider.Name, SettingsUndo(delta), huddleSettingsURL, nil, othersEnabled)
	if err != nil {
		t.Fatalf("undo %s: %v", provider.Name, err)
	}
	return lines
}

// envOf reads the settings file's env object back as strings.
func envOf(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env := proxyRoot(t, string(data)).get("env")
	got := make(map[string]string, env.size())
	for _, key := range env.keys {
		value, _ := env.get(key).text()
		got[key] = value
	}
	return got
}

// modelsOf reads the settings file's /model ids back.
func modelsOf(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	options := proxyRoot(t, string(data)).get("modelPicker").get("options")
	ids := make([]string, 0, options.size())
	for _, entry := range options.items {
		if model, ok := entry.get("model").text(); ok {
			ids = append(ids, model)
		}
	}
	return ids
}

func TestOneRouterInTheSettings(t *testing.T) {
	bdd.Feature(t, "one router in Claude Code's settings", func(f *bdd.F) {
		f.Scenario("setting up zai points HTTPS_PROXY at huddle-router and adds its models to /model", func(s *bdd.S) {
			var path, caDir string
			s.Given("a settings file with one unrelated key", func(t *testing.T) {
				path = settingsFilePath(s.T)
				caDir = proxyCADir(s.T)
				writeSettingsFile(s.T, path, "{\n  \"unrelated\": \"keep\"\n}\n")
			})
			s.When("zai is set up", func(t *testing.T) {
				applyProvider(t, path, providers.Zai, caDir, ApplyProviderOptions{})
			})
			s.Then("HTTPS_PROXY points at huddle-router, with its CA and the loopback NO_PROXY beside it", func(t *testing.T) {
				env := envOf(t, path)
				if env["HTTPS_PROXY"] != huddleSettingsURL {
					t.Errorf("HTTPS_PROXY = %q, want %q", env["HTTPS_PROXY"], huddleSettingsURL)
				}
				if env["NO_PROXY"] != "localhost,127.0.0.1" {
					t.Errorf("NO_PROXY = %q, want the loopback entries", env["NO_PROXY"])
				}
				if env["NODE_EXTRA_CA_CERTS"] == "" {
					t.Error("NODE_EXTRA_CA_CERTS is unset")
				}
				if _, has := env["ANTHROPIC_BASE_URL"]; has {
					t.Error("ANTHROPIC_BASE_URL is set; proxy mode replaces it")
				}
			})
			s.And("zai's models are in /model", func(t *testing.T) {
				got := strings.Join(modelsOf(t, path), ",")
				if got != "glm-5.3,glm-5.3-flash" {
					t.Errorf("/model = %q, want zai's two models", got)
				}
			})
		})

		f.Scenario("setting up kimi after zai keeps the proxy and adds kimi's models", func(s *bdd.S) {
			var path, caDir, proxy string
			s.Given("zai is set up", func(t *testing.T) {
				path = settingsFilePath(s.T)
				caDir = proxyCADir(s.T)
				writeSettingsFile(s.T, path, "{\n  \"unrelated\": \"keep\"\n}\n")
				applyProvider(t, path, providers.Zai, caDir, ApplyProviderOptions{})
				proxy = envOf(t, path)["HTTPS_PROXY"]
			})
			s.When("kimi is set up", func(t *testing.T) {
				applyProvider(t, path, providers.Kimi, caDir, ApplyProviderOptions{})
			})
			s.Then("the proxy keys are exactly what zai wrote", func(t *testing.T) {
				env := envOf(t, path)
				if env["HTTPS_PROXY"] != proxy {
					t.Errorf("HTTPS_PROXY = %q, want the kept %q", env["HTTPS_PROXY"], proxy)
				}
				if env["NO_PROXY"] != "localhost,127.0.0.1" {
					t.Errorf("NO_PROXY = %q, want it untouched", env["NO_PROXY"])
				}
			})
			s.And("kimi's models sit beside zai's in /model", func(t *testing.T) {
				got := strings.Join(modelsOf(t, path), ",")
				if got != "glm-5.3,glm-5.3-flash,kimi-k3,kimi-k2.6" {
					t.Errorf("/model = %q, want both providers' models", got)
				}
			})
		})

		f.Scenario("removing zai while kimi is enabled keeps the proxy and removes only zai's models", func(s *bdd.S) {
			var path, caDir string
			var zaiDelta SettingsDelta
			s.Given("zai and kimi are set up", func(t *testing.T) {
				path = settingsFilePath(s.T)
				caDir = proxyCADir(s.T)
				writeSettingsFile(s.T, path, "{\n  \"unrelated\": \"keep\"\n}\n")
				zaiDelta = applyProvider(t, path, providers.Zai, caDir, ApplyProviderOptions{})
				applyProvider(t, path, providers.Kimi, caDir, ApplyProviderOptions{})
			})
			s.When("zai is removed while kimi is still enabled", func(t *testing.T) {
				undoProvider(t, path, providers.Zai, zaiDelta, []string{"kimi"})
			})
			s.Then("the proxy keys stay: kimi still routes through them", func(t *testing.T) {
				env := envOf(t, path)
				if env["HTTPS_PROXY"] != huddleSettingsURL || env["NODE_EXTRA_CA_CERTS"] == "" || env["NO_PROXY"] != "localhost,127.0.0.1" {
					t.Errorf("env = %v, want the shared proxy keys kept", env)
				}
			})
			s.And("only zai's models are gone", func(t *testing.T) {
				got := strings.Join(modelsOf(t, path), ",")
				if got != "kimi-k3,kimi-k2.6" {
					t.Errorf("/model = %q, want kimi's models only", got)
				}
			})
		})

		f.Scenario("removing the last provider restores settings byte for byte", func(s *bdd.S) {
			var path, caDir string
			var original []byte
			var deltas = map[string]SettingsDelta{}
			s.Given("zai and kimi are set up over a settings file with one unrelated key", func(t *testing.T) {
				path = settingsFilePath(s.T)
				caDir = proxyCADir(s.T)
				writeSettingsFile(s.T, path, "{\n  \"unrelated\": \"keep\"\n}\n")
				original = mustRead(t, path)
				deltas["zai"] = applyProvider(t, path, providers.Zai, caDir, ApplyProviderOptions{})
				deltas["kimi"] = applyProvider(t, path, providers.Kimi, caDir, ApplyProviderOptions{})
			})
			s.When("zai is removed while kimi is enabled, then kimi is removed", func(t *testing.T) {
				undoProvider(t, path, providers.Zai, deltas["zai"], []string{"kimi"})
				undoProvider(t, path, providers.Kimi, deltas["kimi"], nil)
			})
			s.Then("the file's bytes are the ones it started with", func(t *testing.T) {
				after := mustRead(t, path)
				if string(after) != string(original) {
					t.Errorf("settings.json = %q, want the original %q", after, original)
				}
			})
		})
	})
}

// mustRead reads a file that must be there.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
