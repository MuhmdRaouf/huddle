package store

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// NodeOf is the node id the history writer would give a request of the session and agent,
// answered from the session state the store holds.

func TestNodeOf(t *testing.T) {
	t.Run("a session the store does not hold takes the defaults", func(t *testing.T) {
		st := newTestStore()
		if got := st.NodeOf("s9", ""); got != "s9/main" {
			t.Errorf("NodeOf(s9, \"\") = %q, want s9/main", got)
		}
	})

	t.Run("a plain session puts a subagent on its bare-id node", func(t *testing.T) {
		st := newTestStore()
		st.UpsertSession(SessionUpsert{ID: "s1"})
		st.UpsertAgent(AgentUpsert{SessionID: "s1", ID: "main"})
		if got := st.NodeOf("s1", "agent-a7"); got != "s1/a7" {
			t.Errorf("NodeOf(s1, agent-a7) = %q, want s1/a7", got)
		}
	})

	t.Run("an external session's own agent and main both sit on the job node", func(t *testing.T) {
		st := newTestStore()
		// what applyJob sends for one provider job
		st.UpsertSession(SessionUpsert{ID: "zai:1", External: boolPtr(true)})
		st.UpsertAgent(AgentUpsert{
			SessionID: "zai:1", ID: "1", Kind: kindPtr(shared.KindExternal),
			Name: str("1"),
		})
		for _, agent := range []string{"1", "main"} {
			if got := st.NodeOf("zai:1", agent); got != "job:zai:1" {
				t.Errorf("NodeOf(zai:1, %s) = %q, want job:zai:1", agent, got)
			}
		}
	})

	t.Run("an external session with no external agent yet still reads as the job node", func(t *testing.T) {
		st := newTestStore()
		st.UpsertSession(SessionUpsert{ID: "zai:1", External: boolPtr(true)})
		// the lead falls back to the session id, and "main" hits the rule's first clause
		if got := st.NodeOf("zai:1", "main"); got != "job:zai:1" {
			t.Errorf("NodeOf(zai:1, main) = %q, want job:zai:1", got)
		}
	})
}
