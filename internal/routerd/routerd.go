package routerd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/fsutil"
	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/adapters/spool"
	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/gateway"
)

const (
	// pollDefault is how often providers.json is re-read.
	pollDefault = 2 * time.Second
	// anthropicDefault is where Claude models go when no override names another Anthropic.
	anthropicDefault = "https://api.anthropic.com"
	// stopBudget is how long a shutdown lets the streams in flight finish.
	stopBudget = 10 * time.Second
)

// Options shapes one daemon run.
type Options struct {
	Env domain.EnvLookup
	// Port is the port to serve; 0 takes 18786. Tests pass a free port. A takeover ignores it:
	// the taken socket's port comes with the socket.
	Port int
	// Version is recorded in router.json, for status.
	Version string
	// Log receives the daemon's lines; nil takes a rotating log at HomeOf/router.log.
	Log func(line string)
	// Poll is how often providers.json is re-read; 0 is two seconds.
	Poll time.Duration
	// Takeover first asks the daemon serving the home for its listening socket over the
	// handover socket and serves on it at once; a takeover that finds nobody to take from falls
	// back to the normal path (lock, listen).
	Takeover bool
	// RSSCeiling is the resident size over which the daemon hands itself over to a fresh copy
	// of its own binary; 0 takes $HUDDLE_ROUTER_RSS_CEILING_MB, else one GiB.
	RSSCeiling int64
	// Hosted is the router that runs inside the hub's process; its RSS is the hub's, so the
	// memory handover is off.
	Hosted bool
	// panicOnce is a test-only hook the handler runs before serving, so a scenario can put a
	// panic in a lane and prove the recovery.
	panicOnce func(req *http.Request)
}

// record is what the running router publishes about itself, for status and the start word.
type record struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	Version string `json:"version"`
	// Executable is the binary the daemon runs, so a start from another binary knows the
	// running daemon is not this one and takes the socket over.
	Executable string `json:"executable,omitempty"`
	Started    string `json:"started"`
}

// Run serves huddle-router until ctx ends. It answers a *singleton.HeldError when another
// process serves it, and nil after a clean stop or after handing its socket to a taker. While
// no provider is enabled the port stays free: the daemon polls for one instead of binding an
// empty router.
func Run(ctx context.Context, o Options) error {
	env := o.Env
	if env == nil {
		env = domain.EnvLookup{}
	}
	poll := o.Poll
	if poll == 0 {
		poll = pollDefault
	}
	home := router.HomeOf(env)
	runDir := filepath.Join(home, "run")
	log := o.Log
	if log == nil {
		log = router.RotatingLog(filepath.Join(home, "router.log"), nil, 0)
	}
	if o.Takeover {
		listener, err := Takeover(ctx, home)
		if err == nil {
			// The previous daemon holds the router lock while it drains, up to ten minutes:
			// this one picks the lock up in the background when it lets go.
			done := make(chan struct{})
			go holdRouterLock(filepath.Join(runDir, "router.lock"), done, log)
			port := listener.Addr().(*net.TCPAddr).Port
			return serve(ctx, o, env, home, log, listener, port, done)
		}
		log("huddle-router: takeover: " + err.Error())
	}
	// One router per user: whoever holds the lock serves the port. A standing holder is named
	// at once; a dead one never blocks — the kernel has already dropped its lock. The lock file
	// itself is never deleted, so a starter racing a releaser never locks a fresh inode.
	release, err := singleton.Lock(filepath.Join(runDir, "router.lock"), router.MultiName)
	if err != nil {
		return err
	}
	defer release()
	enabledFile := router.EnabledFile(env)
	names, err := router.ReadEnabled(env)
	if err != nil {
		log("huddle-router: " + err.Error())
		names = nil
	}
	if len(names) == 0 {
		log("huddle-router: no provider is enabled yet; waiting for " + enabledFile)
	}
	for len(names) == 0 {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
		if names, err = router.ReadEnabled(env); err != nil {
			log("huddle-router: " + err.Error())
			names = nil
		}
	}
	port := o.Port
	if port == 0 {
		if port, err = router.PortOf(env); err != nil {
			return err
		}
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("huddle-router: cannot listen on 127.0.0.1:%d (%s)", port, fsutil.ErrorMessage(err))
	}
	return serve(ctx, o, env, home, log, listener, port, nil)
}

