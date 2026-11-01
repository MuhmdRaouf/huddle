// Package leftover finds and stops the processes a test run started and left running. A run
// owns a root directory (tmpguard's TMPDIR, or a TestMain's run folder): every executable under
// it — a *.test binary, or a binary the suite built there — that started after the run began is
// the run's own work still running detached, and a daemon started with Setsid outlives the
// test process that asked for it.
package leftover

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ownerFile is the file in a run's root that holds the pid of the process running it.
const ownerFile = "owner.pid"

// noAgeLimit matches every process under a root, whenever it started.
const noAgeLimit = time.Duration(1<<63 - 1)

// Process is one escaped process: its pid and the command line ps printed.
type Process struct {
	PID     string
	Command string
}

// Sweep answers whether an executable of this run outlived it, saying which: only an
// executable under root, started within maxElapsed, is the run's to answer — never another
// run's, never one that was already running, never a process ps cannot name. Each match's
// command line and HOME are printed to w; one whose HOME is a temp dir is killed, one whose
// HOME is not is reported and left alone — a sweep never signals a process it cannot prove is
// this run's. A ps failure answers true.
func Sweep(root string, maxElapsed time.Duration, w io.Writer) bool {
	out, err := exec.Command("ps", "-eo", "pid=,etime=,command=").Output()
	if err != nil {
		fmt.Fprintf(w, "leftover: list processes: %v\n", err)
		return true
	}
	escaped := Match(strings.Split(string(out), "\n"), root, maxElapsed)
	for _, proc := range escaped {
		home := processHome(proc.PID)
		fmt.Fprintf(w, "leftover: a process of this run outlived it: pid %s HOME %s\n  %s\n", proc.PID, home, proc.Command)
		if !tempHome(home) {
			fmt.Fprintf(w, "leftover: pid %s stays: its HOME is not a temp dir, so it is not proven to be this run's; report it\n", proc.PID)
			continue
		}
		pid, parseErr := strconv.Atoi(proc.PID)
		if parseErr != nil || pid <= 1 {
			fmt.Fprintf(w, "leftover: pid %s stays: no pid to signal\n", proc.PID)
			continue
		}
		if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr != nil {
			fmt.Fprintf(w, "leftover: kill pid %d: %v\n", pid, killErr)
			continue
		}
		fmt.Fprintf(w, "leftover: pid %d killed\n", pid)
	}
	return len(escaped) > 0
}

// SweepStale sweeps the run roots a dead run left in tempDir: each directory tempDir/<prefix>*
// whose owner.pid names a process that no longer runs has its processes stopped (whatever
// their age) and is removed. A root whose owner still runs, or whose owner.pid is missing or
// unreadable, is left alone. It answers how many roots it removed.
func SweepStale(tempDir, prefix string, w io.Writer) int {
	roots, err := filepath.Glob(filepath.Join(tempDir, prefix+"*"))
	if err != nil {
		fmt.Fprintf(w, "leftover: list run roots: %v\n", err)
		return 0
	}
	removed := 0
	for _, root := range roots {
		if !ownerGone(root) {
			continue
		}
		fmt.Fprintf(w, "leftover: the run that made %s ended; sweeping what it left\n", root)
		Sweep(root, noAgeLimit, w)
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintf(w, "leftover: remove %s: %v\n", root, err)
			continue
		}
		removed++
	}
	return removed
}

// ownerGone answers whether root's owner.pid names a process that no longer runs.
func ownerGone(root string) bool {
	text, err := os.ReadFile(filepath.Join(root, ownerFile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	if err != nil || pid <= 1 {
		return false
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

// Match picks the executables under root whose ps etime says they started after a run that
// began maxElapsed ago: started-since means elapsed < maxElapsed. It reads ps's
// `pid=,etime=,command=` lines and nothing else; a line whose executable it cannot read (a
// reaped process ps names in parentheses) is never a match.
func Match(lines []string, root string, maxElapsed time.Duration) []Process {
	var found []Process
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		pid, elapsedText := fields[0], fields[1]
		command := strings.Join(fields[2:], " ")
		executable := fields[2]
		if strings.HasPrefix(executable, "(") || !underDir(executable, root) {
			continue
		}
		elapsed, ok := ParseElapsed(elapsedText)
		if !ok || elapsed >= maxElapsed {
			continue
		}
		found = append(found, Process{PID: pid, Command: command})
	}
	return found
}

// underDir answers whether path is dir itself or a file inside it: a sibling whose name extends
// dir's (tmpguard-12 beside tmpguard-1) is not inside it.
func underDir(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// ParseElapsed reads ps's etime, [[dd-]hh:]mm:ss, into a duration; the days fold into the hours.
func ParseElapsed(text string) (time.Duration, bool) {
	hours := int64(0)
	rest := text
	if before, after, found := strings.Cut(text, "-"); found {
		days, err := strconv.ParseInt(before, 10, 64)
		if err != nil || days < 0 {
			return 0, false
		}
		hours, rest = days*24, after
	}
	parts := strings.Split(rest, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var seconds int64
	for i, part := range parts {
		parsed, err := strconv.ParseInt(part, 10, 64)
		if err != nil || parsed < 0 || part == "" {
			return 0, false
		}
		if i == 0 {
			parsed += hours
		}
		seconds = seconds*60 + parsed
	}
	return time.Duration(seconds) * time.Second, true
}

// processHome reads one process's HOME out of `ps eww`, "" when it cannot be read.
func processHome(pid string) string {
	out, err := exec.Command("ps", "eww", pid).Output()
	if err != nil {
		return ""
	}
	for _, field := range strings.Fields(string(out)) {
		if strings.HasPrefix(field, "HOME=") {
			return strings.TrimPrefix(field, "HOME=")
		}
	}
	return ""
}

// tempHome answers whether home is a directory under the temp root this run's throwaway homes
// come from (os.MkdirTemp's base, spelled as typed or with its symlinks resolved, as ps shows
// either): the sandboxed suites' homes are, the owner's is not.
func tempHome(home string) bool {
	if home == "" || home == string(filepath.Separator) {
		return false
	}
	base := os.TempDir()
	if strings.HasPrefix(home, base) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(base)
	return err == nil && strings.HasPrefix(home, resolved)
}
