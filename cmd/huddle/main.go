// Command huddle is the huddle binary: one dispatch over the plugin's entry points (the CLI,
// the MCP server, the HTTP server and the hooks), each behind the subcommand the plugin invokes
// it with, and over the binary's own words for running without Claude Code (alone.go).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// version is the huddle version, set at build time with -ldflags "-X main.version=<VERSION>".
var version = "dev"

// The import budgets: a hook grants the one-time import 2 s of its own budget, anything else
// 10 s; what a budget does not finish waits for the next entry.
const (
	hookImportBudget  = 2 * time.Second
	entryImportBudget = 10 * time.Second
)

// run is one invocation; the returned int is the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--version" {
		fmt.Fprintf(stdout, "huddle %s\n", version)
		return 0
	}
	command, rest := splitCommand(args)
	key := command
	if command == "hook" && len(rest) > 0 {
		key = "hook " + rest[0]
		rest = rest[1:]
	}
	importLegacy(key)
	if entry, ok := entries[key]; ok {
		return entry(context.Background(), rest, stdout, stderr)
	}
	if code, taken := aloneRun(context.Background(), command, args, stdout, stderr); taken {
		return code
	}
	// every CLI word — a subcommand or an operation by name — reaches the CLI as one argv, so a
	// flag keeps its place and its meaning wherever it stands around the command word
	return cli.Huddle(context.Background(), &cli.World{}, args, stdout, stderr)
}

// importLegacy brings this project's legacy huddle homes into the user's home before the entry
// runs, inside the entry's budget. It never says anything and never fails the entry.
func importLegacy(key string) {
	budget := entryImportBudget
	if strings.HasPrefix(key, "hook") {
		budget = hookImportBudget
	}
	cli.ImportLegacy(context.Background(), &cli.World{}, budget)
}

// splitCommand mirrors the CLI's argument split: a --flag takes the next argument as its value
// unless that is a flag too; the subcommand is the first plain word, "help" when there is none.
// Only the binary's own entries (hook, mcp, serve) are picked out with it.
func splitCommand(args []string) (command string, rest []string) {
	command = "help"
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
			continue
		}
		if i+1 < len(args) {
			return args[i], args[i+1:]
		}
		return args[i], nil
	}
	return command, nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
