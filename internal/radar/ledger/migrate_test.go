package ledger

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The migrate match: a fixture history.db holds one requests row per match-rule shape,
// and every test reads and matches those rows against a ledger in a temp directory only.

// The fixture rows' ts values, in Unix milliseconds: fixed, so the rule-4 id's spelling
// is exact.
const (
	fixtureCodexTs      = int64(1785747600000) // 2026-08-03T09:00:00.000Z
	fixtureTranscriptTs = int64(1785747620000) // 2026-08-03T09:00:20.000Z
	fixtureRefusedTs    = int64(1785747624000) // 2026-08-03T09:00:24.000Z
	fixtureHeaderTs     = int64(1785747626000) // 2026-08-03T09:00:26.000Z
	fixtureRouteTs      = int64(1785747630000) // 2026-08-03T09:00:30.000Z
	fixtureMergedTs     = int64(1785747640000) // 2026-08-03T09:00:40.000Z
	fixtureSubTs        = int64(1785747645000) // 2026-08-03T09:00:45.000Z
)

// fixtureRecords answers the seven requests rows the fixture stores, one per shape: a
// harness record, a merged copy-first row, an id-less copy, a transcript-only request id,
// a header-less hash-less refusal, a header row and a route-only hash row.
func fixtureRecords() []shared.RequestRecord {
	return []shared.RequestRecord{
		{
			ID: "codex:s1:1", Harness: "codex", SessionID: "s1",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: fixtureCodexTs,
			Tokens: shared.Tokens{Input: 10, Output: 5}, Totals: true,
			CostUSD: f64p(0.01),
		},
		{
			ID: "msg_m1", SessionID: "sess-m1", AgentID: "main",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: fixtureMergedTs,
			LatencyMs: i64p(900), Tokens: shared.Tokens{Input: 100, Output: 20, CacheWrite: 40},
			CacheWrite1h: i64p(5), Speed: strp("fast"), StopReason: strp("end_turn"),
			Provider: "Z.ai", Route: strp("provider"), Via: strp("zai"), Status: strp("200"),
			PromptHash: strp("ph2"), CostUSD: f64p(0.25),
		},
		{
			ID: "msg_sub1", SessionID: "sess-m1", AgentID: "sub", ParentAgentID: strp("main"),
			Model: "glm-5.3", Ts: fixtureSubTs,
			Tokens: shared.Tokens{Input: 7, Output: 3},
		},
		{
			ID: "req_t1", SessionID: "sess-t", AgentID: "main",
			Model: "claude-sonnet-5-5", Upstream: "api.anthropic.com", Ts: fixtureTranscriptTs,
			Tokens: shared.Tokens{Input: 11, Output: 4}, StopReason: strp("end_turn"),
		},
		{
			ID: "route:sess-a::glm-5.3:1785747624000:429", SessionID: "sess-a", AgentID: "main",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: fixtureRefusedTs,
			Route: strp("refused"), Status: strp("429"), Error: strp("rate_limited"),
		},
		{
			ID: "route:sess-a:main:glm-5.3:1785747626000:200", SessionID: "sess-a", AgentID: "main",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: fixtureHeaderTs,
			LatencyMs: i64p(1200), Tokens: shared.Tokens{Input: 50, Output: 10},
			Provider: "Z.ai", Route: strp("provider"), Via: strp("zai"), Status: strp("200"),
			Headers: &shared.Headers{{Name: "request-id", Value: "req_h1"}},
		},
		{
			ID: "route:sess-b:main:claude-sonnet-5-5:1785747630000:200", SessionID: "sess-b", AgentID: "main",
			Model: "claude-sonnet-5-5", Upstream: "api.anthropic.com", Ts: fixtureRouteTs,
			LatencyMs: i64p(800), Tokens: shared.Tokens{Input: 30, Output: 6},
			Route: strp("anthropic"), Status: strp("200"), PromptHash: strp("ph1"),
		},
	}
}

