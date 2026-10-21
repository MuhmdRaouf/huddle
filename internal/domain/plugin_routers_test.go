package domain

import "testing"

func TestPluginRouters(t *testing.T) {
	want := map[int64]string{
		18786: "huddle-router",
		18787: "zai",
		18788: "kimi",
		18789: "deepseek",
		18790: "minimax",
		18791: "qwen",
	}
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
		{"http://localhost:18787/", 18787},
		{"http://127.0.0.1:18791/x", 18791},
		{"http://LOCALHOST:18788", 18788},
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
		{"//localhost:18787", -1},
		{"localhost:18787", -1},
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
		{"http://localhost:18787/", true},
		{"http://127.0.0.1:18786", true},
		{"http://127.0.0.1:18791/x", true},
		{"HTTP://LOCALHOST:18788", true},
		{"https://api.z.ai", false},
		{"http://localhost:8080", false},
		{"http://example.com:18787", false},
		{"http://[::1]:18787", false},
		{"http://localhost", false},
		{"//localhost:18787", false},
		{"localhost:18787", false},
		{"http://[::1", false},
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
		{"http://127.0.0.1:18787", true},
		{"http://127.0.0.1:18786", true},
		{"localhost:18786", true},
		{"http://localhost:18790", true},
		{"https://LOCALHOST:18788", true},
		{"127.0.0.1:18787", true},
		{"localhost:18791", true},
		{"http://proxy.corp:3128", false},
		{"proxy.corp:3128", false},
		{"http://localhost:8080", false},
		{"http://example.com:18787", false},
		{"127.0.0.1", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsPluginRouterProxy(tc.proxy); got != tc.want {
			t.Errorf("IsPluginRouterProxy(%q) = %v, want %v", tc.proxy, got, tc.want)
		}
	}
}

