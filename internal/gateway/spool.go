// The gateway's spool half: where its route lines go when it serves inside the
// router daemon.
package gateway

import (
	"github.com/MuhmdRaouf/huddle/internal/adapters/spool"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// SpoolRecorder wires the spool writer radar's own readers drain: one route line per request
// under `$RADAR_HOME/spool`, the same files the provider routers append to. log carries the
// writer's own trouble, or nil to drop it.
func SpoolRecorder(env domain.EnvLookup, log func(string)) func(domain.RouteEvent) {
	write := spool.CreateSpoolWriter(env, log, nil)
	return func(event domain.RouteEvent) { write(event) }
}
