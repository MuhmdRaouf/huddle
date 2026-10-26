// A pid's command line: CommandLineOf reads the command a pid runs (/proc, else `ps`).
package router

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
)

// CommandLineOf is a pid's command line (/proc, else `ps`), or false when neither can say.
func CommandLineOf(ctx context.Context, pid int64) (string, bool) {
	line, ok := procCmdline(fmt.Sprintf("/proc/%d/cmdline", pid))
	if !ok {
		line, ok = psCmdline(ctx, pid)
	}
	return line, ok
}

// procCmdline is one /proc cmdline file: NUL-separated argv as one line (the trailing NUL becomes
// a trailing space).
func procCmdline(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.Join(strings.Split(string(data), "\x00"), " "), true
}

// psCmdline is the ps fallback: the pid's command as `ps -p <pid> -o command=` reports it.
func psCmdline(ctx context.Context, pid int64) (string, bool) {
	timeoutMs := int64(2000)
	answer := process.RunToolSync(ctx, "ps", []string{"-p", strconv.FormatInt(pid, 10), "-o", "command="}, process.ToolOptions{TimeoutMs: &timeoutMs})
	if !process.Succeeded(answer) {
		return "", false
	}
	return strings.TrimSpace(string(answer.Stdout)), true
}
