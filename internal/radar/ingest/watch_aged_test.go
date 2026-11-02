package ingest_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The calls a pass reads past the store's window wait for their twin until a full pass ends.
// The store's clock is the real one, so everything stamped watchT0 arrives past the window.

// agedRouteLine is a route line for a call the transcript names by the same request id.
func agedRouteLine(t *testing.T, session, requestID string) string {
	t.Helper()
	return oneLine(t, map[string]any{
		"ts": iso(watchT0), "event": "route", "session_id": session, "model": "claude-sonnet-5-5",
		"upstream": "https://api.anthropic.com", "route": "anthropic", "status": 200, "latency_ms": 900,
		"headers": map[string]any{"request-id": requestID},
	})
}

// idsPerCall records, for every request id a store change carries, the record ids it came under.
type idsPerCall struct {
	mu     sync.Mutex
	seen   map[string]map[string]struct{}
	events map[string]int
}

func trackIDs(st *store.Store) *idsPerCall {
	tracker := &idsPerCall{seen: map[string]map[string]struct{}{}, events: map[string]int{}}
	st.OnUpdate(func(change store.Change) {
		tracker.mu.Lock()
		defer tracker.mu.Unlock()
		for _, record := range change.Requests {
			recordIDs(tracker.seen, record)
			tracker.events[record.RequestID]++
		}
	})
	return tracker
}

func recordIDs(seen map[string]map[string]struct{}, record shared.RequestRecord) {
	if seen[record.RequestID] == nil {
		seen[record.RequestID] = map[string]struct{}{}
	}
	seen[record.RequestID][record.ID] = struct{}{}
}

// count answers how many record ids the call came under, and how many changes carried it.
func (p *idsPerCall) count(requestID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen[requestID])
}

// An aged route line read in one pass over the changed files meets its transcript copy read
// in the next: a feed pass leaves the set-aside calls for the full pass to release.
func TestAnAgedCallPairsAcrossTwoFeedPasses(t *testing.T) {
	e := makeEnv(t)
	writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-armed.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-armed")))
	spool := writeText(t, spoolFilePath(t, e.env), oneLine(t, map[string]any{
		"ts": iso(watchT0), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "a",
	}))
	st := newStore()
	st.KeepWindow(store.RecordWindow)
	ids := trackIDs(st)
	startFolderWatcher(t, e, st, ingest.WatcherOptions{})

	appendText(t, spool, agedRouteLine(t, "sess-late", "req_late"))
	waitFor(t, "the route line, no tick", time.Second, func() bool { return ids.sightings("req_late") == 1 })
	writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-late.jsonl"),
		jsonl(t, assistantLine(watchT1, "req_late")))
	waitFor(t, "the transcript copy, no tick", time.Second, func() bool { return ids.sightings("req_late") == 2 })
	if got := ids.count("req_late"); got != 1 {
		t.Errorf("the call reached the store under %d ids, want 1", got)
	}
}

// sightings answers how many times a change carried the call.
func (p *idsPerCall) sightings(requestID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.events[requestID]
}

// The calls one full pass set aside are released when it ends: a copy that arrives in a later
// pass finds no route line to join.
func TestAFullPassReleasesTheCallsItSetAside(t *testing.T) {
	e := makeEnv(t)
	writeText(t, spoolFilePath(t, e.env), agedRouteLine(t, "sess-late", "req_late"))
	writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-armed.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-armed")))
	st := newStore()
	st.KeepWindow(store.RecordWindow)
	ids := trackIDs(st)
	startFolderWatcher(t, e, st, ingest.WatcherOptions{})
	if got := ids.sightings("req_late"); got != 1 {
		t.Fatalf("the first pass carried the route line %d times, want 1", got)
	}

	writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-late.jsonl"),
		jsonl(t, assistantLine(watchT1, "req_late")))
	waitFor(t, "the copy of a released call, no tick", 2*time.Second, func() bool { return ids.sightings("req_late") == 2 })
	if got := ids.count("req_late"); got != 2 {
		t.Errorf("the call reached the store under %d ids, want 2 (the route line released before its copy came)", got)
	}
}
