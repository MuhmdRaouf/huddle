// Package lifecycle watches Claude Code's plugin registry: the server outlives the plugin
// that started it (there is no uninstall hook), so it detects its own uninstall or disable and
// cleans up after itself.
package lifecycle

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// Presence is one reading of the plugin registry.
type Presence string

const (
	PresencePresent     Presence = "present"
	PresenceDisabled    Presence = "disabled"
	PresenceUninstalled Presence = "uninstalled"
	PresenceUnknown     Presence = "unknown"
)

// RemovalKind is what the removal callback learns: the registry key is gone, or the plugin is
// switched off.
type RemovalKind string

const (
	RemovalNone        RemovalKind = ""
	RemovalUninstalled RemovalKind = "uninstalled"
	RemovalDisabled    RemovalKind = "disabled"
)

// DefaultRemovalIntervalMs is how often the registry is re-read; RADAR_REMOVAL_MS overrides it.
const DefaultRemovalIntervalMs = 10_000

// jsonState is what a read of a JSON file can come back as.
type jsonState int

const (
	jsonMissing jsonState = iota
	jsonUnreadable
	jsonOK
)

type jsonFile struct {
	state jsonState
	value any
}

// readJSON treats a missing file as a fact (nothing is there to read); every other failure, and
// a body that is not JSON, is unreadable.
func readJSON(path string) jsonFile {
	text, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return jsonFile{state: jsonMissing}
		}
		return jsonFile{state: jsonUnreadable}
	}
	var value any
	if json.Unmarshal(text, &value) != nil {
		return jsonFile{state: jsonUnreadable}
	}
	return jsonFile{state: jsonOK, value: value}
}

