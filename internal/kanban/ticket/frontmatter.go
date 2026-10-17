// Package ticket reads and writes the kanban ticket file format: front matter,
// body sections, the ticket bar and the derived badges. Pure bytes in, bytes
// out; the board packages own the disk.
package ticket

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrUnquotable marks a value the front matter cannot store: mixing quote kinds,
// or a backslash next to a single quote, has no safe quoting.
var ErrUnquotable = errors.New("value mixes quote kinds")

// foldedHeads are the block-scalar heads a field value can open with.
var foldedHeads = map[string]bool{">": true, ">-": true, ">+": true, "|": true, "|-": true, "|+": true}

// yamlWords read back as bool or null when bare, so they are stored quoted.
var yamlWords = map[string]bool{
	"true": true, "false": true, "yes": true, "no": true,
	"on": true, "off": true, "null": true, "~": true,
}

// quotedKeys hold prose: written double-quoted (single-quoted when the value
// carries a double quote or a backslash), whitespace collapsed.
var quotedKeys = map[string]bool{
	"title": true, "summary": true, "unblock_when": true, "blocked_reason": true,
	"result": true, "review_note": true, "test_note": true, "checks_passed": true,
	"moved_by_hand": true,
}

// scalarKeys hold short free values: bare when they read back unchanged,
// quoted otherwise.
var scalarKeys = map[string]bool{"branch": true, "worktree": true, "repos": true, "verified_merge": true}

