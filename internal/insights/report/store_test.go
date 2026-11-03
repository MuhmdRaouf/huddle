package report

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunStoreKeepsPrivateFilesThatListNewestFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "insights", "runs")
	store := NewRunStore(dir)
	older := Run{ID: "20261101T100000Z-aaaa", Status: RunDone, Provider: "zai", Model: "glm-5.3", Answer: "ok", StartedAt: t0}
	newer := Run{ID: "20261102T100000Z-bbbb", Status: RunRunning, Step: StepAsking, StartedAt: t0.Add(24 * time.Hour)}
	for _, r := range []Run{older, newer} {
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, older.ID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the run file is %v (%v), want mode 0600", info, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the runs directory is %v (%v), want mode 0700", info, err)
	}
	got, err := store.Load(older.ID)
	if err != nil || got != older {
		t.Fatalf("Load = %+v (%v), want %+v", got, err, older)
	}
	listed, err := store.List(10)
	if err != nil || len(listed) != 2 || listed[0].ID != newer.ID || listed[1].ID != older.ID {
		t.Fatalf("List = %+v (%v), want the newer run first", listed, err)
	}
	if limited, _ := store.List(1); len(limited) != 1 || limited[0].ID != newer.ID {
		t.Errorf("List(1) = %+v, want only the newer run", limited)
	}
}

func TestRunStoreOfAMissingDirectoryListsNothing(t *testing.T) {
	listed, err := NewRunStore(filepath.Join(t.TempDir(), "none")).List(10)
	if err != nil || len(listed) != 0 {
		t.Fatalf("List = %+v (%v), want an empty list", listed, err)
	}
}

func TestRunStoreRefusesAnIDThatLeavesTheDirectory(t *testing.T) {
	store := NewRunStore(t.TempDir())
	var bad *InvalidRunIDError
	for _, id := range []string{"", "../x", "a/b", ".hidden"} {
		if err := store.Save(Run{ID: id}); !errors.As(err, &bad) {
			t.Errorf("Save(%q) = %v, want an InvalidRunIDError", id, err)
		}
		if _, err := store.Load(id); !errors.As(err, &bad) {
			t.Errorf("Load(%q) = %v, want an InvalidRunIDError", id, err)
		}
	}
}

func TestNewRunIDIsValidAndSortsByStartTime(t *testing.T) {
	first, second := NewRunID(t0), NewRunID(t0.Add(time.Second))
	if !runIDPattern.MatchString(first) || first >= second || first == NewRunID(t0) {
		t.Errorf("ids %q, %q: want valid names that sort by time and differ within one second", first, second)
	}
}
