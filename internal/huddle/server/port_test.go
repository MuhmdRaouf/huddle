package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// The port tests share the machine with the other packages' tests, so they never demand that a
// port in the range be free or busy: they read what is there first (a foreign holder is a
// stranger like one they bind themselves) and hold only for the one call they are pinning.

// hold binds every port in ports on loopback and answers how to let them go: a stranger holding
// them for the caller's length.
func hold(t *testing.T, ports ...int) (close func()) {
	t.Helper()
	var listeners []net.Listener
	for _, port := range ports {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("bind %d: %v", port, err)
		}
		listeners = append(listeners, listener)
	}
	return func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}
}

// freePort answers a loopback port nothing holds, outside every OS's ephemeral range.
func freePort(t *testing.T) int {
	t.Helper()
	return ports.Free(t)
}

// rangeState answers the range's busy ports and the first free one (0 when a stranger holds all
// ten).
func rangeState() (busy []int, firstFree int) {
	for p := PortFirst; p <= PortLast; p++ {
		if PortFree(p) {
			if firstFree == 0 {
				firstFree = p
			}
		} else {
			busy = append(busy, p)
		}
	}
	return busy, firstFree
}

// holdRange pins the whole range busy — binding what is free, counting what a stranger already
// holds — and answers how to let the bindings go.
func holdRange(t *testing.T) (close func()) {
	t.Helper()
	var free []int
	for p := PortFirst; p <= PortLast; p++ {
		if PortFree(p) {
			free = append(free, p)
		}
	}
	return hold(t, free...)
}

func TestPortFreeKnowsATakenOne(t *testing.T) {
	port := 0
	for _, candidate := range []int{18860, 18861, 28888} {
		if PortFree(candidate) {
			port = candidate
			break
		}
	}
	if port == 0 {
		t.Skip("no port around 18860 is free on this machine")
	}
	defer hold(t, port)()
	if PortFree(port) {
		t.Fatalf("portFree says a taken port (%d) is free", port)
	}
}

func TestChoosePortKeepsAPortTheOwnerSetEvenWhenItIsBusy(t *testing.T) {
	ctx := context.Background()
	// a port outside the range, so the other port tests never contend for it
	own := freePort(t)
	// free and chosen: kept as the owner's
	chosen, err := ChoosePort(ctx, PortPrefs{Port: own}, nil)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != own || chosen.Auto || chosen.Huddle {
		t.Errorf("chosen = %+v, want the owner's port, not auto, not huddle's", chosen)
	}
	// busy and chosen, and no huddle answers there: refused, the error naming the port
	release := hold(t, own)
	chosen, err = ChoosePort(ctx, PortPrefs{Port: own}, func(context.Context, int) bool { return false })
	release()
	var taken PortTakenError
	if !errors.As(err, &taken) || taken.Port != own {
		t.Fatalf("ChoosePort = %v, want PortTakenError naming %d", err, own)
	}
	if got := err.Error(); !strings.Contains(got, fmt.Sprintf("port %d", own)) {
		t.Errorf("the error does not name the port: %q", got)
	}
	// busy but a huddle answers its health path there: reused, never refused
	release = hold(t, own)
	chosen, err = ChoosePort(ctx, PortPrefs{Port: own}, func(_ context.Context, port int) bool { return port == own })
	release()
	if err != nil {
		t.Fatalf("ChoosePort on a huddle-held port: %v", err)
	}
	if chosen.Port != own || !chosen.Huddle {
		t.Errorf("chosen = %+v, want the huddle-held port reused", chosen)
	}
}

func TestChoosePortWalksTheRangeThenTheOS(t *testing.T) {
	ctx := context.Background()
	noHuddle := func(context.Context, int) bool { return false }
	// nothing to keep: the walk answers the first free port in the range, marked auto — or an
	// OS-picked one when a stranger holds all ten
	_, firstFree := rangeState()
	chosen, err := ChoosePort(ctx, PortPrefs{}, noHuddle)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if firstFree == 0 {
		if chosen.Port >= PortFirst && chosen.Port <= PortLast {
			t.Errorf("chosen = %+v, want an OS pick outside the range", chosen)
		}
	} else if chosen.Port != firstFree || !chosen.Auto {
		t.Errorf("chosen = %+v, want the first free port %d", chosen, firstFree)
	}
	// a huddle answering in the range is reused before a free port is taken: one server per
	// home, never a second beside the first
	held := PortFirst + 2
	release := func() {}
	if PortFree(held) {
		release = hold(t, held)
	}
	chosen, err = ChoosePort(ctx, PortPrefs{}, func(_ context.Context, port int) bool { return port == held })
	release()
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != held || !chosen.Huddle || chosen.Auto {
		t.Errorf("chosen = %+v, want the huddle-held %d reused", chosen, held)
	}
	// a saved auto port that is still free is kept, not walked past; one another program took
	// moves to the walk's answer
	saved := freePort(t)
	chosen, err = ChoosePort(ctx, PortPrefs{Port: saved, Auto: true}, noHuddle)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != saved || !chosen.Auto {
		t.Errorf("chosen = %+v, want the saved %d kept", chosen, saved)
	}
	release = hold(t, saved)
	_, firstFree = rangeState()
	chosen, err = ChoosePort(ctx, PortPrefs{Port: saved, Auto: true}, noHuddle)
	release()
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port == saved {
		t.Errorf("chosen = %+v, want the taken %d moved past", chosen, saved)
	}
	if firstFree != 0 && (chosen.Port != firstFree || !chosen.Auto) {
		t.Errorf("chosen = %+v, want the walk's first free %d", chosen, firstFree)
	}
	if firstFree == 0 && chosen.Port >= PortFirst && chosen.Port <= PortLast {
		t.Errorf("chosen = %+v, want an OS pick outside the range", chosen)
	}
}

// TestChoosePortAsksTheOSWhenTheWholeRangeIsHeld pins the walk's last step: with 18860 to 18869
// all held (by this test or by strangers), the OS picks — a free port outside the range, still
// marked auto.
func TestChoosePortAsksTheOSWhenTheWholeRangeIsHeld(t *testing.T) {
	release := holdRange(t)
	chosen, err := ChoosePort(context.Background(), PortPrefs{}, func(context.Context, int) bool { return false })
	release()
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port >= PortFirst && chosen.Port <= PortLast {
		t.Errorf("chosen = %+v, want a port outside %d-%d", chosen, PortFirst, PortLast)
	}
	if !chosen.Auto || chosen.Huddle {
		t.Errorf("chosen = %+v, want an OS-picked auto port", chosen)
	}
	if !PortFree(chosen.Port) {
		// the walk closed its probe listener moments ago; a lingering socket would fail the
		// next holder, so the port it answers must be free right away
		t.Errorf("the OS-picked port %d is not free", chosen.Port)
	}
}
