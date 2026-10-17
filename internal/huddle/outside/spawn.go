package outside

import "syscall"

// launchAttr starts the notifier in its own session, as a detached spawn does.
func launchAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
