package alerts

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/budget"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

const (
	minMs = 60_000
	now   = 1_800_000_000_000
)

func ptr[T any](value T) *T { return &value }

// session is one live session's card, the shape the live rail serves.
func session() store.SessionListItem {
	project, cwd := "app", "/w/app"
	cost := shared.USD(0.5)
	counts := make([]int64, shared.ActivityBuckets)
	models := make([]string, shared.ActivityBuckets)
	return store.SessionListItem{
		ID:             "s1",
		Project:        &project,
		Cwd:            &cwd,
		StartedAt:      ptr(int64(now - 60*minMs)),
		Live:           true,
		Activity:       shared.Activity{BucketMs: shared.ActivityLiveBucketMs, Counts: counts, Models: models},
		AgentCount:     1,
		LiveAgentCount: 0,
		RequestCount:   1,
		Tokens:         0,
		LastAt:         now,
		External:       false,
		CostUsd:        &cost,
	}
}

var eventSeq int64

// event is one timeline hook line for session s1.
func event(kind string, ts int64) shared.EventRecord {
	eventSeq++
	sid := "s1"
	return shared.EventRecord{Seq: eventSeq, Ts: ts, Kind: kind, SessionID: &sid}
}

// makeRequest is one model request, main agent, 10 input tokens.
func makeRequest() shared.RequestRecord {
	return shared.RequestRecord{
		ID:         "req-1",
		SessionID:  "s1",
		AgentID:    "main",
		Model:      "claude-sonnet-5-5",
		Upstream:   "https://api.anthropic.com",
		Ts:         1_000,
		LatencyMs:  ptr(int64(100)),
		Tokens:     shared.Tokens{Input: 10, Output: 5},
		StopReason: nil,
		Provider:   "Anthropic",
	}
}

// makeTool is one Bash tool call for session s1's main agent.
func makeTool() shared.ToolCallRecord {
	return shared.ToolCallRecord{
		ID:         "tool-1",
		SessionID:  "s1",
		AgentID:    ptr("main"),
		Name:       "Bash",
		StartedAt:  1_000,
		DurationMs: ptr(int64(50)),
		Ok:         true,
	}
}

// input is one live session and nothing else happening.
func input() AlertInput {
	return AlertInput{Now: now, Sessions: []store.SessionListItem{session()}}
}

// detect runs the engine at its default thresholds.
func detect(in AlertInput) []shared.Alert {
	return DetectAlerts(in, DefaultThresholds)
}

// kinds lists the alerts' kinds.
func kinds(alerts []shared.Alert) []string {
	out := make([]string, len(alerts))
	for i, alert := range alerts {
		out[i] = string(alert.Kind)
	}
	return out
}

// wantKinds fails unless the alerts are exactly these kinds.
func wantKinds(t *testing.T, alerts []shared.Alert, want ...string) {
	t.Helper()
	got := kinds(alerts)
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v (alerts: %+v)", got, want, alerts)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v (alerts: %+v)", got, want, alerts)
		}
	}
}

// wantNoAlerts fails unless the engine stayed quiet.
func wantNoAlerts(t *testing.T, alerts []shared.Alert) {
	t.Helper()
	if len(alerts) != 0 {
		t.Fatalf("alerts = %+v, want none", alerts)
	}
}

