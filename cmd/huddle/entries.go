package main

import (
	"context"
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
// server and the six hooks hooks.json runs. Every CLI subcommand and any operation by name go
// through the CLI (run hands it the whole argv), which reads one line.
var entries = map[string]entry{
	// The MCP server and the HTTP server.
	"mcp":   mcpEntry,
	"serve": serveEntry,
	// The hooks: a hook never fails the session it runs in, so each stays silent and returns 0
	// whatever happens.
	"hook session-start": hookEntry(hooks.SessionStart),
	"hook approve":       hookEntry(hooks.Approve),
	"hook listen":        hookEntry(hooks.Listen),
	"hook permit":        hookEntry(hooks.Permit),
	"hook stop":          hookEntry(hooks.Stop),
	"hook report":        hookEntry(hooks.Report),
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
