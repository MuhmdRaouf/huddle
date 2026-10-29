package ingest

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The watcher's feed into the ledger: one route line and one transcript request naming the
// same request id land one row both sources read.

func TestTheWatcherFeedsTheLedger(t *testing.T) {
	home := t.TempDir()
	env := domain.EnvLookup{
		"HOME":              home,
		"RADAR_HOME":        filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
	}
	led, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })

	w := StartWatcher(t.Context(), WatcherOptions{
		Env:        env,
		Store:      store.CreateStore(time.Now),
		Ledger:     led,
		SinceMs:    24 * 3_600_000,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        func() int64 { return t1 },
		Alive:      func(int64) bool { return false },
		NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
	})
	t.Cleanup(w.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for w.CatchingUp() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the initial ingest")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// One route line the router wrote, its answer's request-id header on it.
	route, err := json.Marshal(map[string]any{
		"ts": iso(t1), "event": "route", "session_id": "s9", "model": "glm-5.3",
		"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200, "latency_ms": 42,
		"usage":   map[string]any{"input_tokens": 100, "output_tokens": 20},
		"headers": map[string]string{"request-id": "req_feed"},
	})
	if err != nil {
		t.Fatalf("marshal the route line: %v", err)
	}
	w.ingestSpoolText(string(route))
	// One transcript assistant line naming the same request id.
	w.applyEmit(feedFile(NewTranscriptState(), assistantLine(assistantOverrides{
		requestID: strp("req_feed"),
	}), mainFile))

	row, err := led.ByAnthropicID(t.Context(), "req_feed")
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if row == nil {
		t.Fatal("no ledger row for the request id both the route line and the transcript name")
	}
	if row.Model != "glm-5.3" {
		t.Errorf("Model = %q, want the route line's glm-5.3", row.Model)
	}
	if row.Input != 100 || row.Output != 20 {
		t.Errorf("usage = %d in, %d out, want the route line's 100 and 20 (the router's usage wins)", row.Input, row.Output)
	}
	if row.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want the transcript's end_turn", row.StopReason)
	}
	if !row.InTranscript {
		t.Error("InTranscript = false, want the transcript's arrival to mark it")
	}

	if _, ok := transcriptFactsOf(shared.RequestRecord{}); ok {
		t.Error("transcriptFactsOf a record with no request id = true, want false")
	}
}

// TestTheWatcherFeedsTheLedgerTheNodeAndTheVia: the facts name the node the request lands
// on — the store's answer at ingest — and a transcript-only request names the way its call
// left, the loopback base URL its session's hooks reported before the call.
func TestTheWatcherFeedsTheLedgerTheNodeAndTheVia(t *testing.T) {
	home := t.TempDir()
	env := domain.EnvLookup{
		"HOME":              home,
		"RADAR_HOME":        filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
	}
	led, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })

	w := StartWatcher(t.Context(), WatcherOptions{
		Env:        env,
		Store:      store.CreateStore(time.Now),
		Ledger:     led,
		SinceMs:    24 * 3_600_000,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        func() int64 { return t1 },
		Alive:      func(int64) bool { return false },
		NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
	})
	t.Cleanup(w.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for w.CatchingUp() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the initial ingest")
		}
		time.Sleep(2 * time.Millisecond)
	}

	// The session's hooks reported a loopback router before the call was made.
	hook, err := json.Marshal(map[string]any{
		"ts": iso(t0), "event": "UserPromptSubmit", "session_id": "s8",
		"base_url": "http://127.0.0.1:18999",
	})
	if err != nil {
		t.Fatalf("marshal the hook line: %v", err)
	}
	w.ingestSpoolText(string(hook))
	w.applyEmit(feedFile(NewTranscriptState(), assistantLine(assistantOverrides{
		requestID: strp("req_via"),
	}), FileIdentity{SessionID: "s8", Kind: shared.KindMain}))

	// A subagent's transcript record lands on the node its bare id names.
	w.applyEmit(feedFile(NewTranscriptState(), assistantLine(assistantOverrides{
		requestID: strp("req_sub"),
	}), FileIdentity{SessionID: "s1", AgentID: strp("agent-a7"), Kind: shared.KindSubagent}))

	via, err := led.ByAnthropicID(t.Context(), "req_via")
	if err != nil || via == nil {
		t.Fatalf("the ledger row of req_via = %+v, %v", via, err)
	}
	if via.Via == nil || *via.Via != "127.0.0.1:18999" {
		t.Errorf("Via = %v, want 127.0.0.1:18999", via.Via)
	}
	if via.NodeID != "s8/main" {
		t.Errorf("NodeID = %q, want s8/main", via.NodeID)
	}

	sub, err := led.ByAnthropicID(t.Context(), "req_sub")
	if err != nil || sub == nil {
		t.Fatalf("the ledger row of req_sub = %+v, %v", sub, err)
	}
	if sub.NodeID != "s1/a7" {
		t.Errorf("NodeID = %q, want s1/a7", sub.NodeID)
	}
	if sub.Via != nil {
		t.Errorf("Via = %v, want nil: no hook reported a base URL for s1", *sub.Via)
	}
}

