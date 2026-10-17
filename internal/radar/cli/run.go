// Package cli holds the radar command line — start, stop, status, url, open and watch — with
// every effect injected, so the logic runs under test without processes or sockets. `start`
// reuses a healthy server recorded in server.json; otherwise it spawns itself detached — the
// child binds the port and writes server.json itself, which makes the parent's view race-free —
// then the parent polls health briefly and prints exactly one line. `watch` polls the running
// server's alerts and prints one line per new alert id. Exit codes: 0 ok, 1 start failed,
// 3 wanted-a-server-and-found-none.
package cli

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/server"
)

// FetchResult is one fetched endpoint: OK says it answered, Body its JSON object (nil when the
// answer was not an object).
type FetchResult struct {
	OK   bool
	Body map[string]any
}

// Deps is every effect one command run can have; the process shim builds the real ones, the
// tests record them.
type Deps struct {
	// Env resolves the state dir everything reads and writes.
	Env domain.EnvLookup
	// Version prints in the status line.
	Version string
	Out     func(line string)
	Err     func(line string)
	// FetchJSON gets one endpoint with a short timeout; never fails — network trouble is OK false.
	FetchJSON func(url string) FetchResult
	// Sleep waits between polls, in milliseconds.
	Sleep func(ms int64)
	// ShouldStop ends the watch loop quietly; the real one never does.
	ShouldStop func() bool
	// SpawnDetached starts this binary again with the given sub-args; 0 when the start failed.
	SpawnDetached func(args []string) int64
	// Signal delivers the stop signal to one pid; true when a process took it.
	Signal func(pid int64) bool
	// Command answers a pid's command line as `ps -o command=` prints it; false when the pid
	// is gone or ps cannot name it.
	Command func(pid int64) (string, bool)
	IsAlive func(pid int64) bool
	// OpenBrowser hands one url to the platform's browser opener.
	OpenBrowser func(url string)
	// Now is the epoch-milliseconds clock the start budget reads.
	Now func() int64
	// SinceMs is the default --since window.
	SinceMs int64
}

// ParsedArgs is one command line read: the command, its flags, or the first complaint.
type ParsedArgs struct {
	Command string
	// Port pins one port; 0 leaves it open.
	Port int
	// SinceMs is how far back the dashboard reads.
	SinceMs int64
	// SinceGiven says --since was given (not the default): a running server cannot take it.
	SinceGiven bool
	Foreground bool
	// Bad carries the first complaint; nil when the line parsed.
	Bad *string
}

// flagResult is one parsed flag: either the next argument index, or the complaint.
type flagResult struct {
	ok   bool
	next int
	bad  string
}

// applyPort reads one --port value: a whole port number in the live range.
func applyPort(parsed *ParsedArgs, argv []string, i int) flagResult {
	value, exists := "", false
	if i+1 < len(argv) {
		value, exists = argv[i+1], true
	}
	port, ok := parseInt10(value)
	if !ok || port < 1024 || port > 65535 {
		return flagResult{bad: "--port needs a number in 1024..65535, got: " + shownValue(value, exists)}
	}
	parsed.Port = int(port)
	return flagResult{ok: true, next: i + 2}
}

// applySince reads one --since value: a duration like 30m, 24h or 7d (a bare number is hours).
func applySince(parsed *ParsedArgs, argv []string, i int) flagResult {
	value, exists := "", false
	if i+1 < len(argv) {
		value, exists = argv[i+1], true
	}
	ms, ok := ParseSince(value)
	if !ok {
		return flagResult{bad: "--since needs a duration like 30m, 24h or 7d, got: " + shownValue(value, exists)}
	}
	parsed.SinceMs = ms
	parsed.SinceGiven = true
	return flagResult{ok: true, next: i + 2}
}

// shownValue is what a complaint names: the argument, or "nothing" when the flag ended the line.
func shownValue(value string, exists bool) string {
	if !exists {
		return "nothing"
	}
	return value
}

