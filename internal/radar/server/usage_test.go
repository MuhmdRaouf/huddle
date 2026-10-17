package server

import (
	"math"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The usage route: a 3-day fixture set read back to the token and the cent.

// usageMoment is one moment dayOffset local days from the test clock's today, as epoch ms.
func usageMoment(dayOffset, hour, minute int) int64 {
	day := time.Date(nowAt.Year(), nowAt.Month(), nowAt.Day(), 0, 0, 0, 0, nowAt.Location()).
		AddDate(0, 0, dayOffset)
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, day.Location()).UnixMilli()
}

// usageDayDate spells the local date dayOffset days from the test clock's today.
func usageDayDate(dayOffset int) string {
	return time.UnixMilli(usageMoment(dayOffset, 0, 0)).Format("2006-01-02")
}

// seedUsage writes three local days of requests across two projects and three sessions:
// glm-5.3 and its flash on app, a Claude request on web, and a model no price covers.
func seedUsage(t *testing.T, h *history.History) {
	t.Helper()
	migrate := "Migrate the build"
	tune := "Tune the radar page"
	provider := "Try the new provider"
	app, web := "app", "web"
	node := func(id, session string, project, label *string) {
		n := history.NodeUpsert{ID: id, Kind: shared.NodeMain, SessionID: session, AgentID: "main"}
		n.Project, n.Label = project, label
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert the node %s: %v", id, err)
		}
	}
	node("u1/main", "u1", &app, &migrate)
	node("u2/main", "u2", &web, &tune)
	node("u3/main", "u3", &app, &provider)
	put := func(nodeID, id, session, model, upstream, providerName string, ts int64, in, out, cacheRead int64) {
		record := shared.RequestRecord{
			ID: id, SessionID: session, AgentID: "main", Model: model,
			Upstream: upstream, Provider: providerName, Ts: ts,
			Tokens: shared.Tokens{Input: in, Output: out, CacheRead: cacheRead},
		}
		if err := h.PutRequest(nodeID, record); err != nil {
			t.Fatalf("put the request %s: %v", id, err)
		}
	}
	put("u1/main", "ur1", "u1", "glm-5.3", "https://api.z.ai", "zai", usageMoment(-2, 10, 0), 2_000_000, 500_000, 1_000_000)
	put("u1/main", "ur2", "u1", "glm-5.3", "https://api.z.ai", "zai", usageMoment(-1, 15, 30), 1_000_000, 250_000, 2_000_000)
	put("u1/main", "ur3", "u1", "glm-5.3-flash", "https://api.z.ai", "zai", usageMoment(0, 8, 0), 4_000_000, 200_000, 1_000_000)
	put("u2/main", "ur4", "u2", "claude-sonnet-5-5", "https://api.anthropic.com", "anthropic", usageMoment(-1, 11, 0), 1_000_000, 100_000, 500_000)
	put("u2/main", "ur5", "u2", "glm-5.3", "https://api.z.ai", "zai", usageMoment(0, 9, 45), 250_000, 50_000, 0)
	put("u3/main", "ur6", "u3", "mystery-9", "", "", usageMoment(0, 10, 15), 1_000, 0, 0)
}

