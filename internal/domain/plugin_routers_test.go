package domain

import (
	"fmt"
	"strconv"
	"testing"
)

// The router's canonical address, built from the constant so no test holds the literal port the
// owner's live router answers on. canonicalLoopback is the same address in its localhost
// spelling; overrideURL is the router moved off it by a `HUDDLE_ROUTER_PORT` override, on a port
// outside the reserved range.
var (
	canonicalURL      = fmt.Sprintf("http://127.0.0.1:%d", HuddleRouterPort)
	canonicalLoopback = fmt.Sprintf("http://localhost:%d", HuddleRouterPort)
	canonicalPort     = strconv.Itoa(HuddleRouterPort)
	overrideURL       = "http://127.0.0.1:26244"
)

func TestPluginRouters(t *testing.T) {
	want := map[int64]string{HuddleRouterPort: "huddle-router"}
	if len(PluginRouters) != len(want) {
		t.Fatalf("PluginRouters has %d entries, want %d", len(PluginRouters), len(want))
	}
	for port, plugin := range want {
		if PluginRouters[port] != plugin {
			t.Fatalf("PluginRouters[%d] = %q, want %q", port, PluginRouters[port], plugin)
		}
	}
}

func TestPortOf(t *testing.T) {
	cases := []struct {
		url  string
		want int64
	}{
		{"http://localhost:20787/", 20787},
		{"http://127.0.0.1:20791/x", 20791},
		{"http://LOCALHOST:20788", 20788},
		{"http://x:043/", 43},
		{"http://localhost:8080", 8080},
		{"https://localhost:8443", 8443},
		{"https://api.z.ai", -1},
		{"http://localhost/", -1},
		{"https://localhost:443/", -1},
		{"http://x:80/", -1},
		{"ws://x:80/", -1},
		{"http://x:99999999999999999999/", -1},
		{"http://[::1", -1},
		{"http://x:abc/", -1},
		{"//localhost:20787", -1},
		{"localhost:20787", -1},
		{"not a url", -1},
		{"", -1},
	}
	for _, tc := range cases {
		if got := PortOf(tc.url); got != tc.want {
			t.Errorf("PortOf(%q) = %d, want %d", tc.url, got, tc.want)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		hostname string
		want     bool
	}{
		{"localhost", true},
		{"::1", true},
		{"127.0.0.1", true},
		{"127.1", true},
		{"[::1]", false},
		{"::ffff:127.0.0.1", false},
		{"LocalHost", false},
		{"localhost.example", false},
		{"example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsLoopback(tc.hostname); got != tc.want {
			t.Errorf("IsLoopback(%q) = %v, want %v", tc.hostname, got, tc.want)
		}
	}
}

func TestIsPluginRouterURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://localhost:" + canonicalPort + "/", true},
		{canonicalURL, true},
		{"http://127.0.0.1:" + canonicalPort + "/x", true},
		{"HTTP://LOCALHOST:" + canonicalPort, true},
		{"https://api.z.ai", false},
		{"http://localhost:8080", false},
		{"http://example.com:20787", false},
		{"http://[::1]:20787", false},
		{"http://localhost", false},
		{"//localhost:20787", false},
		{"localhost:20787", false},
		{"http://[::1", false},
		// The override address is this router, but not the canonical one the map holds:
		// ProxyIsThisRouter is the helper that claims it.
		{overrideURL, false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsPluginRouterURL(tc.url); got != tc.want {
			t.Errorf("IsPluginRouterURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestIsPluginRouterProxy(t *testing.T) {
	cases := []struct {
		proxy string
		want  bool
	}{
		{canonicalURL, true},
		{"localhost:" + canonicalPort, true},
		{"127.0.0.1:" + canonicalPort, true},
		{"http://proxy.corp:3128", false},
		{"proxy.corp:3128", false},
		{"http://localhost:8080", false},
		{"http://localhost:20787", false},
		{"127.0.0.1:20787", false},
		{"localhost:20791", false},
		{"http://example.com:20787", false},
		{"127.0.0.1", false},
		{overrideURL, false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsPluginRouterProxy(tc.proxy); got != tc.want {
			t.Errorf("IsPluginRouterProxy(%q) = %v, want %v", tc.proxy, got, tc.want)
		}
	}
}

func TestPlanBaseUrl(t *testing.T) {
	routerURL := canonicalURL

	t.Run("unset points it at this router", func(t *testing.T) {
		plan := PlanBaseUrl(nil, routerURL)
		if plan.Kind != BaseUrlPlanSet || plan.URL != routerURL {
			t.Fatalf("plan = %+v, want set %q", plan, routerURL)
		}
		if want := "pointed ANTHROPIC_BASE_URL at " + routerURL; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("already this router keeps it silently", func(t *testing.T) {
		current := routerURL
		plan := PlanBaseUrl(&current, routerURL)
		if plan.Kind != BaseUrlPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want keep with no report", plan)
		}
	})

	t.Run("the canonical address moves when the router answers on its override", func(t *testing.T) {
		current := canonicalURL
		plan := PlanBaseUrl(&current, overrideURL)
		if plan.Kind != BaseUrlPlanSet || plan.URL != overrideURL {
			t.Fatalf("plan = %+v, want set %q", plan, overrideURL)
		}
		want := "moved ANTHROPIC_BASE_URL from " + canonicalURL + " to " + overrideURL
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("the user's own proxy is kept and reported", func(t *testing.T) {
		current := "https://proxy.example.com"
		plan := PlanBaseUrl(&current, routerURL)
		if plan.Kind != BaseUrlPlanKeep {
			t.Fatalf("plan = %+v, want keep", plan)
		}
		want := "kept ANTHROPIC_BASE_URL (https://proxy.example.com): your own proxy"
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("a loopback server on an unknown port is the user's own too", func(t *testing.T) {
		current := "http://localhost:8080"
		plan := PlanBaseUrl(&current, routerURL)
		if plan.Kind != BaseUrlPlanKeep {
			t.Fatalf("plan = %+v, want keep", plan)
		}
		if plan.Report == nil || *plan.Report != "kept ANTHROPIC_BASE_URL (http://localhost:8080): your own proxy" {
			t.Fatalf("report = %v, want the own-proxy note", plan.Report)
		}
	})

	t.Run("a base URL on a port no router of ours serves is the user's own", func(t *testing.T) {
		current := "http://localhost:20787"
		plan := PlanBaseUrl(&current, routerURL)
		if plan.Kind != BaseUrlPlanKeep {
			t.Fatalf("plan = %+v, want keep", plan)
		}
		want := "kept ANTHROPIC_BASE_URL (http://localhost:20787): your own proxy"
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})
}

func TestPlanBaseUrlRemoval(t *testing.T) {
	huddleURL := canonicalURL

	t.Run("nothing set keeps it silently", func(t *testing.T) {
		plan := PlanBaseUrlRemoval(nil, huddleURL, nil)
		if plan.Kind != BaseUrlPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want keep with no report", plan)
		}
	})

	t.Run("another URL is not ours to remove", func(t *testing.T) {
		current := "https://proxy.example.com"
		plan := PlanBaseUrlRemoval(&current, huddleURL, nil)
		if plan.Kind != BaseUrlPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want keep with no report", plan)
		}
	})

	t.Run("ours with no other provider enabled removes it", func(t *testing.T) {
		current := huddleURL
		plan := PlanBaseUrlRemoval(&current, huddleURL, nil)
		if plan.Kind != BaseUrlPlanRemove {
			t.Fatalf("plan = %+v, want remove", plan)
		}
		if want := "removed ANTHROPIC_BASE_URL (" + huddleURL + ")"; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("ours while another provider is enabled keeps it silently: the router is shared", func(t *testing.T) {
		current := huddleURL
		plan := PlanBaseUrlRemoval(&current, huddleURL, []string{"kimi"})
		if plan.Kind != BaseUrlPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want keep with no report", plan)
		}
	})

	t.Run("the canonical address in another spelling is ours to remove too", func(t *testing.T) {
		current := canonicalLoopback
		plan := PlanBaseUrlRemoval(&current, huddleURL, nil)
		if plan.Kind != BaseUrlPlanRemove || plan.Report == nil || *plan.Report != "removed ANTHROPIC_BASE_URL ("+current+")" {
			t.Fatalf("plan = %+v, want a remove", plan)
		}
	})

	t.Run("a base URL on a port no router of ours serves is not ours to remove", func(t *testing.T) {
		current := "http://localhost:20788"
		plan := PlanBaseUrlRemoval(&current, huddleURL, nil)
		if plan.Kind != BaseUrlPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want keep with no report", plan)
		}
	})
}

func TestPlanProxy(t *testing.T) {
	const caPath = "/agents/zai/ca/ca.pem"
	routerURL := canonicalURL
	ownProxyNote := "kept your own proxy/CA; Remote Control needs ANTHROPIC_BASE_URL unset, so routing uses the base URL"

	t.Run("nothing set sets the three keys", func(t *testing.T) {
		plan := PlanProxy(ProxySettings{}, routerURL, caPath)
		if plan.Kind != ProxyPlanSet || plan.Proxy != routerURL || plan.CA != caPath {
			t.Fatalf("plan = %+v, want set with proxy %q and CA %q", plan, routerURL, caPath)
		}
		if plan.NoProxy != "localhost,127.0.0.1" {
			t.Fatalf("noProxy = %q, want the loopback entries", plan.NoProxy)
		}
		if want := "pointed HTTPS_PROXY at " + routerURL; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
		if plan.RemoveBaseURL {
			t.Fatal("removeBaseURL = true; want false")
		}
	})

	t.Run("this router with this CA keeps silently", func(t *testing.T) {
		proxy, ca := routerURL, caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil || plan.NoProxy != "" {
			t.Fatalf("plan = %+v, want a silent keep with nothing to write", plan)
		}
	})

	t.Run("a bare this-router value keeps silently", func(t *testing.T) {
		proxy, ca := "127.0.0.1:"+canonicalPort, caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("the canonical address moves when the router answers on its override", func(t *testing.T) {
		proxy, ca := canonicalURL, caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, overrideURL, caPath)
		if plan.Kind != ProxyPlanSet || plan.Proxy != overrideURL || plan.CA != caPath {
			t.Fatalf("plan = %+v, want a set at the override", plan)
		}
		want := "moved HTTPS_PROXY from " + canonicalURL + " to " + overrideURL
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("the huddle-router proxy is ours and keeps silently", func(t *testing.T) {
		proxy, ca := canonicalURL, caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("the user's own proxy falls back to base-URL mode", func(t *testing.T) {
		proxy := "http://proxy.corp:3128"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy}, routerURL, caPath)
		if plan.Kind != ProxyPlanBaseURL {
			t.Fatalf("plan = %+v, want the base-URL fallback", plan)
		}
		if plan.Report == nil || *plan.Report != ownProxyNote {
			t.Fatalf("report = %v, want %q", plan.Report, ownProxyNote)
		}
	})

	t.Run("this router on a port override is never the user's own proxy", func(t *testing.T) {
		plan := PlanProxy(ProxySettings{HTTPSProxy: &overrideURL}, overrideURL, caPath)
		if plan.Kind != ProxyPlanKeep {
			t.Fatalf("plan = %+v, want a keep of our own overridden router", plan)
		}
		plan = PlanProxy(ProxySettings{}, overrideURL, caPath)
		if plan.Kind != ProxyPlanSet || plan.Proxy != overrideURL {
			t.Fatalf("plan = %+v, want a set on the overridden router", plan)
		}
		plan = PlanProxyRemoval(ProxySettings{HTTPSProxy: &overrideURL}, overrideURL, nil)
		if plan.Kind != ProxyPlanRemove {
			t.Fatalf("removal = %+v, want remove on the overridden router", plan)
		}
	})

	t.Run("a foreign CA falls back too, with no proxy of ours in the way", func(t *testing.T) {
		ca := "/elsewhere/ca.pem"
		plan := PlanProxy(ProxySettings{CA: &ca}, routerURL, caPath)
		if plan.Kind != ProxyPlanBaseURL || plan.Report == nil || *plan.Report != ownProxyNote {
			t.Fatalf("plan = %+v, want the base-URL fallback with its note", plan)
		}
	})

	t.Run("a foreign CA next to this router falls back rather than clobbers it", func(t *testing.T) {
		proxy, ca := routerURL, "/elsewhere/ca.pem"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath)
		if plan.Kind != ProxyPlanBaseURL {
			t.Fatalf("plan = %+v, want the base-URL fallback", plan)
		}
	})

	t.Run("a stale base URL of ours is marked for removal", func(t *testing.T) {
		proxy, ca, baseURL := routerURL, caPath, canonicalLoopback
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, BaseURL: &baseURL}, routerURL, caPath)
		if plan.Kind != ProxyPlanKeep || !plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a keep that removes the base URL", plan)
		}
		plan = PlanProxy(ProxySettings{BaseURL: &baseURL}, routerURL, caPath)
		if plan.Kind != ProxyPlanSet || !plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a set that removes the base URL", plan)
		}
	})

	t.Run("the user's NO_PROXY entries stay first, the missing loopback entries appended", func(t *testing.T) {
		proxy, ca, noProxy := routerURL, caPath, "example.com, other.org"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, NoProxy: &noProxy}, routerURL, caPath)
		if plan.Kind != ProxyPlanKeep || plan.NoProxy != "example.com,other.org,localhost,127.0.0.1" {
			t.Fatalf("plan = %+v, want a keep writing the merged NO_PROXY", plan)
		}
		if want := "appended localhost and 127.0.0.1 to NO_PROXY"; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}

		noProxy = "localhost"
		plan = PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, NoProxy: &noProxy}, routerURL, caPath)
		if plan.NoProxy != "localhost,127.0.0.1" {
			t.Fatalf("noProxy = %q, want 127.0.0.1 appended", plan.NoProxy)
		}
		if want := "appended 127.0.0.1 to NO_PROXY"; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}

		noProxy = "localhost,127.0.0.1"
		plan = PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, NoProxy: &noProxy}, routerURL, caPath)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil || plan.NoProxy != "" {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}

		noProxy = "example.com"
		plan = PlanProxy(ProxySettings{NoProxy: &noProxy}, routerURL, caPath)
		if plan.Kind != ProxyPlanSet || plan.NoProxy != "example.com,localhost,127.0.0.1" {
			t.Fatalf("plan = %+v, want a set writing the merged NO_PROXY", plan)
		}
	})
}