func TestStuck(t *testing.T) {
	t.Run("fires mid-turn after 10 quiet minutes", func(t *testing.T) {
		request := makeRequest()
		request.Ts = now - 12*minMs
		in := input()
		in.Events = []shared.EventRecord{event("UserPromptSubmit", now-30*minMs)}
		in.Requests = []shared.RequestRecord{request}
		alerts := detect(in)
		if len(alerts) != 1 {
			t.Fatalf("alerts = %+v, want one", alerts)
		}
		got := alerts[0]
		if got.Kind != shared.AlertStuck || got.SessionID != "s1" || got.AgentID != nil ||
			got.Project != "app" || got.Since != now-12*minMs || got.Severity != shared.SeverityErr {
			t.Errorf("alert = %+v", got)
		}
		if got.CostUsd == nil || *got.CostUsd != shared.USD(0.5) {
			t.Errorf("costUsd = %v, want 0.5", got.CostUsd)
		}
		if !strings.Contains(got.Detail, "12 min") {
			t.Errorf("detail %q does not hold \"12 min\"", got.Detail)
		}
	})

	t.Run("waits 30 minutes while a tool is running, and names a subagent", func(t *testing.T) {
		toolRun := func(ago int64) []shared.Alert {
			request := makeRequest()
			request.Ts = now - ago*minMs
			request.StopReason = ptr("tool_use")
			request.AgentID = "w1"
			in := input()
			in.Events = []shared.EventRecord{event("UserPromptSubmit", now-50*minMs)}
			in.Requests = []shared.RequestRecord{request}
			return detect(in)
		}
		wantNoAlerts(t, toolRun(20))
		alerts := toolRun(31)
		if len(alerts) != 1 || alerts[0].Kind != shared.AlertStuck {
			t.Fatalf("alerts = %+v, want one stuck", alerts)
		}
		if alerts[0].AgentID == nil || *alerts[0].AgentID != "w1" {
			t.Errorf("agentId = %v, want w1", alerts[0].AgentID)
		}
		if !strings.Contains(alerts[0].Detail, "tool has been running") {
			t.Errorf("detail %q does not hold \"tool has been running\"", alerts[0].Detail)
		}
	})

	t.Run("stays quiet when the session waits for the user", func(t *testing.T) {
		quiet := makeRequest()
		quiet.Ts = now - 20*minMs
		ended := makeRequest()
		ended.Ts = now - 20*minMs
		ended.StopReason = ptr("end_turn")
		recent := makeRequest()
		recent.Ts = now - 5*minMs
		abandoned := makeRequest()
		abandoned.Ts = now - 7*60*minMs
		prompt := event("UserPromptSubmit", now-30*minMs)
		dead := session()
		dead.Live = false
		external := session()
		external.External = true
		cases := []func(in *AlertInput){
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{prompt, event("Stop", now-19*minMs)}
				in.Requests = []shared.RequestRecord{quiet}
			},
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{prompt, event("Interrupted", now-19*minMs)}
				in.Requests = []shared.RequestRecord{quiet}
			},
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{prompt, event("Notification", now-19*minMs)}
				in.Requests = []shared.RequestRecord{quiet}
			},
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{prompt}
				in.Requests = []shared.RequestRecord{ended}
			},
			func(in *AlertInput) { in.Requests = []shared.RequestRecord{quiet} },
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{prompt}
				in.Requests = []shared.RequestRecord{quiet}
				in.Sessions = []store.SessionListItem{dead}
			},
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{prompt}
				in.Requests = []shared.RequestRecord{quiet}
				in.Sessions = []store.SessionListItem{external}
			},
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{event("UserPromptSubmit", now-7*60*minMs)}
				in.Requests = []shared.RequestRecord{abandoned}
			},
			func(in *AlertInput) {
				in.Events = []shared.EventRecord{prompt}
				in.Requests = []shared.RequestRecord{recent}
			},
		}
		for index, build := range cases {
			in := input()
			build(&in)
			if alerts := detect(in); len(alerts) != 0 {
				t.Errorf("case %d: alerts = %+v, want none", index, alerts)
			}
		}
	})

	t.Run("without hook lines, only a request that asked for a tool keeps the turn open", func(t *testing.T) {
		request := makeRequest()
		request.Ts = now - 40*minMs
		request.StopReason = ptr("tool_use")
		in := input()
		in.Requests = []shared.RequestRecord{request}
		wantKinds(t, detect(in), "stuck")
	})

	t.Run("counts tool results and agent events as activity", func(t *testing.T) {
		request := makeRequest()
		request.Ts = now - 20*minMs
		tool := makeTool()
		tool.StartedAt = now - 20*minMs
		tool.DurationMs = ptr(int64(19 * minMs))
		in := input()
		in.Events = []shared.EventRecord{event("UserPromptSubmit", now-30*minMs), event("SubagentStart", now-2*minMs)}
		in.Requests = []shared.RequestRecord{request}
		in.Tools = []shared.ToolCallRecord{tool}
		wantNoAlerts(t, detect(in))
	})
}

