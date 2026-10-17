package usage

import (
	"testing"
	"time"
)

// blockAt is one moment of 2026-10-15 in the test zone, as epoch ms.
func blockAt(hour, minute, second int) int64 {
	return time.Date(2026, time.October, 15, hour, minute, second, 0, zone).UnixMilli()
}

// blockNextDay is one moment one local day later, hour and minute only, as epoch ms.
func blockNextDay(hour, minute int) int64 {
	return time.Date(2026, time.October, 16, hour, minute, 0, 0, zone).UnixMilli()
}

// blockRow is one request row of the block tests, edited in place by edit when there is one.
func blockRow(ts int64, edit func(*Row)) Row {
	test := Row{
		TS:        ts,
		Model:     "claude-sonnet-5-5",
		Provider:  "anthropic",
		Project:   "app",
		SessionID: "s1",
		Kind:      KindMain,
		Label:     "Ship the block view",
		Input:     1_000,
		Output:    100,
	}
	if edit != nil {
		edit(&test)
	}
	return test
}

// claudeRates is what the block tests price a Claude model at, per million tokens.
var claudeRates = priceTable{"claude-sonnet-5-5": {in: 1, out: 2, cacheRead: 0.1}}

// blockClock is the moment the block tests read the clock at.
func blockClock(hour, minute int) time.Time {
	return time.Date(2026, time.October, 15, hour, minute, 0, 0, zone)
}

// wantBlock fails the test when a block misses its window, its activity or its rates.
func wantBlock(
	t *testing.T,
	name string,
	got Block,
	start, end, firstTS, lastTS int64,
	active bool,
	requests int64,
	burnTokens, burnCost, projectedTokens, projectedCost float64,
) {
	t.Helper()
	if got.Start != start || got.End != end || got.FirstTS != firstTS || got.LastTS != lastTS || got.Active != active {
		t.Errorf("%s window = start %d end %d first %d last %d active %v, want %d %d %d %d %v",
			name, got.Start, got.End, got.FirstTS, got.LastTS, got.Active, start, end, firstTS, lastTS, active)
	}
	if got.Requests != requests {
		t.Errorf("%s requests = %d, want %d", name, got.Requests, requests)
	}
	if !near(got.BurnTokensPerHour, burnTokens) || !near(float64(got.BurnCostPerHour), burnCost) {
		t.Errorf("%s burn = %.3f tokens/h %.6f $/h, want %.3f and %.6f",
			name, got.BurnTokensPerHour, got.BurnCostPerHour, burnTokens, burnCost)
	}
	if !near(got.ProjectedTokens, projectedTokens) || !near(float64(got.ProjectedCostUSD), projectedCost) {
		t.Errorf("%s projection = %.3f tokens %.6f $, want %.3f and %.6f",
			name, got.ProjectedTokens, got.ProjectedCostUSD, projectedTokens, projectedCost)
	}
}