func TestPlanProxyRemoval(t *testing.T) {
	const caPath = "/agents/zai/ca/ca.pem"
	huddleURL := canonicalURL

	t.Run("no proxy at all is kept silently", func(t *testing.T) {
		plan := PlanProxyRemoval(ProxySettings{}, huddleURL, nil)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("a proxy that is not a router of ours is not ours to remove", func(t *testing.T) {
		proxy := "http://proxy.corp:3128"
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy}, huddleURL, nil)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil || plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a silent keep that touches nothing", plan)
		}
	})

	t.Run("ours with no other provider enabled removes the three keys", func(t *testing.T) {
		proxy, ca := huddleURL, caPath
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, huddleURL, nil)
		if plan.Kind != ProxyPlanRemove {
			t.Fatalf("plan = %+v, want remove", plan)
		}
		want := "removed HTTPS_PROXY and NODE_EXTRA_CA_CERTS (" + huddleURL + ")"
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("ours while another provider is enabled keeps silently: the proxy is shared", func(t *testing.T) {
		proxy, baseURL := huddleURL, canonicalLoopback
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy, BaseURL: &baseURL}, huddleURL, []string{"kimi"})
		if plan.Kind != ProxyPlanKeep || plan.Report != nil || plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a silent keep that touches nothing", plan)
		}
	})

	t.Run("a proxy on a port no router of ours serves is not ours to remove", func(t *testing.T) {
		proxy := "http://localhost:20788"
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy}, huddleURL, nil)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("this router on a port override is ours to remove", func(t *testing.T) {
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &overrideURL}, overrideURL, nil)
		if plan.Kind != ProxyPlanRemove {
			t.Fatalf("removal = %+v, want remove on the overridden router", plan)
		}
	})

	t.Run("a stale base URL of ours goes with the removal", func(t *testing.T) {
		proxy, baseURL := huddleURL, canonicalLoopback
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy, BaseURL: &baseURL}, huddleURL, nil)
		if plan.Kind != ProxyPlanRemove || !plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a remove that takes the base URL with it", plan)
		}
	})
}

