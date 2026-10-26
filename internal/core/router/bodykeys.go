package router

// The body's top-level byte splitter: one string-aware scan that answers a whole JSON object's
// members as spans of the body's own bytes, so a provider-bound body is rewritten without the
// parse. A body the splitter refuses keeps the parsed path.

import (
	"encoding/binary"
	"encoding/json"
)

// keySpan is one top-level member: its key as the JSON text decodes it, and the half-open span of
// its value's bytes.
type keySpan struct {
	key        string
	valueStart int
	valueEnd   int
}

// splitTopLevel scans body as one whole JSON object and answers every top-level member as a span
// of the body's own bytes. It answers false for everything else: a body that is no object, bytes
// after the closing brace, a repeated key, or JSON the decoder would refuse. The scan counts
// brackets outside strings and builds no tree; no value's bytes are copied.
func splitTopLevel(body []byte) ([]keySpan, bool) {
	at := skipSpace(body, 0)
	if at >= len(body) || body[at] != '{' {
		return nil, false
	}
	at = skipSpace(body, at+1)
	var spans []keySpan
	if at < len(body) && body[at] == '}' {
		return spans, skipSpace(body, at+1) == len(body)
	}
	for {
		at = skipSpace(body, at)
		if at >= len(body) || body[at] != '"' {
			return nil, false
		}
		raw, next, ok := scanString(body, at)
		if !ok {
			return nil, false
		}
		key, err := unescapedKey(raw)
		if err != nil {
			return nil, false
		}
		at = skipSpace(body, next)
		if at >= len(body) || body[at] != ':' {
			return nil, false
		}
		at = skipSpace(body, at+1)
		valueStart := at
		at, ok = scanValue(body, at, 1)
		if !ok {
			return nil, false
		}
		for _, seen := range spans {
			if seen.key == key {
				return nil, false
			}
		}
		spans = append(spans, keySpan{key: key, valueStart: valueStart, valueEnd: at})
		at = skipSpace(body, at)
		if at >= len(body) {
			return nil, false
		}
		if body[at] == ',' {
			at++
			continue
		}
		if body[at] == '}' {
			return spans, skipSpace(body, at+1) == len(body)
		}
		return nil, false
	}
}

// unescapedKey answers the text a member's raw key bytes decode to, so a key written with escapes
// matches the plain name.
func unescapedKey(raw []byte) (string, error) {
	var text string
	err := json.Unmarshal(raw, &text)
	return text, err
}

// spanOf is the member named key, or nil when the spans carry none.
func spanOf(spans []keySpan, key string) *keySpan {
	for at := range spans {
		if spans[at].key == key {
			return &spans[at]
		}
	}
	return nil
}

// firstItem is the first element of a JSON array as the array's own bytes, quotes and escapes as
// they came. A body that is no array of at least one element has none.
func firstItem(array []byte) ([]byte, bool) {
	at := skipSpace(array, 0)
	if at >= len(array) || array[at] != '[' {
		return nil, false
	}
	at = skipSpace(array, at+1)
	if at < len(array) && array[at] == ']' {
		return nil, false
	}
	start := at
	at, ok := scanValue(array, at, 2)
	if !ok {
		return nil, false
	}
	return array[start:at], true
}

// spliced is the body with one member's value replaced: the bytes before it, the new value and
// the bytes after it, in one allocation.
func spliced(body []byte, span keySpan, value []byte) []byte {
	out := make([]byte, 0, len(body)+len(value)-(span.valueEnd-span.valueStart))
	out = append(out, body[:span.valueStart]...)
	out = append(out, value...)
	out = append(out, body[span.valueEnd:]...)
	return out
}

// maxScanDepth is how deep the scan follows nesting, the decoder's own ceiling: a compound one
// past it is refused, as the decoder refuses it, and the body keeps the parsed path.
const maxScanDepth = 10000

// skipSpace is the index of the first byte at or after at that is no JSON whitespace.
func skipSpace(body []byte, at int) int {
	for at < len(body) {
		switch body[at] {
		case ' ', '\t', '\n', '\r':
			at++
		default:
			return at
		}
	}
	return at
}

// scanValue scans one JSON value opening at `at`, `depth` brackets in, and answers the index of
// the first byte past it.
func scanValue(body []byte, at, depth int) (int, bool) {
	if at >= len(body) {
		return 0, false
	}
	switch c := body[at]; {
	case c == '"':
		_, next, ok := scanString(body, at)
		return next, ok
	case c == '{':
		return scanCompound(body, at, depth, '{', '}')
	case c == '[':
		return scanCompound(body, at, depth, '[', ']')
	case c == 't':
		return literalEnd(body, at, "true")
	case c == 'f':
		return literalEnd(body, at, "false")
	case c == 'n':
		return literalEnd(body, at, "null")
	case c == '-' || c >= '0' && c <= '9':
		return scanNumber(body, at)
	default:
		return 0, false
	}
}

