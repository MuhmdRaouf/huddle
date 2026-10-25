package server

import (
	"context"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

// routerHealth is the part of the hub's health answer that names huddle-router, the process
// beside the hub: whether it serves, its pid and its port.
type routerHealth struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
	Port    int  `json:"port,omitempty"`
}

// routerOf reads the router the process environment's router home names. A server that names no
// environment (a test with bare Options) reports none: it cannot be told where the router's
// files live, and must never read the owner's.
func routerOf(ctx context.Context, environ []string) routerHealth {
	if len(environ) == 0 {
		return routerHealth{}
	}
	env := make(domain.EnvLookup, len(environ))
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	running, ok := routerd.RunningOf(ctx, env)
	if !ok {
		return routerHealth{}
	}
	return routerHealth{Running: true, PID: running.PID, Port: running.Port}
}
