package store

import "github.com/MuhmdRaouf/huddle/internal/radar/shared"

// NodeOf is the node id a request of the session and agent lands on, by the history writer's
// rule over the session the store holds now; a session it does not hold takes the defaults.
func (s *Store) NodeOf(sessionID, agentID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.sessions[sessionID]
	if !ok || !state.external {
		// a session the store does not hold, or a plain one, has main for its lead
		return shared.NodeID(sessionID, agentID, false, "main")
	}
	// the lead is the session's own external agent, first in the agent order the session
	// detail builds its agent rows from; a session whose job agent has not landed takes the
	// session id, so every agent still sits on the job node
	lead := state.id
	for _, id := range state.agentOrder.ids {
		if agent, held := state.agents[id]; held && agent.kind == shared.KindExternal {
			lead = id
			break
		}
	}
	return shared.NodeID(sessionID, agentID, true, lead)
}
