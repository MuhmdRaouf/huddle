package outside

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
)

// Presence is what the plugin registry says about the huddle plugin. Unknown never acts: a
// missing or half-written file is a fact about the file, not about the plugin.
type Presence string

const (
	PresencePresent     Presence = "present"
	PresenceDisabled    Presence = "disabled"
	PresenceUninstalled Presence = "uninstalled"
	PresenceUnknown     Presence = "unknown"
)

// Gone is the verdict that stops the server.
type Gone string

const (
	GoneUninstalled Gone = "uninstalled"
	GoneDisabled    Gone = "disabled"
)

// jsonRead is one file read: missing (nothing there; a fact), unreadable (any other failure,
// including a body that is not JSON; not a fact), or ok with its value.
type jsonRead struct {
	missing    bool
	unreadable bool
	value      any
}

func readJSONFile(path string) jsonRead {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsonRead{missing: true}
		}
		return jsonRead{unreadable: true}
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return jsonRead{unreadable: true}
	}
	return jsonRead{value: value}
}

// jsonObject is a JSON object, or not (an array or null is not one).
func jsonObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

// enabledPlugins is the settings' enabledPlugins table, nil when there is none.
func enabledPlugins(value any) map[string]any {
	settings, ok := jsonObject(value)
	if !ok {
		return nil
	}
	table, ok := jsonObject(settings["enabledPlugins"])
	if !ok {
		return nil
	}
	return table
}

// PluginPresence reads the plugin registry under the Claude config dir. The plugin is
// uninstalled when no key names it (any marketplace: a fork must not look uninstalled),
// disabled when the user scope says false and no known project's settings say true, present
// otherwise, and unknown while a file is missing or half-written.
func PluginPresence(_ context.Context, env map[string]string, plugin string, projects []string) Presence {
	config := ClaudeConfigDir(env)
	registry := readJSONFile(filepath.Join(config, "plugins", "installed_plugins.json"))
	top, ok := jsonObject(registry.value)
	if registry.missing || registry.unreadable || !ok {
		return PresenceUnknown
	}
	// the table sits under "plugins", or is the whole file
	table, ok := jsonObject(top["plugins"])
	if !ok {
		table = top
	}
	for _, entry := range table {
		if _, isList := entry.([]any); !isList {
			return PresenceUnknown // not the shape Claude Code writes
		}
	}
	var keys []string
	for key := range table {
		at := strings.LastIndex(key, "@")
		if at > 0 && key[:at] == plugin {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return PresenceUninstalled
	}
	user := readJSONFile(filepath.Join(config, "settings.json"))
	if user.missing {
		return PresencePresent // no settings at all: nothing disables anything
	}
	userSettings, ok := jsonObject(user.value)
	if user.unreadable || !ok {
		return PresenceUnknown
	}
	userEnabled := enabledPlugins(userSettings)
	if userEnabled == nil {
		return PresencePresent
	}
	for _, key := range keys {
		enabled, present := userEnabled[key]
		if !present || enabled != false {
			return PresencePresent
		}
	}
	for _, project := range projects {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			settings := readJSONFile(filepath.Join(project, ".claude", name))
			projectEnabled := enabledPlugins(settings.value)
			if projectEnabled == nil {
				continue
			}
			for _, key := range keys {
				if projectEnabled[key] == true {
					return PresencePresent
				}
			}
		}
	}
	return PresenceDisabled
}

// ClaudeConfigDir is the Claude config dir: $CLAUDE_CONFIG_DIR passes through exactly as given,
// else ~/.claude.
func ClaudeConfigDir(env map[string]string) string {
	if dir, present := env["CLAUDE_CONFIG_DIR"]; present {
		return dir
	}
	home := ""
	if value, present := env["HOME"]; present {
		home = value
	} else if fallback, err := os.UserHomeDir(); err == nil {
		home = fallback
	}
	return path.Join(home, ".claude")
}

// RemovalOptions is what the removal watch needs: where the registry is, which plugin to watch,
// how often to check, and what to do once it is gone.
type RemovalOptions struct {
	Env      map[string]string
	Plugin   string
	Projects func() []string
	// Interval is how often the background check runs; 10 s when zero.
	Interval time.Duration
	Gone     func(kind Gone)
}

// Watch checks the plugin registry until two consecutive checks agree it is gone. Present and
// unknown reset the count: a torn write is never an uninstall.
type Watch struct {
	options  RemovalOptions
	mu       sync.Mutex
	seen     Presence
	fired    bool
	stop     chan struct{}
	stopOnce sync.Once
}

// WatchRemoval starts the watch. gone fires once, after two consecutive checks agreed on the
// same verdict, and the watch stops itself there; gone decides what "gone" means, this only
// decides that it is time.
func WatchRemoval(options RemovalOptions) *Watch {
	interval := options.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	w := &Watch{options: options, stop: make(chan struct{})}
	safego.Go(nil, func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stop:
				return
			case <-ticker.C:
				w.Tick(context.Background())
			}
		}
	})
	return w
}

// Tick is one check on demand, the same one the interval runs.
func (w *Watch) Tick(ctx context.Context) {
	var projects []string
	if w.options.Projects != nil {
		projects = w.options.Projects()
	}
	w.mu.Lock()
	p := PluginPresence(ctx, w.options.Env, w.options.Plugin, projects)
	if p != PresenceUninstalled && p != PresenceDisabled {
		w.seen = ""
		w.mu.Unlock()
		return
	}
	if w.fired || w.seen != p { // one sighting is not enough; the next check must agree
		w.seen = p
		w.mu.Unlock()
		return
	}
	w.fired = true
	w.mu.Unlock()
	w.Stop()
	if w.options.Gone != nil {
		w.options.Gone(Gone(p))
	}
}

// Stop ends the interval; a fired watch is already stopped.
func (w *Watch) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
}

// RemoveRunFiles deletes the run files that leave with the server: its run directory (pid, port,
// log, the owner key), the hooks' log and their state (what each session already saw, the asks a
// stop was blocked for); nothing a person wrote.
func RemoveRunFiles(home string) {
	_ = os.RemoveAll(filepath.Join(home, "run"))
	for _, name := range []string{"hooks.log", "stop-raised.json"} {
		_ = os.Remove(filepath.Join(home, name))
	}
	_ = os.RemoveAll(filepath.Join(home, "seen"))
}

// LeftBehindPath is where the note for the owner sits.
func LeftBehindPath(home string) string {
	return filepath.Join(home, "LEFT-BEHIND.md")
}

// WriteLeftBehind leaves the note the owner needs after the plugin is gone: the channels are
// their conversations, so they stay, with the note saying where they are and how to delete them.
func WriteLeftBehind(home, dataDir string, at time.Time) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	channels := filepath.Join(dataDir, "channels")
	note := fmt.Sprintf(`Huddle's server stopped itself: the huddle plugin was removed from Claude Code (or
disabled) at %[1]s.

The channels are still here — each file is one channel's whole conversation:
  %[2]s

To delete them:
  rm -rf "%[2]s"

Everything else in %[3]s (the config, certificates, what each session
has read) is not the server's to delete. This note can go:
  rm "%[4]s"
`, jsTimestamp(at), channels, home, LeftBehindPath(home))
	return os.WriteFile(LeftBehindPath(home), []byte(note), 0o600)
}
