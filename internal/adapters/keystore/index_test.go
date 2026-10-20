package keystore_test

import (
	"context"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters/keystore"
)

// SpawnRunner runs harmless real tools only: never `security` or `secret-tool`, so the tests
// never touch the OS keychain they describe.
func TestSpawnRunner(t *testing.T) {
	ctx := context.Background()

	t.Run("writes stdin, collects stdout and reports exit 0", func(t *testing.T) {
		stdin := "hello"
		result := keystore.SpawnRunner(ctx, "cat", nil, &stdin)
		if result.Missing || result.Code == nil || *result.Code != 0 {
			t.Fatalf("run = %+v, want exit 0", result)
		}
		if result.Stdout != "hello" {
			t.Fatalf("stdout = %q, want the stdin back", result.Stdout)
		}
		if result.Stderr != "" {
			t.Fatalf("stderr = %q, want none", result.Stderr)
		}
	})

	t.Run("a nil stdin is an empty stdin, closed", func(t *testing.T) {
		result := keystore.SpawnRunner(ctx, "cat", nil, nil)
		if result.Missing || result.Code == nil || *result.Code != 0 {
			t.Fatalf("run = %+v, want exit 0", result)
		}
		if result.Stdout != "" {
			t.Fatalf("stdout = %q, want empty", result.Stdout)
		}
	})

	t.Run("carries a non-zero exit", func(t *testing.T) {
		result := keystore.SpawnRunner(ctx, "false", nil, nil)
		if result.Missing || result.Code == nil || *result.Code != 1 {
			t.Fatalf("run = %+v, want exit 1", result)
		}
	})

	t.Run("a tool that cannot start answers Missing, not a throw", func(t *testing.T) {
		result := keystore.SpawnRunner(ctx, "/nonexistent/keystore-tool", nil, nil)
		if !result.Missing {
			t.Fatalf("run = %+v, want Missing", result)
		}
	})

	t.Run("carries stderr", func(t *testing.T) {
		// sh, not ls: a missing path makes BSD ls exit 1 and GNU ls exit 2
		result := keystore.SpawnRunner(ctx, "sh", []string{"-c", "echo no such path >&2; exit 1"}, nil)
		if result.Code == nil || *result.Code != 1 {
			t.Fatalf("run = %+v, want exit 1", result)
		}
		if result.Stderr == "" {
			t.Fatal("stderr = empty, want the tool's complaint")
		}
	})
}
