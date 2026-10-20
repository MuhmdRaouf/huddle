// One way in for the satellite words: radar's and kanban's commands reach the
// hub through EnsureHub, so no word of theirs starts a server of its own. It
// answers a hub that already runs, starts the hub when the huddle home is set
// up, and says to run setup when it never was.
package cli

import (
	"context"
	"os"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hubaddr"
)

// EnsureHub answers the local huddle hub's base url, starting the hub when no
// hub answers and the huddle home is set up. The address lookup is the one the
// radar hook and kanban's flow client already take (hubaddr), so every part
// asks the same question and gets the same answer. ok is false with the
// message that says why not: never an error across the port.
func EnsureHub(ctx context.Context, w *World, budgetMS int) (string, bool, string) {
	base := hubaddr.Base(envLookupOf(w))
	if base != "" && hubaddr.Healthy(ctx, base) {
		return base, true, ""
	}
	hh, ok := TheHome(ctx, w)
	if !ok || !homeIsSetUp(hh) {
		return "", false, "the huddle hub is not set up; run /huddle:setup"
	}
	result := UpAlone(ctx, w, base, budgetMS)
	if !result.OK {
		return "", false, result.Msg
	}
	return result.URL, true, ""
}

// envLookupOf adapts the world's one-value read to the two-value one hubaddr
// takes.
func envLookupOf(w *World) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value := w.getenv(key)
		return value, value != ""
	}
}

// homeIsSetUp says whether the home's files are there: /huddle:setup lays the
// root out with home.Ensure, so a resolved root that does not exist is a
// machine that never set huddle up — one whose hub must not be started behind
// the user's back.
func homeIsSetUp(h home.Home) bool {
	info, err := os.Stat(h.Root)
	return err == nil && info.IsDir()
}
