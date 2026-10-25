// Package outside holds what huddle reaches outside its own walls for: the desktop notifier,
// the radar plugin's loopback API, the plugin registry that decides whether the server is
// still wanted, and the system service a hub machine runs the server through.
package outside

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The rate window, the switch's read budget and the spawn budget. HUDDLE_NOTIFY_WINDOW_MS
// narrows the window for a process (tests).
const (
	defaultWindow   = 10 * time.Minute
	spawnBudget     = 5 * time.Second
	enabledCacheTTL = 2 * time.Second
	seenTableLimit  = 2000
)

var (
	controlRe     = regexp.MustCompile(`[\x00-\x1f\x7f]+`)
	credentialRe  = regexp.MustCompile(`\b(?:sk|pk|rk|ghp|gho|ghs|ghu|github_pat|glpat|xox[abpr]|AKIA|ASIA)[-_A-Za-z0-9]{8,}`)
	blobRe        = regexp.MustCompile(`\b[A-Za-z0-9+/_=-]{40,}\b`)
	assignmentRe  = regexp.MustCompile(`(?i)((?:token|secret|password|passwd|api[-_]?key|key|credential|auth)` + jsfmt.SpaceClass + `*[=:]` + jsfmt.SpaceClass + `*)\S+`)
	spaceRe       = regexp.MustCompile(jsfmt.SpaceClass + `+`)
	notifySetting = `"notify"`
)

// Clean masks anything that looks like a credential, strips control characters and caps the
// length in UTF-16 units, the measure the desktop shows. Its \s and trim are JavaScript's:
// Unicode whitespace folds and masks too.
func Clean(s string, max int) string {
	t := controlRe.ReplaceAllString(s, " ")
	t = credentialRe.ReplaceAllString(t, "…")
	t = blobRe.ReplaceAllString(t, "…")
	t = assignmentRe.ReplaceAllString(t, "$1…")
	t = jsfmt.Trim(spaceRe.ReplaceAllString(t, " "))
	if jsfmt.UTF16Len(t) > max {
		t = jsfmt.UTF16Slice(t, 0, max-1) + "…"
	}
	return t
}

// onPath is the first directory on PATH holding a file of this name.
func onPath(name string) string {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

// NotifierProgram names the program that shows notifications here: osascript on macOS,
// notify-send on Linux, nothing anywhere else.
func NotifierProgram() string {
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat("/usr/bin/osascript"); err == nil || onPath("osascript") != "" {
			return "osascript"
		}
	case "linux":
		if onPath("notify-send") != "" {
			return "notify-send"
		}
	}
	return ""
}

// State is the dashboard's Settings answer: the switch, the program that shows notifications
// here, and whether the environment forced them off.
type State struct {
	Notify    bool    `json:"notify"`
	Notifier  *string `json:"notifier"`
	ForcedOff bool    `json:"forced_off"`
}

// enabledCache is the switch as last read from the file, with the moment it was read: the file
// is looked at at most every 2 s.
type enabledCache struct {
	at time.Time
	on bool
}

// Notifier shows desktop notifications for what needs the owner. The text is built from names
// and ids only, then cleaned; the same kind and subject notify at most once per window.
type Notifier struct {
	// Now is the clock; Spawn starts the OS notifier program (tests replace it).
	Now   func() time.Time
	Spawn func(program string, args []string) error

	dir    string
	getenv func(string) string
	mu     sync.Mutex
	last   map[string]time.Time
	cache  *enabledCache
}

// NewNotifier reads and writes its switch in dir, the server's data dir.
func NewNotifier(dir string) *Notifier {
	return &Notifier{
		Now:    time.Now,
		Spawn:  spawnDetached,
		dir:    dir,
		getenv: os.Getenv,
		last:   map[string]time.Time{},
	}
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *Notifier) env(name string) string {
	if n.getenv != nil {
		return n.getenv(name)
	}
	return os.Getenv(name)
}

func (n *Notifier) file() string { return filepath.Join(n.dir, "settings.json") }

