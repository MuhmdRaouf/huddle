package router

// The provider route's answers: the refusals that become a non-retryable 400, the key that never
// reaches a log, the error that quotes it, and the request that goes nowhere. The lane here is
// the worker a router server serves with, driven the way the front drives it.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// laneUpstream is one upstream whose answer the row writes itself.
func laneUpstream(t *testing.T, answer func(w http.ResponseWriter, req *http.Request, body string)) *url.URL {
	t.Helper()
	port := serve(t, func(w http.ResponseWriter, req *http.Request) {
		body, _ := ReadBody(req, BodyLimitBytes)
		answer(w, req, string(body))
	}, nil)
	return mustURL(t, fmt.Sprintf("http://127.0.0.1:%d/api/anthropic", port))
}

// laneServer serves one worker the way a router server does, on a testkit port.
func laneServer(t *testing.T, worker *routerWorker) int {
	t.Helper()
	return serve(t, func(w http.ResponseWriter, req *http.Request) {
		res := NewResponse(w)
		Receive(req, res, "test", BodyLimitBytes, func(body []byte) {
			worker.forwardParsed(req, res, body, parseBodyNode(body))
		})
	}, nil)
}

const keyOfTheLane = "sk-lane-key-0123456789abcdef0123456789abcdef"

// laneWorker is one provider lane over the given upstream and key answer.
func laneWorker(t *testing.T, upstream *url.URL, key string, mutate func(*RouterOptions)) *routerWorker {
	t.Helper()
	opts := RouterOptions{
		Provider:    routerProvider,
		ProviderURL: upstream,
		Anthropic:   mustURL(t, "http://127.0.0.1:1/anthropic"),
		Key:         func(context.Context) (string, error) { return key, nil },
		Log:         func(string) {},
	}
	if mutate != nil {
		mutate(&opts)
	}
	return newRouterWorker(opts, nil)
}

func postToLane(t *testing.T, port int, body string) answer {
	t.Helper()
	got, err := postRawBody(port, "/v1/messages", body, map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer anthropic-oauth",
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestProviderRouteRefusesWhatMustNotReachClaude(t *testing.T) {
	t.Parallel()

	t.Run("no key is a non-retryable 400 naming setup", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			t.Error("a request without a key went upstream")
		})
		worker := laneWorker(t, upstream, "", nil)
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 400 {
			t.Fatalf("the answer = %d %q, want 400", got.status, got.body)
		}
		if !strings.Contains(got.body, "/zai:") || !strings.Contains(got.body, "no key is set") {
			t.Errorf("the answer = %q, want the setup line", got.body)
		}
		if got.header.Get("X-Should-Retry") == "true" {
			t.Error("the refusal is retryable")
		}
	})

	t.Run("a hung key lookup answers 503 and nothing goes upstream", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			t.Error("a request with a hung lookup went upstream")
		})
		worker := laneWorker(t, upstream, "", func(o *RouterOptions) {
			o.Key = func(ctx context.Context) (string, error) {
				<-ctx.Done()
				return "", ctx.Err()
			}
			o.KeyTimeoutMs = 80
		})
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 503 {
			t.Fatalf("the answer = %d %q, want 503", got.status, got.body)
		}
		if !strings.Contains(got.body, "timed out") {
			t.Errorf("the answer = %q, want the timeout", got.body)
		}
	})

	t.Run("a refused key becomes the non-retryable refusal and the cache forgets it", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, req *http.Request, _ string) {
			if got := req.Header.Get("Authorization"); got != "Bearer "+keyOfTheLane {
				t.Errorf("the upstream's authorization = %q, want the lane's key", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"type":"authentication_error","message":"bad key"}}`))
		})
		worker := laneWorker(t, upstream, keyOfTheLane, nil)
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 400 {
			t.Fatalf("the answer = %d %q, want 400", got.status, got.body)
		}
		if !strings.Contains(got.body, "key refused or missing") || strings.Contains(got.body, "bad key") {
			t.Errorf("the answer = %q, want the refusal without the upstream's text", got.body)
		}
	})

	t.Run("an empty balance is the balance refusal", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"type":"insufficient_balance","message":"top up"}}`))
		})
		worker := laneWorker(t, upstream, keyOfTheLane, nil)
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 400 || !strings.Contains(got.body, "no balance or quota") {
			t.Fatalf("the answer = %d %q, want the balance refusal", got.status, got.body)
		}
	})

	t.Run("an error quoting the key goes out with the key blanked", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"key ` + keyOfTheLane + ` is disabled"}}`))
		})
		worker := laneWorker(t, upstream, keyOfTheLane, nil)
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 500 {
			t.Fatalf("the answer = %d, want the upstream's 500", got.status)
		}
		if strings.Contains(got.body, keyOfTheLane) {
			t.Errorf("the answer = %q, want the key redacted", got.body)
		}
		if !strings.Contains(got.body, "[key redacted]") {
			t.Errorf("the answer = %q, want the redaction mark", got.body)
		}
	})

	t.Run("a plain error goes through with its reason on the line", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"the model is busy"}}`))
		})
		worker := laneWorker(t, upstream, keyOfTheLane, nil)
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 500 || !strings.Contains(got.body, "the model is busy") {
			t.Fatalf("the answer = %d %q, want the upstream's error", got.status, got.body)
		}
	})

	t.Run("an upstream that answers nothing is a 502 naming the errno", func(t *testing.T) {
		t.Parallel()
		dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		deadURL := dead.URL
		dead.Close()
		worker := laneWorker(t, mustURL(t, deadURL+"/api/anthropic"), keyOfTheLane, nil)
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 502 {
			t.Fatalf("the answer = %d %q, want 502", got.status, got.body)
		}
		if !strings.Contains(got.body, "unreachable") {
			t.Errorf("the answer = %q, want the errno named", got.body)
		}
	})

	t.Run("an upstream that is not https and not loopback is refused", func(t *testing.T) {
		t.Parallel()
		worker := laneWorker(t, mustURL(t, "http://provider.example:1/api/anthropic"), keyOfTheLane, nil)
		got := postToLane(t, laneServer(t, worker), `{"model":"glm-5.3","messages":[]}`)
		if got.status != 502 || !strings.Contains(got.body, "must be https unless it is loopback") {
			t.Fatalf("the answer = %d %q, want the forward refusal", got.status, got.body)
		}
	})
}