// Split answers the front-matter text (the lines between the two --- markers)
// and the body after the closing marker. ok is false when the text does not
// open with a --- line, in which case the whole text is the body. Carriage
// returns paired with a newline are dropped, so a card saved with CRLF reads.
func Split(src string) (front, body string, ok bool) {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(src, "\n")
	if lines[0] != "---" {
		return "", src, false
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t\r") == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return strings.Join(lines[1:], "\n"), "", true
}

// Field answers one key's value from the front-matter text: plain, quoted (also
// multi-line) or a folded or literal block, with the outer quotes removed. The
// second result is false when the key is absent.
func Field(front, key string) (string, bool) {
	return fieldIn(strings.Split(front, "\n"), key)
}

func fieldIn(lines []string, key string) (string, bool) {
	for i, line := range lines {
		head, ok := keyHead(line, key)
		if !ok {
			continue
		}
		return valueAt(lines, i, head), true
	}
	return "", false
}

// keyHead answers the value text after "key:" on one field line, with the outer
// whitespace removed; ok is false on any other line.
func keyHead(line, key string) (string, bool) {
	if key == "" || !strings.HasPrefix(line, key) || len(line) <= len(key) || line[len(key)] != ':' {
		return "", false
	}
	return strings.TrimSpace(line[len(key)+1:]), true
}

// valueAt answers the value that starts at lines[i] with the scalar head.
func valueAt(lines []string, i int, head string) string {
	if foldedHeads[head] {
		return fold(lines[i+1 : blockEnd(lines, i, head)])
	}
	if head != "" {
		if q := head[0]; (q == '"' || q == '\'') && !(len(head) >= 2 && head[len(head)-1] == q) {
			end := blockEnd(lines, i, head)
			parts := []string{head}
			for _, line := range lines[i+1 : end] {
				parts = append(parts, strings.TrimSpace(line))
			}
			return unquote(strings.Join(parts, "\n"))
		}
	}
	return unquote(head)
}

// blockEnd answers the index past the value that starts at lines[i] with the
// scalar head: a folded block or a multi-line quoted scalar spans lines, a
// single-line value ends at i+1.
func blockEnd(lines []string, i int, head string) int {
	if foldedHeads[head] {
		j := i + 1
		for j < len(lines) {
			if indented(lines[j]) {
				j++
			} else if strings.TrimSpace(lines[j]) == "" && j+1 < len(lines) && indented(lines[j+1]) {
				j += 2
			} else {
				break
			}
		}
		return j
	}
	if head != "" {
		if q := head[0]; q == '"' || q == '\'' {
			if !(len(head) >= 2 && head[len(head)-1] == q) {
				j := i + 1
				for j < len(lines) && !endsWithQuote(lines[j], q) {
					j++
				}
				return j + 1
			}
		}
	}
	return i + 1
}

func indented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

func endsWithQuote(line string, q byte) bool {
	trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
	return len(trimmed) > 0 && trimmed[len(trimmed)-1] == q
}

// fold joins an indented block the way folded (>) and literal (|) scalars read:
// consecutive lines become one space-joined paragraph, a blank line starts the
// next paragraph.
func fold(block []string) string {
	var paragraphs []string
	var current []string
	flush := func() {
		if len(current) > 0 {
			paragraphs = append(paragraphs, strings.Join(current, " "))
			current = nil
		}
	}
	for _, line := range block {
		if text := strings.TrimSpace(line); text != "" {
			current = append(current, text)
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(paragraphs, "\n")
}

// unquote strips one pair of matching outer quotes.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// SetField writes key=value into the whole file text, replacing the old value
// including its continuation lines, or appending the field after the authored
// ones. Prose keys are quoted and short scalars stay bare when they read back
// unchanged; a multi-line value becomes a folded block, and any other value is
// written verbatim (a formatted list may mix quote kinds and still read back).
// A value the writer would re-quote that mixes quote kinds, or holds a
// backslash next to a single quote, answers ErrUnquotable.
func SetField(src, key, value string) (string, error) {
	if quotedKeys[key] || scalarKeys[key] {
		if mixedQuotes(value) || unquotableValue(value) {
			return "", ErrUnquotable
		}
	}
	block := key + ": " + value
	if strings.Contains(value, "\n") {
		var folded []string
		for _, line := range strings.Split(value, "\n") {
			folded = append(folded, "  "+line)
		}
		block = key + ": >-\n" + strings.Join(folded, "\n")
	} else if quotedKeys[key] {
		block = key + ": " + quote(value)
	} else if scalarKeys[key] {
		block = key + ": " + scalar(value)
	}
	front, body, ok := Split(src)
	if !ok {
		return "---\n" + block + "\n---\n" + body, nil
	}
	lines := strings.Split(front, "\n")
	var out []string
	done := false
	for i := 0; i < len(lines); {
		if head, is := keyHead(lines[i], key); is && !done {
			out = append(out, block)
			done = true
			i = blockEnd(lines, i, head)
			continue
		}
		out = append(out, lines[i])
		i++
	}
	if !done {
		out = append(out, block) // stamped fields follow the authored ones
	}
	return rebuild(out, body), nil
}

// rebuild closes the front matter around the field lines and appends the body
// as Split answered it; a front matter with no body always ends with a newline.
func rebuild(frontLines []string, body string) string {
	parts := append([]string{"---"}, frontLines...)
	parts = append(parts, "---")
	out := strings.Join(parts, "\n")
	if body == "" {
		return out + "\n"
	}
	return out + "\n" + body
}

// ClearFields removes keys, with any folded or multi-line value, from the front
// matter. A text without front matter is returned unchanged.
func ClearFields(src string, keys ...string) string {
	front, body, ok := Split(src)
	if !ok {
		return src
	}
	want := make(map[string]bool, len(keys))
	for _, key := range keys {
		want[key] = true
	}
	lines := strings.Split(front, "\n")
	var out []string
	for i := 0; i < len(lines); {
		if key, head, is := fieldLine(lines[i]); is && want[key] {
			i = blockEnd(lines, i, head)
			continue
		}
		out = append(out, lines[i])
		i++
	}
	return rebuild(out, body)
}

// fieldLine splits a front-matter line into its key and value head; ok is false
// for continuation lines, prose and lines whose key is not a name.
func fieldLine(line string) (key, head string, ok bool) {
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return "", "", false
	}
	key = line[:colon]
	if !isKeyName(key) {
		return "", "", false
	}
	return key, strings.TrimSpace(line[colon+1:]), true
}

func isKeyName(key string) bool {
	runes := []rune(key)
	if len(runes) == 0 {
		return false
	}
	first := runes[0]
	if !(first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z' || first == '_') {
		return false
	}
	for _, r := range runes[1:] {
		if !isWordRune(r) && r != '-' {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// ParseList splits a bracketed, comma-separated list; quotes protect commas and
// are removed from the items. A bare comma-separated string parses too, and
// empty items drop out.
func ParseList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = raw[1 : len(raw)-1]
	}
	var items []string
	var current strings.Builder
	var quote rune
	for _, ch := range raw {
		switch {
		case quote != 0:
			current.WriteRune(ch)
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
			current.WriteRune(ch)
		case ch == ',':
			items = append(items, current.String())
			current.Reset()
		default:
			current.WriteRune(ch)
		}
	}
	items = append(items, current.String())
	var out []string
	for _, item := range items {
		if v := unquote(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// FormatList renders a list value: bare items that read back unchanged, quoted
// otherwise.
func FormatList(items []string) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if plain(item) {
			parts = append(parts, item)
		} else {
			parts = append(parts, quote(item))
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// quote stores a scalar so it reads back verbatim: double quotes normally,
// single quotes when the value holds a double quote or a backslash.
func quote(v string) string {
	v = collapse(v)
	if strings.ContainsAny(v, `"\`) {
		return "'" + v + "'"
	}
	return `"` + v + `"`
}

// scalar stores a short free value: bare when it reads back unchanged, quoted
// otherwise.
func scalar(v string) string {
	v = collapse(v)
	if plain(v) {
		return v
	}
	return quote(v)
}

// plain answers whether a bare value reads back as the same string: a token
// that starts with a letter or underscore, continues with word characters,
// dots, slashes, ats, pluses or minuses, and is no reserved word.
func plain(v string) bool {
	if v == "" || yamlWords[strings.ToLower(v)] {
		return false
	}
	runes := []rune(v)
	first := runes[0]
	if !(first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z' || first == '_') {
		return false
	}
	for _, r := range runes[1:] {
		if !isWordRune(r) && !strings.ContainsRune("./@+-", r) {
			return false
		}
	}
	return true
}

// collapse squeezes runs of whitespace to single spaces and trims the ends.
func collapse(v string) string {
	return strings.Join(strings.Fields(v), " ")
}

// mixedQuotes answers whether a value holds both quote kinds, which no quoting
// can store.
func mixedQuotes(v string) bool {
	return strings.Contains(v, "\"") && strings.Contains(v, "'")
}

// unquotableValue answers whether a value holds a backslash and a single quote:
// the single quoting that keeps a backslash literal cannot hold one.
func unquotableValue(v string) bool {
	return strings.Contains(v, "\\") && strings.Contains(v, "'")
}

// AppendNote joins note onto the key's existing value with " | " and writes it
// back, so a later note never erases an earlier one. An empty note changes
// nothing. A join that would mix quote kinds answers ErrUnquotable.
func AppendNote(src, key, note string) (string, error) {
	if note == "" {
		return src, nil
	}
	front, _, _ := Split(src)
	joined := note
	if prev, ok := Field(front, key); ok && prev != "" {
		joined = prev + " | " + note
	}
	return SetField(src, key, joined)
}

// WrapFolded renders a folded field block with its text wrapped at 96 columns,
// the width a new ticket's summary is written at.
func WrapFolded(key, text string) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= 96:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	if line != "" || len(lines) == 0 {
		lines = append(lines, line)
	}
	wrapped := make([]string, len(lines))
	for i, l := range lines {
		wrapped[i] = "  " + l
	}
	return key + ": >-\n" + strings.Join(wrapped, "\n")
}
