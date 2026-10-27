package ledger

// The prune beat: SchedulePrune prunes the store once at start, then on an hour beat, so a
// process that holds ledger.db open keeps it inside the metadata TTL without a restart. A
// failed prune is dropped and retried on the next beat: serving never depends on it.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock is a now the test moves by hand. The beat reads it from its own goroutine, so
// its reads and the test's writes share a mutex.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) Advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(by)
}

// TestTheBeatPrunesAtStart: the first prune runs before SchedulePrune returns, so a start
// cuts the file back to the TTL before anything reads it. A 100-day-old row goes; a
// 1-day-old one stays.
func TestTheBeatPrunesAtStart(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rows := []Row{
		{ID: "req_100", StartedAt: now.Add(-100 * 24 * time.Hour).UnixMilli()},
		{ID: "req_1", StartedAt: now.Add(-24 * time.Hour).UnixMilli()},
	}
	for _, row := range rows {
		if err := st.Upsert(ctx, row); err != nil {
			t.Fatalf("upsert %s: %v", row.ID, err)
		}
	}

	beat := SchedulePrune(ctx, st, 0, func() time.Time { return now })
	beat.Stop()

	if row, err := st.Get(ctx, "req_100"); err != nil || row != nil {
		t.Fatalf("the 100-day row = %+v, %v; want it gone at the first prune", row, err)
	}
	if row, err := st.Get(ctx, "req_1"); err != nil || row == nil {
		t.Fatalf("the 1-day row = %+v, %v; want it kept", row, err)
	}
}

// TestTheBeatPrunesOnEachTick: a row that turns spent after the start goes on a tick, not
// only at the first prune.
func TestTheBeatPrunesOnEachTick(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{at: now}
	beat := SchedulePrune(ctx, st, 20*time.Millisecond, clock.Now)
	t.Cleanup(beat.Stop)

	if err := st.Upsert(ctx, Row{ID: "req_89", StartedAt: now.Add(-89 * 24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	clock.Advance(2 * 24 * time.Hour) // the row sits 91 days behind the clock now

	deadline := time.Now().Add(2 * time.Second)
	for {
		row, err := st.Get(ctx, "req_89")
		if err != nil {
			t.Fatalf("read the 89-day row: %v", err)
		}
		if row == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the beat never pruned the spent row on a tick")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestStoppingTheBeatEndsIt: after Stop a row that would be pruned stays across five
// intervals; Stop twice is safe, a nil beat's Stop is safe, and a missing store starts no
// beat at all.
func TestStoppingTheBeatEndsIt(t *testing.T) {
	ctx := context.Background()
	if beat := SchedulePrune(ctx, nil, time.Millisecond, nil); beat != nil {
		t.Fatalf("SchedulePrune without a store = %+v, want nil", beat)
	}
	var nilBeat *PruneBeat
	nilBeat.Stop() // nothing to end; it must not panic

	st := openLedger(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{at: now}
	beat := SchedulePrune(ctx, st, 20*time.Millisecond, clock.Now)
	beat.Stop()
	beat.Stop() // twice is safe
	// the first prune already ran, so the row lands after it, spent against the advanced clock
	if err := st.Upsert(ctx, Row{ID: "req_100", StartedAt: now.Add(-100 * 24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	clock.Advance(100 * time.Millisecond) // five intervals
	time.Sleep(100 * time.Millisecond)    // the real time a live beat's ticks would take

	if row, err := st.Get(ctx, "req_100"); err != nil || row == nil {
		t.Fatalf("the 100-day row = %+v, %v; a stopped beat prunes nothing", row, err)
	}
}

// TestTheBeatEndsWithItsContext: cancelling the context ends the beat's goroutine, and a
// Stop after that returns at once.
func TestTheBeatEndsWithItsContext(t *testing.T) {
	st := openLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	beat := SchedulePrune(ctx, st, 20*time.Millisecond, func() time.Time {
		return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	})
	cancel()
	select {
	case <-beat.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the beat outlived its context")
	}
	ended := make(chan struct{})
	go func() { beat.Stop(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return once the context had ended the beat")
	}
}
