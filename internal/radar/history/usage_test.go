package history

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/usage"
)

// TestUsageRows reads the usage read back over a temp database written through the
// existing writes: the join answers what the request and its node carry together, the
// window keeps what falls outside it out, and the rows leave in time order.
func TestUsageRows(t *testing.T) {
	h := openHistoryAt(t)
	base := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC).UnixMilli()
	project, label := "app", "Fix the login flow"
	write1h := int64(5)
	fast, us, priority := "fast", "us", "priority"
	harnessCost := 0.0123
	if err := h.UpsertNode(NodeUpsert{
		ID: "s1/main", Kind: shared.NodeMain, SessionID: "s1", AgentID: "main",
		Project: &project, Label: &label,
	}); err != nil {
		t.Fatalf("upsert the main node: %v", err)
	}
	if err := h.UpsertNode(NodeUpsert{
		ID: "s1/a1", Kind: shared.NodeSubagent, SessionID: "s1", AgentID: "a1",
	}); err != nil {
		t.Fatalf("upsert the subagent node: %v", err)
	}
	put := func(nodeID string, record shared.RequestRecord) {
		if err := h.PutRequest(nodeID, record); err != nil {
			t.Fatalf("put the request %s: %v", record.ID, err)
		}
	}
	put("s1/main", shared.RequestRecord{
		ID: "r1", SessionID: "s1", AgentID: "main", Model: "glm-5.3",
		Upstream: "https://api.z.ai", Provider: "zai", Ts: base,
		Tokens:       shared.Tokens{Input: 1000, Output: 100, CacheRead: 50, CacheWrite: 20},
		CacheWrite1h: &write1h, Speed: &fast, Geo: &us, ServiceTier: &priority,
	})
	put("s1/a1", shared.RequestRecord{
		ID: "r2", SessionID: "s1", AgentID: "a1", Model: "glm-5.3-flash",
		Upstream: "https://api.z.ai", Provider: "zai", Ts: base + 60_000,
		Tokens: shared.Tokens{Input: 200, Output: 40},
	})
	// an hour before the window: never read back
	put("s1/main", shared.RequestRecord{
		ID: "r3", SessionID: "s1", AgentID: "main", Model: "glm-5.3", Provider: "zai",
		Ts: base - 3*3_600_000, Tokens: shared.Tokens{Input: 9, Output: 9},
	})
	// a harness collector's rows: one Codex turn, one opencode message whose own cost rides
	// along to win over the estimate
	put("s1/main", shared.RequestRecord{
		ID: "r4", SessionID: "s1", AgentID: "main", Model: "gpt-5-codex", Ts: base + 120_000,
		Tokens: shared.Tokens{Input: 800, Output: 300, CacheRead: 200}, Harness: shared.HarnessCodex,
	})
	put("s1/main", shared.RequestRecord{
		ID: "r5", SessionID: "s1", AgentID: "main", Model: "glm-5.3", Provider: "zai", Ts: base + 180_000,
		Tokens: shared.Tokens{Input: 600, Output: 400, CacheRead: 300, CacheWrite: 40}, Harness: shared.HarnessOpencode,
		CostUSD: &harnessCost,
	})
	rows, err := h.UsageRows(context.Background(), base-3_600_000, base+3_600_000)
	if err != nil {
		t.Fatalf("read the usage rows: %v", err)
	}
	want := []usage.Row{
		{
			TS: base, Model: "glm-5.3", Upstream: "https://api.z.ai", Provider: "zai",
			Project: "app", SessionID: "s1", Kind: "main", Label: "Fix the login flow",
			Input: 1000, Output: 100, CacheRead: 50, CacheWrite: 20, CacheWrite1h: 5,
			Speed: &fast, Geo: &us, ServiceTier: &priority, Harness: shared.HarnessClaude,
		},
		{
			TS: base + 60_000, Model: "glm-5.3-flash", Upstream: "https://api.z.ai", Provider: "zai",
			SessionID: "s1", Kind: "subagent",
			Input: 200, Output: 40, Harness: shared.HarnessClaude,
		},
		{
			TS: base + 120_000, Model: "gpt-5-codex", SessionID: "s1", Kind: "main",
			Label: "Fix the login flow", Project: "app",
			Input: 800, Output: 300, CacheRead: 200, Harness: shared.HarnessCodex,
		},
		{
			TS: base + 180_000, Model: "glm-5.3", Provider: "zai",
			Project: "app", SessionID: "s1", Kind: "main", Label: "Fix the login flow",
			Input: 600, Output: 400, CacheRead: 300, CacheWrite: 40,
			Harness: shared.HarnessOpencode, CostUSD: &harnessCost,
		},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("usage rows = %+v, want %+v", rows, want)
	}
}

// TestUsageRowsContext honors the read's context: a canceled one refuses before the query.
func TestUsageRowsContext(t *testing.T) {
	h := openHistoryAt(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.UsageRows(ctx, 0, 1); err == nil {
		t.Error("the canceled context read anyway, want an error")
	}
}
