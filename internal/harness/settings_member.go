// One member of Claude Code's settings.json that huddle owns: install writes it in
// byte-exactly, records the value it replaced in the huddle home's ledger, and remove gives
// the file back byte for byte (or takes a key install added back out, or a file install
// created away entirely).
package harness

import (
	"fmt"
	"os"
)

// settingsMember is one settings.json member, parametrising the shared install/remove
// procedure: the ledger record's name, the member's key and value, and the test that
// recognises huddle's own value when the ledger record is lost.
type settingsMember struct {
	ledgerName string                  // the ledger record's name
	key        string                  // the member's key in settings.json
	value      string                  // the JSON value install writes; remove never reads it
	ours       func(value []byte) bool // recognises huddle's value with the record lost
}

// install writes the member into Claude Code's settings.json and records the value it
// replaced in the ledger. A file that does not parse is refused before anything is written.
// Running it again with the member already ours changes nothing.
func (m settingsMember) install(hudHome string, env Env) (StatusLineResult, error) {
	path := StatusLineSettingsPath(env)
	before, existed, err := readIfExists(path)
	if err != nil {
		return StatusLineResult{}, err
	}
	if !existed {
		out := fmt.Sprintf("{\n  %q:%s\n}\n", m.key, m.value)
		if err := writeAtomic0600(path, []byte(out)); err != nil {
			return StatusLineResult{}, err
		}
		if err := recordLedger(hudHome, ledgerEntry{
			Harness: m.ledgerName, Path: path, Created: true, Before: "",
		}); err != nil {
			return StatusLineResult{}, err
		}
		return StatusLineResult{Path: path, Changed: true, Created: true}, nil
	}
	if err := jsonCheck(before); err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	previous := ""
	mine, err := jsonFind(before, 0, m.key)
	if err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	if mine != nil {
		if jsonEqual(before[mine.value.start:mine.value.end], []byte(m.value)) {
			// already ours: only a lost ledger record is filled in, never over the first
			// install's own previous value
			ledger := readLedger(ledgerPath(hudHome))
			if _, has := ledger.named(m.ledgerName); !has {
				if err := recordLedger(hudHome, ledgerEntry{
					Harness: m.ledgerName, Path: path, Before: string(before[mine.value.start:mine.value.end]),
				}); err != nil {
					return StatusLineResult{}, err
				}
			}
			return StatusLineResult{Path: path, Already: true}, nil
		}
		previous = string(before[mine.value.start:mine.value.end])
		// the value's span swapped for ours: the key, the members around it and every byte
		// of whitespace stay exactly as they were, and remove's swap back is its exact inverse
		next := jsonReplaceValue(before, *mine, m.value)
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	} else {
		next, err := jsonInsertMember(before, 0, fmt.Sprintf("%q:%s", m.key, m.value))
		if err != nil {
			return StatusLineResult{}, &MalformedError{Path: path, Err: err}
		}
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	}
	if err := recordLedger(hudHome, ledgerEntry{
		Harness: m.ledgerName, Path: path, Before: previous,
	}); err != nil {
		return StatusLineResult{}, err
	}
	return StatusLineResult{Path: path, Changed: true}, nil
}

// remove takes the member back out and restores the value the ledger recorded, byte for byte;
// a key there was none of is taken out, and a file install created and nothing else holds goes
// away entirely. The ledger record goes with it.
func (m settingsMember) remove(hudHome string, env Env) (StatusLineResult, error) {
	ledger := readLedger(ledgerPath(hudHome))
	record, hasRecord := ledger.named(m.ledgerName)
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
			if err := dropLedger(hudHome, m.ledgerName); err != nil {
				return StatusLineResult{}, err
			}
		}
		return StatusLineResult{Path: path, NotThere: true}, nil
	}
	if err := jsonCheck(before); err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	mine, err := jsonFind(before, 0, m.key)
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
				if err := dropLedger(hudHome, m.ledgerName); err != nil {
					return StatusLineResult{}, err
				}
				return StatusLineResult{Path: path, Removed: true, FileGone: true}, nil
			}
			if err := writeAtomic0600(path, next); err != nil {
				return StatusLineResult{}, err
			}
		}
	} else {
		// no record: ours is still recognisable, and anything else is the user's, never touched
		if !m.ours(before[mine.value.start:mine.value.end]) {
			return StatusLineResult{Path: path, NotThere: true}, nil
		}
		next := jsonCutMember(before, *mine)
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	}
	if err := dropLedger(hudHome, m.ledgerName); err != nil {
		return StatusLineResult{}, err
	}
	return StatusLineResult{Path: path, Removed: true}, nil
}