// TestTheRouteFactsKeepOnlyTheAllowListedHeaders: the spool is data, so the facts a route
// line files keep only the allow-listed response headers, whatever wrote the line; a line
// whose headers all fail the list files none.
func TestTheRouteFactsKeepOnlyTheAllowListedHeaders(t *testing.T) {
	route := func(headers string) string {
		return fmt.Sprintf(`{"ts":%q,"event":"route","session_id":"s9","model":"glm-5.3",`+
			`"upstream":"http://127.0.0.1:8787","route":"provider","status":200,"latency_ms":42,`+
			`"headers":{%s}}`, iso(t1), headers)
	}
	facts, ok := routeFactsOf(route(
		`"request-id":"req_headers","x-ratelimit-remaining":"99",`+
			`"authorization":"Bearer sk-test-not-a-real-secret","x-api-key":"test-key-not-real"`,
	), shared.SpoolLine{}, "glm-5.3", t1)
	if !ok {
		t.Fatal("routeFactsOf refused a route event")
	}
	if facts.Event.Headers == nil {
		t.Fatal("Headers = nil, want exactly the two allow-listed pairs")
	}
	got := *facts.Event.Headers
	if len(got) != 2 || got["request-id"] != "req_headers" || got["x-ratelimit-remaining"] != "99" {
		t.Fatalf("Headers = %v, want exactly request-id and x-ratelimit-remaining", got)
	}

	none, ok := routeFactsOf(route(
		`"authorization":"Bearer sk-test-not-a-real-secret","x-api-key":"test-key-not-real"`,
	), shared.SpoolLine{}, "glm-5.3", t1)
	if !ok {
		t.Fatal("routeFactsOf refused a route event")
	}
	if none.Event.Headers != nil {
		t.Fatalf("Headers = %v, want nil when every header fails the list", *none.Event.Headers)
	}
}

// TestTranscriptFactsCarryTheModelAndTheBillingKeys: the facts carry the record's model and
// put the four billing conditions into the usage map in the shape the provider's usage block
// has — cache_creation nested, the other three flat — each only when the record has it, so
// one reader in the ledger handles both sources.
func TestTranscriptFactsCarryTheModelAndTheBillingKeys(t *testing.T) {
	hour := int64(32)
	facts, ok := transcriptFactsOf(shared.RequestRecord{
		RequestID:    "req_bill",
		Model:        "glm-4.6",
		CacheWrite1h: &hour,
		Speed:        strp("fast"),
		Geo:          strp("us"),
		ServiceTier:  strp("priority"),
	})
	if !ok {
		t.Fatal("transcriptFactsOf refused a record with a request id")
	}
	if facts.Model != "glm-4.6" {
		t.Errorf("Model = %q, want glm-4.6", facts.Model)
	}
	creation, held := facts.Usage["cache_creation"].(map[string]any)
	if !held {
		t.Fatalf("cache_creation = %v, want an object holding the 1-hour writes", facts.Usage["cache_creation"])
	}
	if creation["ephemeral_1h_input_tokens"] != int64(32) {
		t.Errorf("ephemeral_1h_input_tokens = %v, want 32", creation["ephemeral_1h_input_tokens"])
	}
	for key, want := range map[string]string{"speed": "fast", "inference_geo": "us", "service_tier": "priority"} {
		if facts.Usage[key] != want {
			t.Errorf("%s = %v, want %q", key, facts.Usage[key], want)
		}
	}

	bare, ok := transcriptFactsOf(shared.RequestRecord{RequestID: "req_bare", Model: "glm-4.6"})
	if !ok {
		t.Fatal("transcriptFactsOf refused a record with a request id")
	}
	if bare.Model != "glm-4.6" {
		t.Errorf("Model = %q, want glm-4.6 on a record with no billing", bare.Model)
	}
	for _, key := range []string{"cache_creation", "speed", "inference_geo", "service_tier"} {
		if _, held := bare.Usage[key]; held {
			t.Errorf("the usage holds %s for a record that has none", key)
		}
	}
}

// TestTranscriptFactsCarryTheCaptureFacts: the facts carry the what line the transcript
// computed, the result's totals flag, and the thinking tokens inside the usage map in the
// shape the provider's output_tokens_details block has — each only when the record has it,
// so one reader in the ledger handles both sources.
func TestTranscriptFactsCarryTheCaptureFacts(t *testing.T) {
	facts, ok := transcriptFactsOf(shared.RequestRecord{
		RequestID: "req_capture",
		What:      strp("↳ prompt: hi  → Bash npm test"),
		Totals:    true,
		Tokens:    shared.Tokens{Thinking: i64p(7)},
	})
	if !ok {
		t.Fatal("transcriptFactsOf refused a record with a request id")
	}
	if facts.What == nil || *facts.What != "↳ prompt: hi  → Bash npm test" {
		t.Errorf("What = %v, want the computed what line", facts.What)
	}
	if !facts.Totals {
		t.Error("Totals = false, want the record's true")
	}
	details, held := facts.Usage["output_tokens_details"].(map[string]any)
	if !held {
		t.Fatalf("output_tokens_details = %v, want an object holding the thinking tokens", facts.Usage["output_tokens_details"])
	}
	if details["thinking_tokens"] != int64(7) {
		t.Errorf("thinking_tokens = %v, want 7", details["thinking_tokens"])
	}

	bare, ok := transcriptFactsOf(shared.RequestRecord{RequestID: "req_bare"})
	if !ok {
		t.Fatal("transcriptFactsOf refused a record with a request id")
	}
	if bare.What != nil {
		t.Errorf("What = %q, want nil for a record with no what line", *bare.What)
	}
	if bare.Totals {
		t.Error("Totals = true, want false for a record with no result fill")
	}
	if _, held := bare.Usage["output_tokens_details"]; held {
		t.Error("the usage holds output_tokens_details for a record with no thinking")
	}
}
