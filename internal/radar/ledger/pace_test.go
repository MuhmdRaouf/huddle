package ledger

// The pacing: a background job of the ledger rests nine times the batch's own time after
// it, so on average it takes at most a tenth of one core.

import (
	"syscall"
	"testing"
	"time"
)

// TestRestAfterKeepsTheJobUnderATenthOfACore: a batch's rest is nine times its own time,
// never less than the floor, so busy / (busy + rest) holds at most a tenth of a core.
func TestRestAfterKeepsTheJobUnderATenthOfACore(t *testing.T) {
	if got := restAfter(100*time.Millisecond, 50*time.Millisecond); got != 900*time.Millisecond {
		t.Errorf("restAfter(100ms, 50ms) = %s, want 900ms", got)
	}
	if got := restAfter(time.Millisecond, 50*time.Millisecond); got != 50*time.Millisecond {
		t.Errorf("restAfter(1ms, 50ms) = %s, want the 50ms floor", got)
	}
	for _, busy := range []time.Duration{5 * time.Millisecond, 120 * time.Millisecond, 2 * time.Second} {
		share := float64(busy) / float64(busy+restAfter(busy, 0))
		if share > 0.10+1e-9 {
			t.Errorf("a batch of %s holds %.12f of a core, want at most 0.10", busy, share)
		}
	}
}

// TestABatchRestsByItsCPUTimeWhenThatExceedsItsWallTime: a batch's busy time is the longer
// of its wall time and the process's CPU time over it, since collector and runtime threads
// burn CPU beside the batch; the rest is nine times that.
func TestABatchRestsByItsCPUTimeWhenThatExceedsItsWallTime(t *testing.T) {
	var wall time.Time
	var cpu time.Duration
	meter := batchMeter{
		now: func() time.Time { return wall },
		cpu: func() time.Duration { return cpu },
	}

	busy := meter.time(func() {
		wall = wall.Add(100 * time.Millisecond)
		cpu += 250 * time.Millisecond
	})
	if busy != 250*time.Millisecond {
		t.Errorf("a batch of 100ms wall and 250ms CPU was busy %s, want 250ms", busy)
	}
	if rest := restAfter(busy, 50*time.Millisecond); rest != 2250*time.Millisecond {
		t.Errorf("the rest after it is %s, want 2.25s, nine times the CPU time", rest)
	}

	busy = meter.time(func() {
		wall = wall.Add(100 * time.Millisecond)
		cpu += 40 * time.Millisecond
	})
	if busy != 100*time.Millisecond {
		t.Errorf("a batch of 100ms wall and 40ms CPU was busy %s, want 100ms", busy)
	}
}

// measuredRows is the row count of the measured share tests: 11 batches of BackfillBatch
// or MigrateBatch, enough for the share to settle near the tenth.
const measuredRows = 10_500

// skipMeasuredShare skips a test that reads CPU time against wall time: in short mode it
// is too long, and under the race detector the instrumented CPU time is not the cost the
// share bounds.
func skipMeasuredShare(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("the measured share is skipped in short mode")
	}
	if raceDetector {
		t.Skip("the race detector inflates CPU time, so the measured share holds only in a plain run")
	}
}

// cpuTime answers this process's own CPU time, user and system together: the measured
// tests read it around a run and divide by the run's wall time.
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatalf("read the process CPU time: %v", err)
	}
	return time.Duration(ru.Utime.Sec)*time.Second + time.Duration(ru.Utime.Usec)*time.Microsecond +
		time.Duration(ru.Stime.Sec)*time.Second + time.Duration(ru.Stime.Usec)*time.Microsecond
}
