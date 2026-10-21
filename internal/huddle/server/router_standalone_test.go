package server

import "testing"

func TestIsStandaloneRouterCommand(t *testing.T) {
	t.Parallel()
	rows := []struct {
		command string
		want    bool
	}{
		{"/Users/x/.agents/huddle-bin/src/abc/huddle router serve", true},
		// the memory handover and an update start the daemon with --takeover; it is still
		// a standalone daemon the hub takes back
		{"/Users/x/.agents/huddle-bin/src/abc/huddle router serve --takeover", true},
		{"/Users/x/.agents/huddle-bin/src/abc/huddle serve", false},
		{"/Users/x/.agents/core/0.0.1/plugin-core zai router run", false},
		{"/usr/bin/router serve-something", false},
		{"", false},
	}
	for _, row := range rows {
		if got := isStandaloneRouterCommand(row.command); got != row.want {
			t.Errorf("isStandaloneRouterCommand(%q) = %v, want %v", row.command, got, row.want)
		}
	}
}
