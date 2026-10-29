// The ledger's migrate sweep: history.db's settled rows filed into the ledger, one
// MigrateBatch per transaction, its cursor in ledger_meta. The sweep resumes where it
// stopped and re-runs without filing a row twice. A batch holds the file's one connection
// only for its own transaction, so the watcher's next statement waits out one batch and
// lands after it.
package ledger

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// MigrateSettle is how old a history row must be before the sweep files it: past it, no route
// line can still pair with the row. Every hub start re-reads the spool from byte 0 over
// ingest.SpoolWindowDays days plus the day before (internal/radar/ingest/spool.go), and a line
// pairs with a copy within matchMs, so a younger row may still change.
const MigrateSettle = (7+1)*24*time.Hour + matchMs*time.Millisecond

// MigrateHealthName is the name the sweep reports under.
const MigrateHealthName = "ledger.migrate"

// MigrateBatch is how many history rows one transaction handles.
const MigrateBatch = 1000

// MigrateVersion is the version of the match rules the cursor was saved under; a change to
// the rules raises it, and the sweep starts over (a re-run adds no row).
const MigrateVersion = 1

// The keys the sweep keeps in ledger_meta: the version whose rules saved the cursor, and
// the ts and id of the last row that version handled.
const (
	migrateVersionKey  = "migrate.version"
	migrateCursorTsKey = "migrate.cursor_ts"
	migrateCursorIDKey = "migrate.cursor_id"
)

// MigrateStep is what one batch did.
type MigrateStep struct {
	Filed   int   // rows the ledger lacked, filed as HistoryFacts
	Matched int   // rows the ledger already held
	Waiting int64 // history rows younger than the settle age
	More    bool  // the batch was full, so more settled rows may wait
}

// MigrateOnce handles up to limit settled history rows after the saved cursor, in one
// transaction with the cursor, and answers what it did.
func (s *Store) MigrateOnce(ctx context.Context, r *HistoryReader, now time.Time, limit int) (MigrateStep, error) {
	cutoff := now.Add(-MigrateSettle).UnixMilli()
	cursor, err := s.migrateCursor()
	if err != nil {
		return MigrateStep{}, err
	}
	page, err := r.SettledPage(ctx, cursor, cutoff, limit)
	if err != nil {
		return MigrateStep{}, fmt.Errorf("migrate the ledger: %w", err)
	}
	filed, matched := 0, 0
	if err := s.db.Tx(func(tx DB) error {
		for _, row := range page {
			if err := ctx.Err(); err != nil {
				return err
			}
			m, err := matchOf(ctx, tx, row)
			if err != nil {
				return err
			}
			if m.InLedger {
				matched++
				continue
			}
			facts := HistoryFacts(HistoryFactsOf(row, m.ID))
			if err := fileIn(ctx, tx, m.ID, nil, nil, nil, &facts); err != nil {
				return err
			}
			filed++
		}
		if len(page) == 0 {
			return nil
		}
		return saveMigrateCursor(tx, page[len(page)-1])
	}); err != nil {
		return MigrateStep{}, fmt.Errorf("migrate the ledger: %w", err)
	}
	waiting, err := r.WaitingCount(ctx, cutoff)
	if err != nil {
		return MigrateStep{}, fmt.Errorf("migrate the ledger: %w", err)
	}
	return MigrateStep{Filed: filed, Matched: matched, Waiting: waiting, More: len(page) == limit}, nil
}

// migrateCursor reads the cursor the sweep last saved; it holds only when the saved
// version matches, otherwise the sweep starts from the zero cursor.
func (s *Store) migrateCursor() (HistoryCursor, error) {
	saved, err := s.db.Get("SELECT value FROM ledger_meta WHERE key = ?", migrateVersionKey)
	if err != nil {
		return HistoryCursor{}, fmt.Errorf("read the migrate's saved version: %w", err)
	}
	cursor := HistoryCursor{}
	if textColumn(saved, "value") != strconv.Itoa(MigrateVersion) {
		return cursor, nil
	}
	ts, err := s.db.Get("SELECT value FROM ledger_meta WHERE key = ?", migrateCursorTsKey)
	if err != nil {
		return HistoryCursor{}, fmt.Errorf("read the migrate's cursor: %w", err)
	}
	id, err := s.db.Get("SELECT value FROM ledger_meta WHERE key = ?", migrateCursorIDKey)
	if err != nil {
		return HistoryCursor{}, fmt.Errorf("read the migrate's cursor: %w", err)
	}
	cursor.Ts, _ = strconv.ParseInt(textColumn(ts, "value"), 10, 64)
	cursor.ID = textColumn(id, "value")
	return cursor, nil
}

// saveMigrateCursor stamps the version and the last row's ts and id, inside the batch's
// own transaction: the rows and their cursor commit whole or not at all.
func saveMigrateCursor(tx DB, last HistoryRow) error {
	if err := tx.Run("INSERT OR REPLACE INTO ledger_meta (key, value) VALUES (?, ?)",
		migrateVersionKey, strconv.Itoa(MigrateVersion)); err != nil {
		return fmt.Errorf("save the migrate's version: %w", err)
	}
	if err := tx.Run("INSERT OR REPLACE INTO ledger_meta (key, value) VALUES (?, ?)",
		migrateCursorTsKey, strconv.FormatInt(last.Ts, 10)); err != nil {
		return fmt.Errorf("save the migrate's cursor: %w", err)
	}
	if err := tx.Run("INSERT OR REPLACE INTO ledger_meta (key, value) VALUES (?, ?)",
		migrateCursorIDKey, last.ID); err != nil {
		return fmt.Errorf("save the migrate's cursor: %w", err)
	}
	return nil
}

// MigrateRun is a running migrate sweep.
type MigrateRun struct {
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

// StartMigrate runs the sweep in the background: a batch, pause, the next while More; when no
// settled row waits it checks again every idle, so rows that settle later still file. Each
// batch reports to health, and its step to progress when progress is set. It ends with ctx or
// Stop. Without a store or a reader it starts nothing and answers nil.
func StartMigrate(ctx context.Context, st *Store, r *HistoryReader, now func() time.Time,
	pause, idle time.Duration, health HealthReport, progress func(MigrateStep)) *MigrateRun {
	if st == nil || r == nil {
		return nil
	}
	if pause <= 0 {
		pause = 50 * time.Millisecond
	}
	if idle <= 0 {
		idle = time.Hour
	}
	run := &MigrateRun{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(run.done)
		for {
			step, err := st.MigrateOnce(ctx, r, now(), MigrateBatch)
			reportTo(health, MigrateHealthName, err)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-run.stop:
					return
				case <-time.After(pause * 10):
				}
				continue
			}
			if progress != nil {
				progress(step)
			}
			next := idle
			if step.More {
				next = pause
			}
			select {
			case <-ctx.Done():
				return
			case <-run.stop:
				return
			case <-time.After(next):
			}
		}
	}()
	return run
}

// Stop ends the run; its goroutine is out once it returns, the batch it was inside
// committed or rolled back whole. Safe on a nil run and twice.
func (r *MigrateRun) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done
}

// Done answers a channel closed once the run's goroutine is out.
func (r *MigrateRun) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
}
