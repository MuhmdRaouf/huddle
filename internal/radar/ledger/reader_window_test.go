package ledger

import (
	"context"
	"fmt"
	"testing"
)

// The ledger reader's window read: every request that started inside a window, no page cap,
// as the numbers and ids the insights report needs and no captured text.

// TestRequestFactsReadEveryRequestOfTheWindow seeds more requests than any page holds inside
// the window, and a few outside it, and reads the window back whole.
func TestRequestFactsReadEveryRequestOfTheWindow(t *testing.T) {
	st, file := openStore(t)
	ctx := context.Background()
	const inside = 1200
	for i := 0; i < inside; i++ {
		row := Row{
			ID: fmt.Sprintf("in-%05d", i), StartedAt: 10_000 + int64(i), DurationMs: 40, Status: 200,
			Model: "glm-5.3", Provider: "zai", SessionID: "s1", Input: 10, Output: 2, CacheRead: 3, CacheWrite: 1,
			PriceUSD: 0.5, What: strp("a captured line"),
		}
		if err := st.Upsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	for _, started := range []int64{9_999, 11_200} {
		if err := st.Upsert(ctx, Row{ID: fmt.Sprintf("out-%d", started), StartedAt: started, SessionID: "s2"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := openReader(t, file).RequestFacts(ctx, 10_000, 11_199, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != inside {
		t.Fatalf("read %d requests, want all %d of the window", len(got), inside)
	}
	first := got[0]
	want := RequestFact{
		Session: "s1", Model: "glm-5.3", Provider: "zai", StartedAt: 10_000, DurationMs: 40, Status: 200,
		Input: 10, Output: 2, CacheRead: 3, CacheWrite: 1, CostUSD: 0.5,
	}
	if first != want {
		t.Fatalf("first request = %+v, want %+v", first, want)
	}
}

// TestRequestFactsNarrowToOneSession reads one session's requests, whatever their time.
func TestRequestFactsNarrowToOneSession(t *testing.T) {
	st, file := openStore(t)
	ctx := context.Background()
	for i, session := range []string{"s1", "s2", "s1"} {
		if err := st.Upsert(ctx, Row{ID: fmt.Sprintf("r%d", i), StartedAt: int64(100 + i), SessionID: session}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := openReader(t, file).RequestFacts(ctx, 0, 1<<62, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d requests of s1, want 2", len(got))
	}
}

// TestRequestFactsPreferTheHarnessCostAndFallBackToTheLane prices a request at the cost the
// harness reported, else at radar's price, and names the lane when no provider is recorded.
func TestRequestFactsPreferTheHarnessCostAndFallBackToTheLane(t *testing.T) {
	st, file := openStore(t)
	ctx := context.Background()
	rows := []Row{
		{ID: "a", StartedAt: 1, PriceUSD: 0.5, HarnessCostUSD: f64p(1.25), Lane: "provider:zai"},
		{ID: "b", StartedAt: 2, PriceUSD: 0.5, Provider: "kimi", Lane: "provider:kimi"},
	}
	for _, row := range rows {
		if err := st.Upsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	got, err := openReader(t, file).RequestFacts(ctx, 0, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CostUSD != 1.25 || got[0].Provider != "provider:zai" {
		t.Errorf("request a = %+v, want the harness cost and the lane as provider", got[0])
	}
	if got[1].CostUSD != 0.5 || got[1].Provider != "kimi" {
		t.Errorf("request b = %+v, want radar's price and the provider", got[1])
	}
}
