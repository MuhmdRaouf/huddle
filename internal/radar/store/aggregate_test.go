package store

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

func tokens(input, output, cacheRead, cacheWrite int64) shared.Tokens {
	return shared.Tokens{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite}
}

func TestPercentile(t *testing.T) {
	t.Run("returns null for no data", func(t *testing.T) {
		if got := Percentile(nil, 50); got != nil {
			t.Errorf("Percentile([], 50): got %s, want null", showInt(got))
		}
	})

	t.Run("interpolates between ranks", func(t *testing.T) {
		cases := []struct {
			values []int64
			p      float64
			want   int64
			note   string
		}{
			{values: []int64{5}, p: 50, want: 5},
			{values: []int64{4, 1, 3, 2}, p: 50, want: 3, note: "rank 1.5 → 2.5 → 3"},
			{values: []int64{4, 1, 3, 2}, p: 0, want: 1},
			{values: []int64{4, 1, 3, 2}, p: 95, want: 4, note: "rank 2.85 → 3.85 → 4"},
			{values: []int64{4, 1, 3, 2}, p: 100, want: 4},
		}
		for _, c := range cases {
			if got := Percentile(c.values, c.p); got == nil || *got != c.want {
				t.Errorf("Percentile(%v, %v): got %s, want %d (%s)", c.values, c.p, showInt(got), c.want, c.note)
			}
		}
	})
}

func TestMeanOf(t *testing.T) {
	t.Run("averages and rounds, or bows out on no data", func(t *testing.T) {
		if got := MeanOf(nil); got != nil {
			t.Errorf("MeanOf([]): got %s, want null", showInt(got))
		}
		if got := MeanOf([]int64{1, 2}); got == nil || *got != 2 {
			t.Errorf("MeanOf([1, 2]): got %s, want 2 (1.5 rounds away from zero)", showInt(got))
		}
		if got := MeanOf([]int64{10, 20, 31}); got == nil || *got != 20 {
			t.Errorf("MeanOf([10, 20, 31]): got %s, want 20", showInt(got))
		}
	})
}

