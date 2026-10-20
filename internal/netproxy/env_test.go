package netproxy

// The proxy environment read from an explicit map: NO_PROXY's spellings, the scheme's variable in
// either case with lowercase first, the loopback exemption and the router-loop guard.

import (
	"net/url"
	"reflect"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return parsed
}

func TestProxyEnvironment(t *testing.T) {
	t.Parallel()
	t.Run("exempts loopback from a proxy: NO_PROXY and no_proxy gain 127.0.0.1 and localhost, only when a proxy is set", func(t *testing.T) {
		if got := ExemptLoopback(map[string]string{"HOME": "/h"}); !reflect.DeepEqual(got, map[string]string{"HOME": "/h"}) {
			t.Errorf("ExemptLoopback without a proxy = %v", got)
		}
		if got := ExemptLoopback(map[string]string{"HTTPS_PROXY": " "}); !reflect.DeepEqual(got, map[string]string{"HTTPS_PROXY": " "}) {
			t.Errorf("ExemptLoopback with a blank proxy = %v", got)
		}
		if got := ExemptLoopback(map[string]string{"https_proxy": "http://p:1"}); !reflect.DeepEqual(got, map[string]string{
			"https_proxy": "http://p:1",
			"NO_PROXY":    "127.0.0.1,localhost",
			"no_proxy":    "127.0.0.1,localhost",
		}) {
			t.Errorf("ExemptLoopback = %v", got)
		}
		if got := ExemptLoopback(map[string]string{"HTTP_PROXY": "http://p:1", "No_Proxy": "corp, localhost"}); !reflect.DeepEqual(got, map[string]string{
			"HTTP_PROXY": "http://p:1",
			"NO_PROXY":   "corp,localhost,127.0.0.1",
			"no_proxy":   "corp,localhost,127.0.0.1",
		}) {
			t.Errorf("ExemptLoopback = %v", got)
		}
		// Lowercase wins, as in NoProxy.
		got := ExemptLoopback(map[string]string{"HTTP_PROXY": "http://p:1", "no_proxy": "a", "NO_PROXY": "b"})
		if got["NO_PROXY"] != "a,127.0.0.1,localhost" {
			t.Errorf("NO_PROXY = %q", got["NO_PROXY"])
		}
		if got := ExemptLoopback(map[string]string{"HTTP_PROXY": "http://p:1", "NO_PROXY": "corp"}); !reflect.DeepEqual(got, map[string]string{
			"HTTP_PROXY": "http://p:1",
			"NO_PROXY":   "corp,127.0.0.1,localhost",
			"no_proxy":   "corp,127.0.0.1,localhost",
		}) {
			t.Errorf("ExemptLoopback = %v", got)
		}
		// Two oddly spelled no_proxy keys: the first in sorted order wins.
		got = ExemptLoopback(map[string]string{"HTTP_PROXY": "http://p:1", "No_Proxy": "a", "NO_proxY": "b"})
		if got["NO_PROXY"] != "b,127.0.0.1,localhost" {
			t.Errorf("NO_PROXY = %q", got["NO_PROXY"])
		}
	})

	t.Run("honours NO_PROXY: everything, a host, a domain suffix in its three spellings, a port", func(t *testing.T) {
		cases := []struct {
			host string
			port string
			env  Env
			want bool
		}{
			{"api.anthropic.com", "443", Env{}, false},
			{"api.anthropic.com", "443", Env{"NO_PROXY": "*"}, true},
			{"api.anthropic.com", "443", Env{"no_proxy": "api.anthropic.com"}, true},
			{"api.anthropic.com", "443", Env{"NO_PROXY": ".anthropic.com"}, true},
			{"api.anthropic.com", "443", Env{"NO_PROXY": "*.anthropic.com"}, true},
			{"api.anthropic.com", "443", Env{"NO_PROXY": "anthropic.com"}, true},
			{"api.anthropic.com", "443", Env{"NO_PROXY": "anthropic.com:8443"}, false},
			{"api.anthropic.com", "443", Env{"NO_PROXY": "other.com, ,api.anthropic.com:443"}, true},
			{"api.anthropic.com", "443", Env{"NO_PROXY": "anthropic.com:443:9"}, true},
			{"notanthropic.com", "443", Env{"NO_PROXY": "anthropic.com"}, false},
		}
		for _, tc := range cases {
			if got := NoProxy(tc.host, tc.port, tc.env); got != tc.want {
				t.Errorf("NoProxy(%q, %q, %v) = %v, want %v", tc.host, tc.port, tc.env, got, tc.want)
			}
		}
	})

	t.Run("picks HTTPS_PROXY or HTTP_PROXY by the target's scheme, in either case, lowercase first", func(t *testing.T) {
		https := mustURL(t, "https://api.anthropic.com/v1")
		plain := mustURL(t, "http://127.0.0.1:9/x")
		if proxy, ok := ProxyFor(https, Env{}); ok {
			t.Errorf("ProxyFor(no env) = %v", proxy)
		}
		cases := []struct {
			target *url.URL
			env    Env
			want   string
		}{
			{https, Env{"HTTPS_PROXY": "http://proxy:3128"}, "http://proxy:3128"},
			{https, Env{"https_proxy": "proxy:1", "HTTPS_PROXY": "http://other:2"}, "http://proxy:1"},
			{plain, Env{"HTTP_PROXY": "http://proxy:3128", "HTTPS_PROXY": "http://no:1"}, "http://proxy:3128"},
			{mustURL(t, "http://plain.example/y"), Env{"HTTP_PROXY": "proxy:1"}, "http://proxy:1"},
		}
		for _, tc := range cases {
			proxy, ok := ProxyFor(tc.target, tc.env)
			if !ok || proxy.String() != tc.want {
				t.Errorf("ProxyFor(%v, %v) = %v, %v, want %q", tc.target, tc.env, proxy, ok, tc.want)
			}
		}
		for _, env := range []Env{
			{"HTTPS_PROXY": "  "},
			{"HTTPS_PROXY": "http://[bad"},
			{"HTTPS_PROXY": "http://proxy:1", "NO_PROXY": "anthropic.com"},
		} {
			if proxy, ok := ProxyFor(https, env); ok {
				t.Errorf("ProxyFor(%v) = %v, want none", env, proxy)
			}
		}
		// A proxy naming a plugin router is no proxy: routing the router's own upstream through
		// the router is a loop.
		for _, env := range []Env{
			{"HTTPS_PROXY": "http://127.0.0.1:18787"},
			{"https_proxy": "http://127.0.0.1:18787"},
			{"HTTPS_PROXY": "127.0.0.1:18788"},
			{"HTTP_PROXY": "http://localhost:18790"},
		} {
			if proxy, ok := ProxyFor(https, env); ok {
				t.Errorf("ProxyFor(%v) = %v, want none", env, proxy)
			}
			if proxy, ok := ProxyFor(plain, env); ok {
				t.Errorf("ProxyFor(%v) = %v, want none", env, proxy)
			}
		}
		if proxy, ok := ProxyFor(https, Env{"HTTPS_PROXY": "http://proxy.corp:3128"}); !ok || proxy.String() != "http://proxy.corp:3128" {
			t.Errorf("ProxyFor(corporate proxy) = %v, %v, want http://proxy.corp:3128", proxy, ok)
		}
	})
}
