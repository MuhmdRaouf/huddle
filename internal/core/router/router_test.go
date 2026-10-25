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
