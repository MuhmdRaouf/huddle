// One server per home: the home's serve lock. Every server takes an exclusive flock on
// <home>/run/serve.lock for its whole life, and a second start for the same home sees it held
// and says so. The lock itself lives in internal/adapters/singleton; these are the home's two
// thin uses of it.
package server

import (
	"path/filepath"

	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// HeldError is a second server's answer: another process already serves this home.
type HeldError = singleton.HeldError

// serveLock is the home's serve lock, in its run directory.
func serveLock(root string) string { return filepath.Join(home.Home{Root: root}.Run(), "serve.lock") }

// LockHome takes the home's serve lock for the life of the process: an exclusive, non-blocking
// flock on <home>/run/serve.lock, this process's pid written into the file. The kernel drops
// the lock when the process dies, however it dies, so a stale file never blocks a start.
func LockHome(root string) (release func(), err error) {
	return singleton.Lock(serveLock(root), "a Huddle server")
}

// LockHolder answers the pid that holds root's serve lock: it tries the same non-blocking flock
// on a fresh handle — free means no holder (it is released at once), held means the pid the
// file names.
func LockHolder(root string) (pid int, held bool) {
	return singleton.Holder(serveLock(root))
}