// applyFlag parses one argument, answering where parsing continues.
func applyFlag(parsed *ParsedArgs, argv []string, i int) flagResult {
	arg := ""
	if i < len(argv) {
		arg = argv[i]
	}
	switch arg {
	case "--foreground":
		parsed.Foreground = true
		return flagResult{ok: true, next: i + 1}
	case "--port":
		return applyPort(parsed, argv, i)
	case "--since":
		return applySince(parsed, argv, i)
	default:
		return flagResult{bad: "unknown argument: " + arg}
	}
}

// ParseArgs reads `start|stop|status|url|open|watch [--port N] [--since 24h] [--foreground]`;
// Bad carries the first complaint, if any.
func ParseArgs(argv []string, defaultSinceMs int64) ParsedArgs {
	command := ""
	if len(argv) > 0 {
		command = argv[0]
	}
	parsed := ParsedArgs{Command: command, SinceMs: defaultSinceMs}
	for i := 1; i < len(argv); {
		result := applyFlag(&parsed, argv, i)
		if !result.ok {
			parsed.Bad = &result.bad
			return parsed
		}
		i = result.next
	}
	return parsed
}

// parseInt10 reads a leading decimal integer the way parseInt(value, 10) does: whitespace
// trimmed at both ends, an optional sign, then digits, and anything after the digits ignored;
// ok is false when no digits were there.
func parseInt10(value string) (int64, bool) {
	text := jsfmt.Trim(value)
	sign := int64(1)
	if strings.HasPrefix(text, "-") {
		sign = -1
		text = text[1:]
	} else if strings.HasPrefix(text, "+") {
		text = text[1:]
	}
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	number, err := strconv.ParseInt(text[:end], 10, 64)
	if err != nil {
		return 0, false // more digits than an int64 holds is no port either
	}
	return sign * number, true
}

// unitMs maps a --since unit suffix to milliseconds; the fallback is hours, so a bare number
// means hours.
var unitMs = map[string]int64{"s": 1_000, "m": 60_000, "h": 3_600_000, "d": 86_400_000}

// sincePattern matches `Ns`, `Nm`, `Nh`, `Nd` case-insensitively, with the unit's word allowed
// after it (`2 hrs`); the space class is JavaScript's \s, not RE2's.
var sincePattern = regexp.MustCompile(`(?i)^(\d+)[` + jsfmt.SpaceSet + `]*([smhd]?)[a-z]*$`)

// ParseSince reads one --since duration; ok is false when the text is unusable.
func ParseSince(raw string) (int64, bool) {
	match := sincePattern.FindStringSubmatch(jsfmt.Trim(raw))
	if match == nil {
		return 0, false
	}
	amount, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || amount <= 0 {
		return 0, false
	}
	unit := strings.ToLower(match[2]) // the capture is "" when no unit was given
	if unit == "" {
		unit = "h"
	}
	return amount * unitMs[unit], true
}

const (
	startTimeoutMs = 15_000
	pollIntervalMs = 250
)

// usage is the one line every misuse prints.
const usage = "usage: radar start|stop|status|url|open|watch [--port N] [--since 24h]"

// healthy fetches one recorded server's health endpoint.
func healthy(deps Deps, info *server.ServerInfo) FetchResult {
	return deps.FetchJSON(fmt.Sprintf("http://127.0.0.1:%d/api/health", info.Port))
}

// fmtUptime is the compact uptime a status line shows: seconds, then minutes, then hours.
func fmtUptime(ms int64) string {
	seconds := int64(math.Round(float64(ms) / 1000))
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return strconv.FormatInt(seconds, 10) + "s"
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm%ds", minutes, seconds%60)
	}
	return fmt.Sprintf("%dh%dm", minutes/60, minutes%60)
}

