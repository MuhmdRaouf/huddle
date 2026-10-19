package main

// The merged event hooks: one process per Claude Code event runs, in order, the parts
// internal/hookplan lists for it — huddle's own handler for the event, huddle's report, radar's
// hook, kanban's session start (SessionStart only) — where the plugin used to start one process
// per part. Every part runs under its own deadline and the deadlines of one event sum inside the
// timeout hooks.json grants it. A part that fails or overruns is dropped with its output; the
// next part still runs, and the exit code is 0 whatever happened: a hook never fails the session
// it runs in.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/hookplan"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hooks"
	kanbanapp "github.com/MuhmdRaouf/huddle/internal/kanban/app"
	radarhook "github.com/MuhmdRaouf/huddle/internal/radar/hook"
)

// stdinBudget bounds the one stdin read the event hook does: stdin that never closes must not
// hold the session, and every part reads the bytes that arrived.
const stdinBudget = 750 * time.Millisecond

// eventHook wires one event's entry point: it reads stdin once and runs the event's parts in
// plan order, concatenating in the old order whatever output a finished part has for Claude.
func eventHook(event string) entry {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
		payload := readStdinOnce(stdinBudget)
		for _, part := range hookplan.Parts(event) {
			if out, ok := runPart(ctx, part, payload); ok {
				_, _ = stdout.Write(out)
			}
		}
		return 0
	}
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

// runNamedPart runs one part by its plan name: the huddle handlers in process, radar's hook, and
// kanban's session start through its own word. A part stays silent on any failure; stderr goes
// nowhere, because a hook never speaks to the session it observes.
func runNamedPart(ctx context.Context, name string, in io.Reader, out io.Writer) {
	switch name {
	case "session-start":
		hooks.SessionStart(ctx, &cli.World{}, in, out)
	case "listen":
		hooks.Listen(ctx, &cli.World{}, in, out)
	case "reserve":
		hooks.Reserve(ctx, &cli.World{}, in, out)
	case "stop":
		hooks.Stop(ctx, &cli.World{}, in, out)
	case "report":
		hooks.Report(ctx, &cli.World{}, in, out)
	case "radar":
		radarhook.Main(ctx, in, out, os.Environ(), time.Now, int64(os.Getpid()), int64(os.Getppid()))
	case "kanban":
		kanbanapp.Run([]string{"hook", "session-start"}, out, io.Discard, version)
	}
}
