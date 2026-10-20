package domain

import "testing"

func TestHuddleRouterPort(t *testing.T) {
	if HuddleRouterPort != 18786 {
		t.Fatalf("HuddleRouterPort = %d, want 18786", HuddleRouterPort)
	}
}

func TestHuddleRouterURL(t *testing.T) {
	cases := []struct {
		name string
		env  EnvLookup
		want string
	}{
		{"no override is the canonical port", EnvLookup{}, "http://127.0.0.1:18786"},
		{"the override moves the port", EnvLookup{"HUDDLE_ROUTER_PORT": "19000"}, "http://127.0.0.1:19000"},
		{"a junk override falls back", EnvLookup{"HUDDLE_ROUTER_PORT": "not-a-port"}, "http://127.0.0.1:18786"},
		{"zero falls back the way Number(x) || port does", EnvLookup{"HUDDLE_ROUTER_PORT": "0"}, "http://127.0.0.1:18786"},
		{"whitespace around the override is trimmed", EnvLookup{"HUDDLE_ROUTER_PORT": " 19001 "}, "http://127.0.0.1:19001"},
		{"an empty override is unset", EnvLookup{"HUDDLE_ROUTER_PORT": ""}, "http://127.0.0.1:18786"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HuddleRouterURL(tc.env); got != tc.want {
				t.Errorf("HuddleRouterURL(%v) = %q, want %q", tc.env, got, tc.want)
			}
		})
	}
}

// TestRouterBaseURLIsHuddleRouterForEveryProvider pins the one address: every provider's setup
// points the settings at huddle-router, whatever its own router port says (the old router code
// still reads RouterPort until it is deleted).
func TestRouterBaseURLIsHuddleRouterForEveryProvider(t *testing.T) {
	zai := Provider{Name: "zai", Router: ProviderRouter{Port: 18787}}
	kimi := Provider{Name: "kimi", Router: ProviderRouter{Port: 18788}}
	for _, provider := range []Provider{zai, kimi} {
		if got := RouterBaseURL(provider, EnvLookup{}); got != "http://127.0.0.1:18786" {
			t.Errorf("RouterBaseURL(%s) = %q, want huddle-router's URL", provider.Name, got)
		}
	}
	if got := RouterBaseURL(zai, EnvLookup{"HUDDLE_ROUTER_PORT": "19000"}); got != "http://127.0.0.1:19000" {
		t.Errorf("RouterBaseURL(zai, override) = %q, want the overridden huddle-router URL", got)
	}
}
