// Package settings keeps the dashboard's own preferences in the state dir so every browser
// and the server agree: whether desktop notifications are on (default on), how many days of
// history to keep (0 = forever, default 30), and which alerts the user dismissed (by id,
// forgotten after a week). Reads never fail — a missing or broken file is the defaults.
package settings

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// keepDismissedMs is how long one dismissal holds: a week.
const keepDismissedMs = 7 * 86_400_000

// Settings is the dashboard's preferences.
type Settings struct {
	Notifications        bool `json:"notifications"`
	HistoryRetentionDays int  `json:"historyRetentionDays"`
}

// DefaultSettings is what no file, or a file that says nothing useful, leaves in place.
var DefaultSettings = Settings{Notifications: true, HistoryRetentionDays: 30}

// RetentionDays is the retention choices the dashboard offers, in days; 0 keeps everything.
var RetentionDays = []int{7, 30, 90, 0}

// retentionChoices is the offered list as one sentence fragment: "7, 30, 90, 0".
var retentionChoices = strings.Join(intsText(RetentionDays), ", ")

// Dismissal is one alert the user dismissed, and when.
type Dismissal struct {
	ID string
	At float64
}

// SettingsPath is the preferences file, in radar's state dir.
func SettingsPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(paths.StateDir(ctx, env), "settings.json")
}

// DismissedPath is the dismissed alerts file, in radar's state dir.
func DismissedPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(paths.StateDir(ctx, env), "dismissed.json")
}

// ReadSettings loads the preferences; anything missing or wrong keeps the default.
func ReadSettings(ctx context.Context, env domain.EnvLookup) Settings {
	record := readObject(SettingsPath(ctx, env))
	settings := DefaultSettings
	if on, ok := boolField(fieldOf(record, "notifications")); ok {
		settings.Notifications = on
	}
	if days, ok := numberField(fieldOf(record, "historyRetentionDays")); ok && isRetentionDays(days) {
		settings.HistoryRetentionDays = int(days)
	}
	return settings
}

// ValidateSettings merges a partial update from the dashboard into current, key by key in
// arrival order; an unknown key or a wrong type is refused with the reason.
func ValidateSettings(input *jsfmt.Value, current Settings) (Settings, string) {
	if input == nil || input.Kind() != jsfmt.KindObject {
		return Settings{}, "expected an object"
	}
	next := current
	keys, fields := input.Keys(), input.Fields()
	for i, key := range keys {
		var refusal string
		next, refusal = validatedKey(key, fields[i], next)
		if refusal != "" {
			return Settings{}, refusal
		}
	}
	return next, ""
}

// WriteSettings stores the preferences: version first, then the two keys, nothing else.
func WriteSettings(ctx context.Context, env domain.EnvLookup, settings Settings) error {
	return WriteJSONAtomic(SettingsPath(ctx, env), settingsFile{Version: 1, Settings: settings})
}

// settingsFile is settings.json's shape: the version first, then the two preferences.
type settingsFile struct {
	Version int `json:"version"`
	Settings
}

// ReadDismissed loads the alerts still dismissed at now, in file order.
func ReadDismissed(ctx context.Context, env domain.EnvLookup, now int64) []Dismissal {
	ids := fieldOf(readObject(DismissedPath(ctx, env)), "ids")
	out := []Dismissal{}
	for _, pair := range objectEntries(ids) {
		if at, ok := numberField(pair.value); ok && float64(now)-at < keepDismissedMs {
			out = append(out, Dismissal{ID: pair.key, At: at})
		}
	}
	return out
}

// WriteDismissed stores the dismissals, ids in dismissal order.
func WriteDismissed(ctx context.Context, env domain.EnvLookup, dismissed []Dismissal) error {
	if dismissed == nil {
		dismissed = []Dismissal{}
	}
	return WriteJSONAtomic(DismissedPath(ctx, env), dismissedFile{Version: 1, IDs: dismissedIDs(dismissed)})
}

// dismissedFile is dismissed.json's shape.
type dismissedFile struct {
	Version int          `json:"version"`
	IDs     dismissedIDs `json:"ids"`
}

// dismissedIDs is the ids object: pairs in dismissal order, so the file keeps the order the
// dismissals happened in.
type dismissedIDs []Dismissal

