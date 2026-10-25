package router

// Anthropic's unified limits: every claude-* answer carries the 5-hour and 7-day windows'
// utilization and reset times as headers, and the route event carries them as typed fields.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestAClaudeAnswerCarriesItsUnifiedUtilizationOnTheRouteEvent(t *testing.T) {
	t.Parallel()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
		w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", "1792951800")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.61")
		w.Header().Set("Anthropic-Ratelimit-Unified-7d-Reset", "1793030400")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	t.Cleanup(upstream.Close)
	rig := newMultiRig(t, multiOpts{anthropic: mustURL(t, upstream.URL)})

	if got := rig.postModel(t, "/v1/messages", "claude-opus-5-5"); got.status != 200 {
		t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(rig.events.routes()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	routes := rig.events.routes()
	if len(routes) != 1 {
		t.Fatalf("the router wrote %d route events, want 1", len(routes))
	}
	reset5h, reset7d := int64(1792951800), int64(1793030400)
	want := domain.UnifiedUtilization{
		FiveHour: &domain.UnifiedWindow{Used: 0.42, ResetsAt: &reset5h},
		SevenDay: &domain.UnifiedWindow{Used: 0.61, ResetsAt: &reset7d},
	}
	if got := routes[0].Utilization; got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("the route event's utilization = %s, want 0.42 until %d and 0.61 until %d", describeUtilization(got), reset5h, reset7d)
	}
}

// describeUtilization prints a route event's utilization with its windows spelled out.
func describeUtilization(u *domain.UnifiedUtilization) string {
	if u == nil {
		return "none"
	}
	window := func(w *domain.UnifiedWindow) string {
		if w == nil {
			return "none"
		}
		if w.ResetsAt == nil {
			return fmt.Sprintf("%v (no reset)", w.Used)
		}
		return fmt.Sprintf("%v until %d", w.Used, *w.ResetsAt)
	}
	return "5h " + window(u.FiveHour) + ", 7d " + window(u.SevenDay)
}
