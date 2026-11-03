package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// This machine's live counts, taken from structure-only counts of its transcripts: the
// requests and tool calls inside the 24-hour record window, spread over the sessions the
// store holds at its cap. A catch-up walks far more transcripts than the cap, so nearly
// every new session drops a stale one.
const (
	liveWindowRequests = 44_561
	liveWindowTools    = 36_159
	liveTranscripts    = 1_702

	// liveSessionRequests and liveSessionTools are one session's share of the live counts
	// at the 200-session cap, rounded up.
	liveSessionRequests = (liveWindowRequests + shared.MaxSessions - 1) / shared.MaxSessions
	liveSessionTools    = (liveWindowTools + shared.MaxSessions - 1) / shared.MaxSessions

	// evictDropped is how many records-heavy sessions a cost round drops or sheds from; the
	// rest of the store is where the two sizes differ.
	evictDropped = 8
	evictRounds  = 5

	// evictCostRatio is how many times dearer the same drops may get on the live-size store
	// than on one a twentieth of its size. A cost that follows what is dropped keeps the
	// ratio near 1; a pass over every held record puts it near the sizes' own ratio.
	evictCostRatio = 4.0
)

// evictFixture is a store of 200 sessions in staleness order. The stalest evictDropped
// sessions are always heavy, with the live per-session shares of requests and tool calls;
// the rest are heavy at live size and one request and one tool call each in the small
// store, so the two stores drop the same records and differ in everything they keep.
func evictFixture(t *testing.T, live bool) *Store {
	t.Helper()
	s := newTestStore()
	s.KeepWindow(RecordWindow)
	for i := 0; i < shared.MaxSessions; i++ {
		requests, tools := 1, 1
		if live || i < evictDropped {
			requests, tools = liveSessionRequests, liveSessionTools
		}
		session := fmt.Sprintf("s%03d", i)
		for j := 0; j < requests; j++ {
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("req-%03d-%04d", i, j)
				r.SessionID = session
				r.Ts = ts0 + int64(i)*1_000 + int64(j)
			}), nil)
		}
		for j := 0; j < tools; j++ {
			s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
				c.ID = fmt.Sprintf("tool-%03d-%04d", i, j)
				c.SessionID = session
				c.StartedAt = ts0 + int64(i)*1_000 + int64(j)
			}))
		}
	}
	return s
}

// bestOf runs a cost round on a fresh store evictRounds times and answers the cheapest
// round: the least disturbed reading of what the operation costs.
func bestOf(t *testing.T, live bool, round func(s *Store)) time.Duration {
	t.Helper()
	best := time.Duration(1 << 62)
	for i := 0; i < evictRounds; i++ {
		s := evictFixture(t, live)
		start := time.Now()
		round(s)
		best = min(best, time.Since(start))
	}
	return best
}

// requireCostFollowsWhatIsDropped runs one round on the live-size store and on the small
// one and fails when the live-size round costs more than evictCostRatio times the small one.
func requireCostFollowsWhatIsDropped(t *testing.T, round func(s *Store)) {
	t.Helper()
	small := bestOf(t, false, round)
	full := bestOf(t, true, round)
	ratio := float64(full) / float64(small)
	t.Logf("live-size store %v, store a twentieth the size %v: ratio %.1f", full, small, ratio)
	if ratio >= evictCostRatio {
		t.Errorf("the same drops cost %.1f times more on the live-size store (%v against %v), want under %.0f: eviction walks the store, not what it drops",
			ratio, full, small, evictCostRatio)
	}
}

// A catch-up stream: each new session lands a request, then its upsert drops the stalest
// session and everything that session owns. The cost of a drop follows the session dropped.
func TestEvictingASessionCostsWhatItDropsNotWhatTheStoreHolds(t *testing.T) {
	requireCostFollowsWhatIsDropped(t, func(s *Store) {
		for i := 0; i < evictDropped; i++ {
			id := fmt.Sprintf("new-%03d", i)
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.SessionID, r.Ts = "req-"+id, id, ts0+dayMs/2+int64(i)
			}), nil)
			s.UpsertSession(SessionUpsert{ID: id})
		}
	})
}

// A catch-up of aged records: each request arrives dated past the window and is shed the
// moment it lands. The cost of a shed follows the records shed.
func TestShedAgedRecordsCostsWhatItShedsNotWhatTheStoreHolds(t *testing.T) {
	requireCostFollowsWhatIsDropped(t, func(s *Store) {
		aged := ts9 - RecordWindow.Milliseconds() - dayMs
		for i := 0; i < liveTranscripts/10; i++ {
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.SessionID, r.Ts = fmt.Sprintf("aged-%04d", i), "s000", aged+int64(i)
			}), nil)
		}
	})
}

