// The process-level wiring of the command line: the writers it prints on, a bounded HTTP
// client, and the platform's browser opener — every effect the command's callers inject, built
// for a real process here. The hub effects (the lookup and the start) are wired by the radar
// word in internal/radar/app, which can name the huddle packages the command line itself never
// imports.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// DefaultSinceMs is the window a start watches when --since is not given.
const DefaultSinceMs = 24 * 3_600_000

// RealDeps builds the effects a real invocation runs with: the writers, a bounded HTTP client,
// the platform's browser opener and the wall clock. Hub and EnsureHub stay unwired here — the
// app fills them in, so the words can reach the hub.
func RealDeps(version string, stdout, stderr io.Writer) Deps {
	return Deps{
		Env:         envOf(os.Environ()),
		Version:     version,
		Out:         func(line string) { fmt.Fprintln(stdout, line) },
		Err:         func(line string) { fmt.Fprintln(stderr, line) },
		FetchJSON:   fetchJSON,
		Sleep:       func(ms int64) { time.Sleep(time.Duration(ms) * time.Millisecond) },
		ShouldStop:  func() bool { return false }, // the watch loop ends only when the server stops or the process is signalled
		IsAlive:     process.IsAlive,
		OpenBrowser: openBrowser,
		Now:         func() int64 { return time.Now().UnixMilli() },
		SinceMs:     DefaultSinceMs,
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

// openBrowser hands the url to the platform's browser opener, detached and best-effort: the
// url is already printed.
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
