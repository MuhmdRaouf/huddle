package board

import (
	"bytes"
	"os"
	"strings"
)

// originFromConfigFile answers the remote.origin.url of the repository's own
// config file. ok is false when the file cannot answer exactly as
// `git config --get` would, and the caller asks git.
//
// git also reads the global and system config, but a remote.origin.url there
// is not a shape a board supports, so a repository whose own file has no
// origin answers empty. insteadOf rules are not applied by `config --get`
// (only `remote get-url` applies them), so the raw value is the answer.
func originFromConfigFile(repoRoot string) (url string, ok bool) {
	path, ok := localConfigPath(repoRoot)
	if !ok {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return originFromConfig(data)
}

// originFromConfig parses git's config syntax far enough to find the last
// url of [remote "origin"]: section and key names ignore case, the subsection
// does not; values take quotes, the escapes \" \\ \n \t \b, a trailing
// backslash as a line continuation, and # or ; comments. [include] and
// [includeIf] pull in files this parser does not follow, and anything it does
// not understand is malformed to it, so both answer ok false and git decides.
func originFromConfig(data []byte) (url string, ok bool) {
	s := bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	s = bytes.ReplaceAll(s, []byte("\r\n"), []byte("\n"))

	var sec configHeader
	inSection := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case isConfigSpace(c):
			i++
		case c == '#' || c == ';':
			i = skipLine(s, i)
		case c == '[':
			var next int
			sec, next, ok = parseConfigHeader(s, i+1)
			if !ok || sec.isInclude() {
				return "", false
			}
			inSection = true
			i = next
		case isAlpha(c) && inSection:
			j := i
			for j < len(s) && isKeyChar(s[j]) {
				j++
			}
			name := strings.ToLower(string(s[i:j]))
			value, hasValue, next, valid := parseConfigValue(s, j)
			if !valid {
				return "", false
			}
			if sec.isOrigin() && name == "url" {
				if !hasValue {
					return "", false
				}
				url = value
			}
			i = next
		default:
			return "", false
		}
	}
	return strings.TrimSpace(url), true
}

// configHeader is a section header: the lower-cased section and the
// subsection exactly as written.
type configHeader struct {
	section, subsection string
	hasSubsection       bool
}

func (h configHeader) isInclude() bool {
	return h.section == "include" || h.section == "includeif"
}

func (h configHeader) isOrigin() bool {
	return h.section == "remote" && h.hasSubsection && h.subsection == "origin"
}

// parseConfigHeader reads a header after its "[": a section name, then "]" or
// a quoted subsection and "]". The deprecated [section.subsection] spelling
// is not parsed.
func parseConfigHeader(s []byte, i int) (h configHeader, next int, ok bool) {
	j := i
	for j < len(s) && isKeyChar(s[j]) {
		j++
	}
	if j == i || j >= len(s) {
		return h, 0, false
	}
	h.section = strings.ToLower(string(s[i:j]))
	if s[j] == ']' {
		return h, j + 1, true
	}
	if s[j] != ' ' && s[j] != '\t' {
		return h, 0, false
	}
	for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
		j++
	}
	if j >= len(s) || s[j] != '"' {
		return h, 0, false
	}
	var sub []byte
	for j++; j < len(s) && s[j] != '"'; j++ {
		if s[j] == '\n' {
			return h, 0, false
		}
		if s[j] == '\\' {
			j++
			if j >= len(s) || s[j] == '\n' {
				return h, 0, false
			}
		}
		sub = append(sub, s[j])
	}
	if j+1 >= len(s) || s[j+1] != ']' {
		return h, 0, false
	}
	h.subsection, h.hasSubsection = string(sub), true
	return h, j + 2, true
}

// parseConfigValue reads what follows a key name: nothing (a key without a
// value, hasValue false) or "=" and a value, up to the end of its line.
func parseConfigValue(s []byte, i int) (value string, hasValue bool, next int, ok bool) {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r') {
		i++
	}
	if i >= len(s) {
		return "", false, i, true
	}
	if s[i] == '\n' {
		return "", false, i + 1, true
	}
	if s[i] != '=' {
		return "", false, 0, false
	}

	var out, pendingSpace []byte
	quoted, comment := false, false
	for i++; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\n':
			if quoted {
				return "", false, 0, false
			}
			return string(out), true, i + 1, true
		case comment:
		case !quoted && isConfigSpace(c):
			if len(out) > 0 {
				pendingSpace = append(pendingSpace, c)
			}
		case !quoted && (c == '#' || c == ';'):
			comment = true
		default:
			out = append(out, pendingSpace...)
			pendingSpace = nil
			switch c {
			case '"':
				quoted = !quoted
			case '\\':
				i++
				if i >= len(s) {
					return "", false, 0, false
				}
				escaped, valid := configEscape(s[i])
				if !valid {
					return "", false, 0, false
				}
				if escaped != 0 {
					out = append(out, escaped)
				}
			default:
				out = append(out, c)
			}
		}
	}
	if quoted {
		return "", false, 0, false
	}
	return string(out), true, i, true
}

// configEscape answers the byte a backslash escape stands for; 0 is a line
// continuation, which adds nothing.
func configEscape(c byte) (b byte, ok bool) {
	switch c {
	case '\n':
		return 0, true
	case 'n':
		return '\n', true
	case 't':
		return '\t', true
	case 'b':
		return '\b', true
	case '"', '\\':
		return c, true
	}
	return 0, false
}

func skipLine(s []byte, i int) int {
	for i < len(s) && s[i] != '\n' {
		i++
	}
	return i
}

func isConfigSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func isAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isKeyChar(c byte) bool {
	return isAlpha(c) || c >= '0' && c <= '9' || c == '-'
}
