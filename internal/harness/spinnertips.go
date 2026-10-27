// The spinner tips: the owner's insights shown under Claude Code's spinner. Install writes
// the spinnerTipsOverride member into Claude Code's settings.json — every other byte left
// where it was — and records the value it replaced in the huddle home's ledger, so remove
// gives the file back byte for byte (or takes a key install added back out, or a file install
// created away entirely).
package harness

import (
	"fmt"
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
	member := settingsMember{
		ledgerName: spinnerTipsLedgerName,
		key:        "spinnerTipsOverride",
		value:      fmt.Sprintf("{%q:%s}", "tipsFile", jsonString(tipsFile)),
		ours:       holdsSpinnerTipsOurs,
	}
	return member.install(hudHome, env)
}

// RemoveSpinnerTips takes the spinnerTipsOverride entry back out and restores the value the
// ledger recorded, byte for byte; a key there was none of is taken out, and a file install
// created and nothing else holds goes away entirely. The ledger record goes with it.
func RemoveSpinnerTips(hudHome string, env Env) (StatusLineResult, error) {
	member := settingsMember{
		ledgerName: spinnerTipsLedgerName,
		key:        "spinnerTipsOverride",
		ours:       holdsSpinnerTipsOurs,
	}
	return member.remove(hudHome, env)
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
