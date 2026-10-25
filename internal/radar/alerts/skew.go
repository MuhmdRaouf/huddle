package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// PluginSkew is the huddle commit Claude Code's plugin cache holds beside the commit the
// launcher's binary was built from, as the newest session start found them apart, and when
// that skew was first seen (unix milliseconds).
type PluginSkew struct {
	Cache  string `json:"cache"`
	Binary string `json:"binary"`
	Since  int64  `json:"since"`
}

// pluginSkewPath is the skew file in radar's state dir: present while the two commits differ.
func pluginSkewPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(paths.StateDir(ctx, env), "plugin-skew.json")
}

// RecordPluginCommits keeps what one session start found: two known commits that differ leave
// the skew file (its Since kept while the pair stays the same); equal commits, or either one
// unknown, remove it.
func RecordPluginCommits(ctx context.Context, env domain.EnvLookup, cache, binary string, nowMs int64) error {
	path := pluginSkewPath(ctx, env)
	if cache == "" || binary == "" || cache == binary {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if held := ReadPluginSkew(ctx, env); held != nil && held.Cache == cache && held.Binary == binary {
		return nil
	}
	body, err := json.Marshal(PluginSkew{Cache: cache, Binary: binary, Since: nowMs})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

// ReadPluginSkew is the skew the newest session start found, nil when there is none or the file
// cannot be read.
func ReadPluginSkew(ctx context.Context, env domain.EnvLookup) *PluginSkew {
	raw, err := os.ReadFile(pluginSkewPath(ctx, env))
	if err != nil {
		return nil
	}
	var skew PluginSkew
	if err := json.Unmarshal(raw, &skew); err != nil || skew.Cache == "" || skew.Binary == "" {
		return nil
	}
	return &skew
}

// pluginBehindAlerts raises one plugin_behind alert while a skew is held, its id keyed by the
// two commits so a dismissal holds until the pair changes.
func pluginBehindAlerts(in AlertInput) []shared.Alert {
	skew := in.PluginSkew
	if skew == nil {
		return nil
	}
	return []shared.Alert{{
		ID:    "plugin_behind:" + skew.Cache + ":" + skew.Binary,
		Kind:  shared.AlertPluginBehind,
		Since: skew.Since,
		Detail: "Claude Code's plugin cache holds huddle " + skew.Cache + ", the binary is " + skew.Binary +
			": run `claude plugin update huddle@muhmdraouf`, then start a new session",
		Severity: shared.SeverityWarn,
	}}
}
