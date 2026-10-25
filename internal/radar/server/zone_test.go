package server

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The routes that name a day name it in the hub's zone: the day card's window, the usage
// report's dates and the spend since midnight. Storage stays UTC; only the boundary moves.

// athensZone loads Europe/Athens, three hours ahead of UTC in summer and two in winter.
func athensZone(t *testing.T) *time.Location {
	t.Helper()
	zone, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Fatalf("load Europe/Athens: %v", err)
	}
	return zone
}

func TestTheDayCardStartsAtTheZonesMidnight(t *testing.T) {
	zone := athensZone(t)
	// 00:30 UTC on the 25th is 03:30 on the 25th in Athens: the window opens at 21:00 UTC on the 24th
	now := time.Date(2026, time.October, 25, 0, 30, 0, 0, time.UTC)
	source := &fakeDay{}
	if _, err := dayRoute(context.Background(), source, now, zone); err != nil {
		t.Fatalf("dayRoute: %v", err)
	}
	want := time.Date(2026, time.October, 25, 0, 0, 0, 0, zone)
	if !source.from.Equal(want) || !source.to.Equal(now) {
		t.Errorf("asked %s to %s, want the Athens midnight %s to now", source.from, source.to, want)
	}
}

func TestUsageDaysFollowTheZone(t *testing.T) {
	zone := athensZone(t)
	h := openTestHistory(t)
	if err := h.UpsertNode(history.NodeUpsert{ID: "z1/main", Kind: shared.NodeMain, SessionID: "z1", AgentID: "main"}); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	// 22:30 UTC on the 25th is 00:30 on the 26th in Athens (winter time, two hours ahead)
	at := time.Date(2026, time.October, 25, 22, 30, 0, 0, time.UTC)
	record := shared.RequestRecord{
		ID: "zr1", SessionID: "z1", AgentID: "main", Model: "glm-5.3",
		Upstream: "https://api.z.ai", Provider: "zai", Ts: at.UnixMilli(),
		Tokens: shared.Tokens{Input: 1_000, Output: 100},
	}
	if err := h.PutRequest("z1/main", record); err != nil {
		t.Fatalf("put the request: %v", err)
	}
	now := func() time.Time { return at.Add(time.Minute) }
	answer, err := usageRoute(h, routeQuery("days=7"), now, zone)
	if err != nil {
		t.Fatalf("usageRoute: %v", err)
	}
	var report usageReport
	decodeBody(t, answer, &report)
	if report.From != "2026-10-20" || report.To != "2026-10-26" {
		t.Errorf("window = %s..%s, want the Athens dates 2026-10-20..2026-10-26", report.From, report.To)
	}
	if len(report.Days) != 7 {
		t.Fatalf("days = %d, want 7", len(report.Days))
	}
	if last := report.Days[6]; last.Date != "2026-10-26" || last.Requests != 1 {
		t.Errorf("last day = %s with %d requests, want 2026-10-26 with the one request", last.Date, last.Requests)
	}

	today, err := todayRoute(h, now, zone)
	if err != nil {
		t.Fatalf("todayRoute: %v", err)
	}
	var day todayReport
	decodeBody(t, today, &day)
	if day.Date != "2026-10-26" || len(day.HourlyRequests) != 24 || day.HourlyRequests[0] != 1 {
		t.Errorf("today = %s with hours %v, want 2026-10-26 with the request in hour 00", day.Date, day.HourlyRequests)
	}
}

func TestSpendTodayStartsAtTheZonesMidnight(t *testing.T) {
	zone := athensZone(t)
	// 01:00 UTC on the 26th is 03:00 on the 26th in Athens; a call at 23:00 UTC on the 25th is
	// 01:00 on the 26th there: today's in Athens, yesterday's in UTC
	now := time.Date(2026, time.October, 26, 1, 0, 0, 0, time.UTC)
	clockAt := func() time.Time { return now }
	st := store.CreateStore(clockAt)
	world := makeEnv(t)
	insights := CreateInsights(context.Background(), InsightsOptions{
		Env:    world.env,
		Store:  st,
		Now:    clockAt,
		Zone:   func() *time.Location { return zone },
		Runner: func(string, []string) {},
	})
	t.Cleanup(insights.Stop)
	st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.Ts, r.Model = "late", time.Date(2026, time.October, 25, 23, 0, 0, 0, time.UTC).UnixMilli(), "glm-5.3"
		r.Tokens = shared.Tokens{Input: 1_000_000}
	}), nil)
	spend := insights.SpendToday()
	if spend.USD == nil || *spend.USD <= 0 {
		t.Errorf("spend today = %+v, want the call made after the Athens midnight", spend)
	}
}
