package ingest_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The watcher files a harness record's facts under the record's own id: a codex rollout and
// an opencode database land ledger rows beside their history rows, carrying the record's own
// model, session, counters and reported cost.

func TestTheWatcherFilesHarnessRecordsInTheLedger(t *testing.T) {
	codex := codexEnv(t)
	opencode := opencodeFixture(t)
	home := t.TempDir()
	env := domain.EnvLookup{
		"HOME":              home,
		"RADAR_HOME":        filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
		"CODEX_HOME":        codex["CODEX_HOME"],
		"OPENCODE_DATA":     opencode["OPENCODE_DATA"],
	}
	led, err := ledger.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })

	now := time.Now().UnixMilli()
	w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env:        env,
		Store:      store.CreateStore(time.Now),
		Ledger:     led,
		SinceMs:    24 * 3_600_000,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        func() int64 { return now },
		Alive:      func(int64) bool { return false },
		NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
		HarnessCollectors: []ingest.HarnessCollector{
			ingest.NewCodexCollector(env),
			ingest.NewOpencodeCollector(env),
		},
	})
	t.Cleanup(w.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for w.CatchingUp() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the initial ingest")
		}
		time.Sleep(2 * time.Millisecond)
	}

	ctx := t.Context()
	codexRow, err := led.Get(ctx, "codex:"+codexSessionID+":0")
	if err != nil {
		t.Fatalf("read the codex row: %v", err)
	}
	if codexRow == nil {
		t.Fatalf("no ledger row for codex:%s:0", codexSessionID)
	}
	if codexRow.Harness != shared.HarnessCodex || codexRow.Model != "gpt-5-codex" ||
		codexRow.SessionID != codexSessionID || codexRow.AgentID != "main" ||
		codexRow.StartedAt != codexFirstAt ||
		codexRow.Input != 800 || codexRow.Output != 300 || codexRow.CacheRead != 200 ||
		codexRow.Thinking == nil || *codexRow.Thinking != 100 {
		t.Errorf("codex row = %+v, want the rollout's own facts", codexRow)
	}
	if codexRow.UsageSource != "harness" || codexRow.Provider != "other" ||
		codexRow.NodeID != codexSessionID+"/main" {
		t.Errorf("codex row source = %s, provider = %s, node = %s, want harness, other, %s/main",
			codexRow.UsageSource, codexRow.Provider, codexRow.NodeID, codexSessionID)
	}

	opencodeRow, err := led.Get(ctx, "opencode:msg_aaa")
	if err != nil {
		t.Fatalf("read the opencode row: %v", err)
	}
	if opencodeRow == nil {
		t.Fatal("no ledger row for opencode:msg_aaa")
	}
	if opencodeRow.Harness != shared.HarnessOpencode || opencodeRow.Model != "claude-sonnet-5-5" ||
		opencodeRow.SessionID != opencodeSessionID || opencodeRow.AgentID != "main" ||
		opencodeRow.Input != 600 || opencodeRow.Output != 400 || opencodeRow.CacheRead != 300 ||
		opencodeRow.CacheWrite != 40 ||
		opencodeRow.Thinking == nil || *opencodeRow.Thinking != 50 {
		t.Errorf("opencode row = %+v, want the message's own facts", opencodeRow)
	}
	if opencodeRow.UsageSource != "harness" || opencodeRow.Provider != "Anthropic" ||
		opencodeRow.NodeID != opencodeSessionID+"/main" {
		t.Errorf("opencode row source = %s, provider = %s, node = %s, want harness, Anthropic, %s/main",
			opencodeRow.UsageSource, opencodeRow.Provider, opencodeRow.NodeID, opencodeSessionID)
	}
	if opencodeRow.HarnessCostUSD == nil || *opencodeRow.HarnessCostUSD != 0.0123 {
		t.Errorf("opencode HarnessCostUSD = %v, want the reported 0.0123", opencodeRow.HarnessCostUSD)
	}
}
