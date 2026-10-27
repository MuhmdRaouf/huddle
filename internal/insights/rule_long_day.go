package insights

import (
	"fmt"
	"slices"
	"time"
)

// The points long_day fires at.
const (
	longDayLength   = 10 * time.Hour // the alive hours of a day over which it is a long one
	longDayDaysBack = 1              // how many days before today the rule still looks at
)

// longDayTips names a day whose sessions together stayed alive past 10 hours: the union of the
// spans clipped to the day, a running span ending at now, with overlapping sessions counted
// once. Only the current UTC day and the one before it are candidates, and the id carries the
// day, so one dismissal holds for the day.
func longDayTips(in Input) []Tip {
	now := in.Now.UTC()
	var tips []Tip
	for back := 0; back <= longDayDaysBack; back++ {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -back)
		total := aliveIn(day, day.AddDate(0, 0, 1), in.Spans, now)
		if total <= longDayLength {
			continue
		}
		when := "on " + day.Weekday().String()
		if back == 0 {
			when = "today"
		}
		tips = append(tips, Tip{
			ID:   fmt.Sprintf("%s:%s", RuleLongDay, day.Format("2006-01-02")),
			Rule: RuleLongDay,
			Sentence: fmt.Sprintf("Sessions ran for %s %s, past %d h.",
				formatWait(total), when, int64(longDayLength/time.Hour)),
			Count: int(total / time.Minute),
		})
	}
	return tips
}

// aliveIn is how long the spans together kept a session alive in the day: each span clipped to
// the day and to now, a zero End meaning still running, and overlaps merged so two sessions at
// once count once.
func aliveIn(dayStart, dayEnd time.Time, spans []SessionSpan, now time.Time) time.Duration {
	pieces := make([][2]time.Time, 0, len(spans))
	for _, span := range spans {
		from, to := span.Start, span.End
		if from.Before(dayStart) {
			from = dayStart
		}
		if to.IsZero() || to.After(now) {
			to = now
		}
		if to.After(dayEnd) {
			to = dayEnd
		}
		if !from.Before(to) {
			continue
		}
		pieces = append(pieces, [2]time.Time{from, to})
	}
	slices.SortFunc(pieces, func(a, b [2]time.Time) int { return a[0].Compare(b[0]) })
	total := time.Duration(0)
	var end time.Time
	for _, piece := range pieces {
		switch {
		case end.Before(piece[0]): // a gap: the whole piece adds
			total += piece[1].Sub(piece[0])
			end = piece[1]
		case end.Before(piece[1]): // an overlap: only what reaches past the piece before adds
			total += piece[1].Sub(end)
			end = piece[1]
		}
	}
	return total
}
