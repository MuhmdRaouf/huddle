package store

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// agedStore is a store with the record window whose clock reads a day and an hour after the
// route fixture's call, so a call made at routeT arrives past the window. It collects the
// requests every change carries, the stream the history writer upserts by id.
func agedStore(t *testing.T) (*Store, *[]shared.RequestRecord) {
	t.Helper()
	store := CreateStore(func() time.Time { return time.UnixMilli(routeT).Add(RecordWindow + time.Hour) })
	store.KeepWindow(RecordWindow)
	var written []shared.RequestRecord
	store.OnUpdate(func(change Change) { written = append(written, change.Requests...) })
	return store, &written
}

// requireWrittenOnce fails unless the changes carry one call under one id, the last of
// them the merged record both sources made.
func requireWrittenOnce(t *testing.T, written []shared.RequestRecord) {
	t.Helper()
	ids := map[string]struct{}{}
	for _, record := range written {
		ids[record.ID] = struct{}{}
	}
	if len(ids) != 1 {
		t.Fatalf("the changes carry the call under %d ids, want 1: %v", len(ids), ids)
	}
	last := written[len(written)-1]
	if last.Upstream != "api.anthropic.com" || shared.TotalTokens(last.Tokens) != 3+120+9_000+400 || !eqStr(last.StopReason, "tool_use") {
		t.Errorf("the last change is not the merged call: upstream %q, tokens %+v", last.Upstream, last.Tokens)
	}
}

// A call whose route line and transcript copy both arrive past the window reaches the
// history writer once, under one id, whichever source comes first.
func TestAgedCallIsPairedOnce(t *testing.T) {
	cases := []struct {
		name   string
		inputs []shared.RequestRecord
	}{
		{"the route line first", []shared.RequestRecord{
			route(func(r *shared.RequestRecord) { r.AgentID = "a31cd3de580148dc3"; r.StopReason = nil }),
			transcriptCopy(nil),
		}},
		{"the transcript copy first", []shared.RequestRecord{
			transcriptCopy(nil),
			route(func(r *shared.RequestRecord) { r.AgentID = "a31cd3de580148dc3"; r.StopReason = nil }),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, written := agedStore(t)
			for _, record := range tc.inputs {
				store.AddRequest(record, nil)
			}
			requireWrittenOnce(t, *written)
			if got := store.Summary().Requests; got != 0 {
				t.Errorf("the store holds %d requests, want 0", got)
			}
		})
	}
}

// A transcript streams one call as several lines under one id: a sighting after the call
// paired folds into it, so the call stays one row.
func TestAgedCallRepeatedSightingStaysOneCall(t *testing.T) {
	store, written := agedStore(t)
	store.AddRequest(route(func(r *shared.RequestRecord) { r.AgentID = "a31cd3de580148dc3"; r.StopReason = nil }), nil)
	store.AddRequest(transcriptCopy(nil), nil)
	store.AddRequest(transcriptCopy(nil), nil)
	requireWrittenOnce(t, *written)
	if got := store.Summary().Requests; got != 0 {
		t.Errorf("the store holds %d requests, want 0", got)
	}
}

// A store at rest holds no call set aside for a twin once the pass that read them ends.
func TestReleaseAgedCallsLetsGoOfTheSet(t *testing.T) {
	store, _ := agedStore(t)
	store.AddRequest(transcriptCopy(nil), nil)
	store.mu.RLock()
	held := len(store.agedCalls.held)
	store.mu.RUnlock()
	if held != 1 {
		t.Fatalf("%d calls set aside before the release, want 1", held)
	}
	store.ReleaseAgedCalls()
	store.mu.RLock()
	defer store.mu.RUnlock()
	if got := len(store.agedCalls.held) + len(store.agedCalls.bySlot) + len(store.agedCalls.byRequest); got != 0 {
		t.Errorf("%d entries left after the release, want 0", got)
	}
	if got := len(store.requests); got != 0 {
		t.Errorf("%d requests held, want 0", got)
	}
}

// catchUpAged feeds a catch-up of aged calls in the order a pass reads them, every route line
// before any transcript copy, and answers how many calls reached the history writer under more
// than one id and how many allocations the feed took.
func catchUpAged(calls int) (split int, mallocs uint64) {
	clock := time.UnixMilli(routeT).Add(10 * 24 * time.Hour)
	store := CreateStore(func() time.Time { return clock })
	store.KeepWindow(RecordWindow)
	idsOf := map[string]map[string]struct{}{}
	store.OnUpdate(func(change Change) {
		for _, record := range change.Requests {
			if idsOf[record.RequestID] == nil {
				idsOf[record.RequestID] = map[string]struct{}{}
			}
			idsOf[record.RequestID][record.ID] = struct{}{}
		}
	})
	at := func(i int) int64 { return routeT + int64(i)*60_000 }
	routes := make([]shared.RequestRecord, calls)
	copies := make([]shared.RequestRecord, calls)
	for i := range calls {
		routes[i] = route(func(r *shared.RequestRecord) {
			r.ID, r.RequestID, r.Ts = fmt.Sprintf("route:%d", i), fmt.Sprintf("req_%d", i), at(i)
		})
		copies[i] = transcriptCopy(func(r *shared.RequestRecord) {
			r.ID, r.RequestID, r.Ts = fmt.Sprintf("req_%d", i), fmt.Sprintf("req_%d", i), at(i)+40
		})
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for _, record := range routes {
		store.AddRequest(record, nil)
	}
	for _, record := range copies {
		store.AddRequest(record, nil)
	}
	runtime.ReadMemStats(&after)
	for _, ids := range idsOf {
		if len(ids) != 1 {
			split++
		}
	}
	if len(idsOf) != calls {
		split += calls - len(idsOf)
	}
	return split, after.Mallocs - before.Mallocs
}

// A catch-up reads every route line of the spool before any transcript: each call of days that
// arrive past the window waits for its copy, however many calls that is, and reaches the
// history writer under one id.
func TestAgedCatchUpPairsEveryCallOnce(t *testing.T) {
	if split, _ := catchUpAged(3_000); split != 0 {
		t.Errorf("%d of 3000 calls reached the history writer under more than one id", split)
	}
}

// The work of a catch-up grows with its calls: ten times the calls take under fifteen times
// the allocations.
func TestAgedCatchUpStaysLinear(t *testing.T) {
	_, small := catchUpAged(300)
	_, large := catchUpAged(3_000)
	if large >= small*15 {
		t.Errorf("3000 calls took %d allocations against %d for 300: %.1f times, want under 15",
			large, small, float64(large)/float64(small))
	}
	t.Logf("300 calls: %d allocations, 3000 calls: %d (%.1f times)", small, large, float64(large)/float64(small))
}
