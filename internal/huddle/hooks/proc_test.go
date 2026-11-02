package hooks

// The session walk's kernel readers, proven against a process this test owns: each answers one
// pid's command line or parent straight from the kernel — the sysctls on macOS, /proc on Linux
// — and refuses a pid the kernel does not answer.

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// procChildScript is the child's whole program: it says "ready", then blocks reading stdin.
// Both are shell builtins, so the child stays one process, and the trailing ":" keeps the shell
// from exec-ing into the last command and reopening the window below.
const procChildScript = "echo ready; read line; :"

// TestProcReadsAChildsCommandAndParent: the readers answer a child this test started — its
// command line the way the walk reads it, its parent this test's own pid — and refuse a pid
// nothing owns.
func TestProcReadsAChildsCommandAndParent(t *testing.T) {
	child := exec.Command("sh", "-c", procChildScript)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatalf("pipe the child's stdin: %v", err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe the child's stdout: %v", err)
	}
	if err := child.Start(); err != nil {
		t.Fatalf("start the child: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	// Start returns once the exec is committed, but the kernel fills the argument area a moment
	// later, and /proc/<pid>/cmdline reads empty until then. A line the child writes proves
	// its exec finished, so the readers are asked only after it.
	spoke := make(chan error, 1)
	go func() {
		_, err := bufio.NewReader(stdout).ReadString('\n')
		spoke <- err
	}()
	select {
	case err := <-spoke:
		if err != nil {
			t.Fatalf("read the child's ready line: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the child never said ready")
	}
	command, ok := procCommandOf(child.Process.Pid)
	if !ok || !strings.Contains(command, procChildScript) {
		t.Errorf("procCommandOf(%d) = (%q, %v), want it to name %q", child.Process.Pid, command, ok, procChildScript)
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
