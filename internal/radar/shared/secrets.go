package shared

import (
	"bytes"
	"regexp"
)

// The stores keep no secret: RedactSecrets runs before router bodies are split into blocks
// and before transcript content is written to history.db, so neither file holds a known
// secret shape. The shape list below is the one set both stores use; a match becomes
// [redacted:<name>], after the prefix its rule keeps.
//
// Every shape reads plain text and JSON text alike. A JSON string writes a newline as the
// two characters \n, so a shape that must start at a word boundary also accepts the end
// of such an escape, and a shape that ends at a quote or a backslash stops there and
// leaves the string's own quotes and escapes in place.

// secretShape is one secret shape: name labels the replacement, find answers the
// submatch indexes of every match in a text (the whole match, then group 1 when the shape
// keeps a prefix, which stands verbatim before the label), and hints are literals of which
// at least one must appear in the text for the shape to be tried (none: always tried).
type secretShape struct {
	name  string
	find  func(text []byte) [][]int
	hints []string
}

// regexShape is a shape written as a regular expression.
func regexShape(name, pattern string, hints ...string) secretShape {
	shape := regexp.MustCompile(pattern)
	return secretShape{
		name:  name,
		find:  func(text []byte) [][]int { return shape.FindAllSubmatchIndex(text, -1) },
		hints: hints,
	}
}

// leadShape is a shape written as a regular expression that must begin right after a
// lead. The lead is kept verbatim, with the pattern's own group 1 when it has one.
func leadShape(name, pattern string, hints ...string) secretShape {
	shape := regexp.MustCompile(pattern)
	find := func(text []byte) [][]int {
		var matches [][]int
		taken := 0
		for from := 0; from < len(text); {
			loc := shape.FindSubmatchIndex(text[from:])
			if loc == nil {
				break
			}
			start, end := from+loc[0], from+loc[1]
			leadStart, ok := leadBefore(text, start)
			if !ok || leadStart < taken {
				// every later start inside this word has a word character before it too
				from = start + 1
				for from < len(text) && isWordByte(text[from]) {
					from++
				}
				continue
			}
			keptEnd := start
			if len(loc) > 3 && loc[2] >= 0 {
				keptEnd = from + loc[3]
			}
			matches = append(matches, []int{leadStart, end, leadStart, keptEnd})
			taken = end
			from = end
		}
		return matches
	}
	return secretShape{name: name, find: find, hints: hints}
}

// A lead is the text just before a token: the start of the text, a character that cannot
// continue a word, or the end of a JSON newline, return or tab escape. leadShape checks
// it by hand and keeps it verbatim, so its pattern can open on a literal that the regular
// expression engine finds fast.

// pemBody is the characters a private key block holds between its marker lines: base64,
// the header lines of an encrypted key, and line ends, raw or as JSON escapes.
const pemBody = `(?:[A-Za-z0-9+/=:,. \t\r\n-]|\\[nrt])`

// envName is an upper-case environment variable name with a whole word that says secret,
// token, key, password or credential: API_KEY, GITHUB_TOKEN, AWS_SECRET_ACCESS_KEY, but
// not MONKEY, KEYBOARD or max_tokens.
const envName = `(?:[A-Z][A-Z0-9]*_)*(?:SECRET|TOKEN|KEY|PASSWORD|PASSWD|CREDENTIALS?)(?:_[A-Z0-9]+)*`

var secretShapes = []secretShape{
	regexShape("private-key",
		`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`+pemBody+`*?-----END [A-Z0-9 ]*PRIVATE KEY-----`,
		"PRIVATE KEY"),
	// a block cut off before its end marker
	regexShape("private-key",
		`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?:[A-Za-z0-9+/=\r\n]|\\[nrt])+`,
		"PRIVATE KEY"),
	// an access key id with its secret key right behind it
	leadShape("aws-key",
		`(?:AKIA|ASIA)[0-9A-Z]{16}(?:[ \t,;:="']|\\[nrt"']|\r?\n){1,6}[A-Za-z0-9/+]{40,}`,
		"AKIA", "ASIA"),
	// a secret key named by its setting
	regexShape("aws-key",
		`((?i:aws_?secret_?access_?key)(?:\\?["'])?[ \t]*[:=][ \t]*(?:\\?["'])?)[A-Za-z0-9/+]{40}`,
		"ecret", "ECRET"),
	leadShape("aws-key", `(?:AKIA|ASIA)[0-9A-Z]{16}\b`, "AKIA", "ASIA"),
	leadShape("github-token",
		`(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,})`,
		"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"),
	leadShape("slack-token", `xox[abprs]-[A-Za-z0-9-]{10,}`, "xox"),
	leadShape("jwt", `eyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`, "eyJ"),
	regexShape("bearer", `(Bearer[ \t]+)[A-Za-z0-9._~+/=-]{8,}`, "Bearer"),
	leadShape("api-key", `sk-[A-Za-z0-9][A-Za-z0-9_-]{7,}`, "sk-"),
	{name: "api-key", find: dottedFinder(32, isLowerHex, isAlnum)},
	leadShape("huddle-credential", `h(?:cred|dev|sat)_[A-Za-z0-9_-]{16,}`, "hcred_", "hdev_", "hsat_"),
	{name: "join-token", find: dottedFinder(6, isLowerAlnum, isLowerAlnum)},
	regexShape("sign-in-code", `([?&#]code=)[A-Za-z0-9_-]{24}\b`, "code="),
	// NAME=value where NAME says secret: a value of at least four characters, bare or
	// quoted, that is not a placeholder ($VAR, <value>, {x}, (x), %x) or a label an earlier
	// shape already put there
	leadShape("env-secret",
		`(`+envName+`=)`+
			`(?:\\?"[^"\\\r\n\[][^"\\\r\n]{3,}\\?"|'[^'"\\\r\n\[][^'"\\\r\n]{3,}'|[^\s"'\\$<{(%\[][^\s"'\\]{3,})`,
		"SECRET", "TOKEN", "KEY", "PASSWORD", "PASSWD", "CREDENTIAL"),
}