// RunCli runs one command line — argv with the command first — and answers the process exit
// code.
func RunCli(ctx context.Context, argv []string, deps Deps) int {
	parsed := ParseArgs(argv, deps.SinceMs)
	if parsed.Bad != nil {
		deps.Err("radar: " + *parsed.Bad)
		deps.Err(usage)
		return 1
	}
	switch parsed.Command {
	case "start":
		if parsed.Foreground {
			return runForeground(ctx, parsed, deps)
		}
		return runStart(ctx, parsed, deps)
	case "stop":
		return runStop(ctx, deps)
	case "status":
		return runStatus(ctx, deps)
	case "watch":
		return runWatch(ctx, deps)
	case "url":
		return runURL(ctx, deps)
	case "open":
		// "start if needed, then open": a first /radar:open must not answer "not running"
		started := runStart(ctx, parsed, deps)
		if started == 0 {
			if info := server.ReadServerInfo(ctx, deps.Env); info != nil {
				deps.OpenBrowser(info.URL)
			}
		}
		return started
	case "help", "--help", "-h":
		deps.Out(usage)
		deps.Out("start runs the dashboard server on 127.0.0.1 on a random port unless --port is given")
		return 0
	default:
		deps.Err("radar: unknown command: " + commandName(parsed.Command))
		deps.Err(usage)
		return 1
	}
}

// commandName is what the unknown-command error names; an empty argv shows as "(none)".
func commandName(command string) string {
	if command == "" {
		return "(none)"
	}
	return command
}

// reportRunning prints what a healthy recorded server owes: its url, and the flags it cannot
// take until it stops.
func reportRunning(ctx context.Context, parsed ParsedArgs, deps Deps, recorded *server.ServerInfo) {
	paths.RememberAutostart(ctx, deps.Env, true)
	deps.Out("radar: " + recorded.URL)
	asked := []string{}
	if parsed.SinceGiven {
		asked = append(asked, "--since "+sinceArg(parsed.SinceMs))
	}
	if parsed.Port != 0 && parsed.Port != recorded.Port {
		asked = append(asked, "--port "+strconv.Itoa(parsed.Port))
	}
	if len(asked) > 0 {
		deps.Out("radar: already running; " + strings.Join(asked, " and ") + " applies after /radar:stop")
	}
}

// reportStarted prints what a start this run launched owes: the url, and a word when the child
// could not have its saved port.
func reportStarted(deps Deps, info *server.ServerInfo, before int, hadBefore bool) {
	deps.Out("radar: " + info.URL)
	if hadBefore && before != info.Port {
		deps.Out(fmt.Sprintf("radar: port %d was taken; moved to %d", before, info.Port))
	}
}

// runStart brings the dashboard up: a recorded server that still answers health is reused, a
// dead record is cleared, and anything else launches a detached child this binary runs as
// `start --foreground`, whose server.json the parent follows until it answers health.
func runStart(ctx context.Context, parsed ParsedArgs, deps Deps) int {
	recorded := server.ReadServerInfo(ctx, deps.Env)
	if recorded != nil && deps.IsAlive(recorded.Pid) {
		if healthy(deps, recorded).OK {
			reportRunning(ctx, parsed, deps, recorded)
			return 0
		}
		server.RemoveServerInfo(ctx, deps.Env)
	}
	// Remembered before the launch, not once it answers: a stop in between (the server takes a
	// moment) must win.
	paths.RememberAutostart(ctx, deps.Env, true)
	before, hadBefore := 0, false
	if parsed.Port == 0 {
		before, hadBefore = paths.SavedPort(ctx, deps.Env)
	}
	args := []string{"start", "--foreground", "--since", sinceArg(parsed.SinceMs)}
	if parsed.Port != 0 {
		args = append(args, "--port", strconv.Itoa(parsed.Port))
	}
	deps.SpawnDetached(args)
	deadline := deps.Now() + startTimeoutMs
	for deps.Now() < deadline {
		deps.Sleep(pollIntervalMs)
		info := server.ReadServerInfo(ctx, deps.Env)
		if info == nil {
			continue
		}
		if healthy(deps, info).OK {
			reportStarted(deps, info, before, hadBefore)
			return 0
		}
	}
	deps.Err(fmt.Sprintf("radar: server did not become healthy within %ds", startTimeoutMs/1000))
	return 1
}

// sinceArg rounds ms back to a compact flag value the child re-parses to the same window.
func sinceArg(ms int64) string {
	switch {
	case ms%86_400_000 == 0:
		return strconv.FormatInt(ms/86_400_000, 10) + "d"
	case ms%3_600_000 == 0:
		return strconv.FormatInt(ms/3_600_000, 10) + "h"
	case ms%60_000 == 0:
		return strconv.FormatInt(ms/60_000, 10) + "m"
	default:
		return strconv.FormatInt(int64(math.Round(float64(ms)/1000)), 10) + "s"
	}
}

