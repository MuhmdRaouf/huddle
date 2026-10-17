// Desktop notifications: macOS `osascript -e 'display notification …'`, Linux `notify-send`
// when it is on PATH, otherwise nothing at all. Each one is started detached with no stdio and
// killed after a few seconds if it hangs; nothing ever waits on it and nothing here fails a
// caller. Each alert notifies once (its id is remembered in <state>/notified.json for a week),
// and one kind + subject at most once per 10 minutes. The text is the alert's kind, project
// folder name and one-line detail: never a prompt, a token, a key or file contents.
package alerts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// Runner starts one OS notifier command.
type Runner func(command string, args []string)

// notifyKinds are the kinds worth a desktop notification.
var notifyKinds = map[shared.AlertKind]bool{
	shared.AlertBudget: true, shared.AlertStuck: true, shared.AlertLoop: true,
}

const (
	rateMs     = 10 * 60_000
	rememberMs = 7 * 86_400_000
	killAfter  = 5 * time.Second
)

// NotifiedPath is the notification memory, in radar's state dir.
func NotifiedPath(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(paths.StateDir(ctx, env), "notified.json")
}

// SpawnDetached starts a command and forgets it: its own session, no stdio, a kill timer that
// nothing waits on. A start failure is the notifier's business, never the caller's.
func SpawnDetached(command string, args []string) {
	cmd := exec.Command(command, args...)
	cmd.SysProcAttr = launchAttr()
	if err := cmd.Start(); err != nil {
		return
	}
	kill := time.AfterFunc(killAfter, func() { _ = cmd.Process.Kill() })
	go func() {
		_ = cmd.Wait() // reaps the child without making anyone wait on a notification
		kill.Stop()
	}()
}

// onPath reports whether name sits executable on PATH.
func onPath(env domain.EnvLookup, name string) bool {
	for _, dir := range filepath.SplitList(env["PATH"]) {
		if dir == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if info.Mode().Perm()&0o111 != 0 {
			return true
		}
	}
	return false
}

