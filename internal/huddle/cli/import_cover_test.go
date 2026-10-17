package cli

// ImportLegacy, the entry's one-time import: the legacy home a project still carries moves into
// the user's huddle home within the entry's budget, and a project with nothing to import
// answers at once.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestImportLegacyBringsTheProjectsLegacyHomeIntoTheUsersHome pins the import: a project whose
// .agents/huddle still holds channels has them copied into the user's home, and the ledger
// notes the source so the next entry imports nothing again.
func TestImportLegacyBringsTheProjectsLegacyHomeIntoTheUsersHome(t *testing.T) {
	proj := t.TempDir()
	legacy := filepath.Join(proj, ".agents", "huddle")
	if err := os.MkdirAll(filepath.Join(legacy, "data", "channels"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(legacy, "data", "channels", "shop.db"), "an old channel's rows")
	homeRoot := t.TempDir()
	w := world(t, proj, map[string]string{"HUDDLE_HOME": homeRoot, "CLAUDE_PROJECT_DIR": proj})
	ImportLegacy(context.Background(), w, 10*time.Second)
	if _, err := os.Stat(filepath.Join(homeRoot, "imports.json")); err != nil {
		t.Errorf("the home has no import ledger: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homeRoot, "channels", "shop.db")); err != nil {
		t.Errorf("the channel did not move in: %v", err)
	}
}

// TestImportLegacyAnswersAtOnceWithoutALegacyHome pins the quiet path: a project with no
// channels left imports nothing and writes nothing.
func TestImportLegacyAnswersAtOnceWithoutALegacyHome(t *testing.T) {
	homeRoot := t.TempDir()
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": homeRoot, "CLAUDE_PROJECT_DIR": t.TempDir()})
	ImportLegacy(context.Background(), w, time.Second)
	if _, err := os.Stat(filepath.Join(homeRoot, "imports.json")); !os.IsNotExist(err) {
		t.Errorf("an import ledger appeared with nothing to import")
	}
}
