package shared

import (
	"net/url"
	"strings"
)

// ProviderOf maps a model id to a provider by prefix, case-insensitively. The empty id —
// the caller's stand-in for a missing one — is "other", like anything unknown.
func ProviderOf(model string) string {
	id := strings.ToLower(model)
	switch {
	case strings.HasPrefix(id, "claude-"):
		return "Anthropic"
	case strings.HasPrefix(id, "glm-"):
		return "Z.ai"
	case strings.HasPrefix(id, "kimi-"):
		return "Moonshot"
	case strings.HasPrefix(id, "deepseek-"):
		return "DeepSeek"
	case strings.HasPrefix(id, "minimax-"):
		return "MiniMax"
	case strings.HasPrefix(id, "qwen"), strings.HasPrefix(id, "qwq"):
		return "Qwen"
	default:
		return "other"
	}
}

// DefaultUpstream is where a request went when nothing says otherwise.
const DefaultUpstream = "https://api.anthropic.com"

// DirectUpstream names a request with no recorded upstream: Claude Code called its API
// without a router in between.
const DirectUpstream = "Claude Code direct (no router)"

// UpstreamHost shows a URL as its host; a value that does not parse as a URL shows as it
// is, and an empty one (a request with no recorded upstream) is a direct call.
func UpstreamHost(raw string) string {
	if raw == "" {
		return DirectUpstream
	}
	host, ok := jsURLHost(raw)
	if !ok {
		return raw
	}
	return host
}

// jsURLHost reads a URL's host the way JavaScript's new URL does: the string needs a
// scheme before it parses at all, the host lowercases, and a default port disappears.
func jsURLHost(raw string) (string, bool) {
	if !hasURLScheme(raw) {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Host)
	if def, ok := defaultPorts[strings.ToLower(parsed.Scheme)]; ok {
		host = strings.TrimSuffix(host, ":"+def)
	}
	return host, true
}

// defaultPorts are the ports new URL leaves off an http(s) host.
var defaultPorts = map[string]string{"http": "80", "https": "443"}

// hasURLScheme reports whether raw starts with a URL scheme — a letter, then letters,
// digits, "+", "-" or ".", then ":" — the least new URL needs before it even tries.
func hasURLScheme(raw string) bool {
	colon := strings.IndexByte(raw, ':')
	if colon <= 0 {
		return false
	}
	for i := 0; i < colon; i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

// pluginDisplay maps a provider plugin's name to how people read it.
var pluginDisplay = map[string]string{
	"zai":      "Z.ai",
	"kimi":     "Kimi",
	"deepseek": "DeepSeek",
	"minimax":  "MiniMax",
	"qwen":     "Qwen",
}

// PluginLabel is a provider plugin's display name ("zai" → "Z.ai"); unknown names show
// as they are.
func PluginLabel(plugin string) string {
	if label, ok := pluginDisplay[plugin]; ok {
		return label
	}
	return plugin
}

// ScopeLabel is a budget scope as people read it: "total" → "Total", "provider:zai" →
// "Z.ai".
func ScopeLabel(scope string) string {
	if scope == "total" {
		return "Total"
	}
	if name, ok := strings.CutPrefix(scope, "provider:"); ok {
		return PluginLabel(name)
	}
	return PluginLabel(scope)
}
