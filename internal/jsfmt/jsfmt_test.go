package jsfmt

import (
	"math"
	"testing"
	"time"
)

// TestJSNumber holds the strings node v24.21.0 prints for String(number); JSNumber must return
// each one byte for byte.
func TestJSNumber(t *testing.T) {
	// jsSum adds as the doubles JavaScript adds: untyped constants would be exact, and 0.1+0.2
	// would come out as 0.3.
	jsSum := func(a, b float64) float64 { return a + b }
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{1, "1"},
		{-1, "-1"},
		{42, "42"},
		{-42.5, "-42.5"},
		{0.1, "0.1"},
		{0.5, "0.5"},
		{-0.1, "-0.1"},
		{100, "100"},
		{1000, "1000"},
		{123.456, "123.456"},
		{1234567.891, "1234567.891"},
		{1.0 / 3, "0.3333333333333333"},
		{2.0 / 3, "0.6666666666666666"},
		{jsSum(0.1, 0.2), "0.30000000000000004"},
		{0.004, "0.004"},
		{0.005, "0.005"},
		{1.005, "1.005"},
		{2.5, "2.5"},
		{-1.5, "-1.5"},
		{310, "310"},
		{1200, "1200"},
		{999999, "999999"},
		{100000.0 / 1000, "100"},
		{512000.0 / 1000, "512"},
		{100000.0 / 1000000, "0.1"},
		{1234567.0 / 1000000, "1.234567"},
		{3.141592653589793, "3.141592653589793"},
		{31.1337, "31.1337"},
		{1e6, "1000000"},
		{1048576, "1048576"},
		{1e9 + 1, "1000000001"},
		{1e15, "1000000000000000"},
		{1e16, "10000000000000000"},
		{1e20, "100000000000000000000"},
		{9.9e20, "990000000000000000000"},
		{1.2345678901234568e20, "123456789012345680000"},
		{1e21, "1e+21"},
		{1.1e21, "1.1e+21"},
		{1.2345678901234568e21, "1.2345678901234568e+21"},
		{1e38, "1e+38"},
		{6.02e23, "6.02e+23"},
		{1.7976931348623157e308, "1.7976931348623157e+308"},
		{1e-2, "0.01"},
		{1e-3, "0.001"},
		{1e-4, "0.0001"},
		{1e-5, "0.00001"},
		{1e-6, "0.000001"},
		{1.000001e-6, "0.000001000001"},
		{1.5e-6, "0.0000015"},
		{1e-7, "1e-7"},
		{1.5e-7, "1.5e-7"},
		{5e-7, "5e-7"},
		{2.5e-8, "2.5e-8"},
		{4.656612873077393e-10, "4.656612873077393e-10"},
		{5e-324, "5e-324"},
		{-1e-7, "-1e-7"},
		{9007199254740991, "9007199254740991"},
		{9007199254740992, "9007199254740992"},
		{9007199254740993, "9007199254740992"},
		{math.NaN(), "NaN"},
		{math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"},
	}
	for _, tt := range tests {
		if got := JSNumber(tt.in); got != tt.want {
			t.Errorf("JSNumber(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestToFixed is ECMAScript's Number.prototype.toFixed: the exact binary value rounded to the
// requested decimals, ties away from zero (V8 negates first), the sign kept even on "-0.00", and
// the plain JSNumber form from 1e21 up.
func TestToFixed(t *testing.T) {
	cases := []struct {
		in     float64
		digits int
		want   string
	}{
		{0, 2, "0.00"},
		{0.004, 2, "0.00"},
		{0.005, 2, "0.01"},
		{1.005, 2, "1.00"},
		{1234567.891, 2, "1234567.89"},
		{1234567.891, 4, "1234567.8910"},
		{1234567.891, 1, "1234567.9"},
		{-1, 2, "-1.00"},
		{1e21, 2, "1e+21"},
		{-1e21, 2, "-1e+21"},
		{1e20, 2, "100000000000000000000.00"},
		{1e-7, 2, "0.00"},
		{9.999, 1, "10.0"},
		{0.05, 1, "0.1"},
		{0.15, 1, "0.1"},
		{0.35, 1, "0.3"},
		{2.675, 2, "2.67"},
		{2.5, 0, "3"},
		{-2.5, 0, "-3"},
		{-0.004, 2, "-0.00"},
		{0.0123, 4, "0.0123"},
		{3.456, 2, "3.46"},
		{42.13, 1, "42.1"},
		{0.1, 1, "0.1"},
		{1e15, 1, "1000000000000000.0"},
		{8.125, 2, "8.13"},
		{8.4, 2, "8.40"},
		{10, 2, "10.00"},
		{math.NaN(), 2, "NaN"},
		{math.Inf(1), 2, "Infinity"},
		{math.Inf(-1), 2, "-Infinity"},
	}
	for _, c := range cases {
		if got := ToFixed(c.in, c.digits); got != c.want {
			t.Errorf("ToFixed(%v, %d): got %q, want %q", c.in, c.digits, got, c.want)
		}
	}
	if got := ToFixed(math.Copysign(0, -1), 2); got != "0.00" {
		t.Errorf("ToFixed(-0, 2): got %q, want %q", got, "0.00")
	}
}

func TestUTF16Len(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"日本語", 3},
		{"é", 1},  // precomposed U+00E9 is one unit
		{"é", 2}, // e plus a combining acute is two
		{"😀", 2},  // U+1F600 is a surrogate pair
		{"a😀b", 4},
		{"中文a😀bc", 7},
	}
	for _, tt := range tests {
		if got := UTF16Len(tt.in); got != tt.want {
			t.Errorf("UTF16Len(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestUTF16Slice(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		start, end int
		want       string
	}{
		{"plain range", "hello world", 0, 5, "hello"},
		{"to the end", "hello", 2, 5, "llo"},
		{"end past the length clamps", "hello", 2, 99, "llo"},
		{"negative start counts from the end", "hello", -3, 5, "llo"},
		{"negative end counts from the end", "hello", 0, -1, "hell"},
		{"both negative", "hello", -4, -2, "el"},
		{"start past end is empty", "hello", 4, 2, ""},
		{"start clamps to zero", "hello", -10, 2, "he"},
		{"past the end is empty", "hello", 10, 12, ""},
		{"cjk is one unit per character", "日本語", 1, 2, "本"},
		{"surrogate pair kept whole", "a😀b", 0, 3, "a😀"},
		{"pair alone", "a😀b", 1, 3, "😀"},
		// A boundary inside a pair keeps the lone unit, as JavaScript does; the lone surrogate
		// rides in the string as its WTF-8 bytes (0xED 0xA0 0xBD is U+D83D).
		{"pair split keeps the high surrogate", "a😀b", 0, 2, "a\xed\xa0\xbd"},
		{"pair split keeps the low surrogate", "a😀b", 2, 4, "\xed\xb8\x80b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UTF16Slice(tt.in, tt.start, tt.end); got != tt.want {
				t.Errorf("UTF16Slice(%q, %d, %d) = %q, want %q", tt.in, tt.start, tt.end, got, tt.want)
			}
		})
	}
}

func TestISOTime(t *testing.T) {
	plus3 := time.FixedZone("+0300", 3*60*60)
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"epoch", time.Unix(0, 0).UTC(), "1970-01-01T00:00:00.000Z"},
		{"milliseconds print as .000 when zero", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-01-01T00:00:00.000Z"},
		{"milliseconds", time.Date(2026, 10, 14, 21, 1, 2, 3000000, time.UTC), "2026-10-14T21:01:02.003Z"},
		{"a zone converts to UTC", time.Date(2026, 10, 14, 23, 1, 2, 3000000, plus3), "2026-10-14T20:01:02.003Z"},
		{"sub-millisecond fraction truncates", time.Date(2026, 10, 14, 21, 1, 2, 3500000, time.UTC), "2026-10-14T21:01:02.003Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ISOTime(tt.in); got != tt.want {
				t.Errorf("ISOTime(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseISO(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Time
		ok   bool
	}{
		{"utc with milliseconds", "2026-01-01T00:00:00.000Z", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"utc without milliseconds", "2030-01-01T00:00:00Z", time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"an offset shifts the moment", "2026-01-01T03:00:00+03:00", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"a bare date is its utc midnight", "2026-01-15", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), true},
		{"not a date", "not-a-date", time.Time{}, false},
		{"empty", "", time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseISO(tt.in)
			if ok != tt.ok || (ok && !got.Equal(tt.want)) {
				t.Errorf("ParseISO(%q) = %v, %v, want %v, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}