// literalEnd answers past a literal's last byte when the bytes at `at` are exactly it.
func literalEnd(body []byte, at int, literal string) (int, bool) {
	if at+len(literal) <= len(body) && string(body[at:at+len(literal)]) == literal {
		return at + len(literal), true
	}
	return 0, false
}

// scanCompound scans one whole object or array opening at `at`: every member a string key, a
// colon and a value, every element a value, commas between them, strings and nesting aware.
func scanCompound(body []byte, at, depth int, open, shut byte) (int, bool) {
	if depth >= maxScanDepth {
		return 0, false
	}
	at = skipSpace(body, at+1)
	if at < len(body) && body[at] == shut {
		return at + 1, true
	}
	for {
		at = skipSpace(body, at)
		if open == '{' {
			if at >= len(body) || body[at] != '"' {
				return 0, false
			}
			_, next, ok := scanString(body, at)
			if !ok {
				return 0, false
			}
			at = skipSpace(body, next)
			if at >= len(body) || body[at] != ':' {
				return 0, false
			}
			at = skipSpace(body, at+1)
		}
		next, ok := scanValue(body, at, depth+1)
		if !ok {
			return 0, false
		}
		at = skipSpace(body, next)
		if at >= len(body) {
			return 0, false
		}
		if body[at] == ',' {
			at++
			continue
		}
		if body[at] == shut {
			return at + 1, true
		}
		return 0, false
	}
}

// The plain-string skip's byte lanes: the quote and the backslash a string's inside can hold, the
// space every raw control byte ends below, and the borrow and high-bit masks of the byte-equality
// test.
const (
	quoteEvery  = 0x2222222222222222 // '"'
	slashEvery  = 0x5c5c5c5c5c5c5c5c // '\\'
	spaceEvery  = 0x2020202020202020 // ' '
	onesEvery   = 0x0101010101010101
	hiBitsEvery = 0x8080808080808080
)

// scanString scans the JSON string opening at `at` and answers its raw bytes, quotes and escapes
// as they came. Every escape form is a string's inside: \" \\ \/ \b \f \n \r \t and \u with four
// hex digits. A raw control byte, a bad escape or an unterminated quote is no string.
func scanString(body []byte, at int) ([]byte, int, bool) {
	start := at
	at++
	for at < len(body) {
		// Plain string bytes skip eight at a time: the test trips on a word holding a quote, a
		// backslash or a byte below the space, and on nothing else, so a quiet word is eight safe
		// bytes at once. A tripped word falls to the byte loop, which decides every byte exactly.
		for at+8 <= len(body) {
			word := binary.LittleEndian.Uint64(body[at:])
			quote := word ^ quoteEvery
			slash := word ^ slashEvery
			if ((quote-onesEvery)&^quote|(slash-onesEvery)&^slash|(word-spaceEvery)&^word)&hiBitsEvery != 0 {
				break
			}
			at += 8
		}
		if at >= len(body) {
			break
		}
		switch body[at] {
		case '"':
			return body[start : at+1], at + 1, true
		case '\\':
			if at+1 >= len(body) {
				return nil, 0, false
			}
			switch body[at+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				at += 2
			case 'u':
				if at+6 > len(body) || !isHex(body[at+2:at+6]) {
					return nil, 0, false
				}
				at += 6
			default:
				return nil, 0, false
			}
		default:
			if body[at] < 0x20 {
				return nil, 0, false
			}
			at++
		}
	}
	return nil, 0, false
}

// isHex answers whether the bytes are exactly four hex digits, as a \u escape's tail must be.
func isHex(digits []byte) bool {
	if len(digits) != 4 {
		return false
	}
	for _, digit := range digits {
		switch {
		case digit >= '0' && digit <= '9', digit >= 'a' && digit <= 'f', digit >= 'A' && digit <= 'F':
		default:
			return false
		}
	}
	return true
}

// scanNumber scans a JSON number opening at `at`: no leading zero, a fraction and an exponent each
// carrying at least one digit, the decoder's grammar byte for byte.
func scanNumber(body []byte, at int) (int, bool) {
	if at < len(body) && body[at] == '-' {
		at++
	}
	digits := 0
	for at < len(body) && body[at] >= '0' && body[at] <= '9' {
		at++
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	if digits > 1 && body[at-digits] == '0' {
		return 0, false
	}
	if at < len(body) && body[at] == '.' {
		at++
		fraction := 0
		for at < len(body) && body[at] >= '0' && body[at] <= '9' {
			at++
			fraction++
		}
		if fraction == 0 {
			return 0, false
		}
	}
	if at < len(body) && (body[at] == 'e' || body[at] == 'E') {
		at++
		if at < len(body) && (body[at] == '+' || body[at] == '-') {
			at++
		}
		exponent := 0
		for at < len(body) && body[at] >= '0' && body[at] <= '9' {
			at++
			exponent++
		}
		if exponent == 0 {
			return 0, false
		}
	}
	return at, true
}
