package cli

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// fixedHubPort is the port the owner's hub also answers on when it is set up with a fixed one.
const fixedHubPort = 4835

// TestEveryTestWorldWalksARangeOfItsOwn pins the test worlds' port walk: huddle up in a world
// the helpers build picks its port from a run of ports that world owns, never from the
// product's range, huddle-router's port or the fixed hub port, where the owner's live servers
// answer and a walk would probe them.
func TestEveryTestWorldWalksARangeOfItsOwn(t *testing.T) {
	worlds := map[string]*World{
		"cliWorld": cliWorld(t, t.TempDir(), t.TempDir(), nil),
		"world":    world(t, t.TempDir(), nil),
	}
	for name, w := range worlds {
		r := w.PortRange
		if r == (server.PortRange{}) || r.First > r.Last {
			t.Errorf("%s has the range %+v, want a run of ports of its own", name, r)
			continue
		}
		for port := r.First; port <= r.Last; port++ {
			if port >= server.PortFirst && port <= server.PortLast || port == domain.HuddleRouterPort || port == fixedHubPort {
				t.Errorf("%s walks %d, a port the owner's live servers answer on", name, port)
			}
		}
	}
}
