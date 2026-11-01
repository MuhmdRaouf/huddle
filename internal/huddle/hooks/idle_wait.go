// The Stop hook that keeps an idle session listening: `huddle hook idle-wait` runs as an
// asyncRewake command hook. It follows the session's channel and exits 2 with one line per
// event meant for the session, which Claude Code shows as a system reminder and answers at
// once, also when the session is idle. A session that has not joined a channel is not listened
// for: the hook exits 0 at once and never joins or creates anything.
//
// One waiter listens per session (a pid file under the huddle home); a new waiter stops the one
// before it. A waiter lives for the hook's timeout less a margin. At the end of that cycle it
// delivers anything it still finds, and otherwise exits 2 with a keepalive line: the session
// answers it, the turn ends, the Stop hook starts the next waiter, and the session never goes
// deaf. The waiter reads the feed's cursor and never moves it, so the next prompt's feed still
// shows what the reminder said; its own cursor file keeps the next waiter from repeating a wake.
// The hook runs at session start and at every Stop, so a session is covered before its first
// prompt; the one pid file keeps the two from listening twice, and an ask the Stop hook already
// raised is not repeated. Any failure is silent (quiet.go), and an error never wakes the session.
package hooks

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

const (
	// idleWaitTimeoutSeconds is the timeout plugin/hooks/hooks.json grants the hook.
	idleWaitTimeoutSeconds = 2000000
	// idleWaitMargin is how long before that timeout a waiter ends its cycle.
	idleWaitMargin = 10 * time.Second
	// idleWaitRun is how long a waiter listens.
	idleWaitRun = idleWaitTimeoutSeconds*time.Second - idleWaitMargin
	// idleWaitStdinMS bounds the one read of the hook's input.
	idleWaitStdinMS = 500
	// idleFinalLook bounds the last look at the channel when the cycle ends.
	idleFinalLook = 4 * time.Second
	// idleJoinWait is how long a waiter started with the session waits for it to join a channel;
	// idleJoinPoll paces its looks. The session-start hook joins within 3 s.
	idleJoinWait = 20 * time.Second
	idleJoinPoll = time.Second
	// idleBackoffMin and idleBackoffMax bound the pause between failed attempts.
	idleBackoffMin = time.Second
	idleBackoffMax = 30 * time.Second
)

// IdleWait is the idle-wait hook's entry: the exit code is 2 when the session should wake (its
// reminder lines go to errOut) and 0 otherwise. HUDDLE_IDLE_WAIT_SECONDS shortens the cycle.
func IdleWait(ctx context.Context, w *cli.World, in io.Reader, errOut io.Writer) int {
	run := idleWaitRun
	if seconds, err := strconv.Atoi(getenvOf(w, "HUDDLE_IDLE_WAIT_SECONDS")); err == nil && seconds > 0 {
		run = time.Duration(seconds) * time.Second
	}
	return idleWait(ctx, w, in, errOut, run)
}

func idleWait(ctx context.Context, w *cli.World, in io.Reader, errOut io.Writer, run time.Duration) int {
	code := 0
	Run(ctx, w, "idle-wait", func() error {
		lines, err := listen(ctx, w, in, run)
		if len(lines) > 0 {
			for _, line := range lines {
				_, _ = io.WriteString(errOut, line+"\n")
			}
			code = 2
		}
		return err
	})
	return code
}

// listen waits for what is meant for the session and answers the reminder lines: events, or the
// keepalive at the end of the cycle; none when the session ends the wait or never joined.
func listen(parent context.Context, w *cli.World, in io.Reader, run time.Duration) ([]string, error) {
	var input hookInput
	readInput(in, idleWaitStdinMS, &input)
	session := input.SessionID
	if session == "" || input.AgentID != "" {
		return nil, nil
	}
	home := cli.Home(parent, w)
	id, joined := waitForJoin(parent, w, session, min(idleJoinWait, run))
	if !joined || home == "" {
		return nil, nil
	}
	files := waiterFiles{dir: filepath.Join(home, "run", "idle-wait"), commandOf: procCommandOf, stop: stopWaiter}
	pid := os.Getpid()
	if err := files.claim(session, pid, nowOf(w)); err != nil {
		return nil, err
	}
	defer files.release(session, pid)

	stream := &idleStream{w: w, session: session, home: home}
	cursor, haveCursor := startCursor(home, session, id.Channel)
	stream.cursor = cursor
	cycle, cancel := context.WithTimeout(parent, run)
	defer cancel()

	var failure error
	backoff := idleBackoffMin
	for cycle.Err() == nil {
		id = cli.Identify(cycle, w, session) // the hub's url is read again on every attempt
		if id.Channel == "" || id.As == "" {
			return nil, nil
		}
		woken, connected, err := stream.follow(cycle, id, haveCursor)
		haveCursor = haveCursor || connected
		if len(woken) > 0 {
			return delivered(home, session, id, stream, woken), nil
		}
		if connected {
			backoff = idleBackoffMin
		}
		if err != nil && cycle.Err() == nil {
			failure = err
		}
		select {
		case <-cycle.Done():
		case <-time.After(backoff):
		}
		if !connected {
			backoff = min(backoff*2, idleBackoffMax)
		}
	}
	if parent.Err() != nil {
		return nil, nil // the hook was stopped: a newer waiter took over, or the session ended
	}
	return endOfCycle(parent, home, session, id, stream, failure)
}

// waitForJoin resolves the session's identity, again every second for up to wait while it has no
// channel: a waiter started with the session can run before the session-start hook has joined.
func waitForJoin(ctx context.Context, w *cli.World, session string, wait time.Duration) (cli.Identity, bool) {
	deadline := time.Now().Add(wait)
	for {
		id := cli.Identify(ctx, w, session)
		if id.Channel != "" && id.As != "" {
			return id, true
		}
		left := time.Until(deadline)
		if left <= 0 {
			return id, false
		}
		select {
		case <-ctx.Done():
			return id, false
		case <-time.After(min(idleJoinPoll, left)):
		}
	}
}

// startCursor is where a waiter starts: past what the feed has shown and what an earlier waiter
// delivered, whichever is later.
func startCursor(home, session, channel string) (int64, bool) {
	seen, haveSeen := seenCursor(home, session, channel)
	woken, haveWoken := wokenCursor(home, session, channel)
	return max(seen, woken), haveSeen || haveWoken
}

// delivered records what a wake carries and answers its lines.
func delivered(home, session string, id cli.Identity, stream *idleStream, woken []wakeEvent) []string {
	saveWokenCursor(home, session, id.Channel, stream.woken, time.Now())
	lines := make([]string, 0, len(woken))
	for _, event := range woken {
		lines = append(lines, wakeLine(event))
	}
	return lines
}

// endOfCycle takes a last look at the channel, so a message that arrived at the edge of the
// cycle is delivered, and otherwise answers the keepalive.
func endOfCycle(parent context.Context, home, session string, id cli.Identity, stream *idleStream, failure error) ([]string, error) {
	look, cancel := context.WithTimeout(parent, idleFinalLook)
	defer cancel()
	if woken, err := stream.catchUp(look, id); err == nil && len(woken) > 0 {
		return delivered(home, session, id, stream, woken), nil
	}
	return []string{idleKeepalive(id.Channel)}, failure
}
