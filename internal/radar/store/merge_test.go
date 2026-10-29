package store

import (
	"reflect"
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

func TestFromRouter(t *testing.T) {
	t.Run("a record carrying a route came from the router's spool", func(t *testing.T) {
		routed := makeRequest(func(r *shared.RequestRecord) { r.Route = str("provider") })
		if !FromRouter(routed) {
			t.Error("FromRouter(route set): got false, want true")
		}
		if FromRouter(makeRequest(nil)) {
			t.Error("FromRouter(no route): got true, want false")
		}
	})
}

func TestFillUpstream(t *testing.T) {
	t.Run("fills an empty upstream with the host the model's own provider serves", func(t *testing.T) {
		filled := FillUpstream(makeRequest(func(r *shared.RequestRecord) {
			r.Model = "glm-5.3"
			r.Upstream = ""
		}))
		if filled.Upstream != "https://api.z.ai" {
			t.Errorf("upstream: got %q, want %q", filled.Upstream, "https://api.z.ai")
		}
	})

	t.Run("keeps a recorded upstream as it is", func(t *testing.T) {
		kept := FillUpstream(makeRequest(func(r *shared.RequestRecord) {
			r.Model = "glm-5.3"
			r.Upstream = "http://localhost:8787/v1"
		}))
		if kept.Upstream != "http://localhost:8787/v1" {
			t.Errorf("upstream: got %q, want %q", kept.Upstream, "http://localhost:8787/v1")
		}
	})

	t.Run("keeps Anthropic and unknown ids empty", func(t *testing.T) {
		for _, model := range []string{"claude-opus-5-5", "not-a-model"} {
			direct := FillUpstream(makeRequest(func(r *shared.RequestRecord) {
				r.Model = model
				r.Upstream = ""
			}))
			if direct.Upstream != "" {
				t.Errorf("upstream of %q: got %q, want %q", model, direct.Upstream, "")
			}
		}
	})
}

func TestMatchDistance(t *testing.T) {
	transcript := makeRequest(func(r *shared.RequestRecord) {
		r.AgentID = "agent-a31cd3de"
		r.Ts = 1_000
		r.LatencyMs = i64(100)
	})
	route := makeRequest(func(r *shared.RequestRecord) {
		r.AgentID = "a31cd3de"
		r.Ts = 1_040
		r.LatencyMs = i64(110)
	})

	t.Run("rejects another session or another model", func(t *testing.T) {
		otherSession := route
		otherSession.SessionID = "s2"
		if got := MatchDistance(transcript, otherSession, "a31cd3de"); got != nil {
			t.Errorf("another session: got %s, want no match", showInt(got))
		}
		otherModel := route
		otherModel.Model = "glm-5.3"
		if got := MatchDistance(transcript, otherModel, "a31cd3de"); got != nil {
			t.Errorf("another model: got %s, want no match", showInt(got))
		}
	})

	t.Run("rejects a different agent when the line names one", func(t *testing.T) {
		if got := MatchDistance(transcript, route, "agent-someone-else"); got != nil {
			t.Errorf("another agent: got %s, want no match", showInt(got))
		}
	})

	t.Run("accepts any agent when the line names none", func(t *testing.T) {
		if got := MatchDistance(transcript, route, ""); got == nil || *got != 50 {
			t.Errorf("no named agent: got %s, want 50", showInt(got))
		}
	})

	t.Run("rejects a call further apart than MatchMs, accepts one exactly at it", func(t *testing.T) {
		late := route
		late.Ts = transcript.Ts + MatchMs + 1
		if got := MatchDistance(transcript, late, ""); got != nil {
			t.Errorf("apart %d: got %s, want no match", MatchMs+1, showInt(got))
		}
		edge := route
		edge.Ts = transcript.Ts + MatchMs
		edge.LatencyMs = i64(100)
		if got := MatchDistance(transcript, edge, ""); got == nil || *got != MatchMs {
			t.Errorf("apart %d: got %s, want %d", MatchMs, showInt(got), MatchMs)
		}
	})

	t.Run("measures only the time apart when either side lacks a latency", func(t *testing.T) {
		unmeasured := transcript
		unmeasured.LatencyMs = nil
		if got := MatchDistance(unmeasured, route, ""); got == nil || *got != 40 {
			t.Errorf("no transcript latency: got %s, want 40", showInt(got))
		}
	})
}

func TestCombine(t *testing.T) {
	transcriptCall := func(shape func(*shared.RequestRecord)) shared.RequestRecord {
		return makeRequest(func(r *shared.RequestRecord) {
			r.ID = "req_011"
			r.AgentID = "agent-a31cd3de580148dc3"
			r.Ts = 1_000
			r.LatencyMs = i64(1_951)
			r.Tokens = tokens(3, 120, 9_000, 400)
			r.StopReason = str("tool_use")
			r.Upstream = ""
			if shape != nil {
				shape(r)
			}
		})
	}
	routeLine := func(shape func(*shared.RequestRecord)) shared.RequestRecord {
		return makeRequest(func(r *shared.RequestRecord) {
			r.ID = "route:s1:1000:200"
			r.AgentID = "a31cd3de580148dc3"
			r.Ts = 1_040
			r.LatencyMs = i64(1_940)
			r.Tokens = shared.ZeroTokens
			r.StopReason = str("200")
			r.Provider = "Anthropic"
			r.Upstream = "api.anthropic.com"
			r.Route = str("anthropic")
			r.Via = str("zai")
			if shape != nil {
				shape(r)
			}
		})
	}

	t.Run("takes the route's upstream, latency and provider under the transcript's id", func(t *testing.T) {
		merged := Combine(transcriptCall(nil), routeLine(nil), "req_011")
		if merged.ID != "req_011" {
			t.Errorf("id: got %q, want %q", merged.ID, "req_011")
		}
		if merged.Upstream != "api.anthropic.com" {
			t.Errorf("upstream: got %q, want %q", merged.Upstream, "api.anthropic.com")
		}
		if merged.LatencyMs == nil || *merged.LatencyMs != 1_940 {
			t.Errorf("latencyMs: got %s, want 1940", showInt(merged.LatencyMs))
		}
		if merged.Provider != "Anthropic" {
			t.Errorf("provider: got %q, want %q", merged.Provider, "Anthropic")
		}
		if merged.Route == nil || *merged.Route != "anthropic" {
			t.Errorf("route: got %v, want anthropic", merged.Route)
		}
		if merged.Via == nil || *merged.Via != "zai" {
			t.Errorf("via: got %v, want zai", merged.Via)
		}
	})

	t.Run("keeps the transcript's tokens and stop reason, falling back to the route's", func(t *testing.T) {
		merged := Combine(transcriptCall(nil), routeLine(nil), "req_011")
		if want := tokens(3, 120, 9_000, 400); merged.Tokens != want {
			t.Errorf("tokens: got %+v, want %+v (a route line without usage never wins)", merged.Tokens, want)
		}
		if merged.StopReason == nil || *merged.StopReason != "tool_use" {
			t.Errorf("stopReason: got %v, want tool_use", merged.StopReason)
		}
		noStop := transcriptCall(func(r *shared.RequestRecord) { r.StopReason = nil })
		fallback := Combine(noStop, routeLine(nil), "req_011")
		if fallback.StopReason == nil || *fallback.StopReason != "200" {
			t.Errorf("stopReason fallback: got %v, want 200", fallback.StopReason)
		}
	})

	t.Run("the route line's usage wins when it carries any", func(t *testing.T) {
		route := routeLine(func(r *shared.RequestRecord) { r.Tokens = tokens(5, 0, 0, 0) })
		merged := Combine(transcriptCall(nil), route, "req_011")
		if want := tokens(5, 0, 0, 0); merged.Tokens != want {
			t.Errorf("tokens: got %+v, want %+v", merged.Tokens, want)
		}
	})

	t.Run("keeps the transcript's latency when the route line never measured one", func(t *testing.T) {
		route := routeLine(func(r *shared.RequestRecord) { r.LatencyMs = nil })
		merged := Combine(transcriptCall(nil), route, "req_011")
		if merged.LatencyMs == nil || *merged.LatencyMs != 1_951 {
			t.Errorf("latencyMs: got %s, want 1951", showInt(merged.LatencyMs))
		}
	})

	t.Run("carries the capture and headers the route line recorded", func(t *testing.T) {
		route := routeLine(func(r *shared.RequestRecord) {
			r.PromptHash = str("h1")
			r.Headers = &shared.Headers{{Name: "x-foo", Value: "bar"}}
		})
		merged := Combine(transcriptCall(nil), route, "req_011")
		if merged.PromptHash == nil || *merged.PromptHash != "h1" {
			t.Errorf("promptHash: got %v, want h1", merged.PromptHash)
		}
		if value, ok := merged.Headers.Get("x-foo"); !ok || value != "bar" {
			t.Errorf("headers: got %v, want x-foo=bar", merged.Headers)
		}
	})

	t.Run("keeps the model the route line fell back from, after the transcript's own tail", func(t *testing.T) {
		route := routeLine(func(r *shared.RequestRecord) { r.Fallback = str("glm-5.3") })
		merged := Combine(transcriptCall(nil), route, "req_011")
		if merged.Fallback == nil || *merged.Fallback != "glm-5.3" {
			t.Errorf("fallback: got %v, want glm-5.3", merged.Fallback)
		}
		if !slices.Contains(merged.TailKeys, "fallback") {
			t.Errorf("tail keys: got %v, want fallback among them", merged.TailKeys)
		}
	})

	t.Run("fills an empty upstream from the model's own provider", func(t *testing.T) {
		merged := Combine(
			transcriptCall(func(r *shared.RequestRecord) { r.Model = "glm-5.3" }),
			routeLine(func(r *shared.RequestRecord) { r.Upstream = "" }),
			"req_011",
		)
		if merged.Upstream != "https://api.z.ai" {
			t.Errorf("upstream: got %q, want %q", merged.Upstream, "https://api.z.ai")
		}
	})
}

func TestRefresh(t *testing.T) {
	t.Run("an unrouted record takes the later copy's latency, upstream, tokens and stop reason", func(t *testing.T) {
		merged := Refresh(
			makeRequest(func(r *shared.RequestRecord) {
				r.Upstream = ""
				r.Tokens = tokens(10, 5, 0, 0)
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.Model = "glm-5.3"
				r.LatencyMs = i64(300)
				r.Upstream = "api.z.ai"
				r.Tokens = tokens(1, 2, 0, 0)
				r.StopReason = str("end_turn")
				r.What = str("→ text")
			}),
		)
		if merged.Model != "glm-5.3" {
			t.Errorf("model: got %q, want %q", merged.Model, "glm-5.3")
		}
		if merged.LatencyMs == nil || *merged.LatencyMs != 300 {
			t.Errorf("latencyMs: got %s, want 300", showInt(merged.LatencyMs))
		}
		if merged.Upstream != "api.z.ai" {
			t.Errorf("upstream: got %q, want %q", merged.Upstream, "api.z.ai")
		}
		if want := tokens(1, 2, 0, 0); merged.Tokens != want {
			t.Errorf("tokens: got %+v, want %+v", merged.Tokens, want)
		}
		if merged.StopReason == nil || *merged.StopReason != "end_turn" {
			t.Errorf("stopReason: got %v, want end_turn", merged.StopReason)
		}
		if merged.What == nil || *merged.What != "→ text" {
			t.Errorf("what: got %v, want → text", merged.What)
		}
	})

	t.Run("a routed record keeps its own latency and upstream", func(t *testing.T) {
		merged := Refresh(
			makeRequest(func(r *shared.RequestRecord) {
				r.Route = str("provider")
				r.LatencyMs = i64(100)
				r.Upstream = "api.z.ai"
				r.Tokens = tokens(10, 5, 0, 0)
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.LatencyMs = i64(900)
				r.Upstream = "other.example"
				r.Tokens = tokens(0, 0, 0, 0)
				r.StopReason = nil
			}),
		)
		if merged.LatencyMs == nil || *merged.LatencyMs != 100 {
			t.Errorf("latencyMs: got %s, want 100", showInt(merged.LatencyMs))
		}
		if merged.Upstream != "api.z.ai" {
			t.Errorf("upstream: got %q, want %q", merged.Upstream, "api.z.ai")
		}
		if want := tokens(10, 5, 0, 0); merged.Tokens != want {
			t.Errorf("tokens: got %+v, want %+v", merged.Tokens, want)
		}
	})

	t.Run("an empty later copy changes no field", func(t *testing.T) {
		existing := makeRequest(func(r *shared.RequestRecord) {
			r.Tokens = tokens(10, 5, 0, 0)
			r.StopReason = str("tool_use")
			r.What = str("→ text")
		})
		merged := Refresh(existing, makeRequest(func(r *shared.RequestRecord) {
			r.Model = ""
			r.LatencyMs = nil
			r.Upstream = ""
			r.Tokens = shared.ZeroTokens
			r.StopReason = nil
		}))
		if !reflect.DeepEqual(merged, existing) {
			t.Errorf("merged: got %+v, want the record unchanged", merged)
		}
	})

	t.Run("totals and what land only when the later copy carries them", func(t *testing.T) {
		existing := makeRequest(nil)
		if merged := Refresh(existing, makeRequest(nil)); merged.Totals {
			t.Error("totals: got true, want the later copy's false to leave it unset")
		}
		totals := Refresh(existing, makeRequest(func(r *shared.RequestRecord) { r.Totals = true }))
		if !totals.Totals {
			t.Error("totals: got false, want true")
		}
	})
}
