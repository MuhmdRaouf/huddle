package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAFolderCostsOneDescriptorPerEntryOnKqueue(t *testing.T) {
	dir := t.TempDir()
	for i := range 3_000 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%04d.jsonl", i)), nil, 0o600); err != nil {
			t.Fatalf("write entry %d: %v", i, err)
		}
	}
	if got := folderCost(dir, true); got != 3_001 {
		t.Errorf("kqueue cost of a folder with 3,000 entries = %d, want 3,001", got)
	}
	if got := folderCost(dir, false); got != 1 {
		t.Errorf("inotify cost of the same folder = %d, want 1", got)
	}
}

func TestTheBudgetIsAQuarterOfTheSoftOpenFilesLimit(t *testing.T) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	t.Logf("RLIMIT_NOFILE soft = %d, hard = %d", limit.Cur, limit.Max)
	got := openFilesBudget(nil)
	if want := int(limit.Cur / 4); got != want {
		t.Errorf("budget = %d, want a quarter of the soft limit, %d", got, want)
	}
	fake := openFilesBudget(func() (uint64, error) { return 40, nil })
	if fake != 10 {
		t.Errorf("budget from a limit of 40 = %d, want 10", fake)
	}
}
