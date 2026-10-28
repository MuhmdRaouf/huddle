package adapters

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// psStartedAt runs `ps -p <pid> -o etime=` and turns the elapsed time into an estimated start
// time in Unix milliseconds. It is the independent witness the start-time answers are checked
// against.
func psStartedAt(t *testing.T, pid int) (int64, bool) {
	t.Helper()
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "etime=").Output()
	if err != nil {
		return 0, false
	}
	elapsed, err := parseEtime(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps etime %q: %v", string(out), err)
	}
	return time.Now().UnixMilli() - elapsed.Milliseconds(), true
}

// parseEtime reads ps's [[dd-]hh:]mm:ss.
func parseEtime(text string) (time.Duration, error) {
	var days, hours int
	rest := text
	if i := strings.IndexByte(text, '-'); i >= 0 {
		d, err := strconv.Atoi(text[:i])
		if err != nil {
			return 0, err
		}
		days, rest = d, text[i+1:]
	}
	parts := strings.Split(rest, ":")
	switch len(parts) {
	case 3:
		h, err := strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		hours = h
	case 2:
	default:
		return 0, errors.New("not [[dd-]hh:]mm:ss")
	}
	m, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil {
		return 0, err
	}
	s, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, err
	}
	return time.Duration(days)*24*time.Hour + time.Duration(hours)*time.Hour +
		time.Duration(m)*time.Minute + time.Duration(s)*time.Second, nil
}

func TestProcStartTimeAnswersThisProcess(t *testing.T) {
	mine := int64(os.Getpid())
	start, ok := ProcStartTime(mine)
	if !ok {
		t.Fatalf("ProcStartTime(%d) = not ok, want this process's start time", mine)
	}
	witness, ok := psStartedAt(t, int(mine))
	if !ok {
		t.Skip("ps could not see this process")
	}
	if diff := start - witness; diff < -60_000 || diff > 60_000 {
		t.Errorf("ProcStartTime(%d) = %d, ps witnesses %d (off by %d ms)", mine, start, witness, diff)
	}
}

func TestProcStartTimeAnswersFalseForANonexistentPid(t *testing.T) {
	// Walk down from a pid above any live one until ps confirms the gap; a pid is probed with
	// ps alone, never signalled.
	for pid := 99998; pid > 40000; pid-- {
		out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "pid=").Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			continue
		}
		if _, ok := ProcStartTime(int64(pid)); ok {
			t.Errorf("ProcStartTime(%d) answered a start time, want not ok for a pid ps cannot see", pid)
		}
		return
	}
	t.Skip("no free pid found to probe")
}

func TestProcessControlImplementsProcessStart(t *testing.T) {
	control := CreateProcessControl("/unused/plugin-core", "zai", func(string) string { return "" })
	start, ok := control.(ports.ProcessStart)
	if !ok {
		t.Fatalf("the process control does not carry StartTime")
	}
	mine := int64(os.Getpid())
	got, ok := start.StartTime(mine)
	if !ok {
		t.Fatalf("StartTime(%d) = not ok, want this process's start time", mine)
	}
	want, ok := ProcStartTime(mine)
	if !ok {
		t.Fatalf("ProcStartTime(%d) = not ok", mine)
	}
	if got != want {
		t.Errorf("StartTime(%d) = %d, want %d", mine, got, want)
	}
}

// TestDriverLockRoundTripsPid locks a job the way a driver does and reads the lock back: the pid
// survives whatever the lock records beside it, and a second lock from the live holder is refused
// naming it.
func TestDriverLockRoundTripsPid(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mine := int64(os.Getpid())
	store := CreateFsStore(root, func(pid int64) bool { return pid == mine })

	job, err := store.Create(ctx, testkit.AJob())
	if err != nil {
		t.Fatalf("create the job: %v", err)
	}
	release, err := store.Lock(ctx, job.ID, mine)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	raw, err := os.ReadFile(domain.NewStateLayout(root).Job(job.ID).DriverLock)
	if err != nil {
		t.Fatalf("read the driver lock: %v", err)
	}
	lock, ok := domain.ParseDriverLock(string(raw))
	if !ok {
		t.Fatalf("the lock %q does not parse", raw)
	}
	if lock.Pid != mine {
		t.Errorf("the lock names pid %d, want %d", lock.Pid, mine)
	}

	release()
	if _, err := store.Lock(ctx, job.ID, mine); err != nil {
		t.Fatalf("relock after the release: %v", err)
	}
	_, err = store.Lock(ctx, job.ID, mine)
	var storeErr *ports.StoreError
	if !errors.As(err, &storeErr) || storeErr.Kind != ports.StoreErrorLocked {
		t.Fatalf("a second lock while held = %v, want locked", err)
	}
	if storeErr.HolderPid != mine {
		t.Errorf("the locked error names pid %d, want %d", storeErr.HolderPid, mine)
	}
}
