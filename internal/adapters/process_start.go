package adapters

import "github.com/MuhmdRaouf/huddle/internal/core/ports"

// CreateProcessHandling is the process handling for one provider in both views: ProcessControl
// to stop drivers, ProcessStart to read when one began. One hostProcessControl behind both — the
// dead-driver sweep asks both questions about the same pid.
func CreateProcessHandling(command string, provider string, logPath func(jobID string) string) (ports.ProcessControl, ports.ProcessStart) {
	c := &hostProcessControl{command: command, provider: provider, logPath: logPath}
	return c, c
}

// StartTime is when pid began, in unix milliseconds: the ProcessStart view of the same process
// handling as ProcessControl. The dead-driver sweep tells a reused pid from the lock's holder by
// it. false when the platform cannot tell.
func (c *hostProcessControl) StartTime(pid int64) (int64, bool) { return procStartTime(pid) }
