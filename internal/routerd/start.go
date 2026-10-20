// The daemon's start: the words, the setup wiring and the ensure hook all come here, so one
// probe, one spawn shape and one report line serve them all. The probe is a health GET within
// 300 ms; a start that must act spawns this binary as `router serve`, detached — its output on
// the router log, its own session — and never turns a failed spawn into a failed hook.
package routerd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// healthWait is the probe every start begins with: a daemon answering within it is up.
const healthWait = 300 * time.Millisecond

// startBudget is how long Start waits for a spawned daemon's health answer.
const startBudget = 3 * time.Second

// updateWait is how long a start waits for a takeover by this binary to finish: the old daemon
// keeps serving while the new one takes the socket, so the wait is only the handover's.
const updateWait = 10 * time.Second

// spawnPoll is how often the start re-probes while its daemon comes up.
const spawnPoll = 50 * time.Millisecond

// HealthAnswer is the running router's health answer.
type HealthAnswer struct {
	Name      string   `json:"name"`
	Providers []string `json:"providers"`
}

// Health is the running router's health answer, ok false while nothing answers naming
// huddle-router within 300 ms.
func Health(ctx context.Context, port int) (HealthAnswer, bool) {
	bounded, cancel := context.WithTimeout(ctx, healthWait)
	defer cancel()
	request, err := http.NewRequestWithContext(bounded, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, router.MultiHealthPath), nil)
	if err != nil {
		return HealthAnswer{}, false
	}
	resp, err := (&http.Client{}).Do(request)
	if err != nil {
		return HealthAnswer{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return HealthAnswer{}, false
	}
	var answer HealthAnswer
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || json.Unmarshal(body, &answer) != nil {
		return HealthAnswer{}, false
	}
	if answer.Name != router.MultiName {
		return HealthAnswer{}, false
	}
	return answer, true
}

// StartOptions shape one daemon start.
type StartOptions struct {
	// Env is where the port, the home and the daemon's own environment come from.
	Env domain.EnvLookup
	// Executable is the running binary; the daemon is it, run as `router serve`. Empty takes
	// this process's executable.
	Executable string
	// BudgetMs bounds the wait for a spawned daemon's health answer; 0 is 3 s.
	BudgetMs int64
}

// Start makes sure one daemon serves: a health answer within 300 ms is enough, else one is
// spawned detached and waited for up to the budget. The line names the address and the providers
// the daemon serves. No provider enabled is the quiet answer, and nothing is spawned.
func Start(ctx context.Context, o StartOptions) (string, error) {
	env := o.Env
	if env == nil {
		env = domain.EnvLookup{}
	}
	port, err := router.PortOf(env)
	if err != nil {
		return "", err
	}
	if health, up := Health(ctx, port); up {
		return updatedOrUp(ctx, o, env, port, health)
	}
	names, err := router.ReadEnabled(env)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "huddle-router: no provider is set up", nil
	}
	log, err := startLog(env)
	if err != nil {
		return "", err
	}
	defer log.Close()
	if err := spawnServe(o.executable(), env, log); err != nil {
		return "", err
	}
	budget := o.BudgetMs
	if budget == 0 {
		budget = startBudget.Milliseconds()
	}
	deadline := time.Now().Add(time.Duration(budget) * time.Millisecond)
	for {
		if health, up := Health(ctx, port); up {
			return upLine(port, health), nil
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("huddle-router: the daemon never answered at http://127.0.0.1:%d (its log: %s)",
				port, filepath.Join(router.HomeOf(env), "router.log"))
		}
		time.Sleep(spawnPoll)
	}
}

// Wake is the ensure hook's start: a provider recorded in providers.json gets the same probe and
// detached spawn as Start, never waited for — the next hook finds the daemon up. It is silent,
// never fails, and does nothing for a provider that is not recorded.
func Wake(ctx context.Context, o StartOptions, name string) {
	defer func() { _ = recover() }()
	env := o.Env
	if env == nil {
		env = domain.EnvLookup{}
	}
	names, err := router.ReadEnabled(env)
	if err != nil || !slices.Contains(names, name) {
		return
	}
	port, err := router.PortOf(env)
	if err != nil {
		return
	}
	if _, up := Health(ctx, port); up {
		return
	}
	log, err := startLog(env)
	if err != nil {
		return
	}
	defer log.Close()
	_ = spawnServe(o.executable(), env, log)
}

