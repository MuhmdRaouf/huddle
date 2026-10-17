// The process-level wiring of the command line: the writers it prints on, a bounded HTTP
// client, the detached self-spawn, signals, and the platform's browser opener — every effect
// the command's callers inject, built for a real process here.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// DefaultSinceMs is the window a start watches when --since is not given.
const DefaultSinceMs = 24 * 3_600_000

// Executable resolves the binary a detached start spawns; the tests swap it for one they build.
var Executable = os.Executable

// RealDeps builds the effects a real invocation runs with: the writers, a bounded HTTP client,
// a detached self-spawn, signals, the platform's browser opener and the wall clock.
func RealDeps(version string, stdout, stderr io.Writer) Deps {
	return Deps{
		Env:           envOf(os.Environ()),
		Version:       version,
		Out:           func(line string) { fmt.Fprintln(stdout, line) },
		Err:           func(line string) { fmt.Fprintln(stderr, line) },
		FetchJSON:     fetchJSON,
		Sleep:         func(ms int64) { time.Sleep(time.Duration(ms) * time.Millisecond) },
		ShouldStop:    func() bool { return false }, // the watch loop ends only when the server stops or the process is signalled
		SpawnDetached: spawnDetached,
		Signal:        signalStop,
		Command:       commandOf,
		IsAlive:       process.IsAlive,
		OpenBrowser:   openBrowser,
		Now:           func() int64 { return time.Now().UnixMilli() },
		SinceMs:       DefaultSinceMs,
	}
}

// envOf splits an ordered environment into the lookup the path resolvers take.
func envOf(environ []string) domain.EnvLookup {
	env := make(domain.EnvLookup, len(environ))
	for _, item := range environ {
		key, value, _ := strings.Cut(item, "=")
		env[key] = value
	}
	return env
}

// fetchJSON gets one endpoint with a two-second budget; network trouble is OK false, never an
// error, and a body that is not a JSON object leaves Body nil.
func fetchJSON(url string) FetchResult {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return FetchResult{}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return FetchResult{}
	}
	var body any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return FetchResult{}
	}
	object, _ := body.(map[string]any)
	return FetchResult{OK: true, Body: object}
}

// spawnDetached starts this binary again detached with the given sub-args — the server the
// parent then follows; 0 when the start failed.
func spawnDetached(args []string) int64 {
	self, err := Executable()
	if err != nil {
		return 0
	}
	return process.StartDetached(self, args, process.DetachedOptions{Env: os.Environ()}).Pid
}

// signalStop delivers the stop signal to the server's own group: a detached start runs in its
// own session, and a group stop also reaches nothing else.
func signalStop(pid int64) bool {
	return process.SignalGroup(pid, syscall.SIGTERM)
}

// commandOf answers a pid's command line as `ps -o command=` prints it; false when ps cannot
// name the pid. The probe is bounded, so a wedged ps never holds the stop.
func commandOf(pid int64) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "command=", "-p", strconv.FormatInt(pid, 10)).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// openBrowser hands the url to the platform's opener, detached and best-effort: the url is
// already printed.
func openBrowser(url string) {
	process.StartDetached(whichOpener(), []string{url}, process.DetachedOptions{})
}

// whichOpener is the platform's browser opener.
func whichOpener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}
