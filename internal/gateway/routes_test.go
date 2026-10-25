package gateway

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The config: $HUDDLE_HOME/gateway.json names routes (a model glob to an upstream) and the two
// family defaults. A missing file is an empty config; anything unreadable or ill-shaped is a
// startup error naming the file. Matching picks the longest glob that fits, else the family
// default.

func loadFrom(t *testing.T, env domain.EnvLookup) (Config, error) {
	t.Helper()
	return LoadConfig(env)
}

func envWithConfig(t *testing.T, text string) domain.EnvLookup {
	t.Helper()
	home := t.TempDir()
	env := domain.EnvLookup{"HUDDLE_HOME": home}
	if text != "" {
		if err := os.WriteFile(filepath.Join(home, "gateway.json"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

const sampleConfig = `{
  "routes": [
    {"match": "glm-*", "upstream": "https://api.z.ai/api/anthropic"},
    {"match": "kimi-*", "upstream": "https://api.moonshot.ai/anthropic"}
  ],
  "anthropic": "https://api.anthropic.com",
  "openai": "https://api.openai.com"
}`

func TestLoadConfig(t *testing.T) {
	t.Run("reads the file the home names", func(t *testing.T) {
		env := envWithConfig(t, sampleConfig)
		config, err := loadFrom(t, env)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		want := Config{
			Routes: []Route{
				{Match: "glm-*", Upstream: "https://api.z.ai/api/anthropic"},
				{Match: "kimi-*", Upstream: "https://api.moonshot.ai/anthropic"},
			},
			Anthropic: "https://api.anthropic.com",
			OpenAI:    "https://api.openai.com",
			builtin:   builtinAnthropicRoutes(),
		}
		if !reflect.DeepEqual(config, want) {
			t.Errorf("config = %+v, want %+v", config, want)
		}
	})

	t.Run("a missing file is the built-in defaults, not an error", func(t *testing.T) {
		config, err := loadFrom(t, envWithConfig(t, ""))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		want := Config{Anthropic: "https://api.anthropic.com", OpenAI: "https://api.openai.com", builtin: builtinAnthropicRoutes()}
		if !reflect.DeepEqual(config, want) {
			t.Errorf("config = %+v, want %+v", config, want)
		}
	})

	t.Run("an invalid file is an error naming the file", func(t *testing.T) {
		env := envWithConfig(t, "{not json")
		_, err := loadFrom(t, env)
		if err == nil {
			t.Fatal("LoadConfig = nil error, want one")
		}
		var bad *ConfigError
		if !errors.As(err, &bad) {
			t.Fatalf("error %T (%v), want a *ConfigError", err, err)
		}
		if !strings.Contains(err.Error(), filepath.Join(bad.Path)) || !strings.HasSuffix(bad.Path, "gateway.json") {
			t.Errorf("error %q does not name the config file", err)
		}
	})

	t.Run("an ill-shaped file is an error naming the file", func(t *testing.T) {
		for name, text := range map[string]string{
			"a route without an upstream": `{"routes":[{"match":"glm-*"}]}`,
			"a route without a match":     `{"routes":[{"upstream":"https://api.z.ai"}]}`,
			"a non-http upstream":         `{"routes":[{"match":"glm-*","upstream":"ftp://api.z.ai"}]}`,
			"a non-http default":          `{"anthropic":"api.anthropic.com"}`,
			"a broken glob":               `{"routes":[{"match":"glm-[","upstream":"https://api.z.ai"}]}`,
		} {
			_, err := loadFrom(t, envWithConfig(t, text))
			if err == nil {
				t.Errorf("%s: LoadConfig = nil error, want one", name)
				continue
			}
			if !strings.Contains(err.Error(), "gateway.json") {
				t.Errorf("%s: error %q does not name gateway.json", name, err)
			}
		}
	})
}

func TestUpstreamFor(t *testing.T) {
	config := Config{
		Routes: []Route{
			{Match: "glm-*", Upstream: "https://glm.example"},
			{Match: "glm-5.3*", Upstream: "https://glm-53.example"},
			{Match: "*-flash", Upstream: "https://flash.example"},
			{Match: "kimi-k2", Upstream: "https://kimi.example"},
		},
		Anthropic: "https://anthropic.example",
		OpenAI:    "https://openai.example",
	}
	cases := []struct {
		name  string
		kind  apiKind
		model string
		want  string
	}{
		{"the longest matching glob wins", kindAnthropic, "glm-5.3-flash", "https://glm-53.example"},
		{"a shorter glob holds when nothing longer fits", kindAnthropic, "glm-4.7", "https://glm.example"},
		{"a suffix glob matches its tail", kindChat, "deepseek-chat-flash", "https://flash.example"},
		{"an exact glob matches only itself", kindResponses, "kimi-k2", "https://kimi.example"},
		{"no match falls to the anthropic default", kindAnthropic, "claude-sonnet-4", "https://anthropic.example"},
		{"no match falls to the openai default", kindChat, "gpt-5", "https://openai.example"},
		{"no match falls to the openai default for responses too", kindResponses, "gpt-5", "https://openai.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := config.UpstreamFor(tc.kind, tc.model); got != tc.want {
				t.Errorf("UpstreamFor(%s, %q) = %q, want %q", tc.kind, tc.model, got, tc.want)
			}
		})
	}
	empty := Config{}
	if got := empty.UpstreamFor(kindAnthropic, "glm-5.3"); got != "" {
		t.Errorf("UpstreamFor on an empty config = %q, want %q", got, "")
	}
	// a tie between globs of the same length takes the one the file named first
	tied := Config{Routes: []Route{
		{Match: "glm-*", Upstream: "https://first.example"},
		{Match: "glm-*", Upstream: "https://second.example"},
	}}
	if got := tied.UpstreamFor(kindAnthropic, "glm-4"); got != "https://first.example" {
		t.Errorf("tied globs = %q, want the first named", got)
	}
}

// A config that is there but cannot be read — a directory where the file belongs, for one
// — is refused with the file named, the way a broken one is.
func TestLoadConfigRefusesAnUnreadableFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "gateway.json")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := loadFrom(t, domain.EnvLookup{"HUDDLE_HOME": home})
	var bad *ConfigError
	if !errors.As(err, &bad) {
		t.Fatalf("error %T (%v), want a *ConfigError", err, err)
	}
	if bad.Path != path {
		t.Errorf("path = %q, want %q", bad.Path, path)
	}
}

