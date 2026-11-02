// Package spawner is the fixture the tmpguard tests run the guard over: one test whose child
// starts a notifier program, one that starts nothing. Both pass on their own; the guard decides.
package spawner

import (
	"os/exec"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
)

func TestStartsANotifier(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "notify-send Huddle blocked")
	cmd.Env = isoenv.Environ(t, t.TempDir())
	_ = cmd.Run() // the shim fails the spawn; the test does not care
}

func TestStartsNothing(t *testing.T) {}
