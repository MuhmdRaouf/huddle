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
// router lock and any other error are one stderr line each.
func hostHuddleRouter(ctx context.Context, o Options) {
	defer func() {
		if reason := recover(); reason != nil {
			fmt.Fprintf(o.Stderr, "huddle-router: recovered: %v\n", reason)
		}
	}()
	err := routerd.Run(ctx, routerd.Options{Env: routerEnv(o), Version: o.Version})
	var held *singleton.HeldError
	switch {
	case err == nil:
		return
	case errors.As(err, &held):
		fmt.Fprintf(o.Stderr, "huddle-router: served by pid %d\n", held.PID)
	default:
		fmt.Fprintf(o.Stderr, "huddle-router: %v\n", err)
	}
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
