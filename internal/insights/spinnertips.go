package insights

// The spinner tips: the file Claude Code reads through the spinnerTipsOverride entry. Only
// whole-huddle tips become entries, one line each at Claude Code's own limits, and the file
// lands at mode 0600 through a rename, so Claude Code never reads half a file.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// spinnerTipCooldown is the sessions Claude Code waits before showing one of these tips again:
// three keeps a tip fresh where one a turn would wear it out.
const spinnerTipCooldown = 3

// spinnerTipLimit is how many tips Claude Code reads across tips and tipsFile.
const spinnerTipLimit = 200

// spinnerTipTextLimit is the runes one tip's line may hold.
const spinnerTipTextLimit = 500

// SpinnerTipsFile names the spinner tips file in the owner's home: the path the
// spinnerTipsOverride entry points Claude Code at.
func SpinnerTipsFile(userHome string) string {
	return filepath.Join(userHome, ".agents", "huddle", "spinner-tips.json")
}

// SpinnerTip is one entry of the spinner tips file, named as Claude Code reads it.
type SpinnerTip struct {
	ID               string `json:"id"`
	Text             string `json:"text"`
	CooldownSessions int    `json:"cooldownSessions"`
	Priority         int    `json:"priority"`
}

// SpinnerTipsOf answers the spinner entries the owner's tips make: a tip about one session
// belongs to that session, not to every spinner, so only whole-huddle tips become entries, in
// the given order, at most the 200 Claude Code reads.
func SpinnerTipsOf(tips []Tip, settings Settings) []SpinnerTip {
	if !settings.TipsForMe {
		return []SpinnerTip{}
	}
	entries := make([]SpinnerTip, 0, len(tips))
	for _, tip := range tips {
		if len(entries) == spinnerTipLimit {
			break
		}
		if tip.Session != "" {
			continue
		}
		entries = append(entries, SpinnerTip{
			ID:               spinnerTipID(tip.ID),
			Text:             spinnerTipText(tip.Sentence),
			CooldownSessions: spinnerTipCooldown,
		})
	}
	return entries
}

// spinnerTipID is the entry id for one tip: huddle- plus the first 16 hex characters of the
// SHA-256 of the tip's id. The tip ids carry ":" and can run past Claude Code's 64-character
// id limit; the hash is always a valid id and stable for one tip.
func spinnerTipID(tipID string) string {
	sum := sha256.Sum256([]byte(tipID))
	return "huddle-" + hex.EncodeToString(sum[:])[:16]
}

// spinnerTipText is one tip's sentence as one line: every run of whitespace, newlines
// included, becomes one space, and a sentence past the 500 runes Claude Code reads is cut on a
// rune boundary with an ellipsis for the last rune.
func spinnerTipText(sentence string) string {
	line := strings.Join(strings.Fields(sentence), " ")
	runes := []rune(line)
	if len(runes) <= spinnerTipTextLimit {
		return line
	}
	return string(runes[:spinnerTipTextLimit-1]) + "…"
}

// WriteSpinnerTips writes the entries as {"tips":[…]} for Claude Code to read: the directory
// is created when missing, and the file lands at mode 0600 through a temp file in its own
// folder and a rename, so a reader never meets a half-written file.
func WriteSpinnerTips(path string, tips []SpinnerTip) error {
	if tips == nil {
		tips = []SpinnerTip{} // a nil list writes {"tips":[]}, never {"tips":null}
	}
	whole, err := json.Marshal(struct {
		Tips []SpinnerTip `json:"tips"`
	}{Tips: tips})
	if err != nil {
		return fmt.Errorf("marshal the spinner tips: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("make %s: %w", filepath.Dir(path), err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".spinner-tips-*.json")
	if err != nil {
		return fmt.Errorf("temp file for %s: %w", path, err)
	}
	name := temp.Name()
	defer os.Remove(name) // nothing left once the rename has moved it
	if _, err := temp.Write(whole); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("put %s in place: %w", path, err)
	}
	return nil
}
