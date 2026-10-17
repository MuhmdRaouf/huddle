// The process runtime's system layer: session and group signalling and exit decoding.

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// detachedAttr starts a child in its own session and group, outliving its caller: nothing that kills
// the caller's process tree reaches it.
func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func killOne(pid int64, signal syscall.Signal) error {
	return syscall.Kill(int(pid), signal)
}

func killGroup(pid int64, signal syscall.Signal) error {
	return syscall.Kill(-int(pid), signal)
}

// termProcess is the timeout stop: SIGTERM, like Node's own timeout kill.
func termProcess(child *exec.Cmd) error {
	return child.Process.Signal(syscall.SIGTERM)
}

// applyKilled folds the signal our own stop sent into a wait result. Unix keeps the wait status's
// truth: a leader that trapped SIGTERM and exited cleanly reports that code, not the signal.
func applyKilled(recorded string, code *int, signal string) (*int, string) {
	return code, signal
}

// exitSignalName names the signal that ended the process; "" when it was not a signal.
func exitSignalName(state *os.ProcessState) string {
	waitStatus, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() {
		return ""
	}
	return signalName(waitStatus.Signal())
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL:  "SIGILL",
	syscall.SIGTRAP: "SIGTRAP",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGBUS:  "SIGBUS",
	syscall.SIGFPE:  "SIGFPE",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGUSR1: "SIGUSR1",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGTERM: "SIGTERM",
	syscall.SIGCHLD: "SIGCHLD",
	syscall.SIGCONT: "SIGCONT",
	syscall.SIGSTOP: "SIGSTOP",
	syscall.SIGTSTP: "SIGTSTP",
	syscall.SIGTTIN: "SIGTTIN",
	syscall.SIGTTOU: "SIGTTOU",
}

// signalName is the signal's Node spelling; "SIG<n>" for one Node does not name.
func signalName(signal syscall.Signal) string {
	if name, ok := signalNames[signal]; ok {
		return name
	}
	return fmt.Sprintf("SIG%d", int(signal))
}