// Enabled reports whether notifications are on: HUDDLE_NOTIFY=0 forces them off for this
// process, and the file is read at most every 2 s.
func (n *Notifier) Enabled() bool {
	if n.env("HUDDLE_NOTIFY") == "0" {
		return false
	}
	now := n.now()
	n.mu.Lock()
	cached := n.cache
	n.mu.Unlock()
	if cached != nil && now.Sub(cached.at) < enabledCacheTTL {
		return cached.on
	}
	on := true
	for _, setting := range readSettings(n.file()) {
		if setting.key == notifySetting && setting.raw == "false" {
			on = false
		}
	}
	n.mu.Lock()
	n.cache = &enabledCache{at: now, on: on}
	n.mu.Unlock()
	return on
}

// Set writes the switch, keeping every other member of the file in place, and returns the state.
func (n *Notifier) Set(on bool) State {
	value := "false"
	if on {
		value = "true"
	}
	writeSettings(n.dir, n.file(), putMember(readSettings(n.file()), notifySetting, value))
	n.mu.Lock()
	n.cache = &enabledCache{at: n.now(), on: on}
	n.mu.Unlock()
	return n.State()
}

// State is the Settings answer: on or off, what shows notifications here, and a forced off.
func (n *Notifier) State() State {
	var program *string
	if n.env("HUDDLE_NOTIFY_LOG") != "" {
		name := "log"
		program = &name
	} else if how := NotifierProgram(); how != "" {
		program = &how
	}
	return State{Notify: n.Enabled(), Notifier: program, ForcedOff: n.env("HUDDLE_NOTIFY") == "0"}
}

// note is one line of the notification log, its keys in this order.
type note struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

// Send shows one notification: false when they are off, when this kind and subject showed
// within the window, or when nothing here can show one. A failure anywhere is quiet.
func (n *Notifier) Send(kind, subject, title, body string) bool {
	if !n.Enabled() {
		return false
	}
	now := n.now()
	window := n.window()
	n.mu.Lock()
	key := kind + "\x00" + subject
	if prev, seen := n.last[key]; seen && now.Sub(prev) < window {
		n.mu.Unlock()
		return false
	}
	n.last[key] = now
	if len(n.last) > seenTableLimit {
		for each, at := range n.last {
			if now.Sub(at) >= window {
				delete(n.last, each)
			}
		}
	}
	n.mu.Unlock()
	cleanTitle, cleanBody := Clean(title, 80), Clean(body, 160)
	if logTo := n.env("HUDDLE_NOTIFY_LOG"); logTo != "" {
		return appendNote(logTo, note{
			At:      jsTimestamp(now),
			Kind:    kind,
			Subject: subject,
			Title:   cleanTitle,
			Body:    cleanBody,
		}) == nil
	}
	how := NotifierProgram()
	if how == "" {
		return false
	}
	// the text goes in as arguments, never into the script: nothing in it can run
	program, args := how, []string{"-a", "Huddle", cleanTitle, cleanBody}
	if how == "osascript" {
		program = "osascript"
		args = []string{
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run",
			cleanTitle, cleanBody,
		}
	}
	spawn := n.Spawn
	if spawn == nil {
		spawn = spawnDetached
	}
	_ = spawn(program, args) // a start failure is the program's business, not the caller's
	return true
}

// window is how long one kind and subject stays quiet after a notification.
func (n *Notifier) window() time.Duration {
	ms := float64(defaultWindow / time.Millisecond)
	if v := n.env("HUDDLE_NOTIFY_WINDOW_MS"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil && parsed != 0 {
			ms = parsed
		}
	}
	return time.Duration(ms * float64(time.Millisecond))
}

// appendNote appends one notification as a JSON line, the form tests and headless machines read.
func appendNote(path string, n note) error {
	line, err := domain.MarshalCompactJSON(n)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

// spawnDetached starts the notifier program in its own session and forgets it: nothing waits
// for a notification, and one that never comes back is killed after the spawn budget.
func spawnDetached(program string, args []string) error {
	cmd := exec.Command(program, args...)
	cmd.SysProcAttr = launchAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	kill := time.AfterFunc(spawnBudget, func() { _ = cmd.Process.Kill() })
	go func() {
		_ = cmd.Wait()
		kill.Stop()
	}()
	return nil
}

// jsTimestamp is new Date().toISOString(): UTC, milliseconds, a literal Z.
func jsTimestamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}

// settingMember is one member of the settings file: the key as it stands in the file (quotes
// included) and the value's JSON text.
type settingMember struct {
	key string
	raw string
}

// readSettings is the settings file as an ordered member list; a file that is not JSON, or not
// an object, has no members.
func readSettings(path string) []settingMember {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return scanMembers(string(data))
}

