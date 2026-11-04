package store

import (
	"context"
	"fmt"
	"time"
)

// EventTTL is how long a channel keeps its events: 90 days. The hub registry keeps its reported
// events under its own, shorter retention (the hub package's EventTTL).
const EventTTL = 90 * 24 * time.Hour

// eventTSCut is the format a channel writes ts in — UTC, whole seconds, always "Z" (the
// channel's nowISO) — so a cutoff in the same format compares against it as text.
const eventTSCut = "2006-01-02T15:04:05Z"

// eventAskWaiting and siblingAskWaiting hold for an ask with no event naming it in reply_to, read
// off the pruned event and off another copy of its group.
const (
	eventAskWaiting   = "(events.needs_reply = 1 AND NOT EXISTS (SELECT 1 FROM events r WHERE r.reply_to = events.seq))"
	siblingAskWaiting = "(s.needs_reply = 1 AND NOT EXISTS (SELECT 1 FROM events r WHERE r.reply_to = s.seq))"
)

// groupKept holds for an event of a message to several sessions when another copy of that message
// stays: a copy that is young or an ask still waiting. The copies of one group sit on consecutive
// seqs from data.group, as many as data.to names, so the lookup is a primary-key range. Copies
// leave together, because a reader the message was not addressed to sees only the group's first
// copy.
const groupKept = "EXISTS (SELECT 1 FROM events s WHERE s.seq BETWEEN json_extract(events.data, '$.group')" +
	" AND json_extract(events.data, '$.group') + json_array_length(events.data, '$.to') - 1" +
	" AND s.seq != events.seq AND json_extract(s.data, '$.group') = json_extract(events.data, '$.group')" +
	" AND (s.ts >= ?1 OR " + siblingAskWaiting + "))"

// spentEvents is the WHERE both the count and the delete run, with the cutoff as ?1: an event past
// the cutoff goes unless it is an ask still waiting for its reply, which stays whatever its age so
// an inbox never loses a question, or a copy of a message to several sessions whose group still
// keeps another copy.
const spentEvents = "WHERE ts < ?1 AND NOT " + eventAskWaiting +
	" AND (json_extract(data, '$.group') IS NULL OR NOT " + groupKept + ")"

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
