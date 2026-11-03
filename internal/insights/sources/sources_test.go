package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

const sentinel = "SENTINEL-0x42"

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

const (
	seededRequests = 1250
	seededTools    = 1100
)

type fakeAsks struct{ records []hub.ItemRecord }

func (f fakeAsks) ItemHistory(_ context.Context, since time.Time) ([]hub.ItemRecord, error) {
	var out []hub.ItemRecord
	for _, r := range f.records {
		if !r.CreatedAt.Before(since) {
			out = append(out, r)
		}
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

// seeded writes a ledger and a history far over any page size: seededRequests requests and
// seededTools tool calls inside the last 7 days, and rows of both just outside them.
func seeded(t *testing.T) (ledgerFile, historyFile string) {
	t.Helper()
	dir := t.TempDir()
	ledgerFile, historyFile = filepath.Join(dir, "ledger", "ledger.db"), filepath.Join(dir, "history.db")
	ctx := context.Background()

	st, err := ledger.Open(ledgerFile)
	if err != nil {
		t.Fatal(err)
	}
	inside := now.Add(-3 * 24 * time.Hour).UnixMilli()
	for i := 0; i < seededRequests; i++ {
		session := "s1"
		if i%5 == 0 {
			session = "s2"
		}
		row := ledger.Row{
			ID: fmt.Sprintf("in-%05d", i), StartedAt: inside + int64(i), DurationMs: 100, Status: 200,
			Model: "glm-5.3", Provider: "zai", SessionID: session, Input: 100, Output: 10,
			PriceUSD: 0.001, What: ptr("prompt " + sentinel),
		}
		if err := st.Upsert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	outside := now.Add(-8 * 24 * time.Hour).UnixMilli()
	if err := st.Upsert(ctx, ledger.Row{ID: "old", StartedAt: outside, SessionID: "s1", Input: 999_999}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	h, err := history.OpenHistory(historyFile, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []history.NodeUpsert{
		{ID: "n1", Kind: shared.NodeMain, SessionID: "s1", AgentID: "main", Repo: ptr("/work/" + sentinel + "/a"), StartedAt: ptr(inside)},
		{ID: "n2", Kind: shared.NodeMain, SessionID: "s2", AgentID: "main", Cwd: ptr("/work/b"), StartedAt: ptr(inside)},
		{ID: "n3", Kind: shared.NodeSubagent, SessionID: "s1", AgentID: "a1", ParentID: ptr("n1"), StartedAt: ptr(inside), EndedAt: history.EndAt(inside + 1000)},
	} {
		if err := h.UpsertNode(n); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < seededTools; i++ {
		tool := shared.ToolCallRecord{
			ID: fmt.Sprintf("t%05d", i), SessionID: "s1", Name: "Bash", StartedAt: inside + int64(i),
			DurationMs: ptr(int64(5)), Ok: true, InputKey: ptr(fmt.Sprintf("k%d", i)),
		}
		if err := h.PutTool("n1", tool); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.PutTool("n1", shared.ToolCallRecord{ID: "told", SessionID: "s1", Name: "Bash", StartedAt: outside, Ok: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.AddEvent(nil, shared.EventRecord{Seq: 1, Ts: inside + 5, Kind: "PreCompact", SessionID: ptr("s1")}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	return ledgerFile, historyFile
}

func open(t *testing.T, ledgerFile, historyFile string, asks AskHistory) *Stores {
	t.Helper()
	stores, err := Open(ledgerFile, historyFile, asks)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stores.Close() })
	return stores
}

func TestBuildCountsEveryRowOfTheWindow(t *testing.T) {
	ledgerFile, historyFile := seeded(t)
	asks := fakeAsks{records: []hub.ItemRecord{
		{SessionID: "s1", Kind: hub.KindPermission, Command: "rm -rf " + sentinel, Decision: "allow",
			CreatedAt: now.Add(-time.Hour), DecidedAt: now.Add(-time.Hour + 30*time.Second)},
		{SessionID: "s1", Kind: hub.KindTurn, CreatedAt: now.Add(-time.Hour)},
	}}
	stores := open(t, ledgerFile, historyFile, asks)

	s, labels, err := report.Build(context.Background(), stores.Sources(), report.LastDays(now, 7))
	if err != nil {
		t.Fatal(err)
	}
	if s.Totals.Requests != seededRequests || s.Totals.Sessions != 2 {
		t.Errorf("totals = %+v, want %d requests in 2 sessions", s.Totals, seededRequests)
	}
	if want := (report.Tokens{Input: 100 * seededRequests, Output: 10 * seededRequests}); s.Totals.Tokens != want {
		t.Errorf("tokens = %+v, want %+v", s.Totals.Tokens, want)
	}
	if want := 0.001 * seededRequests; math.Abs(s.Totals.CostUSD-want) > 1e-6 {
		t.Errorf("cost = %v, want %v", s.Totals.CostUSD, want)
	}
	if len(s.Tools) != 1 || s.Tools[0].Name != "Bash" || s.Tools[0].Calls != seededTools {
		t.Errorf("tools = %+v, want %d Bash calls", s.Tools, seededTools)
	}
	if s.Subagents.Count != 1 || s.Waste.Compactions != 1 || s.Blocked.Asks.Count != 1 {
		t.Errorf("subagents %+v, compactions %d, asks %+v; want 1, 1 and 1 ask (the turn is no ask)",
			s.Subagents, s.Waste.Compactions, s.Blocked.Asks)
	}
	if len(labels.Repos()) != 2 {
		t.Errorf("repo labels = %+v, want the repo and the cwd fallback", labels.Repos())
	}
	payload, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), sentinel) {
		t.Fatalf("the payload carries the sentinel: %s", payload)
	}
}

func TestOneSessionWindowReadsThatSessionOnly(t *testing.T) {
	ledgerFile, historyFile := seeded(t)
	stores := open(t, ledgerFile, historyFile, nil)
	s, _, err := report.Build(context.Background(), stores.Sources(), report.OneSession("s2"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Totals.Requests != seededRequests/5 || s.Totals.Sessions != 1 {
		t.Errorf("totals = %+v, want the %d requests of s2", s.Totals, seededRequests/5)
	}
}

func TestMissingStoresReadAsEmpty(t *testing.T) {
	dir := t.TempDir()
	stores := open(t, filepath.Join(dir, "none", "ledger.db"), filepath.Join(dir, "history.db"), nil)
	s, _, err := report.Build(context.Background(), stores.Sources(), report.LastDays(now, 7))
	if err != nil {
		t.Fatal(err)
	}
	if s.Totals.Requests != 0 || s.Totals.Sessions != 0 {
		t.Errorf("totals = %+v, want none", s.Totals)
	}
}