// near reports whether two figures sit within one part in a million.
func near(a, b float64) bool {
	return abs(a-b) <= max(1, max(abs(a), abs(b)))*1e-6
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

func TestBlocksGapBoundaries(t *testing.T) {
	t.Run("a request past the window's end opens the next block, gap or not", func(t *testing.T) {
		// the first request at 10:30 floors the block to 10:00, so its window ends 15:00; the
		// 15:29 request is only 4:59:59.999 after the last one but past that end, so it opens
		// the 15:00 block, as the plan's own meter does
		rows := []Row{
			blockRow(blockAt(10, 30, 0), nil),
			blockRow(blockAt(15, 29, 59)+999, nil),
		}
		report := Blocks(rows, blockClock(16, 0), ClaudeModels, claudeRates, zone)
		if len(report.Blocks) != 2 {
			t.Fatalf("blocks = %d, want 2", len(report.Blocks))
		}
		newer, older := report.Blocks[0], report.Blocks[1]
		if newer.Start != blockAt(15, 0, 0) || newer.End != blockAt(20, 0, 0) || !newer.Active {
			t.Errorf("newer block = %d..%d active=%v, want 15:00..20:00 active", newer.Start, newer.End, newer.Active)
		}
		if older.Start != blockAt(10, 0, 0) || older.Requests != 1 || older.Active {
			t.Errorf("older block = start %d, %d requests, active=%v; want 10:00, 1, inactive",
				older.Start, older.Requests, older.Active)
		}
	})

	t.Run("a gap of exactly five hours starts the next block", func(t *testing.T) {
		rows := []Row{
			blockRow(blockAt(10, 0, 0), nil),
			blockRow(blockAt(15, 0, 0), nil),
		}
		report := Blocks(rows, blockClock(16, 0), ClaudeModels, claudeRates, zone)
		if len(report.Blocks) != 2 {
			t.Fatalf("blocks = %d, want 2", len(report.Blocks))
		}
		if report.Blocks[0].FirstTS != blockAt(15, 0, 0) || report.Blocks[1].FirstTS != blockAt(10, 0, 0) {
			t.Errorf("block order = %d then %d, want the newer block first",
				report.Blocks[0].FirstTS, report.Blocks[1].FirstTS)
		}
	})

	t.Run("one millisecond short of five hours stays in the block", func(t *testing.T) {
		rows := []Row{
			blockRow(blockAt(10, 0, 0), nil),
			blockRow(blockAt(15, 0, 0)-1, nil),
		}
		report := Blocks(rows, blockClock(16, 0), ClaudeModels, claudeRates, zone)
		if len(report.Blocks) != 1 {
			t.Fatalf("blocks = %d, want one", len(report.Blocks))
		}
		if report.Blocks[0].Requests != 2 {
			t.Errorf("requests = %d, want both rows in the one block", report.Blocks[0].Requests)
		}
	})
}

func TestBlocksEmpty(t *testing.T) {
	report := Blocks(nil, blockClock(12, 0), ClaudeModels, claudeRates, zone)
	if len(report.Blocks) != 0 {
		t.Errorf("blocks = %d, want none", len(report.Blocks))
	}
	if report.Approximate || report.Unpriced != 0 {
		t.Errorf("flags = approximate %v unpriced %d, want false and 0", report.Approximate, report.Unpriced)
	}
}

func TestBlocksOneRequest(t *testing.T) {
	// 60M tokens at the $5 the harness itself reported; the block floors to 12:00 and the
	// clock reads 14:00, so two of its hours are active: 30M tokens and $2.50 per hour, 90M
	// tokens and $7.50 projected to the 17:00 end
	rows := []Row{blockRow(blockAt(12, 34, 0), func(r *Row) {
		r.Input, r.Output, r.CacheRead, r.CacheWrite = 40_000_000, 10_000_000, 5_000_000, 5_000_000
		r.CostUSD = dollars(5)
	})}
	report := Blocks(rows, blockClock(14, 0), ClaudeModels, claudeRates, zone)
	if len(report.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(report.Blocks))
	}
	wantBlock(t, "the active block", report.Blocks[0],
		blockAt(12, 0, 0), blockAt(17, 0, 0), blockAt(12, 34, 0), blockAt(12, 34, 0),
		true, 1, 30_000_000, 2.5, 90_000_000, 7.5)
	if !close(report.Blocks[0].Totals.CostUSD, 5) {
		t.Errorf("cost = %.6f, want the harness's 5", report.Blocks[0].Totals.CostUSD)
	}
	if !close(report.Blocks[0].Totals.SavingsUSD, 0) {
		t.Errorf("savings = %.6f, want none behind a harness-reported cost", report.Blocks[0].Totals.SavingsUSD)
	}
}

func TestBlocksNowPastTheEnd(t *testing.T) {
	// the only request ran yesterday 10:30; its window closed 15:00 and nothing ran since, so
	// the block reads as ended: its burn spreads over the whole five hours and nothing projects
	rows := []Row{blockRow(blockAt(10, 30, 0), func(r *Row) {
		r.Input, r.Output = 10_000_000, 1_000_000
	})}
	at := time.Date(2026, time.October, 16, 9, 0, 0, 0, zone)
	report := Blocks(rows, at, ClaudeModels, claudeRates, zone)
	if len(report.Blocks) != 1 {
		t.Fatalf("blocks = %d, want the ended one", len(report.Blocks))
	}
	// 11M tokens at $12 (10M in + 1M out at 1 and 2 per million) over the full five hours
	wantBlock(t, "the ended block", report.Blocks[0],
		blockAt(10, 0, 0), blockAt(15, 0, 0), blockAt(10, 30, 0), blockAt(10, 30, 0),
		false, 1, 11_000_000.0/5, 12.0/5, 0, 0)
}

func TestBlocksBurnFloorsAtOneMinute(t *testing.T) {
	// the request ran seconds ago: the block floors to the hour, and the burn never divides by
	// a shorter span than a minute
	rows := []Row{blockRow(blockAt(12, 0, 30), func(r *Row) { r.Input, r.Output = 6_000, 0 })}
	at := time.Date(2026, time.October, 15, 12, 0, 45, 0, zone)
	report := Blocks(rows, at, ClaudeModels, claudeRates, zone)
	if len(report.Blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(report.Blocks))
	}
	// 15 seconds of the floored hour are active, floored to one minute: 6k tokens a minute,
	// and 4h59m15s left of the window
	wantBlock(t, "the fresh block", report.Blocks[0],
		blockAt(12, 0, 0), blockAt(17, 0, 0), blockAt(12, 0, 30), blockAt(12, 0, 30),
		true, 1, 6_000*60, 6_000.0/1e6*60, 6_000*299.25, 6_000.0/1e6*299.25)
}