// usageTotals is one totals block as the answer carries it.
type usageTotals struct {
	Requests   int64   `json:"requests"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	CostUSD    float64 `json:"costUsd"`
	SavingsUSD float64 `json:"savingsUsd"`
}

// usageDay is one day of the answer.
type usageDay struct {
	Date string `json:"date"`
	usageTotals
}

// usageSlice is one breakdown row of the answer.
type usageSlice struct {
	Key string `json:"key"`
	usageTotals
	Days []usageDay `json:"days"`
}

// usageSession is one top session of the answer.
type usageSession struct {
	SessionID string `json:"sessionId"`
	Label     string `json:"label"`
	Project   string `json:"project"`
	LastTS    int64  `json:"lastTs"`
	usageTotals
}

// usageReport is the whole answer.
type usageReport struct {
	From            string                    `json:"from"`
	To              string                    `json:"to"`
	Days            []usageDay                `json:"days"`
	Totals          usageTotals               `json:"totals"`
	ByModel         []usageSlice              `json:"byModel"`
	ByProject       []usageSlice              `json:"byProject"`
	ByKind          []usageSlice              `json:"byKind"`
	ByHarness       []usageSlice              `json:"byHarness"`
	TopSessions     map[string][]usageSession `json:"topSessions"`
	Approximate     bool                      `json:"approximate"`
	Unpriced        int                       `json:"unpriced"`
	PricesRetrieved string                    `json:"pricesRetrieved"`
}

// wantMoney fails the test when a dollar figure misses by half a cent or more.
func wantMoney(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) >= 0.005 {
		t.Errorf("%s = %.6f, want %.6f", name, got, want)
	}
}

func TestUsageRoute(t *testing.T) {
	t.Run("503s when the history failed to open", func(t *testing.T) {
		answer := historyGet(t, nil, "/api/usage", nil, nil)
		if answer.Status != 503 {
			t.Fatalf("status = %d, want 503", answer.Status)
		}
		var body struct {
			Error string `json:"error"`
		}
		decodeBody(t, answer, &body)
		if body.Error != "history unavailable" {
			t.Errorf("error = %q, want history unavailable", body.Error)
		}
	})

	t.Run("400s a days value that is not 7, 30 or 90", func(t *testing.T) {
		h := openTestHistory(t)
		for _, days := range []string{"10", "0", "-7", "abc", ""} {
			answer := historyGet(t, h, "/api/usage?days="+days, nil, nil)
			if answer.Status != 400 {
				t.Errorf("days=%q status = %d, want 400", days, answer.Status)
			}
			var body struct {
				Error string `json:"error"`
			}
			decodeBody(t, answer, &body)
			if body.Error != "days: one of 7, 30, 90" {
				t.Errorf("days=%q error = %q, want the allowed values", days, body.Error)
			}
		}
	})

	t.Run("defaults to 30 days ending today", func(t *testing.T) {
		h := openTestHistory(t)
		seedUsage(t, h)
		var report usageReport
		decodeBody(t, historyGet(t, h, "/api/usage", nil, nil), &report)
		if report.From != usageDayDate(-29) || report.To != usageDayDate(0) {
			t.Errorf("window = %s..%s, want %s..%s", report.From, report.To, usageDayDate(-29), usageDayDate(0))
		}
		if len(report.Days) != 30 {
			t.Errorf("days = %d, want 30 zero-filled", len(report.Days))
		}
	})

	t.Run("sums the 3-day fixture to the token and the cent", func(t *testing.T) {
		h := openTestHistory(t)
		seedUsage(t, h)
		// the fixture's three days sit inside the shortest window the route answers
		answer := historyGet(t, h, "/api/usage?days=7", nil, nil)
		if answer.Status != 200 {
			t.Fatalf("status = %d, want 200", answer.Status)
		}
		var report usageReport
		decodeBody(t, answer, &report)
		if report.From != usageDayDate(-6) || report.To != usageDayDate(0) {
			t.Errorf("window = %s..%s, want %s..%s", report.From, report.To, usageDayDate(-6), usageDayDate(0))
		}
		if len(report.Days) != 7 {
			t.Fatalf("days = %d, want 7", len(report.Days))
		}
		// ur1: 2M in, 500k out, 1M cache read; glm-5.3 at 1.40 / 4.40 / 0.26 → 5.26
		wantTotals := func(name string, got usageTotals, requests, in, out, cacheRead int64, cost, savings float64) {
			t.Helper()
			if got.Requests != requests || got.Input != in || got.Output != out || got.CacheRead != cacheRead {
				t.Errorf("%s totals = %+v, want requests %d input %d output %d cacheRead %d",
					name, got, requests, in, out, cacheRead)
			}
			wantMoney(t, name+" cost", got.CostUSD, cost)
			wantMoney(t, name+" savings", got.SavingsUSD, savings)
		}
		wantTotals("grand", report.Totals, 6, 8_251_000, 1_100_000, 4_500_000, 12.63, 4.49)
		wantTotals("day -2", report.Days[4].usageTotals, 1, 2_000_000, 500_000, 1_000_000, 5.26, 1.14)
		wantTotals("day -1", report.Days[5].usageTotals, 2, 2_000_000, 350_000, 2_500_000, 6.07, 3.23)
		wantTotals("today", report.Days[6].usageTotals, 3, 4_251_000, 250_000, 1_000_000, 1.30, 0.12)
		if report.Days[4].Date != usageDayDate(-2) || report.Days[6].Date != usageDayDate(0) {
			t.Errorf("day dates = %s..%s, want %s..%s", report.Days[4].Date, report.Days[6].Date,
				usageDayDate(-2), usageDayDate(0))
		}
		empty := 0
		for _, day := range report.Days {
			if day.Requests == 0 {
				empty++
			}
		}
		if empty != 4 {
			t.Errorf("empty days = %d, want the 4 the fixture holds nothing on", empty)
		}
		// by model, costliest first: glm-5.3, the Claude one, the flash, the unpriced one
		wantModel := []struct {
			key       string
			totals    usageTotals
			cost      float64
			savings   float64
			requests  int64
			in, out   int64
			cacheRead int64
		}{
			{"glm-5.3", usageTotals{}, 8.85, 3.42, 3, 3_250_000, 800_000, 3_000_000},
			{"claude-sonnet-5-5", usageTotals{}, 3.05, 0.95, 1, 1_000_000, 100_000, 500_000},
			{"glm-5.3-flash", usageTotals{}, 0.73, 0.12, 1, 4_000_000, 200_000, 1_000_000},
			{"mystery-9", usageTotals{}, 0, 0, 1, 1_000, 0, 0},
		}
		if len(report.ByModel) != len(wantModel) {
			t.Fatalf("byModel rows = %d, want %d", len(report.ByModel), len(wantModel))
		}
		for i, want := range wantModel {
			got := report.ByModel[i]
			if got.Key != want.key {
				t.Errorf("byModel[%d] = %q, want %q", i, got.Key, want.key)
				continue
			}
			wantTotals("byModel "+want.key, got.usageTotals, want.requests, want.in, want.out, want.cacheRead, want.cost, want.savings)
			if len(got.Days) != 7 {
				t.Errorf("byModel %s days = %d, want 7", want.key, len(got.Days))
			}
		}
		// by project: app carries the flash and the unpriced one, web the Claude one
		if len(report.ByProject) != 2 || report.ByProject[0].Key != "app" || report.ByProject[1].Key != "web" {
			t.Fatalf("byProject = %+v, want app then web", report.ByProject)
		}
		wantTotals("project app", report.ByProject[0].usageTotals, 4, 7_001_000, 950_000, 4_000_000, 9.01, 3.54)
		wantTotals("project web", report.ByProject[1].usageTotals, 2, 1_250_000, 150_000, 500_000, 3.62, 0.95)
		wantTotals("kind main", report.ByKind[0].usageTotals, 6, 8_251_000, 1_100_000, 4_500_000, 12.63, 4.49)
		// top sessions: each project's costliest first, the label the session's first prompt
		u1 := report.TopSessions["app"]
		if len(u1) != 2 || u1[0].SessionID != "u1" || u1[1].SessionID != "u3" {
			t.Fatalf("app sessions = %+v, want u1 then u3", u1)
		}
		wantTotals("session u1", u1[0].usageTotals, 3, 7_000_000, 950_000, 4_000_000, 9.01, 3.54)
		if u1[0].Label != "Migrate the build" || u1[0].Project != "app" || u1[0].LastTS != usageMoment(0, 8, 0) {
			t.Errorf("session u1 = %+v, want its label, project and last activity", u1[0])
		}
		u2 := report.TopSessions["web"]
		if len(u2) != 1 || u2[0].SessionID != "u2" || u2[0].Label != "Tune the radar page" {
			t.Errorf("web sessions = %+v, want u2 with its label", u2)
		}
		wantTotals("session u2", u2[0].usageTotals, 2, 1_250_000, 150_000, 500_000, 3.62, 0.95)
		// the honest flags: one request sat on a model no price covers
		if !report.Approximate || report.Unpriced != 1 {
			t.Errorf("approximate=%v unpriced=%d, want true 1", report.Approximate, report.Unpriced)
		}
		if report.PricesRetrieved == "" {
			t.Error("pricesRetrieved is empty, want the price table's day")
		}
	})

	t.Run("breaks the report down by harness, claude the untagged default", func(t *testing.T) {
		h := openTestHistory(t)
		seedUsage(t, h)
		project, label := "app", "Try Codex"
		node := history.NodeUpsert{ID: "u9/main", Kind: shared.NodeMain, SessionID: "u9", AgentID: "main"}
		node.Project, node.Label = &project, &label
		if err := h.UpsertNode(node); err != nil {
			t.Fatalf("upsert the node: %v", err)
		}
		if err := h.PutRequest("u9/main", shared.RequestRecord{
			ID: "ur9", SessionID: "u9", AgentID: "main", Model: "gpt-5-codex",
			Ts: usageMoment(-1, 12, 0), Tokens: shared.Tokens{Input: 1_000, Output: 100},
			Harness: shared.HarnessCodex,
		}); err != nil {
			t.Fatalf("put the codex request: %v", err)
		}
		var report usageReport
		decodeBody(t, historyGet(t, h, "/api/usage?days=7", nil, nil), &report)
		if len(report.ByHarness) != 2 || report.ByHarness[0].Key != "claude" || report.ByHarness[1].Key != "codex" {
			t.Fatalf("byHarness = %+v, want claude then codex", report.ByHarness)
		}
		claude, codex := report.ByHarness[0], report.ByHarness[1]
		if claude.Requests != 6 {
			t.Errorf("claude requests = %d, want the 6 untagged ones", claude.Requests)
		}
		if codex.Requests != 1 || codex.Input != 1_000 || codex.Output != 100 {
			t.Errorf("codex totals = %+v, want its one request's tokens", codex.usageTotals)
		}
		var harnessTotal int64
		for _, slice := range report.ByHarness {
			harnessTotal += slice.Requests
		}
		if harnessTotal != report.Totals.Requests {
			t.Errorf("byHarness sums to %d requests, want the grand %d", harnessTotal, report.Totals.Requests)
		}
	})
}
