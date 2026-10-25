package clock

import (
	"testing"
	"time"
)

// eastern is a fixed UTC-05:00 zone: its late evening is already the next UTC date.
var eastern = time.FixedZone("UTC-5", -5*3600)

func TestDayKeysAreUTC(t *testing.T) {
	late := time.Date(2026, time.October, 15, 23, 30, 0, 0, eastern)
	if got := DayKey(late); got != "2026-10-16" {
		t.Errorf("DayKey(23:30 at -05:00 on the 15th) = %s, want the UTC date 2026-10-16", got)
	}
	early := time.Date(2026, time.October, 16, 0, 30, 0, 0, time.FixedZone("UTC+3", 3*3600))
	if got := DayKey(early); got != "2026-10-15" {
		t.Errorf("DayKey(00:30 at +03:00 on the 16th) = %s, want the UTC date 2026-10-15", got)
	}
}

func TestMidnightIsTheUTCDayStart(t *testing.T) {
	late := time.Date(2026, time.October, 15, 23, 30, 0, 0, eastern)
	want := time.Date(2026, time.October, 16, 0, 0, 0, 0, time.UTC)
	if got := Midnight(late); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("Midnight(23:30 at -05:00 on the 15th) = %s, want %s", got, want)
	}
}
