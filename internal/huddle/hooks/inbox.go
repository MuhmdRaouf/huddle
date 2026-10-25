package hooks

// Where a session's inbox is and what rides with it: Claude Code hands every hook the session's
// messaging socket in CLAUDE_CODE_MESSAGING_SOCKET, writes the same socket into
// <Claude config dir>/sessions/<pid>.json, and hands a messaging token in
// CLAUDE_CODE_MESSAGING_TOKEN at the session's start. The report carries the socket for the hub
// to keep on the session's row; the token goes to the secret store under the session's own
// account, which the session's end empties. A store that refuses, hangs or is missing costs the
// token alone — never the report, never the hook's silence.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
)

// inboxSocketOf answers the messaging socket a session's hooks were handed: the env wins, else
// the sessions file Claude Code wrote for the pid counts when its sessionId and pid both name the
// session asking — a file that merely sits under the pid's name is never trusted. Nothing else in
// that directory is read.
func inboxSocketOf(env func(string) string, configDir string, pid int, sessionID string) string {
	if socket := env("CLAUDE_CODE_MESSAGING_SOCKET"); socket != "" {
		return socket
	}
	if configDir == "" || pid == 0 || sessionID == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(configDir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return ""
	}
	// the three fields that name a session's inbox, and nothing else of the file
	var file struct {
		MessagingSocketPath string `json:"messagingSocketPath"`
		SessionID           string `json:"sessionId"`
		Pid                 int    `json:"pid"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return ""
	}
	if file.SessionID != sessionID || file.Pid != pid || file.MessagingSocketPath == "" {
		return ""
	}
	return file.MessagingSocketPath
}

// inboxConfigDir is the Claude config dir the sessions file sits under, read from the world's
// environment.
func inboxConfigDir(env func(string) string) string {
	return outside.ClaudeConfigDir(map[string]string{
		"HOME":              env("HOME"),
		"CLAUDE_CONFIG_DIR": env("CLAUDE_CONFIG_DIR"),
	})
}

// messagingAccount is the secret-store account one session's messaging token lives under. The
// store refuses an account that is not its name shape, so a session id that does not fit stores
// nothing — the token waits for a start whose id does.
func messagingAccount(sessionID string) string {
	return "cc-messaging:" + sessionID
}

// secretDeleter is the half of the secret store a session's end needs: a store without it holds
// the token until it is told otherwise, and the hook says nothing.
type secretDeleter interface {
	Delete(ctx context.Context, account string) error
}

// keepInboxToken hands the session's messaging token to the secret store at the session's start
// and takes it back out at its end, so only a live session holds one. Both moves sit inside the
// report's deadline, and whatever the store says no to costs nothing but the token.
func keepInboxToken(ctx context.Context, w *cli.World, env func(string) string, event, sessionID string, left func() int) {
	root := cli.Home(ctx, w)
	if root == "" {
		return
	}
	switch event {
	case "SessionStart":
		token := env("CLAUDE_CODE_MESSAGING_TOKEN")
		if token == "" {
			return
		}
		bounded, cancel := context.WithTimeout(ctx, time.Duration(left())*time.Millisecond)
		defer cancel()
		_ = w.SecretsOf(bounded, home.Home{Root: root}).Set(bounded, messagingAccount(sessionID), token)
	case "SessionEnd":
		bounded, cancel := context.WithTimeout(ctx, time.Duration(left())*time.Millisecond)
		defer cancel()
		if deleter, ok := w.SecretsOf(bounded, home.Home{Root: root}).(secretDeleter); ok {
			_ = deleter.Delete(bounded, messagingAccount(sessionID))
		}
	}
}

// envOf answers one variable of the world.
func envOf(w *cli.World) func(string) string {
	if w != nil && w.Getenv != nil {
		return w.Getenv
	}
	return os.Getenv
}
