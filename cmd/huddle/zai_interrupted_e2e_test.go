package main

// The interrupted path's end to end, on the built binary: a job whose driver died mid-run reads
// as interrupted, not as running forever. The board and the review sweep the dead driver first,
// move the job to interrupted through the lifecycle, and nothing is signalled beyond the death
// the test itself dealt and nothing reruns: the worktree keeps its untouched file, the lock keeps
// its dead pid, and only the owner's return runs the job again. Runs in the same world as
// TestProviderCliE2E; every home, state root and repository is the test's own, and the only
// process it signals is the driver its own run started.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// TestZaiInterruptedE2E kills a running job's driver and reads the board and the review after:
// interrupted, the four ways out named, the worktree and lock untouched.
func TestZaiInterruptedE2E(t *testing.T) {
	job := newProviderJob(t, "src/value.txt:right\n", "src/value.txt:right\n", "sleep")
	brief := job.brief("timeout: 30s", "retries: { fix: 0, infra: 0 }")

	stdout, stderr, code := job.run("run", brief, "--bg")
	if code != 0 {
		t.Fatalf("run exited %d: %s%s", code, stdout, stderr)
	}
	id := startedID(t, strings.SplitN(stdout, "\n", 2)[0])
	job.waitForState(id, "running")

	// The driver dies the way a reboot takes it: SIGKILL, its whole group, no verdict, no lock
	// removal. The group is the driver's own, so the lingering worker goes with it.
	driver := lockPid(t, job, id)
	if !testkit.IsPidAlive(driver) {
		t.Fatalf("the driver %d died before the test reached it", driver)
	}
	if err := syscall.Kill(-driver, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("kill the driver's group: %v", err)
	}
	if err := syscall.Kill(driver, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("kill the driver: %v", err)
	}
	if err := testkit.Until(t, func() bool { return !testkit.IsPidAlive(driver) },
		testkit.UntilOptions{TimeoutMs: 10_000, Message: "the driver never died"}); err != nil {
		t.Fatal(err)
	}

	board, stderr, code := job.run("board")
	if code != 0 {
		t.Fatalf("board exited %d: %s%s", code, board, stderr)
	}
	if !strings.Contains(board, "interrupted") {
		t.Fatalf("the board never says interrupted:\n%s", board)
	}
	if !strings.Contains(board, id) {
		t.Fatalf("the board never shows job %s:\n%s", id, board)
	}
	job.waitForState(id, "interrupted")

	review, stderr, code := job.run("review", id[:9])
	if code != 0 {
		t.Fatalf("review exited %d: %s%s", code, review, stderr)
	}
	if !strings.Contains(review, "interrupted") {
		t.Fatalf("the review never says interrupted:\n%s", review)
	}
	for _, want := range []string{
		"/zai:accept " + id + " --no-commit",
		`/zai:return ` + id + ` "<feedback>"`,
		"/zai:discard " + id,
		"/zai:run <brief> --flash --bg",
	} {
		if !strings.Contains(review, want) {
			t.Fatalf("the review never names %q:\n%s", want, review)
		}
	}

	// Nothing reran and nothing new started: the worktree keeps its untouched file and the lock
	// still names the same dead pid.
	if got := job.repo.Read("src/value.txt"); got != "wrong\n" {
		t.Fatalf("the worktree holds %q, want the untouched wrong", got)
	}
	if again := lockPid(t, job, id); again != driver {
		t.Fatalf("the lock moved from the dead pid %d to %d: a driver started", driver, again)
	}
	if testkit.IsPidAlive(driver) {
		t.Fatalf("the dead pid %d is alive: a driver took it", driver)
	}
}

// lockPid is the pid a job's driver lock names, whatever format the lock holds.
func lockPid(t *testing.T, job *providerJob, id string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(job.stateRoot, "jobs", id, "driver.lock"))
	if err != nil {
		t.Fatalf("read the driver lock: %v", err)
	}
	lock, ok := domain.ParseDriverLock(string(raw))
	if !ok {
		t.Fatalf("driver lock = %q does not parse", raw)
	}
	return int(lock.Pid)
}
