package ledger

import "github.com/MuhmdRaouf/huddle/internal/radar/shared"

// node fills the row's node and, for a request only a transcript saw, the way its call
// left. It runs after billing: billing names the via when a route line saw the request and
// leaves it unset when none did. A harness row takes the node its facts carry.
func node(s Sources, row *Row) {
	switch {
	case s.Harness != nil:
		row.NodeID = s.Harness.NodeID
	case s.Transcript != nil && s.Transcript.NodeID != "":
		// the merged record's session and agent are the transcript's (Combine), so its
		// node rules the row
		row.NodeID = s.Transcript.NodeID
	case s.Route != nil && s.Route.NodeID != "":
		row.NodeID = s.Route.NodeID
	case s.Route != nil:
		// facts that name no node: the rule runs over the route event's own session and
		// agent
		session := shared.OutsideSession
		if s.Route.Event.SessionID != nil && *s.Route.Event.SessionID != "" {
			session = *s.Route.Event.SessionID
		}
		agent := ""
		if s.Route.Event.AgentID != nil {
			agent = *s.Route.Event.AgentID
		}
		row.NodeID = shared.NodeID(session, agent, false, "main")
	}
	if s.Route == nil && s.Transcript != nil && s.Transcript.Via != nil {
		row.Via = s.Transcript.Via
	}
}
