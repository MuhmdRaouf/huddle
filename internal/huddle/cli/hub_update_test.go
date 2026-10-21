package cli

// The replace decision's rows: a hub of this binary, another home's hub and a hub the pid file
// does not vouch for are never replaced; an older binary's hub of this home, vouched, gives
// way — and a hub that names no executable is an old build's (0.0.1), so it gives way too.

import "testing"

func TestHubNeedsReplace(t *testing.T) {
	const self = "/bin/huddle"
	for _, row := range []struct {
		name    string
		health  hubHealth
		home    string
		vouched bool
		want    bool
	}{
		{"the same executable serves", hubHealth{Home: "/h", Executable: self}, "/h", true, false},
		{"an older executable serves, vouched", hubHealth{Home: "/h", Executable: "/old/huddle"}, "/h", true, true},
		{"a 0.0.1 hub names no executable, vouched", hubHealth{Home: "/h"}, "/h", true, true},
		{"another home's hub", hubHealth{Home: "/other", Executable: "/old/huddle"}, "/h", true, false},
		{"the pid file does not vouch", hubHealth{Home: "/h", Executable: "/old/huddle"}, "/h", false, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := hubNeedsReplace(row.health, row.home, self, row.vouched); got != row.want {
				t.Errorf("hubNeedsReplace(%+v, %q, %q, %v) = %v, want %v",
					row.health, row.home, self, row.vouched, got, row.want)
			}
		})
	}
}
