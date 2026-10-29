package ledger

// The prune beat: SchedulePrune prunes the store once at start, then on an hour beat, so a
// process that holds ledger.db open keeps it inside the retention its source answers without
// a restart. A failed prune is dropped and retried on the next beat: serving never depends
// on it.

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fixedDays answers a source that always names the same retention.
func fixedDays(n int) RetentionSource {
	return func(context.Context) int { return n }
}

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

	beat := SchedulePrune(ctx, st, 0, func() time.Time { return now }, fixedDays(90), nil)
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
	beat := SchedulePrune(ctx, st, 20*time.Millisecond, clock.Now, fixedDays(90), nil)
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

// TestTheBeatReadsTheRetentionOnEveryPass: the beat asks the source on every pass, so a
// shorter setting takes effect on the next tick without a restart. A 10-day row survives the
// start pass at 90 days and goes once the source answers 7.
func TestTheBeatReadsTheRetentionOnEveryPass(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var days atomic.Int64
	days.Store(90)
	if err := st.Upsert(ctx, Row{ID: "req_10", StartedAt: now.Add(-10 * 24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	beat := SchedulePrune(ctx, st, 20*time.Millisecond, func() time.Time { return now },
		func(context.Context) int { return int(days.Load()) }, nil)
	t.Cleanup(beat.Stop)

	if row, err := st.Get(ctx, "req_10"); err != nil || row == nil {
		t.Fatalf("the 10-day row = %+v, %v; want it kept at 90 days", row, err)
	}
	days.Store(7)

	deadline := time.Now().Add(2 * time.Second)
	for {
		row, err := st.Get(ctx, "req_10")
		if err != nil {
			t.Fatalf("read the 10-day row: %v", err)
		}
		if row == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the beat never pruned the row after the retention changed to 7 days")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTheBeatWithoutASourceKeepsEveryRow: a nil source prunes nothing, the same as history's
// PruneOnce.
func TestTheBeatWithoutASourceKeepsEveryRow(t *testing.T) {
	st := openLedger(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if err := st.Upsert(ctx, Row{ID: "req_1000", StartedAt: now.Add(-1000 * 24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	beat := SchedulePrune(ctx, st, 0, func() time.Time { return now }, nil, nil)
	beat.Stop()

	if row, err := st.Get(ctx, "req_1000"); err != nil || row == nil {
		t.Fatalf("the 1000-day row = %+v, %v; want it kept", row, err)
	}
}

// TestStoppingTheBeatEndsIt: after Stop a row that would be pruned stays across five
// intervals; Stop twice is safe, a nil beat's Stop is safe, and a missing store starts no
// beat at all.
func TestStoppingTheBeatEndsIt(t *testing.T) {
	ctx := context.Background()
	if beat := SchedulePrune(ctx, nil, time.Millisecond, nil, fixedDays(90), nil); beat != nil {
		t.Fatalf("SchedulePrune without a store = %+v, want nil", beat)
	}
	var nilBeat *PruneBeat
	nilBeat.Stop() // nothing to end; it must not panic

	st := openLedger(t)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	clock := &fakeClock{at: now}
	beat := SchedulePrune(ctx, st, 20*time.Millisecond, clock.Now, fixedDays(90), nil)
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
	}, fixedDays(90), nil)
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

// recordingHealth records what the beat reported, the cases' stand-in for the registry.
type recordingHealth struct {
	mu      sync.Mutex
	records []healthRecord
}

// healthRecord is one report the beat left: the component, and whether its pass was good.
type healthRecord struct {
	name string
	ok   bool
	err  error
}

func (r *recordingHealth) OK(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, healthRecord{name: name, ok: true})
}

func (r *recordingHealth) Fail(name string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, healthRecord{name: name, err: err})
}

func (r *recordingHealth) snapshot() []healthRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

// TestThePruneBeatReportsEachPass: every pass of the prune beat lands its result on health —
// ok after a good pass, failing after a bad one — so a ledger that stopped pruning shows in
// /health instead of only in silence.
func TestThePruneBeatReportsEachPass(t *testing.T) {
	t.Run("records its first pass ok over a good store", func(t *testing.T) {
		st := openLedger(t)
		health := &recordingHealth{}
		beat := SchedulePrune(context.Background(), st, 0, nil, fixedDays(90), health)
		t.Cleanup(beat.Stop)
		if got := health.snapshot(); len(got) != 1 || got[0].name != PruneHealthName || !got[0].ok {
			t.Errorf("the first pass reported %v, want one %s ok", got, PruneHealthName)
		}
	})

	t.Run("records its first pass failing over a store closed before the beat", func(t *testing.T) {
		st := openLedger(t)
		if err := st.Close(); err != nil {
			t.Fatalf("close the store: %v", err)
		}
		health := &recordingHealth{}
		beat := SchedulePrune(context.Background(), st, 0, nil, fixedDays(90), health)
		t.Cleanup(beat.Stop)
		got := health.snapshot()
		if len(got) != 1 || got[0].name != PruneHealthName || got[0].ok {
			t.Fatalf("the first pass reported %v, want one %s failing", got, PruneHealthName)
		}
		if got[0].err == nil {
			t.Errorf("the failing pass carried no error")
		}
	})
}
