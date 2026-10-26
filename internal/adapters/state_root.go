package adapters

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// UninstallMarkerName is the one file the plugin keeps in its Claude Code data dir on purpose:
// Claude Code deletes the dir's contents on uninstall, and the router's uninstall watch reads the
// marker by the path the ledger recorded.
const UninstallMarkerName = "installed"

// StateRootRule says where one provider keeps its state: an explicit env var, else ~/.agents.
type StateRootRule struct {
	// The variable that names the root outright, e.g. ZAI_STATE_DIR.
	EnvVar string
	// The dir name under ~/.agents.
	XdgName string
}

// ResolveStateRoot is a provider's state root: $<EnvVar>, else ~/.agents/<XdgName> — one root
// for every caller (hooks, commands, the router). The root never depends on $CLAUDE_PLUGIN_DATA:
// Claude Code sets it only for hooks, and hooks, setup and the router must find the same files.
// Claude Code leaks one plugin's CLAUDE_PLUGIN_DATA into every Bash tool call, so a foreign one
// is never read: the plugin would walk another plugin's data dir.
func ResolveStateRoot(ctx context.Context, env domain.EnvLookup, rule StateRootRule) string {
	if explicit, ok := env[rule.EnvVar]; ok && explicit != "" {
		return explicit
	}
	return AgentsStateDir(env, rule.XdgName)
}

// NewStateRootRule derives every provider's rule from its own names: $<envPrefix>_STATE_DIR, `<name>`.
func NewStateRootRule(provider domain.Provider) StateRootRule {
	return StateRootRule{
		EnvVar:  provider.EnvPrefix + "_STATE_DIR",
		XdgName: provider.Name,
	}
}

// StateRoot is a provider's state root (see ResolveStateRoot).
func StateRoot(ctx context.Context, provider domain.Provider, env domain.EnvLookup) string {
	return ResolveStateRoot(ctx, env, NewStateRootRule(provider))
}
