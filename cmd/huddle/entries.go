package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hooks"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// entry runs one subcommand: args are the arguments after the subcommand name, and the returned
// int is the process exit code.
type entry = func(ctx context.Context, args []string, stdout, stderr io.Writer) int

// entries holds one function per entry point this binary runs itself: the MCP server, the HTTP
// server, the two decision hooks that keep their own hook lines, and one entry per Claude Code
// event, whose one process runs every part internal/hookplan lists for it. Every CLI subcommand
// and any operation by name go through the CLI (run hands it the whole argv), which reads one
// line.
var entries = map[string]entry{
	// The MCP server and the HTTP server.
	"mcp":   mcpEntry,
	"serve": serveEntry,
	// The decision hooks, still their own lines (approve for Bash, permit for the prompt): each
	// stays silent and returns 0 whatever happens.
	"hook approve": hookEntry(hooks.Approve),
	"hook permit":  hookEntry(hooks.Permit),
	// One entry per event, one process per event: the parts run in plan order and the exit code
	// is 0 whatever happened. The hidden word another agent's hook runs (`hook external`) is
	// dispatched in run(), outside this table: it serves no hooks.json line.
	"hook session-start":      eventHook("session-start"),
	"hook user-prompt-submit": eventHook("user-prompt-submit"),
	"hook pre-tool-use":       eventHook("pre-tool-use"),
	"hook post-tool-use":      eventHook("post-tool-use"),
	"hook notification":       eventHook("notification"),
	"hook permission-request": eventHook("permission-request"),
	"hook stop":               eventHook("stop"),
	"hook subagent-start":     eventHook("subagent-start"),
	"hook subagent-stop":      eventHook("subagent-stop"),
	"hook session-end":        eventHook("session-end"),
	"hook pre-compact":        eventHook("pre-compact"),
}

// serveEntry runs the HTTP server in the foreground, as the detached server `huddle up` starts
// and as a hand start does. It lays the home's tree out first: a server start is an entry that
// owns the home. The process environment rides along for the radar tab (RADAR_HOME, the
// transcripts' config dir, the providers' state dirs).
func serveEntry(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	options := server.OptionsFromEnv(version, os.Getenv, stdout, stderr, os.Stdin)
	if options.Home != "" {
		if err := (home.Home{Root: options.Home}).Ensure(ctx); err != nil {
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 1
		}
		refreshInstalledService(ctx, options.Home)
	}
	options.Environ = os.Environ()
	options.Signals = true
	if err := server.Serve(ctx, options); err != nil {
		var held *server.HeldError
		if errors.As(err, &held) {
			// a duplicate start is not a failure: the home's server is already up
			fmt.Fprintf(stderr, "huddle: %v\n", err)
			return 0
		}
		fmt.Fprintf(stderr, "huddle: %v\n", err)
		return 1
	}
	return 0
}

// mcpEntry is the stdio MCP bridge Claude Code spawns: line-delimited JSON-RPC on stdin and
// stdout, the channel's events pushed as claude/channel notifications.
func mcpEntry(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	w := &cli.World{}
	return cli.McpStdio(ctx, w, version, os.Stdin, stdout, stderr)
}

// refreshInstalledService keeps an installed service pointing at the binary that runs: an update
// replaces the versioned binary the unit file names, and the next entry re-renders it. Silent
// and bounded, like everything an entry does beside its own work.
func refreshInstalledService(ctx context.Context, root string) {
	bin, err := os.Executable()
	if err != nil {
		return
	}
	outside.RefreshServiceIfStale(ctx, outside.ServiceOptions{
		Home: root, Bin: bin, UID: os.Getuid(), Runner: outside.CommandRunner{},
	})
}

// hookEntry wires one hook's entry point: it runs against the process's own environment and
// stdin, stays silent on any failure and always answers 0 — a hook never fails the session it
// runs in.
func hookEntry(hook func(context.Context, *cli.World, io.Reader, io.Writer)) entry {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
		hook(ctx, &cli.World{}, os.Stdin, stdout)
		return 0
	}
}

// externalEntry runs the hidden word another agent's hook runs:
// `huddle hook external --harness <name> --event <event>`, the agent's own payload on stdin.
// The answer is silence with exit 0 whatever happened: a hook never blocks the agent that ran
// it, so a wrong flag or an unknown harness changes nothing.
func externalEntry(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	name, event := externalArgs(args)
	hooks.External(ctx, &cli.World{}, name, event, os.Stdin, stdout)
	return 0
}

// externalArgs reads the word's two flags; anything else on the line is ignored.
func externalArgs(args []string) (name, event string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--harness":
			if i+1 < len(args) {
				i++
				name = args[i]
			}
		case "--event":
			if i+1 < len(args) {
				i++
				event = args[i]
			}
		}
	}
	return name, event
}
