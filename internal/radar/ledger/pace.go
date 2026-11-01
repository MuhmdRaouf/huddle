package ledger

import (
	"syscall"
	"time"
)

// backgroundShare is the share of one core a background job of the ledger may take on
// average: after each batch it rests for the batch's own time multiplied by restFactor.
const backgroundShare = 0.10

// restFactor is how much longer than a batch took the job rests after it.
const restFactor = (1 - backgroundShare) / backgroundShare // 9

// restAfter answers how long to rest after a batch that took busy: restFactor times busy,
// and never less than floor.
func restAfter(busy, floor time.Duration) time.Duration {
	rest := time.Duration(float64(busy) * restFactor)
	if rest < floor {
		return floor
	}
	return rest
}

// batchMeter measures how busy a batch kept the process: the longer of the batch's wall
// time and the process's CPU time over it, because collector and runtime threads burn CPU
// beside the batch, so a batch can cost more CPU than its wall time.
type batchMeter struct {
	now func() time.Time
	cpu func() time.Duration
}

// processMeter answers a meter that reads the wall time from now and the CPU time of
// this process.
func processMeter(now func() time.Time) batchMeter {
	return batchMeter{now: now, cpu: processCPU}
}

// time runs batch and answers how busy it kept the process.
func (m batchMeter) time(batch func()) time.Duration {
	wallStart, cpuStart := m.now(), m.cpu()
	batch()
	wall, cpu := m.now().Sub(wallStart), m.cpu()-cpuStart
	return max(wall, cpu)
}

// processCPU answers this process's CPU time so far, user and system together; a
// failed read answers zero, which leaves the wall time to decide.
func processCPU() time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
