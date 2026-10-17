// Package app is radar's command line under the huddle binary: one dispatch over its entry
// points (the CLI and the hook), each behind the subcommand the plugin invokes it with —
// `huddle radar start`, `huddle radar hook`, and so on.
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/cli"
	"github.com/MuhmdRaouf/huddle/internal/radar/hook"
)

// usage is the line an unknown command prints, naming the word the huddle binary runs radar
// with.
const usage = "usage: huddle radar start|stop|status|url|open|watch [--port N] [--since 24h]"

// runner runs one subcommand: args are the arguments after the subcommand name, version is the
// version the binary was built with, and the returned int is the process exit code.
type runner = func(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int

// entries holds one function per entry point: the hook and every subcommand the CLI accepts.
var entries = map[string]runner{
	"hook":   hookEntry,
	"start":  cliEntry("start"),
	"stop":   cliEntry("stop"),
	"status": cliEntry("status"),
	"url":    cliEntry("url"),
	"open":   cliEntry("open"),
	"watch":  cliEntry("watch"),
	"help":   cliEntry("help"),
	"--help": cliEntry("--help"),
	"-h":     cliEntry("-h"),
}

// Run is one invocation of the radar word; args are the arguments after the word, version the
// version the binary was built with, and the returned int the process exit code.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && args[0] == "--version" {
		fmt.Fprintf(stdout, "radar %s\n", version)
		return 0
	}
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	entry, ok := entries[command]
	if !ok {
		fmt.Fprintf(stderr, "radar: unknown command: %s\n", commandName(command))
		fmt.Fprintln(stderr, usage)
		return 1
	}
	return entry(context.Background(), args[1:], stdout, stderr, version)
}

// commandName is what the unknown-command error names; an empty argv shows as "(none)".
func commandName(command string) string {
	if command == "" {
		return "(none)"
	}
	return command
}

// hookEntry is the hook entry point: read stdin, record one spool line, maybe start the
// dashboard. A hook never fails Claude Code, so it stays silent and returns 0 whatever happens.
func hookEntry(ctx context.Context, args []string, stdout, stderr io.Writer, _ string) int {
	return hook.Main(ctx, os.Stdin, stdout, os.Environ(), time.Now, int64(os.Getpid()), int64(os.Getppid()))
}

// cliEntry runs one CLI subcommand, the command back at the front of the argv it parses.
func cliEntry(name string) runner {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
		return cli.RunCli(ctx, append([]string{name}, args...), realDeps(version, stdout, stderr))
	}
}

// realDeps builds the effects a real invocation runs with, the detached start aimed at this
// word: the child this binary spawns is `huddle radar …`, so the start it follows is the same
// word the caller used.
func realDeps(version string, stdout, stderr io.Writer) cli.Deps {
	deps := cli.RealDeps(version, stdout, stderr)
	spawn := deps.SpawnDetached
	deps.SpawnDetached = func(args []string) int64 {
		return spawn(append([]string{"radar"}, args...))
	}
	return deps
}
