// Package jsfmt holds the JavaScript-compatible formatting rules: number and date strings,
// UTF-16 length and slice semantics, and an order-keeping JSON value that re-encodes the way
// JSON.stringify writes it.
package jsfmt

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// JSNumber renders f exactly as JavaScript's String(n): integers without a decimal point,
// shortest round-trip digits, exponent form beyond 1e21 and below 1e-7, "NaN" and "±Infinity",
// and -0 as "0".
func JSNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case f == 0: // covers -0 as well: String(-0) is "0"
		return "0"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f < 0:
		return "-" + JSNumber(-f)
	}
	digits, exp := shortestDigits(f) // f = 0.digits × 10^(exp+1)
	k := len(digits)
	n := exp + 1
	switch {
	case k <= n && n <= 21: // a whole number: the digits then zeros
		return digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21: // the point falls inside the digits
		return digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0: // below one: zeros after "0." then the digits
		return "0." + strings.Repeat("0", -n) + digits
	case k == 1: // a single digit in exponent form has no decimal point
		return digits + "e" + exponent(exp)
	default:
		return digits[:1] + "." + digits[1:] + "e" + exponent(exp)
	}
}

// exponent renders e-notation's signed exponent ("+21", "-7"); JavaScript always writes the sign.
func exponent(exp int) string {
	if exp < 0 {
		return "-" + strconv.Itoa(-exp)
	}
	return "+" + strconv.Itoa(exp)
}

// shortestDigits returns f's shortest round-trip decimal digits and its decimal exponent, so
// f = 0.d₁d₂…dₖ × 10^(exp+1); 1e21 comes back as "1", 21.
func shortestDigits(f float64) (string, int) {
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, expText, _ := strings.Cut(sci, "e")
	exp, _ := strconv.Atoi(expText)
	return strings.Replace(mantissa, ".", "", 1), exp
}

// ToFixed renders f exactly as JavaScript's Number.prototype.toFixed(digits): the exact
// binary value rounded to digits decimals, ties away from zero (V8 negates first, so -2.5
// fixes to -3), the sign kept even on a zero result ("-0.00"), and the plain JSNumber form
// from 1e21 up, where toFixed stops fixing.
func ToFixed(f float64, digits int) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f >= 1e21 || f <= -1e21:
		return JSNumber(f)
	}
	negative := f < 0
	rat := new(big.Rat).SetFloat64(f)
	scaled := new(big.Int).Mul(new(big.Int).Abs(rat.Num()), pow10(digits))
	quotient, remainder := new(big.Int).QuoRem(scaled, rat.Denom(), new(big.Int))
	// Ties away from zero: |x|·10^digits exactly halfway (2·remainder = denominator) rounds up.
	if new(big.Int).Lsh(remainder, 1).Cmp(rat.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	text := quotient.String()
	if len(text) <= digits {
		text = strings.Repeat("0", digits-len(text)+1) + text
	}
	out := text[:len(text)-digits]
	if digits > 0 {
		out += "." + text[len(text)-digits:]
	}
	if negative {
		return "-" + out
	}
	return out
}

// pow10 is 10^digits, for the toFixed shift.
func pow10(digits int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
}

// UTF16Len returns s's length in UTF-16 code units: JavaScript's string .length.
func UTF16Len(s string) int {
	units := 0
	for _, r := range s {
		units++
		if r > 0xFFFF {
			units++ // a rune above the BMP is a surrogate pair
		}
	}
	return units
}

// UTF16Slice returns s's UTF-16 code units from start to end with JavaScript's slice semantics:
// negative indexes count from the end, indexes clamp to [0, length], and a boundary that lands
// inside a surrogate pair keeps the lone unit it cuts out.
func UTF16Slice(s string, start, end int) string {
	n := UTF16Len(s)
	if start < 0 {
		start = max(n+start, 0)
	} else {
		start = min(start, n)
	}
	if end < 0 {
		end = max(n+end, 0)
	} else {
		end = min(end, n)
	}
	if start >= end {
		return ""
	}
	units := utf16Units(s)
	out := make([]byte, 0, 4*(end-start))
	for i := start; i < end; i++ {
		if i+1 < end {
			if r, ok := decodePair(units[i], units[i+1]); ok {
				out = utf8.AppendRune(out, r)
				i++
				continue
			}
		}
		out = appendUnit(out, units[i])
	}
	return string(out)
}

// utf16Units lists s's UTF-16 code units.
func utf16Units(s string) []uint16 {
	units := make([]uint16, 0, len(s))
	for _, r := range s {
		if r <= 0xFFFF {
			units = append(units, uint16(r))
			continue
		}
		hi, lo := utf16.EncodeRune(r)
		units = append(units, uint16(hi), uint16(lo))
	}
	return units
}

// decodePair decodes a surrogate pair; ok is false when the two units are not a pair.
func decodePair(hi, lo uint16) (rune, bool) {
	if hi >= 0xD800 && hi <= 0xDBFF && lo >= 0xDC00 && lo <= 0xDFFF {
		return (rune(hi-0xD800)<<10 | rune(lo-0xDC00)) + 0x10000, true
	}
	return 0, false
}

// appendUnit encodes one UTF-16 code unit. A lone surrogate gets its WTF-8 form, the only way a
// Go string can carry the unit a JavaScript slice can cut out of a pair.
func appendUnit(b []byte, u uint16) []byte {
	switch {
	case u < 0x80:
		return append(b, byte(u))
	case u < 0x800:
		return append(b, byte(0xC0|u>>6), byte(0x80|u&0x3F))
	default:
		return append(b, byte(0xE0|u>>12), byte(0x80|u>>6&0x3F), byte(0x80|u&0x3F))
	}
}

// ISOTime renders t the way JavaScript's toISOString does: UTC, milliseconds, always "Z".
func ISOTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// ParseISO is the inverse of ISOTime for the shapes Date.parse meets here: a full timestamp
// with or without milliseconds and an offset, or a bare date. Anything else is not a date.
func ParseISO(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
