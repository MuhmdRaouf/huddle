package adapters

// SyncClaudeAgents against real temp dirs: the folder is $CLAUDE_CONFIG_DIR/agents, never the
// real home. The expected bodies come from the pure package (agents.Render), whose own tests pin
// the bytes; these tests pin what the writer does with them.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/agents"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// agentsEnv hands out a temp Claude config dir wrapped as the env lookup the sync reads; the
// agents folder itself starts absent.
func agentsEnv(t *testing.T) (domain.EnvLookup, string) {
	t.Helper()
	dir := t.TempDir()
	return domain.EnvLookup{"CLAUDE_CONFIG_DIR": dir}, filepath.Join(dir, "agents")
}

// wantedModels is what huddle wants today: one model per Claude family from the pricing table.
func wantedModels() []agents.Model {
	return agents.Newest(agents.ClaudeIDs())
}

// readAgentFolder lists the .md files directly in the folder, name to bytes.
func readAgentFolder(t *testing.T, folder string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(folder)
	if err != nil {
		t.Fatalf("read agents folder: %v", err)
	}
	files := map[string][]byte{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(folder, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		files[entry.Name()] = raw
	}
	return files
}

// TestSyncWritesFreshFolderToExactlyTheWantedFiles: an empty config dir ends up with one file per
// wanted model, byte-identical to its render — and nothing else.
func TestSyncWritesFreshFolderToExactlyTheWantedFiles(t *testing.T) {
	env, folder := agentsEnv(t)
	if err := SyncClaudeAgents(context.Background(), env); err != nil {
		t.Fatalf("SyncClaudeAgents: %v", err)
	}
	files := readAgentFolder(t, folder)
	want := map[string][]byte{}
	for _, model := range wantedModels() {
		want[model.FileName()] = agents.Render(model)
	}
	if len(files) != len(want) {
		t.Fatalf("agents folder holds %v, want exactly %v", keysOfFiles(files), keysOfFiles(want))
	}
	for name, body := range want {
		got, ok := files[name]
		if !ok {
			t.Errorf("agents folder misses %s", name)
			continue
		}
		if !bytes.Equal(got, body) {
			t.Errorf("%s = %q, want %q", name, got, body)
		}
	}
	if _, ok := files["fable-5.1.md"]; !ok {
		t.Errorf("agents folder %v never names fable-5.1.md", keysOfFiles(files))
	}
}

// TestSyncSecondPassChangesNothing: a rerun over an up-to-date folder rewrites no byte and bumps
// no mtime (the files are pinned to a fixed past time first, so any rewrite shows).
func TestSyncSecondPassChangesNothing(t *testing.T) {
	env, folder := agentsEnv(t)
	if err := SyncClaudeAgents(context.Background(), env); err != nil {
		t.Fatalf("first SyncClaudeAgents: %v", err)
	}
	past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, model := range wantedModels() {
		if err := os.Chtimes(filepath.Join(folder, model.FileName()), past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := SyncClaudeAgents(context.Background(), env); err != nil {
		t.Fatalf("second SyncClaudeAgents: %v", err)
	}
	for _, model := range wantedModels() {
		path := filepath.Join(folder, model.FileName())
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", model.FileName(), err)
		}
		if !info.ModTime().Equal(past) {
			t.Errorf("%s mtime = %s, the pass rewrote it (want %s)", model.FileName(), info.ModTime(), past)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(raw, agents.Render(model)) {
			t.Errorf("%s bytes changed on the second pass", model.FileName())
		}
	}
}

// TestSyncLeavesUnmarkedFilesAlone: a user's opus-5.5.md without the marker is never written,
// rewritten or removed — huddle writes no copy of its own under that name — and a user's file
// with any other name survives too.
func TestSyncLeavesUnmarkedFilesAlone(t *testing.T) {
	env, folder := agentsEnv(t)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	usersOpus := []byte("# my own opus notes\n")
	usersReviewer := []byte("# reviewer\n")
	for name, body := range map[string][]byte{"opus-5.5.md": usersOpus, "reviewer.md": usersReviewer} {
		if err := os.WriteFile(filepath.Join(folder, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := SyncClaudeAgents(context.Background(), env); err != nil {
		t.Fatalf("SyncClaudeAgents: %v", err)
	}
	for name, want := range map[string][]byte{"opus-5.5.md": usersOpus, "reviewer.md": usersReviewer} {
		raw, err := os.ReadFile(filepath.Join(folder, name))
		if err != nil {
			t.Fatalf("the user's %s vanished: %v", name, err)
		}
		if !bytes.Equal(raw, want) {
			t.Errorf("the user's %s was rewritten to %q", name, raw)
		}
	}
	files := readAgentFolder(t, folder)
	for _, model := range wantedModels() {
		if model.FileName() == "opus-5.5.md" {
			continue
		}
		if raw, ok := files[model.FileName()]; !ok || !bytes.Equal(raw, agents.Render(model)) {
			t.Errorf("%s = (%q, %v), want huddle's render beside the user's files", model.FileName(), raw, ok)
		}
	}
}

// TestSyncRemovesStaleManagedFile: a managed file whose name no family wants any more (an older
// opus the table moved past) is gone after one pass.
func TestSyncRemovesStaleManagedFile(t *testing.T) {
	env, folder := agentsEnv(t)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := agents.Render(agents.Model{Family: "opus", Version: []int{5}, ID: "claude-opus-5"})
	if err := os.WriteFile(filepath.Join(folder, "opus-5.md"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncClaudeAgents(context.Background(), env); err != nil {
		t.Fatalf("SyncClaudeAgents: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folder, "opus-5.md")); !os.IsNotExist(err) {
		t.Errorf("the stale managed opus-5.md survived: %v", err)
	}
	for _, model := range wantedModels() {
		raw, err := os.ReadFile(filepath.Join(folder, model.FileName()))
		if err != nil || !bytes.Equal(raw, agents.Render(model)) {
			t.Errorf("%s = (%q, %v), want huddle's render", model.FileName(), raw, err == nil)
		}
	}
}

// TestSyncReportsUnwritableFolder: an agents folder the process cannot write into answers an
// error value — no panic, nothing written. Skipped as root, where every folder is writable.
func TestSyncReportsUnwritableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: chmod 0500 cannot make a folder unwritable")
	}
	env, folder := agentsEnv(t)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(folder, 0o700) })
	err := SyncClaudeAgents(context.Background(), env)
	if err == nil {
		t.Fatal("SyncClaudeAgents on an unwritable folder = nil, want an error value")
	}
}

// keysOfFiles lists a name→bytes map's keys, sorted by the map read order.
func keysOfFiles(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	return names
}
