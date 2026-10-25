// The session walk's kernel readers on Linux: one /proc file per answer, no process started.

package hooks

import (
	"os"
	"strconv"
	"strings"
)

// procCommandOf answers a pid's command line from /proc/<pid>/cmdline, its NUL-separated
// arguments joined with one space. "" and false when the kernel does not answer.
func procCommandOf(pid int) (string, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return "", false
	}
	words := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	if len(words) == 0 || words[0] == "" {
		return "", false
	}
	return strings.Join(words, " "), true
}

// procParentOf answers a pid's parent from /proc/<pid>/stat: the fourth field of the line,
// read past the last ')' so a command name with spaces or parentheses of its own cannot shift
// it. 0 and false when the kernel does not answer.
func procParentOf(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	line := string(raw)
	at := strings.LastIndexByte(line, ')')
	if at < 0 {
		return 0, false
	}
	// past the command name the state comes first and the parent is the next field
	fields := strings.Fields(line[at+1:])
	if len(fields) < 2 {
		return 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent <= 0 {
		return 0, false
	}
	return parent, true
}
