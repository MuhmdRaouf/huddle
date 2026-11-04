package omprpc

// The fake omp's backstop: a fake that stops hearing from its host dies, so a fake stuck
// without its host never lingers.

import (
	"bufio"
	"io"
	"strings"
	"testing"
	"time"
)

// resettable is the part of a timer the backstop restarts.
type resettable interface{ Reset(d time.Duration) bool }

// hostSilence ends the fake when its host says nothing for the limit. Every command the host
// sends starts the limit over, so how long a replay takes never ends it: a starved machine slows
// the host and the fake together, and only a host that stopped talking is cut off.
type hostSilence struct {
	limit time.Duration
	timer resettable
}

// startHostSilence arms the backstop: expire runs once the host has been silent for the limit.
// afterFunc is time.AfterFunc in the fake and a virtual clock in the tests.
func startHostSilence(limit time.Duration, expire func(), afterFunc func(time.Duration, func()) resettable) *hostSilence {
	return &hostSilence{limit: limit, timer: afterFunc(limit, expire)}
}

// realAfterFunc is the fake's clock.
func realAfterFunc(d time.Duration, f func()) resettable { return time.AfterFunc(d, f) }

// heard starts the limit over: the host just sent a command.
func (s *hostSilence) heard() { s.timer.Reset(s.limit) }

// eachHostLine calls onLine with every non-empty line the host writes, after telling silence it
// heard the host, until the host closes the stream.
func eachHostLine(in io.Reader, silence *hostSilence, onLine func(string)) {
	reader := bufio.NewReader(in)
	for {
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimRight(line, "\r\n"); trimmed != "" {
			silence.heard()
			onLine(trimmed)
		}
		if err != nil {
			return
		}
	}
}

// virtualClock hands out timers that fire when the test advances time past them.
type virtualClock struct {
	now    time.Duration
	timers []*virtualTimer
}

type virtualTimer struct {
	clock  *virtualClock
	due    time.Duration
	fire   func()
	fired  bool
	resets int
}

func (c *virtualClock) afterFunc(d time.Duration, f func()) resettable {
	timer := &virtualTimer{clock: c, due: c.now + d, fire: f}
	c.timers = append(c.timers, timer)
	return timer
}

func (t *virtualTimer) Reset(d time.Duration) bool {
	active := !t.fired
	t.due = t.clock.now + d
	t.fired = false
	t.resets++
	return active
}

func (c *virtualClock) advance(d time.Duration) {
	c.now += d
	for _, timer := range c.timers {
		if !timer.fired && timer.due <= c.now {
			timer.fired = true
			timer.fire()
		}
	}
}

func TestAFakeWhoseHostKeepsTalkingIsNeverCutOff(t *testing.T) {
	clock := &virtualClock{}
	expired := false
	silence := startHostSilence(time.Minute, func() { expired = true }, clock.afterFunc)

	for range 10 {
		clock.advance(59 * time.Second)
		silence.heard()
	}

	if expired {
		t.Errorf("the fake was cut off after %v with its host speaking every 59 s, want it kept", clock.now)
	}
}

func TestAFakeWhoseHostFallsSilentIsCutOffAtTheLimit(t *testing.T) {
	clock := &virtualClock{}
	expired := false
	silence := startHostSilence(time.Minute, func() { expired = true }, clock.afterFunc)
	clock.advance(30 * time.Second)
	silence.heard()

	clock.advance(59*time.Second + 999*time.Millisecond)
	if expired {
		t.Fatal("the fake was cut off 1 ms before the limit")
	}
	clock.advance(time.Millisecond)

	if !expired {
		t.Errorf("the fake outlived a host silent for the whole limit, want it cut off")
	}
}

func TestEveryCommandTheHostWritesStartsTheLimitOver(t *testing.T) {
	clock := &virtualClock{}
	silence := startHostSilence(time.Minute, func() {}, clock.afterFunc)
	var lines []string

	eachHostLine(strings.NewReader("first\n\nsecond\r\nthird"), silence, func(line string) {
		lines = append(lines, line)
	})

	if got := strings.Join(lines, ","); got != "first,second,third" {
		t.Errorf("lines = %q, want first,second,third", got)
	}
	if got := clock.timers[0].resets; got != 3 {
		t.Errorf("the limit started over %d times, want once per command (3)", got)
	}
}
