// The hub's own-machine delivery of an owner's message: the words posted straight
// through the session's inbox socket, and the sessions file read to tell the route — inbox when
// the session's claude reads the turn at once, held while it keeps the words for the owner's
// approval in its terminal. No socket, a dead socket and a failed post answer nothing: the
// channel push and the next prompt stay as they are.
package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/ccinbox"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

const (
	// inboxPostFor bounds the socket write; a claude that reads its inbox reads it fast.
	inboxPostFor = 2 * time.Second

	// heldWindow is how long the sessions file is read for a claude that holds the turn for the
	// owner's approval; a file without the hold past the window leaves the message delivered.
	heldWindow = time.Second
)

// The moods Claude Code's sessions file gives a claude.
const (
	statusIdle    = "idle"
	statusBusy    = "busy"
	statusWaiting = "waiting"
)

// heldPoll is how often the sessions file is read inside heldWindow.
const heldPoll = 100 * time.Millisecond

// inboxSession is the piece of Claude Code's sessions file the verdict reads: the fields that
// name a live session and its composer's mood.
type inboxSession struct {
	SessionID string `json:"sessionId"`
	Pid       int    `json:"pid"`
	Status    string `json:"status"`
}

// The ways an inbox post arrives in a session's claude: between its tool calls while it works,
// as the start of a new turn while it idles, and held for the owner's approval in its terminal.
// An empty arrival names a route that does not use the socket.
const (
	arrivalBetweenCalls = "between_calls"
	arrivalStartedTurn  = "started_turn"
	arrivalHeld         = "held"
)

// delivery is how one owner message reached its session: the route it took and, on the inbox
// route, how it arrived.
type delivery struct {
	Route   string
	Arrival string
}

// postInbox carries an owner's message through the session's inbox socket and answers the
// delivery: route inbox when the post landed and the claude reads it at once, held when the
// claude keeps it for the owner's approval in its terminal, and an empty delivery when the
// session has no socket or the post fails — the channel push and the next prompt take it from
// there. The session's messaging token goes nowhere but the socket's wire.
func (s *server) postInbox(ctx context.Context, id int64, in hub.MessageIn) delivery {
	socket, err := s.registry.InboxSocket(ctx, in.SessionID)
	if err != nil || socket == "" {
		return delivery{}
	}

	// The post itself flips an idle claude to busy, so its mood is read before the post.
	row, err := s.registry.SessionRow(ctx, in.SessionID, s.now())
	live := err == nil && row != nil && row.Pid > 0
	file, arrival := "", arrivalOf("", row)
	if live {
		file = s.sessionsFile(row.Pid)
		arrival = arrivalOf(inboxStatus(file, in.SessionID, row.Pid), row)
	}

	m := ccinbox.Message{SessionID: in.SessionID, Text: in.Header()}
	if token, ok := s.secrets(ctx, home.Home{Root: s.o.Home}).Get(ctx, "cc-messaging:"+in.SessionID); ok {
		m.Token = token
	}
	post, cancel := context.WithTimeout(ctx, inboxPostFor)
	defer cancel()
	if err := ccinbox.Post(post, socket, m); err != nil {
		return delivery{}
	}

	// No live pid names no sessions file: the claude that answers the socket reads the turn.
	if !live {
		_ = s.registry.MarkDelivered(ctx, id, hub.RouteInbox, jsscalar.ISOTime(s.now()))
		return delivery{hub.RouteInbox, arrival}
	}

	// The claude reads the turn at once or holds it for the owner's approval in its terminal;
	// the sessions file tells which, read for a short window before the message counts as
	// delivered. The window waits on a real file, so it runs on the wall clock: a fixed clock (a
	// test's) must not hold the read open past the window.
	deadline := time.Now().Add(heldWindow)
	for {
		if ctx.Err() == nil && inboxStatus(file, in.SessionID, row.Pid) == statusWaiting {
			if err := s.registry.MarkHeld(ctx, id, jsscalar.ISOTime(s.now())); err != nil {
				return delivery{}
			}
			return delivery{hub.RouteHeld, arrivalHeld}
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(heldPoll)
	}
	_ = s.registry.MarkDelivered(ctx, id, hub.RouteInbox, jsscalar.ISOTime(s.now()))
	return delivery{hub.RouteInbox, arrival}
}

// sessionsFile is the path of Claude Code's sessions file for one pid. The config dir comes from
// the server's own environment, or the process's when the server was handed none.
func (s *server) sessionsFile(pid int) string {
	env := s.o.Environ
	if len(env) == 0 {
		env = os.Environ()
	}
	return filepath.Join(outside.ClaudeConfigDir(environOf(env)), "sessions", strconv.Itoa(pid)+".json")
}

// arrivalOf maps the claude's mood to how a post arrives: an idle claude starts a turn and a busy
// one reads it between tool calls. A mood the sessions file does not give (no file, an
// unreadable one, a waiting claude) falls back to the registry row's state.
func arrivalOf(status string, row *hub.SessionRow) string {
	switch status {
	case statusIdle:
		return arrivalStartedTurn
	case statusBusy:
		return arrivalBetweenCalls
	}
	if row != nil && row.State == hub.StateIdle {
		return arrivalStartedTurn
	}
	return arrivalBetweenCalls
}

// inboxStatus answers the mood the sessions file gives this session's claude: idle, busy or
// waiting. A file names the session only with its id and its pid both matching; an unreadable
// file and a stale one answer "".
func inboxStatus(file, sessionID string, pid int) string {
	raw, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	var f inboxSession
	if err := json.Unmarshal(raw, &f); err != nil {
		return ""
	}
	if f.SessionID != sessionID || f.Pid != pid {
		return ""
	}
	return f.Status
}

// environOf maps a process environment's entries, one "NAME=value" per line.
func environOf(entries []string) map[string]string {
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	return env
}
