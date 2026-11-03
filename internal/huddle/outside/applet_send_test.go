package outside

// The macOS applet send, driven directly so it is proven on every platform: Send reaches it only
// where osascript is the notifier program.

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// appletNotifier answers a notifier whose home holds nothing yet, with its spawns and builds
// recorded.
type appletNotifier struct {
	*Notifier
	home   string
	spawns chan appletSpawn
	builds atomic.Int32
}

type appletSpawn struct {
	program string
	args    []string
}

func newAppletNotifier(t *testing.T) *appletNotifier {
	t.Helper()
	a := &appletNotifier{Notifier: NewNotifier(t.TempDir()), home: t.TempDir(), spawns: make(chan appletSpawn, 4)}
	t.Setenv("HOME", a.home)
	a.Spawn = func(program string, args []string) error {
		a.spawns <- appletSpawn{program, args}
		return nil
	}
	return a
}

func (a *appletNotifier) nextSpawn(t *testing.T) appletSpawn {
	t.Helper()
	select {
	case spawn := <-a.spawns:
		return spawn
	case <-time.After(5 * time.Second):
		t.Fatal("no notifier program was started")
		return appletSpawn{}
	}
}

// TestAReadyAppletsStubTakesTheTextAsArgumentsAndNothingBuilds: with the applet built, the stub
// is started with the title and the body as its arguments, and no build begins.
func TestAReadyAppletsStubTakesTheTextAsArgumentsAndNothingBuilds(t *testing.T) {
	a := newAppletNotifier(t)
	stub := installApplet(t, a.home)
	a.Build = func(string) { a.builds.Add(1) }

	a.sendThroughApplet("ask", "s", "T", "b")

	if got := a.nextSpawn(t); got.program != stub || !slices.Equal(got.args, []string{"T", "b"}) {
		t.Errorf("started %q %v, want the stub %q with [T b]", got.program, got.args, stub)
	}
	if got := a.builds.Load(); got != 0 {
		t.Errorf("a ready applet started %d builds, want none", got)
	}
}

// TestAnUnbuiltAppletBuildsOnceAndTheStubSendsWhenItFinishes: two notifications that find no
// applet share one build, and each goes out through the stub it leaves.
func TestAnUnbuiltAppletBuildsOnceAndTheStubSendsWhenItFinishes(t *testing.T) {
	a := newAppletNotifier(t)
	var stub string
	a.Build = func(home string) {
		a.builds.Add(1)
		stub = installApplet(t, home)
	}

	a.sendThroughApplet("ask", "s1", "T1", "b1")
	a.sendThroughApplet("ask", "s2", "T2", "b2")

	var got []appletSpawn
	for range 2 {
		got = append(got, a.nextSpawn(t))
	}
	for _, want := range []appletSpawn{{stub, []string{"T1", "b1"}}, {stub, []string{"T2", "b2"}}} {
		if !slices.ContainsFunc(got, func(s appletSpawn) bool { return s.program == want.program && slices.Equal(s.args, want.args) }) {
			t.Errorf("no start of the stub %q with %v; started %v", want.program, want.args, got)
		}
	}
	if n := a.builds.Load(); n != 1 {
		t.Errorf("builds = %d, want 1 across two notifications", n)
	}
}

// TestABuildThatLeavesNoAppletDropsTheNotificationWithALogLineNamingItsKindAndSubject: nothing
// is started, and the line never carries the notification's text.
func TestABuildThatLeavesNoAppletDropsTheNotificationWithALogLineNamingItsKindAndSubject(t *testing.T) {
	a := newAppletNotifier(t)
	logs := make(chan string, 4)
	a.Build = func(string) { a.builds.Add(1) }
	a.Log = func(line string) { logs <- line }

	a.sendThroughApplet("ask", "s1", "T", "secret body")

	select {
	case line := <-logs:
		if !strings.Contains(line, "ask") || !strings.Contains(line, "s1") {
			t.Errorf("log line %q, want the kind and the subject", line)
		}
		if strings.Contains(line, "secret body") {
			t.Errorf("log line %q holds the notification text", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no log line for the dropped notification")
	}
	select {
	case spawn := <-a.spawns:
		t.Errorf("started %q for an applet that never built", spawn.program)
	default:
	}
	if n := a.builds.Load(); n != 1 {
		t.Errorf("builds = %d, want 1", n)
	}
}