func TestProviderRouteRecordsTheEventsClaudeCodeReads(t *testing.T) {
	t.Parallel()
	events := &eventLog{}
	upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, body string) {
		if !strings.Contains(body, `"system"`) {
			t.Errorf("the upstream saw %q, want the body as it came", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":1}}}\n\n"))
	})
	worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
		o.Events = events.record
	})
	got := postToLane(t, laneServer(t, worker),
		`{"model":"glm-5.3","system":"the system prompt","messages":[{"role":"user"}]}`)
	if got.status != 200 {
		t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(events.routes()) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	routes := events.routes()
	if len(routes) == 0 {
		t.Fatal("the lane wrote no route event")
	}
	route := routes[len(routes)-1]
	if route.Status != 200 || route.Model != "glm-5.3" {
		t.Errorf("the route event = %+v, want the answer's status and model", route)
	}
	if route.Usage == nil || (*route.Usage)["input_tokens"] != float64(12) {
		t.Errorf("the route event's usage = %v, want the stream's counters", route.Usage)
	}
	if route.PromptHash == nil || *route.PromptHash == "" {
		t.Error("the route event carries no prompt hash")
	}
	captured := false
	for _, event := range events.all() {
		if _, ok := event.(domain.CaptureEvent); ok {
			captured = true
		}
	}
	if !captured {
		t.Error("the lane captured nothing for the body's system")
	}
}

func TestRouteOfSendsAModelWhereItBelongs(t *testing.T) {
	t.Parallel()
	claim := routerProvider.ModelClaim()
	route, _, hasPeer := routeOf(claim, "glm-5.3", nil)
	if route != "provider" || hasPeer {
		t.Errorf("routeOf(glm-5.3) = %q, %t, want the provider", route, hasPeer)
	}
	route, _, _ = routeOf(claim, "claude-opus-5-5", nil)
	if route != "anthropic" {
		t.Errorf("routeOf(claude) = %q, want anthropic", route)
	}
	route, peer, hasPeer := routeOf(claim, "kimi-main", func(string) (PeerTarget, bool) {
		return PeerTarget{Name: "kimi", Port: 18788}, true
	})
	if !hasPeer || route != "peer:kimi" || peer.Port != 18788 {
		t.Errorf("routeOf(kimi-main) = %q, %+v, %t, want the peer", route, peer, hasPeer)
	}
	route, _, _ = routeOf(claim, "kimi-main", nil)
	if route != "anthropic" {
		t.Errorf("routeOf with no peers = %q, want anthropic", route)
	}
}

func TestForwardableAndRequestPath(t *testing.T) {
	t.Parallel()
	if why, refused := forwardable("provider", mustURL(t, "https://api.z.ai/api/anthropic")); refused {
		t.Errorf("forwardable(https) = %q, want it through", why)
	}
	if _, refused := forwardable("provider", mustURL(t, "http://127.0.0.1:1/api")); refused {
		t.Error("forwardable(loopback http) refused the lane's own neighbour")
	}
	if why, refused := forwardable("provider", mustURL(t, "http://provider.example:1/api")); !refused || !strings.Contains(why, "must be https") {
		t.Errorf("forwardable(remote http) = %q, %t, want the refusal", why, refused)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", nil)
	if got := requestPath(request); got != "/v1/messages?beta=true" {
		t.Errorf("requestPath = %q, want the request's own", got)
	}
	plain := &http.Request{URL: &url.URL{Path: "/v1/messages", RawQuery: "a=b"}}
	if got := requestPath(plain); got != "/v1/messages?a=b" {
		t.Errorf("requestPath = %q, want the url's own", got)
	}
}

func TestKeyWithinTimesOutUnderItsBudget(t *testing.T) {
	t.Parallel()
	worker := newRouterWorker(RouterOptions{Provider: routerProvider, KeyTimeoutMs: 60},
		func(ctx context.Context) (ports.ProviderKey, *ports.WorkerError) {
			<-ctx.Done()
			return ports.ProviderKey{}, nil
		})
	started := time.Now()
	key, outcome := keyWithin(context.Background(), worker)
	if outcome != keyTimeout || key != "" {
		t.Fatalf("keyWithin = %q, %v, want the timeout", key, outcome)
	}
	if time.Since(started) > 2*time.Second {
		t.Errorf("keyWithin took %s, want the budget", time.Since(started))
	}
}
