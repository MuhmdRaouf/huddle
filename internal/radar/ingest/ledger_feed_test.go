package ingest

import (
	"encoding/json"
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
