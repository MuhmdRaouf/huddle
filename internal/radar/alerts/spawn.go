package alerts

import "syscall"

// launchAttr starts the notifier detached: its own session, so it survives this process.
func launchAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
