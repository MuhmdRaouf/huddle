// The huddle-router's behaviour: one router serves every provider at once, picking the lane from
// the request's model, with one Anthropic lane for everything no provider claims, a health check
// of its own and the HTTPS proxy mode. Written as scenarios over the rig in multi_test.go.
package router

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestMultiRouterScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "huddle-router serves every provider", func(f *bdd.F) {
		f.Scenario("a zai model reaches the zai upstream with zai's key and nothing else hears of it", func(s *bdd.S) {
			var rig *multiRig
			s.Given("zai and kimi lanes with their own keys", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{})
			})
			var got answer
			s.When("a request for glm-5.3-flash arrives", func(t *testing.T) {
				got = rig.postModel(t, "/v1/messages", "glm-5.3-flash")
			})
			s.Then("only the zai upstream answers, with zai's key", func(t *testing.T) {
				if got.status != 200 || got.body != "data: zai-1\n\ndata: zai-2\n\n" {
					t.Fatalf("answer = %d %q", got.status, got.body)
				}
				seen := rig.laneOf("zai").seen.list()
				if len(seen) != 1 {
					t.Fatalf("zai saw %d requests", len(seen))
				}
				if header := seen[0].headers.Get("Authorization"); header != "Bearer zai-secret" {
					t.Errorf("authorization = %q", header)
				}
			})
			s.Then("neither kimi nor Anthropic saw it", func(t *testing.T) {
				if rig.laneOf("kimi").seen.len() != 0 {
					t.Error("kimi saw the request")
				}
				if rig.anthropicSeen.len() != 0 {
					t.Error("Anthropic saw the request")
				}
			})
		})

		f.Scenario("a kimi model reaches the kimi upstream with kimi's key, by id and by prefix", func(s *bdd.S) {
			var rig *multiRig
			s.Given("zai and kimi lanes with their own keys", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{})
			})
			s.When("requests for kimi-flash and kimi-anything arrive", func(t *testing.T) {
				for _, model := range []string{"kimi-flash", "kimi-anything"} {
					if got := rig.postModel(t, "/v1/messages", model); got.status != 200 {
						t.Fatalf("%s = %d %q", model, got.status, got.body)
					}
				}
			})
			s.Then("both reached kimi with kimi's key and zai saw none of them", func(t *testing.T) {
				seen := rig.laneOf("kimi").seen.list()
				if len(seen) != 2 {
					t.Fatalf("kimi saw %d requests", len(seen))
				}
				for i, want := range []string{"kimi-flash", "kimi-anything"} {
					if !strings.Contains(seen[i].body, want) {
						t.Errorf("request %d body = %q, want it for %s", i, seen[i].body, want)
					}
					if header := seen[i].headers.Get("Authorization"); header != "Bearer kimi-secret" {
						t.Errorf("request %d authorization = %q", i, header)
					}
				}
				if rig.laneOf("zai").seen.len() != 0 {
					t.Error("zai saw a kimi request")
				}
			})
		})

		f.Scenario("a claude model reaches Anthropic with the caller's headers and reads no key", func(s *bdd.S) {
			var rig *multiRig
			var got answer
			s.Given("zai and kimi lanes with their own keys", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{})
			})
			s.When("a request for claude-opus-5-5 arrives", func(t *testing.T) {
				got = rig.postModel(t, "/v1/messages", "claude-opus-5-5")
			})
			s.Then("Anthropic answers it with the caller's headers untouched", func(t *testing.T) {
				if got.status != 200 || got.body != "data: anthropic-1\n\ndata: anthropic-2\n\n" {
					t.Fatalf("answer = %d %q", got.status, got.body)
				}
				seen := rig.anthropicSeen.list()
				if len(seen) != 1 {
					t.Fatalf("Anthropic saw %d requests", len(seen))
				}
				for name, want := range map[string]string{
					"Authorization":  "Bearer anthropic-oauth",
					"X-Api-Key":      "anthropic-key",
					"Anthropic-Beta": "claude-code-20250219",
				} {
					if gotHeader := seen[0].headers.Get(name); gotHeader != want {
						t.Errorf("%s = %q, want %q", name, gotHeader, want)
					}
				}
			})
			s.Then("no lane's key func was called", func(t *testing.T) {
				for _, lane := range rig.lanes {
					if calls := lane.keyCalls.Load(); calls != 0 {
						t.Errorf("%s's key func ran %d times", lane.provider.Name, calls)
					}
				}
			})
		})

		f.Scenario("an unknown model and a body without a model reach Anthropic", func(s *bdd.S) {
			var rig *multiRig
			s.Given("zai and kimi lanes with their own keys", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{})
			})
			s.When("a request for a model nobody claims arrives", func(t *testing.T) {
				if got := rig.postModel(t, "/v1/messages", "some-unknown-model"); got.status != 200 {
					t.Fatalf("unknown model = %d %q", got.status, got.body)
				}
			})
			s.When("a request with no model at all arrives", func(t *testing.T) {
				if got := rig.postModel(t, "/v1/messages", ""); got.status != 200 {
					t.Fatalf("no model = %d %q", got.status, got.body)
				}
			})
			s.Then("both reached Anthropic and neither provider saw one", func(t *testing.T) {
				if rig.anthropicSeen.len() != 2 {
					t.Fatalf("Anthropic saw %d requests, want 2", rig.anthropicSeen.len())
				}
				if rig.laneOf("zai").seen.len() != 0 || rig.laneOf("kimi").seen.len() != 0 {
					t.Error("a provider saw an unclaimed request")
				}
			})
		})

		f.Scenario("a count_tokens request reaches its provider like any other", func(s *bdd.S) {
			var rig *multiRig
			s.Given("zai and kimi lanes with their own keys", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{})
			})
			s.When("/v1/messages/count_tokens is posted for glm-5.3-flash", func(t *testing.T) {
				if got := rig.postModel(t, "/v1/messages/count_tokens", "glm-5.3-flash"); got.status != 200 {
					t.Fatalf("count_tokens = %d %q", got.status, got.body)
				}
			})
			s.Then("zai answered it", func(t *testing.T) {
				seen := rig.laneOf("zai").seen.list()
				if len(seen) != 1 || seen[0].path != "/api/anthropic/v1/messages/count_tokens" {
					t.Fatalf("zai saw %+v", seen)
				}
			})
		})

		f.Scenario("the health check names the router and the providers it serves, empty when there are none", func(s *bdd.S) {
			var rig *multiRig
			var health answer
			s.Given("zai and kimi lanes", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{})
			})
			s.When("GET /huddle-router/health is read", func(t *testing.T) {
				got, err := getBody(rig.port, MultiHealthPath)
				if err != nil {
					t.Fatal(err)
				}
				health = got
			})
			s.Then(`it answers {"ok":true,"name":"huddle-router","providers":["zai","kimi"]}`, func(t *testing.T) {
				if health.status != 200 {
					t.Fatalf("status = %d", health.status)
				}
				if health.body != `{"ok":true,"name":"huddle-router","providers":["zai","kimi"]}` {
					t.Errorf("health = %s", health.body)
				}
			})
			s.Given("a huddle-router with no lanes", func(t *testing.T) {
				anthropic := recordingUpstream(s.T, "anthropic", "", &seenList{})
				rig = &multiRig{t: t, anthropicSeen: &seenList{}, events: &eventLog{}, anthropic: anthropic}
				rig.port = reservePort(s.T)
				server, _ := CreateMulti(MultiOptions{Port: rig.port, Anthropic: anthropic})
				serveRouter(s.T, server, rig.port)
			})
			s.When("GET /huddle-router/health is read", func(t *testing.T) {
				got, err := getBody(rig.port, MultiHealthPath)
				if err != nil {
					t.Fatal(err)
				}
				health = got
			})
			s.Then("providers is []", func(t *testing.T) {
				if health.body != `{"ok":true,"name":"huddle-router","providers":[]}` {
					t.Errorf("health = %s", health.body)
				}
			})
		})

		f.Scenario("every spool event carries the plugin of the lane that served the request", func(s *bdd.S) {
			var rig *multiRig
			s.Given("zai and kimi lanes with their own keys", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{})
			})
			s.When("a glm, a kimi and a claude request are served", func(t *testing.T) {
				for _, model := range []string{"glm-5.3-flash", "kimi-flash", "claude-opus-5-5"} {
					if got := rig.postModel(t, "/v1/messages", model); got.status != 200 {
						t.Fatalf("%s = %d %q", model, got.status, got.body)
					}
				}
			})
			s.Then("the route events name zai, kimi and huddle-router, in order", func(t *testing.T) {
				if !until(func() bool { return len(rig.events.routes()) >= 3 }) {
					t.Fatalf("route events = %+v", rig.events.routes())
				}
				var got []string
				for _, route := range rig.events.routes() {
					got = append(got, route.Plugin)
				}
				if !slices.Equal(got, []string{"zai", "kimi", "huddle-router"}) {
					t.Errorf("plugins = %v", got)
				}
			})
		})

		f.Scenario("a stopped budget on one lane stops only that lane's models", func(s *bdd.S) {
			month := domain.BudgetMonth
			var rig *multiRig
			var got answer
			s.Given("a zai lane whose budget is stopped, beside a kimi lane", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{lanes: []multiLaneOpts{
					{provider: routerProvider, key: "zai-secret", budget: func() (domain.BudgetStop, bool) {
						return domain.BudgetStop{Scope: "provider:zai", Period: &month}, true
					}},
					{provider: kimiProvider(), key: "kimi-secret"},
				}})
			})
			s.When("a request for glm-5.3-flash arrives", func(t *testing.T) {
				got = rig.postModel(t, "/v1/messages", "glm-5.3-flash")
			})
			s.Then("it is answered with the budget refusal and zai saw nothing", func(t *testing.T) {
				if got.status != 400 {
					t.Fatalf("status = %d, want 400", got.status)
				}
				if retry := got.header.Get("X-Should-Retry"); retry != "false" {
					t.Errorf("x-should-retry = %q", retry)
				}
				if !strings.Contains(got.body, "budget reached") {
					t.Errorf("body = %s", got.body)
				}
				if rig.laneOf("zai").seen.len() != 0 {
					t.Error("the provider saw a stopped request")
				}
			})
			s.And("kimi and claude requests still pass", func(t *testing.T) {
				if got := rig.postModel(t, "/v1/messages", "kimi-flash"); got.status != 200 {
					t.Errorf("kimi = %d %q", got.status, got.body)
				}
				if got := rig.postModel(t, "/v1/messages", "claude-opus-5-5"); got.status != 200 {
					t.Errorf("claude = %d %q", got.status, got.body)
				}
			})
		})

		f.Scenario("an exact catalog id beats another lane's longer prefix", func(s *bdd.S) {
			var rig *multiRig
			s.Given("a lane whose catalog id is kimi-special, beside the kimi lane with the kimi- prefix", func(t *testing.T) {
				special := kimiProvider()
				special.Name = "special"
				special.Display = "Special"
				special.Router = domain.ProviderRouter{ModelPrefixes: nil}
				special.Catalog = map[domain.ModelTier]domain.ModelRef{
					domain.ModelTierMain:  {Tier: domain.ModelTierMain, ID: "kimi-special", Label: "Special"},
					domain.ModelTierFlash: {Tier: domain.ModelTierFlash, ID: "kimi-special-flash", Label: "Special Flash"},
				}
				rig = newMultiRig(s.T, multiOpts{lanes: []multiLaneOpts{
					{provider: special, key: "special-secret"},
					{provider: kimiProvider(), key: "kimi-secret"},
				}})
			})
			s.When("kimi-special and kimi-flash are requested", func(t *testing.T) {
				for _, model := range []string{"kimi-special", "kimi-flash"} {
					if got := rig.postModel(t, "/v1/messages", model); got.status != 200 {
						t.Fatalf("%s = %d %q", model, got.status, got.body)
					}
				}
			})
			s.Then("the special lane took kimi-special and the kimi lane kept kimi-flash", func(t *testing.T) {
				if seen := rig.laneOf("special").seen.list(); len(seen) != 1 || !strings.Contains(seen[0].body, "kimi-special") {
					t.Fatalf("special saw %+v", seen)
				}
				if seen := rig.laneOf("kimi").seen.list(); len(seen) != 1 || !strings.Contains(seen[0].body, "kimi-flash") {
					t.Fatalf("kimi saw %+v", seen)
				}
			})
		})

		f.Scenario("a request for another host is refused when the router serves a port", func(s *bdd.S) {
			var rig *multiRig
			var status int
			s.Given("a huddle-router answering one port's Host only", func(t *testing.T) {
				rig = newMultiRig(s.T, multiOpts{portSet: true})
			})
			s.When("a POST with Host evil.example arrives", func(t *testing.T) {
				request, err := http.NewRequest(http.MethodPost,
					fmt.Sprintf("http://127.0.0.1:%d/v1/messages", rig.port),
					strings.NewReader(`{"model":"glm-5.3"}`))
				if err != nil {
					t.Fatal(err)
				}
				request.Host = "evil.example"
				request.Header.Set("Content-Type", "application/json")
				client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
				defer client.CloseIdleConnections()
				response, err := client.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				_, _ = io.Copy(io.Discard, response.Body)
				status = response.StatusCode
			})
			s.Then("it is refused with 421 and nothing went upstream", func(t *testing.T) {
				if status != 421 {
					t.Fatalf("status = %d, want 421", status)
				}
				if rig.laneOf("zai").seen.len() != 0 || rig.anthropicSeen.len() != 0 {
					t.Error("a refused request reached an upstream")
				}
			})
		})

		f.Scenario("CONNECT decrypts api.anthropic.com and routes it lane by lane, and refuses a tunnel to this machine", func(s *bdd.S) {
			var (
				port          int
				roots         *x509.CertPool
				zaiSeen       = &seenList{}
				kimiSeen      = &seenList{}
				anthropicSeen = &seenList{}
				dials         []string
				client        *http.Client
			)
			s.Given("a huddle-router with a CA of its own, zai and kimi lanes, and a client that trusts that CA", func(t *testing.T) {
				caDir := s.T.TempDir()
				ca, err := pki.LoadOrCreateRouterCA(context.Background(), caDir, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				roots = x509.NewCertPool()
				roots.AddCert(ca.Certificate())
				anthropic := recordingUpstream(s.T, "anthropic", "", anthropicSeen)
				zai := recordingUpstream(s.T, "zai", "/api/anthropic", zaiSeen)
				kimi := recordingUpstream(s.T, "kimi", "/api/anthropic", kimiSeen)
				port = reservePort(s.T)
				server, _ := CreateMulti(MultiOptions{
					Port:      port,
					Anthropic: anthropic,
					CADir:     caDir,
					Lanes: []RouterOptions{
						{Provider: routerProvider, ProviderURL: zai, Key: func(context.Context) (string, error) { return "zai-secret", nil }},
						{Provider: kimiProvider(), ProviderURL: kimi, Key: func(context.Context) (string, error) { return "kimi-secret", nil }},
					},
					// Decryption serves from the router itself and a refused CONNECT dials
					// nothing, so any dial here is a bug the scenario counts.
					Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
						dials = append(dials, addr)
						return nil, fmt.Errorf("no fake target for %s", addr)
					},
				})
				serveRouter(s.T, server, port)
				client = proxyClientAt(port, roots)
			})
			s.When("a glm-5.3-flash request arrives through the proxy", func(t *testing.T) {
				got, err := postViaProxy(client, "https://api.anthropic.com/v1/messages?beta=true",
					`{"model":"glm-5.3-flash","messages":[]}`, map[string]string{"X-Api-Key": "anthropic-key"})
				if err != nil {
					t.Fatal(err)
				}
				if got.status != 200 || got.body != "data: zai-1\n\ndata: zai-2\n\n" {
					t.Fatalf("answer = %d %q", got.status, got.body)
				}
			})
			s.Then("zai answered it with zai's key, decrypted and never dialled", func(t *testing.T) {
				seen := zaiSeen.list()
				if len(seen) != 1 {
					t.Fatalf("zai saw %d requests", len(seen))
				}
				if header := seen[0].headers.Get("Authorization"); header != "Bearer zai-secret" {
					t.Errorf("authorization = %q", header)
				}
				if len(dials) != 0 {
					t.Errorf("the router dialled %v", dials)
				}
			})
			s.When("a claude-opus-5-5 request arrives through the proxy", func(t *testing.T) {
				got, err := postViaProxy(client, "https://api.anthropic.com/v1/messages?beta=true",
					`{"model":"claude-opus-5-5","messages":[]}`, nil)
				if err != nil {
					t.Fatal(err)
				}
				if got.status != 200 || got.body != "data: anthropic-1\n\ndata: anthropic-2\n\n" {
					t.Fatalf("answer = %d %q", got.status, got.body)
				}
			})
			s.Then("Anthropic answered it and zai saw only the first request", func(t *testing.T) {
				if anthropicSeen.len() != 1 {
					t.Fatalf("Anthropic saw %d requests", anthropicSeen.len())
				}
				if zaiSeen.len() != 1 {
					t.Errorf("zai saw %d requests, want 1", zaiSeen.len())
				}
			})
			s.When("a CONNECT to 127.0.0.1:1 arrives", func(t *testing.T) {
				conn, got := connectThrough(t, port, "127.0.0.1:1")
				defer conn.Close()
				if got.StatusCode != http.StatusForbidden {
					t.Fatalf("CONNECT = %d, want 403", got.StatusCode)
				}
				if body := refuseBody(t, got); !strings.Contains(body, `"type":"error"`) {
					t.Fatalf("body = %q, want the Anthropic error shape", body)
				}
			})
			s.Then("still nothing was dialled", func(t *testing.T) {
				if len(dials) != 0 {
					t.Errorf("the router dialled %v", dials)
				}
			})
		})
	})
}
