package cli

// The pid check behind `huddle down` and friends (HR-ROUTER-OWNERSHIP defect 4): a hub the
// launcher started through its `current` link runs as `<launcher>/current serve`, and the pid
// file it owns names it all the same.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRunningPidNamesAHubTheLauncherStarted pins the command check: the serve entry of a
// symlinked huddle (the launcher's `current`) is a huddle server; the same link left dangling
// names nothing, and any other entry through the link is not the server.
func TestRunningPidNamesAHubTheLauncherStarted(t *testing.T) {
	dir := t.TempDir()
	build := filepath.Join(dir, "build", "huddle")
	if err := os.MkdirAll(filepath.Dir(build), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(build, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(dir, "current")
	if err := os.Symlink(build, current); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "gone", "huddle"), dangling); err != nil {
		t.Fatal(err)
	}
	w := &World{Proc: Proc{Command: func(pid int) (string, bool) {
		switch pid {
		case 1:
			return current + " serve", true
		case 2:
			return current + " up", true
		case 3:
			return dangling + " serve", true
		default:
			return "", false
		}
	}}, Dir: t.TempDir()}
	root := w.Dir
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		pid  string
		want int
	}{
		{"1", 1}, // the launcher's own hub, through the current link
		{"2", 0}, // the up entry is not the server
		{"3", 0}, // a link that names nothing is no server
	} {
		writeFile(t, filepath.Join(root, "run", "huddle.pid"), row.pid)
		if pid := RunningPid(context.Background(), w, root); pid != row.want {
			t.Errorf("pid file %s: RunningPid = %d, want %d", row.pid, pid, row.want)
		}
	}
}
