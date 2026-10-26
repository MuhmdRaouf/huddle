package router

// The capture hash and Claude Code's billing block: the block changes on every request, so the
// hash leaves it out and the route event carries its ids instead.

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// billedBody is a request body whose system opens with Claude Code's billing block.
func billedBody(promptID, prevReq, turnIndex string) string {
	return `{"model":"glm-5.3","system":[` +
		`{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.30.a1b; cc_entrypoint=cli; ` +
		`cc_prompt_id=` + promptID + `; cc_prev_req=` + prevReq + `; cc_turn_index=` + turnIndex + `;"},` +
		`{"type":"text","text":"You are Claude Code."}],` +
		`"tools":[{"name":"Read"}],"messages":[{"role":"user","content":"hi"}]}`
}

func TestCaptureHashLeavesOutTheBillingBlock(t *testing.T) {
	t.Parallel()
	first := billedBody("p-1", "req_aaa", "3")
	second := billedBody("p-2", "req_bbb", "4")

	t.Run("two bodies that differ only in the billing block hash the same", func(t *testing.T) {
		t.Parallel()
		one, ok1 := PromptHashOf(parseBodyNode([]byte(first)))
		two, ok2 := PromptHashOf(parseBodyNode([]byte(second)))
		if !ok1 || !ok2 || one != two {
			t.Errorf("hashes = %q/%q (%t/%t), want one hash", one, two, ok1, ok2)
		}
		plain, _ := PromptHashOf(parseBodyNode([]byte(
			`{"system":[{"type":"text","text":"You are Claude Code."}],"tools":[{"name":"Read"}]}`)))
		if plain != one {
			t.Errorf("the billed hash %q differs from the same prompt without the block %q", one, plain)
		}
	})

	t.Run("the block's ids reach the route event and the capture leaves the block out", func(t *testing.T) {
		t.Parallel()
		events := &eventLog{}
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
		})
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Events = events.record
		})
		port := laneServer(t, worker)
		for _, body := range []string{first, second} {
			if got := postToLane(t, port, body); got.status != 200 {
				t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
			}
		}
		captures := func() []domain.CaptureEvent {
			var out []domain.CaptureEvent
			for _, event := range events.all() {
				if capture, ok := event.(domain.CaptureEvent); ok {
					out = append(out, capture)
				}
			}
			return out
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && (len(events.routes()) < 2 || len(captures()) == 0) {
			time.Sleep(10 * time.Millisecond)
		}
		routes := events.routes()
		if len(routes) != 2 {
			t.Fatalf("the lane wrote %d route events, want 2", len(routes))
		}
		if routes[0].PromptHash == nil || routes[1].PromptHash == nil || *routes[0].PromptHash != *routes[1].PromptHash {
			t.Errorf("the route events' prompt hashes = %v/%v, want one hash", routes[0].PromptHash, routes[1].PromptHash)
		}
		want := []struct {
			promptID, prevReq string
			turn              int64
		}{{"p-1", "req_aaa", 3}, {"p-2", "req_bbb", 4}}
		for i, route := range routes {
			if route.PromptID == nil || *route.PromptID != want[i].promptID {
				t.Errorf("route %d prompt_id = %v, want %q", i, route.PromptID, want[i].promptID)
			}
			if route.PrevRequest == nil || *route.PrevRequest != want[i].prevReq {
				t.Errorf("route %d prev_request = %v, want %q", i, route.PrevRequest, want[i].prevReq)
			}
			if route.TurnIndex == nil || *route.TurnIndex != want[i].turn {
				t.Errorf("route %d turn_index = %v, want %d", i, route.TurnIndex, want[i].turn)
			}
		}
		written := captures()
		if len(written) == 0 {
			t.Fatal("the lane captured nothing")
		}
		raw, err := base64.StdEncoding.DecodeString(written[0].Gz)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "x-anthropic-billing-header") || !strings.Contains(string(content), "You are Claude Code.") {
			t.Errorf("the capture = %s, want the prompt without the billing block", content)
		}
	})
}

