// The gateway's config: $HUDDLE_HOME/gateway.json names routes — a model glob to an upstream —
// and the two family defaults a model no route names falls back to. A missing file, or a default
// it leaves out, is the API's own host, and a GLM model on the Anthropic route no route names
// goes to Z.ai; anything unreadable or ill-shaped is a startup error naming the file.
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

// zaiAnthropic is Z.ai's Anthropic Messages root, the one its coding plans speak.
const zaiAnthropic = "https://api.z.ai/api/anthropic"

// builtinAnthropicRoutes are the routes a model on the Anthropic route falls to when no route
// the file names matches it, ahead of the family default: GLM models go to Z.ai, so a harness
// that speaks GLM on its own Z.ai key (zcode) reaches Z.ai through the gateway with no file.
func builtinAnthropicRoutes() []Route {
	return []Route{{Match: "GLM-*", Upstream: zaiAnthropic}, {Match: "glm-*", Upstream: zaiAnthropic}}
}

// Config is the gateway's whole configuration.
type Config struct {
	Routes []Route `json:"routes"`
	// Anthropic and OpenAI are the family defaults: where a request goes when no route's glob
	// names its model.
	Anthropic string `json:"anthropic"`
	OpenAI    string `json:"openai"`
	// builtin is the Anthropic routes the file cannot name, filled with the defaults.
	builtin []Route
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
	c.builtin = builtinAnthropicRoutes()
	return c
}

// validUpstream answers whether raw is an absolute http or https URL.
func validUpstream(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// UpstreamFor answers where a model on one API kind goes: the longest route glob that matches
// wins (a tie goes to the one the file named first), else on the Anthropic route the built-in
// route that matches, else the family default, else nothing.
func (c Config) UpstreamFor(kind apiKind, model string) string {
	if best := longestMatch(c.Routes, model); best != "" {
		return best
	}
	if kind.family() == "anthropic" {
		if builtin := longestMatch(c.builtin, model); builtin != "" {
			return builtin
		}
		return c.Anthropic
	}
	return c.OpenAI
}

// longestMatch answers the upstream of the longest glob that matches the model, the first
// named on a tie; "" when none does.
func longestMatch(routes []Route, model string) string {
	best, bestLen := "", 0
	for _, route := range routes {
		ok, _ := doublestar.Match(route.Match, model)
		if !ok || len(route.Match) <= bestLen {
			continue
		}
		best, bestLen = route.Upstream, len(route.Match)
	}
	return best
}
