package jsfmt

import "strings"

// SpaceSet lists the code points of JavaScript's \s and String.prototype.trim: the ECMAScript
// WhiteSpace and LineTerminator characters. RE2's \s is ASCII without \v, so a Go regular
// expression that matches what a JavaScript one matches builds its classes from this instead.
const SpaceSet = `\t\n\v\f\r \x{00A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

// SpaceClass is SpaceSet as one character class.
const SpaceClass = `[` + SpaceSet + `]`

// IsSpace reports whether r is one of the code points SpaceSet lists: what JavaScript counts
// as whitespace. Go's unicode.IsSpace differs on both edges: it admits U+0085 and not U+FEFF.
func IsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ',
		0x00A0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return 0x2000 <= r && r <= 0x200A
}

// Trim removes JavaScript's whitespace from both ends of s, as String.prototype.trim does.
func Trim(s string) string {
	return strings.TrimFunc(s, IsSpace)
}
