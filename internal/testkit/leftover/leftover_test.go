package leftover

// The leftover-process matcher over ps lines, and the sweeps built on it: any executable under a
// run's root — a *.test binary, or a binary the suite built there — started after the run began,
// is the run's to answer — never another run's, never one that was already running, never a
// process ps cannot name. A sweep stops the ones it can prove are the run's, and a dead run's
// root is swept at the next start.

import (
	"bytes"
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

// sleeperEnv makes a re-executed copy of this test binary sleep instead of running the suite.
const sleeperEnv = "LEFTOVER_TEST_SLEEP"

// TestSleeperHelper is the body of the detached process: it sleeps when sleeperEnv asks for it
// and is a no-op in a normal run.
func TestSleeperHelper(t *testing.T) {
	if os.Getenv(sleeperEnv) == "" {
		t.Skip("runs only as the detached process of the sweep tests")
	}
	time.Sleep(5 * time.Minute)
}

// sleeper starts a detached copy of this test binary from under root, so ps names an executable
// under root, and answers its pid. A copy of the test binary, not of a system tool: macOS hides
// the environment of its own signed binaries from ps, and the sweep reads the HOME of the
// process it stops. The process is killed when the test ends, whatever the sweep did.
func sleeper(t *testing.T, root string) int {
	t.Helper()
	return sleeperIn(t, root, "")
}

// sleeperIn is sleeper with the process's working directory set to dir ("" keeps this one's).
func sleeperIn(t *testing.T, root, dir string) int {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(bin, "sleeper")
	if err := os.WriteFile(copied, image, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(copied, "-test.run=^TestSleeperHelper$")
	cmd.Env = append(isoenv.Environ(t, t.TempDir()), sleeperEnv+"=1")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		<-exited
	})
	return pid
}

// gone polls until pid no longer runs, within 5 s.
func gone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func TestSweepStopsADetachedProcessUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	pid := sleeper(t, root)
	var out bytes.Buffer
	if !Sweep(root, time.Hour, &out) {
		t.Fatalf("Sweep answered false with a process running under the root; output: %s", out.String())
	}
	if !strings.Contains(out.String(), "pid "+strconv.Itoa(pid)) {
		t.Errorf("output does not name pid %d: %s", pid, out.String())
	}
	if !gone(pid) {
		t.Errorf("pid %d still runs after the sweep; output: %s", pid, out.String())
	}
}

func TestSweepStopsAProcessWhoseWorkingDirectoryIsOutsideTheRoot(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	pid := sleeperIn(t, root, elsewhere)
	var out bytes.Buffer
	if !Sweep(root, time.Hour, &out) {
		t.Fatalf("Sweep answered false for a process started from under the root; output: %s", out.String())
	}
	if !gone(pid) {
		t.Errorf("pid %d still runs after the sweep; output: %s", pid, out.String())
	}
}

func TestTempHomeAcceptsTheTempRootAsTypedAndResolved(t *testing.T) {
	resolved, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, home := range []string{filepath.Join(os.TempDir(), "hr-x"), filepath.Join(resolved, "hr-x")} {
		if !tempHome(home) {
			t.Errorf("tempHome(%q) = false, want true", home)
		}
	}
	for _, home := range []string{"", "/", "/Users/someone"} {
		if tempHome(home) {
			t.Errorf("tempHome(%q) = true, want false", home)
		}
	}
}

func TestSweepLeavesOtherProcessesAlone(t *testing.T) {
	root, elsewhere := t.TempDir(), t.TempDir()
	pid := sleeper(t, elsewhere)
	var out bytes.Buffer
	if Sweep(root, time.Hour, &out) {
		t.Errorf("Sweep answered true with nothing under the root; output: %s", out.String())
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Errorf("the sleeper outside the root was stopped: %v", err)
	}
}

func TestStaleRootsOfDeadOwnersAreSwept(t *testing.T) {
	tmp := t.TempDir()
	finished := exec.Command("true")
	if err := finished.Run(); err != nil {
		t.Fatal(err)
	}
	deadRoot := filepath.Join(tmp, "huddle-cmd-run-a")
	liveRoot := filepath.Join(tmp, "huddle-cmd-run-b")
	writeOwner(t, deadRoot, finished.Process.Pid)
	writeOwner(t, liveRoot, os.Getpid())
	pid := sleeper(t, deadRoot)
	var out bytes.Buffer
	if got := SweepStale(tmp, "huddle-cmd-run-", &out); got != 1 {
		t.Errorf("SweepStale removed %d dirs, want 1; output: %s", got, out.String())
	}
	if !gone(pid) {
		t.Errorf("pid %d of the dead owner's root still runs", pid)
	}
	if _, err := os.Stat(deadRoot); !os.IsNotExist(err) {
		t.Errorf("the dead owner's root stays: %v", err)
	}
	if _, err := os.Stat(liveRoot); err != nil {
		t.Errorf("the running owner's root went: %v", err)
	}
}

