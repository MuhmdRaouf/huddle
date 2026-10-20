package hook

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hubaddr"
)

// HubLookup answers the base url of the healthy huddle hub this machine runs,
// or false when none does: the same lookup kanban's flow client takes, and the
// radar word's own status and url take it too. The hub already serves radar in
// process, so a SessionStart that finds one starts no standalone dashboard —
// two servers would hold radar's history.db at once. The probe is
// hubaddr.ProbeTimeout, bounded inside the radar part's session-start budget,
// and runs for SessionStart only.
func HubLookup(environ []string) func(ctx context.Context) (string, bool) {
	env := envOf(environ)
	return func(ctx context.Context) (string, bool) {
		base := hubaddr.Base(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
		if base == "" {
			return "", false
		}
		if !hubaddr.Healthy(ctx, base) {
			return "", false
		}
		return base, true
	}
}
