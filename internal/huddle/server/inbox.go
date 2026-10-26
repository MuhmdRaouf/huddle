// The hub's own-machine delivery of an owner's message (SPEC 6.4): the words posted straight
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
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

const (
	// inboxPostFor bounds the socket write; a claude that reads its inbox reads it fast.
	inboxPostFor = 2 * time.Second

	// heldWindow is how long the sessions file is read for a claude that holds the turn for the
	// owner's approval; a file without the hold past the window leaves the message delivered.
	heldWindow = time.Second
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

// postInbox carries an owner's message through the session's inbox socket and answers the route
// it took: inbox when the post landed and the claude reads it at once, held when the claude
// keeps it for the owner's approval in its terminal, and "" when the session has no socket or
// the post fails — the channel push and the next prompt take it from there. The session's
// messaging token goes nowhere but the socket's wire.
func (s *server) postInbox(ctx context.Context, id int64, in hub.MessageIn) string {
	socket, err := s.registry.InboxSocket(ctx, in.SessionID)
	if err != nil || socket == "" {
		return ""
	}
	m := ccinbox.Message{SessionID: in.SessionID, Text: in.Header()}
	if token, ok := s.secrets(ctx, home.Home{Root: s.o.Home}).Get(ctx, "cc-messaging:"+in.SessionID); ok {
		m.Token = token
	}
	post, cancel := context.WithTimeout(ctx, inboxPostFor)
	defer cancel()
	if err := ccinbox.Post(post, socket, m); err != nil {
		return ""
	}

	// No live pid names no sessions file: the claude that answers the socket reads the turn.
	row, err := s.registry.SessionRow(ctx, in.SessionID, s.now())
	if err != nil || row == nil || row.Pid <= 0 {
		_ = s.registry.MarkDelivered(ctx, id, hub.RouteInbox, jsfmt.ISOTime(s.now()))
		return hub.RouteInbox
	}

	// The claude reads the turn at once or holds it for the owner's approval in its terminal;
	// the sessions file tells which, read for a short window before the message counts as
	// delivered. The config dir comes from the server's own environment, or the process's when
	// the server was handed none.
	env := s.o.Environ
	if len(env) == 0 {
		env = os.Environ()
	}
	file := filepath.Join(outside.ClaudeConfigDir(environOf(env)),
		"sessions", strconv.Itoa(row.Pid)+".json")
	// The window waits on a real file, so it runs on the wall clock: a fixed clock (a test's)
	// must not hold the read open past the window.
	deadline := time.Now().Add(heldWindow)
	for {
		if ctx.Err() == nil && inboxHolds(file, in.SessionID, row.Pid) {
			if err := s.registry.MarkHeld(ctx, id, jsfmt.ISOTime(s.now())); err != nil {
				return ""
			}
			return hub.RouteHeld
		}
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(heldPoll)
	}
	_ = s.registry.MarkDelivered(ctx, id, hub.RouteInbox, jsfmt.ISOTime(s.now()))
	return hub.RouteInbox
}

// inboxHolds answers whether the sessions file says this session's claude keeps a turn for the
// owner's approval. A file names the session only with its id and its pid both matching; an
// unreadable file, a stale one and any other mood hold nothing.
func inboxHolds(file, sessionID string, pid int) bool {
	raw, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	var f inboxSession
	if err := json.Unmarshal(raw, &f); err != nil {
		return false
	}
	return f.SessionID == sessionID && f.Pid == pid && f.Status == "waiting"
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