func TestLoop(t *testing.T) {
	// call is one tool call inputKey minutes ago; over mutates it further.
	call := func(i int, key string, over func(*shared.ToolCallRecord)) shared.ToolCallRecord {
		tool := makeTool()
		tool.ID = fmt.Sprintf("t%d", i)
		tool.StartedAt = now - int64(10-i)*minMs
		tool.InputKey = &key
		if over != nil {
			over(&tool)
		}
		return tool
	}

	t.Run("finds the trailing run of identical calls", func(t *testing.T) {
		ids := func(calls []shared.ToolCallRecord) []string {
			out := make([]string, len(calls))
			for i, call := range calls {
				out[i] = call.ID
			}
			return out
		}
		if streak := TrailingStreak(nil); len(streak) != 0 {
			t.Errorf("TrailingStreak(nil) = %v, want empty", ids(streak))
		}
		if streak := TrailingStreak([]shared.ToolCallRecord{makeTool()}); len(streak) != 0 {
			t.Errorf("TrailingStreak(no input key) = %v, want empty", ids(streak))
		}
		streak := TrailingStreak([]shared.ToolCallRecord{call(1, "a", nil), call(2, "k", nil), call(3, "k", nil)})
		if fmt.Sprint(ids(streak)) != fmt.Sprint([]string{"t2", "t3"}) {
			t.Errorf("streak = %v, want [t2 t3]", ids(streak))
		}
		mixed := TrailingStreak([]shared.ToolCallRecord{
			call(1, "k", func(tool *shared.ToolCallRecord) { tool.Name = "Read" }),
			call(2, "k", nil),
		})
		if fmt.Sprint(ids(mixed)) != fmt.Sprint([]string{"t2"}) {
			t.Errorf("streak = %v, want [t2]", ids(mixed))
		}
	})

	t.Run("fires at five identical calls in a row, recent ones only, per agent", func(t *testing.T) {
		five := make([]shared.ToolCallRecord, 0, 5)
		for i := 1; i <= 5; i++ {
			five = append(five, call(i, "k", nil))
		}
		in := input()
		in.Tools = five
		alerts := detect(in)
		if len(alerts) != 1 {
			t.Fatalf("alerts = %+v, want one", alerts)
		}
		got := alerts[0]
		if got.Kind != shared.AlertLoop || got.Since != now-9*minMs || got.AgentID != nil ||
			got.Severity != shared.SeverityWarn {
			t.Errorf("alert = %+v", got)
		}
		if got.Detail != "Bash called 5 times in a row with the same input" {
			t.Errorf("detail = %q", got.Detail)
		}
		shorter := input()
		shorter.Tools = five[1:]
		wantNoAlerts(t, detect(shorter))
		sub := input()
		sub.Tools = make([]shared.ToolCallRecord, 0, 5)
		for _, tool := range five {
			tool.AgentID = ptr("w1")
			sub.Tools = append(sub.Tools, tool)
		}
		subAlerts := detect(sub)
		if len(subAlerts) != 1 || subAlerts[0].AgentID == nil || *subAlerts[0].AgentID != "w1" {
			t.Fatalf("alerts = %+v, want one for w1", subAlerts)
		}
		stale := input()
		stale.Tools = five
		stale.Now = now + 40*minMs
		wantNoAlerts(t, detect(stale))
		broken := input()
		broken.Tools = append(slices.Clone(five), call(6, "other", nil))
		wantNoAlerts(t, detect(broken))
	})
}

