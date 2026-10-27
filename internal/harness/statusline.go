// The statusline: huddle's one line in Claude Code's status bar. Install writes the statusLine
// member into Claude Code's settings.json — every other byte left where it was — and records
// the value it replaced in the huddle home's ledger, so remove gives the file back byte for
// byte (or takes a key install added back out, or a file install created away entirely).
package harness

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// statuslineLedgerName is the ledger record's name: not a harness, but a file outside the home
// the same ledger covers.
const statuslineLedgerName = "statusline"

// StatusLineSettingsPath answers the settings file this machine's Claude Code reads:
// $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json. An empty variable counts as
// unset.
func StatusLineSettingsPath(env Env) string {
	if dir := env.lookup("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json")
	}
	return filepath.Join(env.lookup("HOME"), ".claude", "settings.json")
}

// StatusLineResult is what one install or remove did.
type StatusLineResult struct {
	Path     string // the settings file
	Changed  bool   // install wrote the entry
	Already  bool   // the file already held exactly this entry
	Created  bool   // the file was not there; install wrote it
	Removed  bool   // the entry is gone and the previous value (or no key) is back
	NotThere bool   // nothing of ours was in the file
	FileGone bool   // a file install had created is gone with the entry
}

// InstallStatusLine writes the statusline entry into Claude Code's settings.json and records
// the value it replaced in the ledger. A file that does not parse is refused before anything
// is written. Running it again with the entry already ours changes nothing.
func InstallStatusLine(hudHome string, env Env, bin string) (StatusLineResult, error) {
	member := settingsMember{
		ledgerName: statuslineLedgerName,
		key:        "statusLine",
		value:      fmt.Sprintf("{%q:%q,%q:%s}", "type", "command", "command", jsonString(statusLineCommand(bin))),
		ours:       holdsStatusLineOurs,
	}
	return member.install(hudHome, env)
}

// RemoveStatusLine takes the statusline entry back out and restores the value the ledger
// recorded, byte for byte; a key there was none of is taken out, and a file install created
// and nothing else holds goes away entirely. The ledger record goes with it.
func RemoveStatusLine(hudHome string, env Env) (StatusLineResult, error) {
	member := settingsMember{
		ledgerName: statuslineLedgerName,
		key:        "statusLine",
		ours:       holdsStatusLineOurs,
	}
	return member.remove(hudHome, env)
}

// StatusLineChain answers the command of the statusline install replaced, empty when there is
// none or the replaced value runs no command: the statusline word runs it ahead of its own
// line, so huddle's entry keeps the line the user already had.
func StatusLineChain(hudHome string) string {
	ledger := readLedger(ledgerPath(hudHome))
	record, has := ledger.named(statuslineLedgerName)
	if !has || record.Before == "" {
		return ""
	}
	value, err := jsfmt.DecodeValue([]byte(record.Before))
	if err != nil {
		return ""
	}
	if field, ok := strFieldOf(value, "command"); ok {
		return field
	}
	return ""
}

// statusLineCommand answers the command the entry runs: this binary, quoted for a shell, with
// the statusline word after it. bin is the launcher's stable path when one sits beside the
// running binary (StableBin), so the settings survive an upgrade.
func statusLineCommand(bin string) string {
	return jsonString(bin) + " statusline"
}

// holdsStatusLineOurs answers whether a raw member value is the shape of our entry: a command
// statusline whose command ends with the statusline word.
func holdsStatusLineOurs(value []byte) bool {
	decoded, err := jsfmt.DecodeValue(value)
	if err != nil {
		return false
	}
	if kind, ok := strFieldOf(decoded, "type"); !ok || kind != "command" {
		return false
	}
	command, ok := strFieldOf(decoded, "command")
	return ok && strings.HasSuffix(command, " statusline")
}

// strFieldOf answers one string field of an object value.
func strFieldOf(value *jsfmt.Value, key string) (string, bool) {
	if value == nil || value.Kind() != jsfmt.KindObject {
		return "", false
	}
	for i, name := range value.Keys() {
		if name == key {
			if field := value.Fields()[i]; field.Kind() == jsfmt.KindString {
				return field.Text(), true
			}
			return "", false
		}
	}
	return "", false
}

// emptyObject answers whether the bytes are an object with no members (whitespace allowed).
func emptyObject(cfg []byte) bool {
	trimmed := bytes.TrimSpace(cfg)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}
	members, _, err := jsonMembers(trimmed, 0)
	return err == nil && len(members) == 0
}
