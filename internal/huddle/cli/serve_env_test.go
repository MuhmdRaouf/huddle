package cli

// The hub's child server never inherits a proxy variable that names a router of ours: the hub's
// own requests must not chain into one (the 2026-10-21 incident). The user's other proxies ride
// along.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestWithoutRouterProxies(t *testing.T) {
	env := []string{
		"HUDDLE_ROUTER_PORT=20999",
		"HTTPS_PROXY=http://127.0.0.1:20999",
		"https_proxy=127.0.0.1:" + strconv.Itoa(domain.HuddleRouterPort),
		"HTTP_PROXY=http://proxy.corp:3128",
		"NO_PROXY=corp",
		"ZAI_API_KEY=k",
		"PORT=18860",
	}
	got := strings.Join(withoutRouterProxies(env), "\n")
	for _, dropped := range []string{"HTTPS_PROXY=http://127.0.0.1:20999", "https_proxy=127.0.0.1:" + strconv.Itoa(domain.HuddleRouterPort)} {
		if strings.Contains(got, dropped) {
			t.Errorf("withoutRouterProxies kept %s, want the router proxy dropped", dropped)
		}
	}
	for _, kept := range []string{"HTTP_PROXY=http://proxy.corp:3128", "NO_PROXY=corp", "ZAI_API_KEY=k", "PORT=18860"} {
		if !strings.Contains(got, kept) {
			t.Errorf("withoutRouterProxies lost %s, want every non-router entry kept", kept)
		}
	}
}

func TestWithoutRouterProxiesFallsBackToTheCanonicalPort(t *testing.T) {
	env := []string{"HUDDLE_ROUTER_PORT=not-a-port", "ALL_PROXY=127.0.0.1:" + strconv.Itoa(domain.HuddleRouterPort)}
	got := strings.Join(withoutRouterProxies(env), "\n")
	if strings.Contains(got, "ALL_PROXY") {
		t.Errorf("withoutRouterProxies kept %q, want the canonical port's router proxy dropped", env[1])
	}
}
