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

func TestDayOfFollowsTheZone(t *testing.T) {
	athens, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Fatalf("load Europe/Athens: %v", err)
	}
	// 00:30 UTC on the 25th is already 03:30 on the 25th in Athens
	at := time.Date(2026, time.October, 25, 0, 30, 0, 0, time.UTC)
	want := time.Date(2026, time.October, 25, 0, 0, 0, 0, athens)
	if got := DayOf(at, athens); !got.Equal(want) || got.Location() != athens {
		t.Errorf("DayOf(00:30Z on the 25th, Athens) = %s, want %s", got, want)
	}
	if got := DayKeyIn(at, athens); got != "2026-10-25" {
		t.Errorf("DayKeyIn(00:30Z on the 25th, Athens) = %s, want 2026-10-25", got)
	}
	// 22:30 UTC on the 24th is 01:30 on the 25th in Athens, a day the UTC calendar has not reached
	if got := DayKeyIn(time.Date(2026, time.October, 24, 22, 30, 0, 0, time.UTC), athens); got != "2026-10-25" {
		t.Errorf("DayKeyIn(22:30Z on the 24th, Athens) = %s, want 2026-10-25", got)
	}
	// a nil zone is UTC
	if got := DayOf(at, nil); !got.Equal(Midnight(at)) || got.Location() != time.UTC {
		t.Errorf("DayOf(at, nil) = %s, want the UTC midnight %s", got, Midnight(at))
	}
}
