package ledger

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
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
		ID:              "req_full",
		AnthropicID:     "req_full",
		StartedAt:       1761386400123,
		TtfbMs:          i64p(21),
		TtftMs:          i64p(300),
		DurationMs:      4123,
		Lane:            "provider:zai",
		Harness:         "claude",
		Model:           "glm-5.3-flash",
		FallbackFrom:    "claude-sonnet-5-5",
		Upstream:        "api.z.ai",
		SessionID:       "ses_full",
		AgentID:         "agt_full",
		ParentAgentID:   "par_full",
		PromptID:        "pr_full",
		TurnIndex:       i64p(3),
		PrevRequest:     "req_prev",
		QuerySource:     "user",
		Outcome:         "ok",
		Status:          200,
		ErrorCode:       "",
		StopReason:      "end_turn",
		Input:           120,
		Output:          45,
		CacheRead:       7,
		CacheWrite:      9,
		UsageSource:     "router",
		CostUSD:         0.0042,
		CostReportedUSD: f64p(0.0123),
		Ratelimit5h:     f64p(0.25),
		Ratelimit7d:     f64p(0.5),
		PromptHash:      "hash_full",
		InTranscript:    true,
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
