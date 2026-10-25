package server

import (
	"math"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The blocks route: the 5-hour metering windows over a seeded history, Claude models by
// default and the routed provider ones behind the toggle.

// blockMoment is one moment the fixture's requests ran at: dayOffset UTC days from the test
// clock's today at the UTC hour and minute, as epoch ms.
func blockMoment(dayOffset, hour, minute int) int64 {
	day := time.Date(nowAt.Year(), nowAt.Month(), nowAt.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, dayOffset)
	return time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, time.UTC).UnixMilli()
}

// seedBlocks writes the blocks fixture: a Claude pair this morning two hours and one hour
// before the clock, a GLM call between them, a Claude call yesterday, and one eight days
// back that only the 30-day window reaches.
func seedBlocks(t *testing.T, h *history.History) {
	t.Helper()
	project := "app"
	label := "Ship the block view"
	if err := h.UpsertNode(history.NodeUpsert{
		ID: "b1/main", Kind: shared.NodeMain, SessionID: "b1", AgentID: "main",
		Project: &project, Label: &label,
	}); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	put := func(id, model, upstream, providerName string, ts int64, in, out, cacheRead, cacheWrite int64) {
		record := shared.RequestRecord{
			ID: id, SessionID: "b1", AgentID: "main", Model: model,
			Upstream: upstream, Provider: providerName, Ts: ts,
			Tokens: shared.Tokens{Input: in, Output: out, CacheRead: cacheRead, CacheWrite: cacheWrite},
		}
		if err := h.PutRequest("b1/main", record); err != nil {
			t.Fatalf("put the request %s: %v", id, err)
		}
	}
	anthropic := "https://api.anthropic.com"
	zai := "https://api.z.ai"
	put("bc1", "claude-sonnet-5-5", anthropic, "anthropic", blockMoment(0, 10, 0), 1_000_000, 200_000, 500_000, 300_000)
	put("bg1", "glm-5.3", zai, "zai", blockMoment(0, 11, 30), 250_000, 50_000, 0, 0)
	put("bc2", "claude-sonnet-5-5", anthropic, "anthropic", blockMoment(0, 11, 0), 500_000, 100_000, 0, 0)
	put("byc", "claude-sonnet-5-5", anthropic, "anthropic", blockMoment(-1, 12, 0), 1_000, 100, 0, 0)
	put("boc", "claude-sonnet-5-5", anthropic, "anthropic", blockMoment(-8, 12, 0), 1_000, 100, 0, 0)
}

// blockTotals is one block's usage as the answer carries it.
type blockTotals struct {
	Requests   int64   `json:"requests"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cacheRead"`
	CacheWrite int64   `json:"cacheWrite"`
	CostUSD    float64 `json:"costUsd"`
	SavingsUSD float64 `json:"savingsUsd"`
}

// blockRow is one block of the answer.
type blockRow struct {
	Start   int64 `json:"start"`
	End     int64 `json:"end"`
	FirstTS int64 `json:"firstTs"`
	LastTS  int64 `json:"lastTs"`
	Active  bool  `json:"active"`
	blockTotals
	BurnTokensPerHour float64 `json:"burnTokensPerHour"`
	BurnCostPerHour   float64 `json:"burnCostPerHour"`
	ProjectedTokens   float64 `json:"projectedTokens"`
	ProjectedCostUSD  float64 `json:"projectedCostUsd"`
}

// blocksAnswer is the whole answer.
type blocksAnswer struct {
	Blocks      []blockRow `json:"blocks"`
	Approximate bool       `json:"approximate"`
	Unpriced    int        `json:"unpriced"`
}

// wantNear fails the test when a figure misses its want by more than one part in a thousand.
func wantNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > math.Max(1, math.Abs(want))*0.001 {
		t.Errorf("%s = %.6f, want about %.6f", name, got, want)
	}
}

