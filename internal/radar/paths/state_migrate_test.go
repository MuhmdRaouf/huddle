// The state-dir migration cases, plus the exported legacy and agents dir layout.
package paths_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// migrateHome builds a HOME whose old ~/.local/state/<name> holds entries
// ("<name>/<file>" → content).
func migrateHome(t *testing.T, entries map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range entries {
		file := filepath.Join(dir, ".local", "state", name)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(file), err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	return dir
}

func readText(t *testing.T, path string) string {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(text)
}

func TestStateDirAndProviderStateDirMoveTheOldStateOver(t *testing.T) {
	t.Run("a fresh install: no old dir, nothing extra created", func(t *testing.T) {
		dir := migrateHome(t, nil)
		if got := paths.StateDir(t.Context(), domain.EnvLookup{"HOME": dir}); got != filepath.Join(dir, ".agents", "radar") {
			t.Fatalf("StateDir = %q, want %q", got, filepath.Join(dir, ".agents", "radar"))
		}
		if pathExists(t, filepath.Join(dir, ".agents")) {
			t.Fatal(".agents exists; resolving the root must create nothing")
		}
	})

	t.Run("a full move: the old radar dir lands in ~/.agents/radar and is removed", func(t *testing.T) {
		dir := migrateHome(t, map[string]string{"radar/port": "1234\n"})
		if got := paths.StateDir(t.Context(), domain.EnvLookup{"HOME": dir}); got != filepath.Join(dir, ".agents", "radar") {
			t.Fatalf("StateDir = %q, want %q", got, filepath.Join(dir, ".agents", "radar"))
		}
		if got := readText(t, filepath.Join(dir, ".agents", "radar", "port")); got != "1234\n" {
			t.Fatalf("moved port file = %q, want %q", got, "1234\n")
		}
		if pathExists(t, filepath.Join(dir, ".local", "state", "radar")) {
			t.Fatal("the old radar dir still exists, want it removed")
		}
	})

	t.Run("a partial move: jobs the new place already has stay behind and are never overwritten", func(t *testing.T) {
		dir := migrateHome(t, map[string]string{"zai/jobs/old/job.json": "{}"})
		kept := filepath.Join(dir, ".agents", "zai", "jobs", "kept", "job.json")
		if err := os.MkdirAll(filepath.Dir(kept), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(kept), err)
		}
		if err := os.WriteFile(kept, []byte("{}"), 0o600); err != nil {
			t.Fatalf("write %s: %v", kept, err)
		}
		if got := paths.ProviderStateDir(t.Context(), domain.EnvLookup{"HOME": dir}, "zai"); got != filepath.Join(dir, ".agents", "zai") {
			t.Fatalf("ProviderStateDir = %q, want %q", got, filepath.Join(dir, ".agents", "zai"))
		}
		if !pathExists(t, kept) {
			t.Fatal("the kept job is gone; an entry the new dir has is never overwritten")
		}
		if !pathExists(t, filepath.Join(dir, ".local", "state", "zai", "jobs", "old", "job.json")) {
			t.Fatal("the old job is gone; a lost entry stays behind")
		}
	})

	t.Run("a failed move keeps the old state and logs one line; the env overrides skip the migration", func(t *testing.T) {
		dir := migrateHome(t, map[string]string{"radar/port": "1234\n"})
		if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Join(dir, ".agents"), err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".agents", "radar"), []byte("a file where the dir must go"), 0o600); err != nil {
			t.Fatalf("write the blocker: %v", err)
		}
		var lines []string
		paths.MigrateStateDir(t.Context(), "radar", domain.EnvLookup{"HOME": dir}, func(line string) {
			lines = append(lines, line)
		})
		if len(lines) != 1 {
			t.Fatalf("the failed move logged %d lines (%v), want 1", len(lines), lines)
		}
		if got := readText(t, filepath.Join(dir, ".local", "state", "radar", "port")); got != "1234\n" {
			t.Fatalf("old port file = %q, want %q", got, "1234\n")
		}

		// An override means no migration is even attempted.
		if got := paths.StateDir(t.Context(), domain.EnvLookup{"HOME": dir, "RADAR_HOME": "/pin"}); got != "/pin" {
			t.Fatalf("StateDir(RADAR_HOME=/pin) = %q, want /pin", got)
		}
		if got := paths.ProviderStateDir(t.Context(), domain.EnvLookup{"HOME": dir, "ZAI_STATE_DIR": "/own"}, "zai"); got != "/own" {
			t.Fatalf("ProviderStateDir(ZAI_STATE_DIR=/own) = %q, want /own", got)
		}
		if !pathExists(t, filepath.Join(dir, ".local", "state", "radar", "port")) {
			t.Fatal("the old port file is gone; an override must skip the migration")
		}
	})
}

func TestLegacyAndAgentsStateDirs(t *testing.T) {
	if got := paths.LegacyStateDir(domain.EnvLookup{"HOME": "/h"}, "radar"); got != filepath.Join("/h", ".local", "state", "radar") {
		t.Fatalf("LegacyStateDir = %q, want %q", got, filepath.Join("/h", ".local", "state", "radar"))
	}
	xdg := domain.EnvLookup{"HOME": "/h", "XDG_STATE_HOME": "/xdg"}
	if got := paths.LegacyStateDir(xdg, "radar"); got != filepath.Join("/xdg", "radar") {
		t.Fatalf("LegacyStateDir(XDG_STATE_HOME) = %q, want %q", got, filepath.Join("/xdg", "radar"))
	}
	if got := paths.AgentsStateDir(domain.EnvLookup{"HOME": "/h"}, "radar"); got != filepath.Join("/h", ".agents", "radar") {
		t.Fatalf("AgentsStateDir = %q, want %q", got, filepath.Join("/h", ".agents", "radar"))
	}
}
