// Command huddle is the huddle binary: one dispatch over the plugin's entry points (the CLI,
// the MCP server, the HTTP server and the hooks), each behind the subcommand the plugin invokes
// it with, over radar's, kanban's and the gateway's command lines (the radar, kanban and gateway
// words), and over the binary's own words for running without Claude Code (alone.go).
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	kanbanapp "github.com/MuhmdRaouf/huddle/internal/kanban/app"
	radarapp "github.com/MuhmdRaouf/huddle/internal/radar/app"

	coreentry "github.com/MuhmdRaouf/huddle/internal/core/entry"
	coreproviders "github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/gateway"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/netproxy"
)

// version is the huddle version, set at build time with -ldflags "-X main.version=<VERSION>".
var version = "dev"

// run is one invocation; the returned int is the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	// A provider plugin in proxy mode points HTTPS_PROXY at its localhost router, and that
	// router refuses tunnels to this machine: huddle's own requests to a hub on this machine
	// go direct, the rest keep the environment's proxy.
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		transport.Proxy = netproxy.FromEnvironment
	}
	if len(args) > 0 && args[0] == "--version" {
		fmt.Fprintf(stdout, "huddle %s\n", version)
		return 0
	}
	// The provider engine's argv shapes: `provider <p> <entry> ...` for people and plugins, and
	// the bare `<p> <entry> ...` and `--orphan-launch ...` its own detached children are started
	// with.
	if len(args) > 0 {
		switch {
		case args[0] == "provider":
			return coreentry.Run(args[1:], stdout, stderr)
		case args[0] == "--orphan-launch" || slices.Contains(coreproviders.Names, args[0]):
			return coreentry.Run(args, stdout, stderr)
		}
	}
	// The satellite words: radar, kanban and the gateway run their own command lines inside this
	// binary, ahead of every huddle word — a satellite invocation never touches a huddle home.
	if len(args) > 0 {
		switch args[0] {
		case "radar":
			return radarapp.Run(args[1:], stdout, stderr, version)
		case "kanban":
			return kanbanapp.Run(args[1:], stdout, stderr, version)
		case "gateway":
			return gateway.Run(args[1:], stdout, stderr, gateway.RunOptions{
				Env: envOf(os.Environ()),
			})
		case "router":
			// The huddle-router daemon's words: ahead of the CLI, so a session's identity never
			// turns a daemon command into a channel operation.
			return routerWord(context.Background(), args[1:], stdout, stderr)
		}
	}
	command, rest := splitCommand(args)
	// The hidden word another agent's hook runs is no plugin entry point: it is its own
	// dispatch, ahead of the entries map.
	if command == "hook" && len(rest) > 0 && rest[0] == "external" {
		return externalEntry(context.Background(), args[2:], stdout, stderr)
	}
	key := command
	if command == "hook" && len(rest) > 0 {
		key = "hook " + rest[0]
		rest = rest[1:]
	}
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

// envOf splits an ordered environment into the lookup the state resolvers take.
func envOf(environ []string) domain.EnvLookup {
	env := make(domain.EnvLookup, len(environ))
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
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
	// The provider engine's entries report and compare this binary's version.
	coreentry.SetVersion(version)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
