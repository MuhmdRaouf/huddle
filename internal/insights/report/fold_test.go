package report

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// busyWeek is a week of many one-request sessions, enough that the whole structure is far over
// the cap: session n spends n tokens, so the busiest session is the last one.
func busyWeek(sessions int) *fakeStores {
	f := &fakeStores{}
	for n := 1; n <= sessions; n++ {
		id := fmt.Sprintf("s-%05d", n)
		day := time.Duration(n%7) * 24 * time.Hour
		f.requests = append(f.requests, req(id, "glm-5.3", "zai", -day, 100, 200, Tokens{Input: int64(n), Output: 1}, 0.001))
		f.sessions = append(f.sessions, Session{ID: id, Repo: fmt.Sprintf("/work/repo-%d", n%40), Start: at(-day)})
	}
	return f
}

func estimateOf(t *testing.T, s Structure) int64 {
	t.Helper()
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return EstimateTokens(payload)
}

func TestBuildFoldsAWindowOverTheCapIntoDays(t *testing.T) {
	const sessions = 5000
	f := busyWeek(sessions)
	s, labels := build(t, f, LastDays(t0, 7))

	if got := estimateOf(t, s); got > MaxInputTokens {
		t.Fatalf("the payload is about %d tokens, want at most %d", got, MaxInputTokens)
	}
	if len(s.Days) != 7 || len(s.Sessions) != 0 {
		t.Fatalf("%d days and %d top-level sessions, want 7 days and none", len(s.Days), len(s.Sessions))
	}
	if s.Totals.Sessions != sessions || s.Totals.Requests != sessions {
		t.Fatalf("totals = %+v, want every session and request kept in the totals", s.Totals)
	}
	var requests int
	for _, d := range s.Days {
		requests += d.Totals.Requests
		if len(d.Sessions) == 0 || len(d.Sessions) > daySessions {
			t.Errorf("day %s lists %d sessions, want 1 to %d", d.Day, len(d.Sessions), daySessions)
		}
	}
	if requests != sessions {
		t.Errorf("the days hold %d requests, want %d", requests, sessions)
	}
	busiest := labels.Session(fmt.Sprintf("s-%05d", sessions))
	found := false
	for _, d := range s.Days {
		if len(d.Sessions) > 0 && d.Sessions[0].Label == busiest {
			found = true
		}
	}
	if !found {
		t.Errorf("no day lists the busiest session %s first; its label must match the local map", busiest)
	}
}

func TestBuildKeepsAWindowUnderTheCapWhole(t *testing.T) {
	s, _ := build(t, fixture(), LastDays(t0, 7))
	if len(s.Days) != 0 || len(s.Sessions) != 3 {
		t.Fatalf("%d days and %d sessions, want the whole structure: no days, 3 sessions", len(s.Days), len(s.Sessions))
	}
}

func TestEstimateTokensRoundsUpThreeBytesAToken(t *testing.T) {
	for size, want := range map[int]int64{0: 0, 1: 1, 3: 1, 4: 2, 300: 100} {
		if got := EstimateTokens(make([]byte, size)); got != want {
			t.Errorf("EstimateTokens(%d bytes) = %d, want %d", size, got, want)
		}
	}
}
