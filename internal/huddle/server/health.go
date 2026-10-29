package server

import (
	"context"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/health"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

// routerHealth is the part of the hub's health answer that names huddle-router, the process
// beside the hub: whether it serves, its pid, its port and the providers its providers.json
// enables (the Radar routers tile reads "served of enabled" from it).
type routerHealth struct {
	Running   bool     `json:"running"`
	PID       int      `json:"pid,omitempty"`
	Port      int      `json:"port,omitempty"`
	Providers []string `json:"providers"`
	// parts is what the router's own process reports, folded into the answer's components and
	// its healthy verdict; never serialized here.
	parts []routerd.HealthPart `json:"-"`
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
	providers, err := router.ReadEnabled(env)
	if err != nil {
		providers = nil
	}
	if providers == nil {
		providers = []string{}
	}
	running, ok := routerd.RunningOf(ctx, env)
	if !ok {
		return routerHealth{Providers: providers}
	}
	return routerHealth{Running: true, PID: running.PID, Port: running.Port, Providers: providers, parts: running.Components}
}

// routerParts answers the router's components as the hub's parts, each named router.<name>.
func routerParts(parts []routerd.HealthPart) []health.Part {
	out := make([]health.Part, 0, len(parts))
	for _, part := range parts {
		out = append(out, health.Part{
			Name:      "router." + part.Name,
			Kind:      part.Kind,
			State:     part.State,
			LastOKAt:  part.LastOKAt,
			LastErrAt: part.LastErrAt,
			LastError: part.LastError,
			Errors:    part.Errors,
		})
	}
	return out
}

// healthyWith answers the hub's health once the router's parts are folded in: the hub's own
// verdict, and no router part failing or stale.
func healthyWith(hub bool, parts []routerd.HealthPart) bool {
	if !hub {
		return false
	}
	for _, part := range parts {
		if part.State == string(domain.HealthFailing) || part.State == string(domain.HealthStale) {
			return false
		}
	}
	return true
}
