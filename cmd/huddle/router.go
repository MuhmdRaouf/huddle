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
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

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
		return routerServe(ctx, env, stdout, stderr)
	case "start":
		return routerStart(ctx, env, stdout, stderr)
	case "status":
		return routerStatus(ctx, env, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: huddle router serve | start | status [--json]")
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
// serving is the happy answer, not a failure.
func routerServe(ctx context.Context, env domain.EnvLookup, stdout, stderr io.Writer) int {
	port, ok := routerPortOf(env, stderr)
	if !ok {
		return 2
	}
	foreground, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := routerd.Run(foreground, routerd.Options{Env: env, Port: port, Version: version})
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

// routerStart makes sure one daemon serves: a health answer within 300 ms is enough, else one
// is spawned detached (its output on the router log, its own session) and waited for up to
// 3 s. No provider set up is the quiet answer, exit 0, and nothing is spawned.
func routerStart(ctx context.Context, env domain.EnvLookup, stdout, stderr io.Writer) int {
	port, ok := routerPortOf(env, stderr)
	if !ok {
		return 2
	}
	if _, up := routerHealthOf(ctx, port); up {
		return routerUpLine(port, stdout)
	}
	names, err := router.ReadEnabled(env)
	if err != nil {
		fmt.Fprintf(stderr, "huddle-router: %v\n", err)
		return 1
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "huddle-router: no provider is set up")
		return 0
	}
	logFile := filepath.Join(router.HomeOf(env), "router.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		fmt.Fprintf(stderr, "huddle-router: %v\n", err)
		return 1
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "huddle-router: %v\n", err)
		return 1
	}
	log, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "huddle-router: %v\n", err)
		return 1
	}
	defer log.Close()
	cmd := exec.Command(self, "router", "serve")
	cmd.Env = os.Environ()
	cmd.Stdout, cmd.Stderr = log, log
	// Its own session: the daemon outlives this command and no signal of the caller's ends it
	// except the one delivered to it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "huddle-router: %v\n", err)
		return 1
	}
	_ = cmd.Process.Release()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, up := routerHealthOf(ctx, port); up {
			return routerUpLine(port, stdout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintf(stderr, "huddle-router: the daemon never answered at http://127.0.0.1:%d (its log: %s)\n", port, logFile)
	return 1
}

// routerUpLine prints the one line a script reads: where the router answers.
func routerUpLine(port int, stdout io.Writer) int {
	fmt.Fprintf(stdout, "huddle-router: http://127.0.0.1:%d\n", port)
	return 0
}

// routerHealthAnswer is the router health answer's shape.
type routerHealthAnswer struct {
	Name      string   `json:"name"`
	Providers []string `json:"providers"`
}

// routerHealthOf is the running router's health answer, ok false while nothing answers it
// naming huddle-router within 300 ms.
func routerHealthOf(ctx context.Context, port int) (routerHealthAnswer, bool) {
	bounded, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(bounded, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, router.MultiHealthPath), nil)
	if err != nil {
		return routerHealthAnswer{}, false
	}
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return routerHealthAnswer{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return routerHealthAnswer{}, false
	}
	var answer routerHealthAnswer
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || json.Unmarshal(body, &answer) != nil {
		return routerHealthAnswer{}, false
	}
	if answer.Name != router.MultiName {
		return routerHealthAnswer{}, false
	}
	return answer, true
}

// routerStatus says how the daemon is: its address and what it serves, with the record it
// wrote at start. Nothing running is exit 6, one line; --json answers for a script.
func routerStatus(ctx context.Context, env domain.EnvLookup, args []string, stdout, stderr io.Writer) int {
	port, ok := routerPortOf(env, stderr)
	if !ok {
		return 2
	}
	health, up := routerHealthOf(ctx, port)
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
