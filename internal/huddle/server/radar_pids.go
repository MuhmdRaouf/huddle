package server

import (
	"context"

	radar "github.com/MuhmdRaouf/huddle/internal/radar/server"
)

// radarPids answers Radar's process sampler from the registry: each open session's pid.
type radarPids struct{ s *server }

// LivePids answers every open session that reported a pid; a server without a registry
// answers none.
func (p radarPids) LivePids(ctx context.Context) ([]radar.SessionPid, error) {
	if p.s.registry == nil {
		return nil, nil
	}
	pids, err := p.s.registry.LivePids(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]radar.SessionPid, 0, len(pids))
	for _, pid := range pids {
		out = append(out, radar.SessionPid{SessionID: pid.SessionID, Pid: pid.Pid})
	}
	return out, nil
}
