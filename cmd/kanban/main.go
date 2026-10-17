// Command kanban is the kanban binary: one dispatch over its entry points —
// every board command, plus the server entries and the hook later cards add.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// version is the kanban version, set at build time with
// -ldflags "-X main.version=<VERSION>".
var version = "dev"

// run is one invocation; the returned int is the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
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
	return entry(context.Background(), args[1:], stdout, stderr)
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

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
