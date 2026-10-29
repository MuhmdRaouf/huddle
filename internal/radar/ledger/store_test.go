package ledger

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The store scenarios, run against a ledger.db in a temp directory only.

func TestStoreRoundTripsARow(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	row := Row{
		ID:             "req_full",
		AnthropicID:    "req_full",
		StartedAt:      1761386400123,
		TtfbMs:         i64p(21),
		TtftMs:         i64p(300),
		DurationMs:     4123,
		Lane:           "provider:zai",
		Harness:        "claude",
		Model:          "glm-5.3-flash",
		FallbackFrom:   "claude-sonnet-5-5",
		Upstream:       "api.z.ai",
		SessionID:      "ses_full",
		AgentID:        "agt_full",
		ParentAgentID:  "par_full",
		PromptID:       "pr_full",
		TurnIndex:      i64p(3),
		PrevRequest:    "req_prev",
		QuerySource:    "user",
		Outcome:        "ok",
		Status:         200,
		ErrorCode:      "",
		StopReason:     "end_turn",
		Input:          120,
		Output:         45,
		CacheRead:      7,
		CacheWrite:     9,
		UsageSource:    "router",
		PriceUSD:       0.0042,
		HarnessCostUSD: f64p(0.0123),
		Ratelimit5h:    f64p(0.25),
		Ratelimit7d:    f64p(0.5),
		PromptHash:     "hash_full",
		InTranscript:   true,
	}
	if err := store.Upsert(ctx, row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("get answered no row for the id just written")
	}
	if !reflect.DeepEqual(*got, row) {
		t.Errorf("row =\n %+v\nwant\n %+v", *got, row)
	}
	byAnthropic, err := store.ByAnthropicID(ctx, row.AnthropicID)
	if err != nil {
		t.Fatalf("get by anthropic id: %v", err)
	}
	if byAnthropic == nil {
		t.Fatal("no row for the request id just written")
	}
	if !reflect.DeepEqual(*byAnthropic, row) {
		t.Errorf("row =\n %+v\nwant\n %+v", *byAnthropic, row)
	}

	changed := row
	changed.DurationMs = row.DurationMs + 1
	if err := store.Upsert(ctx, changed); err != nil {
		t.Fatalf("upsert the changed copy: %v", err)
	}
	reread, err := store.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if reread == nil {
		t.Fatal("the changed copy vanished")
	}
	if !reflect.DeepEqual(*reread, changed) {
		t.Errorf("row =\n %+v\nwant\n %+v", *reread, changed)
	}

	missing, err := store.Get(ctx, "no_such_request")
	if err != nil {
		t.Fatalf("get an unknown id: %v", err)
	}
	if missing != nil {
		t.Errorf("an unknown id answered %#v, want nil", missing)
	}
}

