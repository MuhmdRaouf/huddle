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
		}
		if !reflect.DeepEqual(config, want) {
			t.Errorf("config = %+v, want %+v", config, want)
		}
	})

	t.Run("a missing file is an empty config, not an error", func(t *testing.T) {
		config, err := loadFrom(t, envWithConfig(t, ""))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if len(config.Routes) != 0 || config.Anthropic != "" || config.OpenAI != "" {
			t.Errorf("config = %+v, want the zero config", config)
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
