package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/cli"
	"github.com/MuhmdRaouf/huddle/internal/radar/hook"
)

// usage is the line the CLI prints for an unknown command.
const usage = "usage: radar start|stop|status|url|open|watch [--port N] [--since 24h]"

// entry runs one subcommand: args are the arguments after the subcommand name, and the returned
// int is the process exit code.
type entry = func(ctx context.Context, args []string, stdout, stderr io.Writer) int

// entries holds one function per entry point: the hook and every subcommand the CLI accepts.
var entries = map[string]entry{
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

// hookEntry is the hook entry point: read stdin, record one spool line, maybe start the
// dashboard. A hook never fails Claude Code, so it stays silent and returns 0 whatever happens.
func hookEntry(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return hook.Main(ctx, os.Stdin, stdout, os.Environ(), time.Now, int64(os.Getpid()), int64(os.Getppid()))
}

// cliEntry runs one CLI subcommand, the command back at the front of the argv it parses.
func cliEntry(name string) entry {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
		return cli.RunCli(ctx, append([]string{name}, args...), cli.RealDeps(version, stdout, stderr))
	}
}