func TestKeptPrefix(t *testing.T) {
	if KeptPrefix != "kept " {
		t.Fatalf("KeptPrefix = %q, want %q", KeptPrefix, "kept ")
	}
}

func TestIsRouterProxy(t *testing.T) {
	own := int64(20999)
	cases := []struct {
		raw     string
		ownPort int64
		want    bool
	}{
		{canonicalURL, own, true},
		{"localhost:" + canonicalPort, own, true},
		{"http://127.0.0.1:20999", own, true},
		{"127.0.0.1:20999", own, true},
		{"localhost:20999", own, true},
		{"[::1]:20999", own, true},
		{"http://[::1]:20999", own, true},
		{"http://127.0.0.1:3128", own, false},
		{"127.0.0.1:3128", HuddleRouterPort, false},
		{"http://127.0.0.1:3128", HuddleRouterPort, false},
		{"proxy.corp:" + canonicalPort, own, false},
		{"127.0.0.1:20787", own, false},
		{"http://localhost:20791", own, false},
		{"http://proxy.corp:20999", own, false},
		{"", own, false},
		{"not a url", own, false},
		{"http://[bad", own, false},
	}
	for _, tc := range cases {
		if got := IsRouterProxy(tc.raw, tc.ownPort); got != tc.want {
			t.Errorf("IsRouterProxy(%q, %d) = %v, want %v", tc.raw, tc.ownPort, got, tc.want)
		}
	}
}
