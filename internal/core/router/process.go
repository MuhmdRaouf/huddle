// Retiring, stopping and identifying the per-provider routers a 0.0.1 setup left behind:
// RetireRouter is what the migration and setup --remove run (the router keeps serving the
// sessions already open), StopRouter is the hard stop, and the command-line checks keep every
// signal away from a pid that is not a router of ours.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// RegistryEntryFor is this plugin's registry entry: its resolved catalog ids (env overrides
// included) and the pid when it is known.
func RegistryEntryFor(provider domain.Provider, env domain.EnvLookup, pid *int64) RegisteredRouter {
	claim := domain.ResolveProvider(provider, env).ModelClaim()
	return RegisteredRouter{
		Name:          provider.Name,
		Port:          int64(domain.RouterPort(provider, env)),
		ModelPrefixes: claim.Prefixes,
		CatalogIds:    claim.IDs,
		Pid:           pid,
		UpdatedAt:     isoNow(),
	}
}

// ── loopback calls ──────────────────────────────────────────────────────────────────────────────────────────────────

// probeClient asks a router port, pooling its connections as any client does: a probe whose
// budget runs out has its context canceled, and a canceled request's connection is closed rather
// than reused, so a silent port still cannot leak a connection into the next probe.
var probeClient = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}}

// fetchBody makes one loopback call and reads its whole body; every failure is one error.
func fetchBody(ctx context.Context, method, target, token string, body []byte) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		request.Header.Set(TokenHeader, token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	answer, err := probeClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	text, readErr := io.ReadAll(answer.Body)
	closeErr := answer.Body.Close()
	return answer.StatusCode, text, errors.Join(readErr, closeErr)
}

// controlAnswer is one control call's answer: ok when the call reached the router and its body
// parsed.
type controlAnswer struct {
	OK *bool
}