// serve runs one daemon's router on the listener until ctx ends, a taker takes the socket over,
// or the serve loop dies: it mounts the lanes, publishes its record, follows providers.json,
// and watches its own resident size. done closes when serve returns; a takeover start's lock
// holder waits on it.
func serve(ctx context.Context, o Options, env domain.EnvLookup, home string, log func(line string), listener net.Listener, port int, done chan struct{}) error {
	if done != nil {
		defer close(done)
	}
	poll := o.Poll
	if poll == 0 {
		poll = pollDefault
	}
	// The no-chain rule: the engine environment carries no proxy variable that names a router,
	// whatever port it serves, so upstream traffic never chains through one.
	env = CleanProxyEnv(ctx, env, port, log)
	anthropic, err := anthropicOf(env)
	if err != nil {
		_ = listener.Close()
		return err
	}
	enabledFile := router.EnabledFile(env)
	recordFile := filepath.Join(home, "run", "router.json")
	names, err := router.ReadEnabled(env)
	if err != nil {
		log("huddle-router: " + err.Error())
		names = nil
	}
	if len(names) == 0 {
		// a takeover serves an empty router rather than closing the port it just took
		log("huddle-router: no provider is enabled yet; waiting for " + enabledFile)
	}
	// The CA is the one directory every provider's setup points NODE_EXTRA_CA_CERTS at: the
	// state roots' parent, where the shared local CA lives.
	server, multi := router.CreateMulti(router.MultiOptions{
		Port:       port,
		ExtraPorts: legacyPortNumbers(legacyPorts(env)),
		Anthropic:  anthropic,
		Lanes:      lanesOf(env, names, log),
		CADir:      filepath.Join(filepath.Dir(home), "ca"),
		Env:        router.ProxyEnv(env),
		Log:        log,
		Events:     spool.CreateSpoolWriter(env, log, nil),
		Gateway:    gatewayOf(env, log),
	})
	server.Handler = guardPanics(server.Handler, log, o.panicOnce)
	// The handover socket listens before the first health answer: a taker that sees the router
	// up and dials in between would find nobody to take from.
	handed := make(chan int, 1)
	stopHandover := serveHandover(home, port, listener, handed, log)
	defer stopHandover()
	served := make(chan error, 1)
	go func() {
		defer func() {
			if reason := recover(); reason != nil {
				log(fmt.Sprintf("huddle-router: recovered: %v", reason))
				served <- errors.New("huddle-router: panic in the serve loop")
			}
		}()
		served <- server.Serve(listener)
	}()
	// The 0.0.1 ports are this server's too: one generation serves them, so a session started
	// before a migration keeps working and the old generation has nothing left to respawn.
	// They ride no handover — the next daemon adopts them on its own first tick, so during an
	// update a legacy port may refuse connections for up to a second.
	stopAdopting := adoptLegacyPorts(ctx, env, server, log)
	defer stopAdopting()
	if err := writeRecord(ctx, recordFile, port, o.Version, selfExecutable()); err != nil {
		// A router without its record still serves; status and start are the ones that miss it.
		log("huddle-router: " + err.Error())
	}
	before := statOf(enabledFile)
	// The 0.0.1 setups move once per start: a machine the per-provider routers served joins
	// huddle-router on its first serve, old routers retiring as their sessions' lanes come up.
	go migrateOnce(ctx, env, log)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	ceiling := rssCeilingOf(env, o)
	interval := rssIntervalOf(env)
	spawned := time.Time{}
	nextRSS := time.Now().Add(interval)
	if o.Hosted {
		log("huddle-router: hosted by the hub; the memory handover is off")
	}
	for {
		select {
		case <-ctx.Done():
			return stop(server, recordFile, log)
		case err := <-served:
			_ = os.Remove(recordFile)
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case taker := <-handed:
			return handOver(server, taker, log)
		case <-ticker.C:
			now := statOf(enabledFile)
			if now != before {
				before = now
				names, err := router.ReadEnabled(env)
				if err != nil {
					log("huddle-router: " + err.Error())
				} else if len(names) == 0 {
					// Nothing is enabled: keep serving. Sessions still point at the router until
					// their settings are restored, and an empty answer would cut them off.
				} else if lanes := lanesOf(env, names, log); len(lanes) != 0 {
					multi.Reload(lanes)
					log("huddle-router: now serving " + strings.Join(multi.Providers(), ", "))
				}
			}
			// A hosted router's RSS is the hub's, so only a standalone daemon watches its own.
			if !o.Hosted && !nextRSS.After(time.Now()) {
				nextRSS = time.Now().Add(interval)
				spawned = checkRSS(ceiling, spawned, env, log)
			}
		}
	}
}