func TestPlanBaseUrl(t *testing.T) {
	const routerURL = "http://127.0.0.1:18787"
	kimi := "http://localhost:18788"

	t.Run("unset points it at this router", func(t *testing.T) {
		plan := PlanBaseUrl(nil, routerURL, nil)
		if plan.Kind != BaseUrlPlanSet || plan.URL != routerURL {
			t.Fatalf("plan = %+v, want set %q", plan, routerURL)
		}
		if want := "pointed ANTHROPIC_BASE_URL at " + routerURL; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("already this router keeps it silently", func(t *testing.T) {
		current := routerURL
		plan := PlanBaseUrl(&current, routerURL, nil)
		if plan.Kind != BaseUrlPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want keep with no report", plan)
		}
	})

	t.Run("a live plugin router keeps it silently", func(t *testing.T) {
		current := kimi
		plan := PlanBaseUrl(&current, routerURL, []string{kimi})
		if plan.Kind != BaseUrlPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want keep with no report", plan)
		}
	})

	t.Run("a dead plugin router moves it here", func(t *testing.T) {
		current := kimi
		plan := PlanBaseUrl(&current, routerURL, []string{"http://localhost:18790"})
		if plan.Kind != BaseUrlPlanSet || plan.URL != routerURL {
			t.Fatalf("plan = %+v, want set %q", plan, routerURL)
		}
		want := "moved ANTHROPIC_BASE_URL from the kimi plugin's router (" + kimi + ", not running) to " + routerURL
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("the user's own proxy is kept and reported", func(t *testing.T) {
		current := "https://proxy.example.com"
		plan := PlanBaseUrl(&current, routerURL, nil)
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
		plan := PlanBaseUrl(&current, routerURL, nil)
		if plan.Kind != BaseUrlPlanKeep {
			t.Fatalf("plan = %+v, want keep", plan)
		}
		if plan.Report == nil || *plan.Report != "kept ANTHROPIC_BASE_URL (http://localhost:8080): your own proxy" {
			t.Fatalf("report = %v, want the own-proxy note", plan.Report)
		}
	})
}

func TestPlanBaseUrlRemoval(t *testing.T) {
	const huddleURL = "http://127.0.0.1:18786"

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

	t.Run("a 0.0.1 router URL in the settings is ours to remove too", func(t *testing.T) {
		current := "http://localhost:18788"
		plan := PlanBaseUrlRemoval(&current, huddleURL, nil)
		if plan.Kind != BaseUrlPlanRemove || plan.Report == nil || *plan.Report != "removed ANTHROPIC_BASE_URL ("+current+")" {
			t.Fatalf("plan = %+v, want a remove", plan)
		}
	})
}

func TestPlanProxy(t *testing.T) {
	const (
		routerURL = "http://127.0.0.1:18787"
		caPath    = "/agents/zai/ca/ca.pem"
	)
	kimi := "http://localhost:18788"
	ownProxyNote := "kept your own proxy/CA; Remote Control needs ANTHROPIC_BASE_URL unset, so routing uses the base URL"

	t.Run("nothing set sets the three keys", func(t *testing.T) {
		plan := PlanProxy(ProxySettings{}, routerURL, caPath, nil)
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
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil || plan.NoProxy != "" {
			t.Fatalf("plan = %+v, want a silent keep with nothing to write", plan)
		}
	})

	t.Run("a bare this-router value keeps silently", func(t *testing.T) {
		proxy, ca := "127.0.0.1:18787", caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("a live other plugin router keeps silently", func(t *testing.T) {
		proxy, ca := kimi, caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath, []string{"http://127.0.0.1:18788"})
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("a dead other plugin router moves here", func(t *testing.T) {
		proxy, ca := kimi, caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath, []string{"http://localhost:18790"})
		if plan.Kind != ProxyPlanSet || plan.Proxy != routerURL || plan.CA != caPath {
			t.Fatalf("plan = %+v, want set with proxy %q", plan, routerURL)
		}
		want := "moved HTTPS_PROXY from the kimi plugin's router (" + kimi + ", not running) to " + routerURL
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("a 0.0.1 router URL in the settings is moved to huddle-router", func(t *testing.T) {
		proxy, ca := kimi, caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, "http://127.0.0.1:18786", caPath, nil)
		if plan.Kind != ProxyPlanSet || plan.Proxy != "http://127.0.0.1:18786" || plan.CA != caPath {
			t.Fatalf("plan = %+v, want a set at huddle-router", plan)
		}
		want := "moved HTTPS_PROXY from the kimi plugin's router (" + kimi + ", not running) to http://127.0.0.1:18786"
		if plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}
	})

	t.Run("the huddle-router proxy is ours and keeps silently", func(t *testing.T) {
		proxy, ca := "http://127.0.0.1:18786", caPath
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, "http://127.0.0.1:18786", caPath, nil)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("the user's own proxy falls back to base-URL mode", func(t *testing.T) {
		proxy := "http://proxy.corp:3128"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanBaseURL {
			t.Fatalf("plan = %+v, want the base-URL fallback", plan)
		}
		if plan.Report == nil || *plan.Report != ownProxyNote {
			t.Fatalf("report = %v, want %q", plan.Report, ownProxyNote)
		}
	})

	t.Run("this router on a port override is never the user's own proxy", func(t *testing.T) {
		overridden := "http://127.0.0.1:26244"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &overridden}, overridden, caPath, nil)
		if plan.Kind != ProxyPlanKeep {
			t.Fatalf("plan = %+v, want a keep of our own overridden router", plan)
		}
		plan = PlanProxy(ProxySettings{}, overridden, caPath, nil)
		if plan.Kind != ProxyPlanSet || plan.Proxy != overridden {
			t.Fatalf("plan = %+v, want a set on the overridden router", plan)
		}
		plan = PlanProxyRemoval(ProxySettings{HTTPSProxy: &overridden}, overridden, nil)
		if plan.Kind != ProxyPlanRemove {
			t.Fatalf("removal = %+v, want remove on the overridden router", plan)
		}
	})

	t.Run("a foreign CA falls back too, with no proxy of ours in the way", func(t *testing.T) {
		ca := "/elsewhere/ca.pem"
		plan := PlanProxy(ProxySettings{CA: &ca}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanBaseURL || plan.Report == nil || *plan.Report != ownProxyNote {
			t.Fatalf("plan = %+v, want the base-URL fallback with its note", plan)
		}
	})

	t.Run("a foreign CA next to this router falls back rather than clobbers it", func(t *testing.T) {
		proxy, ca := routerURL, "/elsewhere/ca.pem"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanBaseURL {
			t.Fatalf("plan = %+v, want the base-URL fallback", plan)
		}
	})

	t.Run("another plugin's CA rides along with its live router", func(t *testing.T) {
		proxy, ca := kimi, "/agents/kimi/ca/ca.pem"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca}, routerURL, caPath, []string{kimi})
		if plan.Kind != ProxyPlanKeep || plan.Report != nil {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}
	})

	t.Run("a stale plugin-router base URL is marked for removal", func(t *testing.T) {
		proxy, ca, baseURL := routerURL, caPath, kimi
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, BaseURL: &baseURL}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanKeep || !plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a keep that removes the base URL", plan)
		}
		plan = PlanProxy(ProxySettings{BaseURL: &baseURL}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanSet || !plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a set that removes the base URL", plan)
		}
	})

	t.Run("the user's NO_PROXY entries stay first, the missing loopback entries appended", func(t *testing.T) {
		proxy, ca, noProxy := routerURL, caPath, "example.com, other.org"
		plan := PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, NoProxy: &noProxy}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanKeep || plan.NoProxy != "example.com,other.org,localhost,127.0.0.1" {
			t.Fatalf("plan = %+v, want a keep writing the merged NO_PROXY", plan)
		}
		if want := "appended localhost and 127.0.0.1 to NO_PROXY"; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}

		noProxy = "localhost"
		plan = PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, NoProxy: &noProxy}, routerURL, caPath, nil)
		if plan.NoProxy != "localhost,127.0.0.1" {
			t.Fatalf("noProxy = %q, want 127.0.0.1 appended", plan.NoProxy)
		}
		if want := "appended 127.0.0.1 to NO_PROXY"; plan.Report == nil || *plan.Report != want {
			t.Fatalf("report = %v, want %q", plan.Report, want)
		}

		noProxy = "localhost,127.0.0.1"
		plan = PlanProxy(ProxySettings{HTTPSProxy: &proxy, CA: &ca, NoProxy: &noProxy}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanKeep || plan.Report != nil || plan.NoProxy != "" {
			t.Fatalf("plan = %+v, want a silent keep", plan)
		}

		noProxy = "example.com"
		plan = PlanProxy(ProxySettings{NoProxy: &noProxy}, routerURL, caPath, nil)
		if plan.Kind != ProxyPlanSet || plan.NoProxy != "example.com,localhost,127.0.0.1" {
			t.Fatalf("plan = %+v, want a set writing the merged NO_PROXY", plan)
		}
	})
}

