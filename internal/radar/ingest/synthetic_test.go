package ingest_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// `<synthetic>` is the model name Claude Code writes on assistant messages it makes itself, as
// found in real transcripts: "No response requested.", usage-limit and connection notices. None
// of them is a model call.
func syntheticLine(ts int64, text, failure string) map[string]any {
	line := map[string]any{
		"type": "assistant", "timestamp": iso(ts), "isSidechain": false,
		"message": map[string]any{
			"model":       "<synthetic>",
			"role":        "assistant",
			"stop_reason": "stop_sequence",
			"usage":       map[string]any{"input_tokens": 0, "output_tokens": 0},
			"content":     []any{map[string]any{"type": "text", "text": text}},
		},
	}
	if failure != "" {
		line["isApiErrorMessage"] = true
		line["error"] = failure
	}
	return line
}

func realLine(ts int64, id string) map[string]any {
	return map[string]any{
		"type": "assistant", "timestamp": iso(ts), "requestId": id,
		"message": map[string]any{
			"model":   "claude-opus-5-5",
			"usage":   map[string]any{"input_tokens": 3, "output_tokens": 7},
			"content": []any{},
		},
	}
}

func TestSyntheticMessages(t *testing.T) {
	t.Run("are never model requests; the error ones show as a Claude Code notice on their session and agent", func(t *testing.T) {
		e := makeEnv(t)
		t0 := time.Now().UnixMilli() - 60_000
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "s1.jsonl"), jsonl(t,
			map[string]any{"type": "user", "timestamp": iso(t0), "cwd": "/w/app", "message": map[string]any{"content": "go"}},
			realLine(t0+1000, "req-1"),
			syntheticLine(t0+2000, "No response requested.", ""),
			syntheticLine(t0+3000, "You've hit your session limit · resets 1pm", "rate_limit"),
			syntheticLine(t0+4000, "API Error: Connection lost mid-response.", "server_error"),
		))
		st := newStore()
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env: e.env, Store: st, SinceMs: 86_400_000, IntervalMs: 3_600_000, Alive: ownPidAlive,
		})
		t.Cleanup(w.Stop)
		waitFor(t, "synthetic transcript ingest", 5*time.Second, func() bool { return !w.CatchingUp() })
		models := st.Models(nil).Models
		if len(models) != 1 || models[0].Model != "claude-opus-5-5" {
			t.Errorf("models = %+v, want only claude-opus-5-5", models)
		}
		if got := len(st.Requests(store.RequestFilter{})); got != 1 {
			t.Errorf("request count = %d, want 1", got)
		}
		summary := st.Summary()
		if summary.Requests != 1 || summary.Errors != 2 {
			t.Errorf("summary = %+v, want 1 request and 2 errors", summary)
		}
		session := st.SessionDetail("s1")
		if session == nil {
			t.Fatalf("session s1 not found")
		}
		if session.ErrorCount != 2 {
			t.Errorf("session error count = %d, want 2", session.ErrorCount)
		}
		if agent := findAgent(t, session, "main"); agent.Errors != 2 {
			t.Errorf("main agent errors = %d, want 2", agent.Errors)
		}
		var labels []string
		for _, event := range st.Events(store.EventsFilter{Session: ptr("s1")}) {
			if event.Kind == shared.NoticeEvent && event.Label != nil {
				labels = append(labels, *event.Label)
			}
		}
		want := []string{"Claude Code notice: connection error", "Claude Code notice: usage limit"}
		if !slices.Equal(labels, want) {
			t.Errorf("notice labels = %v, want %v", labels, want)
		}
	})
}
