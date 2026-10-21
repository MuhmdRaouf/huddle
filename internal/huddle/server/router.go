// The full edition: the hub hosts huddle-router in its own process — one router port for every
// enabled provider beside the hub — unless another process already holds the router lock, in
// which case one line says so and the hub serves alone.
package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

// hostHuddleRouter runs the router daemon beside the hub, on a context the server cancels at
// shutdown. The hub never waits on the router, and the router never waits on the hub: a held
// router lock and any other error are one stderr line each. A standalone `huddle router serve`
// holding the lock is taken over instead — the hub asks its Run for the listening socket over
// the handover socket, so the user ends with one process and the port never closes.
func hostHuddleRouter(ctx context.Context, o Options) {
	defer func() {
		if reason := recover(); reason != nil {
			fmt.Fprintf(o.Stderr, "huddle-router: recovered: %v\n", reason)
		}
	}()
	env := routerEnv(o)
	err := routerd.Run(ctx, routerd.Options{Env: env, Version: o.Version, Hosted: true})
	var held *singleton.HeldError
	if errors.As(err, &held) && standaloneRouter(held.PID) {
		// The takeover's Run serves until the hub stops, so the line names the handover as it
		// starts, not at the end of the hub's life. A takeover that finds nobody to take from
		// falls back to the normal start and its answer follows below.
		fmt.Fprintf(o.Stderr, "huddle-router: taken over from pid %d\n", held.PID)
		err = routerd.Run(ctx, routerd.Options{Env: env, Version: o.Version, Takeover: true, Hosted: true})
		if err == nil {
			return
		}
	}
	switch {
	case err == nil:
		return
	case errors.As(err, &held):
		fmt.Fprintf(o.Stderr, "huddle-router: served by pid %d\n", held.PID)
	default:
		fmt.Fprintf(o.Stderr, "huddle-router: %v\n", err)
	}
}

// standaloneRouter says whether the pid runs `huddle router serve`: a standalone daemon, not
// another hub, so the full edition takes its socket over instead of serving alone.
func standaloneRouter(pid int) bool {
	return strings.HasSuffix(routerd.CommandLine(pid), "router serve")
}

// routerEnv is the environment the hub's router goroutine reads: the process environment the
// server start carried, split into a lookup.
func routerEnv(o Options) domain.EnvLookup {
	env := make(domain.EnvLookup, len(o.Environ))
	for _, entry := range o.Environ {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}
