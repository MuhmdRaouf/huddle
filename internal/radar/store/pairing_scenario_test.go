package store_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// How radar pairs a router's route line with the transcript's copy of the same call, as
// scenarios: both records come through the ingest edges a real spool line and a real
// transcript line take, so the request id each source carries is the one the store pairs on.

const (
	pairSession = "8c1f2e9a-5d1b-4a43-9a51-0f7c3d2b6e10"
	pairAgent   = "ae2990ce36222aba5"
	pairModel   = "claude-opus-5-5"
	pairCallID  = "req_011CgMy9ewCgDMWzoGEYREUC"
	// pairStart is 2026-10-25T08:38:08.571Z, the route line's own stamp.
	pairStart int64 = 1_792_886_288_571
)

func pairISO(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

// routeLine is the record a router's route line for one subagent call lands as: the agent
// spelled bare, as Claude Code's agent header carries it, and Anthropic's request-id among
// the answer's headers.
func routeLine(t *testing.T, ts int64, requestID string) shared.RequestRecord {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"ts": pairISO(ts), "event": "route", "plugin": "zai", "session_id": pairSession,
		"agent_id": pairAgent, "model": pairModel, "upstream": "https://api.anthropic.com",
		"route": "anthropic", "status": 200, "latency_ms": 900,
		"headers": map[string]any{"request-id": requestID, "content-type": "text/event-stream"},
	})
	if err != nil {
		t.Fatalf("marshal route line: %v", err)
	}
	var line shared.SpoolLine
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatalf("decode route line: %v", err)
	}
	return ingest.RouteRequest(line, *line.Model, ts)
}

// transcriptCopy is the record the subagent's own transcript line lands as: the file names
// the agent agent-<id>, and the entry carries the call's requestId and its final usage.
func transcriptCopy(t *testing.T, ts int64, requestID string) shared.RequestRecord {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": pairISO(ts), "requestId": requestID,
		"sessionId": pairSession, "isSidechain": true,
		"message": map[string]any{
			"id": "msg_01PairScenario", "role": "assistant", "model": pairModel, "stop_reason": "end_turn",
			"content": []any{map[string]any{"type": "text", "text": "done"}},
			"usage": map[string]any{
				"input_tokens": 12, "output_tokens": 512,
				"cache_read_input_tokens": 40_000, "cache_creation_input_tokens": 300,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal transcript line: %v", err)
	}
	file := ingest.IdentifyFile(fmt.Sprintf("/home/projects/-w-app/%s/subagents/agent-%s.jsonl", pairSession, pairAgent))
	emit := ingest.FeedTranscriptLine(ingest.NewTranscriptState(), file, string(raw))
	if len(emit.Requests) != 1 {
		t.Fatalf("the transcript line made %d requests, want 1", len(emit.Requests))
	}
	return emit.Requests[0]
}

// callsOf lists the stored requests that stand for the one call, whatever id they sit under:
// a route row names it in its headers, a transcript row by its own id.
func callsOf(st *store.Store, requestID string) []shared.RequestRecord {
	var calls []shared.RequestRecord
	for _, record := range st.Raw().Requests {
		header := ""
		if record.Headers != nil {
			header, _ = record.Headers.Get("request-id")
		}
		if record.ID == requestID || header == requestID {
			calls = append(calls, record)
		}
	}
	return calls
}

func TestRouterAndTranscriptCopiesPairOnTheRequestIDScenarios(t *testing.T) {
	bdd.Feature(t, "a route line and its transcript copy are one stored request", func(f *bdd.F) {
		f.Scenario("the copy lands 12 ms later, 5,000 records after its route line", func(s *bdd.S) {
			const unrelated = 5_000
			var (
				st    *store.Store
				route shared.RequestRecord
				copy  shared.RequestRecord
			)
			s.Given("a subagent's route line, then 5,000 route lines of the same session's later calls", func(t *testing.T) {
				st = store.CreateStore(func() time.Time { return time.UnixMilli(pairStart) })
				route = routeLine(t, pairStart, pairCallID)
				st.AddRequest(route, nil)
				for call := 1; call <= unrelated; call++ {
					st.AddRequest(routeLine(t, pairStart+int64(call)*1_000, fmt.Sprintf("req_unrelated%05d", call)), nil)
				}
			})
			s.When("the transcript's copy of the first call arrives, stamped 12 ms after its route line", func(t *testing.T) {
				copy = transcriptCopy(t, pairStart+12, pairCallID)
				st.AddRequest(copy, nil)
			})
			s.Then("the store holds one request for the call, with the transcript's tokens", func(t *testing.T) {
				calls := callsOf(st, pairCallID)
				if len(calls) != 1 {
					t.Fatalf("the call is stored %d times (%+v), want once", len(calls), calls)
				}
				if calls[0].Tokens != copy.Tokens {
					t.Errorf("the call's tokens are %+v, want the transcript's %+v", calls[0].Tokens, copy.Tokens)
				}
				if total := len(st.Raw().Requests); total != unrelated+1 {
					t.Errorf("the store holds %d requests, want %d: one per call", total, unrelated+1)
				}
			})
			s.And("the call sits under the transcript's agent- spelling, the route line's bare one folded in", func(t *testing.T) {
				if route.AgentID != pairAgent || copy.AgentID != "agent-"+pairAgent {
					t.Fatalf("the sources spell the agent %q and %q; the scenario needs %q and agent-%s",
						route.AgentID, copy.AgentID, pairAgent, pairAgent)
				}
				calls := callsOf(st, pairCallID)
				if len(calls) != 1 || calls[0].AgentID != copy.AgentID {
					t.Errorf("the call is stored as %+v, want one request under %s", calls, copy.AgentID)
				}
			})
			s.And("re-ingesting both sources keeps one request per call", func(t *testing.T) {
				st.AddRequest(route, nil)
				st.AddRequest(copy, nil)
				for call := 1; call <= unrelated; call++ {
					st.AddRequest(routeLine(t, pairStart+int64(call)*1_000, fmt.Sprintf("req_unrelated%05d", call)), nil)
				}
				if calls := callsOf(st, pairCallID); len(calls) != 1 || calls[0].Tokens != copy.Tokens {
					t.Errorf("after the re-ingest the call is stored as %+v, want once with the transcript's tokens", calls)
				}
				if total := len(st.Raw().Requests); total != unrelated+1 {
					t.Errorf("after the re-ingest the store holds %d requests, want %d", total, unrelated+1)
				}
			})
		})
	})
}