// control makes one control call to a router; reached is false when it cannot be reached.
func control(ctx context.Context, port int, healthPath, token, action string, body []byte, ms int64) (controlAnswer, bool) {
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
	defer cancel()
	_, text, err := fetchBody(callCtx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s/%s", port, healthPath, action), token, body)
	if err != nil {
		return controlAnswer{}, false
	}
	var parsed struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(text, &parsed) != nil {
		return controlAnswer{}, false
	}
	return controlAnswer{OK: parsed.OK}, true
}

// ── who a pid is ────────────────────────────────────────────────────────────────────────────────────────────────────

// CommandLineOf is a pid's command line (/proc, else `ps`), or false when neither can say.
func CommandLineOf(ctx context.Context, pid int64) (string, bool) {
	line, ok := procCmdline(fmt.Sprintf("/proc/%d/cmdline", pid))
	if !ok {
		line, ok = psCmdline(ctx, pid)
	}
	return line, ok
}

// procCmdline is one /proc cmdline file: NUL-separated argv as one line (the trailing NUL becomes
// a trailing space).
func procCmdline(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return strings.Join(strings.Split(string(data), "\x00"), " "), true
}

// psCmdline is the ps fallback: the pid's command as `ps -p <pid> -o command=` reports it.
func psCmdline(ctx context.Context, pid int64) (string, bool) {
	timeoutMs := int64(2000)
	answer := process.RunToolSync(ctx, "ps", []string{"-p", strconv.FormatInt(pid, 10), "-o", "command="}, process.ToolOptions{TimeoutMs: &timeoutMs})
	if !process.Succeeded(answer) {
		return "", false
	}
	return strings.TrimSpace(string(answer.Stdout)), true
}

// signalPid sends the signal to one pid and reports whether a process is there to receive it:
// os.FindProcess always hands a process back, and a signal to one that is gone is a no-op.
func signalPid(pid int64, signal string) bool {
	target, _ := os.FindProcess(int(pid))
	if err := target.Signal(signalName(signal)); err != nil {
		// Already gone.
		_ = target.Kill()
	}
	return process.IsAlive(pid)
}

// signalName is the signal's name as the stop names it.
func signalName(signal string) syscall.Signal {
	switch signal {
	case "SIGKILL":
		return syscall.SIGKILL
	case "SIGTERM":
		return syscall.SIGTERM
	default:
		return syscall.SIGKILL
	}
}

// IsOurProcess is true when the pid runs one of this plugin's router processes: this binary as
// plugin-core (`<name> router run`, `<name> passthrough`), or the node bundle an earlier release
// still runs (kept so a migration can stop it). A nil read is the real command line.
func IsOurProcess(pid int64, name string, read func(int64) (string, bool)) bool {
	if read == nil {
		read = func(pid int64) (string, bool) { return CommandLineOf(context.Background(), pid) }
	}
	command, ok := read(pid)
	if !ok {
		return false
	}
	return strings.Contains(command, name+" router") ||
		strings.Contains(command, name+" passthrough") ||
		strings.Contains(command, name+"-router.js") ||
		strings.Contains(command, name+"-passthrough.js")
}

// ── stop and retire ─────────────────────────────────────────────────────────────────────────────────────────────────

// StopOptions is what one router stop needs.
type StopOptions struct {
	Provider    domain.Provider
	Env         domain.EnvLookup
	StateRoot   string
	CommandLine func(pid int64) (string, bool)
}

// isOurPid is IsOurProcess with the options' command line reader (nil is the real one).
func isOurPid(ctx context.Context, o StopOptions, pid int64) bool {
	read := o.CommandLine
	if read == nil {
		read = func(pid int64) (string, bool) { return CommandLineOf(ctx, pid) }
	}
	return IsOurProcess(pid, o.Provider.Name, read)
}

// StopRouter stops the router (a retired one included): SIGTERM only a pid whose command line is
// one of ours; a stale pid file just goes.
func StopRouter(ctx context.Context, o StopOptions) string {
	record, hasRunning := ReadPidFile(ctx, o.StateRoot)
	if !hasRunning {
		record, hasRunning = ReadRetiredFile(ctx, o.StateRoot)
	}
	line := "stopped (was not running)"
	if hasRunning {
		if isOurPid(ctx, o, record.Pid) {
			if signalPid(record.Pid, "SIGTERM") {
				line = fmt.Sprintf("stopped (pid %d)", record.Pid)
			}
		} else {
			line = fmt.Sprintf("stopped (pid %d is not our router; removed the stale record)", record.Pid)
		}
	}
	RemovePidFile(o.StateRoot)
	RemoveRetiredFile(o.StateRoot)
	_ = os.Remove(domain.NewStateLayout(o.StateRoot).RouterExit)
	_ = os.Remove(domain.NewStateLayout(o.StateRoot).RouterLock)
	RemoveRegistryEntry(o.Env, o.Provider.Name)
	return line
}

// RetireOptions is the stop's own options under the retire step.
type RetireOptions struct {
	StopOptions
}

// retiredLineFor is the remove report's router line for a retired router.
func retiredLineFor(pid int64) string {
	return fmt.Sprintf("retired (pid %d): keeps Claude working in open sessions, exits when they close", pid)
}

// RetireRouter is setup --remove's router step. Claude Code sessions keep the base URL they
// started with, so stopping the router would cut every open session off: the router is told to
// retire instead (the retire control endpoint), and its record moves to `router.retired` so the
// hooks stop bringing it back while a later setup can still take the port over. A router that is
// already retired is left as it is. For the emergency passthrough, or when the retire call fails,
// the router is stopped instead.
func RetireRouter(ctx context.Context, o RetireOptions) string {
	record, ok := ReadPidFile(ctx, o.StateRoot)
	if !ok {
		if retired, hasRetired := ReadRetiredFile(ctx, o.StateRoot); hasRetired && isOurPid(ctx, o.StopOptions, retired.Pid) {
			return retiredLineFor(retired.Pid)
		}
		return StopRouter(ctx, o.StopOptions)
	}
	if record.Mode == PidModeEmergency || !isOurPid(ctx, o.StopOptions, record.Pid) {
		return StopRouter(ctx, o.StopOptions)
	}
	answer, reached := control(ctx, int(record.Port), o.Provider.Router.HealthPath, record.Token, "retire", []byte("{}"), 2000)
	if !reached || answer.OK == nil || !*answer.OK {
		return StopRouter(ctx, o.StopOptions)
	}
	if err := RetirePidFile(ctx, o.StateRoot, record); err != nil {
		return StopRouter(ctx, o.StopOptions)
	}
	_ = os.Remove(domain.NewStateLayout(o.StateRoot).RouterLock)
	RemoveRegistryEntry(o.Env, o.Provider.Name)
	return retiredLineFor(record.Pid)
}