// RouterKeyCheck is setup's router key line: the daemon's health answers once it serves the
// provider, waiting up to waitMs (8 s when 0) for a reload that is still catching up. The key
// itself is setup's own `key` line.
func RouterKeyCheck(ctx context.Context, provider domain.Provider, env domain.EnvLookup, waitMs int64) (string, error) {
	port, err := router.PortOf(env)
	if err != nil {
		return "", err
	}
	if waitMs == 0 {
		waitMs = 8000
	}
	deadline := time.Now().Add(time.Duration(waitMs) * time.Millisecond)
	for {
		health, up := Health(ctx, port)
		if up && slices.Contains(health.Providers, provider.Name) {
			return "huddle-router serves " + provider.Display, nil
		}
		if !time.Now().Before(deadline) {
			if up {
				return "", fmt.Errorf("huddle-router serves %s, not %s",
					strings.Join(health.Providers, ", "), provider.Name)
			}
			return "", fmt.Errorf("huddle-router is not running on port %d", port)
		}
		time.Sleep(spawnPoll)
	}
}

// upLine is the one line a script reads: where the router answers and what it serves.
func upLine(port int, health HealthAnswer) string {
	return fmt.Sprintf("huddle-router: http://127.0.0.1:%d (%s)", port, strings.Join(health.Providers, ", "))
}

// executable is the binary the daemon is spawned from: the options', else this process's.
func (o StartOptions) executable() string {
	if o.Executable != "" {
		return o.Executable
	}
	return selfExecutable()
}

// updatedOrUp is a start's answer when a healthy daemon already serves: when the daemon's
// record names another executable than this one, this binary replaces the running one — a
// takeover serves the same socket at once and the daemon under it drains — and the line names
// the handover. The same executable is the quiet address line as always.
func updatedOrUp(ctx context.Context, o StartOptions, env domain.EnvLookup, port int, health HealthAnswer) (string, error) {
	record, ok := readRecord(env)
	if !ok || record.Executable == "" || record.Executable == o.executable() {
		return upLine(port, health), nil
	}
	old := record.PID
	log, err := startLog(env)
	if err != nil {
		return "", err
	}
	defer log.Close()
	if err := spawnServeArgs(o.executable(), env, log, "--takeover"); err != nil {
		return "", err
	}
	deadline := time.Now().Add(updateWait)
	for {
		if fresh, ok := readRecord(env); ok && fresh.PID > 0 && fresh.PID != old {
			return fmt.Sprintf("huddle-router: updated (pid %d → %d)", old, fresh.PID), nil
		}
		if !time.Now().Before(deadline) {
			return "", fmt.Errorf("huddle-router: the daemon (pid %d) never handed over to %s (its log: %s)",
				old, o.executable(), filepath.Join(router.HomeOf(env), "router.log"))
		}
		time.Sleep(spawnPoll)
	}
}

// startLog opens the router log the spawned daemon's output appends to.
func startLog(env domain.EnvLookup) (*os.File, error) {
	logFile := filepath.Join(router.HomeOf(env), "router.log")
	if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// spawnServe runs the daemon detached: its output on the router log, its own session, so it
// outlives this command and no signal of the caller's ends it except the one delivered to it.
func spawnServe(executable string, env domain.EnvLookup, log *os.File) error {
	return spawnServeArgs(executable, env, log)
}

// spawnServeArgs runs the daemon detached with extra serve flags — a takeover start passes
// --takeover, so the new binary takes the running socket over instead of failing on the lock.
func spawnServeArgs(executable string, env domain.EnvLookup, log *os.File, args ...string) error {
	// A Go test binary is never the router: started as `router serve` it re-runs its own suite,
	// detached and with the environment of the test that reached here — on a developer machine,
	// the owner's real home.
	if process.IsTestBinary(executable) {
		return fmt.Errorf("huddle-router: %s is a test binary, not huddle; the daemon is not started", filepath.Base(executable))
	}
	argv := append([]string{"router", "serve"}, args...)
	cmd := exec.Command(executable, argv...)
	cmd.Env = environ(env)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

// environ is the daemon's environment: the caller's own, so the overrides and keys it resolved
// reach the lanes.
func environ(env domain.EnvLookup) []string {
	out := make([]string, 0, len(env))
	for name, value := range env {
		out = append(out, name+"="+value)
	}
	return out
}
