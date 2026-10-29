package shared

import "strings"

// The node id rule: one spelling for the node a request lands on, shared by the history
// writer and every reader that joins requests onto nodes.

// BareAgent strips the transcript's agent- prefix: "agent-a31cd…" (a transcript file
// name) and "a31cd…" (the router's header) name the same agent.
func BareAgent(id string) string {
	if bare, ok := strings.CutPrefix(id, "agent-"); ok {
		return bare
	}
	return id
}

// NodeID is a record's node id: the session's lead agent (main, or a job's own agent)
// sits on the session node, every other agent on its own /agent node. A job's phantom main
// agent collapses onto the job node. A subagent's node takes its bare id: the router and the
// hooks spell it bare, its transcript file agent-<id>, and both name one agent.
func NodeID(sessionID, agentID string, external bool, lead string) string {
	if agentID == "main" || agentID == "" || (external && agentID == lead) {
		if external {
			return "job:" + sessionID
		}
		return sessionID + "/main"
	}
	return sessionID + "/" + BareAgent(agentID)
}
