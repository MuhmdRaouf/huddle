package ledger

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// TestReconcileNamesTheSessionEveryRowSitsIn: every row names the session and agent its
// sources agree on — the transcript's naming when a transcript saw the request (history's
// merged rule), else the route event's, else outside-a-session for a request no source
// puts in a session.
func TestReconcileNamesTheSessionEveryRowSitsIn(t *testing.T) {
	cases := []struct {
		name      string
		in        Sources
		sessionID string
		agentID   string
	}{
		{
			name: "a request only a transcript saw sits in the transcript's session under its agent",
			in: Sources{
				Transcript: &TranscriptFacts{RequestID: "req_t", SessionID: "s1", AgentID: "main"},
			},
			sessionID: "s1",
			agentID:   "main",
		},
		{
			name: "the transcript's session and agent rule when both saw the request",
			in: Sources{
				Route:      &RouteFacts{Event: domain.RouteEvent{SessionID: strp("s1"), AgentID: strp("")}},
				Transcript: &TranscriptFacts{RequestID: "req_m", SessionID: "s1", AgentID: "agent-a7"},
			},
			sessionID: "s1",
			agentID:   "agent-a7",
		},
		{
			name: "a request only a route line saw sits where the line says",
			in: Sources{
				Route: &RouteFacts{Event: domain.RouteEvent{SessionID: strp("s2"), AgentID: strp("a9")}},
			},
			sessionID: "s2",
			agentID:   "a9",
		},
		{
			name: "a route line that names no agent sits under the main agent",
			in: Sources{
				Route: &RouteFacts{Event: domain.RouteEvent{SessionID: strp("s2")}},
			},
			sessionID: "s2",
			agentID:   "main",
		},
		{
			name: "a route line with no session counts outside any session under the main agent",
			in: Sources{
				Route: &RouteFacts{Event: domain.RouteEvent{}},
			},
			sessionID: shared.OutsideSession,
			agentID:   "main",
		},
		{
			name: "a transcript blob from before the facts carried the session counts outside any session under the main agent",
			in: Sources{
				Transcript: &TranscriptFacts{RequestID: "req_old"},
			},
			sessionID: shared.OutsideSession,
			agentID:   "main",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			row, ok := Reconcile(testCase.in)
			if !ok {
				t.Fatal("reconcile refused sources that know the request")
			}
			if row.SessionID != testCase.sessionID {
				t.Errorf("SessionID = %q, want %q", row.SessionID, testCase.sessionID)
			}
			if row.AgentID != testCase.agentID {
				t.Errorf("AgentID = %q, want %q", row.AgentID, testCase.agentID)
			}
		})
	}
}