// The OpenAI default holds itself to the same rule as the Anthropic one and as every route.
func TestLoadConfigRefusesABadOpenAIDefault(t *testing.T) {
	_, err := loadFrom(t, envWithConfig(t, `{"openai":"api.openai.com"}`))
	var bad *ConfigError
	if !errors.As(err, &bad) {
		t.Fatalf("error %T (%v), want a *ConfigError", err, err)
	}
	if !strings.Contains(bad.Reason, "openai") {
		t.Errorf("reason = %q, want the openai default named", bad.Reason)
	}
}

// resolveHome reads the one variable the environment names, in the order every entry does:
// HUDDLE_HOME, then XDG_CONFIG_HOME, then HOME, and an error when none is set.
func TestResolveHomeFollowsTheEnvironment(t *testing.T) {
	if _, err := resolveHome(domain.EnvLookup{}); err == nil {
		t.Error("resolveHome with an empty environment = nil error, want one")
	}
	given := t.TempDir()
	if got, err := resolveHome(domain.EnvLookup{"HUDDLE_HOME": given}); err != nil || got != given {
		t.Errorf("resolveHome = %q, %v; want %q, nil", got, err, given)
	}
	xdg := t.TempDir()
	if got, err := resolveHome(domain.EnvLookup{"XDG_CONFIG_HOME": xdg}); err != nil || got != filepath.Join(xdg, "huddle") {
		t.Errorf("resolveHome = %q, %v; want the huddle dir under the XDG root", got, err)
	}
	user := t.TempDir()
	if got, err := resolveHome(domain.EnvLookup{"HOME": user}); err != nil || got != filepath.Join(user, ".config", "huddle") {
		t.Errorf("resolveHome = %q, %v; want the huddle dir under the user's config", got, err)
	}
}

// A file that names no family default gets the API's own host for it, so a harness pointed at
// the gateway reaches its provider with no gateway.json at all; a default the file names wins.
func TestLoadConfigFillsTheFamilyDefaults(t *testing.T) {
	config, err := loadFrom(t, envWithConfig(t, `{"routes":[{"match":"glm-*","upstream":"https://glm.example"}]}`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if config.Anthropic != "https://api.anthropic.com" || config.OpenAI != "https://api.openai.com" {
		t.Errorf("defaults = %q, %q; want the API hosts", config.Anthropic, config.OpenAI)
	}
	named, err := loadFrom(t, envWithConfig(t, `{"openai":"https://openai.example"}`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if named.OpenAI != "https://openai.example" || named.Anthropic != "https://api.anthropic.com" {
		t.Errorf("config = %+v, want the named openai default kept and anthropic filled", named)
	}
}

// A GLM model on the Anthropic route goes to Z.ai's Anthropic endpoint when gateway.json names
// no route for it, so a harness pointed at the gateway with a GLM model reaches Z.ai with no
// file at all. A route the file names still wins, the OpenAI routes keep their default, and
// every other model keeps the Anthropic default.
func TestUpstreamForSendsGLMToZaiWhenNoRouteNamesIt(t *testing.T) {
	const zai = "https://api.z.ai/api/anthropic"
	bare, err := loadFrom(t, envWithConfig(t, ""))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	named, err := loadFrom(t, envWithConfig(t, `{"routes":[{"match":"glm-5*","upstream":"https://mine.example"}],"anthropic":"https://anthropic.example"}`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cases := []struct {
		name   string
		config Config
		kind   apiKind
		model  string
		want   string
	}{
		{"zcode's uppercase GLM", bare, kindAnthropic, "GLM-5.3", zai},
		{"a lowercase glm", bare, kindAnthropic, "glm-4.7", zai},
		{"a claude model keeps the anthropic default", bare, kindAnthropic, "claude-sonnet-4-5", "https://api.anthropic.com"},
		{"a glm on chat completions keeps the openai default", bare, kindChat, "glm-4.7", "https://api.openai.com"},
		{"a route the file names wins", named, kindAnthropic, "glm-5.3", "https://mine.example"},
		{"a glm the file's route misses still reaches z.ai", named, kindAnthropic, "GLM-5.3", zai},
		{"the file's anthropic default does not take glm", named, kindAnthropic, "glm-4.7", zai},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.config.UpstreamFor(tc.kind, tc.model); got != tc.want {
				t.Errorf("UpstreamFor(%s, %q) = %q, want %q", tc.kind, tc.model, got, tc.want)
			}
		})
	}
}
