// Package clock is Radar's one calendar. Storage keys a day by its UTC date, the way
// Anthropic's and the providers' usage pages count them, so the files and the 5-hour blocks
// turn over at the same instant wherever the machine's clock is set. The views name the owner's
// day in the hub's zone: DayOf and DayKeyIn take that zone, and a nil one is UTC.
package clock

import "time"

// DayLayout is the shape every day key spells: 2006-01-02.
const DayLayout = "2006-01-02"

// DayKey is the UTC date of a moment.
func DayKey(at time.Time) string {
	return DayKeyIn(at, time.UTC)
}

// Midnight is the start of a moment's UTC day.
func Midnight(at time.Time) time.Time {
	return DayOf(at, time.UTC)
}

// DayKeyIn is the date of a moment in zone; a nil zone is UTC.
func DayKeyIn(at time.Time, zone *time.Location) string {
	return at.In(orUTC(zone)).Format(DayLayout)
}

// DayOf is the start of the day a moment falls on in zone, in that zone; a nil zone is UTC.
func DayOf(at time.Time, zone *time.Location) time.Time {
	local := at.In(orUTC(zone))
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location())
}

func orUTC(zone *time.Location) *time.Location {
	if zone == nil {
		return time.UTC
	}
	return zone
}
