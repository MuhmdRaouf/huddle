// The spinner tips: the owner's insights shown under Claude Code's spinner. Install writes
// the spinnerTipsOverride member into Claude Code's settings.json — every other byte left
// where it was — and records the value it replaced in the huddle home's ledger, so remove
// gives the file back byte for byte (or takes a key install added back out, or a file install
// created away entirely).
package harness

import (
	"fmt"
	"os"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// spinnerTipsLedgerName is the ledger record's name: not a harness, but a file outside the home
// the same ledger covers.
const spinnerTipsLedgerName = "spinner-tips"

// InstallSpinnerTips writes the spinnerTipsOverride entry into Claude Code's settings.json and
// records the value it replaced in the ledger. A file that does not parse is refused before
// anything is written. Running it again with the entry already ours changes nothing.
func InstallSpinnerTips(hudHome string, env Env, tipsFile string) (StatusLineResult, error) {
	path := StatusLineSettingsPath(env)
	before, existed, err := readIfExists(path)
	if err != nil {
		return StatusLineResult{}, err
	}
	value := fmt.Sprintf("{%q:%s}", "tipsFile", jsonString(tipsFile))
	if !existed {
		out := fmt.Sprintf("{\n  %q:%s\n}\n", "spinnerTipsOverride", value)
		if err := writeAtomic0600(path, []byte(out)); err != nil {
			return StatusLineResult{}, err
		}
		if err := recordLedger(hudHome, ledgerEntry{
			Harness: spinnerTipsLedgerName, Path: path, Created: true, Before: "",
		}); err != nil {
			return StatusLineResult{}, err
		}
		return StatusLineResult{Path: path, Changed: true, Created: true}, nil
	}
	if err := jsonCheck(before); err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	previous := ""
	mine, err := jsonFind(before, 0, "spinnerTipsOverride")
	if err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	if mine != nil {
		if jsonEqual(before[mine.value.start:mine.value.end], []byte(value)) {
			// already ours: only a lost ledger record is filled in, never over the first
			// install's own previous value
			ledger := readLedger(ledgerPath(hudHome))
			if _, has := ledger.named(spinnerTipsLedgerName); !has {
				if err := recordLedger(hudHome, ledgerEntry{
					Harness: spinnerTipsLedgerName, Path: path, Before: string(before[mine.value.start:mine.value.end]),
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
		next, err := jsonInsertMember(before, 0, fmt.Sprintf("%q:%s", "spinnerTipsOverride", value))
		if err != nil {
			return StatusLineResult{}, &MalformedError{Path: path, Err: err}
		}
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	}
	if err := recordLedger(hudHome, ledgerEntry{
		Harness: spinnerTipsLedgerName, Path: path, Before: previous,
	}); err != nil {
		return StatusLineResult{}, err
	}
	return StatusLineResult{Path: path, Changed: true}, nil
}

// RemoveSpinnerTips takes the spinnerTipsOverride entry back out and restores the value the
// ledger recorded, byte for byte; a key there was none of is taken out, and a file install
// created and nothing else holds goes away entirely. The ledger record goes with it.
func RemoveSpinnerTips(hudHome string, env Env) (StatusLineResult, error) {
	ledger := readLedger(ledgerPath(hudHome))
	record, hasRecord := ledger.named(spinnerTipsLedgerName)
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
			if err := dropLedger(hudHome, spinnerTipsLedgerName); err != nil {
				return StatusLineResult{}, err
			}
		}
		return StatusLineResult{Path: path, NotThere: true}, nil
	}
	if err := jsonCheck(before); err != nil {
		return StatusLineResult{}, &MalformedError{Path: path, Err: err}
	}
	mine, err := jsonFind(before, 0, "spinnerTipsOverride")
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
				if err := dropLedger(hudHome, spinnerTipsLedgerName); err != nil {
					return StatusLineResult{}, err
				}
				return StatusLineResult{Path: path, Removed: true, FileGone: true}, nil
			}
			if err := writeAtomic0600(path, next); err != nil {
				return StatusLineResult{}, err
			}
		}
	} else {
		// no record: ours is still recognisable — an override whose tipsFile ends in the
		// spinner-tips file's name — and anything else is the user's, never touched
		if !holdsSpinnerTipsOurs(before[mine.value.start:mine.value.end]) {
			return StatusLineResult{Path: path, NotThere: true}, nil
		}
		next := jsonCutMember(before, *mine)
		if err := writeAtomic0600(path, next); err != nil {
			return StatusLineResult{}, err
		}
	}
	if err := dropLedger(hudHome, spinnerTipsLedgerName); err != nil {
		return StatusLineResult{}, err
	}
	return StatusLineResult{Path: path, Removed: true}, nil
}

// holdsSpinnerTipsOurs answers whether a raw member value is the shape of our entry: an
// override object whose tipsFile ends in the spinner-tips file's name.
func holdsSpinnerTipsOurs(value []byte) bool {
	decoded, err := jsfmt.DecodeValue(value)
	if err != nil {
		return false
	}
	tipsFile, ok := strFieldOf(decoded, "tipsFile")
	return ok && strings.HasSuffix(tipsFile, "spinner-tips.json")
}
