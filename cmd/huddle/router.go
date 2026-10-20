// The huddle-router words: `serve` runs the one router for every enabled provider in the
// foreground, `start` makes sure one runs detached and prints its address, and `status` says
// how it is. One router per user: whoever holds the router lock serves the port, and a second
// start steps aside with exit 0.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

// routerWord runs one of the router words: args are the words after `router`.
func routerWord(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	env := envOf(os.Environ())
	var word string
	if len(args) > 0 {
		word = args[0]
	}
	switch word {
	case "serve":
		return routerServe(ctx, env, args[1:], stdout, stderr)
	case "start":
		return routerStart(ctx, env, stdout, stderr)
	case "status":
		return routerStatus(ctx, env, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: huddle router serve [--takeover] | start | status [--json]")
		return 2
	}
}

// routerPortOf is the port the router words talk to, or false after naming a bad
// $HUDDLE_ROUTER_PORT.
func routerPortOf(env domain.EnvLookup, stderr io.Writer) (int, bool) {
	port, err := router.PortOf(env)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 0, false
	}
	return port, true
}

// routerServe runs the daemon in the foreground until a signal ends it. A daemon already
// serving is the happy answer, not a failure. With --takeover the run first asks the serving
// daemon for its listening socket over the handover socket — the flag behind updates and the
// memory ceiling — and falls back to the normal start when nobody answers.
func routerServe(ctx context.Context, env domain.EnvLookup, args []string, stdout, stderr io.Writer) int {
	port, ok := routerPortOf(env, stderr)
	if !ok {
		return 2
	}
	foreground, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := routerd.Run(foreground, routerd.Options{Env: env, Port: port, Version: version, Takeover: hasFlag(args, "--takeover")})
	var held *singleton.HeldError
	if errors.As(err, &held) {
		fmt.Fprintf(stdout, "huddle-router: already served by pid %d\n", held.PID)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "huddle-router: %v\n", err)
		return 1
	}
	return 0
}

// routerStart makes sure one daemon serves: the probe, the detached spawn and the report line
// are the daemon start's own (routerd.Start); this word adds only the usage rule and the exit
// codes. No provider set up is the quiet answer, exit 0, and nothing is spawned.
func routerStart(ctx context.Context, env domain.EnvLookup, stdout, stderr io.Writer) int {
	if _, ok := routerPortOf(env, stderr); !ok {
		return 2
	}
	line, err := routerd.Start(ctx, routerd.StartOptions{Env: env})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, line)
	return 0
}

// routerStatus says how the daemon is: its address and what it serves, with the record it
// wrote at start. Nothing running is exit 6, one line; --json answers for a script.
func routerStatus(ctx context.Context, env domain.EnvLookup, args []string, stdout, stderr io.Writer) int {
	port, ok := routerPortOf(env, stderr)
	if !ok {
		return 2
	}
	health, up := routerd.Health(ctx, port)
	if !up {
		if hasFlag(args, "--json") {
			fmt.Fprintln(stdout, `{"running":false}`)
		} else {
			fmt.Fprintln(stderr, "huddle-router: not running")
		}
		return 6
	}
	var record struct {
		PID     int    `json:"pid"`
		Port    int    `json:"port"`
		Version string `json:"version"`
		Started string `json:"started"`
	}
	if raw, err := os.ReadFile(filepath.Join(router.HomeOf(env), "run", "router.json")); err == nil {
		_ = json.Unmarshal(raw, &record)
	}
	if hasFlag(args, "--json") {
		answer := struct {
			Running   bool     `json:"running"`
			Name      string   `json:"name"`
			Providers []string `json:"providers"`
			PID       int      `json:"pid,omitempty"`
			Port      int      `json:"port,omitempty"`
			Version   string   `json:"version,omitempty"`
			Started   string   `json:"started,omitempty"`
		}{
			Running:   true,
			Name:      health.Name,
			Providers: health.Providers,
			PID:       record.PID,
			Port:      port,
			Version:   record.Version,
			Started:   record.Started,
		}
		encoded, err := json.Marshal(answer)
		if err != nil {
			fmt.Fprintf(stderr, "huddle-router: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, string(encoded))
		return 0
	}
	fmt.Fprintf(stdout, "huddle-router: http://127.0.0.1:%d\n", port)
	pidWord := "unknown"
	if record.PID > 0 {
		pidWord = strconv.Itoa(record.PID)
	}
	versionWord := "unknown"
	if record.Version != "" {
		versionWord = record.Version
	}
	fmt.Fprintf(stdout, "pid %s · version %s · providers %s\n", pidWord, versionWord, strings.Join(health.Providers, ", "))
	return 0
}
