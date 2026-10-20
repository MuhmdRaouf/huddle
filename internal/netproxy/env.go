// The proxy environment as an explicit map: which outbound request goes through which proxy, read
// from the variables in either case (lowercase first), NO_PROXY honoured, and a proxy that names a
// plugin router refused — routing the router's own upstream through the router is a loop.
package netproxy

import (
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/MuhmdRaouf/huddle/internal/core/domain"
)

// Env is the environment proxy settings are read from.
type Env map[string]string

// envValue reads one variable from the environment, lowercase first, trimmed; an empty value
// counts as unset.
func envValue(env Env, name string) (string, bool) {
	for _, key := range []string{strings.ToLower(name), strings.ToUpper(name)} {
		if value, present := env[key]; present && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// splitNoProxy splits a NO_PROXY list on runs of spaces and commas, dropping empties.
func splitNoProxy(list string) []string {
	return strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}

// NoProxy is true when NO_PROXY exempts the host: `*`, the host itself, or a domain suffix (`.corp`,
// `corp`, `*.corp`).
func NoProxy(host, port string, env Env) bool {
	list, ok := envValue(env, "NO_PROXY")
	if !ok {
		return false
	}
	for _, entry := range splitNoProxy(list) {
		if entry == "*" {
			return true
		}
		name := entry
		only := ""
		hasPort := false
		if index := strings.IndexByte(entry, ':'); index >= 0 {
			name = entry[:index]
			only = entry[index+1:]
			hasPort = true
			// The port part is the entry's second segment; anything after another colon is not
			// part of it.
			if end := strings.IndexByte(only, ':'); end >= 0 {
				only = only[:end]
			}
		}
		if hasPort && only != port {
			continue
		}
		exempt := strings.TrimPrefix(name, "*.")
		exempt = strings.ToLower(strings.TrimPrefix(exempt, "."))
		lower := strings.ToLower(host)
		if lower == exempt || strings.HasSuffix(lower, "."+exempt) {
			return true
		}
	}
	return false
}

// ExemptLoopback is an environment with loopback exempt from any proxy: NO_PROXY (and no_proxy)
// gain 127.0.0.1 and localhost whenever a proxy is set. Front ends, workers and peer routers talk
// over loopback, and an HTTP client may apply HTTP_PROXY to every request it sends unless NO_PROXY
// exempts the host. Keys come out sorted, so a build stays deterministic.
func ExemptLoopback(env map[string]string) map[string]string {
	proxySet := false
	for name, value := range env {
		if (strings.EqualFold(name, "http_proxy") || strings.EqualFold(name, "https_proxy")) && strings.TrimSpace(value) != "" {
			proxySet = true
			break
		}
	}
	if !proxySet {
		out := make(map[string]string, len(env))
		for name, value := range env {
			out[name] = value
		}
		return out
	}
	current := ""
	if value, present := env["no_proxy"]; present {
		current = value
	} else if value, present := env["NO_PROXY"]; present {
		current = value
	} else {
		keys := make([]string, 0, len(env))
		for name := range env {
			if strings.EqualFold(name, "no_proxy") {
				keys = append(keys, name)
			}
		}
		slices.Sort(keys)
		if len(keys) > 0 {
			current = env[keys[0]]
		}
	}
	entries := splitNoProxy(current)
	for _, loopback := range []string{"127.0.0.1", "localhost"} {
		present := false
		for _, entry := range entries {
			if entry == loopback {
				present = true
				break
			}
		}
		if !present {
			entries = append(entries, loopback)
		}
	}
	out := make(map[string]string, len(env)+2)
	for name, value := range env {
		if !strings.EqualFold(name, "no_proxy") {
			out[name] = value
		}
	}
	joined := strings.Join(entries, ",")
	out["NO_PROXY"] = joined
	out["no_proxy"] = joined
	return out
}

// ProxyFor is the proxy a request to the target goes through, from HTTPS_PROXY/HTTP_PROXY (either
// case), or none. A proxy naming a plugin router is no proxy: routing the router's own upstream
// through the router is a loop.
func ProxyFor(target *url.URL, env Env) (*url.URL, bool) {
	port := target.Port()
	if port == "" {
		if target.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if NoProxy(target.Hostname(), port, env) {
		return nil, false
	}
	name := "HTTP_PROXY"
	if target.Scheme == "https" {
		name = "HTTPS_PROXY"
	}
	raw, ok := envValue(env, name)
	if !ok {
		return nil, false
	}
	if domain.IsPluginRouterProxy(raw) {
		return nil, false
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	proxy, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	return proxy, true
}
