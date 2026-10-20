// One server per home: the home's serve lock. Every server takes an exclusive flock on
// <home>/run/serve.lock for its whole life, and a second start for the same home sees it held
// and says so. The kernel drops the lock when its holder dies, however it dies, so a stale file
// never blocks a start. The file itself is never deleted: deleting it would let a racing
// starter lock a new inode while the old one is still held.
package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// HeldError is a second server's answer: another process already serves this home.
type HeldError struct{ PID int }

func (e *HeldError) Error() string {
	return fmt.Sprintf("a Huddle server (pid %d) already serves this home", e.PID)
}

// serveLock is the home's serve lock, in its run directory.
func serveLock(root string) string { return filepath.Join(home.Home{Root: root}.Run(), "serve.lock") }

// LockHome takes the home's serve lock for the life of the process: an exclusive, non-blocking
// flock on <home>/run/serve.lock, this process's pid written into the file. The kernel drops
// the lock when the process dies, however it dies, so a stale file never blocks a start.
func LockHome(root string) (release func(), err error) {
	if err := os.MkdirAll(home.Home{Root: root}.Run(), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(serveLock(root), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, err
		}
		pid := 0
		if raw, err := os.ReadFile(serveLock(root)); err == nil {
			pid = lockPid(raw)
		}
		return nil, &HeldError{PID: pid}
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

// LockHolder answers the pid that holds root's serve lock: it tries the same non-blocking flock
// on a fresh handle — free means no holder (it is released at once), held means the pid the
// file names.
func LockHolder(root string) (pid int, held bool) {
	file, err := os.OpenFile(serveLock(root), os.O_RDWR, 0o600)
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
