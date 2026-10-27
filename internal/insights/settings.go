package insights

// The owner's Insights switches: whether the hub shows its tips, and whether a session may get
// one tip line. One file, insights.json in the huddle home, mode 0600. The hub reads it on every
// insights request, so no cache stands between a flip and the next answer.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Settings are the owner's Insights switches.
type Settings struct {
	TipsForMe      bool `json:"tips_for_me"`
	TipsToSessions bool `json:"tips_to_sessions"`
}

// DefaultSettings answers tips for me on and tips to sessions off: the hub's own card costs the
// owner nothing, a tip line in a session's context spends the session's tokens.
func DefaultSettings() Settings {
	return Settings{TipsForMe: true, TipsToSessions: false}
}

// SettingsFile names the switches' file in the huddle home.
func SettingsFile(home string) string {
	return filepath.Join(home, "insights.json")
}

// ReadSettings answers the switches the file holds. A missing, unreadable or non-object file
// answers the defaults; a field the file leaves out keeps its default.
func ReadSettings(path string) Settings {
	raw, err := os.ReadFile(path)
	if err != nil {
		return DefaultSettings()
	}
	var file struct {
		TipsForMe      *bool `json:"tips_for_me"`
		TipsToSessions *bool `json:"tips_to_sessions"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return DefaultSettings()
	}
	got := DefaultSettings()
	if file.TipsForMe != nil {
		got.TipsForMe = *file.TipsForMe
	}
	if file.TipsToSessions != nil {
		got.TipsToSessions = *file.TipsToSessions
	}
	return got
}

// WriteSettings records the switches: it rewrites only tips_for_me and tips_to_sessions and keeps
// every other top-level key the file holds.
func WriteSettings(path string, s Settings) error {
	set := map[string]json.RawMessage{}
	for _, field := range []struct {
		name  string
		value bool
	}{
		{"tips_for_me", s.TipsForMe},
		{"tips_to_sessions", s.TipsToSessions},
	} {
		raw, err := json.Marshal(field.value)
		if err != nil {
			return fmt.Errorf("marshal %s: %w", field.name, err)
		}
		set[field.name] = raw
	}
	return writeFields(path, set)
}

// writeFields sets the given top-level keys of the JSON object at path and keeps every other key
// the file holds. The file lands at mode 0600 through a temp file in its own folder and a
// rename, so a reader never meets a half-written file.
func writeFields(path string, set map[string]json.RawMessage) error {
	fields := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &fields) != nil {
		fields = map[string]json.RawMessage{} // a file that says nothing readable starts fresh
	}
	for name, raw := range set {
		fields[name] = raw
	}
	whole, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("marshal the fields: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".insights-*.json")
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
