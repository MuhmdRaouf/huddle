// The legacy 0.0.1 ports: one router generation serves the old ports too. huddle-router takes
// the per-provider ports a 0.0.1 setup left behind — a session started before a migration keeps
// its proxy pointed there — stopping a verified 0.0.1 router or watcher that holds one first, so
// the old generation has nothing left to respawn (its ensure only checks that the port accepts a
// connection).
package routerd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

const (
	// adoptInterval is how often the daemon looks at the legacy ports it does not hold yet.
	adoptInterval = 30 * time.Second
	// legacyWait is how long a stop waits for a signalled router to give its port up, and how
	// long an adoption waits for the port to free, before it is killed or given up on.
	legacyWait = 10 * time.Second
	// adoptRetry is how often a freed port is tried while a stopped holder is going.
	adoptRetry = 200 * time.Millisecond
)

// legacyPorts is the legacy ports the daemon adopts: $HUDDLE_ROUTER_LEGACY_PORTS as name=port
// pairs when it is set — an empty value turns adoption off and a malformed pair is skipped —
// else every provider port but huddle-router's own, and only while the daemon itself serves the
// default port: a router moved off 18786 never reaches for a real legacy port.
func legacyPorts(env domain.EnvLookup) map[string]int {
	if raw, set := env["HUDDLE_ROUTER_LEGACY_PORTS"]; set {
		ports := make(map[string]int)
		for _, pair := range strings.Split(raw, ",") {
			name, value, found := strings.Cut(pair, "=")
			port, err := strconv.Atoi(strings.TrimSpace(value))
			if !found || err != nil || port < 1 || port > 65535 {
				continue
			}
			ports[strings.TrimSpace(name)] = port
		}
		return ports
	}
	port, err := router.PortOf(env)
	if err != nil || port != domain.HuddleRouterPort {
		return nil
	}
	ports := make(map[string]int, len(domain.PluginRouters)-1)
	for number, name := range domain.PluginRouters {
		if name == router.MultiName {
			continue
		}
		ports[name] = int(number)
	}
	return ports
}

