package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// safetyDeadline bounds the stdin read: stdin that never closes must not hold the session
// (UserPromptSubmit runs inline with the prompt flow), so the hook gives up and exits 0 well
// within a second.
const safetyDeadline = 750 * time.Millisecond

// Main runs one hook invocation and returns its exit code, always 0: a hook never fails the
// session it observes. It reads stdin, records one spool line, and maybe starts the dashboard
// server; every rule the line follows lives in record.go.
func Main(ctx context.Context, stdin io.Reader, stdout io.Writer, environ []string, now func() time.Time, pid, ppid int64) int {
	return runEntry(ctx, stdin, stdout, environ, now, pid, ppid, autostart)
}

// runEntry is Main with the dashboard start injected, so the entry's decision runs under test
// without spawning anything.
func runEntry(ctx context.Context, stdin io.Reader, stdout io.Writer, environ []string, now func() time.Time, pid, ppid int64, startServer func() bool) int {
	hookIO := MakeIo(environ, now, pid, ppid)
	event, _ := RunHook(ctx, readStdin(stdin), &hookIO)
	if message := sessionStartMessage(ctx, &hookIO, event, startServer); message != "" {
		fmt.Fprintf(stdout, "%s\n", systemMessage(message))
	}
	return 0
}

// sessionStartMessage is the one line a SessionStart that started the dashboard owes the user:
// the saved port's address once the server keeps one, else a pointer at the status command.
// Empty when it owes nothing — not a SessionStart, autostart off, a server already running, or
// a start that never launched (the user sees and Claude's context never gets the message).
func sessionStartMessage(ctx context.Context, hookIO *HookIo, event string, startServer func() bool) string {
	if event != "SessionStart" || !paths.WantsAutostart(ctx, hookIO.Env) || serverRunning(ctx, hookIO.Env) {
		return ""
	}
	if !startServer() {
		return ""
	}
	if port, ok := paths.SavedPort(ctx, hookIO.Env); ok {
		return fmt.Sprintf("radar: dashboard at http://127.0.0.1:%d", port)
	}
	return "radar: starting the dashboard (/radar:status shows its address)"
}

// systemMessage is the line wrapped in the one-object JSON a systemMessage rides in on.
func systemMessage(line string) string {
	message, err := domain.MarshalCompactJSON(struct {
		SystemMessage string `json:"systemMessage"`
	}{SystemMessage: line})
	if err != nil {
		return ""
	}
	return string(message)
}

// readStdin reads all of stdin but gives up once the deadline passes; partial data waits for
// nothing (an empty payload records an unknown event) and the session keeps moving.
func readStdin(stdin io.Reader) string {
	read := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(stdin)
		read <- string(data)
	}()
	select {
	case data := <-read:
		return data
	case <-time.After(safetyDeadline):
		return ""
	}
}

// serverRunning reports whether a server recorded in server.json has a live process (no
// network: the hook stays fast).
func serverRunning(ctx context.Context, env domain.EnvLookup) bool {
	data, err := os.ReadFile(paths.ServerInfoPath(ctx, env))
	if err != nil {
		return false
	}
	var info struct {
		PID int64 `json:"pid"`
	}
	if json.Unmarshal(data, &info) != nil || info.PID <= 0 {
		return false
	}
	return process.IsAlive(info.PID)
}

// autostart starts the dashboard server detached; true when a start was launched. The child
// takes the same radar word this hook runs under — the binary routes every radar command line
// through it. Any failure is silent: never let observability break the session it observes.
func autostart() bool {
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	return process.StartDetached(executable, []string{"radar", "start"}, process.DetachedOptions{}).Pid != 0
}
