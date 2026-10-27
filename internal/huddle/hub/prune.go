package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// EventTTL is how long the registry keeps a reported event: 90 days.
const EventTTL = 90 * 24 * time.Hour

// PruneEvents deletes the reported events older than the TTL and answers how many went.
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
		return q.Run(ctx, "DELETE FROM events WHERE at < ?", cutoff)
	})
	if err != nil {
		return 0, fmt.Errorf("prune the registry's events: %w", err)
	}
	return int(n), nil
}
