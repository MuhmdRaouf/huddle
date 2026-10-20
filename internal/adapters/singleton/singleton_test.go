package singleton

// The lock's own cases: a first Lock holds, a second Lock is refused naming the holder, a
// released lock is free again, and a file a dead process left behind never blocks a start.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "router.lock")
	release, err := Lock(path, "")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	var held *HeldError
	second, err := Lock(path, "")
	if !errors.As(err, &held) {
		t.Fatalf("second Lock = %v, want a *HeldError", err)
	}
	if second != nil {
		t.Fatalf("second Lock returned a release for a lock it does not hold")
	}
	if held.PID != os.Getpid() {
		t.Errorf("HeldError pid = %d, want %d", held.PID, os.Getpid())
	}
	pid, isHeld := Holder(path)
	if !isHeld {
		t.Fatal("Holder says the lock is free while it is held")
	}
	if pid != os.Getpid() {
		t.Errorf("Holder pid = %d, want %d", pid, os.Getpid())
	}
	release()
	// A release twice is nothing: the second one is a no-op.
	release()
	if _, isHeld := Holder(path); isHeld {
		t.Error("the lock is still held after its release")
	}
	again, err := Lock(path, "")
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	defer again()
}

func TestLockCreatesTheLockFilesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "deep", "router.lock")
	release, err := Lock(path, "")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer release()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the lock file: %v", err)
	}
}

func TestStaleLockFileNeverBlocksAStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "router.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// A pid nothing holds the lock for: the file names a process that died.
	if err := os.WriteFile(path, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := Lock(path, "")
	if err != nil {
		t.Fatalf("Lock over a stale file: %v", err)
	}
	defer release()
}

func TestWaitJoinsTheQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "router.lock")
	first, err := Lock(path, "a Huddle server")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	// A short critical section joins the queue instead of failing.
	go func() {
		time.Sleep(20 * time.Millisecond)
		first()
	}()
	second, err := Wait(path, "a Huddle server", 5*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	defer second()
}

func TestWaitNamesTheHolderPastItsDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "router.lock")
	first, err := Lock(path, "a Huddle server")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer first()
	var held *HeldError
	if _, err := Wait(path, "a Huddle server", 20*time.Millisecond); !errors.As(err, &held) {
		t.Fatalf("Wait = %v, want a *HeldError", err)
	}
}

func TestHolderWithoutAFile(t *testing.T) {
	if pid, held := Holder(filepath.Join(t.TempDir(), "absent.lock")); held || pid != 0 {
		t.Errorf("Holder = (%d, %t), want (0, false)", pid, held)
	}
}

func TestHolderOfAnUnheldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "router.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The file names a pid, but nobody holds the flock: free.
	if pid, held := Holder(path); held || pid != 0 {
		t.Errorf("Holder = (%d, %t), want (0, false)", pid, held)
	}
}

func TestHeldErrorWords(t *testing.T) {
	if got := (&HeldError{PID: 42}).Error(); got != "held by pid 42" {
		t.Errorf("a nameless holder's error = %q, want %q", got, "held by pid 42")
	}
	if got := (&HeldError{PID: 42, Holder: "a Huddle server"}).Error(); got != "a Huddle server (pid 42) already serves this home" {
		t.Errorf("a named holder's error = %q, want the holder named", got)
	}
}

func TestLockPidReadsTheFirstLineOnly(t *testing.T) {
	if pid := lockPid([]byte("4242\nand the rest")); pid != 4242 {
		t.Errorf("lockPid = %d, want 4242", pid)
	}
	for _, raw := range [][]byte{nil, []byte("\n"), []byte("not a pid\n"), []byte("  ")} {
		if pid := lockPid(raw); pid != 0 {
			t.Errorf("lockPid(%q) = %d, want 0", raw, pid)
		}
	}
}

func TestLockNamesItsOwnFailures(t *testing.T) {
	base := t.TempDir()
	// The lock's directory sits behind a file: creating it fails.
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("a file, not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if release, err := Lock(filepath.Join(blocker, "run", "router.lock"), ""); err == nil {
		release()
		t.Fatal("Lock behind a file = nil error, want one")
	}
	// The lock's directory is read-only: opening the file fails.
	readonly := filepath.Join(base, "readonly")
	if err := os.MkdirAll(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readonly, 0o700) })
	if release, err := Lock(filepath.Join(readonly, "router.lock"), ""); err == nil {
		release()
		t.Fatal("Lock in a read-only directory = nil error, want one")
	}
}
