package app

import (
	"context"
	"slices"

	"github.com/MuhmdRaouf/huddle/internal/core/ports"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// InterruptDead moves every queued, running or verifying job whose driver died to interrupted, and
// answers the ids it moved. The driver is dead when its lock names a pid that is gone, or one that
// is alive but began after the lock was written — the OS handed the pid to a new process. An old
// pid-only lock decides on liveness alone, and so does a live pid whose start time cannot be had;
// a job without a lock file, or whose lock nobody can read, says nothing and stays. A job whose
// update loses a version race is skipped, not fatal: the writer that won the race proved the
// driver alive. Nothing is signalled and nothing is rerun — only the owner's return runs an
// interrupted job again.
func InterruptDead(ctx context.Context, deps Deps) []string {
	moved := []string{}
	for _, job := range deps.Store.List(ctx, ports.ListFilter{}) {
		if !slices.Contains(stoppable, job.State) {
			continue
		}
		text, err := deps.Files.ReadText(ctx, deps.Store.Paths(job.ID).DriverLock)
		if err != nil {
			continue
		}
		lock, ok := domain.ParseDriverLock(text)
		if !ok || driverAlive(deps, lock) {
			continue
		}
		if err := interrupt(ctx, deps, job); err != nil {
			continue
		}
		moved = append(moved, job.ID)
	}
	return moved
}

// driverAlive says whether the lock's pid is still the process that wrote the lock: alive, and —
// when both start times are known — begun at the recorded time.
func driverAlive(deps Deps, lock domain.DriverLock) bool {
	if !deps.Process.IsAlive(lock.Pid) {
		return false
	}
	if !lock.StartKnown {
		return true
	}
	start, ok := deps.Start.StartTime(lock.Pid)
	return !ok || start == lock.Start
}

// interrupt is one job moved to interrupted through the lifecycle, its open attempt closed at the
// clock's now: the attempt ended when the driver died, with no outcome or verdict anybody
// observed.
func interrupt(ctx context.Context, deps Deps, job domain.Job) error {
	moved, err := Move(job, domain.JobEvent{Type: domain.EventInterrupted})
	if err != nil {
		return err
	}
	if len(moved.Attempts) > 0 && moved.Attempts[len(moved.Attempts)-1].EndedAt == nil {
		last := moved.Attempts[len(moved.Attempts)-1]
		endedAt := deps.Clock.Iso()
		last.EndedAt = &endedAt
		moved = WithLastAttempt(moved, last)
	}
	moved.UpdatedAt = deps.Clock.Iso()
	_, err = deps.Store.Update(ctx, moved)
	return err
}
