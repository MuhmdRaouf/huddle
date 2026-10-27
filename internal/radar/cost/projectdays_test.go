package cost

import (
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// ProjectDays buckets the ledger's priced spend per project and calendar day in the zone:
// two calls on the same UTC day can sit on different days, unpriced calls add nothing, a
// project-day with no priced cost is left out, and so is the session-less "(no project)"
// group.

// pricedUSD is one row's priced spend; a fixture row that never prices is a broken fixture.
func pricedUSD(t *testing.T, row LedgerRow) shared.USD {
	t.Helper()
	c := RequestCost(pricedOfRow(row))
	if c == nil {
		t.Fatalf("fixture row %s (%s) never prices", row.ID, row.Model)
	}
	return c.USD
}

func TestProjectDaysBucketsByZoneDay(t *testing.T) {
	zone := time.FixedZone("test", 2*3600)                 // UTC+2: its midnight is 22:00 UTC
	now := time.Date(2026, 10, 25, 23, 30, 0, 0, time.UTC) // the zone's day is 2026-10-26
	run := func(id, session, model string, at time.Time) LedgerRow {
		return LedgerRow{
			ID: id, Ts: at.UnixMilli(), Model: model, SessionID: session, AgentID: "main",
			Tokens: shared.Tokens{Input: 10_000, Output: 500},
		}
	}
	// 23:00 UTC is 01:00 in the zone: today. 22:01 UTC is 00:01: today too. 21:59 UTC is
	// 23:59 the day before: one zone day back, though the same UTC day as the 22:01 call.
	r1 := run("r1", "s1", "claude-opus-5-5", now.Add(-30*time.Minute))
	r2 := run("r2", "s2", "claude-opus-5-5", time.Date(2026, 10, 25, 21, 59, 0, 0, time.UTC))
	r3 := run("r3", "s1", "claude-opus-5-5", time.Date(2026, 10, 25, 22, 1, 0, 0, time.UTC))
	r4 := run("r4", "s2", "claude-opus-5-5", time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC))
	// nine zone days back: outside the eight-day window
	r5 := run("r5", "s1", "claude-opus-5-5", time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	// unpriced: adds nothing to web's today, and alone on its own day leaves no zero row
	r6 := run("r6", "s1", "not-a-model", now.Add(-30*time.Minute))
	r8 := run("r8", "s1", "not-a-model", time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC))
	// no session, so no project: the "(no project)" group is left out
	r7 := run("r7", "s9", "claude-opus-5-5", now.Add(-30*time.Minute))
	names := LedgerNames{Sessions: map[string]SessionNames{
		"s1": {Project: str("web")},
		"s2": {Project: str("api")},
	}}

	got := ProjectDays([]LedgerRow{r1, r2, r3, r4, r5, r6, r7, r8}, names, now.UnixMilli(), zone, 8)
	want := []ProjectDay{
		{Project: "api", DaysAgo: 1, CostUsd: pricedUSD(t, r2)},
		{Project: "api", DaysAgo: 7, CostUsd: pricedUSD(t, r4)},
		{Project: "web", DaysAgo: 0, CostUsd: *AddCost(
			usdOf(RequestCost(pricedOfRow(r1))), usdOf(RequestCost(pricedOfRow(r3))))},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProjectDays() = %+v, want %+v", got, want)
	}
}

func TestProjectDaysSpansDaylightSaving(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load America/New_York: %v", err)
	}
	run := func(id string, at time.Time) LedgerRow {
		return LedgerRow{
			ID: id, Ts: at.UnixMilli(), Model: "claude-opus-5-5", SessionID: "s1", AgentID: "main",
			Tokens: shared.Tokens{Input: 10_000, Output: 500},
		}
	}
	check := func(t *testing.T, now time.Time, rows []LedgerRow, want []ProjectDay) {
		t.Helper()
		names := LedgerNames{Sessions: map[string]SessionNames{
			"s1": {Project: str("web")},
		}}
		got := ProjectDays(rows, names, now.UnixMilli(), newYork, 8)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ProjectDays() = %+v, want %+v", got, want)
		}
	}
	t.Run("the fall-back change, whose day has 25 hours", func(t *testing.T) {
		now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) // 07:00 EST
		// the change day holds calls on both sides of the 2 AM fall-back; its midnight is
		// 25 hours before today's, so one day back stays one day back either way
		before := run("r-before", time.Date(2026, 11, 1, 1, 0, 0, 0, newYork)) // 01:00 EDT
		after := run("r-after", time.Date(2026, 11, 1, 20, 0, 0, 0, newYork))  // 20:00 EST
		older := run("r-older", time.Date(2026, 10, 31, 12, 0, 0, 0, newYork))
		today := run("r-today", time.Date(2026, 11, 2, 7, 0, 0, 0, newYork))
		check(t, now, []LedgerRow{before, after, older, today}, []ProjectDay{
			{Project: "web", DaysAgo: 0, CostUsd: pricedUSD(t, today)},
			{Project: "web", DaysAgo: 1, CostUsd: *AddCost(
				usdOf(RequestCost(pricedOfRow(before))), usdOf(RequestCost(pricedOfRow(after))))},
			{Project: "web", DaysAgo: 2, CostUsd: pricedUSD(t, older)},
		})
	})
	t.Run("the spring-forward change, whose day has 23 hours", func(t *testing.T) {
		now := time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC) // 08:00 EDT
		// the change day's midnight is 23 hours before today's, so a day kept by truncation
		// reads as today; the days back has to round the hour gap off
		before := run("r-before", time.Date(2026, 3, 8, 1, 0, 0, 0, newYork)) // 01:00 EST
		after := run("r-after", time.Date(2026, 3, 8, 3, 0, 0, 0, newYork))   // 03:00 EDT
		older := run("r-older", time.Date(2026, 3, 7, 12, 0, 0, 0, newYork))
		today := run("r-today", time.Date(2026, 3, 9, 8, 0, 0, 0, newYork))
		check(t, now, []LedgerRow{before, after, older, today}, []ProjectDay{
			{Project: "web", DaysAgo: 0, CostUsd: pricedUSD(t, today)},
			{Project: "web", DaysAgo: 1, CostUsd: *AddCost(
				usdOf(RequestCost(pricedOfRow(before))), usdOf(RequestCost(pricedOfRow(after))))},
			{Project: "web", DaysAgo: 2, CostUsd: pricedUSD(t, older)},
		})
	})
}