// openLedger opens a ledger.db in a temp directory, closed when the test ends.
func openLedger(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestTheLedgerJoinsARouteAndItsTranscriptInEitherOrder: a route and its transcript land in
// either order, and either order leaves the same one row — the router's usage and price over
// the transcript's counters, the transcript's stop reason over the route's silence. A
// transcript filed first sits under its request id, so a route whose answer carried that id
// as its request-id header joins it there.
func TestTheLedgerJoinsARouteAndItsTranscriptInEitherOrder(t *testing.T) {
	route := RouteFacts{
		Event: domain.RouteEvent{
			Ts:        "2026-10-25T12:00:00.123Z",
			Event:     domain.EventRoute,
			Plugin:    "zai",
			Model:     "glm-5.3",
			Upstream:  "api.z.ai",
			Route:     "provider",
			Status:    200,
			LatencyMs: 42,
			Usage: &domain.RouteUsage{
				"input_tokens":  float64(100),
				"output_tokens": float64(20),
			},
			Headers: &map[string]string{"request-id": "req_1"},
		},
		PriceUSD: 0.5,
	}
	transcript := TranscriptFacts{
		RequestID:  "req_1",
		StopReason: "end_turn",
		Usage: domain.RouteUsage{
			"input_tokens":  float64(90),
			"output_tokens": float64(20),
		},
	}
	started, err := time.Parse(time.RFC3339, route.Event.Ts)
	if err != nil {
		t.Fatalf("parse the route ts: %v", err)
	}
	want := Row{
		ID:           "req_1",
		AnthropicID:  "req_1",
		StartedAt:    started.UnixMilli(),
		DurationMs:   42,
		Lane:         "provider:zai",
		Harness:      "claude",
		Model:        "glm-5.3",
		Upstream:     "api.z.ai",
		Outcome:      "ok",
		Status:       200,
		StopReason:   "end_turn",
		Input:        100,
		Output:       20,
		Headers:      map[string]string{"request-id": "req_1"},
		Provider:     "Z.ai",
		Via:          strp("zai"),
		NodeID:       "outside-a-session/main",
		UsageSource:  "router",
		PriceUSD:     0.5,
		InTranscript: true,
	}

	t.Run("route first, then the transcript", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		if err := st.AddRoute(ctx, route); err != nil {
			t.Fatalf("add route: %v", err)
		}
		if err := st.AddTranscript(ctx, transcript); err != nil {
			t.Fatalf("add transcript: %v", err)
		}
		assertLedgerRow(t, st, ctx, want)
	})

	t.Run("transcript first, then the route", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		if err := st.AddTranscript(ctx, transcript); err != nil {
			t.Fatalf("add transcript: %v", err)
		}
		if err := st.AddRoute(ctx, route); err != nil {
			t.Fatalf("add route: %v", err)
		}
		assertLedgerRow(t, st, ctx, want)
	})

	t.Run("a route without a request id names its row by its prompt hash and ts", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		hashed := route
		hashed.Event.Headers = nil
		hashed.Event.PromptHash = strp("hash7")
		if err := st.AddRoute(ctx, hashed); err != nil {
			t.Fatalf("add route: %v", err)
		}
		row, err := st.Get(ctx, "hash7:"+hashed.Event.Ts)
		if err != nil {
			t.Fatalf("get the hash-named row: %v", err)
		}
		if row == nil || row.Model != "glm-5.3" || row.Input != 100 {
			t.Fatalf("row = %+v, want the route filed under hash7:<ts>", row)
		}
	})

	t.Run("a route with neither a request id nor a prompt hash files nothing", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		anonymous := route
		anonymous.Event.Headers = nil
		if err := st.AddRoute(ctx, anonymous); err != nil {
			t.Fatalf("add route: %v", err)
		}
		row, err := st.Get(ctx, "")
		if err != nil {
			t.Fatalf("get the empty id: %v", err)
		}
		if row != nil {
			t.Fatalf("an unnamed route filed %+v, want no row", row)
		}
	})
}

// TestTheLedgerKeepsTheStickyCaptureFacts: a later transcript copy never loses what an
// earlier one captured — a copy with no what line keeps the held one, and once a copy
// carries the result's totals every later copy totals too.
func TestTheLedgerKeepsTheStickyCaptureFacts(t *testing.T) {
	t.Run("a later copy with no what keeps the held what", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		first := TranscriptFacts{RequestID: "req_sticky", What: strp("a")}
		if err := st.AddTranscript(ctx, first); err != nil {
			t.Fatalf("add the first transcript: %v", err)
		}
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_sticky"}); err != nil {
			t.Fatalf("add the second transcript: %v", err)
		}
		row, err := st.Get(ctx, "req_sticky")
		if err != nil || row == nil {
			t.Fatalf("read the row: %+v, %v", row, err)
		}
		if row.What == nil || *row.What != "a" {
			t.Errorf("What = %s, want a", ptrShow(row.What))
		}
	})

	t.Run("a later copy keeps the held totals", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_totals", Totals: true}); err != nil {
			t.Fatalf("add the first transcript: %v", err)
		}
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_totals"}); err != nil {
			t.Fatalf("add the second transcript: %v", err)
		}
		row, err := st.Get(ctx, "req_totals")
		if err != nil || row == nil {
			t.Fatalf("read the row: %+v, %v", row, err)
		}
		if !row.Totals {
			t.Error("Totals = false, want the held true")
		}
	})
}

