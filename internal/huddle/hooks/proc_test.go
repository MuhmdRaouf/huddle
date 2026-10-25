package hooks

// The session walk's kernel readers, proven against a process this test owns: each answers one
// pid's command line or parent straight from the kernel — the sysctls on macOS, /proc on Linux
// — and refuses a pid the kernel does not answer.

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestProcReadsAChildsCommandAndParent: the readers answer a child this test started — its
// command line the way the walk reads it, its parent this test's own pid — and refuse a pid
// nothing owns.
func TestProcReadsAChildsCommandAndParent(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start the child: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	command, ok := procCommandOf(child.Process.Pid)
	if !ok || !strings.Contains(command, "sleep 30") {
		t.Errorf("procCommandOf(%d) = (%q, %v), want it to name sleep 30", child.Process.Pid, command, ok)
	}
	parent, ok := procParentOf(child.Process.Pid)
	if !ok || parent != os.Getpid() {
		t.Errorf("procParentOf(%d) = (%d, %v), want %d", child.Process.Pid, parent, ok, os.Getpid())
	}
	if command, ok := procCommandOf(1 << 30); ok || command != "" {
		t.Errorf("procCommandOf(%d) = (%q, %v), want (\"\", false)", 1<<30, command, ok)
	}
	if parent, ok := procParentOf(1 << 30); ok || parent != 0 {
		t.Errorf("procParentOf(%d) = (%d, %v), want (0, false)", 1<<30, parent, ok)
	}
}
