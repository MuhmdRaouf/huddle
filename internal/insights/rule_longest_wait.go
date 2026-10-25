package insights

import (
	"fmt"
	"time"
)

// The points longest_wait fires at.
const (
	longestWaitWindow = 7 * 24 * time.Hour // how far back a decided item still counts
	longestWaitMin    = 30 * time.Minute   // the wait over which the longest item is a tip
)

// longestWaitTips names the needs-you item that waited longest for its answer: of the items
// decided in the last 7 days, the one with the longest wait from CreatedAt to DecidedAt, when
// that wait is over 30 minutes; on a tie the older item. An item still unanswered does not
// count, and one tip at most: pairing the phone answers every waiting ask at once.
func longestWaitTips(in Input) []Tip {
	windowStart := in.Now.Add(-longestWaitWindow)
	var longest Item
	found := false
	for _, item := range in.Items {
		if item.DecidedAt.IsZero() {
			continue
		}
		if item.CreatedAt.Before(windowStart) || item.CreatedAt.After(in.Now) {
			continue
		}
		wait := item.DecidedAt.Sub(item.CreatedAt)
		best := longest.DecidedAt.Sub(longest.CreatedAt)
		if !found || wait > best || (wait == best && item.CreatedAt.Before(longest.CreatedAt)) {
			longest, found = item, true
		}
	}
	if !found {
		return nil
	}
	wait := longest.DecidedAt.Sub(longest.CreatedAt)
	if wait <= longestWaitMin {
		return nil
	}
	return []Tip{{
		ID:      fmt.Sprintf("%s:%s:%d", RuleLongestWait, longest.Session, longest.CreatedAt.Unix()),
		Rule:    RuleLongestWait,
		Session: longest.Session,
		Sentence: fmt.Sprintf("A %s ask in %s waited %s on %s. Pair your phone to answer from it.",
			longest.Kind, longest.Session, formatWait(wait), longest.CreatedAt.UTC().Weekday()),
		Count: 1,
		Wait:  wait,
	}}
}
