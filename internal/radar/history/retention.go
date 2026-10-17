// History retention: the dashboard's historyRetentionDays setting turned into a cutoff,
// and a prune on an hour beat (plus once at startup) so a long-running server keeps the
// file inside the window the user chose. 0 means keep everything. A failed prune is
// logged nowhere and retried on the next beat: serving never depends on it.
package history

import (
	"context"
	"sync"
	"time"
)

// DayMs is one day, in epoch ms.
const DayMs = 86_400_000

// PruneIntervalMs is the prune beat: one hour.
const PruneIntervalMs = 60 * 60_000

// RetentionSource reads the retention days in force; the dashboard's own setting, read
// fresh on every prune so a change takes effect on the next beat. The layer order keeps
// this package from reading the settings file itself, so the caller wires that read in.
type RetentionSource func(ctx context.Context) int

// RetentionCutoff is the oldest last_at a tree may carry and survive, or nil when
// retention is off (0 = forever).
func RetentionCutoff(retentionDays int, now int64) *int64 {
	if retentionDays <= 0 {
		return nil
	}
	cutoff := now - int64(retentionDays)*DayMs
	return &cutoff
}

// PruneOnce deletes every tree whose newest activity is older than the retention in force
// allows; it answers how many trees went.
func PruneOnce(ctx context.Context, h *History, retention RetentionSource, now int64) (int, error) {
	if retention == nil {
		return 0, nil
	}
	cutoff := RetentionCutoff(retention(ctx), now)
	if cutoff == nil {
		return 0, nil
	}
	return h.Prune(*cutoff)
}

// PruneOptions overrides the beat's clock: how often it fires, and the time each prune
// reads.
type PruneOptions struct {
	IntervalMs int64
	Now        func() int64
}

// PruneSchedule is a running prune beat.
type PruneSchedule struct {
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// Stop ends the beat; its goroutine and its ticker are out once it returns.
func (p *PruneSchedule) Stop() {
	if p == nil {
		return
	}
	p.stopOnce.Do(func() { close(p.stop) })
	<-p.done
}

// Done closes once the beat's goroutine is out.
func (p *PruneSchedule) Done() <-chan struct{} { return p.done }

// SchedulePrunes prunes once now, then every interval; the beat ends with its context.
func SchedulePrunes(ctx context.Context, h *History, retention RetentionSource, options PruneOptions) *PruneSchedule {
	interval := options.IntervalMs
	if interval <= 0 {
		interval = PruneIntervalMs
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	run := func() {
		// the next beat tries again; the dashboard keeps serving either way
		_, _ = PruneOnce(ctx, h, retention, now())
	}
	run()
	p := &PruneSchedule{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return p
}