// runForeground is the detached child: it serves until SIGTERM. The context it takes never ends
// in production — StartApp's own SIGTERM handling stops the process — so the wait holds the
// process for its whole life; a context the caller cancels unblocks it (and stops the app) for
// runs inside another process.
func runForeground(ctx context.Context, parsed ParsedArgs, deps Deps) int {
	app, err := server.StartApp(ctx, server.AppOptions{
		Env:     deps.Env,
		Version: deps.Version,
		SinceMs: parsed.SinceMs,
		Port:    parsed.Port,
	})
	if err != nil {
		return 1
	}
	deps.Out("radar: " + app.URL)
	if app.MovedFrom != 0 {
		deps.Out(fmt.Sprintf("radar: port %d was taken; moved to %d", app.MovedFrom, app.Port))
	}
	<-ctx.Done()
	app.Stop()
	return 0
}

// radarServer matches the command line of this binary serving the dashboard — the detached
// `start --foreground` a start leaves behind. A stale record may name any process, so only a
// radar server is ours to signal. The trailing newline is ps's own.
var radarServer = regexp.MustCompile(`(?:^|/)radar start(?:[ \n]|$)`)

// huddleServer matches a huddle server's command line. While huddle owns radar's lock, the pid
// server.json names is huddle's, and the hub it serves is not radar's to end.
var huddleServer = regexp.MustCompile(`(?:^|/)huddle serve(?:[ \n]|$)`)

// huddleStopLine is the one line a stop prints when huddle owns the lock: how to stop the hub,
// because that is what the recorded pid runs.
const huddleStopLine = "radar runs inside huddle here; stop it with: huddle down"

// commandLineOf reads the recorded pid's command line; a stop without the effect reads nothing,
// so no pid is ever signalled on a look it could not make.
func commandLineOf(deps Deps, pid int64) (string, bool) {
	if deps.Command == nil {
		return "", false
	}
	return deps.Command(pid)
}

// runStop stops the recorded server and forgets it; nothing recorded is a no-op. The pid is
// signalled only when its command line is a radar server: a record huddle owns names huddle —
// the hub stays, with the line that stops it — and a reboot may have given the pid to any other
// process, which is not radar's to signal either.
func runStop(ctx context.Context, deps Deps) int {
	info := server.ReadServerInfo(ctx, deps.Env)
	if info == nil {
		paths.RememberAutostart(ctx, deps.Env, false)
		deps.Out("radar: not running")
		return 0
	}
	if deps.IsAlive(info.Pid) {
		command, _ := commandLineOf(deps, info.Pid)
		switch {
		case radarServer.MatchString(command):
			deps.Signal(info.Pid)
		case huddleServer.MatchString(command):
			deps.Out(huddleStopLine)
			return 0
		}
	}
	server.RemoveServerInfo(ctx, deps.Env)
	paths.RememberAutostart(ctx, deps.Env, false)
	deps.Out("radar: stopped")
	return 0
}

// notRunning is what a wanted-but-absent server prints, with the command that starts one.
const notRunning = "radar: not running — /radar:start (or /radar:open) starts it"

func runStatus(ctx context.Context, deps Deps) int {
	info := server.ReadServerInfo(ctx, deps.Env)
	if info == nil || !deps.IsAlive(info.Pid) {
		deps.Out(notRunning)
		return 3
	}
	health := healthy(deps, info)
	if !health.OK {
		deps.Out(notRunning)
		return 3
	}
	sessions := int64(0)
	if counted, ok := numberField(health.Body, "sessions"); ok {
		sessions = int64(counted)
	}
	uptime := deps.Now() - int64(info.StartedAt)
	if up, ok := numberField(health.Body, "uptimeMs"); ok {
		uptime = int64(up)
	}
	plural := "s"
	if sessions == 1 {
		plural = ""
	}
	deps.Out("radar: " + info.URL)
	deps.Out(fmt.Sprintf("  up %s, %d session%s tracked, v%s", fmtUptime(uptime), sessions, plural, deps.Version))
	return 0
}

