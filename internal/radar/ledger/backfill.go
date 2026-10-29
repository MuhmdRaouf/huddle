// The ledger's backfill: rows written by older Reconcile rules rebuilt from the facts the
// file already holds, one BackfillBatch per transaction, its cursor in ledger_meta. Open
// runs no batch — a hub start never waits on a backfill — and a batch holds the file's one
// connection only for its own transaction, so the watcher's next statement waits out one
// batch and lands after it.
package ledger

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// BackfillHealthName is the name the backfill reports under.
const BackfillHealthName = "ledger.backfill"

// BackfillBatch is how many rows one transaction rebuilds.
const BackfillBatch = 1000

// The keys the backfill keeps in ledger_meta: the version whose rules saved the cursor, and
// the last id that version rebuilt.
const (
	backfillVersionKey = "backfill.version"
	backfillCursorKey  = "backfill.cursor"
)

// BackfillOnce rebuilds up to limit rows written with older rules, after the saved cursor,
// in one transaction, and saves the cursor with them. It answers how many rows it rebuilt
// and whether more wait.
func (s *Store) BackfillOnce(ctx context.Context, limit int) (rebuilt int, more bool, err error) {
	err = s.db.Tx(func(tx DB) error {
		rebuilt, more, err = backfillBatch(ctx, tx, limit)
		return err
	})
	if err != nil {
		return rebuilt, false, fmt.Errorf("backfill the ledger: %w", err)
	}
	return rebuilt, more, nil
}

// backfillBatch is one transaction of the backfill. Every statement lands on the
// transaction's DB, so the batch commits whole — the cursor with its rows — or not at all.
func backfillBatch(ctx context.Context, tx DB, limit int) (rebuilt int, more bool, err error) {
	version, err := tx.Get("SELECT value FROM ledger_meta WHERE key = ?", backfillVersionKey)
	if err != nil {
		return 0, false, fmt.Errorf("read the backfill's saved version: %w", err)
	}
	cursor := ""
	if textColumn(version, "value") == strconv.Itoa(ReconcileVersion) {
		saved, err := tx.Get("SELECT value FROM ledger_meta WHERE key = ?", backfillCursorKey)
		if err != nil {
			return 0, false, fmt.Errorf("read the backfill's cursor: %w", err)
		}
		cursor = textColumn(saved, "value")
	}
	ids, err := tx.All("SELECT id FROM ledger_requests WHERE reconciled < ? AND id > ? ORDER BY id LIMIT ?",
		ReconcileVersion, cursor, limit)
	if err != nil {
		return 0, false, fmt.Errorf("gather the rows waiting for a rebuild: %w", err)
	}
	for _, found := range ids {
		if err := ctx.Err(); err != nil {
			return rebuilt, false, fmt.Errorf("backfill the ledger: %w", err)
		}
		id := textColumn(found, "id")
		facts, err := factsOf(ctx, tx, id)
		if err != nil {
			return rebuilt, false, err
		}
		// a row no source ever spoke for gets only the stamp, so the batch never picks it again
		if row, ok := Reconcile(facts); ok {
			if err := upsertRow(tx, row); err != nil {
				return rebuilt, false, err
			}
		} else if err := tx.Run("UPDATE ledger_requests SET reconciled = ? WHERE id = ?",
			ReconcileVersion, id); err != nil {
			return rebuilt, false, fmt.Errorf("stamp the ledger row %s: %w", id, err)
		}
		rebuilt++
	}
	if err := tx.Run("INSERT OR REPLACE INTO ledger_meta (key, value) VALUES (?, ?)",
		backfillVersionKey, strconv.Itoa(ReconcileVersion)); err != nil {
		return rebuilt, false, fmt.Errorf("save the backfill's version: %w", err)
	}
	last := ""
	if len(ids) > 0 {
		last = textColumn(ids[len(ids)-1], "id")
	}
	if err := tx.Run("INSERT OR REPLACE INTO ledger_meta (key, value) VALUES (?, ?)",
		backfillCursorKey, last); err != nil {
		return rebuilt, false, fmt.Errorf("save the backfill's cursor: %w", err)
	}
	return rebuilt, rebuilt == limit, nil
}

// BackfillRun is a running backfill.
type BackfillRun struct {
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// StartBackfill rebuilds older rows in the background, one BackfillBatch per transaction
// with pause between batches, reporting each batch to health; it ends when no older row is
// left, or with ctx or Stop. Without a store it starts nothing and answers nil.
func StartBackfill(ctx context.Context, st *Store, pause time.Duration, health HealthReport) *BackfillRun {
	if st == nil {
		return nil
	}
	if pause <= 0 {
		pause = 50 * time.Millisecond
	}
	run := &BackfillRun{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(run.done)
		for {
			_, more, err := st.BackfillOnce(ctx, BackfillBatch)
			if err != nil {
				reportTo(health, BackfillHealthName, err)
				select {
				case <-ctx.Done():
					return
				case <-run.stop:
					return
				case <-time.After(pause * 10):
				}
				continue
			}
			reportTo(health, BackfillHealthName, nil)
			if !more {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-run.stop:
				return
			case <-time.After(pause):
			}
		}
	}()
	return run
}

// Stop ends the run; its goroutine is out once it returns, the batch it was inside
// committed or rolled back whole. Safe on a nil run and twice.
func (r *BackfillRun) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
}

// Done answers a channel closed once the run's goroutine is out.
func (r *BackfillRun) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
}