// The sessions a catch-up drops leave no trace in the list readers walk, and the survivors
// keep the order they arrived in.
func TestEvictingASessionLeavesTheSurvivorsInArrivalOrder(t *testing.T) {
	s := evictFixture(t, false)
	before := requestIDsInOrder(s)
	for i := 0; i < evictDropped; i++ {
		id := fmt.Sprintf("new-%03d", i)
		s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "req-"+id, id, ts0+dayMs/2+int64(i)
		}), nil)
		s.UpsertSession(SessionUpsert{ID: id})
	}
	var want []string
	for _, id := range before {
		if !isStalest(id) {
			want = append(want, id)
		}
	}
	for i := 0; i < evictDropped; i++ {
		want = append(want, fmt.Sprintf("req-new-%03d", i))
	}
	if got := requestIDsInOrder(s); !slices.Equal(got, want) {
		t.Errorf("the arrival list holds %d requests, want the %d that survived, in arrival order", len(got), len(want))
	}
}

// isStalest names a request id of one of the evictDropped stalest sessions.
func isStalest(id string) bool {
	for i := 0; i < evictDropped; i++ {
		if strings.HasPrefix(id, fmt.Sprintf("req-%03d-", i)) {
			return true
		}
	}
	return false
}

// Dropping a session takes that session's slice out of the built projection and leaves the
// others standing; evicting one record leaves only its own session's slice to build again.
func TestEvictionLeavesOtherSessionsSlicesStanding(t *testing.T) {
	s := newTestStore()
	for _, session := range []string{"a", "b", "c"} {
		s.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.SessionID, r.Ts = "r-"+session, session, ts0 }), nil)
		s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) { c.ID, c.SessionID, c.StartedAt = "t-"+session, session, ts0 }))
	}
	slices := func() map[string]*sessionSlice {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make(map[string]*sessionSlice)
		for id, slice := range s.projectionLocked() {
			out[id] = slice
		}
		return out
	}

	before := slices()
	s.mu.Lock()
	s.dropSessionLocked("a")
	s.mu.Unlock()
	after := slices()
	if _, ok := after["a"]; ok {
		t.Errorf("session a was dropped and still has a slice")
	}
	if after["b"] != before["b"] || after["c"] != before["c"] {
		t.Errorf("sessions b and c, untouched by dropping a, were rebuilt")
	}

	before = after
	s.mu.Lock()
	s.applyEvictionsLocked([]evictionCandidate{{source: 0, id: "r-b"}})
	s.mu.Unlock()
	after = slices()
	if got := len(after["b"].requests); got != 0 {
		t.Errorf("session b holds %d requests after its request was evicted, want 0", got)
	}
	if after["c"] != before["c"] {
		t.Errorf("session c, untouched by evicting b's request, was rebuilt")
	}
}

// Records for more sessions than the cap, none of them ever upserted (a transcript whose
// opening lines are cut, a sidechain, a spool record without its user line), still leave the
// store, its projection and its session list at the cap, the stalest sessions gone.
func TestRecordsWithoutAnUpsertStillHoldTheSessionCap(t *testing.T) {
	cases := []struct {
		name string
		add  func(s *Store, session string, at int64)
	}{
		{"requests", func(s *Store, session string, at int64) {
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.SessionID, r.Ts = "req-"+session, session, at }), nil)
		}},
		{"tool calls", func(s *Store, session string, at int64) {
			s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) { c.ID, c.SessionID, c.StartedAt = "tool-"+session, session, at }))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore()
			over := shared.MaxSessions + 40
			for i := 0; i < over; i++ {
				tc.add(s, fmt.Sprintf("s%04d", i), ts0+int64(i))
			}
			readProjection(s)
			s.mu.RLock()
			defer s.mu.RUnlock()
			if got := len(s.sessions); got != shared.MaxSessions {
				t.Errorf("the store holds %d sessions, want the cap %d", got, shared.MaxSessions)
			}
			if got := len(s.proj); got > shared.MaxSessions {
				t.Errorf("the projection holds %d sessions, want at most %d", got, shared.MaxSessions)
			}
			if _, held := s.sessions["s0000"]; held {
				t.Errorf("the stalest session survived")
			}
			if _, held := s.sessions[fmt.Sprintf("s%04d", over-1)]; !held {
				t.Errorf("the newest session was dropped")
			}
		})
	}
}