// MarshalJSON writes the ids as one JSON object, its numbers as JavaScript prints them.
func (d dismissedIDs) MarshalJSON() ([]byte, error) {
	if len(d) == 0 {
		return []byte("{}"), nil
	}
	var out bytes.Buffer
	out.WriteByte('{')
	for i, dismissal := range d {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(jsfmt.String(dismissal.ID).EncodeJSON())
		out.WriteByte(':')
		// JSON.stringify writes a non-finite number as null, never as a bare word
		if math.IsInf(dismissal.At, 0) || math.IsNaN(dismissal.At) {
			out.WriteString("null")
		} else {
			out.WriteString(jsscalar.JSNumber(dismissal.At))
		}
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// WriteJSONAtomic writes a file whole or not at all (a temp file renamed into place), so a
// reader never sees half a JSON document: JSON.stringify's layout — two-space indent, a
// trailing newline — 0700 directories, 0600 file. It lives at this layer because the budget
// and alert files share it and import this package.
func WriteJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := domain.MarshalIndentJSON(value)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// validatedKey applies one key of a partial update to current, or refuses it with the
// reason.
func validatedKey(key string, value *jsfmt.Value, current Settings) (Settings, string) {
	if key == "notifications" {
		on, ok := boolField(value)
		if !ok {
			return Settings{}, "notifications: true or false"
		}
		next := current
		next.Notifications = on
		return next, ""
	}
	if key == "historyRetentionDays" {
		days, ok := numberField(value)
		if !ok || !isRetentionDays(days) {
			return Settings{}, "historyRetentionDays: one of " + retentionChoices
		}
		next := current
		next.HistoryRetentionDays = int(days)
		return next, ""
	}
	// the length of the refused key's head counts UTF-16 units, the way the dashboard reads it
	return Settings{}, "unknown setting: " + jsscalar.UTF16Slice(key, 0, 40)
}

// isRetentionDays says whether a number is one of the offered retention choices; anything
// else (14, -1) is not.
func isRetentionDays(days float64) bool {
	for _, offered := range RetentionDays {
		if days == float64(offered) {
			return true
		}
	}
	return false
}

// intsText renders whole numbers for the refusal sentence.
func intsText(values []int) []string {
	texts := make([]string, len(values))
	for i, value := range values {
		texts[i] = strconv.Itoa(value)
	}
	return texts
}

// readObject reads a state file as a JSON object; a missing, broken or non-object file is
// nil, never an error.
func readObject(path string) *jsfmt.Value {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := jsfmt.DecodeValue(data)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return nil
	}
	return value
}

// fieldOf reads one field of an object, the last one when a key repeats: JSON.parse keeps
// the last.
func fieldOf(object *jsfmt.Value, key string) *jsfmt.Value {
	if object == nil {
		return nil
	}
	var found *jsfmt.Value
	keys, fields := object.Keys(), object.Fields()
	for i, name := range keys {
		if name == key {
			found = fields[i]
		}
	}
	return found
}

// pair is one property of a JSON value, as Object.entries yields it.
type pair struct {
	key   string
	value *jsfmt.Value
}

// objectEntries lists a value's own enumerable properties: an object's keys in arrival
// order, an array's indices as its keys, nothing for any other shape.
func objectEntries(value *jsfmt.Value) []pair {
	if value == nil {
		return nil
	}
	switch value.Kind() {
	case jsfmt.KindObject:
		keys, fields := value.Keys(), value.Fields()
		entries := make([]pair, len(keys))
		for i, key := range keys {
			entries[i] = pair{key: key, value: fields[i]}
		}
		return entries
	case jsfmt.KindArray:
		items := value.Items()
		entries := make([]pair, len(items))
		for i, item := range items {
			entries[i] = pair{key: strconv.Itoa(i), value: item}
		}
		return entries
	default:
		return nil
	}
}

// boolField reads a JSON boolean.
func boolField(value *jsfmt.Value) (bool, bool) {
	if value == nil || value.Kind() != jsfmt.KindBool {
		return false, false
	}
	return value.EncodeJSON() == "true", true
}

// numberField reads one JSON number. The literal is re-rendered the way JavaScript prints
// it first, so an out-of-range literal (1e999) reads as ±Inf, the way JSON.parse yields
// Infinity; ParseFloat cannot then fail.
func numberField(value *jsfmt.Value) (float64, bool) {
	if value == nil || value.Kind() != jsfmt.KindNumber {
		return 0, false
	}
	f, _ := strconv.ParseFloat(value.EncodeJSON(), 64)
	return f, true
}
