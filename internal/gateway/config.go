// The gateway's config: $HUDDLE_HOME/gateway.json names routes — a model glob to an upstream —
// and the two family defaults a model no route names falls back to. A missing file, or a default
// it leaves out, is the API's own host; anything unreadable or ill-shaped is a startup error
// naming the file.
package gateway

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	doublestar "github.com/bmatcuk/doublestar/v4"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// Route sends the models its glob matches to one upstream: the API root the request's path
// hangs off, as `https://api.z.ai/api/anthropic` is the root of `/v1/messages`.
type Route struct {
	Match    string `json:"match"`
	Upstream string `json:"upstream"`
}

// The family defaults a config that names none falls back to: each API's own host, so a
// harness pointed at the gateway reaches its provider with no gateway.json at all.
const (
	defaultAnthropic = "https://api.anthropic.com"
	defaultOpenAI    = "https://api.openai.com"
)

// Config is the gateway's whole configuration.
type Config struct {
	Routes []Route `json:"routes"`
	// Anthropic and OpenAI are the family defaults: where a request goes when no route's glob
	// names its model.
	Anthropic string `json:"anthropic"`
	OpenAI    string `json:"openai"`
}

// ConfigError is why a gateway refuses to start: the file and what is wrong with it.
type ConfigError struct {
	Path   string
	Reason string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("gateway: %s: %s", e.Path, e.Reason)
}

// ConfigPath is the config file a home root carries.
func ConfigPath(root string) string {
	return filepath.Join(root, "gateway.json")
}

// resolveHome answers the huddle home the environment names, the way every entry resolves it.
func resolveHome(env domain.EnvLookup) (string, error) {
	look := func(key string) string { return env[key] }
	resolved, err := home.Resolve(look)
	if err != nil {
		return "", err
	}
	return resolved.Root, nil
}

// LoadConfig reads the config the environment's huddle home names. A file that is not there is
// a config of the built-in family defaults, and a family default the file leaves out is the
// built-in one; a file that does not hold up is a *ConfigError naming the file.
func LoadConfig(env domain.EnvLookup) (Config, error) {
	root, err := resolveHome(env)
	if err != nil {
		return Config{}, err
	}
	path := ConfigPath(root)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}.withDefaults(), nil
		}
		return Config{}, &ConfigError{Path: path, Reason: err.Error()}
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, &ConfigError{Path: path, Reason: err.Error()}
	}
	for _, route := range config.Routes {
		if strings.TrimSpace(route.Match) == "" {
			return Config{}, &ConfigError{Path: path, Reason: "a route without a match"}
		}
		if !doublestar.ValidatePattern(route.Match) {
			return Config{}, &ConfigError{Path: path, Reason: fmt.Sprintf("route match %q is not a glob", route.Match)}
		}
		if !validUpstream(route.Upstream) {
			return Config{}, &ConfigError{Path: path, Reason: fmt.Sprintf("route %q: upstream must be an http(s) URL, got %q", route.Match, route.Upstream)}
		}
	}
	if config.Anthropic != "" && !validUpstream(config.Anthropic) {
		return Config{}, &ConfigError{Path: path, Reason: fmt.Sprintf("anthropic must be an http(s) URL, got %q", config.Anthropic)}
	}
	if config.OpenAI != "" && !validUpstream(config.OpenAI) {
		return Config{}, &ConfigError{Path: path, Reason: fmt.Sprintf("openai must be an http(s) URL, got %q", config.OpenAI)}
	}
	return config.withDefaults(), nil
}

// withDefaults answers the config with the built-in family default wherever it names none.
func (c Config) withDefaults() Config {
	if c.Anthropic == "" {
		c.Anthropic = defaultAnthropic
	}
	if c.OpenAI == "" {
		c.OpenAI = defaultOpenAI
	}
	return c
}

// validUpstream answers whether raw is an absolute http or https URL.
func validUpstream(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// UpstreamFor answers where a model on one API kind goes: the longest route glob that matches
// wins (a tie goes to the one the file named first), else the family default, else nothing.
func (c Config) UpstreamFor(kind apiKind, model string) string {
	best, bestLen := "", 0
	for _, route := range c.Routes {
		ok, _ := doublestar.Match(route.Match, model)
		if !ok || len(route.Match) <= bestLen {
			continue
		}
		best, bestLen = route.Upstream, len(route.Match)
	}
	if best != "" {
		return best
	}
	if kind.family() == "anthropic" {
		return c.Anthropic
	}
	return c.OpenAI
}
