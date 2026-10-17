package ingest_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The Codex collector: a fixture rollout (three token-count events, the second an identical
// repeat of the first) reads as two request records, exact to the token; the offsets keep
// the next poll from reading any of it again, and an append brings only the new turn. The
// rollout line shape follows openai/codex's codex-rs/history/src/rollout_payload.rs
// ({"timestamp",…,"type","payload"}, tags snake_case) and the token fields its
// codex-rs/protocol/src/protocol.rs TokenUsageInfo: last_token_usage carries one turn's
// counters, total_token_usage only the cumulative ones radar must never read.

// codexSessionID is the session the fixture's rollout names.
const codexSessionID = "3f9d1c9e-8c2a-4b7e-9a51-0f5c2a81d001"

// codexFirstAt and codexSecondAt are the fixture's two kept token-count lines,
// 2026-10-16T10:00:05.123Z and 2026-10-16T10:01:20Z, as epoch ms.
var (
	codexFirstAt  = time.Date(2026, time.October, 16, 10, 0, 5, 123_000_000, time.UTC).UnixMilli()
	codexSecondAt = time.Date(2026, time.October, 16, 10, 1, 20, 0, time.UTC).UnixMilli()
)

// codexRolloutPath is the fixture rollout's path inside a CODEX_HOME.
func codexRolloutPath(codexHome string) string {
	return filepath.Join(codexHome, "sessions", "2026", "10", "16",
		"rollout-2026-10-16T10-00-00-"+codexSessionID+".jsonl")
}

// codexEnv copies the fixture rollout into a temp CODEX_HOME's sessions tree.
func codexEnv(t *testing.T) domain.EnvLookup {
	t.Helper()
	home := t.TempDir()
	rollout := codexRolloutPath(filepath.Join(home, "codex"))
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(rollout), err)
	}
	blob, err := os.ReadFile(filepath.Join("testdata", "codex", "rollout-sample.jsonl"))
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	if err := os.WriteFile(rollout, blob, 0o600); err != nil {
		t.Fatalf("write the rollout: %v", err)
	}
	return domain.EnvLookup{"HOME": home, "CODEX_HOME": filepath.Join(home, "codex")}
}

// codexRecord is one expected request record, edited in place by edit.
func codexRecord(edit func(*shared.RequestRecord)) shared.RequestRecord {
	record := shared.RequestRecord{
		SessionID: codexSessionID,
		AgentID:   "main",
		Model:     "gpt-5-codex",
		Provider:  "other",
		Harness:   shared.HarnessCodex,
		Cwd:       ptr("/w/app"),
	}
	if edit != nil {
		edit(&record)
	}
	return record
}

func TestCodexCollector(t *testing.T) {
	t.Run("reads the fixture rollout as two records, exact to the token", func(t *testing.T) {
		env := codexEnv(t)
		records, err := ingest.NewCodexCollector(env).Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		want := []shared.RequestRecord{
			codexRecord(func(r *shared.RequestRecord) {
				r.ID = "codex:" + codexSessionID + ":0"
				r.Ts = codexFirstAt
				r.Tokens = shared.Tokens{Input: 800, Output: 300, CacheRead: 200, Thinking: ptr(int64(100))}
			}),
			codexRecord(func(r *shared.RequestRecord) {
				r.ID = "codex:" + codexSessionID + ":1"
				r.Ts = codexSecondAt
				r.Tokens = shared.Tokens{Input: 1000, Output: 700, CacheRead: 500, Thinking: ptr(int64(300))}
			}),
		}
		if !reflect.DeepEqual(records, want) {
			t.Errorf("records = %+v, want %+v", records, want)
		}
	})

	t.Run("reads nothing new on the next poll and only the appended turn after it", func(t *testing.T) {
		env := codexEnv(t)
		collector := ingest.NewCodexCollector(env)
		if _, err := collector.Collect(context.Background(), 0); err != nil {
			t.Fatalf("first collect: %v", err)
		}
		again, err := collector.Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("second collect: %v", err)
		}
		if len(again) != 0 {
			t.Fatalf("second collect read %d records again, want none", len(again))
		}
		appendText(t, codexRolloutPath(env["CODEX_HOME"]),
			`{"timestamp":"2026-10-16T10:02:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":1600,"cached_input_tokens":500,"output_tokens":1200,"reasoning_output_tokens":450,"total_tokens":2800},"last_token_usage":{"input_tokens":1600,"cached_input_tokens":500,"output_tokens":200,"reasoning_output_tokens":50,"total_tokens":2300},"model_context_window":272000}}}`+"\n")
		after, err := collector.Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("third collect: %v", err)
		}
		want := []shared.RequestRecord{
			codexRecord(func(r *shared.RequestRecord) {
				r.ID = "codex:" + codexSessionID + ":2"
				r.Ts = time.Date(2026, time.October, 16, 10, 2, 0, 0, time.UTC).UnixMilli()
				r.Tokens = shared.Tokens{Input: 1100, Output: 200, CacheRead: 500, Thinking: ptr(int64(50))}
			}),
		}
		if !reflect.DeepEqual(after, want) {
			t.Errorf("records = %+v, want %+v", after, want)
		}
	})

	t.Run("answers no records and no error without a sessions dir", func(t *testing.T) {
		records, err := ingest.NewCodexCollector(domain.EnvLookup{
			"HOME": t.TempDir(), "CODEX_HOME": filepath.Join(t.TempDir(), "nowhere"),
		}).Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(records) != 0 {
			t.Errorf("records = %d, want none", len(records))
		}
	})
}
