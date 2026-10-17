package process

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// gone reaps the detached child and reports whether it has exited: StartDetached releases the
// child, so nothing waits for it, and an unreaped zombie still answers an IsAlive probe.
func gone(pid int64) bool {
	var status syscall.WaitStatus
	found, err := syscall.Wait4(int(pid), &status, syscall.WNOHANG, nil)
	return err != nil || found == int(pid)
}

func TestStartDetached(t *testing.T) {
	t.Run("runs detached with stdout and stderr appended to the log", func(t *testing.T) {
		t.Setenv(childModeEnv, "three-lines")
		log := filepath.Join(t.TempDir(), "out.log")
		detached := StartDetached(selfExe(t), nil, DetachedOptions{LogFile: log})
		if detached.Pid <= 1 {
			t.Fatalf("pid = %d, want one above 1", detached.Pid)
		}
		waitFor(t, func() bool { return gone(detached.Pid) })
		content, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		if string(content) != "one\ntwo\nthree" {
			t.Fatalf("log = %q, want one\ntwo\nthree", content)
		}
	})

	t.Run("discards output when the log cannot be opened", func(t *testing.T) {
		t.Setenv(childModeEnv, "three-lines")
		detached := StartDetached(selfExe(t), nil, DetachedOptions{LogFile: filepath.Join(t.TempDir(), "missing", "out.log")})
		if detached.Pid <= 1 {
			t.Fatalf("pid = %d, want the start to survive the bad log path", detached.Pid)
		}
		waitFor(t, func() bool { return gone(detached.Pid) })
	})

	t.Run("swallows a start failure", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "out.log")
		detached := StartDetached("/nonexistent/detached-binary", nil, DetachedOptions{LogFile: log})
		if detached.Pid != 0 {
			t.Fatalf("pid = %d, want 0 for a start that failed", detached.Pid)
		}
	})
}
