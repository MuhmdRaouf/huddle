package router

// Reload: the lane set a running huddle-router serves follows providers.json without a restart.
// One scenario proves the swap: the next request picks from the new lanes and the health answer
// follows.

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestMultiReloadFollowsTheNewLanes(t *testing.T) {
	zaiSeen := &seenList{}
	zaiUp := recordingUpstream(t, "zai", "/api/anthropic", zaiSeen)
	kimiSeen := &seenList{}
	kimiUp := recordingUpstream(t, "kimi", "/api/anthropic", kimiSeen)
	anthropicSeen := &seenList{}
	anthropicUp := recordingUpstream(t, "anthropic", "", anthropicSeen)
	port := reservePort(t)
	key := func(name string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return name + "-secret", nil }
	}
	server, multi := CreateMulti(MultiOptions{
		Port:      port,
		Anthropic: anthropicUp,
		Lanes: []RouterOptions{{
			Provider:    routerProvider,
			ProviderURL: zaiUp,
			Key:         key("zai"),
		}},
	})
	serveRouter(t, server, port)

	body := `{"model":"glm-5.3","messages":[]}`
	if got, err := postRawBody(port, "/v1/messages", body, map[string]string{"Content-Type": "application/json"}); err != nil || got.status != 200 {
		t.Fatalf("before the reload: %v, %d %q", err, got.status, got.body)
	}

	lane := RouterOptions{Provider: kimiProvider(), ProviderURL: kimiUp, Key: key("kimi")}
	multi.Reload([]RouterOptions{lane})

	if got := multi.Providers(); !reflect.DeepEqual(got, []string{"kimi"}) {
		t.Fatalf("Providers after the reload = %v, want [kimi]", got)
	}
	// The model the taken-away lane claimed falls to Anthropic now.
	got, err := postRawBody(port, "/v1/messages", body, map[string]string{"Content-Type": "application/json"})
	if err != nil || got.status != 200 {
		t.Fatalf("glm after the reload: %v, %d %q", err, got.status, got.body)
	}
	if anthropicSeen.len() != 1 {
		t.Fatalf("Anthropic saw %d requests, want the taken-away model", anthropicSeen.len())
	}
	if zaiSeen.len() != 1 {
		t.Errorf("zai saw %d requests after it was taken away, want only the first", zaiSeen.len())
	}
	// The added lane serves its own models.
	if got, err := postRawBody(port, "/v1/messages", `{"model":"kimi-main","messages":[]}`, map[string]string{"Content-Type": "application/json"}); err != nil || got.status != 200 {
		t.Fatalf("kimi after the reload: %v, %d %q", err, got.status, got.body)
	}
	if kimiSeen.len() != 1 {
		t.Errorf("kimi saw %d requests, want 1", kimiSeen.len())
	}
	if header := kimiSeen.list()[0].headers.Get("Authorization"); header != "Bearer kimi-secret" {
		t.Errorf("kimi's authorization = %q", header)
	}
}

// TestMultiReloadKeepsARoutedRequestOnItsLane pins the immutability: a request already on a lane
// finishes on it across a reload (the lane set is swapped, never mutated).
func TestMultiReloadKeepsARoutedRequestOnItsLane(t *testing.T) {
	zaiSeen := &seenList{}
	anthropicUp := recordingUpstream(t, "anthropic", "", &seenList{})
	port := reservePort(t)
	var reloaded atomic.Bool
	var multi *Multi
	// zai's upstream fires the reload once the request reaches it: the lanes swap while the
	// request sits on its lane.
	zaiPort := serve(t, func(w http.ResponseWriter, req *http.Request) {
		body, err := ReadBody(req, BodyLimitBytes)
		if err != nil {
			return
		}
		res := NewResponse(w)
		zaiSeen.record(pathOf(req), req.Header, string(body))
		if !reloaded.Swap(true) {
			multi.Reload(nil)
		}
		res.Header().Set("Content-Type", "text/event-stream")
		res.WriteHeader(200)
		res.Write([]byte("data: zai-1\n\n"))
		res.End([]byte("data: zai-2\n\n"))
	}, nil)
	zaiUp := mustURL(t, fmt.Sprintf("http://127.0.0.1:%d/api/anthropic", zaiPort))
	var server *http.Server
	server, multi = CreateMulti(MultiOptions{
		Port:      port,
		Anthropic: anthropicUp,
		Lanes: []RouterOptions{{
			Provider:    routerProvider,
			ProviderURL: zaiUp,
			Key:         func(context.Context) (string, error) { return "zai-secret", nil },
		}},
	})
	serveRouter(t, server, port)
	got, err := postRawBody(port, "/v1/messages", `{"model":"glm-5.3","messages":[]}`, map[string]string{"Content-Type": "application/json"})
	if err != nil || got.status != 200 {
		t.Fatalf("the request across the reload: %v, %d %q", err, got.status, got.body)
	}
	if zaiSeen.len() != 1 {
		t.Errorf("zai saw %d requests, want the one already routed to it", zaiSeen.len())
	}
	if len(multi.Providers()) != 0 {
		t.Errorf("Providers after Reload(nil) = %v, want none", multi.Providers())
	}
}