func TestSummarize(t *testing.T) {
	t.Run("counts sessions, live sessions, agents, requests, tools and errors", func(t *testing.T) {
		summary := Summarize(
			[]shared.SessionView{
				makeSessionView(func(v *shared.SessionView) {
					v.ID = "a"
					v.Live = true
					v.StartedAt = i64(1_000)
					v.Agents = []shared.AgentView{makeAgentView(nil)}
				}),
				makeSessionView(func(v *shared.SessionView) {
					v.ID = "b"
					v.Live = false
					v.StartedAt = i64(2_000)
					v.Agents = []shared.AgentView{
						makeAgentView(func(a *shared.AgentView) { a.ID = "w1" }),
						makeAgentView(func(a *shared.AgentView) { a.ID = "w2" }),
					}
				}),
			},
			held([]shared.RequestRecord{
				makeRequest(func(r *shared.RequestRecord) {
					r.ID = "r1"
					r.Ts = 1_200
					r.LatencyMs = i64(100)
					r.Tokens = tokens(10, 5, 2, 1)
				}),
				makeRequest(func(r *shared.RequestRecord) {
					r.ID = "r2"
					r.Ts = 1_400
					r.LatencyMs = i64(300)
					r.Tokens = tokens(7, 3, 0, 0)
					r.StopReason = str("error")
				}),
				makeRequest(func(r *shared.RequestRecord) {
					r.ID = "r3"
					r.Ts = 1_600
					r.LatencyMs = nil
					r.Tokens = shared.ZeroTokens
				}),
			}),
			heldTools([]shared.ToolCallRecord{
				makeTool(nil),
				makeTool(func(tool *shared.ToolCallRecord) { tool.ID = "t2"; tool.Ok = false }),
			}),
			2_000,
		)
		if summary.Sessions != 2 {
			t.Errorf("sessions: got %d, want 2", summary.Sessions)
		}
		if summary.LiveSessions != 1 {
			t.Errorf("liveSessions: got %d, want 1", summary.LiveSessions)
		}
		if summary.Agents != 3 {
			t.Errorf("agents: got %d, want 3", summary.Agents)
		}
		if summary.Requests != 3 {
			t.Errorf("requests: got %d, want 3", summary.Requests)
		}
		if summary.ToolCalls != 2 {
			t.Errorf("toolCalls: got %d, want 2", summary.ToolCalls)
		}
		if summary.Errors != 2 { // one failed tool + one error stop reason
			t.Errorf("errors: got %d, want 2", summary.Errors)
		}
		if want := tokens(17, 8, 2, 1); summary.Tokens != want {
			t.Errorf("tokens: got %+v, want %+v", summary.Tokens, want)
		}
		if summary.LatencyP50 == nil || *summary.LatencyP50 != 200 { // interpolated between 100 and 300
			t.Errorf("latencyP50: got %s, want 200", showInt(summary.LatencyP50))
		}
		if summary.LatencyP95 == nil || *summary.LatencyP95 != 290 { // 100 + (300 - 100) * 0.95
			t.Errorf("latencyP95: got %s, want 290", showInt(summary.LatencyP95))
		}
		if summary.StartedAt == nil || *summary.StartedAt != 1_000 {
			t.Errorf("startedAt: got %s, want 1000", showInt(summary.StartedAt))
		}
		if summary.Now != 2_000 {
			t.Errorf("now: got %d, want 2000", summary.Now)
		}
	})

	t.Run("keeps nulls honest when there is nothing to measure", func(t *testing.T) {
		summary := Summarize(nil, nil, nil, 5_000)
		if summary.StartedAt != nil {
			t.Errorf("startedAt: got %s, want null", showInt(summary.StartedAt))
		}
		if summary.LatencyP50 != nil {
			t.Errorf("latencyP50: got %s, want null", showInt(summary.LatencyP50))
		}
		if summary.LatencyP95 != nil {
			t.Errorf("latencyP95: got %s, want null", showInt(summary.LatencyP95))
		}
		if summary.Errors != 0 {
			t.Errorf("errors: got %d, want 0", summary.Errors)
		}
		if summary.Tokens != shared.ZeroTokens {
			t.Errorf("tokens: got %+v, want %+v", summary.Tokens, shared.ZeroTokens)
		}
	})

	t.Run("ignores startedAt nulls and only counts stop reasons in the error set", func(t *testing.T) {
		summary := Summarize(
			[]shared.SessionView{
				makeSessionView(func(v *shared.SessionView) { v.ID = "a"; v.StartedAt = nil }),
			},
			held([]shared.RequestRecord{
				makeRequest(func(r *shared.RequestRecord) { r.StopReason = str("end_turn") }),
				makeRequest(func(r *shared.RequestRecord) { r.StopReason = str("tool_use") }),
				makeRequest(func(r *shared.RequestRecord) { r.StopReason = str("api_error") }),
				makeRequest(func(r *shared.RequestRecord) { r.StopReason = nil }),
			}),
			nil,
			0,
		)
		if summary.StartedAt != nil {
			t.Errorf("startedAt: got %s, want null", showInt(summary.StartedAt))
		}
		if summary.Errors != 1 {
			t.Errorf("errors: got %d, want 1", summary.Errors)
		}
	})
}

func TestModelRows(t *testing.T) {
	t.Run("groups by model with provider, errors, tokens and p50, biggest total first", func(t *testing.T) {
		rows := ModelRows(held([]shared.RequestRecord{
			makeRequest(func(r *shared.RequestRecord) {
				r.Model = "claude-sonnet-5-5"
				r.Tokens = tokens(10, 0, 0, 0)
				r.LatencyMs = i64(100)
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.Model = "claude-sonnet-5-5"
				r.Tokens = tokens(0, 10, 0, 0)
				r.LatencyMs = i64(300)
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.Model = "glm-5.3"
				r.Tokens = tokens(100, 0, 0, 0)
				r.LatencyMs = i64(50)
				r.StopReason = str("overloaded_error")
			}),
		}))
		if len(rows) != 2 {
			t.Fatalf("rows length: got %d, want 2", len(rows))
		}
		if rows[0].Model != "glm-5.3" {
			t.Errorf("rows[0].model: got %q, want %q", rows[0].Model, "glm-5.3")
		}
		if rows[0].Provider != "Z.ai" {
			t.Errorf("rows[0].provider: got %q, want %q", rows[0].Provider, "Z.ai")
		}
		if rows[0].Errors != 1 {
			t.Errorf("rows[0].errors: got %d, want 1", rows[0].Errors)
		}
		if rows[0].Requests != 1 {
			t.Errorf("rows[0].requests: got %d, want 1", rows[0].Requests)
		}
		if rows[1].Model != "claude-sonnet-5-5" {
			t.Errorf("rows[1].model: got %q, want %q", rows[1].Model, "claude-sonnet-5-5")
		}
		if rows[1].Provider != "Anthropic" {
			t.Errorf("rows[1].provider: got %q, want %q", rows[1].Provider, "Anthropic")
		}
		if rows[1].Errors != 0 {
			t.Errorf("rows[1].errors: got %d, want 0", rows[1].Errors)
		}
		if rows[1].Requests != 2 {
			t.Errorf("rows[1].requests: got %d, want 2", rows[1].Requests)
		}
		if want := tokens(10, 10, 0, 0); rows[1].Tokens != want {
			t.Errorf("rows[1].tokens: got %+v, want %+v", rows[1].Tokens, want)
		}
		if rows[1].LatencyP50 == nil || *rows[1].LatencyP50 != 200 {
			t.Errorf("rows[1].latencyP50: got %s, want 200", showInt(rows[1].LatencyP50))
		}
	})

	t.Run("returns nothing for no requests", func(t *testing.T) {
		if rows := ModelRows(nil); len(rows) != 0 {
			t.Errorf("rows: got %v, want none", rows)
		}
	})
}

