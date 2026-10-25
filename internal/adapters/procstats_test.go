package adapters

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

// The process table reads every process in one ps call; a child this test starts must show in
// it under this process, with its memory.
func TestProcStatsListsTheTestsOwnChildWithItsMemory(t *testing.T) {
	child := exec.Command("sleep", "30")
	child.Env = isoenv.Environ(t, t.TempDir())
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	rows, err := ProcessTable(context.Background())
	if err != nil {
		t.Fatalf("process table: %v", err)
	}
	var found *ProcRow
	for i := range rows {
		if rows[i].Pid == child.Process.Pid {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatalf("the sleep child %d is missing from %d rows", child.Process.Pid, len(rows))
	}
	if found.Ppid != os.Getpid() {
		t.Errorf("ppid = %d, want this test's pid %d", found.Ppid, os.Getpid())
	}
	if found.RSSBytes <= 0 {
		t.Errorf("rss = %d bytes, want more than zero", found.RSSBytes)
	}
	if found.CPUSeconds < 0 {
		t.Errorf("cpu = %v s, want zero or more", found.CPUSeconds)
	}
	if found.Command == "" {
		t.Errorf("command is empty, want the sleep line")
	}
}

func TestProcStatsParsesBothPlatformsTimeShapes(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"0:00.03", 0.03},      // macOS: minutes and seconds
		{"12:01.50", 721.5},    // macOS past ten minutes
		{"00:01:02", 62},       // Linux: hours, minutes, seconds
		{"2-01:00:00", 176400}, // Linux past a day
	}
	for _, c := range cases {
		got, ok := parseCPUTime(c.in)
		if !ok || got < c.want-0.001 || got > c.want+0.001 {
			t.Errorf("parseCPUTime(%q) = %v, %v; want %v", c.in, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "x", "1:y"} {
		if _, ok := parseCPUTime(bad); ok {
			t.Errorf("parseCPUTime(%q) parsed, want refused", bad)
		}
	}
}
