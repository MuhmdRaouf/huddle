// Package ccinbox posts one user turn to a Claude Code session's inbox socket. The socket never
// answers: a post is written and the connection closed; whether Claude took the turn is read
// from the transcript or the registry by the caller.
package ccinbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
)

// MaxTextBytes is the largest text a session reads from its inbox; a longer turn is refused
// before the dial.
const MaxTextBytes = 1 << 20

// Message is one user turn for a session.
type Message struct {
	SessionID string // the session's own id: Claude drops a line whose id is not its own
	Text      string // the turn, non-empty
	Token     string // the session's messaging token; set, an auth line goes first
}

// NoSocket says nothing listens at the socket path; it wraps the dial error.
type NoSocket struct {
	Path string
	Err  error
}

func (e *NoSocket) Error() string { return "no inbox socket at " + e.Path }

func (e *NoSocket) Unwrap() error { return e.Err }

// TooLong says the text is over MaxTextBytes.
type TooLong struct {
	Bytes int
}

func (e *TooLong) Error() string {
	return fmt.Sprintf("inbox text is %d bytes, over the %d byte limit", e.Bytes, MaxTextBytes)
}

// Invalid says a message field that must be set is empty.
type Invalid struct {
	Field string
}

func (e *Invalid) Error() string { return "inbox message needs a non-empty " + e.Field }

// wireLine is one user turn on the wire, one line per post.
type wireLine struct {
	Type      string      `json:"type"`
	SessionID string      `json:"session_id"`
	Message   wireMessage `json:"message"`
}

// wireAuthLine proves the poster holds the session's messaging token; it goes before the user
// line whenever the post carries a token.
type wireAuthLine struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type wireMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Post writes the message as one JSON line to the socket at path and closes the connection.
// A message carrying a token writes its auth line first, in the same write. It returns nil
// once the write is done, *NoSocket when nothing listens at path, *TooLong when the text is
// over the limit, and ctx's error when the write does not finish in time.
func Post(ctx context.Context, path string, m Message) error {
	if m.SessionID == "" {
		return &Invalid{Field: "SessionID"}
	}
	if m.Text == "" {
		return &Invalid{Field: "Text"}
	}
	if len(m.Text) > MaxTextBytes {
		return &TooLong{Bytes: len(m.Text)}
	}

	line, err := json.Marshal(wireLine{
		Type:      "user",
		SessionID: m.SessionID,
		Message:   wireMessage{Role: "user", Content: m.Text},
	})
	if err != nil {
		return fmt.Errorf("encode inbox line: %w", err)
	}
	line = append(line, '\n')

	// One write carries the auth line and the user line together, so a peeking reader never
	// sees an authenticated-but-empty turn.
	wire := line
	if m.Token != "" {
		auth, err := json.Marshal(wireAuthLine{Type: "auth", Token: m.Token})
		if err != nil {
			return fmt.Errorf("encode inbox auth line: %w", err)
		}
		auth = append(auth, '\n')
		wire = make([]byte, 0, len(auth)+len(line))
		wire = append(wire, auth...)
		wire = append(wire, line...)
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("dial inbox socket at %s: %w", path, ctxErr)
		}
		return &NoSocket{Path: path, Err: err}
	}
	defer conn.Close()

	// The connection outlives the dial, so the context reaches the write only through the
	// deadline set from it here.
	var bounded bool
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return fmt.Errorf("set write deadline on %s: %w", path, err)
		}
		bounded = true
	}
	if _, err := conn.Write(wire); err != nil {
		// The write deadline was set from ctx, so its expiry is ctx's deadline, whichever
		// timer was observed first.
		if bounded && errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("write to inbox socket at %s: %w", path, context.DeadlineExceeded)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("write to inbox socket at %s: %w", path, ctxErr)
		}
		return fmt.Errorf("write to inbox socket at %s: %w", path, err)
	}
	return nil
}