func TestContext(t *testing.T) {
	// big is one request inputTokens tokens full, a minute ago; over mutates it further.
	big := func(inputTokens int64, over func(*shared.RequestRecord)) *shared.RequestRecord {
		request := makeRequest()
		request.Ts = now - minMs
		request.Tokens = shared.Tokens{Input: inputTokens}
		if over != nil {
			over(&request)
		}
		return &request
	}

	t.Run("knows a session's window", func(t *testing.T) {
		if got := shared.ContextWindow([]*shared.RequestRecord{big(10, nil)}); got != 200_000 {
			t.Errorf("window = %d, want 200000", got)
		}
		if got := shared.ContextWindow([]*shared.RequestRecord{
			big(10, func(request *shared.RequestRecord) { request.Model = "claude-opus-5-5[1m]" }),
		}); got != 1_000_000 {
			t.Errorf("window = %d, want 1000000", got)
		}
		if got := shared.ContextWindow([]*shared.RequestRecord{big(300_000, nil)}); got != 1_000_000 {
			t.Errorf("window = %d, want 1000000", got)
		}
	})

	t.Run("fires at 85% of the window until a compaction", func(t *testing.T) {
		at85 := input()
		at85.Requests = []shared.RequestRecord{*big(171_000, nil)}
		wantKinds(t, detect(at85), "context")

		below := input()
		below.Requests = []shared.RequestRecord{*big(160_000, nil)}
		wantNoAlerts(t, detect(below))

		compacted := input()
		compacted.Requests = []shared.RequestRecord{*big(171_000, nil)}
		compacted.Events = []shared.EventRecord{event("PreCompact", now-10)}
		wantNoAlerts(t, detect(compacted))

		ended := input()
		ended.Requests = []shared.RequestRecord{*big(171_000, nil)}
		ended.Sessions = func() []store.SessionListItem {
			s := session()
			s.Live = false
			return []store.SessionListItem{s}
		}()
		wantNoAlerts(t, detect(ended))

		sub := input()
		sub.Requests = []shared.RequestRecord{*big(171_000, func(request *shared.RequestRecord) { request.AgentID = "w1" })}
		subAlerts := detect(sub)
		if len(subAlerts) != 1 || subAlerts[0].AgentID == nil || *subAlerts[0].AgentID != "w1" {
			t.Fatalf("alerts = %+v, want one for w1", subAlerts)
		}
		const wantDetail = "171k of 200k tokens (86%)"
		if !strings.Contains(subAlerts[0].Detail, wantDetail) {
			t.Errorf("detail %q does not hold %q", subAlerts[0].Detail, wantDetail)
		}
	})

	t.Run("skips a request holding a run total: a job's sum is not one call's context", func(t *testing.T) {
		runTotal := makeRequest()
		runTotal.Ts = now - 2*minMs
		runTotal.Model = "glm-5.3-flash"
		runTotal.Tokens = shared.Tokens{Input: 2_000_000, Output: 10_000, CacheRead: 574_000}
		runTotal.Totals = true
		onlyTotal := input()
		onlyTotal.Requests = []shared.RequestRecord{runTotal}
		wantNoAlerts(t, detect(onlyTotal))
		if got := shared.ContextWindow([]*shared.RequestRecord{&runTotal}); got != 200_000 {
			t.Errorf("window = %d, want 200000: the run total does not make the window 1M", got)
		}
		// a per-call request at 90% of the window still alerts, alongside the run total
		both := input()
		both.Requests = []shared.RequestRecord{runTotal, *big(180_000, func(request *shared.RequestRecord) {
			request.Ts = now - 1000
		})}
		alerts := detect(both)
		wantKinds(t, alerts, "context")
		const wantDetail = "180k of 200k tokens (90%)"
		if !strings.Contains(alerts[0].Detail, wantDetail) {
			t.Errorf("detail %q does not hold %q", alerts[0].Detail, wantDetail)
		}
	})
}

