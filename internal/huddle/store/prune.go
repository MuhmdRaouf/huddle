package store

import (
	"context"
	"fmt"
	"time"
)

// EventTTL is how long a channel keeps its events: 90 days, the retention the hub's registry
// holds its reported events under (the hub package's EventTTL). Declared here because the
// hub imports this package, not the other way round.
const EventTTL = 90 * 24 * time.Hour

// eventTSCut is the format a channel writes ts in — UTC, whole seconds, always "Z" (the
// channel's nowISO) — so a cutoff in the same format compares against it as text.
const eventTSCut = "2006-01-02T15:04:05Z"

// spentEvents is the WHERE both the count and the delete run: an event past the cutoff goes
// unless it is an ask still waiting for its reply — needs_reply with no event naming it in
// reply_to — which stays whatever its age, so an inbox never loses a question.
const spentEvents = "WHERE ts < ? AND (needs_reply != 1 OR EXISTS (" +
	"SELECT 1 FROM events r WHERE r.reply_to = events.seq))"

// PruneEvents deletes the events older than the TTL and answers how many went. The count and
// the delete run in one transaction, so the number answers exactly the rows the delete took.
func (s *Store) PruneEvents(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cutoff := now.Add(-EventTTL).UTC().Format(eventTSCut)
	n := int64(0)
	err := s.Tx(ctx, func(ctx context.Context, q Q) error {
		row, err := q.Get(ctx, "SELECT CAST(COUNT(*) AS INTEGER) AS n FROM events "+spentEvents, cutoff)
		if err != nil {
			return err
		}
		if row != nil {
			n, _ = row["n"].(int64)
		}
		return q.Run(ctx, "DELETE FROM events "+spentEvents, cutoff)
	})
	if err != nil {
		return 0, fmt.Errorf("prune a channel's events: %w", err)
	}
	return int(n), nil
}
