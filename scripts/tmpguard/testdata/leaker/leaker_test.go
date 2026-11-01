// Package leaker is the fixture of tmpguard's own test: a sandboxed suite whose test starts a
// detached child from a binary under a temp dir of its own and never stops it. It lives under testdata,
// so `go test ./...` skips it; only tmpguard's test runs it, by path.
package leaker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// sleeperEnv makes the re-executed copy of this test binary sleep instead of running the suite.
const sleeperEnv = "TMPGUARD_LEAKER_SLEEP"

func TestMain(m *testing.M) {
	if os.Getenv(sleeperEnv) == "1" {
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
	unsandbox, err := isoenv.Sandbox()
	if err != nil {
		os.Exit(1)
	}
	code := m.Run()
	unsandbox()
	os.Exit(code)
}

// TestLeavesAChildRunningFromItsTempDir starts the child and logs its pid for the test that runs
// this fixture.
func TestLeavesAChildRunningFromItsTempDir(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	// A dir of the suite's own, not t.TempDir: the binary stays on disk while the child runs, as a
	// daemon's does until its suite is done with the temp dir.
	dir, err := os.MkdirTemp("", "leaker-")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "leaker")
	if err := os.WriteFile(bin, image, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), sleeperEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	awaitRunning(t, pid, bin)
	t.Logf("leaker child pid %d", pid)
}

// awaitRunning waits until ps names the child by its binary: a fresh copy of a large binary
// takes the system a moment to start, and until then ps lists the pid under its parent's name.
func awaitRunning(t *testing.T, pid int, bin string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(out)), bin) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ps never named pid %d by %s", pid, bin)
}
