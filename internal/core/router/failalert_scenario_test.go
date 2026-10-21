// The 2026-10-21 incident: every claude-* request failed through huddle-router for six hours
// and nothing said why. Written as scenarios over the rig in multi_test.go: a failing Anthropic
// path raises one health event with the failure code and its fix hint, and a success after it
// raises the recovery.
package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestFailAlertScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "the 2026-10-21 incident cannot recur: a failing Anthropic path is visible within a minute", func(f *bdd.F) {
		f.Scenario("three certificate failures raise one event naming the code", func(s *bdd.S) {
			var rig *multiRig
			var lines *lineLog
			s.Given("a huddle-router whose Anthropic serves TLS nothing trusts", func(t *testing.T) {
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				s.T.Cleanup(upstream.Close)
				lines = &lineLog{}
				rig = newMultiRig(s.T, multiOpts{anthropic: mustURL(t, upstream.URL), log: lines.add})
			})
			s.When("three claude requests are each answered 502 with the code ECERT", func(t *testing.T) {
				for i := range 3 {
					got := rig.postModel(t, "/v1/messages", "claude-sonnet-4-5")
					if got.status != 502 || !strings.Contains(got.body, "ECERT") {
						t.Fatalf("request %d = %d %q, want a 502 naming ECERT", i+1, got.status, got.body)
					}
				}
			})
			s.Then("exactly one upstream_failing event names anthropic, the code and the model", func(t *testing.T) {
				failing := healthEvents(rig.events, domain.HealthUpstreamFailing)
				if len(failing) != 1 {
					t.Fatalf("upstream_failing events = %d (%+v), want 1", len(failing), failing)
				}
				got := failing[0]
				if got.Plugin != "anthropic" || got.Reason != "ECERT" {
					t.Errorf("event = plugin %q reason %q, want anthropic and ECERT", got.Plugin, got.Reason)
				}
				if got.Model == nil || *got.Model != "claude-sonnet-4-5" {
					t.Errorf("event model = %v, want claude-sonnet-4-5", got.Model)
				}
			})
			s.And("the router log carries the line with its fix hint", func(t *testing.T) {
				want := "huddle-router: Anthropic path failing (ECERT): a certificate check failed"
				if got := lines.joined(); !strings.Contains(got, want) {
					t.Errorf("log = %q, want %q in it", got, want)
				}
			})
		})

		f.Scenario("a success after a failing streak raises the recovery event", func(s *bdd.S) {
			var rig *multiRig
			var lines *lineLog
			var answering *atomic.Bool
			s.Given("a huddle-router whose Anthropic refuses every request for now", func(t *testing.T) {
				answering = &atomic.Bool{}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					if !answering.Load() {
						panic(http.ErrAbortHandler)
					}
					_, _ = io.Copy(io.Discard, req.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: anthropic-1\n\ndata: anthropic-2\n\n"))
				}))
				s.T.Cleanup(upstream.Close)
				lines = &lineLog{}
				rig = newMultiRig(s.T, multiOpts{anthropic: mustURL(t, upstream.URL), log: lines.add})
			})
			s.When("three claude requests fail and the fourth gets through", func(t *testing.T) {
				for i := range 3 {
					if got := rig.postModel(t, "/v1/messages", "claude-sonnet-4-5"); got.status == 200 {
						t.Fatalf("request %d = 200, want a failure", i+1)
					}
				}
				answering.Store(true)
				if got := rig.postModel(t, "/v1/messages", "claude-sonnet-4-5"); got.status != 200 {
					t.Fatalf("the fourth request = %d %q, want 200", got.status, got.body)
				}
			})
			s.Then("one failing event and one recovery event are all the health there is", func(t *testing.T) {
				// The success is counted once the stream has reached the client, so the client can
				// read its 200 before the recovery event and its log line are written.
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) && !strings.Contains(lines.joined(), "Anthropic path recovered") {
					time.Sleep(10 * time.Millisecond)
				}
				failing := healthEvents(rig.events, domain.HealthUpstreamFailing)
				recovered := healthEvents(rig.events, domain.HealthUpstreamRecovered)
				if len(failing) != 1 || len(recovered) != 1 {
					t.Fatalf("health events = %d failing, %d recovered, want one of each", len(failing), len(recovered))
				}
				if recovered[0].Plugin != "anthropic" {
					t.Errorf("recovery plugin = %q, want anthropic", recovered[0].Plugin)
				}
			})
			s.And("the router log says the path recovered", func(t *testing.T) {
				if got := lines.joined(); !strings.Contains(got, "huddle-router: Anthropic path recovered") {
					t.Errorf("log = %q, want the recovery line in it", got)
				}
			})
		})
	})
}

// healthEvents is the health events the rig collected that name one kind.
func healthEvents(log *eventLog, kind domain.RouterHealth) []domain.RouterHealthEvent {
	var out []domain.RouterHealthEvent
	for _, event := range log.all() {
		if health, ok := event.(domain.RouterHealthEvent); ok && health.Event == kind {
			out = append(out, health)
		}
	}
	return out
}