func isRecord(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// registryKeys lists the `<plugin>@<marketplace>` keys the registry carries, or false when the
// file is not the registry Claude Code writes (every entry a list — a shape change is a foreign
// file, not an uninstall).
func registryKeys(value any) ([]string, bool) {
	table, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	if plugins, isMap := table["plugins"].(map[string]any); isMap {
		table = plugins
	}
	keys := make([]string, 0, len(table))
	for key, entry := range table {
		if _, isList := entry.([]any); !isList {
			return nil, false
		}
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, true
}

// pluginNameOf turns `huddle@muhmdraouf` into `huddle`; a key with no @ (or one in front) is
// not a plugin@marketplace key.
func pluginNameOf(key string) (string, bool) {
	at := strings.LastIndexByte(key, '@')
	if at <= 0 {
		return "", false
	}
	return key[:at], true
}

// enabledPluginsOf reads the settings' enabledPlugins map, or false when the settings carry no
// such map.
func enabledPluginsOf(settings map[string]any) (map[string]any, bool) {
	enabled, ok := settings["enabledPlugins"].(map[string]any)
	return enabled, ok
}

// projectsEnable says whether any known project's settings (project or local scope) turn the
// plugin back on.
func projectsEnable(projectDirs, keys []string) bool {
	for _, dir := range projectDirs {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			file := readJSON(filepath.Join(dir, ".claude", name))
			if file.state != jsonOK || !isRecord(file.value) {
				continue
			}
			enabled, hasEnabled := enabledPluginsOf(file.value.(map[string]any))
			if !hasEnabled {
				continue
			}
			for _, key := range keys {
				if on, ok := enabled[key].(bool); ok && on {
					return true
				}
			}
		}
	}
	return false
}

// PresenceOptions is one check's inputs.
type PresenceOptions struct {
	Env domain.EnvLookup
	// Plugin is this plugin's own name — the part before @ in its registry key.
	Plugin string
	// ProjectDirs lists projects whose settings may re-enable a plugin the user scope
	// disabled, read live per check.
	ProjectDirs func() []string
}

// PluginPresence reads the registry once: present, disabled, uninstalled — or unknown, which
// never acts.
func PluginPresence(ctx context.Context, options PresenceOptions) Presence {
	if err := ctx.Err(); err != nil {
		return PresenceUnknown
	}
	config := paths.ClaudeConfigDir(options.Env)
	registry := readJSON(filepath.Join(config, "plugins", "installed_plugins.json"))
	if registry.state != jsonOK {
		return PresenceUnknown
	}
	allKeys, isRegistry := registryKeys(registry.value)
	if !isRegistry {
		return PresenceUnknown // a shape change is a foreign file, not an uninstall
	}
	// Any marketplace counts: a fork installed from its own marketplace must not look uninstalled.
	var keys []string
	for _, key := range allKeys {
		if name, ok := pluginNameOf(key); ok && name == options.Plugin {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return PresenceUninstalled
	}
	user := readJSON(filepath.Join(config, "settings.json"))
	if user.state == jsonMissing {
		return PresencePresent // no settings at all: nothing disables anything
	}
	if user.state != jsonOK || !isRecord(user.value) {
		return PresenceUnknown
	}
	enabled, hasEnabled := enabledPluginsOf(user.value.(map[string]any))
	if !hasEnabled {
		return PresencePresent
	}
	for _, key := range keys {
		value, listed := enabled[key]
		if !listed {
			return PresencePresent // the key is not named there: not switched off
		}
		if on, isBool := value.(bool); !isBool || on {
			return PresencePresent
		}
	}
	// Every key is switched off in user scope; a known project may still turn it back on.
	projectDirs := options.ProjectDirs
	if projectDirs == nil {
		projectDirs = func() []string { return nil }
	}
	if projectsEnable(projectDirs(), keys) {
		return PresencePresent
	}
	return PresenceDisabled
}

// WatchOptions configures the removal watch.
type WatchOptions struct {
	Env         domain.EnvLookup
	Plugin      string
	ProjectDirs func() []string
	// IntervalMs is the check interval; nil is DefaultRemovalIntervalMs.
	IntervalMs *int
	// OnRemove runs once, after two consecutive checks agreed on the same verdict.
	OnRemove func(ctx context.Context, kind RemovalKind)
}

// RemovalWatch polls the registry on an interval; Tick is the same check, on demand.
type RemovalWatch struct {
	options  WatchOptions
	mu       sync.Mutex
	verdict  RemovalKind
	fired    bool
	done     chan struct{}
	stopOnce sync.Once
}

// WatchRemoval polls the registry until two consecutive checks agree the plugin is gone or
// off, then calls OnRemove once and stops the timer.
func WatchRemoval(ctx context.Context, options WatchOptions) *RemovalWatch {
	watch := &RemovalWatch{options: options, done: make(chan struct{})}
	go watch.loop(ctx)
	return watch
}

// maxIntervalMs is the largest interval a time.Duration carries: a bigger request only ever
// meant "far in the future", never a precise time.
const maxIntervalMs = int(math.MaxInt64 / int64(time.Millisecond))

// loop re-checks the registry on the interval until Stop (or the verdict firing) ends it.
func (w *RemovalWatch) loop(ctx context.Context) {
	ms := DefaultRemovalIntervalMs
	if w.options.IntervalMs != nil {
		ms = *w.options.IntervalMs
	}
	if ms <= 0 {
		return // no timer to run; Tick still checks on demand
	}
	if ms > maxIntervalMs {
		ms = maxIntervalMs
	}
	ticker := time.NewTicker(time.Duration(ms) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.Tick(ctx)
		}
	}
}

// Tick runs one check: a verdict other than uninstalled or disabled resets the count, one
// sighting sets it, and a second agreeing sighting fires the callback once and stops the
// timer.
func (w *RemovalWatch) Tick(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	found := PluginPresence(ctx, PresenceOptions{
		Env:         w.options.Env,
		Plugin:      w.options.Plugin,
		ProjectDirs: w.options.ProjectDirs,
	})
	if found != PresenceUninstalled && found != PresenceDisabled {
		w.verdict = RemovalNone // present, or files that cannot tell: back to square one
		return
	}
	kind := RemovalKind(found)
	if w.fired || w.verdict != kind {
		w.verdict = kind // one sighting is not enough; the next check must agree
		return
	}
	w.fired = true
	w.stopTimer()
	if w.options.OnRemove != nil {
		w.options.OnRemove(ctx, kind)
	}
}

// Stop ends the interval; Tick still checks on demand. Safe to call more than once.
func (w *RemovalWatch) Stop() {
	w.stopTimer()
}

func (w *RemovalWatch) stopTimer() {
	w.stopOnce.Do(func() { close(w.done) })
}

// RemovalIntervalMs picks the check interval: an explicit override wins, then
// RADAR_REMOVAL_MS, then the default.
func RemovalIntervalMs(env domain.EnvLookup, override *int) int {
	if override != nil {
		return *override
	}
	raw, ok := env["RADAR_REMOVAL_MS"]
	if !ok || raw == "" {
		return DefaultRemovalIntervalMs
	}
	parsed, ok := parseInt10(raw)
	if !ok || parsed <= 0 {
		return DefaultRemovalIntervalMs
	}
	return parsed
}

// parseInt10 parses the way JavaScript's Number.parseInt(text, 10) does: leading whitespace and
// a sign, then digits, stopping at the first non-digit (so "250ms" is 250); no digits at all
// is not a number. Digits running past any interval a duration can time keep only their size:
// the extremes clamp, and a number even a float cannot hold (Infinity) is no number.
func parseInt10(text string) (int, bool) {
	s := strings.TrimLeftFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r == 0xFEFF
	})
	sign := ""
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		sign, s = s[:1], s[1:]
	}
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(sign+s[:digits], 64)
	if err != nil {
		return 0, false
	}
	if value >= float64(maxIntervalMs) {
		return maxIntervalMs, true
	}
	if value <= -float64(maxIntervalMs) {
		return -maxIntervalMs, true
	}
	return int(value), true
}

// RemoveStateTree takes the whole state dir — spool, logs, server.json — the "uninstalled"
// case only, and only when the dir carries radar's marker: RADAR_HOME may name any directory,
// and an unmarked one is never deleted.
func RemoveStateTree(ctx context.Context, env domain.EnvLookup) bool {
	if _, err := os.Stat(paths.StateMarkerPath(ctx, env)); err != nil {
		return false
	}
	return os.RemoveAll(paths.StateDir(ctx, env)) == nil
}