func TestBlocksCountsClaudeModelsByDefault(t *testing.T) {
	rows := []Row{
		blockRow(blockAt(10, 0, 0), nil),
		blockRow(blockAt(10, 5, 0), func(r *Row) {
			r.Model, r.Provider, r.Upstream = "glm-5.3", "zai", "https://api.z.ai"
		}),
	}
	prices := priceTable{
		"claude-sonnet-5-5": {in: 1, out: 2, cacheRead: 0.1},
		"glm-5.3":           {in: 1.4, out: 4.4, cacheRead: 0.26},
	}
	claude := Blocks(rows, blockClock(11, 0), ClaudeModels, prices, zone)
	if len(claude.Blocks) != 1 || claude.Blocks[0].Requests != 1 {
		t.Fatalf("claude blocks = %+v, want the one Claude request alone", claude.Blocks)
	}
	if claude.Unpriced != 0 || claude.Approximate {
		t.Error("a provider row the scope skips must not read as unpriced")
	}
	all := Blocks(rows, blockClock(11, 0), AllModels, prices, zone)
	if len(all.Blocks) != 1 || all.Blocks[0].Requests != 2 {
		t.Fatalf("all blocks = %+v, want both requests in one", all.Blocks)
	}
	wantTotals(t, "all models", all.Blocks[0].Totals, 2, 2_000, 200, 0, 0,
		(1_000*1+100*2)/1e6+(1_000*1.4+100*4.4)/1e6, 0)
}

func TestBlocksSumsRowsAndOrdersNewestFirst(t *testing.T) {
	// two blocks, one yesterday morning and one this morning, two rows each
	rows := []Row{
		blockRow(blockAt(9, 10, 0), nil),
		blockRow(blockAt(9, 50, 0), func(r *Row) { r.Input = 2_000 }),
		blockRow(blockNextDay(9, 5), nil),
		blockRow(blockNextDay(9, 55), func(r *Row) { r.Input = 2_000 }),
	}
	at := time.Date(2026, time.October, 16, 11, 0, 0, 0, zone)
	report := Blocks(rows, at, ClaudeModels, claudeRates, zone)
	if len(report.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(report.Blocks))
	}
	today, yesterday := report.Blocks[0], report.Blocks[1]
	if !today.Active || yesterday.Active {
		t.Errorf("active = %v and %v, want today's block alone active", today.Active, yesterday.Active)
	}
	if today.Start != blockNextDay(9, 0) || today.End != blockNextDay(14, 0) {
		t.Errorf("today's window = %d..%d, want 09:00..14:00", today.Start, today.End)
	}
	wantTotals(t, "today", today.Totals, 2, 3_000, 200, 0, 0, (3_000*1+200*2)/1e6, 0)
	if today.FirstTS != blockNextDay(9, 5) || today.LastTS != blockNextDay(9, 55) {
		t.Errorf("today's first and last = %d %d, want the block's own ends",
			today.FirstTS, today.LastTS)
	}
	if yesterday.Start != blockAt(9, 0, 0) || yesterday.End != blockAt(14, 0, 0) {
		t.Errorf("yesterday's window = %d..%d, want 09:00..14:00 on the 15th", yesterday.Start, yesterday.End)
	}
}

func TestBlocksSortsRowsByTime(t *testing.T) {
	rows := []Row{
		blockRow(blockAt(15, 0, 0), nil),
		blockRow(blockAt(10, 0, 0), nil),
	}
	report := Blocks(rows, blockClock(16, 0), ClaudeModels, claudeRates, zone)
	if len(report.Blocks) != 2 || report.Blocks[0].FirstTS != blockAt(15, 0, 0) {
		t.Errorf("blocks = %+v, want two, newest first, whatever order the rows came in", report.Blocks)
	}
}

func TestBlocksUnpricedFlags(t *testing.T) {
	rows := []Row{
		blockRow(blockAt(10, 0, 0), nil),
		blockRow(blockAt(10, 30, 0), func(r *Row) { r.Model = "claude-future-9" }),
	}
	report := Blocks(rows, blockClock(11, 0), ClaudeModels, claudeRates, zone)
	if !report.Approximate || report.Unpriced != 1 {
		t.Errorf("flags = approximate %v unpriced %d, want true and 1", report.Approximate, report.Unpriced)
	}
	if len(report.Blocks) != 1 || report.Blocks[0].Requests != 2 {
		t.Fatalf("blocks = %+v, want the unpriced request counted at no cost", report.Blocks)
	}
	if !close(report.Blocks[0].Totals.CostUSD, (1_000*1+100*2)/1e6) {
		t.Errorf("cost = %.6f, want the priced request alone", report.Blocks[0].Totals.CostUSD)
	}
}
