package paths

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// LegacyStateDir is where state lived before ~/.agents: $XDG_STATE_HOME/<name>, else
// ~/.local/state/<name>.
func LegacyStateDir(env domain.EnvLookup, name string) string {
	return adapters.LegacyStateDir(env, name)
}

// AgentsStateDir is where it lives now: ~/.agents/<name>.
func AgentsStateDir(env domain.EnvLookup, name string) string {
	return adapters.AgentsStateDir(env, name)
}

// MigrateStateDir moves name's state from LegacyStateDir to AgentsStateDir when the old dir
// exists: the new dir is created mode 0700 and every top-level entry it lacks is renamed into it —
// never merged, never overwritten, symlinks moved as the links they are. A concurrent migrator's
// ENOENT/EEXIST is ignored; any other failure leaves everything in place for the next start to
// retry, at the cost of one line on the log. The old dir is removed only once nothing is left in
// it; otherwise the log names what stayed behind. Never fails.
func MigrateStateDir(ctx context.Context, name string, env domain.EnvLookup, log func(line string)) {
	adapters.MigrateStateDir(ctx, name, env, log)
}
