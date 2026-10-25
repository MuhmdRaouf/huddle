package router

// One lane as the router processes build it: the endpoint overrides, the key the environment
// carries, and the error a lane's caller logs in its own words.

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestLaneOfBuildsTheLaneTheEnvironmentNames(t *testing.T) {
	t.Parallel()
	env := domain.EnvLookup{
		"ZAI_ROUTER_URL":     "http://127.0.0.1:1/api/anthropic",
		"ZAI_API_KEY":        "env-key",
		"HUDDLE_ROUTER_PORT": "20001",
		"HOME":               t.TempDir(),
	}
	lane, err := LaneOf(routerProvider, env, func(string) {})
	if err != nil {
		t.Fatalf("LaneOf: %v", err)
	}
	if lane.ProviderURL.Host != "127.0.0.1:1" {
		t.Errorf("ProviderURL = %v, want the override", lane.ProviderURL)
	}
	if lane.Anthropic.Host != "api.anthropic.com" {
		t.Errorf("Anthropic = %v, want the default", lane.Anthropic)
	}
	if lane.KeyLookup == nil {
		t.Fatal("the lane carries no key lookup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key, workerErr := lane.KeyLookup(ctx)
	if workerErr != nil || key.Value != "env-key" || key.Source != "ZAI_API_KEY" {
		t.Errorf("the lane's key = %q from %s (%v), want the environment's", key.Value, key.Source, workerErr)
	}
}

func TestLaneOfNamesTheOverrideThatDoesNotParse(t *testing.T) {
	t.Parallel()
	env := domain.EnvLookup{"ZAI_ROUTER_URL": "http://x/\u007f"}
	_, err := LaneOf(routerProvider, env, func(string) {})
	bad, ok := err.(*BadURL)
	if !ok || bad.Name != "ZAI_ROUTER_URL" {
		t.Fatalf("LaneOf = %v, want a *BadURL naming the override", err)
	}
	if bad.Error() == "" || bad.Unwrap() == nil {
		t.Error("the BadURL carries no words or no cause")
	}
	anthropicEnv := domain.EnvLookup{"ZAI_ROUTER_ANTHROPIC_URL": "http://x/\u007f"}
	if _, err := LaneOf(routerProvider, anthropicEnv, func(string) {}); err == nil {
		t.Error("LaneOf of an unparsable Anthropic = nil error, want one")
	}
}

func TestEnvOrDefaultFallsBackOnlyOnAnEmptyValue(t *testing.T) {
	t.Parallel()
	env := domain.EnvLookup{"set": "value", "empty": ""}
	if got := envOrDefault(env, "set", "fallback"); got != "value" {
		t.Errorf("envOrDefault = %q, want the value", got)
	}
	if got := envOrDefault(env, "empty", "fallback"); got != "fallback" {
		t.Errorf("envOrDefault = %q, want the fallback", got)
	}
	if got := envOrDefault(env, "absent", "fallback"); got != "fallback" {
		t.Errorf("envOrDefault = %q, want the fallback", got)
	}
}

func TestPortOfReadsTheVariableOrTheCanonicalPort(t *testing.T) {
	t.Parallel()
	port, err := PortOf(domain.EnvLookup{})
	if err != nil || port != domain.HuddleRouterPort {
		t.Errorf("PortOf = %d, %v, want the canonical %d", port, err, domain.HuddleRouterPort)
	}
	port, err = PortOf(domain.EnvLookup{"HUDDLE_ROUTER_PORT": "20001"})
	if err != nil || port != 20001 {
		t.Errorf("PortOf = %d, %v, want 20001", port, err)
	}
	for _, bad := range []string{"nope", "0", "70000"} {
		env := domain.EnvLookup{"HUDDLE_ROUTER_PORT": bad}
		if _, err := PortOf(env); err == nil {
			t.Errorf("PortOf(%q) = nil error, want one", bad)
		}
	}
}

func TestWorkerKeyReadsTheEnvironmentBeforeAnythingElse(t *testing.T) {
	t.Parallel()
	env := domain.EnvLookup{"ZAI_API_KEY": "worker-key", "HOME": t.TempDir()}
	key, workerErr := workerKey(context.Background(), routerProvider, env)
	if workerErr != nil || key.Value != "worker-key" {
		t.Fatalf("workerKey = %q (%v), want the environment's key", key.Value, workerErr)
	}
}

func TestLaneOfCarriesTheLaneFallbackList(t *testing.T) {
	t.Parallel()
	env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": t.TempDir(), "HOME": t.TempDir()}
	writeEnabled(t, env, `{"providers":["zai"],"lanes":{"zai":{"fallback":["glm-5.3-flash"]}}}`)
	lane, err := LaneOf(routerProvider, env, func(string) {})
	if err != nil {
		t.Fatalf("LaneOf: %v", err)
	}
	if len(lane.Fallback) != 1 || lane.Fallback[0] != "glm-5.3-flash" {
		t.Errorf("Fallback = %v, want providers.json's list", lane.Fallback)
	}
}
