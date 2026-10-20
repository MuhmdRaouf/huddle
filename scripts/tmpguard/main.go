// tmpguard runs the test suite with TMPDIR pointed at one fresh empty
// directory and fails listing every entry the suite leaves there. A test
// that keeps a temp directory of its own fails the run, so a day of runs
// cannot fill the machine's temp volume. The directory is removed after
// the run, leaks listed or not.
//
// It also fails on the run's own test binaries that outlived it: a *.test
// executable under the run's temp build dirs, started after the run began,
// is a suite re-running detached. One whose HOME is a temp dir is killed;
// one whose HOME is not is reported and left alone.
//
// The directory comes from the system's default temp location, never from
// inside the module: the suite's "outside a repository" cases run git and
// the project detection in temp dirs, and a temp dir under the module
// would sit inside the repository they expect to be out of.
//
// Usage: go run ./scripts/tmpguard [go test flags and packages]
// Everything it is given goes to `go test` unchanged; with no packages
// named it tests ./...
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	patterns := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			patterns++
		}
	}
	if patterns == 0 {
		args = append(args, "./...")
	}
	guardDir, err := freshGuardDir()
	if err != nil {
		fmt.Fprintf(stderr, "tmpguard: %v\n", err)
		return 1
	}
	began := time.Now()
	test := exec.Command("go", append([]string{"test"}, args...)...)
	test.Stdout = stdout
	test.Stderr = stderr
	test.Env = withTmpDir(os.Environ(), guardDir)
	code := 0
	if err := test.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			fmt.Fprintf(stderr, "tmpguard: run go test: %v\n", err)
			code = 1
		}
	}
	left, listErr := leftovers(guardDir)
	escaped := escapedTestBinaries(filepath.Join(guardDir, "go-build"), began, stderr)
	if rmErr := os.RemoveAll(guardDir); rmErr != nil {
		fmt.Fprintf(stderr, "tmpguard: remove %s: %v\n", guardDir, rmErr)
	}
	if listErr != nil {
		fmt.Fprintf(stderr, "tmpguard: %v\n", listErr)
		return 1
	}
	if len(left) > 0 {
		fmt.Fprintf(stderr, "tmpguard: the suite left %d entries in TMPDIR (%s must end empty; a test with a *testing.T takes t.TempDir(), a TestMain removes its own root):\n", len(left), guardDir)
		for _, name := range left {
			fmt.Fprintf(stderr, "  %s\n", name)
		}
		return 1
	}
	if escaped {
		return 1
	}
	return code
}

// leftover is one escaped test process: its pid and the command line ps printed.
type leftover struct {
	pid     string
	command string
}

// escapedTestBinaries answers whether a test binary of this run outlived it, saying which: only
// a *.test executable under the run's own temp build dirs (go test links them under
// TMPDIR/go-build*), started after the run began, is the run's to answer — never another run's,
// never one that was already running, never a process ps cannot name. Each match's command line
// and HOME are printed; one whose HOME is a temp dir is killed, one whose HOME is not is
// reported and left alone — a guard never signals a process it cannot prove is this run's.
func escapedTestBinaries(buildRoot string, began time.Time, stderr io.Writer) bool {
	out, err := exec.Command("ps", "-eo", "pid=,etime=,command=").Output()
	if err != nil {
		fmt.Fprintf(stderr, "tmpguard: list processes: %v\n", err)
		return true
	}
	escaped := matchLeftovers(strings.Split(string(out), "\n"), buildRoot, time.Since(began))
	for _, proc := range escaped {
		home := processHome(proc.pid)
		fmt.Fprintf(stderr, "tmpguard: a test binary outlived the run: pid %s HOME %s\n  %s\n", proc.pid, home, proc.command)
		if !tempHome(home) {
			fmt.Fprintf(stderr, "tmpguard: pid %s stays: its HOME is not a temp dir, so it is not proven to be this run's; report it\n", proc.pid)
			continue
		}
		pid, parseErr := strconv.Atoi(proc.pid)
		if parseErr != nil || pid <= 1 {
			fmt.Fprintf(stderr, "tmpguard: pid %s stays: no pid to signal\n", proc.pid)
			continue
		}
		if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr != nil {
			fmt.Fprintf(stderr, "tmpguard: kill pid %d: %v\n", pid, killErr)
			continue
		}
		fmt.Fprintf(stderr, "tmpguard: pid %d killed\n", pid)
	}
	return len(escaped) > 0
}

// matchLeftovers picks the *.test executables under buildRoot whose ps etime says they started
// after a run that began maxElapsed ago: started-since means elapsed < maxElapsed. It reads ps's
// `pid=,etime=,command=` lines and nothing else; a line whose executable it cannot read (a
// reaped process ps names in parentheses) is never a match.
func matchLeftovers(lines []string, buildRoot string, maxElapsed time.Duration) []leftover {
	var found []leftover
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		pid, elapsedText := fields[0], fields[1]
		command := strings.Join(fields[2:], " ")
		executable := fields[2]
		if strings.HasPrefix(executable, "(") || !process.IsTestBinary(executable) ||
			!strings.HasPrefix(executable, buildRoot) {
			continue
		}
		elapsed, ok := parseElapsed(elapsedText)
		if !ok || elapsed >= maxElapsed {
			continue
		}
		found = append(found, leftover{pid: pid, command: command})
	}
	return found
}

// parseElapsed reads ps's etime, [[dd-]hh:]mm:ss, into a duration; the days fold into the hours.
func parseElapsed(text string) (time.Duration, bool) {
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

// tempHome answers whether home is a directory under the same temp root this run's throwaway
// homes come from (os.MkdirTemp's base): the sandboxed suites' homes are, the owner's is not.
func tempHome(home string) bool {
	return home != "" && home != string(filepath.Separator) && strings.HasPrefix(home, os.TempDir())
}

// freshGuardDir makes the run's empty TMPDIR in the system's default temp location.
func freshGuardDir() (string, error) {
	return os.MkdirTemp("", "tmpguard-")
}

// withTmpDir replaces TMPDIR in the process env, so the suite's every throwaway lands in dir.
func withTmpDir(entries []string, dir string) []string {
	kept := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		if strings.HasPrefix(entry, "TMPDIR=") {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, "TMPDIR="+dir)
}

// leftovers names the entries the suite left in the run's TMPDIR.
func leftovers(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}