func TestUpstreamRows(t *testing.T) {
	t.Run("groups by upstream with a host column, biggest total first", func(t *testing.T) {
		rows := UpstreamRows(held([]shared.RequestRecord{
			makeRequest(func(r *shared.RequestRecord) {
				r.Upstream = "https://api.anthropic.com"
				r.Tokens = tokens(5, 0, 0, 0)
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.Upstream = "http://localhost:8787/v1"
				r.Tokens = tokens(50, 0, 0, 0)
			}),
			makeRequest(func(r *shared.RequestRecord) {
				r.Upstream = "http://localhost:8787/v1"
				r.Tokens = tokens(0, 50, 0, 0)
			}),
		}))
		if len(rows) != 2 {
			t.Fatalf("rows length: got %d, want 2", len(rows))
		}
		if rows[0].Host != "localhost:8787" {
			t.Errorf("rows[0].host: got %q, want %q", rows[0].Host, "localhost:8787")
		}
		if rows[0].Upstream != "http://localhost:8787/v1" {
			t.Errorf("rows[0].upstream: got %q, want %q", rows[0].Upstream, "http://localhost:8787/v1")
		}
		if rows[0].Requests != 2 {
			t.Errorf("rows[0].requests: got %d, want 2", rows[0].Requests)
		}
		if want := tokens(50, 50, 0, 0); rows[0].Tokens != want {
			t.Errorf("rows[0].tokens: got %+v, want %+v", rows[0].Tokens, want)
		}
		if rows[1].Host != "api.anthropic.com" {
			t.Errorf("rows[1].host: got %q, want %q", rows[1].Host, "api.anthropic.com")
		}
	})
}

func TestToolCounts(t *testing.T) {
	t.Run("counts per name, most used first, names breaking ties", func(t *testing.T) {
		counts := ToolCounts(heldTools([]shared.ToolCallRecord{
			makeTool(nil),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Ok = false }),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "Read" }),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "Read" }),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "Grep" }),
		}))
		want := []ToolCountRow{
			{Name: "Bash", Count: 2, Failures: 1},
			{Name: "Read", Count: 2, Failures: 0},
			{Name: "Grep", Count: 1, Failures: 0},
		}
		if len(counts) != len(want) {
			t.Fatalf("length: got %d, want %d", len(counts), len(want))
		}
		for i := range want {
			if counts[i] != want[i] {
				t.Errorf("counts[%d]: got %+v, want %+v", i, counts[i], want[i])
			}
		}
	})

	t.Run("breaks count ties the way localeCompare does, not by bytes", func(t *testing.T) {
		counts := ToolCounts(heldTools([]shared.ToolCallRecord{
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "TodoWrite" }),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "todoWrite" }),
		}))
		// one call each: localeCompare weighs case behind the letters, so the lowercase
		// name leads where byte order would put the capital first
		if counts[0].Name != "todoWrite" || counts[1].Name != "TodoWrite" {
			t.Errorf("tie order = %s then %s, want todoWrite then TodoWrite", counts[0].Name, counts[1].Name)
		}
	})

	t.Run("keeps the unmatched results out: a call named unknown is not a tool", func(t *testing.T) {
		// records read from a later offset used to land as "unknown"; whatever still sits in a
		// store must not reach the Top tools ranking under that name
		counts := ToolCounts(heldTools([]shared.ToolCallRecord{
			makeTool(nil),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "unknown"; tool.Ok = false }),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "unknown" }),
			makeTool(func(tool *shared.ToolCallRecord) { tool.Name = "Read" }),
		}))
		want := []ToolCountRow{
			{Name: "Bash", Count: 1, Failures: 0},
			{Name: "Read", Count: 1, Failures: 0},
		}
		if len(counts) != len(want) {
			t.Fatalf("length: got %d, want %d", len(counts), len(want))
		}
		for i := range want {
			if counts[i] != want[i] {
				t.Errorf("counts[%d]: got %+v, want %+v", i, counts[i], want[i])
			}
		}
	})
}