// tailLen is the length of the run after the dot in a dotted token.
const tailLen = 16

func isLowerHex(b byte) bool   { return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' }
func isLowerAlnum(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' }
func isAlnum(b byte) bool      { return isLowerAlnum(b) || b >= 'A' && b <= 'Z' }
func isWordByte(b byte) bool   { return isAlnum(b) || b == '_' }

func allOf(run []byte, ok func(byte) bool) bool {
	for _, b := range run {
		if !ok(b) {
			return false
		}
	}
	return true
}

// leadBefore answers the span of the lead (see lead) that ends at start, false when the
// text before start continues a word.
func leadBefore(text []byte, start int) (int, bool) {
	if start == 0 {
		return 0, true
	}
	previous := text[start-1]
	if !isWordByte(previous) {
		return start - 1, true
	}
	if start >= 2 && text[start-2] == '\\' && (previous == 'n' || previous == 'r' || previous == 't') {
		return start - 2, true
	}
	return 0, false
}

// dottedFinder finds a token of a head run of headLen characters, a dot and a tail run of
// tailLen characters: after a lead, ending where the next character cannot continue a
// word. It answers what the regular expression lead+head{headLen}\.tail{tailLen}\b would,
// scanning for dots instead of trying every position, which a regular expression of
// this shape does too slowly for every transcript the stores read.
func dottedFinder(headLen int, headOK, tailOK func(byte) bool) func([]byte) [][]int {
	return func(text []byte) [][]int {
		var matches [][]int
		taken := 0
		for from := 0; from < len(text); {
			offset := bytes.IndexByte(text[from:], '.')
			if offset < 0 {
				break
			}
			dot := from + offset
			from = dot + 1
			end := dot + 1 + tailLen
			start := dot - headLen
			if start < 0 || end > len(text) || !allOf(text[dot+1:end], tailOK) || !allOf(text[start:dot], headOK) {
				continue
			}
			if end < len(text) && isWordByte(text[end]) {
				continue
			}
			leadStart, ok := leadBefore(text, start)
			if !ok || leadStart < taken {
				continue
			}
			matches = append(matches, []int{leadStart, end, leadStart, start})
			taken = end
			from = end
		}
		return matches
	}
}

// RedactSecrets replaces every known secret shape in text with its [redacted:<name>] label
// and answers the redacted text with how many replacements it made. Bytes outside a match
// stand exactly as they were; text with no match comes back as the same bytes.
func RedactSecrets(text []byte) ([]byte, int) {
	count := 0
	for _, rule := range secretShapes {
		if !hasAny(text, rule.hints) {
			continue
		}
		matches := rule.find(text)
		if len(matches) == 0 {
			continue
		}
		count += len(matches)
		text = replaceMatches(text, matches, rule.name)
	}
	return text, count
}

// hasAny reports whether text holds one of the literals; no literals means yes.
func hasAny(text []byte, literals []string) bool {
	if len(literals) == 0 {
		return true
	}
	for _, literal := range literals {
		if bytes.Contains(text, []byte(literal)) {
			return true
		}
	}
	return false
}

// replaceMatches writes text with each match replaced by the kept prefix (group 1, when
// the match has one) and the label.
func replaceMatches(text []byte, matches [][]int, name string) []byte {
	label := []byte("[redacted:" + name + "]")
	out := make([]byte, 0, len(text))
	last := 0
	for _, match := range matches {
		out = append(out, text[last:match[0]]...)
		if len(match) > 3 && match[2] >= 0 {
			out = append(out, text[match[2]:match[3]]...)
		}
		out = append(out, label...)
		last = match[1]
	}
	return append(out, text[last:]...)
}
