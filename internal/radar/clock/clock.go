// Package clock is Radar's one calendar. A day is a UTC date, the way Anthropic's and the
// providers' usage pages count them, so Radar's daily totals and theirs turn over at the same
// instant wherever the machine's clock is set.
package clock

import "time"

// DayLayout is the shape every day key spells: 2006-01-02.
const DayLayout = "2006-01-02"

// DayKey is the UTC date of a moment.
func DayKey(at time.Time) string {
	return at.UTC().Format(DayLayout)
}

// Midnight is the start of a moment's UTC day.
func Midnight(at time.Time) time.Time {
	utc := at.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
}
