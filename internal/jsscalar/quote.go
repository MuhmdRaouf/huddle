package jsscalar

import (
	"io"
	"unicode/utf8"
)

// QuoteWriter is the string sink WriteQuoted writes into; a *strings.Builder and a
// *bytes.Buffer both qualify.
type QuoteWriter interface {
	io.ByteWriter
	io.StringWriter
}

// hexDigits spells a nibble in lowercase hex.
const hexDigits = "0123456789abcdef"

// WriteQuoted writes s as a JSON string the way JSON.stringify does: only the quote, the
// backslash and the C0 controls escape, the five controls with short forms take them,
// the rest take \u00xx in lowercase hex, and a lone surrogate — a UTF-16 cut can leave
// one at the end of a truncated string — takes \udXXX.
func WriteQuoted(out QuoteWriter, s string) {
	out.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if unit, ok := wtf16Unit(s[i:]); ok {
				writeHex4(out, unit)
				i += 3
				continue
			}
			out.WriteString(string(utf8.RuneError))
			i++
			continue
		}
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				writeHex4(out, uint16(r))
			} else {
				out.WriteString(s[i : i+size])
			}
		}
		i += size
	}
	out.WriteByte('"')
}

// writeHex4 writes u as \uHHHH in lowercase hex.
func writeHex4(out QuoteWriter, u uint16) {
	out.WriteString(`\u`)
	out.WriteByte(hexDigits[u>>12&0xf])
	out.WriteByte(hexDigits[u>>8&0xf])
	out.WriteByte(hexDigits[u>>4&0xf])
	out.WriteByte(hexDigits[u&0xf])
}

// wtf16Unit reads one lone-surrogate unit a UTF-16 cut left behind in its WTF-8 form;
// ok is false when the bytes are not that shape.
func wtf16Unit(s string) (uint16, bool) {
	if len(s) < 3 || s[0] != 0xED || s[1] < 0xA0 || s[1] > 0xBF || s[2] < 0x80 || s[2] > 0xBF {
		return 0, false
	}
	return 0xD000 | uint16(s[1]&0x3F)<<6 | uint16(s[2]&0x3F), true
}
