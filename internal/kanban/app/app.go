// Package app is kanban's command line under the huddle binary: one dispatch
// over its entry points — every board command, plus the server entries and the
// hook — each behind the word the plugin invokes it with, `huddle kanban …`.
package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	huddlecli "github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/cli"
)

// usage is the line an unknown command prints, naming the word the huddle
// binary runs kanban with.
const usage = "usage: huddle kanban <command> [flags]; run huddle kanban help for the list"

// runner runs one subcommand: args are the arguments after the subcommand
// name, version is the version the binary was built with, and the returned int
// is the process exit code.
type runner = func(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int

// entries holds one function per subcommand: the board commands the cli runs
// today, and the server entries and hook.
var entries = map[string]runner{
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

// Run is one invocation of the kanban word; args are the arguments after the
// word, version the version the binary was built with, and the returned int
// the process exit code.
func Run(args []string, stdout, stderr io.Writer, version string) int {
	if len(args) > 0 && args[0] == "--version" {
		fmt.Fprintf(stdout, "kanban %s\n", version)
		return 0
	}
	args = boardAfterCommand(args)
	command := ""
	if len(args) > 0 {
		command = args[0]
	}
	entry, ok := entries[command]
	if !ok {
		fmt.Fprintf(stderr, "kanban: unknown command: %s\n", commandName(command))
		fmt.Fprintln(stderr, usage)
		return 2
	}
	return entry(context.Background(), args[1:], stdout, stderr, version)
}

// boardAfterCommand moves a --board pair that precedes the command word behind
// the word, where every command's flags live: the table wants the command word
// first, and the cli reads the flag from either side.
func boardAfterCommand(args []string) []string {
	i := 0
	var pairs []string
	for i < len(args) {
		if args[i] == "--board" && i+1 < len(args) {
			pairs = append(pairs, args[i], args[i+1])
			i += 2
			continue
		}
		if strings.HasPrefix(args[i], "--board=") {
			pairs = append(pairs, args[i])
			i++
			continue
		}
		break
	}
	if len(pairs) == 0 || i >= len(args) {
		return args
	}
	out := make([]string, 0, len(args))
	out = append(out, args[i])
	out = append(out, pairs...)
	out = append(out, args[i+1:]...)
	return out
}

// commandName is what the unknown-command error names; an empty argv shows as
// "(none)".
func commandName(command string) string {
	if command == "" {
		return "(none)"
	}
	return command
}

// cliEntry runs one subcommand, the command word back at the front of the argv
// the cli parses.
func cliEntry(name string) runner {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
		return cli.Run(ctx, world(version), append([]string{name}, args...), stdout, stderr)
	}
}

// world builds the real world one invocation runs in: the flow commands go to
// the local huddle hub when one answers and stay direct when none does, the
// hub's board link answers the same way, and start and open start the hub
// through huddle's own EnsureHub — the one way a kanban word starts anything.
func world(version string) cli.World {
	w := cli.DefaultWorld(version)
	w.HubFlow = func(ctx context.Context, key string, req board.FlowRequest) ([]string, string, bool) {
		return hubFlow(ctx, w.Getenv, key, req)
	}
	w.HubBoard = func(ctx context.Context, key string) (string, bool) {
		return hubBoard(ctx, w.Getenv, key)
	}
	w.EnsureHub = func(ctx context.Context, budgetMS int) (string, bool, string) {
		return huddlecli.EnsureHub(ctx, &huddlecli.World{Getenv: envOf(w.Getenv)}, budgetMS)
	}
	return w
}
