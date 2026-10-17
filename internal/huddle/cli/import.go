// The one-time import's place in an entry (the home package does the work): every entry of the
// binary — hook, command, CLI word, server start — brings this project's legacy homes into the
// user's home before it does anything else, inside its budget. Nothing to import answers at
// once and writes nothing; a failure is silence, because the next entry tries again.

package cli

import (
	"context"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// ImportLegacy imports the legacy homes this project names, within budget: a hook grants the
// import 2 s of its own budget, anything else 10 s; what the budget does not finish waits for
// the next entry.
func ImportLegacy(ctx context.Context, w *World, budget time.Duration) {
	h, err := home.Resolve(w.getenv)
	if err != nil {
		return
	}
	sources := home.Discover(ctx, firstNonEmpty(w.getenv("CLAUDE_PROJECT_DIR"), w.dir()))
	if len(sources) == 0 {
		return
	}
	importCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	_, _ = home.Import(importCtx, h, sources)
}
