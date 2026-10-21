package routerd

// The 2026-10-21 incident as scenarios: one router generation serves the old 0.0.1 ports, so a
// session started before a migration keeps working and the old generation has nothing left to
// respawn.

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// awaitLegacyHealth waits until the port answers huddle-router's health, and returns the body.
func awaitLegacyHealth(t *testing.T, port int, log func() string) string {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, router.MultiHealthPath))
		if err == nil {
			body := readAll(t, resp)
			if resp.StatusCode == 200 && strings.Contains(body, `"huddle-router"`) {
				return body
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port %d never answered huddle-router's health; the daemon's log:\n%s", port, log())
	return ""
}

// readAll is one response's body, closed on the way out.
func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw := make([]byte, 0, 512)
	buffer := make([]byte, 512)
	for {
		n, err := resp.Body.Read(buffer)
		raw = append(raw, buffer[:n]...)
		if err != nil {
			break
		}
	}
	return string(raw)
}

// postTo posts one model request to a port the way a session pointed there does, and answers
// the status and body.
func postTo(t *testing.T, port int, model string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port),
		strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, model)))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer anthropic-oauth")
	request.Header.Set("X-Api-Key", "anthropic-key")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(request)
	if err != nil {
		t.Fatalf("post %s to %d: %v", model, port, err)
	}
	return resp.StatusCode, readAll(t, resp)
}

// awaitLog waits until the daemon's log names the line.
func awaitLog(t *testing.T, rig *daemonRig, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(rig.logs.joined(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the log never said %q; it says:\n%s", want, rig.logs.joined())
}

func TestLegacyPortScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "the 2026-10-21 incident cannot recur: one router generation serves the old ports", func(f *bdd.F) {
		f.Scenario("a free legacy port is adopted and answers as huddle-router", func(s *bdd.S) {
			var rig *daemonRig
			var legacy int
			s.Given("huddle-router with zai's legacy port free and zai enabled", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				legacy = daemonFreePort(t)
				rig.env["HUDDLE_ROUTER_LEGACY_PORTS"] = "zai=" + strconv.Itoa(legacy)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
			})
			s.Then("the legacy port answers huddle-router's health", func(t *testing.T) {
				awaitLegacyHealth(t, legacy, rig.logs.joined)
			})
			s.And("the router's own port still answers", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
		})

		f.Scenario("a 0.0.1 router holding a legacy port is replaced", func(s *bdd.S) {
			var rig *daemonRig
			var legacy int
			var fake fakeLegacyProcess
			s.Given("a 0.0.1 zai router holding a legacy port, its pid record beside it", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				legacy = daemonFreePort(t)
				rig.env["HUDDLE_ROUTER_LEGACY_PORTS"] = "zai=" + strconv.Itoa(legacy)
				state := filepath.Join(rig.env["HOME"], ".agents", "zai")
				rig.env["ZAI_STATE_DIR"] = state
				// the fake lives past this step, so its cleanup rides the scenario
				fake = startFakeLegacy(s.T, buildFakeLegacy(s.T), "zai", "run", legacy)
				awaitListening(t, legacy)
				writeLegacyRecord(t, state, "router.pid", fake.pid, legacy)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
			})
			s.When("huddle-router serves", func(t *testing.T) {
				rig.start()
			})
			s.Then("the 0.0.1 router is gone", func(t *testing.T) {
				awaitExit(t, fake.done, "the 0.0.1 router")
			})
			s.And("the legacy port answers huddle-router's health", func(t *testing.T) {
				awaitLegacyHealth(t, legacy, rig.logs.joined)
			})
			s.And("a session pointed at the legacy port still gets its answers", func(t *testing.T) {
				status, body := postTo(t, legacy, "glm-5.3")
				if status != 200 {
					t.Fatalf("glm-5.3 on the legacy port = %d %q", status, body)
				}
				if seen := rig.zai.requests(); len(seen) != 1 {
					t.Errorf("the zai upstream saw %d requests, want 1", len(seen))
				}
			})
			s.And("the log names the stop", func(t *testing.T) {
				awaitLog(t, rig, fmt.Sprintf("huddle-router: stopped the 0.0.1 zai router (pid %d) and took port %d", fake.pid, legacy))
			})
		})

		f.Scenario("a foreign holder of a legacy port is left alone", func(s *bdd.S) {
			var rig *daemonRig
			var legacy int
			var held net.Listener
			s.Given("something that is not ours holding a legacy port", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				legacy = daemonFreePort(t)
				var err error
				held, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(legacy))
				if err != nil {
					t.Fatal(err)
				}
				// the holder lives past this step, so its close rides the scenario
				s.T.Cleanup(func() { _ = held.Close() })
				rig.env["HUDDLE_ROUTER_LEGACY_PORTS"] = "zai=" + strconv.Itoa(legacy)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
			})
			s.Then("the router says it leaves the port", func(t *testing.T) {
				awaitLog(t, rig, fmt.Sprintf("huddle-router: port %d is held by pid-unknown; leaving it", legacy))
			})
			s.And("the holder keeps the port", func(t *testing.T) {
				if !portAccepts(legacy) {
					t.Error("the holder's port stopped answering")
				}
			})
			s.And("the router's own port still answers", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
		})
	})
}
