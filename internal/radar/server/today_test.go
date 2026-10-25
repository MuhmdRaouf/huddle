package server

import (
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The today route: the Overview's requests per UTC hour and its costliest sessions, read
// back from the usage fixture's three days.

// todayReport is the whole answer.
type todayReport struct {
	Date           string         `json:"date"`
	HourlyRequests []int64        `json:"hourlyRequests"`
	Sessions       []usageSession `json:"sessions"`
	Approximate    bool           `json:"approximate"`
	Unpriced       int            `json:"unpriced"`
}

func TestTodayRoute(t *testing.T) {
	t.Run("503s when the history failed to open", func(t *testing.T) {
		answer := historyGet(t, nil, "/api/usage/today", nil, nil)
		if answer.Status != 503 {
			t.Fatalf("status = %d, want 503", answer.Status)
		}
	})

	t.Run("counts today's requests per UTC hour and ranks today's sessions by cost", func(t *testing.T) {
		h := openTestHistory(t)
		seedUsage(t, h)
		answer := historyGet(t, h, "/api/usage/today", nil, nil)
		if answer.Status != 200 {
			t.Fatalf("status = %d, want 200", answer.Status)
		}
		var report todayReport
		decodeBody(t, answer, &report)
		if report.Date != usageDayDate(0) {
			t.Errorf("date = %s, want %s", report.Date, usageDayDate(0))
		}
		// today holds ur3 at 08:00, ur5 at 09:45 and ur6 at 10:15; yesterday's rows stay out
		if len(report.HourlyRequests) != 24 {
			t.Fatalf("hours = %d, want 24", len(report.HourlyRequests))
		}
		for hour, got := range report.HourlyRequests {
			want := int64(0)
			if hour == 8 || hour == 9 || hour == 10 {
				want = 1
			}
			if got != want {
				t.Errorf("hour %02d = %d requests, want %d", hour, got, want)
			}
		}
		// u1's flash call (0.73), u2's glm-5.3 call (0.57), u3's unpriced one last
		if len(report.Sessions) != 3 {
			t.Fatalf("sessions = %+v, want u1, u2 and u3", report.Sessions)
		}
		for i, want := range []struct {
			id, label string
			cost      float64
		}{
			{"u1", "Migrate the build", 0.73},
			{"u2", "Tune the radar page", 0.57},
			{"u3", "Try the new provider", 0},
		} {
			got := report.Sessions[i]
			if got.SessionID != want.id || got.Label != want.label || got.Requests != 1 {
				t.Errorf("sessions[%d] = %+v, want %s %q with today's one request", i, got, want.id, want.label)
			}
			wantMoney(t, "session "+want.id+" cost", got.CostUSD, want.cost)
		}
		if !report.Approximate || report.Unpriced != 1 {
			t.Errorf("flags = approximate %v unpriced %d, want true and 1", report.Approximate, report.Unpriced)
		}
	})

	t.Run("keeps the ten costliest sessions", func(t *testing.T) {
		h := openTestHistory(t)
		for i := range 12 {
			id := string(rune('a' + i))
			seedTodayCall(t, h, id, int64(1_000*(i+1)))
		}
		var report todayReport
		decodeBody(t, historyGet(t, h, "/api/usage/today", nil, nil), &report)
		if len(report.Sessions) != 10 {
			t.Fatalf("sessions = %d, want 10", len(report.Sessions))
		}
		if report.Sessions[0].SessionID != "l" || report.Sessions[9].SessionID != "c" {
			t.Errorf("sessions run %s..%s, want the costliest l down to c", report.Sessions[0].SessionID, report.Sessions[9].SessionID)
		}
	})
}

// seedTodayCall writes one glm-5.3 call of `input` tokens for its own session an hour before
// the test clock.
func seedTodayCall(t *testing.T, h *history.History, session string, input int64) {
	t.Helper()
	if err := h.UpsertNode(history.NodeUpsert{ID: session + "/main", Kind: shared.NodeMain, SessionID: session, AgentID: "main"}); err != nil {
		t.Fatalf("upsert the node %s: %v", session, err)
	}
	record := shared.RequestRecord{
		ID: session + "-r", SessionID: session, AgentID: "main", Model: "glm-5.3",
		Upstream: "https://api.z.ai", Provider: "zai", Ts: nowAt.Add(-time.Hour).UnixMilli(),
		Tokens: shared.Tokens{Input: input},
	}
	if err := h.PutRequest(session+"/main", record); err != nil {
		t.Fatalf("put the request of %s: %v", session, err)
	}
}
