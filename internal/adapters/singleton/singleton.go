// Package singleton is one holder per file: an exclusive, non-blocking flock on a lock file for
// the life of a process, so exactly one of anything runs per user. The kernel drops the lock
// when its holder dies, however it dies, so a stale file never blocks a start. The file itself
// is never deleted: deleting it would let a racing starter lock a new inode while the old one is
// still held.
package singleton

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HeldError is a second starter's answer: another process already holds the lock. Holder is
// what holds it ("a Huddle server", "huddle-router"), named in the error when it is set.
type HeldError struct {
	PID    int
	Holder string
}

func (e *HeldError) Error() string {
	if e.Holder == "" {
		return fmt.Sprintf("held by pid %d", e.PID)
	}
	return fmt.Sprintf("%s (pid %d) already serves this home", e.Holder, e.PID)
}

// Lock takes the lock at path for the life of the process: an exclusive, non-blocking flock,
// this process's pid written into the file. Its directory is created when missing. A second
// Lock for the same path answers a *HeldError naming the holder the file records. holder names
// what the lock holds, for that error's words.
func Lock(path, holder string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, err
		}
		pid := 0
		if raw, err := os.ReadFile(path); err == nil {
			pid = lockPid(raw)
		}
		return nil, &HeldError{PID: pid, Holder: holder}
	}
	_ = file.Truncate(0)
	_, _ = file.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	_ = file.Sync()
	done := false
	return func() {
		if done {
			return
		}
		done = true
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// Wait takes the lock at path, waiting up to within for a holder to let go: for the short
// critical sections — a read-modify-write of a small state file — that must join the queue
// rather than fail because another writer is mid-flight. Past within, the *HeldError stands.
func Wait(path, holder string, within time.Duration) (release func(), err error) {
	deadline := time.Now().Add(within)
	for {
		release, err := Lock(path, holder)
		if err == nil {
			return release, nil
		}
		var held *HeldError
		if !errors.As(err, &held) || !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(time.Millisecond)
	}
}

// Holder answers the pid that holds the lock at path: it tries the same non-blocking flock on a
// fresh handle — free means no holder (it is released at once), held means the pid the file
// names.
func Holder(path string) (pid int, held bool) {
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return 0, false // no file, no holder
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return 0, false
		}
		raw, err := io.ReadAll(file)
		if err != nil {
			return 0, true
		}
		return lockPid(raw), true
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return 0, false
}

// lockPid reads the pid a lock file names: its first line, trimmed; 0 when it names none.
func lockPid(raw []byte) int {
	line, _, _ := strings.Cut(string(raw), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return 0
	}
	return pid
}
