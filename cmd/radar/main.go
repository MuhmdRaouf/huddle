// Command radar is the radar binary: one dispatch over its entry points (the CLI and the hook),
// each behind the subcommand the plugin invokes it with.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

// version is the radar version, set at build time with -ldflags "-X main.version=<VERSION>".
var version = "dev"

// run is one invocation; the returned int is the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
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
	return entry(context.Background(), args[1:], stdout, stderr)
}

// commandName is what the unknown-command error names; an empty argv shows as "(none)".
func commandName(command string) string {
	if command == "" {
		return "(none)"
	}
	return command
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