// claudeStream is one Anthropic answer as an event stream: message_start carries the input and
// cache counters, message_delta the output.
var claudeStream = []string{
	"event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-5",` +
		`"usage":{"input_tokens":12,"cache_read_input_tokens":3400,"cache_creation_input_tokens":560,"output_tokens":1}}}` + "\n\n",
	"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n",
	"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":78}}` + "\n\n",
	"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n",
}

func TestClaudeRouteEventCarriesTheStreamsUsage(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{"gzip", ""} {
		name := encoding
		if name == "" {
			name = "plain"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The upstream's bytes, flushed event by event the way Anthropic streams them.
			var sent bytes.Buffer
			upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Request-Id", "req_stream")
				if encoding != "" {
					w.Header().Set("Content-Encoding", encoding)
				}
				w.WriteHeader(200)
				flusher := w.(http.Flusher)
				var out io.Writer = io.MultiWriter(w, &sent)
				var zipped *gzip.Writer
				if encoding == "gzip" {
					zipped = gzip.NewWriter(out)
					out = zipped
				}
				for _, event := range claudeStream {
					_, _ = io.WriteString(out, event)
					if zipped != nil {
						_ = zipped.Flush()
					}
					flusher.Flush()
				}
				if zipped != nil {
					_ = zipped.Close()
				}
			})
			events := &eventLog{}
			worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
				o.Anthropic = upstream
				o.Events = events.record
			})
			port := laneServer(t, worker)
			got, err := postRawBody(port, "/v1/messages", `{"model":"claude-sonnet-4-5","stream":true,"messages":[]}`, map[string]string{
				"Content-Type":    "application/json",
				"Authorization":   "Bearer anthropic-oauth",
				"Accept-Encoding": "gzip, br",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.status != 200 {
				t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
			}
			if got.body != sent.String() {
				t.Errorf("the client got %d bytes, want the upstream's %d bytes as sent", len(got.body), sent.Len())
			}
			if got.header.Get("Content-Encoding") != encoding || got.header.Get("Content-Type") != "text/event-stream" ||
				got.header.Get("Request-Id") != "req_stream" {
				t.Errorf("the client's headers = %v, want the upstream's", got.header)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) && len(events.routes()) == 0 {
				time.Sleep(10 * time.Millisecond)
			}
			routes := events.routes()
			if len(routes) != 1 {
				t.Fatalf("the router wrote %d route events, want 1", len(routes))
			}
			if routes[0].Route != "anthropic" {
				t.Errorf("the route = %q, want anthropic", routes[0].Route)
			}
			want := domain.RouteUsage{
				"input_tokens":                float64(12),
				"cache_read_input_tokens":     float64(3400),
				"cache_creation_input_tokens": float64(560),
				"output_tokens":               float64(78),
			}
			if routes[0].Usage == nil {
				t.Fatalf("the route event carries no usage, want %v", want)
			}
			usage := *routes[0].Usage
			for key, value := range want {
				if usage[key] != value {
					t.Errorf("usage[%s] = %v, want %v (usage %v)", key, usage[key], value, usage)
				}
			}
		})
	}
}

