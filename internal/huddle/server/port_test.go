package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
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

// ownRange answers a run of four neighbouring ports no other test holds: the range a test walks
// instead of the product's PortFirst to PortLast, where the owner's hub serves.
func ownRange(t *testing.T) PortRange {
	t.Helper()
	first, last := ports.FreeRun(t, 4)
	return PortRange{First: first, Last: last}
}

// holdRange binds every port of r and answers how to let them go: strangers holding the whole
// range.
func holdRange(t *testing.T, r PortRange) (close func()) {
	t.Helper()
	var all []int
	for p := r.First; p <= r.Last; p++ {
		all = append(all, p)
	}
	return hold(t, all...)
}

func TestPortFreeKnowsATakenOne(t *testing.T) {
	port := freePort(t)
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
	r := ownRange(t)
	// nothing to keep: the walk answers the range's first free port, marked auto
	chosen, err := ChoosePort(ctx, PortPrefs{Range: r}, noHuddle)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != r.First || !chosen.Auto {
		t.Errorf("chosen = %+v, want the range's first port %d", chosen, r.First)
	}
	// a stranger on the first port moves the walk to the next
	release := hold(t, r.First)
	chosen, err = ChoosePort(ctx, PortPrefs{Range: r}, noHuddle)
	release()
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != r.First+1 || !chosen.Auto {
		t.Errorf("chosen = %+v, want the second port %d", chosen, r.First+1)
	}
	// a huddle answering in the range is reused before a free port is taken: one server per
	// home, never a second beside the first
	held := r.First + 2
	release = hold(t, held)
	chosen, err = ChoosePort(ctx, PortPrefs{Range: r}, func(_ context.Context, port int) bool { return port == held })
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
	chosen, err = ChoosePort(ctx, PortPrefs{Port: saved, Auto: true, Range: r}, noHuddle)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != saved || !chosen.Auto {
		t.Errorf("chosen = %+v, want the saved %d kept", chosen, saved)
	}
	release = hold(t, saved)
	chosen, err = ChoosePort(ctx, PortPrefs{Port: saved, Auto: true, Range: r}, noHuddle)
	release()
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != r.First || !chosen.Auto {
		t.Errorf("chosen = %+v, want the taken %d moved to the walk's first %d", chosen, saved, r.First)
	}
}

// TestChoosePortAsksTheOSWhenTheWholeRangeIsHeld pins the walk's last step: with the range all
// held, the OS picks a free port outside it, still marked auto.
func TestChoosePortAsksTheOSWhenTheWholeRangeIsHeld(t *testing.T) {
	r := ownRange(t)
	release := holdRange(t, r)
	chosen, err := ChoosePort(context.Background(), PortPrefs{Range: r}, func(context.Context, int) bool { return false })
	release()
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port >= r.First && chosen.Port <= r.Last {
		t.Errorf("chosen = %+v, want a port outside %d-%d", chosen, r.First, r.Last)
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

// walkTrace is what a walk touched: the ports it asked a huddle about and the ports it asked
// whether they bind, in order. Neither fake dials or binds anything.
type walkTrace struct {
	probed []int
	tried  []int
}

// prefs answers PortPrefs whose probe and Free only record, with the given ports busy.
func (w *walkTrace) prefs(r PortRange, busy ...int) (PortPrefs, PortProbe) {
	taken := map[int]bool{}
	for _, port := range busy {
		taken[port] = true
	}
	free := func(port int) bool {
		w.tried = append(w.tried, port)
		return !taken[port]
	}
	probe := func(_ context.Context, port int) bool {
		w.probed = append(w.probed, port)
		return false
	}
	return PortPrefs{Range: r, Free: free}, probe
}

// TestTheDefaultWalkGoesFromPortFirstToPortLastThenTheOS pins the product's order with fakes
// that never listen on or dial the range: every port is asked about before any is bound, the
// first free one in order wins, and a full range hands the choice to the OS.
func TestTheDefaultWalkGoesFromPortFirstToPortLastThenTheOS(t *testing.T) {
	var trace walkTrace
	prefs, probe := trace.prefs(PortRange{}, PortFirst, PortFirst+1)
	chosen, err := ChoosePort(context.Background(), prefs, probe)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != PortFirst+2 || !chosen.Auto || chosen.Huddle {
		t.Errorf("chosen = %+v, want %d, auto", chosen, PortFirst+2)
	}
	var wantProbed []int
	for p := PortFirst; p <= PortLast; p++ {
		wantProbed = append(wantProbed, p)
	}
	if !slices.Equal(trace.probed, wantProbed) {
		t.Errorf("probed = %v, want %v", trace.probed, wantProbed)
	}
	if want := []int{PortFirst, PortFirst + 1, PortFirst + 2}; !slices.Equal(trace.tried, want) {
		t.Errorf("tried = %v, want %v", trace.tried, want)
	}

	var all []int
	for p := PortFirst; p <= PortLast; p++ {
		all = append(all, p)
	}
	trace = walkTrace{}
	prefs, probe = trace.prefs(PortRange{}, all...)
	chosen, err = ChoosePort(context.Background(), prefs, probe)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port >= PortFirst && chosen.Port <= PortLast || !chosen.Auto {
		t.Errorf("chosen = %+v, want an OS pick outside %d-%d", chosen, PortFirst, PortLast)
	}
	if !slices.Equal(trace.tried, all) {
		t.Errorf("tried = %v, want every port of the range once", trace.tried)
	}
}

// TestTheWalkProbesAndBindsOnlyTheRangeItIsGiven pins the test seam: a walk over a range of a
// test's own asks about and tries those ports and no others. The ports the product's walk and
// the owner's router use belong to the owner's live huddle, and a test run must never reach them.
func TestTheWalkProbesAndBindsOnlyTheRangeItIsGiven(t *testing.T) {
	r := PortRange{First: 40100, Last: 40103}
	var trace walkTrace
	prefs, probe := trace.prefs(r, 40100)
	chosen, err := ChoosePort(context.Background(), prefs, probe)
	if err != nil {
		t.Fatalf("ChoosePort: %v", err)
	}
	if chosen.Port != 40101 {
		t.Errorf("chosen = %+v, want 40101", chosen)
	}
	for _, port := range append(slices.Clone(trace.probed), trace.tried...) {
		if port < r.First || port > r.Last {
			t.Errorf("the walk touched %d, outside its range %d-%d (probed %v, tried %v)", port, r.First, r.Last, trace.probed, trace.tried)
		}
	}
	if len(trace.probed) != 4 {
		t.Errorf("probed = %v, want the range's four ports", trace.probed)
	}
}
