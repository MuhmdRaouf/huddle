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
