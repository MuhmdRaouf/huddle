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
