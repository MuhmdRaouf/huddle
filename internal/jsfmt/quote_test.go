package jsfmt

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// controlBytesPair builds the 32 C0 bytes and the text WriteQuoted gives them: the five
// short forms plus \u00xx in lowercase hex for the rest.
func controlBytesPair() (in, want string) {
	var raw, quoted strings.Builder
	for c := range 32 {
		raw.WriteByte(byte(c))
		switch byte(c) {
		case '\b':
			quoted.WriteString(`\b`)
		case '\f':
			quoted.WriteString(`\f`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		default:
			fmt.Fprintf(&quoted, `\u%04x`, c)
		}
	}
	return raw.String(), `"` + quoted.String() + `"`
}

func TestWriteQuoted(t *testing.T) {
	controls, controlsQuoted := controlBytesPair()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"quote and backslash escape", `a"b\c`, `"a\"b\\c"`},
		{"five controls take their short forms", "\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"every C0 byte, lowercase hex", controls, controlsQuoted},
		{"html and separators stay raw", "<>&  ", "\"<>&  \""},
		{"non-ascii stays raw", "é日本\U0001F600", "\"é日本\U0001F600\""},
		{"lone surrogate from a UTF-16 cut", "\xed\xa0\x80", `"\ud800"`},
		{"stray invalid byte becomes raw U+FFFD", "\xff", "\"�\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var builder strings.Builder
			WriteQuoted(&builder, tc.in)
			if got := builder.String(); got != tc.want {
				t.Errorf("*strings.Builder: got %s, want %s", got, tc.want)
			}
			var buffer bytes.Buffer
			WriteQuoted(&buffer, tc.in)
			if got := buffer.String(); got != tc.want {
				t.Errorf("*bytes.Buffer: got %s, want %s", got, tc.want)
			}
		})
	}
}