// TestTheLedgerKeepsTheHeldTsOnALaterTranscriptCopy: a later transcript copy never moves
// the row's start — a copy that says nothing about when the request was made keeps the held
// copy's ts, and a copy stamped later keeps the first copy's ts.
func TestTheLedgerKeepsTheHeldTsOnALaterTranscriptCopy(t *testing.T) {
	t.Run("a later copy with no ts keeps the held ts", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_sticky_ts", Ts: 1730000000000}); err != nil {
			t.Fatalf("add the first transcript: %v", err)
		}
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_sticky_ts"}); err != nil {
			t.Fatalf("add the second transcript: %v", err)
		}
		row, err := st.Get(ctx, "req_sticky_ts")
		if err != nil || row == nil {
			t.Fatalf("read the row: %+v, %v", row, err)
		}
		if row.StartedAt != 1730000000000 {
			t.Errorf("StartedAt = %d, want the held 1730000000000", row.StartedAt)
		}
	})

	t.Run("a later copy stamped later keeps the first ts", func(t *testing.T) {
		st := openLedger(t)
		ctx := context.Background()
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_first_ts", Ts: 1730000000000}); err != nil {
			t.Fatalf("add the first transcript: %v", err)
		}
		if err := st.AddTranscript(ctx, TranscriptFacts{RequestID: "req_first_ts", Ts: 1730000005000}); err != nil {
			t.Fatalf("add the second transcript: %v", err)
		}
		row, err := st.Get(ctx, "req_first_ts")
		if err != nil || row == nil {
			t.Fatalf("read the row: %+v, %v", row, err)
		}
		if row.StartedAt != 1730000000000 {
			t.Errorf("StartedAt = %d, want the first copy's 1730000000000", row.StartedAt)
		}
	})
}

// TestStoreRoundTripsTheCaptureColumns: a row with all four capture columns set reads back
// equal through Get, the headers map round-tripping through its JSON.
func TestStoreRoundTripsTheCaptureColumns(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	row := Row{
		ID:       "req_capture",
		What:     strp("↳ prompt: hi  → Bash npm test"),
		Thinking: i64p(16),
		Headers:  map[string]string{"request-id": "req_capture", "x-ratelimit-remaining": "99"},
		Totals:   true,
	}
	if err := st.Upsert(ctx, row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := st.Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("get answered no row for the id just written")
	}
	if !reflect.DeepEqual(*got, row) {
		t.Errorf("row =\n %+v\nwant\n %+v", *got, row)
	}
}

// assertLedgerRow reads the row back by both of its names and holds it against want.
func assertLedgerRow(t *testing.T, st *Store, ctx context.Context, want Row) {
	t.Helper()
	row, err := st.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("get %s: %v", want.ID, err)
	}
	if row == nil {
		t.Fatalf("no row for %s", want.ID)
	}
	if !reflect.DeepEqual(*row, want) {
		t.Errorf("row =\n %+v\nwant\n %+v", *row, want)
	}
	byAnthropic, err := st.ByAnthropicID(ctx, want.AnthropicID)
	if err != nil {
		t.Fatalf("get by anthropic id: %v", err)
	}
	if byAnthropic == nil || !reflect.DeepEqual(*byAnthropic, want) {
		t.Errorf("by anthropic id =\n %+v\nwant\n %+v", byAnthropic, want)
	}
}

// TestARouteFactsBlobUnderItsTagDecodes: ledger_facts stores RouteFacts as the JSON its
// tags name, so a blob holding the price under its stored key decodes into the field that
// carries it.
func TestARouteFactsBlobUnderItsTagDecodes(t *testing.T) {
	blob := `{"event":{"ts":"2026-10-25T12:00:00Z","event":"route","model":"glm-5.3"},"cost_usd":0.12}`
	var facts RouteFacts
	if err := json.Unmarshal([]byte(blob), &facts); err != nil {
		t.Fatalf("decode the stored blob: %v", err)
	}
	if facts.PriceUSD != 0.12 {
		t.Errorf("PriceUSD = %v, want 0.12", facts.PriceUSD)
	}
	if facts.Event.Model != "glm-5.3" {
		t.Errorf("Event.Model = %q, want glm-5.3", facts.Event.Model)
	}
}
