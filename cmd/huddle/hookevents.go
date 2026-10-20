package main

// The merged event hooks: one process per Claude Code event runs, in order, the parts
// internal/hookplan lists for it — huddle's own handler for the event, huddle's report, radar's
// hook, kanban's session start and the providers' work (SessionStart), the providers' usage line
// (SubagentStop). Every part runs under its own deadline and the deadlines of one event sum
// inside the timeout hooks.json grants it. The parts' outputs fold into one JSON object, so
// their messages never collide. A part that fails or overruns is dropped with its output; the
// next part still runs, and the exit code is 0 whatever happened: a hook never fails the session
// it runs in.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	corecli "github.com/MuhmdRaouf/huddle/internal/core/cli"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/hookplan"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hooks"
	kanbanapp "github.com/MuhmdRaouf/huddle/internal/kanban/app"
	radarhook "github.com/MuhmdRaouf/huddle/internal/radar/hook"
)

// stdinBudget bounds the one stdin read the event hook does: stdin that never closes must not
// hold the session, and every part reads the bytes that arrived.
const stdinBudget = 750 * time.Millisecond

// eventHook wires one event's entry point: it reads stdin once, runs the event's parts in plan
// order and answers with the one JSON object their outputs merge into.
func eventHook(event string) entry {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
		payload := readStdinOnce(stdinBudget)
		var outs [][]byte
		for _, part := range hookplan.Parts(event) {
			if out, ok := runPart(ctx, part, payload); ok {
				outs = append(outs, out)
			}
		}
		if merged := mergeHookOutputs(event, outs); merged != nil {
			_, _ = stdout.Write(merged)
		}
		return 0
	}
}

// hookWorld is the world the huddle handler parts run in: the process's own. A test swaps it to
// hand the parts a world whose Bin names the built binary, so a hub the hook's autostart starts
// is a real huddle the test runs and stops — never this test binary, which a detached `serve`
// would re-run as its own suite, outliving the run.
var hookWorld = func() *cli.World { return &cli.World{} }

// hookSpecificOutput is the context half of the merged answer.
type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// mergedHookOutput is the one object the event hook answers with: a systemMessage for the user,
// context for Claude, either or both.
type mergedHookOutput struct {
	SystemMessage      string              `json:"systemMessage,omitempty"`
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// hookEventName is the event name Claude Code knows ("SessionStart") for a plan word
// ("session-start"): every word is the lowercase, hyphenated form of the event's name.
func hookEventName(event string) string {
	parts := strings.Split(event, "-")
	for i, part := range parts {
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

// mergeHookOutputs folds the parts' outputs into the one object the event answers with: a part's
// JSON line contributes its systemMessage and its hookSpecificOutput.additionalContext (each set
// joined with one newline), any other line joins the context as the text it is. Nothing at all
// comes back when every part was silent.
func mergeHookOutputs(event string, outs [][]byte) []byte {
	var messages, context []string
	for _, out := range outs {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var spoken struct {
				SystemMessage string              `json:"systemMessage"`
				HookSpecific  *hookSpecificOutput `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(line), &spoken); err != nil {
				context = append(context, line)
				continue
			}
			if spoken.SystemMessage != "" {
				messages = append(messages, spoken.SystemMessage)
			}
			if spoken.HookSpecific != nil && spoken.HookSpecific.AdditionalContext != "" {
				context = append(context, spoken.HookSpecific.AdditionalContext)
			}
		}
	}
	if len(messages) == 0 && len(context) == 0 {
		return nil
	}
	merged := mergedHookOutput{SystemMessage: strings.Join(messages, "\n")}
	if len(context) > 0 {
		merged.HookSpecificOutput = &hookSpecificOutput{
			HookEventName:     hookEventName(event),
			AdditionalContext: strings.Join(context, "\n"),
		}
	}
	data, err := domain.MarshalCompactJSON(merged)
	if err != nil {
		return nil
	}
	// As invocation.JSON: the state-file marshaler's trailing newline is trimmed; the one the
	// hook protocol reads is added here.
	return append(data, '\n')
}

// readStdinOnce reads all of stdin but gives up once the budget passes: partial input waits for
// nothing, and the session keeps moving.
func readStdinOnce(budget time.Duration) []byte {
	read := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(os.Stdin)
		read <- data
	}()
	select {
	case data := <-read:
		return data
	case <-time.After(budget):
		return nil
	}
}

// runPart runs one part under its own deadline and answers its output. A part that overruns its
// deadline is dropped — its goroutine keeps its buffer, the event never reads it — and the next
// part runs all the same.
func runPart(ctx context.Context, part hookplan.Part, payload []byte) ([]byte, bool) {
	out := &bytes.Buffer{}
	done := make(chan struct{})
	pctx, cancel := context.WithTimeout(ctx, part.Budget)
	defer cancel()
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				// one part's panic never takes the process down: one line in hooks.log, like
				// any hook failure
				hooks.LogHookError(pctx, &cli.World{}, part.Name, fmt.Errorf("%v", r))
			}
		}()
		runNamedPart(pctx, part.Name, bytes.NewReader(payload), out)
	}()
	select {
	case <-done:
		return out.Bytes(), true
	case <-pctx.Done():
		return nil, false
	}
}

// runNamedPart runs one part by its plan name: the huddle handlers in process, radar's hook,
// kanban's session start through its own word, and the providers' work through the core CLI's
// in-process hook passes. A part stays silent on any failure; stderr goes nowhere, because a hook
// never speaks to the session it observes.
func runNamedPart(ctx context.Context, name string, in io.Reader, out io.Writer) {
	switch name {
	case "session-start":
		hooks.SessionStart(ctx, hookWorld(), in, out)
	case "listen":
		hooks.Listen(ctx, hookWorld(), in, out)
	case "reserve":
		hooks.Reserve(ctx, hookWorld(), in, out)
	case "stop":
		hooks.Stop(ctx, hookWorld(), in, out)
	case "report":
		hooks.Report(ctx, hookWorld(), in, out)
	case "radar":
		radarhook.Main(ctx, in, out, os.Environ(), time.Now, int64(os.Getpid()), int64(os.Getppid()))
	case "kanban":
		kanbanapp.Run([]string{"hook", "session-start"}, out, io.Discard, version)
	case "providers":
		// The five provider plugins' session-start work, one process for them all: each
		// provider's output lands as its own line, and the merge above folds them into the one
		// object the event answers with.
		for _, spoken := range corecli.ProvidersSessionStart(ctx, envOf(os.Environ())) {
			writePartOutput(out, spoken)
		}
	case "usage":
		payload, _ := io.ReadAll(in)
		for _, spoken := range corecli.ProvidersSubagentStop(ctx, envOf(os.Environ()), payload) {
			writePartOutput(out, spoken)
		}
	}
}

// writePartOutput writes one provider's captured output as its own line.
func writePartOutput(out io.Writer, spoken []byte) {
	_, _ = out.Write(bytes.TrimRight(spoken, "\n"))
	_, _ = out.Write([]byte("\n"))
}
