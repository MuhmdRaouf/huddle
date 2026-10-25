// The deploy check `huddle status` prints: the commit Claude Code's plugin cache holds for huddle
// beside the commit the launcher's current binary was built from. Claude Code runs hooks, commands
// and skills from its cache until `claude plugin update`, while the launcher builds the
// marketplace checkout's commit, so the two drift apart after every push. Each session start
// hands the same two commits to radar, behind its plugin_behind alert.
package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/MuhmdRaouf/huddle/internal/radar/alerts"
)

// huddlePluginKey names huddle's entry in Claude Code's installed_plugins.json.
const huddlePluginKey = "huddle@muhmdraouf"

// installedPlugins is the part of Claude Code's installed_plugins.json the check reads.
type installedPlugins struct {
	Plugins map[string][]installedPlugin `json:"plugins"`
}

// installedPlugin is one install of a plugin in the cache.
type installedPlugin struct {
	Scope        string `json:"scope"`
	Version      string `json:"version"`
	GitCommitSha string `json:"gitCommitSha"`
}

// sourceBuild matches the launcher's current link at a source build: src/<12-hex commit>/huddle.
var sourceBuild = regexp.MustCompile(`(?:^|/)src/([0-9a-f]{12})/huddle$`)

// PluginCacheLine answers the status line about the plugin cache, such as
// "plugin cache 052c8e49c9ff · binary 38e9e40e321e (cache behind)", or "" when Claude Code has no
// huddle install to report on.
func PluginCacheLine(ctx context.Context, w *World) string {
	cache := cacheCommit(w)
	if cache == "" {
		return ""
	}
	binary := binaryCommit(w)
	switch {
	case binary == "":
		return "plugin cache " + cache + " · binary unknown"
	case binary == cache:
		return "plugin cache " + cache + " · binary " + binary + " (same)"
	default:
		return "plugin cache " + cache + " · binary " + binary + " (cache behind)"
	}
}

// RecordPluginSkew hands radar the two commits this session start finds, so its plugin_behind
// alert follows them. It only reads the plugin cache.
func RecordPluginSkew(ctx context.Context, w *World) error {
	return alerts.RecordPluginCommits(ctx, worldEnv(w), cacheCommit(w), binaryCommit(w), w.now().UnixMilli())
}

// cacheCommit reads the 12-character commit of huddle's user-scope install (or its first one)
// from $CLAUDE_CONFIG_DIR/plugins/installed_plugins.json, "" when there is none.
func cacheCommit(w *World) string {
	config := w.getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		home := w.getenv("HOME")
		if home == "" {
			return ""
		}
		config = filepath.Join(home, ".claude")
	}
	raw, err := os.ReadFile(filepath.Join(config, "plugins", "installed_plugins.json"))
	if err != nil {
		return ""
	}
	var installed installedPlugins
	if err := json.Unmarshal(raw, &installed); err != nil {
		return ""
	}
	installs := installed.Plugins[huddlePluginKey]
	if len(installs) == 0 {
		return ""
	}
	chosen := installs[0]
	for _, install := range installs {
		if install.Scope == "user" {
			chosen = install
			break
		}
	}
	if len(chosen.GitCommitSha) >= 12 {
		return chosen.GitCommitSha[:12]
	}
	return chosen.Version
}

// binaryCommit reads the commit the launcher's current link names, under
// ${AGENTS_HUDDLE_HOME:-$HOME/.agents/huddle-bin}; "" when the link is missing or names a
// release install rather than a source build.
func binaryCommit(w *World) string {
	launcher := w.getenv("AGENTS_HUDDLE_HOME")
	if launcher == "" {
		home := w.getenv("HOME")
		if home == "" {
			return ""
		}
		launcher = filepath.Join(home, ".agents", "huddle-bin")
	}
	target, err := os.Readlink(filepath.Join(launcher, "current"))
	if err != nil {
		return ""
	}
	match := sourceBuild.FindStringSubmatch(target)
	if match == nil {
		return ""
	}
	return match[1]
}