func TestRouteEventCarriesTimeToFirstByteAndFirstToken(t *testing.T) {
	t.Parallel()

	t.Run("a stream times its head and its first token", func(t *testing.T) {
		t.Parallel()
		// The head goes out after 50 ms and the stream's events after 70 ms more, so both
		// latencies have floors no clock reads under.
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			flusher := w.(http.Flusher)
			flusher.Flush()
			time.Sleep(70 * time.Millisecond)
			for _, event := range claudeStream {
				_, _ = io.WriteString(w, event)
				flusher.Flush()
			}
		})
		events := &eventLog{}
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Anthropic = upstream
			o.Events = events.record
		})
		port := laneServer(t, worker)
		got, err := postRawBody(port, "/v1/messages", `{"model":"claude-sonnet-4-5","stream":true,"messages":[]}`, map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer anthropic-oauth",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.status != 200 {
			t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && len(events.routes()) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		routes := events.routes()
		if len(routes) != 1 {
			t.Fatalf("the router wrote %d route events, want 1", len(routes))
		}
		route := routes[0]
		if route.TtfbMs == nil || *route.TtfbMs < 50 {
			t.Errorf("ttfb_ms = %v, want at least 50", route.TtfbMs)
		}
		if route.TtftMs == nil || *route.TtftMs < 120 {
			t.Errorf("ttft_ms = %v, want at least 120", route.TtftMs)
		}
		if route.TtftMs != nil && route.TtfbMs != nil && *route.TtftMs < *route.TtfbMs {
			t.Errorf("ttft_ms %d came before ttfb_ms %d, want the token after the head", *route.TtftMs, *route.TtfbMs)
		}
		if route.TtfbMs != nil && *route.TtfbMs > route.LatencyMs {
			t.Errorf("ttfb_ms %d is past the request's latency_ms %d", *route.TtfbMs, route.LatencyMs)
		}
		if route.TtftMs != nil && *route.TtftMs > route.LatencyMs {
			t.Errorf("ttft_ms %d is past the request's latency_ms %d", *route.TtftMs, route.LatencyMs)
		}
	})

	t.Run("a JSON answer times its head and carries no first token", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
		})
		events := &eventLog{}
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Anthropic = upstream
			o.Events = events.record
		})
		port := laneServer(t, worker)
		got, err := postRawBody(port, "/v1/messages", `{"model":"claude-sonnet-4-5","messages":[]}`, map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer anthropic-oauth",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.status != 200 {
			t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && len(events.routes()) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		routes := events.routes()
		if len(routes) != 1 {
			t.Fatalf("the router wrote %d route events, want 1", len(routes))
		}
		route := routes[0]
		if route.TtfbMs == nil {
			t.Error("ttfb_ms = nil, want the head's time")
		}
		if route.TtftMs != nil {
			t.Errorf("ttft_ms = %d, want none: a JSON answer streams no content_block_delta", *route.TtftMs)
		}
	})
}

// assertRouterMicroseconds checks one forwarded request's route event: the router's own
// microseconds are there, at least one and never past the request's whole latency.
func assertRouterMicroseconds(t *testing.T, route domain.RouteEvent) {
	t.Helper()
	if route.RouterUs == nil {
		t.Fatal("the route event carries no router_us, want the router's own time")
	}
	if *route.RouterUs < 1 {
		t.Errorf("router_us = %d, want at least 1", *route.RouterUs)
	}
	if *route.RouterUs > route.LatencyMs*1000 {
		t.Errorf("router_us %d is past the request's whole latency_ms %d", *route.RouterUs, route.LatencyMs)
	}
}

