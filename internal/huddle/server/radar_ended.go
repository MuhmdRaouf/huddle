package server

import "context"

// radarEnded answers which sessions huddle's registry has ended, for radar's ingest.
type radarEnded struct{ s *server }

// EndedAmong answers the sessions the registry has ended; a server without a registry
// answers none.
func (p radarEnded) EndedAmong(ctx context.Context, ids []string) (map[string]bool, error) {
	if p.s.registry == nil {
		return nil, nil
	}
	return p.s.registry.EndedAmong(ctx, ids)
}