func TestBlocksRoute(t *testing.T) {
	t.Run("503s when the history failed to open", func(t *testing.T) {
		answer := historyGet(t, nil, "/api/blocks", nil, nil)
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

	t.Run("400s a days value that is not 7 or 30", func(t *testing.T) {
		h := openTestHistory(t)
		for _, days := range []string{"10", "90", "0", "-7", "abc", ""} {
			answer := historyGet(t, h, "/api/blocks?days="+days, nil, nil)
			if answer.Status != 400 {
				t.Errorf("days=%q status = %d, want 400", days, answer.Status)
			}
			var body struct {
				Error string `json:"error"`
			}
			decodeBody(t, answer, &body)
			if body.Error != "days: one of 7, 30" {
				t.Errorf("days=%q error = %q, want the allowed values", days, body.Error)
			}
		}
	})

	t.Run("reads the seeded blocks, Claude models by default, newest first", func(t *testing.T) {
		h := openTestHistory(t)
		seedBlocks(t, h)
		var answer blocksAnswer
		decodeBody(t, historyGet(t, h, "/api/blocks?days=7", nil, nil), &answer)
		if len(answer.Blocks) != 2 {
			t.Fatalf("blocks = %d, want the morning pair and yesterday's call", len(answer.Blocks))
		}
		active, yesterday := answer.Blocks[0], answer.Blocks[1]
		if !active.Active || yesterday.Active {
			t.Fatalf("active = %v and %v, want this morning's block alone active", active.Active, yesterday.Active)
		}
		if active.Start != blockMoment(0, 10, 0) || active.End != blockMoment(0, 15, 0) {
			t.Errorf("active window = %d..%d, want 10:00..15:00 floored to the hour",
				active.Start, active.End)
		}
		if active.FirstTS != blockMoment(0, 10, 0) || active.LastTS != blockMoment(0, 11, 0) {
			t.Errorf("active first/last = %d..%d, want 10:00..11:00", active.FirstTS, active.LastTS)
		}
		if active.Requests != 2 {
			t.Errorf("active requests = %d, want the two Claude calls, the GLM one left out",
				active.Requests)
		}
		if active.Input != 1_500_000 || active.Output != 300_000 ||
			active.CacheRead != 500_000 || active.CacheWrite != 300_000 {
			t.Errorf("active tokens = %+v, want the pair's sums", active.blockTotals)
		}
		// bc1: 2 + 2 + 0.05 + 0.75 = 4.80; bc2: 1 + 1 = 2.00
		wantMoney(t, "active cost", active.CostUSD, 6.80)
		// two hours of the window are active: 2.6M tokens and $6.80 an hour, 3h to go
		wantNear(t, "active token burn", active.BurnTokensPerHour, 1_300_000)
		wantNear(t, "active cost burn", active.BurnCostPerHour, 3.40)
		wantNear(t, "projected tokens", active.ProjectedTokens, 3_900_000)
		wantNear(t, "projected cost", active.ProjectedCostUSD, 10.20)
		if yesterday.Start != blockMoment(-1, 12, 0) || yesterday.End != blockMoment(-1, 17, 0) {
			t.Errorf("yesterday's window = %d..%d, want 12:00..17:00 the day before",
				yesterday.Start, yesterday.End)
		}
		if yesterday.Requests != 1 || yesterday.ProjectedTokens != 0 || yesterday.ProjectedCostUSD != 0 {
			t.Errorf("yesterday's block = %+v, want its one call and no projection", yesterday)
		}
		if answer.Approximate || answer.Unpriced != 0 {
			t.Errorf("flags = %v %d, want every counted row priced", answer.Approximate, answer.Unpriced)
		}
	})

	t.Run("includes the routed provider models behind the toggle", func(t *testing.T) {
		h := openTestHistory(t)
		seedBlocks(t, h)
		var answer blocksAnswer
		decodeBody(t, historyGet(t, h, "/api/blocks?days=7&providers=1", nil, nil), &answer)
		if len(answer.Blocks) != 2 || answer.Blocks[0].Requests != 3 {
			t.Fatalf("blocks = %+v, want the morning block holding the GLM call too", answer.Blocks)
		}
		wantMoney(t, "all-models cost", answer.Blocks[0].CostUSD, 7.37)
		// the toggle does not move the window: the 30-day-only call stays out
		if answer.Blocks[1].Requests != 1 {
			t.Errorf("yesterday's requests = %d, want 1", answer.Blocks[1].Requests)
		}
	})

	t.Run("windows the blocks at 7 or 30 UTC days, 7 the default", func(t *testing.T) {
		h := openTestHistory(t)
		seedBlocks(t, h)
		var week blocksAnswer
		decodeBody(t, historyGet(t, h, "/api/blocks", nil, nil), &week)
		if len(week.Blocks) != 2 {
			t.Fatalf("default blocks = %d, want 2: the call eight days back sits outside", len(week.Blocks))
		}
		var month blocksAnswer
		decodeBody(t, historyGet(t, h, "/api/blocks?days=30", nil, nil), &month)
		if len(month.Blocks) != 3 {
			t.Fatalf("30-day blocks = %d, want 3", len(month.Blocks))
		}
		if month.Blocks[2].Start != blockMoment(-8, 12, 0) {
			t.Errorf("oldest block starts %d, want the call eight days back", month.Blocks[2].Start)
		}
	})
}

// TestBlocksAreUTC reads a clock in UTC+05:30, whose midnight and whole hours sit half an hour
// off UTC's: the window starts at the UTC midnight six days back, and a block starts on its
// first request's UTC hour.
func TestBlocksAreUTC(t *testing.T) {
	h := openTestHistory(t)
	india := time.FixedZone("UTC+5:30", 5*3600+1800)
	now := time.Date(2026, time.October, 16, 1, 0, 0, 0, india) // 19:30 UTC on the 15th
	if err := h.UpsertNode(history.NodeUpsert{ID: "bz/main", Kind: shared.NodeMain, SessionID: "bz", AgentID: "main"}); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	put := func(id string, ts time.Time) {
		record := shared.RequestRecord{
			ID: id, SessionID: "bz", AgentID: "main", Model: "claude-sonnet-5-5",
			Upstream: "https://api.anthropic.com", Provider: "anthropic", Ts: ts.UnixMilli(),
			Tokens: shared.Tokens{Input: 1_000, Output: 100},
		}
		if err := h.PutRequest("bz/main", record); err != nil {
			t.Fatalf("put the request %s: %v", id, err)
		}
	}
	// 12:00 UTC on the 9th: inside the UTC window, before the UTC+05:30 one
	put("edge", time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC))
	// 19:15 UTC on the 15th: its block starts at 19:00 UTC, not at the zone's 18:30 UTC
	put("late", time.Date(2026, time.October, 15, 19, 15, 0, 0, time.UTC))
	answer, err := blocksRoute(h, routeQuery("days=7"), func() time.Time { return now })
	if err != nil {
		t.Fatalf("blocksRoute: %v", err)
	}
	var report blocksAnswer
	decodeBody(t, answer, &report)
	if len(report.Blocks) != 2 {
		t.Errorf("blocks = %d, want 2: the request on the 9th sits inside the UTC window", len(report.Blocks))
	}
	if len(report.Blocks) == 0 {
		t.Fatal("no blocks")
	}
	start := time.UnixMilli(report.Blocks[0].Start).UTC()
	if want := time.Date(2026, time.October, 15, 19, 0, 0, 0, time.UTC); !start.Equal(want) {
		t.Errorf("the newest block starts at %s, want the UTC hour %s", start, want)
	}
	if !report.Blocks[0].Active {
		t.Error("the newest block is not active at 19:30 UTC")
	}
}