// anthropicOf is the Anthropic the Claude models go to: $HUDDLE_ROUTER_ANTHROPIC_URL, else
// Anthropic itself.
func anthropicOf(env domain.EnvLookup) (*url.URL, error) {
	raw := env["HUDDLE_ROUTER_ANTHROPIC_URL"]
	if raw == "" {
		raw = anthropicDefault
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("huddle-router: $HUDDLE_ROUTER_ANTHROPIC_URL: %w", err)
	}
	return parsed, nil
}

// gatewayOf builds the gateway the router mounts under /gateway from the huddle home's
// gateway.json, its route lines riding the same spool writer the lanes use. A config that does
// not hold up is logged once and the router runs without a gateway: never fail the router over
// an extra.
func gatewayOf(env domain.EnvLookup, log func(line string)) http.Handler {
	config, err := gateway.LoadConfig(env)
	if err != nil {
		log("huddle-router: gateway: " + err.Error())
		return nil
	}
	return gateway.NewServer(config, gateway.SpoolRecorder(env, log), nil)
}

// lanesOf builds the lane options for every enabled provider; one whose lane cannot be built (a
// bad endpoint override) is skipped and logged, never allowed to keep the others off the port.
func lanesOf(env domain.EnvLookup, names []string, log func(line string)) []router.RouterOptions {
	lanes := make([]router.RouterOptions, 0, len(names))
	for _, name := range names {
		provider, ok := providers.ByName(name)
		if !ok {
			log("huddle-router: no provider named " + name)
			continue
		}
		lane, err := router.LaneOf(provider, env, log)
		if err != nil {
			log("huddle-router: skipping " + name + ": " + err.Error())
			continue
		}
		lanes = append(lanes, lane)
	}
	return lanes
}

// guardPanics wraps the router's handler: a panic in a lane is answered 502 (an answer whose
// headers are already out is destroyed) and logged, so one lane's bug never ends the process.
func guardPanics(next http.Handler, log func(line string), hook func(req *http.Request)) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		defer func() {
			if reason := recover(); reason != nil {
				log(fmt.Sprintf("huddle-router: recovered: %v", reason))
				router.AnthropicError(router.NewResponse(rw), 502, "api_error",
					router.MultiName+" router: internal error")
			}
		}()
		if hook != nil {
			hook(req)
		}
		next.ServeHTTP(rw, req)
	})
}

// stop drains the router with a bounded budget, then takes its record with it. The lock's
// release runs on Run's defer.
func stop(server *http.Server, recordFile string, log func(line string)) error {
	budget, cancel := context.WithTimeout(context.Background(), stopBudget)
	defer cancel()
	if err := server.Shutdown(budget); err != nil {
		log("huddle-router: " + err.Error())
	}
	_ = os.Remove(recordFile)
	return nil
}

// writeRecord publishes what the running router is: the pid to signal, the port to talk to,
// which binary serves it and when it came up.
func writeRecord(ctx context.Context, path string, port int, version, executable string) error {
	text, err := fsutil.MarshalJSON(record{
		PID:        os.Getpid(),
		Port:       port,
		Version:    version,
		Executable: executable,
		Started:    time.Now().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	_, err = fsutil.WriteFileAtomic(ctx, path, text, fsutil.WriteOptions{Mkdir: true})
	return err
}

// readRecord reads the record the running daemon published in the home's run dir; false when
// there is none or it does not parse.
func readRecord(env domain.EnvLookup) (record, bool) {
	raw, err := os.ReadFile(filepath.Join(router.HomeOf(env), "run", "router.json"))
	if err != nil {
		return record{}, false
	}
	var parsed record
	if json.Unmarshal(raw, &parsed) != nil {
		return record{}, false
	}
	return parsed, true
}

// fingerprint is what a watched file must keep for its reader to skip it: size and mtime. A
// missing file is one fingerprint like any other, so a file that appears is a change too.
type fingerprint struct {
	size  int64
	modAt time.Time
	there bool
}

func statOf(path string) fingerprint {
	info, err := os.Stat(path)
	if err != nil {
		return fingerprint{}
	}
	return fingerprint{size: info.Size(), modAt: info.ModTime(), there: true}
}
