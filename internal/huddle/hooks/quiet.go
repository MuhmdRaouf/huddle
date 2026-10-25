// The hooks that run inside every Claude Code session this plugin serves. A hook never breaks
// the session it runs in: whatever its body does, the failure is one line in hooks.log (the
// project's Huddle home when it exists, else the temp dir) and the process answers 0 in silence.
//
// Every hook reads the same hook JSON on stdin — each field optional, input that is not JSON
// counts as none — and answers with one JSON line: context under hookSpecificOutput, or, for the
// Stop hook alone, a decision.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The deadlines each hook grants itself, in milliseconds. Together they stay at most 70 % inside
// the timeout hooks.json grants the event, with a full second to spare: a hook is dead to Claude
// Code at its timeout, and one that answers just under it is indistinguishable from one that
// never answered.
const (
	approveStdinMS = 300
	approveFetchMS = 600
	listenStdinMS  = 500
	listenTouchMS  = 1000
	listenFeedMS   = 1500
	startStdinMS   = 500
	startJoinMS    = 2500
	startBudgetMS  = 3000
	stopStdinMS    = 500
	stopInboxMS    = 5000
	taskStdinMS    = 500
	taskSendMS     = 1000
)

// hookLogCap caps hooks.log: a hook that fails on every event must not grow it without end.
const hookLogCap = 256 * 1024

// getenvOf reads one variable of the world the hook runs in; a world without its own reader
// reads the process's.
func getenvOf(w *cli.World, key string) string {
	if w == nil || w.Getenv == nil {
		return os.Getenv(key)
	}
	return w.Getenv(key)
}

// nowOf is the world's clock; a world without its own reads the wall's.
func nowOf(w *cli.World) time.Time {
	if w == nil || w.Now == nil {
		return time.Now()
	}
	return w.Now()
}

// Run runs one hook's body: an error it returns (or a panic it raises) becomes one line in
// hooks.log and nothing else.
func Run(ctx context.Context, w *cli.World, hook string, body func() error) {
	defer func() {
		if r := recover(); r != nil {
			LogHookError(ctx, w, hook, fmt.Errorf("%v", r))
		}
	}()
	if err := body(); err != nil {
		LogHookError(ctx, w, hook, err)
	}
}

// LogHookError appends one line for a hook that failed, capping hooks.log at 256 KB. The log is
// a courtesy: when even it cannot be written, silence is right.
func LogHookError(ctx context.Context, w *cli.World, hook string, err error) {
	dir := os.TempDir()
	if home := cli.Home(ctx, w); home != "" {
		if _, statErr := os.Stat(home); statErr == nil {
			dir = home
		}
	}
	file := filepath.Join(dir, "hooks.log")
	if info, statErr := os.Stat(file); statErr == nil && info.Size() > hookLogCap {
		_ = os.WriteFile(file, nil, 0o644)
	}
	log, openErr := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if openErr != nil {
		return
	}
	defer log.Close()
	_, _ = fmt.Fprintf(log, "%s %s: %s\n", jsfmt.ISOTime(nowOf(w)), hook, err.Error())
}

// hookInput is what Claude Code sends a hook on stdin; every field is optional and absent input
// leaves it zero.
type hookInput struct {
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	AgentID        string `json:"agent_id"`
	StopHookActive bool   `json:"stop_hook_active"`
	ToolName       string `json:"tool_name"`
	Cwd            string `json:"cwd"`
	ToolInput      struct {
		Command      string `json:"command"`
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"tool_input"`
}

// readInput reads the hook's stdin as JSON into dst, giving up after ms: a hook never waits on
// its caller. Empty input, input that is not JSON, or a field of the wrong shape leaves dst as it
// was (encoding/json keeps every field it could decode before the failure).
func readInput(in io.Reader, ms int, dst any) {
	text := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(in)
		text <- string(raw)
	}()
	select {
	case got := <-text:
		if got == "" {
			return
		}
		_ = json.Unmarshal([]byte(got), dst)
	case <-time.After(time.Duration(ms) * time.Millisecond):
	}
}

// contextOutput is the one answer a hook that adds context gives: text under the event's name.
type contextOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// writeContext prints one hook's context for Claude Code. event is the hook event the input
// named, or the one the hook serves when it named none.
func writeContext(out io.Writer, event, text string) {
	if event == "" {
		event = "PostToolUse"
	}
	var output contextOutput
	output.HookSpecificOutput.HookEventName = event
	output.HookSpecificOutput.AdditionalContext = text
	writeJSON(out, output)
}

// writeJSON prints one value as the hook's single JSON answer line.
func writeJSON(out io.Writer, value any) {
	raw, err := domain.MarshalCompactJSON(value)
	if err != nil {
		return
	}
	_, _ = out.Write(append(raw, '\n'))
}
