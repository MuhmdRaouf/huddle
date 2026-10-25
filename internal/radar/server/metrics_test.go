package server

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/budget"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// inTokens and outTokens are the metric fixtures' counters: one million on one side.
var (
	inTokens  = shared.Tokens{Input: 1_000_000}
	outTokens = shared.Tokens{Output: 1_000_000}
)

func TestMetrics(t *testing.T) {
	t.Run("escapes labels and names providers", func(t *testing.T) {
		if got := EscapeLabel("a\"b\\c\nd"); got != `a\"b\\c\nd` {
			t.Errorf("EscapeLabel = %q, want %q", got, `a\"b\\c\nd`)
		}
		if got := ProviderSlug("glm-5.3"); got != "zai" {
			t.Errorf("ProviderSlug(glm-5.3) = %q, want zai", got)
		}
		if got := ProviderSlug("claude-opus-5"); got != "anthropic" {
			t.Errorf("ProviderSlug(claude-opus-5) = %q, want anthropic", got)
		}
		if got := ProviderSlug("gpt"); got != "other" {
			t.Errorf("ProviderSlug(gpt) = %q, want other", got)
		}
	})

	t.Run("renders every family in the text format", func(t *testing.T) {
		zai := "zai"
		text := RenderMetrics(MetricsInput{
			Version: "1.2.3",
			Ledger: []cost.LedgerRow{
				{ID: "a", Ts: 1, Model: "glm-5.3", SessionID: "s1", AgentID: "main", Tokens: inTokens},
				{ID: "b", Ts: 2, Model: "mystery-x", SessionID: "s2", AgentID: "main", Tokens: inTokens},
			},
			Names: cost.LedgerNames{
				Sessions: map[string]cost.SessionNames{
					"s1": {Project: strPtr(`my "app"`)},
				},
				Agents: map[string]cost.AgentNames{},
			},
			Requests: []shared.RequestRecord{
				makeRequest(setLatency(700)),
				makeRequest(func(r *shared.RequestRecord) { r.ID = "r2"; r.LatencyMs = nil }),
				makeRequest(func(r *shared.RequestRecord) {
					r.ID = "r4"
					r.Model = "glm-5.3"
					r.LatencyMs = int64Ptr(400_000)
				}),
			},
			Tools: []shared.ToolCallRecord{
				makeTool(nil),
				makeTool(func(t *shared.ToolCallRecord) { t.ID = "t2"; t.Ok = false }),
			},
			APIErrors: []shared.APIErrorRecord{
				{Ts: 1, Status: 429, Source: shared.SourceRouter, Plugin: &zai},
			},
			RouterEvents: []shared.RouterEventRecord{
				{Ts: 1, Plugin: "zai", Event: shared.EventFallback},
			},
			Alerts: nil,
			Sessions: []store.SessionListItem{
				{Live: true},
				{Live: false},
			},
			Budgets: []budget.BudgetSpend{
				{
					ID: "b1", SpentUsd: 1.5, LimitUsd: 10, Pct: 15,
					Scope: "total", Period: budget.PeriodDay, Action: budget.ActionWarn,
				},
			},
		})
		if !strings.HasSuffix(text, "\n") {
			t.Errorf("the exposition does not end in a newline: %q", text)
		}
		for _, want := range []string{
			`radar_info{version="1.2.3"} 1`,
			`radar_requests_total{model="glm-5.3",provider="zai",project="my \"app\""} 1`,
			`radar_cost_usd_total{model="glm-5.3",provider="zai",project="my \"app\""} 1.4`,
			`radar_request_latency_seconds_bucket{provider="anthropic",le="1"} 1`,
			`radar_request_latency_seconds_bucket{provider="zai",le="+Inf"} 1`,
			`radar_request_latency_seconds_count{provider="zai"} 1`,
			`radar_tool_calls_total{tool="Bash",result="error"} 1`,
			`radar_api_errors_total{status="429",source="router"} 1`,
			`radar_router_events_total{provider="zai",event="fallback"} 1`,
			`radar_sessions{state="ended"} 1`,
			`radar_alerts{kind="stuck"} 0`,
			`radar_alerts{kind="quota_nearly_used"} 0`,
			`radar_budget_spent_usd{budget="b1",scope="total",period="day"} 1.5`,
			`# TYPE radar_request_latency_seconds histogram`,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("the exposition is missing %q\ntext:\n%s", want, text)
			}
		}
		if strings.Contains(text, `radar_cost_usd_total{model="mystery-x"`) {
			t.Error("an unpriced model has a cost line")
		}
	})
}
