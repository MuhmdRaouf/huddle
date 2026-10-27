package radartab

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// TestRequestsSinceAnswersTheLanesRequestsOldestFirst: the tab's own API answers the recorded
// requests oldest first, one with no status or no provider left out — a call no router saw says
// nothing about a lane; an unreadable answer answers none.
func TestRequestsSinceAnswersTheLanesRequestsOldestFirst(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	start := time.Now().Add(-time.Hour)
	at := func(minute int) int64 { return start.Add(time.Duration(minute) * time.Minute).UnixMilli() }
	status := func(s string) *string { return &s }
	// two carried requests, added out of order; one with no status and one with no provider stay out
	for _, record := range []shared.RequestRecord{
		{ID: "r-1", Ts: at(3), Model: "kimi-k2", Upstream: "https://api.moonshot.ai", Provider: "Moonshot", Status: status("429")},
		{ID: "r-2", Ts: at(1), Model: "kimi-k2", Upstream: "https://api.moonshot.ai", Provider: "Moonshot", Status: status("200")},
		{ID: "r-3", Ts: at(5), Model: "kimi-k2", Upstream: "https://api.moonshot.ai", Provider: "Moonshot"},
		{ID: "r-4", Ts: at(6), Model: "kimi-k2", Upstream: "https://api.moonshot.ai", Status: status("200")},
	} {
		tab.Store.AddRequest(record, nil)
	}
	requests := tab.RequestsSince(context.Background(), start.Add(-time.Minute))
	if len(requests) != 2 {
		t.Fatalf("the requests since = %+v, want the two carried ones", requests)
	}
	if requests[0].Provider != "Moonshot" || requests[0].Status != 200 {
		t.Errorf("request 0 = %+v, want Moonshot at 200", requests[0])
	}
	if requests[1].Provider != "Moonshot" || requests[1].Status != 429 {
		t.Errorf("request 1 = %+v, want Moonshot at 429", requests[1])
	}
	if !requests[0].At.Equal(time.UnixMilli(at(1))) {
		t.Errorf("request 0 at %s, want %s: the requests answer oldest first", requests[0].At, time.UnixMilli(at(1)))
	}
	if !requests[1].At.Equal(time.UnixMilli(at(3))) {
		t.Errorf("request 1 at %s, want %s: the requests answer oldest first", requests[1].At, time.UnixMilli(at(3)))
	}
	gone := &Tab{target: "127.0.0.1:1", client: &http.Client{Timeout: probeTimeout}}
	if requests := gone.RequestsSince(context.Background(), start.Add(-time.Minute)); len(requests) != 0 {
		t.Errorf("a dead owner's requests = %+v, want none", requests)
	}
}
