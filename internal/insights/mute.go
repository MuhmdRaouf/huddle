package insights

// Muting: a "not useful" on a rule keeps its tips quiet for 30 days, and "Forget what you
// learned" deletes insights.json, the only learning Insights keeps. The muted rules live in the
// file's muted key: [{"rule":"<rule>","until":<unix ms>}, ...].

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// muteFor is how long one "not useful" keeps a rule's tips quiet.
const muteFor = 30 * 24 * time.Hour

// muteEntry is one muted rule in the file's muted key.
type muteEntry struct {
	Rule  string `json:"rule"`
	Until int64  `json:"until"` // unix milliseconds
}

// Mute mutes rule until now plus 30 days: it rewrites the file's muted key, the new entry
// taking the rule's old entry's place, the entries that have run out at now dropped and the
// rest kept in order. The switches and every other key stay.
func Mute(path, rule string, now time.Time) error {
	muted := readMuted(path)
	fresh := muteEntry{Rule: rule, Until: now.Add(muteFor).UnixMilli()}
	kept := make([]muteEntry, 0, len(muted)+1)
	placed := false
	for _, entry := range muted {
		if entry.Rule == rule {
			kept = append(kept, fresh)
			placed = true
			continue
		}
		if entry.Until > now.UnixMilli() {
			kept = append(kept, entry)
		}
	}
	if !placed {
		kept = append(kept, fresh)
	}
	list, err := json.Marshal(kept)
	if err != nil {
		return fmt.Errorf("marshal the muted rules: %w", err)
	}
	return writeFields(path, map[string]json.RawMessage{"muted": list})
}

// MutedRules answers the rules whose mute has not run out at now. A missing or unreadable file,
// or a muted key that does not decode, answers an empty map.
func MutedRules(path string, now time.Time) map[string]bool {
	muted := map[string]bool{}
	for _, entry := range readMuted(path) {
		if entry.Until > now.UnixMilli() {
			muted[entry.Rule] = true
		}
	}
	return muted
}

// WithoutMuted answers the tips whose rule is not muted, in order; never nil.
func WithoutMuted(tips []Tip, muted map[string]bool) []Tip {
	kept := make([]Tip, 0, len(tips))
	for _, tip := range tips {
		if !muted[tip.Rule] {
			kept = append(kept, tip)
		}
	}
	return kept
}

// Forget deletes the file: the switches answer their defaults again and no learning stays. A
// missing file is not an error.
func Forget(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("forget %s: %w", path, err)
	}
	return nil
}

// readMuted answers the muted entries the file holds. A missing or unreadable file, or a muted
// key that does not decode, answers nil.
func readMuted(path string) []muteEntry {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file struct {
		Muted []muteEntry `json:"muted"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil
	}
	return file.Muted
}
