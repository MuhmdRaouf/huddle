// The gateway mounted inside huddle-router: CreateMulti serves /gateway/... through
// gateway.Server with the prefix stripped — the bytes and the caller's credentials unchanged —
// and the gateway keeps writing its route lines.
package gateway_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/gateway"
)

// mountRecorder collects the route lines the mounted gateway wrote.
type mountRecorder struct {
	mu     sync.Mutex
	events []domain.RouteEvent
}

func (r *mountRecorder) record(event domain.RouteEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

// waitFor holds the test until the gateway has recorded n route lines — the client can be done
// reading an answer while the handler is still writing its line — and answers what it saw.
func (r *mountRecorder) waitFor(t *testing.T, n int) []domain.RouteEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if events := r.all(); len(events) >= n {
			return events
		}
		time.Sleep(2 * time.Millisecond)
	}
	return r.all()
}

func (r *mountRecorder) all() []domain.RouteEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.RouteEvent(nil), r.events...)
}

func TestMountedGatewayForwardsThroughTheRouter(t *testing.T) {
	const (
		requestBody = `{"model":"test-1","messages":[]}`
		answerBody  = `{"id":"msg_1","usage":{"input_tokens":3,"output_tokens":1}}`
	)
	var (
		spyBody   []byte
		spyHeader http.Header
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("upstream read: %v", err)
		}
		spyBody, spyHeader = body, r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(answerBody))
	}))
	t.Cleanup(upstream.Close)

	anthropic, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	rec := &mountRecorder{}
	server, _ := router.CreateMulti(router.MultiOptions{
		Anthropic: anthropic,
		Gateway: gateway.NewServer(gateway.Config{
			Routes: []gateway.Route{{Match: "test-*", Upstream: upstream.URL}},
		}, rec.record, nil),
	})
	mounted := httptest.NewServer(server.Handler)
	t.Cleanup(mounted.Close)

	request, err := http.NewRequest(http.MethodPost, mounted.URL+"/gateway/v1/messages", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer caller-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST /gateway/v1/messages: %v", err)
	}
	defer response.Body.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(got) != answerBody {
		t.Fatalf("answer = %d %q, want 200 %q", response.StatusCode, got, answerBody)
	}

	if spyBody == nil {
		t.Fatal("the upstream saw no request")
	}
	if string(spyBody) != requestBody {
		t.Errorf("upstream body = %q, want %q", spyBody, requestBody)
	}
	if header := spyHeader.Get("Authorization"); header != "Bearer caller-token" {
		t.Errorf("upstream authorization = %q, want the caller's own", header)
	}

	events := rec.waitFor(t, 1)
	if len(events) != 1 {
		t.Fatalf("recorded %d route lines, want 1", len(events))
	}
	if events[0].Plugin != "gateway" {
		t.Errorf("route line plugin = %q, want gateway", events[0].Plugin)
	}
}
