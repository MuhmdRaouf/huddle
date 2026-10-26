// The no-chain rule: a router never routes upstream through a router. The daemon's engine
// environment drops every proxy variable that names one — this router at huddle-router's
// canonical address or on its own port, or any loopback proxy that answers huddle-router's
// health path — and keeps the rest, so the user's own proxy survives.
package routerd

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// proxyProbeWait is how long one health probe of a loopback proxy may take.
const proxyProbeWait = 300 * time.Millisecond

// CleanProxyEnv returns a copy of env without the proxy variables that name a router, each
// dropped variable logged once. A loopback proxy is recognised by asking it for huddle-router's
// health answer; a non-loopback proxy is kept and never contacted.
func CleanProxyEnv(ctx context.Context, env domain.EnvLookup, ownPort int, log func(string)) domain.EnvLookup {
	return cleanProxyEnv(ctx, env, ownPort, log, nil)
}

// cleanProxyEnv is CleanProxyEnv with the probe client a test counts dials through; nil takes
// the direct client.
func cleanProxyEnv(ctx context.Context, env domain.EnvLookup, ownPort int, log func(string), client *http.Client) domain.EnvLookup {
	if log == nil {
		log = func(string) {}
	}
	out := make(domain.EnvLookup, len(env))
	for name, value := range env {
		out[name] = value
	}
	for _, name := range domain.ProxyEnvVars {
		value, present := out[name]
		if !present || strings.TrimSpace(value) == "" {
			continue
		}
		if domain.IsRouterProxy(value, int64(ownPort)) || answersRouterHealth(ctx, value, client) {
			delete(out, name)
			log(fmt.Sprintf("huddle-router: %s=%s is a router; going direct", name, value))
		}
	}
	return out
}

// answersRouterHealth is whether a loopback proxy answers huddle-router's health path. A proxy
// that is not loopback is never contacted, and a value without a port is never a router of ours.
func answersRouterHealth(ctx context.Context, raw string, client *http.Client) bool {
	parsed, err := url.Parse(proxyValueURL(raw))
	if err != nil || parsed.Port() == "" || !domain.IsLoopback(parsed.Hostname()) {
		return false
	}
	if client == nil {
		client = &http.Client{Timeout: proxyProbeWait, Transport: &http.Transport{Proxy: nil}}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+parsed.Host+router.MultiHealthPath, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(request)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// proxyValueURL adds the scheme a proxy value without one reads with.
func proxyValueURL(raw string) string {
	if !strings.Contains(raw, "://") {
		return "http://" + raw
	}
	return raw
}
