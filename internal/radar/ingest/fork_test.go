package ingest_test

// A forked or resumed session copies the earlier assistant messages into a new transcript
// file under a new session id. The fixture (../../testdata/fork) holds one original session
// and its fork copy, which repeats two of its requests and adds one new one; the store must
// answer the three requests the calls represent, never the five lines the files carry.

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// feedTranscriptFile runs one committed transcript file into the store the way the watcher
// does: one parse state per file, every line through the parser, then the records the parser
// emitted into the store.
func feedTranscriptFile(t *testing.T, st *store.Store, name string) {
	t.Helper()
	raw, err := os.ReadFile("../testdata/fork/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	state := ingest.NewTranscriptState()
	identity := ingest.FileIdentity{Kind: shared.KindMain}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		emit := ingest.FeedTranscriptLine(state, identity, line)
		if emit.Session != nil {
			st.UpsertSession(store.SessionUpsert{ID: emit.Session.ID, Cwd: emit.Session.Cwd, Slug: emit.Session.Slug})
		}
		for _, agent := range emit.Agents {
			st.UpsertAgent(store.AgentUpsert{SessionID: agent.SessionID, ID: agent.ID, ParentID: &agent.ParentID, Kind: &agent.Kind})
		}
		captured := make(map[string]store.ContentPair, len(emit.Content))
		for _, content := range emit.Content {
			captured[content.RequestID] = store.ContentPair{Input: content.Input, Output: content.Output}
		}
		for _, request := range emit.Requests {
			var pair *store.ContentPair
			if held, ok := captured[request.ID]; ok {
				pair = &held
			}
			st.AddRequest(request, pair)
		}
	}
}

// forkFixtureTimes is the three moments the fixture's requests were made, from the committed
// lines.
var forkFixtureTimes = []int64{
	time.Date(2026, 8, 3, 9, 0, 26, 0, time.UTC).UnixMilli(),
	time.Date(2026, 8, 3, 9, 0, 52, 0, time.UTC).UnixMilli(),
	time.Date(2026, 8, 3, 9, 3, 10, 0, time.UTC).UnixMilli(),
}

// forkFixtureTokens is each unique request's usage, in fixture order.
var forkFixtureTokens = []shared.Tokens{
	{Input: 120, Output: 64, CacheRead: 1000, CacheWrite: 64},
	{Input: 210, Output: 48, CacheRead: 1200, CacheWrite: 80},
	{Input: 90, Output: 30, CacheRead: 1400, CacheWrite: 40},
}

// pricedForkFixture prices the three unique calls the fixture represents: the expectation the
// store's own totals must meet, built from the fixture's lines alone.
func pricedForkFixture() []cost.PricedRequest {
	priced := make([]cost.PricedRequest, 0, len(forkFixtureTokens))
	for i := range forkFixtureTokens {
		stop := "end_turn"
		priced = append(priced, cost.PricedRequest{
			Model:      "glm-5.3",
			Ts:         forkFixtureTimes[i],
			Tokens:     forkFixtureTokens[i],
			StopReason: &stop,
		})
	}
	return priced
}

// assertForkTotals checks the store against the three unique calls: three request records,
// their token sum, and their cost.
func assertForkTotals(t *testing.T, st *store.Store) {
	t.Helper()
	requests := st.Requests(store.RequestFilter{})
	if len(requests) != 3 {
		t.Errorf("request count = %d, want 3 (the calls the fork repeats are one call each)", len(requests))
	}
	summary := st.Summary()
	want := shared.ZeroTokens
	for i := range forkFixtureTokens {
		want = shared.AddTokens(want, forkFixtureTokens[i])
	}
	if summary.Tokens != want {
		t.Errorf("token totals = %+v, want %+v (three calls' worth)", summary.Tokens, want)
	}
	wantCost := cost.CostOfAll(pricedForkFixture())
	if (summary.CostUsd == nil) != (wantCost == nil) {
		t.Fatalf("summary cost = %v, want %v", summary.CostUsd, wantCost)
	}
	if wantCost != nil && *summary.CostUsd != *wantCost {
		t.Errorf("summary cost = %v, want %v (three calls' worth)", *summary.CostUsd, *wantCost)
	}
}

func TestForkedTranscriptCounts(t *testing.T) {
	t.Run("original first, then its fork copy: the two repeated calls count once", func(t *testing.T) {
		st := newStore()
		feedTranscriptFile(t, st, "transcript-original.jsonl")
		feedTranscriptFile(t, st, "transcript-fork.jsonl")
		assertForkTotals(t, st)
	})

	t.Run("the fork copy first, then the original: same totals either way", func(t *testing.T) {
		st := newStore()
		feedTranscriptFile(t, st, "transcript-fork.jsonl")
		feedTranscriptFile(t, st, "transcript-original.jsonl")
		assertForkTotals(t, st)
	})
}
