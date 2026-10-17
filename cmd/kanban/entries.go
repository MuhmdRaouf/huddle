package main

import (
	"context"
	"io"

	"github.com/MuhmdRaouf/huddle/internal/kanban/cli"
)

// usage is the line the CLI prints for an unknown command.
const usage = "usage: kanban <command> [flags]; run kanban help for the list"

// entry runs one subcommand: args are the arguments after the subcommand name,
// and the returned int is the process exit code.
type entry = func(ctx context.Context, args []string, stdout, stderr io.Writer) int

// entries holds one function per subcommand: the board commands the cli runs
// today, and the server entries and hook later cards replace.
var entries = map[string]entry{
	"init":        cliEntry("init"),
	"new":         cliEntry("new"),
	"promote":     cliEntry("promote"),
	"pull":        cliEntry("pull"),
	"review":      cliEntry("review"),
	"reject":      cliEntry("reject"),
	"test":        cliEntry("test"),
	"done":        cliEntry("done"),
	"block":       cliEntry("block"),
	"unblock":     cliEntry("unblock"),
	"set":         cliEntry("set"),
	"move":        cliEntry("move"),
	"log":         cliEntry("log"),
	"list":        cliEntry("list"),
	"show":        cliEntry("show"),
	"status":      cliEntry("status"),
	"check":       cliEntry("check"),
	"lint":        cliEntry("lint"),
	"epoch":       cliEntry("epoch"),
	"stop-line":   cliEntry("stop-line"),
	"resume-line": cliEntry("resume-line"),
	"start":       cliEntry("start"),
	"stop":        cliEntry("stop"),
	"open":        cliEntry("open"),
	"refresh":     cliEntry("refresh"),
	"export":      cliEntry("export"),
	"export-md":   cliEntry("export-md"),
	"migrate-db":  cliEntry("migrate-db"),
	"serve":       cliEntry("serve"),
	"hook":        cliEntry("hook"),
	"help":        cliEntry("help"),
	"--help":      cliEntry("--help"),
	"-h":          cliEntry("-h"),
}

// cliEntry runs one subcommand, the command word back at the front of the argv
// the cli parses.
func cliEntry(name string) entry {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) int {
		return cli.Run(ctx, cli.DefaultWorld(version), append([]string{name}, args...), stdout, stderr)
	}
}
