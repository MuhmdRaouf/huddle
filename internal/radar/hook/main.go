package hook

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// safetyDeadline bounds the stdin read: stdin that never closes must not hold the session
// (UserPromptSubmit runs inline with the prompt flow), so the hook gives up and exits 0 well
// within a second.
const safetyDeadline = 750 * time.Millisecond

// Main runs one hook invocation and returns its exit code, always 0: a hook never fails the
// session it observes. It reads stdin, records one spool line, and on SessionStart maybe points
// at the hub's radar page; it starts no server of its own. Every rule the line follows lives in
// record.go.
func Main(ctx context.Context, stdin io.Reader, stdout io.Writer, environ []string, now func() time.Time, pid, ppid int64) int {
	return runEntry(ctx, stdin, stdout, environ, now, pid, ppid, HubLookup(environ))
}

// runEntry is Main with the hub lookup injected, so the entry's decision runs under test
// without touching the network.
func runEntry(ctx context.Context, stdin io.Reader, stdout io.Writer, environ []string, now func() time.Time, pid, ppid int64, hub func(ctx context.Context) (string, bool)) int {
	hookIO := MakeIo(environ, now, pid, ppid)
	event, _ := RunHook(ctx, readStdin(stdin), &hookIO)
	if message := sessionStartMessage(ctx, &hookIO, event, hub); message != "" {
		fmt.Fprintf(stdout, "%s\n", systemMessage(message))
	}
	return 0
}

// sessionStartMessage is the one line a SessionStart owes the user: where radar lives in the
// running huddle hub. Empty when it owes nothing — not a SessionStart, no hub, or autostart
// off. With no hub the hook starts nothing and says nothing: the hub is the one server this
// edition runs, and the user reaches it through /huddle:setup.
func sessionStartMessage(ctx context.Context, hookIO *HookIo, event string, hub func(ctx context.Context) (string, bool)) string {
	if event != "SessionStart" {
		return ""
	}
	base, ok := hub(ctx)
	if !ok {
		return ""
	}
	// the hub serves radar in process. With autostart off the user asked for
	// nothing — no line, nothing changed.
	if paths.WantsAutostart(ctx, hookIO.Env) {
		return fmt.Sprintf("radar: in the huddle hub at %s/#/radar", base)
	}
	return ""
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
