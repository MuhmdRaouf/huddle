// The Codex CLI keeps its MCP servers in config.toml tables. Huddle appends one block of its
// own, delimited by marker comments, and never parses or rewrites anything outside the block:
// the rest of the file stays byte for byte, whatever TOML it holds.
package harness

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// The markers around huddle's block, one per line.
const (
	tomlBegin = "# huddle:begin"
	tomlEnd   = "# huddle:end"
)

// codex is the Codex CLI adapter.
var codex = tomlAdapter{}

// tomlAdapter is a harness whose config is the one TOML file huddle appends its block to.
type tomlAdapter struct{}

func (tomlAdapter) Name() string { return "codex" }

func (tomlAdapter) ConfigPath(env Env) string {
	if home := env.lookup("CODEX_HOME"); home != "" {
		return filepath.Join(home, "config.toml")
	}
	return filepath.Join(homeDir(env, ".codex"), "config.toml")
}

func (a tomlAdapter) Detect(env Env) bool {
	if env.which("codex") {
		return true
	}
	_, err := os.Stat(a.ConfigPath(env))
	return err == nil
}

// Install answers the config with huddle's block in it: appended when there is none, replaced
// when the block is there but no longer ours.
func (a tomlAdapter) Install(cfg []byte, entry Entry) ([]byte, error) {
	if cfg == nil {
		return a.block(entry, ""), nil
	}
	if err := a.check(cfg); err != nil {
		return nil, err
	}
	block, found := a.find(cfg)
	if !found {
		return append(cfg, a.block(entry, "\n")...), nil
	}
	want := a.inner(entry)
	if block.inner == want {
		return cfg, nil
	}
	out := make([]byte, 0, len(cfg)-len(block.inner)+len(want))
	out = append(out, cfg[:block.innerStart]...)
	out = append(out, want...)
	out = append(out, cfg[block.innerEnd:]...)
	return out, nil
}

// Remove answers the config with huddle's block gone. A block that is no longer ours is the
// user's: ErrEntryChanged, and the bytes come back untouched.
func (a tomlAdapter) Remove(cfg []byte, entry Entry) ([]byte, error) {
	if err := a.check(cfg); err != nil {
		return nil, err
	}
	block, found := a.find(cfg)
	if !found {
		return cfg, ErrEntryChanged
	}
	if block.inner != a.inner(entry) {
		return cfg, ErrEntryChanged
	}
	return append(append([]byte{}, cfg[:block.cutStart]...), cfg[block.cutEnd:]...), nil
}

// contains answers whether the config holds huddle's block.
func (a tomlAdapter) contains(cfg []byte) bool {
	_, found := a.find(cfg)
	return found
}

// emptied answers whether the config is empty of user content: whitespace only, which is what
// a created file looks like once the block is cut out.
func (tomlAdapter) emptied(cfg []byte) bool {
	return len(bytes.TrimLeft(cfg, " \t\n\r")) == 0
}

// tomlBlock is where huddle's block sits in the file: its cut and its inner text.
type tomlBlock struct {
	cutStart   int // where the removal starts: the newline install put before the block
	innerStart int // just past the begin marker's line
	innerEnd   int // where the end marker's line starts
	cutEnd     int // just past the end marker's newline
	inner      string
}

// find walks the file line by line for the markers. A begin marker with no end ends the file
// as far as huddle is concerned: everything after it is the block.
func (tomlAdapter) find(cfg []byte) (tomlBlock, bool) {
	return findMarked(cfg, tomlBegin, tomlEnd)
}