// numberField reads one JSON number field, the only shape typeof n === "number" admits.
func numberField(body map[string]any, key string) (float64, bool) {
	value, ok := body[key].(float64)
	return value, ok
}

func runURL(ctx context.Context, deps Deps) int {
	info := server.ReadServerInfo(ctx, deps.Env)
	if info == nil {
		deps.Out(notRunning)
		return 3
	}
	if !healthy(deps, info).OK {
		deps.Out(notRunning)
		return 3
	}
	deps.Out("radar: " + info.URL)
	return 0
}

// Poll cadence and failure budget of `watch`: three missed polls in a row mean the server is
// gone.
const (
	watchIntervalMs = 15_000
	watchMaxMisses  = 3
)

// kindLabel holds the words the dashboard's alert strip shows for each kind; an unknown kind
// prints as itself.
var kindLabel = map[string]string{
	"stuck":       "Stuck",
	"loop":        "Loop",
	"retry_storm": "Retry storm",
	"context":     "Context nearly full",
	"budget":      "Budget",
}

// watchedAlert is one alert as `watch` prints it: the id it dedupes on, and the three parts of
// its line.
type watchedAlert struct {
	id        string
	kind      string
	project   string
	sessionID string
	message   string
}

// readAlerts reads the alerts of an /api/alerts body leniently; nil when the answer is not an
// alert list.
func readAlerts(body map[string]any) []watchedAlert {
	raw, ok := body["alerts"].([]any)
	if !ok {
		return nil
	}
	alerts := []watchedAlert{}
	for _, item := range raw {
		alert, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, idOK := alert["id"].(string)
		kind, kindOK := alert["kind"].(string)
		detail, detailOK := alert["detail"].(string)
		if !idOK || !kindOK || !detailOK {
			continue
		}
		project, _ := alert["project"].(string)
		sessionID, _ := alert["sessionId"].(string)
		alerts = append(alerts, watchedAlert{
			id: id, kind: kind, project: project, sessionID: sessionID, message: detail,
		})
	}
	return alerts
}

// whereText is the middle of a watch line: the project, else a short session id, else the scope
// the alert covers.
func whereText(alert watchedAlert) string {
	if alert.project != "" {
		return alert.project
	}
	if alert.sessionID != "" {
		return jsfmt.UTF16Slice(alert.sessionID, 0, 8)
	}
	if alert.kind == "budget" {
		return "All sessions"
	}
	return "Router"
}

// printNewAlerts prints one line per alert id not seen before.
func printNewAlerts(deps Deps, alerts []watchedAlert, seen map[string]struct{}) {
	for _, alert := range alerts {
		if _, duplicate := seen[alert.id]; duplicate {
			continue
		}
		seen[alert.id] = struct{}{}
		label, known := kindLabel[alert.kind]
		if !known {
			label = alert.kind
		}
		deps.Out("radar: " + label + " · " + whereText(alert) + " · " + alert.message)
	}
}

// runWatch polls the running server's alerts and prints one line per alert id not seen before,
// alerts already current at the first poll included. Quiet otherwise; three missed polls in a
// row end it with exit 0, since the server it watched is gone and a Monitor would only start
// the watch again.
func runWatch(ctx context.Context, deps Deps) int {
	info := server.ReadServerInfo(ctx, deps.Env)
	if info == nil || !deps.IsAlive(info.Pid) {
		deps.Out("radar: not running")
		return 3
	}
	if !healthy(deps, info).OK {
		deps.Out("radar: not running")
		return 3
	}
	deps.Out("radar: watching alerts at " + info.URL)
	seen := map[string]struct{}{}
	misses := 0
	for !deps.ShouldStop() {
		deps.Sleep(watchIntervalMs)
		result := deps.FetchJSON(info.URL + "/api/alerts")
		var alerts []watchedAlert
		if result.OK {
			alerts = readAlerts(result.Body)
		}
		if alerts == nil {
			misses++
			if misses >= watchMaxMisses {
				deps.Out("radar: server stopped")
				return 0
			}
			continue
		}
		misses = 0
		printNewAlerts(deps, alerts, seen)
	}
	return 0
}
