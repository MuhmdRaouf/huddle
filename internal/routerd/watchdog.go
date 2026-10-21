// The session watchdog inside `huddle mcp`: while the bridge serves a session it asks
// huddle-router's health every 2 s — but only when a provider is enabled — and after 5 s
// without a healthy answer it starts the daemon again, replacing a holder that is alive but
// silent first. The router start lock keeps several sessions' watchdogs to one daemon. The
// watchdog is a goroutine of the session's own bridge, so it ends with the session and can
// never delay an MCP reply.
package routerd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

const (
	// watchEvery is how often the watchdog asks the health path.
	watchEvery = 2 * time.Second
	// watchSilence is how long the health path may stay silent before a start.
	watchSilence = 5 * time.Second
	// watchHealthTimeout bounds one health probe.
	watchHealthTimeout = time.Second
	// watchStartBudgetMs bounds one start's wait for the daemon it spawned.
	watchStartBudgetMs = 3000
	// watchReplaceGraceMs is the SIGTERM grace before a hung holder's group is killed.
	watchReplaceGraceMs = 2000
)

// WatchOptions shapes one session's watchdog. Every field may stay zero: the defaults are the
// real health probe, the real daemon start, the real hung-holder replacement and the router log.
type WatchOptions struct {
	// Env is where the port, the home and a started daemon's environment come from.
	Env domain.EnvLookup
	// Every is the tick; 0 is 2 s.
	Every time.Duration
	// Silence is the unanswered stretch a start follows; 0 is 5 s.
	Silence time.Duration
	// Healthy says whether the daemon answers; nil is the health path with a 1 s timeout.
	Healthy func(ctx context.Context) bool
	// Start makes sure one daemon serves; nil is the detached start the words use.
	Start func(ctx context.Context) error
	// Replace takes a live lock holder out of the way and says whether it did; nil is
	// replaceHungRouter. A false skips the start: a holder that is not ours keeps the port.
	Replace func(ctx context.Context, pid int) bool
	// Log receives one line per restart; nil appends to HomeOf/router.log.
	Log func(line string)
}

// Watch keeps huddle-router up while ctx lives: every o.Every (2 s) it asks the health path, and
// after o.Silence (5 s) without a healthy answer it starts the daemon again. Nothing enabled is
// nothing to watch. It never returns an error to the caller, recovers every panic of its own
// helpers, and returns when ctx ends.
func Watch(ctx context.Context, o WatchOptions) {
	env := o.Env
	if env == nil {
		env = domain.EnvLookup{}
	}
	every := o.Every
	if every == 0 {
		every = watchEvery
	}
	silence := o.Silence
	if silence == 0 {
		silence = watchSilence
	}
	healthy := o.Healthy
	if healthy == nil {
		healthy = func(ctx context.Context) bool { return watchHealthy(ctx, env) }
	}
	start := o.Start
	if start == nil {
		start = func(ctx context.Context) error {
			_, err := Start(ctx, StartOptions{Env: env, Executable: watchExecutable(env), BudgetMs: watchStartBudgetMs})
			return err
		}
	}
	replace := o.Replace
	if replace == nil {
		replace = func(ctx context.Context, pid int) bool { return replaceHungRouter(ctx, watchSelf(env), pid) }
	}
	log := o.Log
	if log == nil {
		log = router.RotatingLog(filepath.Join(router.HomeOf(env), "router.log"), nil, 0)
	}
	lock := filepath.Join(router.HomeOf(env), "run", "router.lock")
	// The silence clock runs from the last healthy answer, so a daemon that dies between two
	// ticks gets its restart at most one tick later than its death deserved.
	lastHealthy := time.Now()
	for {
		// The first check comes at once: a session that starts while the daemon is down owes
		// it a restart, and the silence clock runs from the last healthy answer, so a daemon
		// that dies between two ticks never waits more than one tick past its death.
		if err := func() (err error) {
			defer func() {
				if reason := recover(); reason != nil {
					err = fmt.Errorf("recovered: %v", reason)
				}
			}()
			return watchTick(ctx, env, lock, silence, healthy, start, replace, log, &lastHealthy)
		}(); err != nil {
			log("huddle-router: the session watchdog: " + err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// watchTick is one tick of Watch. The silence clock moves to now on a healthy answer or an empty
// providers.json, and a start (or a refused replacement) moves it too, so the next restart waits
// a fresh silence.
func watchTick(ctx context.Context, env domain.EnvLookup, lock string, silence time.Duration,
	healthy func(context.Context) bool, start func(context.Context) error,
	replace func(context.Context, int) bool, log func(string), lastHealthy *time.Time) error {
	names, err := router.ReadEnabled(env)
	if err != nil || len(names) == 0 {
		*lastHealthy = time.Now()
		return nil
	}
	if healthy(ctx) {
		*lastHealthy = time.Now()
		return nil
	}
	silent := time.Since(*lastHealthy)
	if silent < silence {
		return nil
	}
	if holder, held := singleton.Holder(lock); held && process.IsAlive(int64(holder)) {
		if !replace(ctx, holder) {
			line := fmt.Sprintf("pid %d", holder)
			if command, ok := router.CommandLineOf(ctx, int64(holder)); ok {
				line = command
			}
			log(fmt.Sprintf("huddle-router: port held by %s; not replaced", line))
			*lastHealthy = time.Now()
			return nil
		}
	}
	log(fmt.Sprintf("huddle-router: restarted by the session watchdog (silent %d s)",
		int(silent.Round(time.Second).Seconds())))
	if err := start(ctx); err != nil {
		log("huddle-router: the session watchdog's start failed: " + err.Error())
	}
	*lastHealthy = time.Now()
	return nil
}

// watchHealthy is the health probe: the daemon's own answer at its port within a second.
func watchHealthy(ctx context.Context, env domain.EnvLookup) bool {
	port, err := router.PortOf(env)
	if err != nil {
		return false
	}
	bounded, cancel := context.WithTimeout(ctx, watchHealthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(bounded, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, router.MultiHealthPath), nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	var answer HealthAnswer
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || json.Unmarshal(body, &answer) != nil {
		return false
	}
	return answer.Name == router.MultiName
}

// replaceHungRouter takes a live holder that is this binary running `router serve` out of the
// way: SIGTERM to its process group, two seconds, then SIGKILL — the measure the hub takes on a
// hung server. Anything else the lock names is never signalled, and false sends the start away:
// a program of the user's own keeps the port, and the session waits instead of fighting it.
func replaceHungRouter(ctx context.Context, self string, pid int) bool {
	if pid <= 1 || pid == os.Getpid() {
		return false
	}
	line, ok := router.CommandLineOf(ctx, int64(pid))
	// the binary's name, not its path: each commit's build lives at its own path, so a daemon
	// from an older build is still ours to replace
	fields := strings.Fields(line)
	if !ok || len(fields) == 0 || filepath.Base(fields[0]) != filepath.Base(self) ||
		!(strings.Contains(line, "router serve") || strings.Contains(line, " serve")) {
		return false
	}
	process.TerminateGroup(ctx, int64(pid), watchReplaceGraceMs)
	return true
}

// watchExecutable is the binary a start spawns the daemon from: one that exists now, so a
// session that outlived its own build still starts the launcher's current one.
func watchExecutable(env domain.EnvLookup) string {
	return process.LiveExecutable(env)
}

// watchSelf is the binary's name as a command line names it: the token a hung daemon's argv must
// carry for the replacement to take it.
func watchSelf(env domain.EnvLookup) string {
	return filepath.Base(watchExecutable(env))
}