// findMarked is find over any one pair of marker lines, so the MCP block and the hooks block
// share the walk and the cut arithmetic.
func findMarked(cfg []byte, begin, end string) (tomlBlock, bool) {
	lines := bytes.Split(cfg, []byte("\n"))
	beginLine, endLine := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimRight(string(line), " \t\r")
		if trimmed == begin && beginLine < 0 {
			beginLine = i
		} else if trimmed == end && beginLine >= 0 && endLine < 0 {
			endLine = i
		}
	}
	if beginLine < 0 {
		return tomlBlock{}, false
	}
	block := tomlBlock{}
	if endLine < 0 {
		// no end marker: the block runs to the end of the file
		endLine = len(lines) - 1
	}
	innerLines := lines[beginLine+1 : endLine]
	block.inner = strings.Join(toStrings(innerLines), "\n")
	if len(innerLines) > 0 {
		block.inner += "\n"
	}
	block.innerStart = lineStart(lines, beginLine) + len(lines[beginLine]) + 1
	lastEnd := lineStart(lines, endLine) + len(lines[endLine])
	block.innerEnd = lineStart(lines, endLine)
	// the cut starts at the newline install wrote ahead of the block, so a file that ended
	// with one keeps it and a file that ended without one gets it back
	block.cutStart = lineStart(lines, beginLine)
	if block.cutStart > 0 && cfg[block.cutStart-1] == '\n' {
		block.cutStart--
	}
	block.cutEnd = lastEnd
	if lastEnd < len(cfg) && cfg[lastEnd] == '\n' {
		block.cutEnd++
	}
	return block, true
}

// lineStart answers the offset of one line of the split file.
func lineStart(lines [][]byte, index int) int {
	start := 0
	for i := 0; i < index; i++ {
		start += len(lines[i]) + 1
	}
	return start
}

// toStrings answers the split lines as strings.
func toStrings(lines [][]byte) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, string(line))
	}
	return out
}

// block answers the bytes install appends: the marker lines around the tables, preceded by the
// lead ("" for a file that is not there, one newline for one that is).
func (tomlAdapter) block(entry Entry, lead string) []byte {
	name := "mcp_servers." + entry.Name
	lines := []string{
		tomlBegin,
		"[" + name + "]",
		"command = " + tomlString(entry.Command),
		"args = [" + tomlList(entry.Args) + "]",
		"",
		"[" + name + ".env]",
	}
	keys := sortedKeys(entry.Env)
	for _, key := range keys {
		lines = append(lines, key+" = "+tomlString(entry.Env[key]))
	}
	if entry.Gateway != "" {
		lines = append(lines, providerLines(entry)...)
	}
	lines = append(lines, tomlEnd)
	return []byte(lead + strings.Join(lines, "\n") + "\n")
}

// providerLines answers the opt-in model provider that routes codex through the router's
// gateway: `codex -c model_provider=huddle` picks it, and the user's own default provider and
// openai_base_url stay untouched, so a ChatGPT login keeps working. The gateway's Responses
// route is plain HTTP on loopback, so no CA rides along; codex appends /responses to the
// base, and the gateway forwards it with the client's own key. Websockets stay off because the
// gateway speaks HTTP only, and the harness header names codex on radar's route line.
func providerLines(entry Entry) []string {
	name := "model_providers." + entry.Name
	return []string{
		"",
		"[" + name + "]",
		"name = " + tomlString(entry.Name),
		"base_url = " + tomlString(entry.Gateway+"/v1"),
		"wire_api = \"responses\"",
		"env_key = \"OPENAI_API_KEY\"",
		"supports_websockets = false",
		"",
		"[" + name + ".http_headers]",
		"X-Huddle-Harness = " + tomlString(entry.Env["HUDDLE_HARNESS"]),
	}
}

// inner answers the lines install writes between the two markers.
func (a tomlAdapter) inner(entry Entry) string {
	full := a.block(entry, "")
	return string(full[len(tomlBegin)+1 : len(full)-len(tomlEnd)-1])
}

// check parses the file far enough to know huddle is not reading garbage: the block edit
// itself never needs the TOML.
func (tomlAdapter) check(cfg []byte) error {
	var prim toml.Primitive
	if _, err := toml.Decode(string(cfg), &prim); err != nil {
		return err
	}
	return nil
}

// sortedKeys answers a string map's keys in order, so the block never depends on map order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// tomlString quotes one value as a TOML basic string: the escapes TOML knows, unicode for the
// control characters it does not.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlList answers a string array as one TOML line.
func tomlList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, tomlString(value))
	}
	return strings.Join(quoted, ", ")
}
