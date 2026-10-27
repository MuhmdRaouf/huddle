package main

// The feature word works end to end on the built binary: closing a feature
// takes its cards out of every count and reopening brings the counts back.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EKanbanFeatureCloseReopen(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"PATH":        strippedPath,
		"HOME":        filepath.Join(root, "user"),
		"KANBAN_HOME": filepath.Join(root, "kanban-home"),
	}
	main := filepath.Join(root, "swrproj")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(argv ...string) (int, string, string) {
		t.Helper()
		return runBin(t, huddleBin, env, main, argv...)
	}

	if code, out, errOut := run("kanban", "init"); code != 0 {
		t.Fatalf("kanban init = %d, %q %q", code, out, errOut)
	}
	news := []struct{ id, cluster, title string }{
		{"SWR-1", "swr", "Reroute the swr probes over the local resolver"},
		{"SWR-2", "swr", "Cache the swr zone answers between probe runs"},
		{"SWR-3", "swr", "Alert when the swr probes miss two answers in a row"},
		{"WEB-1", "web", "Move the probe pages under one layout"},
	}
	for _, n := range news {
		summary := "The " + n.cluster + " area has grown without a written-down shape. " +
			"Give it one page that names each part, who owns it and what done looks like."
		if code, out, errOut := run("kanban", "new", n.id, "--actor", "lead",
			"--cluster", n.cluster, "--type", "docs", "--title", n.title, "--summary", summary); code != 0 {
			t.Fatalf("kanban new %s = %d, %q %q", n.id, code, out, errOut)
		}
	}
	before := statusColumns(t, run)

	if code, out, errOut := run("kanban", "feature", "close", "swr", "--actor", "lead"); code != 0 {
		t.Fatalf("kanban feature close = %d, %q %q", code, out, errOut)
	} else if !strings.Contains(out, "feature swr closed: 3 cards archived") {
		t.Fatalf("kanban feature close stdout = %q, want the archived line", out)
	}
	after := statusColumns(t, run)
	for _, col := range before {
		want := col.Count
		if col.ID == "backlog" {
			want -= 3 // the three swr cards leave every count
		}
		if got := after.count(col.ID); got != want {
			t.Errorf("after the close, %s count = %d, want %d", col.ID, got, want)
		}
	}

	if code, out, errOut := run("kanban", "feature", "reopen", "swr", "--actor", "lead"); code != 0 {
		t.Fatalf("kanban feature reopen = %d, %q %q", code, out, errOut)
	} else if !strings.Contains(out, "feature swr reopened: 3 cards back") {
		t.Fatalf("kanban feature reopen stdout = %q, want the cards-back line", out)
	}
	reopened := statusColumns(t, run)
	for _, col := range before {
		if got := reopened.count(col.ID); got != col.Count {
			t.Errorf("after the reopen, %s count = %d, want %d", col.ID, got, col.Count)
		}
	}
}

// statusColumns runs kanban status --json and answers the per-column counts.
func statusColumns(t *testing.T, run func(...string) (int, string, string)) statusSnapshot {
	t.Helper()
	code, out, errOut := run("kanban", "status", "--json")
	if code != 0 {
		t.Fatalf("kanban status --json = %d, %q %q", code, out, errOut)
	}
	var doc struct {
		Columns []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"columns"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode status --json: %v (%q)", err, out)
	}
	return statusSnapshot(doc.Columns)
}

type statusSnapshot []struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

func (s statusSnapshot) count(id string) int {
	for _, col := range s {
		if col.ID == id {
			return col.Count
		}
	}
	return -1
}
