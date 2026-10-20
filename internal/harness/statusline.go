// The statusline: huddle's one line in Claude Code's status bar. Install writes the statusLine
// member into Claude Code's settings.json — every other byte left where it was — and records
// the value it replaced in the huddle home's ledger, so remove gives the file back byte for
// byte (or takes a key install added back out, or a file install created away entirely).
package harness

import (
	"bytes"
	"fmt"
	"os"
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
	path := StatusLineSettingsPath(env)
	before, existed, err := readIfExists(path)
	if err != nil {
		return StatusLineResult{}, err
	}
	value := fmt.Sprintf("{%q:%q,%q:%s}", "type", "command", "command", jsonString(statusLineCommand(bin)))
	if !existed {
		out := fmt.Sprintf("{\n  %q:%s\n}\n", "statusLine", value)
		if err := writeAtomic0600(path, []byte(out)); err != nil {
			return StatusLineResult{}, err
		}
		if err := recordLedger(hudHome, ledgerEntry{
			Harness: statuslineLedgerName, Path: path, Created: true, Before: "",
		}); err != nil {
			return StatusLineResult{}, err
		}
		return StatusLineResult{Path: path, Changed: true, Created: true}, nil
	}
	if err := jsonCheck(before); err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	previous := ""
	mine, err := jsonFind(before, 0, "statusLine")
	if err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	if mine != nil {
		if jsonEqual(before[mine.value.start:mine.value.end], []byte(value)) {
			// already ours: only a lost ledger record is filled in, never over the first
			// install's own previous value
			ledger := readLedger(ledgerPath(hudHome))
			if _, has := ledger.named(statuslineLedgerName); !has {
				if err := recordLedger(hudHome, ledgerEntry{
					Harness: statuslineLedgerName, Path: path, Before: string(before[mine.value.start:mine.value.end]),
				}); err != nil {
					return StatusLineResult{}, err
				}
			}
			return StatusLineResult{Path: path, Already: true}, nil
		}
		previous = string(before[mine.value.start:mine.value.end])
		// the value's span swapped for ours: the key, the members around it and every byte
		// of whitespace stay exactly as they were, and remove's swap back is its exact inverse
		next := jsonReplaceValue(before, *mine, value)
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	} else {
		next, err := jsonInsertMember(before, 0, fmt.Sprintf("%q:%s", "statusLine", value))
		if err != nil {
			return StatusLineResult{}, &MalformedError{Path: path, Err: err}
		}
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	}
	if err := recordLedger(hudHome, ledgerEntry{
		Harness: statuslineLedgerName, Path: path, Before: previous,
	}); err != nil {
		return StatusLineResult{}, err
	}
	return StatusLineResult{Path: path, Changed: true}, nil
}

// RemoveStatusLine takes the statusline entry back out and restores the value the ledger
// recorded, byte for byte; a key there was none of is taken out, and a file install created
// and nothing else holds goes away entirely. The ledger record goes with it.
func RemoveStatusLine(hudHome string, env Env) (StatusLineResult, error) {
	ledger := readLedger(ledgerPath(hudHome))
	record, hasRecord := ledger.named(statuslineLedgerName)
	path := StatusLineSettingsPath(env)
	if hasRecord && record.Path != "" {
		path = record.Path
	}
	before, existed, err := readIfExists(path)
	if err != nil {
		return StatusLineResult{}, err
	}
	if !existed {
		if hasRecord {
			if err := dropLedger(hudHome, statuslineLedgerName); err != nil {
				return StatusLineResult{}, err
			}
		}
		return StatusLineResult{Path: path, NotThere: true}, nil
	}
	if err := jsonCheck(before); err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	mine, err := jsonFind(before, 0, "statusLine")
	if err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	if mine == nil {
		return StatusLineResult{Path: path, NotThere: true}, nil
	}
	if hasRecord {
		if record.Before != "" {
			// the exact inverse of install's swap: the recorded value back in the value's
			// span, so the file is byte for byte what it was
			next := jsonReplaceValue(before, *mine, record.Before)
			if err := writeAtomic0600(path, next); err != nil {
				return StatusLineResult{}, err
			}
		} else {
			next := jsonCutMember(before, *mine)
			if record.Created && emptyObject(next) {
				if err := os.Remove(path); err != nil {
					return StatusLineResult{}, err
				}
				if err := dropLedger(hudHome, statuslineLedgerName); err != nil {
					return StatusLineResult{}, err
				}
				return StatusLineResult{Path: path, Removed: true, FileGone: true}, nil
			}
			if err := writeAtomic0600(path, next); err != nil {
				return StatusLineResult{}, err
			}
		}
	} else {
		// no record: ours is still recognisable — a command entry whose word is the
		// statusline's — and anything else is the user's, never touched
		if !holdsStatusLineOurs(before[mine.value.start:mine.value.end]) {
			return StatusLineResult{Path: path, NotThere: true}, nil
		}
		next := jsonCutMember(before, *mine)
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	}
	if err := dropLedger(hudHome, statuslineLedgerName); err != nil {
		return StatusLineResult{}, err
	}
	return StatusLineResult{Path: path, Removed: true}, nil
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
