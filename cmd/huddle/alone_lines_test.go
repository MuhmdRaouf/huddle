package main

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

// TestUpAndStatusNameBothPids pins the lines up and status print about the two processes: the
// hub with its pid, and huddle-router with its address and pid, or why it does not run.
func TestUpAndStatusNameBothPids(t *testing.T) {
	t.Parallel()
	t.Run("the hub line names its pid", func(t *testing.T) {
		if got, want := hubUpLine("http://127.0.0.1:40860", 4242), "huddle is up at http://127.0.0.1:40860 · pid 4242"; got != want {
			t.Errorf("hubUpLine = %q, want %q", got, want)
		}
	})
	t.Run("a hub with no pid on record is the bare address", func(t *testing.T) {
		if got, want := hubUpLine("http://127.0.0.1:40860", 0), "huddle is up at http://127.0.0.1:40860"; got != want {
			t.Errorf("hubUpLine = %q, want %q", got, want)
		}
	})
	t.Run("a running router names its address and pid", func(t *testing.T) {
		got := routerUpLine(routerd.Running{PID: 5151, Port: 40786}, true, "huddle-router: no provider is set up")
		if want := "huddle-router is up at http://127.0.0.1:40786 · pid 5151"; got != want {
			t.Errorf("routerUpLine = %q, want %q", got, want)
		}
	})
	t.Run("a router that does not run says why", func(t *testing.T) {
		got := routerUpLine(routerd.Running{}, false, "huddle-router: no provider is set up")
		if want := "huddle-router is not running (no provider is set up)"; got != want {
			t.Errorf("routerUpLine = %q, want %q", got, want)
		}
	})
	t.Run("a router that does not run with no reason is the bare line", func(t *testing.T) {
		if got, want := routerUpLine(routerd.Running{}, false, ""), "huddle-router is not running"; got != want {
			t.Errorf("routerUpLine = %q, want %q", got, want)
		}
	})
}