// writeFixtureHistory creates a history.db in a temp directory, stores the fixture
// requests through the real history store and closes it; it answers the file's path.
func writeFixtureHistory(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "history.db")
	h, err := history.OpenHistory(file, nil)
	if err != nil {
		t.Fatalf("open the fixture history: %v", err)
	}
	for _, record := range fixtureRecords() {
		if err := h.PutRequest("fixture/"+record.ID, record); err != nil {
			t.Fatalf("put the fixture request %s: %v", record.ID, err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close the fixture history: %v", err)
	}
	return file
}

// readFixtureRows reads every fixture requests row back through the history reader.
func readFixtureRows(t *testing.T) []HistoryRow {
	t.Helper()
	reader := openHistoryReader(t, writeFixtureHistory(t))
	rows, err := reader.HistoryPage(context.Background(), "", 1000)
	if err != nil {
		t.Fatalf("read the fixture rows: %v", err)
	}
	return rows
}

// TestMatchOfNamesEachShape: on an empty ledger every fixture row answers its rule and
// the id it files under, none of them held.
func TestMatchOfNamesEachShape(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	rows := readFixtureRows(t)
	want := []Match{
		{ID: "codex:s1:1", Rule: 1},
		{ID: "msg_m1", Rule: 5},
		{ID: "msg_sub1", Rule: 6},
		{ID: "req_t1", Rule: 3},
		{ID: "route:sess-a::glm-5.3:1785747624000:429", Rule: 6},
		{ID: "req_h1", Rule: 2},
		{ID: "ph1:2026-08-03T09:00:30.000Z", Rule: 4},
	}
	if len(rows) != len(want) {
		t.Fatalf("the fixture holds %d rows, want %d", len(rows), len(want))
	}
	for at, row := range rows {
		match, err := st.MatchOf(ctx, row)
		if err != nil {
			t.Fatalf("match %s: %v", row.ID, err)
		}
		if match != want[at] {
			t.Errorf("%s matched %+v, want %+v", row.ID, match, want[at])
		}
	}
}

// TestMatchOfFindsWhatTheLedgerHolds: when the ledger filed the call live, rules one to
// five answer InLedger true, rule 5 naming the ledger row inside its window — and a route
// eleven seconds away is outside it.
func TestMatchOfFindsWhatTheLedgerHolds(t *testing.T) {
	rows := readFixtureRows(t)
	byID := map[string]HistoryRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	ctx := context.Background()

	t.Run("rules one to five find the row the ledger filed", func(t *testing.T) {
		st := openLedger(t)
		addRoute := func(event domain.RouteEvent) {
			t.Helper()
			if err := st.AddRoute(ctx, RouteFacts{Event: event}); err != nil {
				t.Fatalf("add the route: %v", err)
			}
		}
		// rule 2: the route filed under the row's request-id header
		addRoute(domain.RouteEvent{
			Ts: "2026-08-03T09:00:26.000Z", Event: domain.EventRoute, Plugin: "zai",
			Model: "glm-5.3", Upstream: "api.z.ai", Route: "provider", Status: 200,
			SessionID: strp("sess-a"), AgentID: strp("main"),
			Usage:   &domain.RouteUsage{"input_tokens": float64(50), "output_tokens": float64(10)},
			Headers: &map[string]string{"request-id": "req_h1"},
		})
		// rule 3: the transcript filed under its request id
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_t1", Model: "claude-sonnet-5-5"}); err != nil {
			t.Fatalf("add the transcript: %v", err)
		}
		// rule 4: the route filed under its hash and its own ts, the row's ts
		addRoute(domain.RouteEvent{
			Ts: "2026-08-03T09:00:30.000Z", Event: domain.EventRoute,
			Model: "claude-sonnet-5-5", Upstream: "api.anthropic.com", Route: "anthropic", Status: 200,
			SessionID: strp("sess-b"), AgentID: strp("main"),
			PromptHash: strp("ph1"),
		})
		// rule 1: the harness filed under its record's own id
		if err := st.AddHarness(ctx, HarnessFacts{
			ID: "codex:s1:1", Harness: "codex", SessionID: "s1",
			Model: "glm-5.3", Ts: fixtureCodexTs,
		}); err != nil {
			t.Fatalf("add the harness: %v", err)
		}
		// rule 5: the route filed under its hash and ts, four seconds after the merged
		// copy's ts, same session and model
		addRoute(domain.RouteEvent{
			Ts: "2026-08-03T09:00:44.000Z", Event: domain.EventRoute, Plugin: "zai",
			Model: "glm-5.3", Upstream: "api.z.ai", Route: "provider", Status: 200,
			SessionID: strp("sess-m1"), AgentID: strp("main"),
			PromptHash: strp("ph2"),
		})

		want := map[string]Match{
			"codex:s1:1": {ID: "codex:s1:1", InLedger: true, Rule: 1},
			"route:sess-a:main:glm-5.3:1785747626000:200": {ID: "req_h1", InLedger: true, Rule: 2},
			"req_t1": {ID: "req_t1", InLedger: true, Rule: 3},
			"route:sess-b:main:claude-sonnet-5-5:1785747630000:200": {
				ID: "ph1:2026-08-03T09:00:30.000Z", InLedger: true, Rule: 4,
			},
			"msg_m1": {ID: "msg_m1", InLedger: true, Rule: 5, ClaimedBy: "ph2:2026-08-03T09:00:44.000Z"},
		}
		for id, wantMatch := range want {
			match, err := st.MatchOf(ctx, byID[id])
			if err != nil {
				t.Fatalf("match %s: %v", id, err)
			}
			if match != wantMatch {
				t.Errorf("%s matched %+v, want %+v", id, match, wantMatch)
			}
		}
	})

	t.Run("eleven seconds away the window finds nothing", func(t *testing.T) {
		// the window must stay the live pairing's window: the same ten seconds the
		// store's two copies of one call pair inside
		if matchMs != store.MatchMs {
			t.Errorf("matchMs = %d, want the store's MatchMs %d", matchMs, store.MatchMs)
		}
		st := openLedger(t)
		if err := st.AddRoute(ctx, RouteFacts{Event: domain.RouteEvent{
			Ts: "2026-08-03T09:00:51.000Z", Event: domain.EventRoute, Plugin: "zai",
			Model: "glm-5.3", Upstream: "api.z.ai", Route: "provider", Status: 200,
			SessionID: strp("sess-m1"), AgentID: strp("main"),
			PromptHash: strp("ph2"),
		}}); err != nil {
			t.Fatalf("add the route: %v", err)
		}
		match, err := st.MatchOf(ctx, byID["msg_m1"])
		if err != nil {
			t.Fatalf("match msg_m1: %v", err)
		}
		if want := (Match{ID: "msg_m1", Rule: 5}); match != want {
			t.Errorf("msg_m1 matched %+v, want %+v", match, want)
		}
	})
}

