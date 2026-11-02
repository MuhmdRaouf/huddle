package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// eventTTLDays is how many days the registry keeps a reported event.
const eventTTLDays = 7

// EventTTL is how long the registry keeps a reported event: 7 days. The event_counts table
// keeps, for ever, how many events of each kind a session reported on each UTC day.
const EventTTL = eventTTLDays * 24 * time.Hour

// countEventsBefore is the statement that adds to event_counts the events whose time is before
// cut, an SQL expression: one row per session, UTC day and kind, added to the row a partly
// swept day already has.
func countEventsBefore(cut string) string {
	return `INSERT INTO event_counts (session_id, day, kind, n)
SELECT session_id, substr(at, 1, 10), kind, COUNT(*) FROM events WHERE at < ` + cut + `
GROUP BY session_id, substr(at, 1, 10), kind
ON CONFLICT(session_id, day, kind) DO UPDATE SET n = n + excluded.n`
}

// PruneEvents deletes the reported events older than the TTL and answers how many went. The
// events are counted into event_counts first, in the same transaction as the delete, so a
// failed delete leaves both tables as they were. Shell tails older than ShellTailTTL go in that
// transaction too. Once it commits, the pages the delete freed go back to the file.
// `at` is written as jsscalar.ISOTime — UTC, milliseconds, always "Z" — so a cutoff in the
// same format compares against it as text.
func (d *DB) PruneEvents(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cutoff := jsscalar.ISOTime(now.Add(-EventTTL))
	n := int64(0)
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		row, err := q.Get(ctx, "SELECT CAST(COUNT(*) AS INTEGER) AS n FROM events WHERE at < ?", cutoff)
		if err != nil {
			return err
		}
		if row != nil {
			n, _ = row["n"].(int64)
		}
		if err := q.Run(ctx, countEventsBefore("?"), cutoff); err != nil {
			return err
		}
		if err := q.Run(ctx, "DELETE FROM events WHERE at < ?", cutoff); err != nil {
			return err
		}
		return q.Run(ctx, "DELETE FROM shell_tails WHERE saved_at < ?", jsscalar.ISOTime(now.Add(-ShellTailTTL)))
	})
	if err != nil {
		return 0, fmt.Errorf("prune the registry's events: %w", err)
	}
	if err := d.st.Run(ctx, "PRAGMA incremental_vacuum"); err != nil {
		return int(n), fmt.Errorf("free the pages of the pruned events: %w", err)
	}
	return int(n), nil
}

// keepIncrementalVacuum puts the file on incremental auto-vacuum, so the pages a sweep frees
// can go back to the file. The mode takes effect through one VACUUM, which a file already on
// it skips; a fresh file has no pages yet and the VACUUM costs nothing.
func keepIncrementalVacuum(ctx context.Context, st *store.Store) error {
	row, err := st.Get(ctx, "PRAGMA auto_vacuum")
	if err != nil {
		return fmt.Errorf("read auto_vacuum: %w", err)
	}
	if mode, _ := row["auto_vacuum"].(int64); mode == 2 {
		return nil
	}
	if err := st.Run(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		return fmt.Errorf("set auto_vacuum: %w", err)
	}
	if err := st.Run(ctx, "VACUUM"); err != nil {
		return fmt.Errorf("vacuum for auto_vacuum: %w", err)
	}
	return nil
}
