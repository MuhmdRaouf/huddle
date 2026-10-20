package routerd

// The gateway rides on the daemon: huddle-router mounts the gateway built from
// the huddle home's gateway.json under /gateway, a request there reaches the
// route's upstream with the prefix stripped, and a config that does not hold up
// costs one log line, never the router.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestHuddleRouterGatewayScenarios(t *testing.T) {
	bdd.Feature(t, "huddle-router serves the gateway", func(f *bdd.F) {
		f.Scenario("huddle-router serves the gateway's routes under /gateway", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("zai is enabled and the home's gateway.json routes a model to a fake upstream", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				rig.env["HUDDLE_HOME"] = filepath.Join(rig.home, "huddle-home")
				if err := os.MkdirAll(rig.env["HUDDLE_HOME"], 0o700); err != nil {
					t.Fatal(err)
				}
				gatewayJSON := filepath.Join(rig.env["HUDDLE_HOME"], "gateway.json")
				body := fmt.Sprintf(`{"routes":[{"match":"gw-*","upstream":%q}]}`, rig.anthropic.server.URL)
				if err := os.WriteFile(gatewayJSON, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
				rig.awaitHealth(t, "zai")
			})
			var status int
			var answer string
			s.When("a chat completions request arrives under /gateway for a routed model", func(t *testing.T) {
				client := &http.Client{Timeout: 10 * time.Second}
				resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/gateway/v1/chat/completions", rig.port),
					"application/json", strings.NewReader(`{"model":"gw-1","messages":[]}`))
				if err != nil {
					t.Fatalf("POST /gateway/v1/chat/completions: %v", err)
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read the answer: %v", err)
				}
				status, answer = resp.StatusCode, string(raw)
			})
			s.Then("the fake upstream answered through the mounted gateway", func(t *testing.T) {
				if status != 200 {
					t.Fatalf("the gateway request = %d %q (log: %s)", status, answer, rig.logs.joined())
				}
				seen := rig.anthropic.requests()
				if len(seen) != 1 {
					t.Fatalf("the upstream saw %d requests, want the one gateway request", len(seen))
				}
				if seen[0].path != "/v1/chat/completions" {
					t.Errorf("the upstream path = %q, want the /gateway prefix stripped", seen[0].path)
				}
				if !strings.Contains(seen[0].body, "gw-1") {
					t.Errorf("the body lost the model: %q", seen[0].body)
				}
			})
		})

		f.Scenario("a gateway.json that does not hold up costs one log line and the router runs on", func(s *bdd.S) {
			var rig *daemonRig
			s.Given("a home whose gateway.json is not JSON", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				rig.env["HUDDLE_HOME"] = filepath.Join(rig.home, "huddle-home")
				if err := os.MkdirAll(rig.env["HUDDLE_HOME"], 0o700); err != nil {
					t.Fatal(err)
				}
				gatewayJSON := filepath.Join(rig.env["HUDDLE_HOME"], "gateway.json")
				if err := os.WriteFile(gatewayJSON, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
			})
			s.Then("the router still serves its lanes", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
			s.And("the config error was logged once, naming the gateway", func(t *testing.T) {
				lines := strings.Split(rig.logs.joined(), "\n")
				said := 0
				for _, line := range lines {
					if strings.HasPrefix(line, "huddle-router: gateway: ") {
						said++
					}
				}
				if said != 1 {
					t.Errorf("the log named the gateway %d times (%s), want once", said, rig.logs.joined())
				}
			})
			s.And("the gateway paths answer 404", func(t *testing.T) {
				client := &http.Client{Timeout: time.Second}
				resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/gateway/v1/chat/completions", rig.port),
					"application/json", strings.NewReader(`{"model":"gw-1"}`))
				if err != nil {
					t.Fatalf("POST /gateway: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 404 {
					t.Errorf("the gateway path = %d, want 404 with no gateway mounted", resp.StatusCode)
				}
			})
		})
	})
}
