// huddle-router beside the hub: `huddle up` starts the router as its own process before the
// hub, through the same start the router words and the providers' session start take.
package cli

import (
	"context"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

// routerStartBudget bounds up's wait for a router it spawned: the router start's own default.
const routerStartBudget = 3 * time.Second

// startRouter makes sure huddle-router serves, and answers the start's line or its error. No
// provider set up is the quiet answer, and nothing is probed: a machine with no provider never
// touches the router's port.
func startRouter(ctx context.Context, w *World, budget time.Duration) string {
	env := worldEnv(w)
	names, err := router.ReadEnabled(env)
	if err != nil {
		return "huddle-router: " + err.Error()
	}
	if len(names) == 0 {
		return "huddle-router: no provider is set up"
	}
	line, err := routerd.Start(ctx, routerd.StartOptions{
		Env:        env,
		Executable: w.bin(),
		BudgetMs:   max(budget.Milliseconds(), 1),
	})
	if err != nil {
		return err.Error()
	}
	return line
}