func TestRetryStorm(t *testing.T) {
	// failure is one retryable answer ago milliseconds ago; over mutates it further.
	failure := func(ago int64, over func(*shared.APIErrorRecord)) shared.APIErrorRecord {
		record := shared.APIErrorRecord{
			Ts:        now - ago,
			SessionID: ptr("s1"),
			Status:    429,
			Source:    shared.SourceTranscript,
		}
		if over != nil {
			over(&record)
		}
		return record
	}

	t.Run("fires at five failures within two minutes, per session", func(t *testing.T) {
		burst := make([]shared.APIErrorRecord, 0, 5)
		for s := int64(0); s < 5; s++ {
			seconds := s
			burst = append(burst, failure(3*minMs-seconds*1000, func(record *shared.APIErrorRecord) {
				if seconds == 0 {
					record.Status = 529
				}
			}))
		}
		in := input()
		in.APIErrors = burst
		alerts := detect(in)
		if len(alerts) != 1 || alerts[0].Kind != shared.AlertRetryStorm || alerts[0].SessionID != "s1" ||
			alerts[0].Project != "app" {
			t.Fatalf("alerts = %+v, want one retry storm for s1", alerts)
		}
		if alerts[0].Detail != "5 rate limits or server errors (429, 529) within 2 min" {
			t.Errorf("detail = %q", alerts[0].Detail)
		}
		shorter := input()
		shorter.APIErrors = burst[1:]
		wantNoAlerts(t, detect(shorter))
	})

	t.Run("does not count failures spread out, stale or from the future", func(t *testing.T) {
		spread := make([]shared.APIErrorRecord, 0, 5)
		for i := int64(0); i < 5; i++ {
			spread = append(spread, failure(i*3*minMs, nil))
		}
		spreadIn := input()
		spreadIn.APIErrors = spread
		wantNoAlerts(t, detect(spreadIn))
		stale := make([]shared.APIErrorRecord, 0, 5)
		for i := int64(0); i < 5; i++ {
			stale = append(stale, failure(20*minMs+i*1000, nil))
		}
		staleIn := input()
		staleIn.APIErrors = stale
		wantNoAlerts(t, detect(staleIn))
		future := make([]shared.APIErrorRecord, 0, 5)
		for i := int64(0); i < 5; i++ {
			future = append(future, failure(-minMs-i, nil))
		}
		futureIn := input()
		futureIn.APIErrors = future
		wantNoAlerts(t, detect(futureIn))
	})

	t.Run("groups a router's failures under its provider when no session is known", func(t *testing.T) {
		burst := make([]shared.APIErrorRecord, 0, 5)
		for i := int64(1); i <= 5; i++ {
			burst = append(burst, failure(i*1000, func(record *shared.APIErrorRecord) {
				record.SessionID = nil
				plugin := "zai"
				record.Plugin = &plugin
				record.Source = shared.SourceRouter
			}))
		}
		in := input()
		in.APIErrors = burst
		alerts := detect(in)
		if len(alerts) != 1 || alerts[0].SessionID != "" || alerts[0].Project != "" || alerts[0].CostUsd != nil {
			t.Fatalf("alerts = %+v, want one router retry storm with no session", alerts)
		}
		if !strings.HasSuffix(alerts[0].Detail, " at the Z.ai router") {
			t.Errorf("detail %q does not end \" at the Z.ai router\"", alerts[0].Detail)
		}
		unknown := input()
		unknown.APIErrors = make([]shared.APIErrorRecord, 0, 5)
		for _, record := range burst {
			record.Plugin = nil
			unknown.APIErrors = append(unknown.APIErrors, record)
		}
		unknownAlerts := detect(unknown)
		if len(unknownAlerts) != 1 || !strings.Contains(unknownAlerts[0].ID, "router:unknown") {
			t.Fatalf("alerts = %+v, want one under router:unknown", unknownAlerts)
		}
	})
}

