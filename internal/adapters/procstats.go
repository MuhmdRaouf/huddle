package adapters

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProcRow is one process as the process table lists it: its parent, its resident memory, the
// CPU time it has used so far and its command line.
type ProcRow struct {
	Pid        int
	Ppid       int
	RSSBytes   int64
	CPUSeconds float64
	Command    string
}

// ProcessTable lists every process on the machine in one ps call, the same columns on macOS
// and Linux. The call is bounded, so a wedged ps costs one sample only.
func ProcessTable(ctx context.Context) ([]ProcRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ps := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,time=,command=")
	// LC_ALL=C keeps the CPU time's decimal point a dot; the last entry of a key wins
	ps.Env = append(os.Environ(), "LC_ALL=C")
	out, err := ps.Output()
	if err != nil {
		return nil, err
	}
	return parseProcessTable(string(out)), nil
}

// parseProcessTable reads ps's lines; a line it cannot read is skipped.
func parseProcessTable(out string) []ProcRow {
	rows := []ProcRow{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pid, errPid := strconv.Atoi(fields[0])
		ppid, errPpid := strconv.Atoi(fields[1])
		rssKB, errRSS := strconv.ParseInt(fields[2], 10, 64)
		cpu, okCPU := parseCPUTime(fields[3])
		if errPid != nil || errPpid != nil || errRSS != nil || !okCPU {
			continue
		}
		rows = append(rows, ProcRow{
			Pid:        pid,
			Ppid:       ppid,
			RSSBytes:   rssKB * 1024,
			CPUSeconds: cpu,
			Command:    strings.Join(fields[4:], " "),
		})
	}
	return rows
}

// parseCPUTime reads ps's time column: macOS writes minutes and seconds ("12:01.50"), Linux
// hours, minutes and seconds with an optional day count ("2-01:00:00"). Both fold the same way,
// each field sixty of the next.
func parseCPUTime(text string) (float64, bool) {
	days := 0.0
	if d, rest, ok := strings.Cut(text, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, false
		}
		days, text = float64(n), rest
	}
	parts := strings.Split(text, ":")
	if len(parts) < 2 {
		return 0, false
	}
	total := 0.0
	for _, part := range parts {
		n, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, false
		}
		total = total*60 + n
	}
	return days*86400 + total, true
}
