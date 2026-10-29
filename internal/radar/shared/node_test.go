package shared

import "testing"

// The node id rule the history writer applies and every reader joins on.

func TestBareAgent(t *testing.T) {
	t.Run("a transcript file name and a router header name the same agent", func(t *testing.T) {
		if got := BareAgent("agent-a31cd3de580148dc3"); got != "a31cd3de580148dc3" {
			t.Errorf("BareAgent(agent-a31cd3de580148dc3): got %q, want %q", got, "a31cd3de580148dc3")
		}
		if got := BareAgent("a31cd3de580148dc3"); got != "a31cd3de580148dc3" {
			t.Errorf("BareAgent(a31cd3de580148dc3): got %q, want %q", got, "a31cd3de580148dc3")
		}
	})
}

func TestNodeID(t *testing.T) {
	cases := []struct {
		session, agent string
		external       bool
		lead, want     string
	}{
		{"s1", "main", false, "main", "s1/main"},
		{"s1", "", false, "main", "s1/main"},
		{"s1", "agent-a7", false, "main", "s1/a7"},
		{"s1", "a7", false, "main", "s1/a7"},
		{"zai:1", "1", true, "1", "job:zai:1"},
		{"zai:1", "main", true, "1", "job:zai:1"},
		{"zai:1", "sub", true, "1", "zai:1/sub"},
	}
	for _, c := range cases {
		if got := NodeID(c.session, c.agent, c.external, c.lead); got != c.want {
			t.Errorf("NodeID(%q, %q, %t, %q) = %q, want %q", c.session, c.agent, c.external, c.lead, got, c.want)
		}
	}
}