func TestBudgetAlerts(t *testing.T) {
	// spend is one budget pct percent spent.
	spend := func(pct float64, action budget.Action) budget.BudgetSpend {
		return budget.BudgetSpend{
			ID:          "b1",
			SpentUsd:    shared.USD(pct / 10),
			LimitUsd:    shared.USD(10),
			Pct:         budget.Percent(pct),
			Scope:       "provider:zai",
			Period:      budget.PeriodMonth,
			Action:      action,
			PeriodStart: 5,
		}
	}

	t.Run("warns from 80% and turns loud at 100%, with a new id for each threshold", func(t *testing.T) {
		below := input()
		below.Budgets = []budget.BudgetSpend{spend(79, budget.ActionWarn)}
		wantNoAlerts(t, detect(below))
		nearIn := input()
		nearIn.Budgets = []budget.BudgetSpend{spend(84, budget.ActionStop)}
		near := detect(nearIn)
		if len(near) != 1 {
			t.Fatalf("alerts = %+v, want one", near)
		}
		if near[0].Kind != shared.AlertBudget || near[0].Severity != shared.SeverityWarn ||
			near[0].ID != "budget:b1:5:80" {
			t.Errorf("alert = %+v", near[0])
		}
		if near[0].CostUsd == nil || *near[0].CostUsd != shared.USD(8.4) {
			t.Errorf("costUsd = %v, want 8.4", near[0].CostUsd)
		}
		if near[0].Detail != "Z.ai monthly budget at 84% ($8.40 of $10.00), stops at 100%" {
			t.Errorf("detail = %q", near[0].Detail)
		}
		overIn := input()
		overIn.Budgets = []budget.BudgetSpend{spend(100, budget.ActionStop)}
		over := detect(overIn)
		if len(over) != 1 || over[0].Severity != shared.SeverityErr || over[0].ID != "budget:b1:5:100" {
			t.Fatalf("alerts = %+v, want one loud at the new threshold", over)
		}
		if !strings.HasSuffix(over[0].Detail, "requests are stopped") {
			t.Errorf("detail %q does not end \"requests are stopped\"", over[0].Detail)
		}
		warnOnlyIn := input()
		warnOnlyIn.Budgets = []budget.BudgetSpend{spend(120, budget.ActionWarn)}
		warnOnly := detect(warnOnlyIn)
		if len(warnOnly) != 1 || !strings.HasSuffix(warnOnly[0].Detail, "warn only") {
			t.Fatalf("alerts = %+v, want one warn-only", warnOnly)
		}
	})

	t.Run("puts loud alerts first, then the newest", func(t *testing.T) {
		tools := make([]shared.ToolCallRecord, 0, 5)
		for i := int64(1); i <= 5; i++ {
			tool := makeTool()
			tool.ID = fmt.Sprintf("t%d", i)
			tool.StartedAt = now - i*1000
			key := "k"
			tool.InputKey = &key
			tools = append(tools, tool)
		}
		stuck := makeRequest()
		stuck.SessionID = "s2"
		stuck.Ts = now - 40*minMs
		stuck.StopReason = ptr("tool_use")
		second := session()
		second.ID = "s2"
		in := input()
		in.Sessions = []store.SessionListItem{session(), second}
		in.Budgets = []budget.BudgetSpend{spend(85, budget.ActionWarn)}
		in.Tools = tools
		in.Requests = []shared.RequestRecord{stuck}
		wantKinds(t, detect(in), "stuck", "loop", "budget")
		if DefaultThresholds.LoopRepeats != 5 {
			t.Errorf("loopRepeats = %d, want 5", DefaultThresholds.LoopRepeats)
		}
	})
}

