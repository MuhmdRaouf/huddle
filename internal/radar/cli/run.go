// Package cli holds the radar command line — start, stop, status, url, open and watch — with
// every effect injected, so the logic runs under test without processes or sockets. The huddle
// hub serves radar in process, so the words only name its page: start and open reach the hub
// through EnsureHub (starting it when the huddle home is set up), stop explains that /huddle:stop
// is the way, and status and url report where the hub's radar page lives. `start --foreground`
// stays the one way to serve a dashboard in this process; no code path spawns it any more.
// `watch` polls a running server's alerts and prints one line per new alert id. Exit codes:
// 0 ok, 1 usage, 3 a watch whose server is gone, 6 the hub is not there.
package cli

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
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
	// Env resolves the state dir everything reads.
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
	IsAlive    func(pid int64) bool
	// OpenBrowser hands one url to the platform's browser opener.
	OpenBrowser func(url string)
	// Now is the epoch-milliseconds clock.
	Now func() int64
	// SinceMs is the default --since window.
	SinceMs int64
	// Hub answers the base url of the huddle hub when one answers health; false when none
	// does. url and status report its radar page.
	Hub func() (string, bool)
	// EnsureHub starts the hub when the huddle home is set up and answers its base url; false
	// with the message that says why not. start and open take this one way in.
	EnsureHub func(budgetMS int) (string, bool, string)
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

// ensureBudgetMs is what start and open grant EnsureHub: the hub's own start
// runs inside it, detached, and the answer comes back within it too.
const ensureBudgetMs = 35_000

// exitNotReady is the exit code for a word whose hub is not there.
const exitNotReady = 6

// usage is the one line every misuse prints.
const usage = "usage: radar start|stop|status|url|open|watch [--port N] [--since 24h]"

// healthy fetches one recorded server's health endpoint.
func healthy(deps Deps, info *server.ServerInfo) FetchResult {
	return deps.FetchJSON(fmt.Sprintf("http://127.0.0.1:%d/api/health", info.Port))
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
		return runEnsure(deps, false)
	case "open":
		return runEnsure(deps, true)
	case "stop":
		return runStop(deps)
	case "status":
		return runStatus(deps)
	case "watch":
		return runWatch(ctx, deps)
	case "url":
		return runURL(deps)
	case "help", "--help", "-h":
		deps.Out(usage)
		deps.Out("start names the huddle hub's radar page, starting the hub when the huddle home is set up")
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

// radarPage is the hub's radar page for one base url.
func radarPage(base string) string {
	return base + "/#/radar"
}

// runEnsure is start and open: name the hub's radar page, starting the hub when the huddle home
// is set up. open hands the page to the browser as well. Nothing here serves anything itself.
func runEnsure(deps Deps, open bool) int {
	base, ok, msg := "", false, ""
	if deps.EnsureHub != nil {
		base, ok, msg = deps.EnsureHub(ensureBudgetMs)
	}
	if !ok {
		deps.Err("radar: " + msg)
		return exitNotReady
	}
	page := radarPage(base)
	deps.Out("radar: in the huddle hub at " + page)
	if open {
		deps.OpenBrowser(page)
	}
	return 0
}

// runStop only points at the hub that serves radar: the hub is the one server
// this edition runs, and /huddle:stop is what ends it.
func runStop(deps Deps) int {
	deps.Out("radar: runs inside the huddle hub; /huddle:stop stops it")
	return 0
}

// notOnHub is what url and status print when no hub answers: the words report,
// they never start.
const notOnHub = "radar: the huddle hub is not running"

// hubPage answers the hub's radar page when a hub answers; ok false and the
// not-running line when none does.
func hubPage(deps Deps) (string, bool) {
	if deps.Hub == nil {
		return "", false
	}
	base, ok := deps.Hub()
	if !ok {
		return "", false
	}
	return radarPage(base), true
}

// runStatus reports the hub's radar page.
func runStatus(deps Deps) int {
	page, ok := hubPage(deps)
	if !ok {
		deps.Out(notOnHub)
		return exitNotReady
	}
	deps.Out("radar: in the huddle hub at " + page)
	return 0
}

// runURL prints the hub's radar page's address.
func runURL(deps Deps) int {
	page, ok := hubPage(deps)
	if !ok {
		deps.Out(notOnHub)
		return exitNotReady
	}
	deps.Out("radar: " + page)
	return 0
}

// runForeground is the hub's own way to serve a dashboard in this process (and
// the shape the tests drive): it serves until its context ends. The context it
// takes never ends in production — StartApp's own SIGTERM handling stops the
// process — so the wait holds the process for its whole life.
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