func TestStaleRootsWithoutAnOwnerFileAreLeftAlone(t *testing.T) {
	tmp := t.TempDir()
	orphan := filepath.Join(tmp, "huddle-cmd-run-c")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := SweepStale(tmp, "huddle-cmd-run-", &bytes.Buffer{}); got != 0 {
		t.Errorf("SweepStale removed %d dirs, want 0", got)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Errorf("a root without owner.pid went: %v", err)
	}
}

func writeOwner(t *testing.T, dir string, pid int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ownerFile), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMatchLeftovers(t *testing.T) {
	const root = "/var/folders/qm/T/tmpguard-1"
	const maxElapsed = 5 * time.Minute
	rows := []struct {
		name string
		line string
		want bool
	}{
		{"a test binary of this run", "  123    00:04 " + root + "/go-build123/b001/huddle.test serve", true},
		{"one started after the run began, late in it", "  130    04:59 " + root + "/go-build7/b001/cli.test -test.timeout=10m -test.count=1", true},
		{"a built binary of this run", "  126    00:01 " + root + "/huddle-cmd-bin-1/huddle serve", true},
		{"one started before the run began", "  124    05:01 " + root + "/go-build123/b001/huddle.test serve", false},
		{"a built binary started before the run began", "  129    05:02 " + root + "/huddle-cmd-bin-1/huddle serve", false},
		{"another run's binary", "  125    00:01 /var/folders/qm/T/go-build999/b001/other.test -test.timeout=10m", false},
		{"a sibling of the guard dir whose name extends it", "  133    00:01 /var/folders/qm/T/tmpguard-12/huddle serve", false},
		{"a reaped process ps cannot name", "  127    00:01 (adapters.test)", false},
		{"a day-old daemon under the guard dir", "  128 2-03:04:05 " + root + "/go-build7/b001/plugins.test", false},
		{"a binary somewhere else entirely", "  131    00:30 /usr/local/bin/x.test", false},
		{"a line without a command", "  132    00:30", false},
		{"an empty line", "", false},
	}
	for _, row := range rows {
		got := Match([]string{row.line}, root, maxElapsed)
		if !row.want {
			if len(got) != 0 {
				t.Errorf("%s: matched %v, want nothing", row.name, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Errorf("%s: matched %v, want the one pid", row.name, got)
			continue
		}
		if want := firstField(row.line); got[0].PID != want || got[0].Command != afterPid(row.line) {
			t.Errorf("%s: matched (%s, %q), want (%s, %q)", row.name, got[0].PID, got[0].Command, want, afterPid(row.line))
		}
	}
}

// ps prints whole seconds, rounded up: a process that started inside a run shorter than a second
// shows 00:01, and it is still the run's.
func TestMatchCountsAProcessStartedInsideARunShorterThanOneSecond(t *testing.T) {
	const root = "/var/folders/qm/T/tmpguard-1"
	line := "  140    00:01 " + root + "/TestX1/001/leaker"
	if got := Match([]string{line}, root, 900*time.Millisecond); len(got) != 1 {
		t.Errorf("matched %v, want the process ps shows at 00:01 in a 900 ms run", got)
	}
	late := "  141    00:03 " + root + "/TestX1/001/leaker"
	if got := Match([]string{late}, root, 900*time.Millisecond); len(got) != 0 {
		t.Errorf("matched %v, want a process that ps shows at 00:03 to predate a 900 ms run", got)
	}
}

// firstField is a line's first whitespace-separated word, its pid.
func firstField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// afterPid is the line past its pid and etime: the command line as ps printed it.
func afterPid(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return ""
	}
	return strings.Join(fields[2:], " ")
}

func TestParseElapsed(t *testing.T) {
	rows := []struct {
		text string
		want time.Duration
		ok   bool
	}{
		{"04:59", 299 * time.Second, true},
		{"01:00:00", time.Hour, true},
		{"1-02:03:04", 26*time.Hour + 3*time.Minute + 4*time.Second, true},
		{"2-00:00:00", 48 * time.Hour, true},
		{"04", 0, false},
		{"01:02:03:04", 0, false},
		{"bogus", 0, false},
		{"", 0, false},
		{"-1:00", 0, false},
	}
	for _, row := range rows {
		got, ok := ParseElapsed(row.text)
		if ok != row.ok || (ok && got != row.want) {
			t.Errorf("ParseElapsed(%q) = (%v, %v), want (%v, %v)", row.text, got, ok, row.want, row.ok)
		}
	}
}
