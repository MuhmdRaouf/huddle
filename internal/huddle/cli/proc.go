// The CLI's process runtime: the detached server start, the stop's signals and liveness probe,
// and the two process lookups behind Proc (ps and lsof).

package cli

import (
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
)

// detachAttr starts the server in its own session, outliving its caller: nothing that kills the
// caller's process tree reaches it.
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// signalTerm asks pid to stop.
func signalTerm(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

// signalKill forces pid to stop.
func signalKill(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// processAlive answers whether pid names a process (signal 0).
func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// forceStop ends a process that outlived the ask. The ask is a real signal here, and a server
// that ignores it is left to its user (the answer names the log), so there is nothing to force.
func forceStop(pid int) bool { return false }

// procCommand answers a pid's command line as `ps -o command=` prints it.
func procCommand(pid int) (string, bool) {
	out, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// procListenOf answers the loopback port a pid listens on, as lsof reports it.
func procListenOf(pid int) (int, bool) {
	out, err := exec.Command("lsof", "-nP", "-a", "-p", strconv.Itoa(pid), "-iTCP", "-sTCP:LISTEN", "-Fn").Output()
	if err != nil {
		return 0, false
	}
	match := listenRegexp.FindStringSubmatch(string(out))
	if match == nil {
		return 0, false
	}
	port, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	return port, true
}

// listenRegexp reads the loopback address line lsof prints (-Fn): n127.0.0.1:<port>.
var listenRegexp = regexp.MustCompile(`(?m)^n(?:127\.0\.0\.1|localhost|\[::1\]):(\d+)$`)
