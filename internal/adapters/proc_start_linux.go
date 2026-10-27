package adapters

import (
	"os"
	"strconv"
	"strings"
)

// procUserHZ is the /proc ABI's fixed clock for stat times (USER_HZ), whatever the kernel's own
// HZ — the constant every reader of these files bakes in.
const procUserHZ = 100

// procStartTime is when pid began, in unix milliseconds: /proc/<pid>/stat's starttime field
// (ticks since boot) over /proc/stat's btime. false when the process cannot be read — a gone pid
// among them.
func procStartTime(pid int64) (int64, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.FormatInt(pid, 10) + "/stat")
	if err != nil {
		return 0, false
	}
	// Field 2 (comm) may hold spaces and parens; the fields after the last ')' are 3, 4, … so
	// starttime (22) sits 19 into the rest.
	after, found := cutLast(string(raw), ")")
	if !found {
		return 0, false
	}
	fields := strings.Fields(after)
	if len(fields) < 20 {
		return 0, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil || ticks < 0 {
		return 0, false
	}
	boot, ok := procBootTimeMs()
	if !ok {
		return 0, false
	}
	return boot + ticks*1000/procUserHZ, true
}

// procBootTimeMs is /proc/stat's btime line, in unix milliseconds.
func procBootTimeMs() (int64, bool) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if text, ok := strings.CutPrefix(line, "btime "); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err != nil {
				return 0, false
			}
			return seconds * 1000, true
		}
	}
	return 0, false
}

// cutLast splits s after its last instance of sep.
func cutLast(s, sep string) (string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, false
	}
	return s[i+len(sep):], true
}