func TestPlanProxyRemoval(t *testing.T) {
	const (
		huddleURL = "http://127.0.0.1:18786"
		caPath    = "/agents/zai/ca/ca.pem"
	)
	kimi := "http://localhost:18788"

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
		proxy, baseURL := huddleURL, kimi
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy, BaseURL: &baseURL}, huddleURL, []string{"kimi"})
		if plan.Kind != ProxyPlanKeep || plan.Report != nil || plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a silent keep that touches nothing", plan)
		}
	})

	t.Run("a 0.0.1 router URL in the settings is ours to remove too", func(t *testing.T) {
		proxy := kimi
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy}, huddleURL, nil)
		if plan.Kind != ProxyPlanRemove || plan.Report == nil || *plan.Report != "removed HTTPS_PROXY and NODE_EXTRA_CA_CERTS ("+proxy+")" {
			t.Fatalf("plan = %+v, want a remove", plan)
		}
	})

	t.Run("this router on a port override is ours to remove", func(t *testing.T) {
		overridden := "http://127.0.0.1:26244"
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &overridden}, overridden, nil)
		if plan.Kind != ProxyPlanRemove {
			t.Fatalf("removal = %+v, want remove on the overridden router", plan)
		}
	})

	t.Run("a stale plugin-router base URL goes with our removal", func(t *testing.T) {
		proxy, baseURL := huddleURL, kimi
		plan := PlanProxyRemoval(ProxySettings{HTTPSProxy: &proxy, BaseURL: &baseURL}, huddleURL, nil)
		if plan.Kind != ProxyPlanRemove || !plan.RemoveBaseURL {
			t.Fatalf("plan = %+v, want a remove that takes the base URL with it", plan)
		}
	})
}

func TestProxyRouterName(t *testing.T) {
	cases := []struct {
		proxy string
		want  string
	}{
		{"http://127.0.0.1:18786", "huddle-router"},
		{"http://127.0.0.1:18787", "zai"},
		{"localhost:18791", "qwen"},
		{"http://LOCALHOST:18788", "kimi"},
		{"http://proxy.corp:3128", ""},
		{"http://localhost:8080", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := ProxyRouterName(tc.proxy); got != tc.want {
			t.Errorf("ProxyRouterName(%q) = %q, want %q", tc.proxy, got, tc.want)
		}
	}
}

func TestProxyRouterIsLive(t *testing.T) {
	live := []string{"http://127.0.0.1:18788", "http://localhost:18791"}
	if !ProxyRouterIsLive(live, "kimi") || !ProxyRouterIsLive(live, "qwen") {
		t.Fatal("kimi and qwen count as live")
	}
	if ProxyRouterIsLive(live, "zai") || ProxyRouterIsLive(nil, "kimi") {
		t.Fatal("zai and an empty list count as not live")
	}
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
		{"http://127.0.0.1:18786", own, true},
		{"127.0.0.1:18787", own, true},
		{"http://localhost:18791", own, true},
		{"http://127.0.0.1:20999", own, true},
		{"127.0.0.1:20999", own, true},
		{"localhost:20999", own, true},
		{"[::1]:20999", own, true},
		{"http://[::1]:20999", own, true},
		{"http://127.0.0.1:3128", own, false},
		{"127.0.0.1:3128", 18786, false},
		{"http://127.0.0.1:3128", 18786, false},
		{"proxy.corp:18786", own, false},
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
