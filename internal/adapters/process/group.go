// Process and process-group existence probes and signalling.
package process

import (
	"os"
	"syscall"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
)

// IsAlive reports whether pid names a live process. Signal 0 probes existence; EPERM means it exists
// but belongs to someone else.
func IsAlive(pid int64) bool {
	if !namesOneProcess(pid) {
		return false
	}
	err := killOne(pid, 0)
	if err == nil {
		return true
	}
	// A process of ours that has exited but not been waited for still answers the probe, so the
	// caller has to keep reaping its children (exec.Cmd.Wait, the orphan's reaper).
	return lastErrnoIsEPerm(err)
}

// SignalGroup signals the group pid leads (a no-op for 0, 1, negatives and our own pid); true when the
// group still had a member to signal.
func SignalGroup(pid int64, signal syscall.Signal) bool {
	if !isForeignGroupLeader(pid) {
		return false
	}
	err := killGroup(pid, signal)
	return err == nil || lastErrnoIsEPerm(err)
}

// isForeignGroupLeader: -1 would signal every process we own, and our own group would take this process
// down with it.
func isForeignGroupLeader(pid int64) bool {
	return namesOneProcess(pid) && pid != 1 && pid != int64(os.Getpid())
}

// namesOneProcess: kill() treats 0 and negatives as process groups (-1: every process we may signal),
// so only positive integers are safe to pass on.
func namesOneProcess(pid int64) bool {
	return pid > 0
}

// lastErrnoIsEPerm is the "exists but belongs to someone else" answer of a failed signal.
func lastErrnoIsEPerm(err error) bool {
	return err != nil && fsutil.ErrnoCode(err) == "EPERM"
}
