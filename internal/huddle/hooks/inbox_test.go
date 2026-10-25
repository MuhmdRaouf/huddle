package hooks

// Where a session's inbox is: Claude Code hands every hook the session's messaging socket in
// CLAUDE_CODE_MESSAGING_SOCKET and writes the same socket into <Claude config dir>/sessions/
// <pid>.json. inboxSocketOf answers the socket a session's report carries: the env wins, the
// sessions file is the fallback, and the file counts only when its sessionId and pid both name
// the session asking. Nothing else in that directory is read.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// writeSessionsFile lays one file under <configDir>/sessions/ the way Claude Code writes it:
// <name>.json holds a body naming pid, sessionId and the socket, beside a field the reader
// knows nothing of.
func writeSessionsFile(t *testing.T, configDir string, name, bodyPid int, sessionID, socket string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"pid":                 bodyPid,
		"sessionId":           sessionID,
		"messagingSocketPath": socket,
		"name":                "an inbox",
	})
	if err != nil {
		t.Fatalf("marshal the sessions file: %v", err)
	}
	dir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("lay the sessions dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(name)+".json"), body, 0o600); err != nil {
		t.Fatalf("write the sessions file: %v", err)
	}
}

// TestTheSocketFallsBackToTheSessionsFile pins the socket rule: the env's socket wins without
// a file read; without it, sessions/<pid>.json answers only when its sessionId and pid both
// name the session asking; nothing anywhere answers none.
func TestTheSocketFallsBackToTheSessionsFile(t *testing.T) {
	none := func(string) string { return "" }
	if got := inboxSocketOf(none, t.TempDir(), 4242, "s-1"); got != "" {
		t.Errorf("no env and no file answers %q, want none", got)
	}
	env := func(key string) string {
		if key == "CLAUDE_CODE_MESSAGING_SOCKET" {
			return "/tmp/env.sock"
		}
		return ""
	}
	if got := inboxSocketOf(env, t.TempDir(), 4242, "s-1"); got != "/tmp/env.sock" {
		t.Errorf("with the env set the socket = %q, want the env's, no file read", got)
	}
	dir := t.TempDir()
	writeSessionsFile(t, dir, 4242, 4242, "s-1", "/tmp/x.sock")
	if got := inboxSocketOf(none, dir, 4242, "s-1"); got != "/tmp/x.sock" {
		t.Errorf("the sessions file's socket = %q, want /tmp/x.sock", got)
	}
	// the file must name this session, not merely sit under this pid's name
	writeSessionsFile(t, dir, 4242, 4242, "s-other", "/tmp/x.sock")
	if got := inboxSocketOf(none, dir, 4242, "s-1"); got != "" {
		t.Errorf("a file naming another session answers %q, want none", got)
	}
	writeSessionsFile(t, dir, 4242, 5151, "s-1", "/tmp/x.sock")
	if got := inboxSocketOf(none, dir, 4242, "s-1"); got != "" {
		t.Errorf("a file naming another pid answers %q, want none", got)
	}
	if got := inboxSocketOf(none, t.TempDir(), 4242, "s-1"); got != "" {
		t.Errorf("a config dir with no sessions file answers %q, want none", got)
	}
}
