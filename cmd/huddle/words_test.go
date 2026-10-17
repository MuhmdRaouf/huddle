package main

// The satellite words: `huddle radar` and `huddle kanban` run radar's and kanban's command
// lines in this binary, before any huddle word — and before the legacy import, so a radar or
// kanban invocation never touches a huddle home.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

func TestRadarWordRunsRadar(t *testing.T) {
	t.Setenv("RADAR_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run([]string{"radar", "--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(radar --version) = %d, stderr %q", code, stderr.String())
	}
	if want := fmt.Sprintf("radar %s\n", version); stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestKanbanWordRunsKanban(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"kanban", "--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(kanban --version) = %d, stderr %q", code, stderr.String())
	}
	if want := fmt.Sprintf("kanban %s\n", version); stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestRadarWordSkipsTheLegacyImport pins the word's place in the dispatch: a machine with a
// legacy huddle home keeps it where it is when a radar invocation runs — only a huddle entry
// imports it.
func TestRadarWordSkipsTheLegacyImport(t *testing.T) {
	t.Setenv("HUDDLE_URL", "")
	t.Setenv("HUDDLE_CHANNEL", "")
	t.Setenv("HUDDLE_AS", "")
	t.Setenv("HUDDLE_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("RADAR_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HUDDLE_HOME", home)
	repo := t.TempDir()
	t.Setenv("CLAUDE_PROJECT_DIR", repo)
	source := filepath.Join(repo, ".agents", "huddle")
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(source, "data", "channels", "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Run(ctx, "INSERT INTO tasks (id, title, created_at) VALUES (?,?,?)", "t1", "the legacy task", "2026-10-16T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"radar", "--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(radar --version) = %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "channels", "main.db")); !os.IsNotExist(err) {
		t.Errorf("the radar word imported the legacy channel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "imports.json")); !os.IsNotExist(err) {
		t.Errorf("the radar word wrote the import ledger: %v", err)
	}
	if !strings.Contains(stdout.String(), "radar "+version) {
		t.Errorf("stdout = %q, want the radar version line", stdout.String())
	}
}