// TestFilingEachRowThenMatchingAgainFindsIt: after every fixture row files under its
// match id, a second match finds each one — the window rule through the row's own hash,
// session, model and ts — and the ledger holds exactly the seven rows.
func TestFilingEachRowThenMatchingAgainFindsIt(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	rows := readFixtureRows(t)
	for _, row := range rows {
		match, err := st.MatchOf(ctx, row)
		if err != nil {
			t.Fatalf("match %s: %v", row.ID, err)
		}
		if err := st.AddHistory(ctx, HistoryFactsOf(row, match.ID)); err != nil {
			t.Fatalf("file %s under %s: %v", row.ID, match.ID, err)
		}
	}
	for _, row := range rows {
		match, err := st.MatchOf(ctx, row)
		if err != nil {
			t.Fatalf("re-match %s: %v", row.ID, err)
		}
		if !match.InLedger {
			t.Errorf("%s (rule %d) filed under %s, and the match still finds nothing", row.ID, match.Rule, match.ID)
		}
	}
	found, err := st.db.Get("SELECT COUNT(*) AS n FROM ledger_requests")
	if err != nil {
		t.Fatalf("count the rows: %v", err)
	}
	if n := intColumn(found, "n"); n != int64(len(rows)) {
		t.Errorf("the ledger holds %d rows, want %d", n, len(rows))
	}
}

// TestHistoryFactsOfCarriesTheRow: the merged row's facts carry the fields the price and
// the dashboard read, and a route or harness id claims no transcript copy.
func TestHistoryFactsOfCarriesTheRow(t *testing.T) {
	rows := readFixtureRows(t)
	byID := map[string]HistoryRow{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	facts := HistoryFactsOf(byID["msg_m1"], "msg_m1")
	if facts.ID != "msg_m1" || facts.Ts != fixtureMergedTs {
		t.Errorf("facts id/ts = %s/%d, want msg_m1/%d", facts.ID, facts.Ts, fixtureMergedTs)
	}
	if facts.Speed == nil || *facts.Speed != "fast" {
		t.Errorf("Speed = %v, want fast", facts.Speed)
	}
	if facts.Via == nil || *facts.Via != "zai" {
		t.Errorf("Via = %v, want zai", facts.Via)
	}
	if facts.Status != "200" {
		t.Errorf("Status = %q, want 200", facts.Status)
	}
	if facts.HarnessCostUSD == nil || *facts.HarnessCostUSD != 0.25 {
		t.Errorf("HarnessCostUSD = %v, want 0.25", facts.HarnessCostUSD)
	}
	if !facts.FromTranscript {
		t.Error("FromTranscript = false for a msg id, want true")
	}
	if facts.PriceUSD <= 0 {
		t.Errorf("PriceUSD = %v, want a priced glm-5.3 call", facts.PriceUSD)
	}
	for _, id := range []string{"route:sess-b:main:claude-sonnet-5-5:1785747630000:200", "codex:s1:1"} {
		if HistoryFactsOf(byID[id], id).FromTranscript {
			t.Errorf("%s facts claim a transcript copy", id)
		}
	}
}