// TestAlertJSONKeepsTheLiteralsOrder pins each alert kind's JSON to its fixed key order: the
// sessionAlert literal puts kind first and costUsd before id, the budget literal puts id
// first and costUsd after detail.
func TestAlertJSONKeepsTheLiteralsOrder(t *testing.T) {
	stuckInput := input()
	stuckRequest := makeRequest()
	stuckRequest.Ts = now - 12*minMs
	stuckInput.Events = []shared.EventRecord{event("UserPromptSubmit", now-30*minMs)}
	stuckInput.Requests = []shared.RequestRecord{stuckRequest}
	loopTools := make([]shared.ToolCallRecord, 0, 5)
	for i := 1; i <= 5; i++ {
		tool := makeTool()
		tool.ID = fmt.Sprintf("t%d", i)
		tool.StartedAt = now - int64(10-i)*minMs
		key := "k"
		tool.InputKey = &key
		loopTools = append(loopTools, tool)
	}
	loopInput := input()
	loopInput.Tools = loopTools
	contextRequest := makeRequest()
	contextRequest.Ts = now - minMs
	contextRequest.Tokens = shared.Tokens{Input: 171_000}
	contextInput := input()
	contextInput.Requests = []shared.RequestRecord{contextRequest}
	retrySessionBurst := make([]shared.APIErrorRecord, 0, 5)
	for s := int64(0); s < 5; s++ {
		record := shared.APIErrorRecord{
			Ts: now - 3*minMs + s*1000, SessionID: ptr("s1"), Status: 429, Source: shared.SourceTranscript,
		}
		if s == 0 {
			record.Status = 529
		}
		retrySessionBurst = append(retrySessionBurst, record)
	}
	retrySession := input()
	retrySession.APIErrors = retrySessionBurst
	retryRouterBurst := make([]shared.APIErrorRecord, 0, 5)
	for i := int64(1); i <= 5; i++ {
		plugin := "zai"
		retryRouterBurst = append(retryRouterBurst, shared.APIErrorRecord{
			Ts: now - i*1000, Status: 429, Source: shared.SourceRouter, Plugin: &plugin,
		})
	}
	retryRouter := input()
	retryRouter.APIErrors = retryRouterBurst
	budgetInput := func(spent float64, pct float64, action budget.Action) AlertInput {
		in := input()
		in.Budgets = []budget.BudgetSpend{{
			ID:          "b1",
			SpentUsd:    shared.USD(spent),
			LimitUsd:    shared.USD(10),
			Pct:         budget.Percent(pct),
			Scope:       "provider:zai",
			Period:      budget.PeriodMonth,
			Action:      action,
			PeriodStart: 5,
		}}
		return in
	}
	cases := []struct {
		name string
		in   AlertInput
		want string
	}{
		{
			"stuck", stuckInput,
			`[{"kind":"stuck","sessionId":"s1","agentId":null,"project":"app","costUsd":0.5,"id":"stuck:s1:1799999280000","since":1799999280000,"detail":"No request or tool activity for 12 min in the middle of a turn","severity":"err"}]`,
		},
		{
			"loop", loopInput,
			`[{"kind":"loop","sessionId":"s1","agentId":null,"project":"app","costUsd":0.5,"id":"loop:s1:main:t1","since":1799999460000,"detail":"Bash called 5 times in a row with the same input","severity":"warn"}]`,
		},
		{
			"context", contextInput,
			`[{"kind":"context","sessionId":"s1","agentId":null,"project":"app","costUsd":0.5,"id":"context:s1:main:req-1","since":1799999940000,"detail":"Context at 171k of 200k tokens (86%): compaction is near","severity":"warn"}]`,
		},
		{
			"retry storm", retrySession,
			`[{"kind":"retry_storm","sessionId":"s1","agentId":null,"project":"app","costUsd":0.5,"id":"retry_storm:s1:1799999820000","since":1799999820000,"detail":"5 rate limits or server errors (429, 529) within 2 min","severity":"err"}]`,
		},
		{
			"retry storm at a router", retryRouter,
			`[{"kind":"retry_storm","sessionId":"","agentId":null,"project":"","costUsd":null,"id":"retry_storm:router:zai:1799999995000","since":1799999995000,"detail":"5 rate limits or server errors (429) within 2 min at the Z.ai router","severity":"err"}]`,
		},
		{
			"budget warn", budgetInput(8.4, 84, budget.ActionStop),
			`[{"id":"budget:b1:5:80","kind":"budget","sessionId":"","agentId":null,"project":"","since":5,"detail":"Z.ai monthly budget at 84% ($8.40 of $10.00), stops at 100%","costUsd":8.4,"severity":"warn"}]`,
		},
		{
			"budget err", budgetInput(10, 100, budget.ActionStop),
			`[{"id":"budget:b1:5:100","kind":"budget","sessionId":"","agentId":null,"project":"","since":5,"detail":"Z.ai monthly budget at 100% ($10.00 of $10.00), requests are stopped","costUsd":10,"severity":"err"}]`,
		},
		{
			"budget warn only", budgetInput(12, 120, budget.ActionWarn),
			`[{"id":"budget:b1:5:100","kind":"budget","sessionId":"","agentId":null,"project":"","since":5,"detail":"Z.ai monthly budget at 120% ($12.00 of $10.00), warn only","costUsd":12,"severity":"err"}]`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := domain.MarshalCompactJSON(detect(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Errorf("json:\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

func TestUpstreamAlerts(t *testing.T) {
	failing := func(ts int64, reason string) shared.RouterEventRecord {
		return shared.RouterEventRecord{Ts: ts, Plugin: "anthropic", Event: shared.EventUpstreamFailing, Reason: reason}
	}
	recovered := func(ts int64) shared.RouterEventRecord {
		return shared.RouterEventRecord{Ts: ts, Plugin: "anthropic", Event: shared.EventUpstreamRecovered}
	}

	t.Run("a failing path newer than its recovery raises one critical alert with the code and the hint", func(t *testing.T) {
		got := upstreamAlerts(AlertInput{
			Now:          now,
			RouterEvents: []shared.RouterEventRecord{failing(now-minMs, "ECERT"), recovered(now - 3*minMs)},
		}, DefaultThresholds)
		if len(got) != 1 {
			t.Fatalf("alerts = %d (%+v), want 1", len(got), got)
		}
		alert := got[0]
		if alert.Kind != shared.AlertUpstream || alert.Severity != shared.SeverityErr {
			t.Errorf("kind/severity = %q/%q, want upstream/err", alert.Kind, alert.Severity)
		}
		if alert.Since != now-minMs {
			t.Errorf("since = %d, want the failing event's time", alert.Since)
		}
		want := "ECERT: a certificate check failed; huddle-router never routes through another router, so restart sessions started before an update"
		if alert.Detail != want {
			t.Errorf("detail = %q, want %q", alert.Detail, want)
		}
		if alert.ID == "" {
			t.Error("the alert has no id")
		}
	})

	t.Run("a recovery newer than the failing event raises nothing", func(t *testing.T) {
		got := upstreamAlerts(AlertInput{
			Now:          now,
			RouterEvents: []shared.RouterEventRecord{failing(now-3*minMs, "ECERT"), recovered(now - minMs)},
		}, DefaultThresholds)
		if len(got) != 0 {
			t.Errorf("alerts = %+v, want none", got)
		}
	})

	t.Run("a failing event older than ten minutes raises nothing", func(t *testing.T) {
		got := upstreamAlerts(AlertInput{
			Now:          now,
			RouterEvents: []shared.RouterEventRecord{failing(now-11*minMs, "ECERT")},
		}, DefaultThresholds)
		if len(got) != 0 {
			t.Errorf("alerts = %+v, want none", got)
		}
	})

	t.Run("a failing event exactly ten minutes old raises nothing", func(t *testing.T) {
		got := upstreamAlerts(AlertInput{
			Now:          now,
			RouterEvents: []shared.RouterEventRecord{failing(now-10*minMs, "ECERT")},
		}, DefaultThresholds)
		if len(got) != 0 {
			t.Errorf("alerts = %+v, want none", got)
		}
	})

	t.Run("no upstream events at all raise nothing", func(t *testing.T) {
		got := upstreamAlerts(AlertInput{Now: now}, DefaultThresholds)
		if len(got) != 0 {
			t.Errorf("alerts = %+v, want none", got)
		}
	})
}

func TestDetectAlertsJoinsTheUpstreamAlert(t *testing.T) {
	event := shared.RouterEventRecord{
		Ts: now - minMs, Plugin: "anthropic", Event: shared.EventUpstreamFailing, Reason: "ECONNREFUSED",
	}
	got := DetectAlerts(AlertInput{Now: now, RouterEvents: []shared.RouterEventRecord{event}}, DefaultThresholds)
	found := false
	for _, alert := range got {
		if alert.Kind == shared.AlertUpstream {
			found = true
		}
	}
	if !found {
		t.Errorf("DetectAlerts = %+v, want an upstream alert among them", got)
	}
}
