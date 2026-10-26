package routerd

// The 2026-10-21 incident: a router tunnelled its claude traffic through another router and
// answered 502s for hours. These scenarios hold the rule that came out of it: a router never
// routes through another router, whatever port the other one serves.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// fakeRouter is one fake router of another generation: it answers the health path a router
// probe asks and records every request it is given.
type fakeRouter struct {
	server *httptest.Server
	mu     sync.Mutex
	method []string
	paths  []string
}

func newFakeRouter(t *testing.T) *fakeRouter {
	fake := &fakeRouter{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.method = append(fake.method, r.Method)
		fake.paths = append(fake.paths, r.URL.Path)
		fake.mu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == router.MultiHealthPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"name":"huddle-router"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// tunnelled says whether the fake router saw a request that is not a health probe.
func (f *fakeRouter) tunnelled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, path := range f.paths {
		if f.method[i] == http.MethodConnect || path != router.MultiHealthPath {
			return true
		}
	}
	return false
}

func TestNoChainScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "the 2026-10-21 incident cannot recur: a router never routes through another router", func(f *bdd.F) {
		f.Scenario("a router on a non-default port is skipped", func(s *bdd.S) {
			var rig *daemonRig
			var other *fakeRouter
			s.Given("another router serves on a free port and the daemon's proxy names it", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				other = newFakeRouter(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.env["HTTPS_PROXY"], rig.env["HTTP_PROXY"] = other.server.URL, other.server.URL
				rig.start()
			})
			s.When("a claude request arrives", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
				if status, body := rig.postModel(t, "claude-sonnet-4-5"); status != 200 {
					t.Fatalf("claude-sonnet-4-5 = %d %q; log:\n%s", status, body, rig.logs.joined())
				}
			})
			s.Then("the request went to Anthropic, never through the other router", func(t *testing.T) {
				if len(rig.anthropic.requests()) != 1 {
					t.Fatalf("Anthropic saw %d requests, want 1", len(rig.anthropic.requests()))
				}
				if other.tunnelled() {
					t.Error("the other router carried the claude request")
				}
			})
			s.And("the log says the proxy is a router and the daemon went direct", func(t *testing.T) {
				if !strings.Contains(rig.logs.joined(), "is a router; going direct") {
					t.Errorf("log =\n%s\nwant the going-direct line", rig.logs.joined())
				}
			})
		})

		f.Scenario("the router's own port is skipped", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("the daemon's proxy names the daemon's own port", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				own := "http://127.0.0.1:" + strconv.Itoa(rig.port)
				rig.env["HTTPS_PROXY"], rig.env["HTTP_PROXY"] = own, own
				rig.start()
			})
			s.When("a claude request arrives", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
				if status, body := rig.postModel(t, "claude-sonnet-4-5"); status != 200 {
					t.Fatalf("claude-sonnet-4-5 = %d %q; log:\n%s", status, body, rig.logs.joined())
				}
			})
			s.Then("the daemon answered from Anthropic and never looped into itself", func(t *testing.T) {
				if len(rig.anthropic.requests()) != 1 {
					t.Fatalf("Anthropic saw %d requests, want 1", len(rig.anthropic.requests()))
				}
				if !strings.Contains(rig.logs.joined(), "is a router; going direct") {
					t.Errorf("log =\n%s\nwant the going-direct line", rig.logs.joined())
				}
			})
		})

		f.Scenario("a corporate proxy is kept", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("the daemon's proxy names a machine far away that is no router", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				corp := "http://proxy.invalid:3128"
				rig.env["HTTPS_PROXY"], rig.env["HTTP_PROXY"] = corp, corp
				rig.start()
			})
			s.When("a claude request arrives", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
				if status, body := rig.postModel(t, "claude-sonnet-4-5"); status != 502 {
					t.Fatalf("claude-sonnet-4-5 through the corporate proxy = %d %q, want the 502 the dead proxy earns", status, body)
				}
			})
			s.Then("the proxy is kept: the log never says going direct", func(t *testing.T) {
				if strings.Contains(rig.logs.joined(), "going direct") {
					t.Errorf("log =\n%s\nthe corporate proxy was dropped", rig.logs.joined())
				}
			})
		})
	})
}