// AppleScriptText is an AppleScript string literal body: backslashes and quotes escaped,
// control characters gone.
func AppleScriptText(text string) string {
	var printable strings.Builder
	for _, r := range text {
		if r < 32 || r == 127 {
			r = ' '
		}
		printable.WriteRune(r)
	}
	escaped := strings.ReplaceAll(printable.String(), `\`, `\\`)
	return strings.ReplaceAll(escaped, `"`, `\"`)
}

// CommandLine is one notifier program and its arguments.
type CommandLine struct {
	Command string
	Args    []string
}

// NotifyCommand is the command line that shows one notification on this platform (GOOS name),
// or nil where there is none.
func NotifyCommand(platform string, env domain.EnvLookup, title, body string) *CommandLine {
	if platform == "darwin" {
		script := fmt.Sprintf("display notification \"%s\" with title \"%s\"", AppleScriptText(body), AppleScriptText(title))
		return &CommandLine{Command: "osascript", Args: []string{"-e", script}}
	}
	if platform == "linux" && onPath(env, "notify-send") {
		return &CommandLine{Command: "notify-send", Args: []string{"--app-name=Radar", title, body}}
	}
	return nil
}

// titles is each kind's notification title.
var titles = map[shared.AlertKind]string{
	shared.AlertBudget:     "Radar: budget",
	shared.AlertStuck:      "Radar: session looks stuck",
	shared.AlertLoop:       "Radar: agent is looping",
	shared.AlertRetryStorm: "Radar: retry storm",
	shared.AlertContext:    "Radar: context nearly full",
}

// NotifierOptions shapes a Notifier: the environment, the switch read on every call (the
// dashboard toggle can flip at any moment), and the seams tests replace.
type NotifierOptions struct {
	Env      domain.EnvLookup
	Enabled  func() bool
	Runner   Runner
	Platform string
	Now      func() time.Time
}

// notifiedID is one alert id remembered as notified, and when.
type notifiedID struct {
	id string
	at float64
}

// Notifier shows desktop notifications for the alerts worth one.
type Notifier struct {
	env      domain.EnvLookup
	enabled  func() bool
	runner   Runner
	platform string
	now      func() time.Time

	mu            sync.Mutex
	notified      []notifiedID // in arrival order, the order the file keeps
	known         map[string]int
	lastBySubject map[string]int64
}

// NewNotifier reads its notification memory once; each Consider updates it.
func NewNotifier(ctx context.Context, options NotifierOptions) *Notifier {
	runner := options.Runner
	if runner == nil {
		runner = SpawnDetached
	}
	platform := options.Platform
	if platform == "" {
		platform = runtime.GOOS
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	notified := readNotified(NotifiedPath(ctx, options.Env))
	known := make(map[string]int, len(notified))
	for i, entry := range notified {
		known[entry.id] = i
	}
	return &Notifier{
		env:           options.Env,
		enabled:       options.Enabled,
		runner:        runner,
		platform:      platform,
		now:           now,
		notified:      notified,
		known:         known,
		lastBySubject: map[string]int64{},
	}
}

// Consider notifies about the alerts worth a notification that have not been notified before;
// it returns how many went out.
func (n *Notifier) Consider(ctx context.Context, alerts []shared.Alert) int {
	if !n.enabled() || n.env["RADAR_NOTIFY"] == "0" {
		return 0
	}
	now := n.now().UnixMilli()
	n.mu.Lock()
	defer n.mu.Unlock()
	var fresh []shared.Alert
	for _, alert := range alerts {
		if !notifyKinds[alert.Kind] {
			continue
		}
		if _, seen := n.known[alert.ID]; seen {
			continue
		}
		fresh = append(fresh, alert)
	}
	if len(fresh) == 0 {
		return 0
	}
	for _, alert := range fresh {
		n.known[alert.ID] = len(n.notified)
		n.notified = append(n.notified, notifiedID{id: alert.ID, at: float64(now)})
	}
	sent := 0
	for _, alert := range fresh {
		if n.send(alert, now) {
			sent++
		}
	}
	n.save(ctx, now)
	return sent
}

// send shows one notification: false when this subject showed within the rate window or
// nothing on this platform can show one.
func (n *Notifier) send(alert shared.Alert, now int64) bool {
	subject := string(alert.Kind) + ":" + alert.SessionID
	if alert.Kind == shared.AlertBudget {
		who := "undefined"
		if parts := strings.Split(alert.ID, ":"); len(parts) > 1 {
			who = parts[1]
		}
		subject = string(alert.Kind) + ":" + who
	}
	if last, seen := n.lastBySubject[subject]; seen && now-last < rateMs {
		return false
	}
	text := alert.Detail
	if alert.Project != "" {
		text = alert.Project + ": " + alert.Detail
	}
	body := clip(text, 220)
	line := NotifyCommand(n.platform, n.env, titles[alert.Kind], body)
	n.lastBySubject[subject] = now
	if line == nil {
		return false
	}
	n.runner(line.Command, line.Args)
	return true
}

// save stores the memory, forgetting entries past a week; a failed write means at most one
// repeat after a restart.
func (n *Notifier) save(ctx context.Context, now int64) {
	cutoff := float64(now - rememberMs)
	kept := make([]notifiedID, 0, len(n.notified))
	known := make(map[string]int, len(n.notified))
	for _, entry := range n.notified {
		if entry.at < cutoff {
			continue
		}
		known[entry.id] = len(kept)
		kept = append(kept, entry)
	}
	n.notified = kept
	n.known = known
	_ = settings.WriteJSONAtomic(NotifiedPath(ctx, n.env), notifiedFile{Version: 1, IDs: kept})
}

// notifiedFile is notified.json's shape.
type notifiedFile struct {
	Version int         `json:"version"`
	IDs     notifiedIDs `json:"ids"`
}

// notifiedIDs is the ids object: pairs in arrival order, so the file keeps the order the
// notifications happened in.
type notifiedIDs []notifiedID

// MarshalJSON writes the ids as one JSON object, its numbers as JavaScript prints them.
func (n notifiedIDs) MarshalJSON() ([]byte, error) {
	if len(n) == 0 {
		return []byte("{}"), nil
	}
	var out bytes.Buffer
	out.WriteByte('{')
	for i, entry := range n {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(jsfmt.String(entry.id).EncodeJSON())
		out.WriteByte(':')
		out.WriteString(jsfmt.JSNumber(entry.at))
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// readNotified loads the notification memory: ids in file order, non-number values skipped. A
// missing, broken or non-object file reads as empty.
func readNotified(path string) []notifiedID {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := jsfmt.DecodeValue(data)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return nil
	}
	var ids *jsfmt.Value
	keys, fields := value.Keys(), value.Fields()
	for i, key := range keys {
		if key == "ids" {
			ids = fields[i]
		}
	}
	if ids == nil || ids.Kind() != jsfmt.KindObject {
		return nil
	}
	out := []notifiedID{}
	idKeys, idFields := ids.Keys(), ids.Fields()
	for i, key := range idKeys {
		if idFields[i].Kind() != jsfmt.KindNumber {
			continue
		}
		at, err := strconv.ParseFloat(idFields[i].EncodeJSON(), 64)
		if err != nil {
			continue
		}
		out = append(out, notifiedID{id: key, at: at})
	}
	return out
}

// clip caps text at max UTF-16 units, the measure the desktop shows, marking the cut.
func clip(text string, max int) string {
	if jsfmt.UTF16Len(text) <= max {
		return text
	}
	return jsfmt.UTF16Slice(text, 0, max-1) + "…"
}
