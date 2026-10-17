// The shared test world of the ingest tests: a whole temp HOME with RADAR_HOME,
// CLAUDE_CONFIG_DIR and ZAI_STATE_DIR inside it, plus the file helpers, so no test reads or
// writes anything real — not the user's transcripts, not a provider's jobs dir, not a live
// spool.
package ingest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// testEnv is one throwaway world, built from scratch (never copied from the process
// environment) so a stray variable set in the surrounding shell cannot leak a real path in.
type testEnv struct {
	env    domain.EnvLookup
	home   string
	state  string
	config string
	zai    string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{
		env: domain.EnvLookup{
			"HOME":              home,
			"RADAR_HOME":        filepath.Join(home, "state"),
			"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
			"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
		},
		home:   home,
		state:  filepath.Join(home, "state"),
		config: filepath.Join(home, "claude"),
		zai:    filepath.Join(home, "zai"),
	}
}

// ptr is the usual pointer-of helper.
func ptr[T any](value T) *T { return &value }

// writeText mkdir -p's the parent, then writes with 0600 — the mode every spool/state file is
// supposed to have.
func writeText(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// appendText appends, the way the spool and transcripts grow — never truncates.
func appendText(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := file.WriteString(text); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
