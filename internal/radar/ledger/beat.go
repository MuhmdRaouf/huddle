// The ledger's prune beat: one prune at start, then one every PruneEvery, so a process that
// holds ledger.db open keeps it inside the metadata TTL without a restart. A failed prune is
// dropped and retried on the next beat: serving never depends on it.
package ledger

import (
	"context"
	"sync"
	"time"
)

// PruneEvery is the prune beat: one hour.
const PruneEvery = time.Hour

// PruneBeat is a running prune beat.
type PruneBeat struct {
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// SchedulePrune prunes the store once now, then every every (0 or less: PruneEvery); each
// prune reads now() (nil: time.Now). The beat ends with ctx or Stop. Without a store it
// starts nothing and answers nil.
func SchedulePrune(ctx context.Context, st *Store, every time.Duration, now func() time.Time) *PruneBeat {
	if st == nil {
		return nil
	}
	if every <= 0 {
		every = PruneEvery
	}
	if now == nil {
		now = time.Now
	}
	run := func() { _, _ = st.Prune(ctx, now()) }
	run() // the first prune lands before the caller carries on, so a start cuts the file back
	beat := &PruneBeat{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(beat.done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-beat.stop:
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return beat
}

// Stop ends the beat; its goroutine and ticker are out once it returns. Safe on a nil beat
// and twice.
func (b *PruneBeat) Stop() {
	if b == nil {
		return
	}
	b.stopOnce.Do(func() { close(b.stop) })
	<-b.done
}
