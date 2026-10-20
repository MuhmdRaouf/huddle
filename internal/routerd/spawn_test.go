package routerd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpawnServeRefusesATestBinary(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "start-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	// The marker proves nothing ran: the stub would create it if it were started.
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	stub := filepath.Join(dir, "routerd.test")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = spawnServeArgs(stub, map[string]string{"HOME": dir}, log)
	if err == nil || !strings.Contains(err.Error(), "test binary") {
		t.Fatalf("spawn of a .test binary = %v, want the test-binary refusal", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the test binary was started")
	}
}

func TestIsTestBinary(t *testing.T) {
	rows := []struct {
		path string
		want bool
	}{
		{"/tmp/go-build1/b001/huddle.test", true},
		{"cli.test", true},
		{"/Users/x/.agents/huddle-bin/src/0123456789ab/huddle", false},
		{"/usr/local/bin/huddle", false},
		{"/tmp/tests/huddle", false},
	}
	for _, row := range rows {
		if got := isTestBinary(row.path); got != row.want {
			t.Errorf("isTestBinary(%q) = %v, want %v", row.path, got, row.want)
		}
	}
}