// putMember sets one member's value where it stands, or adds it last.
func putMember(members []settingMember, key, raw string) []settingMember {
	for i := range members {
		if members[i].key == key {
			members[i].raw = raw
			return members
		}
	}
	return append(members, settingMember{key: key, raw: raw})
}

// writeSettings replaces the settings file with members through a temp file in dir. The write
// is best effort, like everything here: a read-only data dir stops nothing.
func writeSettings(dir, path string, members []settingMember) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(renderSettings(members)), 0o600); err == nil {
		_ = os.Rename(tmp, path)
	}
}

// renderSettings is the whole file: every member on its own line, two spaces deep, notify last
// added or replaced where it stood, one trailing newline.
func renderSettings(members []settingMember) string {
	parts := make([]string, len(members))
	for i, setting := range members {
		parts[i] = "  " + setting.key + ": " + prettyJSON(setting.raw, 1)
	}
	return "{\n" + strings.Join(parts, ",\n") + "\n}\n"
}

// prettyJSON lays one JSON value out two spaces deep, every member and element on its own line,
// empty {} and [] compact. String tokens pass through untouched, so nothing is re-escaped.
func prettyJSON(raw string, depth int) string {
	pad := strings.Repeat("  ", depth)
	i := skipSpace(raw, 0)
	if i >= len(raw) {
		return ""
	}
	switch raw[i] {
	case '{':
		members := scanMembers(raw)
		if len(members) == 0 {
			return "{}"
		}
		parts := make([]string, len(members))
		for k, setting := range members {
			parts[k] = pad + "  " + setting.key + ": " + prettyJSON(setting.raw, depth+1)
		}
		return "{\n" + strings.Join(parts, ",\n") + "\n" + pad + "}"
	case '[':
		var parts []string
		j := skipSpace(raw, i+1)
		for j < len(raw) && raw[j] != ']' {
			end, ok := scanValue(raw, j)
			if !ok {
				return strings.TrimSpace(raw)
			}
			parts = append(parts, pad+"  "+prettyJSON(raw[j:end], depth+1))
			j = skipSpace(raw, end)
			if j < len(raw) && raw[j] == ',' {
				j = skipSpace(raw, j+1)
			}
		}
		if parts == nil {
			return "[]"
		}
		return "[\n" + strings.Join(parts, ",\n") + "\n" + pad + "]"
	}
	return strings.TrimSpace(raw)
}

// scanMembers lists a JSON object's members in file order.
func scanMembers(s string) []settingMember {
	i := skipSpace(s, 0)
	if i >= len(s) || s[i] != '{' {
		return nil
	}
	i = skipSpace(s, i+1)
	var members []settingMember
	for i < len(s) && s[i] != '}' {
		keyStart := i
		keyEnd, ok := scanString(s, i)
		if !ok {
			return nil
		}
		i = skipSpace(s, keyEnd)
		if i >= len(s) || s[i] != ':' {
			return nil
		}
		i = skipSpace(s, i+1)
		valueEnd, ok := scanValue(s, i)
		if !ok {
			return nil
		}
		members = append(members, settingMember{key: s[keyStart:keyEnd], raw: s[i:valueEnd]})
		i = skipSpace(s, valueEnd)
		if i < len(s) && s[i] == ',' {
			i = skipSpace(s, i+1)
		}
	}
	return members
}

func skipSpace(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// scanString returns the index just past the string at i; ok is false when there is none.
func scanString(s string, i int) (int, bool) {
	if i >= len(s) || s[i] != '"' {
		return i, false
	}
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return i, false
}

// scanValue returns the index just past the value at i.
func scanValue(s string, i int) (int, bool) {
	if i >= len(s) {
		return i, false
	}
	switch s[i] {
	case '"':
		return scanString(s, i)
	case '{', '[':
		return scanBracketed(s, i)
	}
	end := i
	for end < len(s) && !strings.ContainsRune(",}] \t\n\r", rune(s[end])) {
		end++
	}
	return end, end > i
}

// scanBracketed returns the index just past the bracketed value at i, strings respected.
func scanBracketed(s string, i int) (int, bool) {
	depth := 0
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '"':
			next, ok := scanString(s, j)
			if !ok {
				return i, false
			}
			j = next - 1
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return i, false
}