// legacyPortNumbers is the map's ports, smallest first, for the Host guard.
func legacyPortNumbers(ports map[string]int) []int {
	numbers := make([]int, 0, len(ports))
	for _, number := range ports {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	return numbers
}

// stopLegacy stops the 0.0.1 generation on one legacy port and answers whether anything was
// signalled. The watchers first: a guard whose pid runs one of ours named `router watch` gets
// SIGTERM, and every guard file goes — a dead watch's file is litter, a foreign pid's file is
// never a reason to signal it. Then the router the pid or retired record names — one of ours,
// on this very port, never pid ≤ 1 and never this daemon. A record that is missing, stale or
// names another port says nothing usable, so the holder is asked for instead: the pid the OS
// shows listening on the port, verified the same way and by `router run` in its command line —
// a router restarted by hand, which no starter wrote a record for — and its `<name> router
// worker` children go with it. Either way: SIGTERM, up to ten seconds to give the port up,
// then SIGKILL while it is still ours and still running. A process of somebody else's is never
// signalled.
func stopLegacy(ctx context.Context, env domain.EnvLookup, name string, port int, log func(string)) bool {
	root := legacyStateRoot(ctx, env, name, log)
	signalled := false
	guards, err := filepath.Glob(filepath.Join(root, "watch.*"))
	if err == nil {
		for _, guard := range guards {
			if pid, ok := legacyGuardPid(guard); ok && router.IsOurProcess(pid, name, nil) {
				if line, ok := router.CommandLineOf(ctx, pid); ok && strings.Contains(line, "router watch") {
					if syscall.Kill(int(pid), syscall.SIGTERM) == nil {
						signalled = true
						log(fmt.Sprintf("huddle-router: stopped the 0.0.1 %s router watch (pid %d)", name, pid))
					}
				}
			}
			_ = os.Remove(guard)
		}
	}
	record, ok := router.ReadPidFile(ctx, root)
	if !ok {
		record, ok = router.ReadRetiredFile(ctx, root)
	}
	if ok && int(record.Port) == port && record.Pid > 1 && record.Pid != int64(os.Getpid()) &&
		process.IsAlive(record.Pid) && router.IsOurProcess(record.Pid, name, nil) {
		stopLegacyPids(name, port, []int64{record.Pid})
		signalled = true
		log(fmt.Sprintf("huddle-router: stopped the 0.0.1 %s router (pid %d) and took port %d", name, record.Pid, port))
		return signalled
	}
	holder, held := portHolder(ctx, port)
	if !held || holder <= 1 || holder == int64(os.Getpid()) || !router.IsOurProcess(holder, name, nil) {
		return signalled
	}
	line, hasLine := router.CommandLineOf(ctx, holder)
	if !hasLine || !strings.Contains(line, "router run") {
		return signalled
	}
	stopLegacyPids(name, port, append([]int64{holder}, workerPids(ctx, name, holder)...))
	signalled = true
	log(fmt.Sprintf("huddle-router: stopped the 0.0.1 %s router (pid %d, found by its port) and took port %d", name, holder, port))
	return signalled
}

// stopLegacyPids runs the stop over one verified 0.0.1 router and its workers: SIGTERM to all,
// up to ten seconds for the port to free, then SIGKILL to whichever is still ours and running.
func stopLegacyPids(name string, port int, pids []int64) {
	for _, pid := range pids {
		_ = syscall.Kill(int(pid), syscall.SIGTERM)
	}
	deadline := time.Now().Add(legacyWait)
	for portAccepts(port) && time.Now().Before(deadline) {
		time.Sleep(adoptRetry / 2)
	}
	for _, pid := range pids {
		if process.IsAlive(pid) && router.IsOurProcess(pid, name, nil) {
			_ = syscall.Kill(int(pid), syscall.SIGKILL)
		}
	}
}

// workerPids is the router's children whose command line names a `<name> router worker` and
// that are ours: they go when their holder goes.
func workerPids(ctx context.Context, name string, pid int64) []int64 {
	timeoutMs := int64(2000)
	answer := process.RunToolSync(ctx, "pgrep", []string{"-P", strconv.FormatInt(pid, 10)},
		process.ToolOptions{TimeoutMs: &timeoutMs})
	if !process.Succeeded(answer) {
		return nil
	}
	var workers []int64
	for _, field := range strings.Fields(string(answer.Stdout)) {
		child, err := strconv.ParseInt(field, 10, 64)
		if err != nil || child <= 0 {
			continue
		}
		if line, ok := router.CommandLineOf(ctx, child); ok &&
			strings.Contains(line, name+" router worker") && router.IsOurProcess(child, name, nil) {
			workers = append(workers, child)
		}
	}
	return workers
}

// legacyStateRoot is where a 0.0.1 provider kept its state: its $<PREFIX>_STATE_DIR, else
// ~/.agents/<name>.
func legacyStateRoot(ctx context.Context, env domain.EnvLookup, name string, log func(string)) string {
	provider, ok := providers.ByName(name)
	if !ok {
		return adapters.AgentsStateDir(env, name)
	}
	return adapters.ResolveStateRoot(ctx, env, adapters.NewStateRootRule(provider), log)
}

// legacyGuardPid is the watcher pid a guard file's content names.
func legacyGuardPid(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// portAccepts dials a loopback port; true when something answers.
func portAccepts(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// portHolder asks the OS which pid listens on the loopback port: the first pid `lsof` answers,
// or false when lsof is missing or finds nothing.
func portHolder(ctx context.Context, port int) (int64, bool) {
	timeoutMs := int64(2000)
	answer := process.RunToolSync(ctx, "lsof",
		[]string{"-nP", "-iTCP:" + strconv.Itoa(port), "-sTCP:LISTEN", "-t"},
		process.ToolOptions{TimeoutMs: &timeoutMs})
	if !process.Succeeded(answer) {
		return 0, false
	}
	for _, field := range strings.Fields(string(answer.Stdout)) {
		if pid, err := strconv.ParseInt(field, 10, 64); err == nil && pid > 0 {
			return pid, true
		}
	}
	return 0, false
}

// adoptLegacyPorts brings every legacy port the map names under this daemon's server: one
// goroutine takes the free ports at once and then every thirty seconds, stopping a verified
// 0.0.1 holder first, and a panic in the loop is logged, never a reason for the daemon to die.
// It answers the stop: it closes every adopted listener and ends the goroutine. Adopted ports
// ride no handover — the next daemon adopts them on its own first tick, so during an update a
// legacy port may refuse connections for up to a second.
func adoptLegacyPorts(ctx context.Context, env domain.EnvLookup, server *http.Server, log func(string)) (stop func()) {
	ports := legacyPorts(env)
	if len(ports) == 0 {
		return func() {}
	}
	var mu sync.Mutex
	var once sync.Once
	held := make(map[int]net.Listener)
	foreign := make(map[int]bool)
	stopped := false
	halt := make(chan struct{})
	finished := make(chan struct{})
	// adopt binds one port and serves it, under the lock: a stop must never race a fresh
	// listener onto a port it just closed.
	adopt := func(name string, port int) bool {
		mu.Lock()
		defer mu.Unlock()
		if stopped || held[port] != nil || foreign[port] {
			return false
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return false
		}
		held[port] = listener
		safego.Go(log, func() { server.Serve(listener) })
		log(fmt.Sprintf("huddle-router: adopted the 0.0.1 %s port %d", name, port))
		return true
	}
	tick := func() {
		defer func() {
			if reason := recover(); reason != nil {
				log(fmt.Sprintf("huddle-router: recovered: %v", reason))
			}
		}()
		for name, port := range ports {
			if adopt(name, port) {
				continue
			}
			mu.Lock()
			_, isHeld := held[port]
			mu.Unlock()
			if isHeld || !portAccepts(port) {
				// adopted since, or the port went free and the next tick takes it
				continue
			}
			if !stopLegacy(ctx, env, name, port, log) {
				mu.Lock()
				first := !foreign[port]
				foreign[port] = true
				mu.Unlock()
				if first {
					log(fmt.Sprintf("huddle-router: port %d is held by pid-unknown; leaving it", port))
				}
				continue
			}
			deadline := time.Now().Add(legacyWait)
			for time.Now().Before(deadline) {
				if adopt(name, port) {
					break
				}
				select {
				case <-halt:
					return
				case <-ctx.Done():
					return
				case <-time.After(adoptRetry):
				}
			}
		}
	}
	safego.Go(log, func() {
		defer close(finished)
		tick()
		ticker := time.NewTicker(adoptInterval)
		defer ticker.Stop()
		for {
			select {
			case <-halt:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick()
			}
		}
	})
	return func() {
		once.Do(func() {
			close(halt)
			mu.Lock()
			stopped = true
			listeners := make([]net.Listener, 0, len(held))
			for _, listener := range held {
				listeners = append(listeners, listener)
			}
			mu.Unlock()
			for _, listener := range listeners {
				_ = listener.Close()
			}
		})
	}
}
