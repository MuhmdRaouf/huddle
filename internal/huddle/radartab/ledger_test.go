package radartab

// The hub's own Radar files what it reads into the request ledger: a route line in the spool
// becomes a ledger row the mount's ledger.db answers.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

func TestTheOwningMountFilesRouteLinesIntoTheLedger(t *testing.T) {
	env := tempEnv(t)
	now := time.Now().UTC()
	line, err := json.Marshal(map[string]any{
		"ts": now.Format("2006-01-02T15:04:05.000Z"), "event": "route", "session_id": "s-ledger",
		"model": "glm-5.3", "upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200,
		"latency_ms": 42, "usage": map[string]any{"input_tokens": 100, "output_tokens": 20},
		"headers": map[string]string{"request-id": "req_tab"},
	})
	if err != nil {
		t.Fatalf("marshal the route line: %v", err)
	}
	spool := paths.SpoolDir(context.Background(), env)
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatalf("mkdir the spool: %v", err)
	}
	file := filepath.Join(spool, now.Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(file, append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write the spool line: %v", err)
	}

	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: 50, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock on an empty radar home")
	}

	led, err := ledger.Open(paths.LedgerPath(context.Background(), env))
	if err != nil {
		t.Fatalf("open the mount's ledger: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		row, err := led.ByAnthropicID(context.Background(), "req_tab")
		if err != nil {
			t.Fatalf("read the ledger: %v", err)
		}
		if row != nil {
			if row.Model != "glm-5.3" {
				t.Errorf("Model = %q, want the route line's glm-5.3", row.Model)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the mount never filed the spool's route line into the ledger")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