func TestTheRouteEventCarriesTheRoutersOwnMicroseconds(t *testing.T) {
	t.Parallel()

	t.Run("a provider request carries the router's own time", func(t *testing.T) {
		t.Parallel()
		// The upstream holds the answer 20 ms, so the event's latency_ms has a floor no clock
		// reads under and the bound against it is exact.
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			time.Sleep(20 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
		})
		events := &eventLog{}
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Events = events.record
		})
		port := laneServer(t, worker)
		if got := postToLane(t, port, `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`); got.status != 200 {
			t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && len(events.routes()) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		routes := events.routes()
		if len(routes) != 1 {
			t.Fatalf("the lane wrote %d route events, want 1", len(routes))
		}
		if routes[0].Route != "provider" {
			t.Fatalf("the route = %q, want provider", routes[0].Route)
		}
		assertRouterMicroseconds(t, routes[0])
	})

	t.Run("an Anthropic request carries the router's own time", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			time.Sleep(20 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
		})
		events := &eventLog{}
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Anthropic = upstream
			o.Events = events.record
		})
		port := laneServer(t, worker)
		got, err := postRawBody(port, "/v1/messages", `{"model":"claude-sonnet-4-5","messages":[]}`, map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer anthropic-oauth",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.status != 200 {
			t.Fatalf("the answer = %d %q, want 200", got.status, got.body)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && len(events.routes()) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		routes := events.routes()
		if len(routes) != 1 {
			t.Fatalf("the router wrote %d route events, want 1", len(routes))
		}
		if routes[0].Route != "anthropic" {
			t.Fatalf("the route = %q, want anthropic", routes[0].Route)
		}
		assertRouterMicroseconds(t, routes[0])
	})

	t.Run("a request refused as a second hop carries none", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		})
		events := &eventLog{}
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Anthropic = upstream
			o.Events = events.record
		})
		port := laneServer(t, worker)
		got, err := postRawBody(port, "/v1/messages", `{"model":"claude-sonnet-4-5","messages":[]}`, map[string]string{
			"Content-Type":          "application/json",
			"Authorization":         "Bearer anthropic-oauth",
			"X-Provider-Router-Hop": "1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.status != 508 {
			t.Fatalf("the answer = %d %q, want 508", got.status, got.body)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && len(events.routes()) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		routes := events.routes()
		if len(routes) != 1 {
			t.Fatalf("the router wrote %d route events, want 1", len(routes))
		}
		if routes[0].Route != "refused" {
			t.Fatalf("the route = %q, want refused", routes[0].Route)
		}
		if routes[0].RouterUs != nil {
			t.Errorf("the refused request's router_us = %d, want none: nothing went upstream", *routes[0].RouterUs)
		}
	})
}

func TestPromptRefWaitsForItsCapture(t *testing.T) {
	t.Parallel()

	t.Run("a ref with no capture answers at once", func(t *testing.T) {
		t.Parallel()
		prompt := &promptRef{}
		if hash := hashWithin(t, prompt); hash != nil {
			t.Errorf("Hash() = %v, want nil", hash)
		}
		if billing := prompt.Billing(); billing.promptID != nil {
			t.Errorf("Billing() = %+v, want the zero line", billing)
		}
	})

	t.Run("a Hash read right after capture waits for the goroutine's hash", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":1}}`))
		})
		events := &eventLog{}
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Events = events.record
		})
		body := []byte(`{"system":"the system prompt","messages":[{"role":"user"}]}`)
		prompt := &promptRef{}
		// nil spans: the capture scans for itself, as on a route that read no spans.
		worker.capture(prompt, body, nil)
		want, ok := PromptHashOf(parseBodyNode(body))
		if !ok {
			t.Fatal("the body hashed to nothing")
		}
		if hash := hashWithin(t, prompt); hash == nil || *hash != want {
			t.Errorf("Hash() = %v, want %q", hash, want)
		}
	})

	t.Run("a body with neither system nor tools answers nil once the capture ends", func(t *testing.T) {
		t.Parallel()
		upstream := laneUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{}`))
		})
		events := &eventLog{}
		worker := laneWorker(t, upstream, keyOfTheLane, func(o *RouterOptions) {
			o.Events = events.record
		})
		prompt := &promptRef{}
		worker.capture(prompt, []byte(`{"model":"glm-5.3","messages":[]}`), nil)
		if hash := hashWithin(t, prompt); hash != nil {
			t.Errorf("Hash() = %v, want nil", hash)
		}
	})
}

// hashWithin reads the hash under a timer, so a wait that never ends fails the test instead of
// hanging it.
func hashWithin(t *testing.T, prompt *promptRef) *string {
	t.Helper()
	done := make(chan *string, 1)
	go func() { done <- prompt.Hash() }()
	select {
	case hash := <-done:
		return hash
	case <-time.After(2 * time.Second):
		t.Fatal("Hash() never answered")
		return nil
	}
}
