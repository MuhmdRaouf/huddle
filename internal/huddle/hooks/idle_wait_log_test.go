package hooks

// A waiter run leaves two lines in hooks.log: one when it arms and one when it exits, with the
// reason and the exit code. The lines name the session and the waiter's pid and carry nothing
// the hub said.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const subagentInput = `{"hook_event_name":"Stop","session_id":"sess-1","agent_id":"agent-9"}`

// idleLogLines answers the arm and exit lines a home's hooks.log holds, without their timestamps.
func idleLogLines(t *testing.T, home string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "hooks.log"))
	if err != nil {
		t.Fatalf("hooks.log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if _, rest, ok := strings.Cut(line, " idle-wait: "); ok && (strings.HasPrefix(rest, "arm ") || strings.HasPrefix(rest, "exit ")) {
			lines = append(lines, rest)
		}
	}
	return lines
}

func armLine(session string) string {
	return fmt.Sprintf("arm session=%s pid=%d", session, os.Getpid())
}

func exitLine(session, reason string, code int) string {
	return fmt.Sprintf("exit session=%s pid=%d reason=%s code=%d", session, os.Getpid(), reason, code)
}

func TestIdleWaitLogsOneArmLineAndOneExitLineWithTheReason(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		session string
		reason  string
		code    int
		prepare func(t *testing.T, hub *fakeHub, home string, set func(key, value string))
		run     time.Duration
		ctx     func() (context.Context, context.CancelFunc)
	}{
		{name: "an ask is delivered", input: stopInput, session: "sess-1", reason: "delivered", code: 2, run: 10 * time.Second,
			prepare: func(t *testing.T, hub *fakeHub, home string, set func(key, value string)) {
				go func() {
					time.Sleep(300 * time.Millisecond)
					hub.sendLive(map[string]any{"seq": 6, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING secret words", "needs_reply": true})
				}()
			}},
		{name: "the cycle ends with the keepalive", input: stopInput, session: "sess-1", reason: "keepalive", code: 2, run: 600 * time.Millisecond},
		{name: "a newer waiter terminates it", input: stopInput, session: "sess-1", reason: "terminated", code: 0, run: 10 * time.Second,
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(400*time.Millisecond, cancel)
				return ctx, cancel
			}},
		{name: "the session never joins", input: stopInput, session: "sess-1", reason: "no-join", code: 0, run: time.Second,
			prepare: func(t *testing.T, hub *fakeHub, home string, set func(key, value string)) {
				set("HUDDLE_CHANNEL", "")
				set("HUDDLE_AS", "")
			}},
		{name: "the input has no session id", input: `{"hook_event_name":"Stop"}`, session: "", reason: "no-session", code: 0, run: time.Second},
		{name: "a subagent runs the hook", input: subagentInput, session: "sess-1", reason: "subagent", code: 0, run: time.Second},
		{name: "the session leaves its channel", input: stopInput, session: "sess-1", reason: "left-channel", code: 0, run: 10 * time.Second,
			prepare: func(t *testing.T, hub *fakeHub, home string, set func(key, value string)) {
				go func() {
					time.Sleep(400 * time.Millisecond)
					set("HUDDLE_CHANNEL", "")
					set("HUDDLE_AS", "")
					hub.server.CloseClientConnections()
				}()
			}},
		{name: "the pid file cannot be written", input: stopInput, session: "sess-1", reason: "claim-failed", code: 0, run: time.Second,
			prepare: func(t *testing.T, hub *fakeHub, home string, set func(key, value string)) {
				if err := os.WriteFile(filepath.Join(home, "run"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hub := newFakeHub(t)
			hub.last = 5
			w, home, set := waitWorldVars(t, hub)
			if c.prepare != nil {
				c.prepare(t, hub, home, set)
			}
			ctx := context.Background()
			if c.ctx != nil {
				var cancel context.CancelFunc
				ctx, cancel = c.ctx()
				defer cancel()
			}
			var errOut bytes.Buffer
			code := idleWait(ctx, w, strings.NewReader(c.input), &errOut, c.run)
			if code != c.code {
				t.Fatalf("exit = %d, want %d; stderr %q", code, c.code, errOut.String())
			}
			want := []string{armLine(c.session), exitLine(c.session, c.reason, c.code)}
			got := idleLogLines(t, home)
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("hooks.log lines = %q, want %q", got, want)
			}
			raw, _ := os.ReadFile(filepath.Join(home, "hooks.log"))
			for _, secret := range []string{"PING", "secret words", "idle-wait-test", "proxbeam"} {
				if strings.Contains(string(raw), secret) {
					t.Errorf("hooks.log carries %q", secret)
				}
			}
		})
	}
}

func TestIdleWaitStaysSilentAndExitsAsBeforeWhenHooksLogCannotBeWritten(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 5
	w, home := waitWorld(t, hub)
	if err := os.Mkdir(filepath.Join(home, "hooks.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stderr, _ := runWaiter(t, w, 600*time.Millisecond)
	if code != 2 || stderr != keepaliveLine() {
		t.Errorf("exit %d, stderr %q, want the keepalive and 2 although hooks.log is a directory", code, stderr)
	}
}
