// Who this session is, which channel it is in, and where this user's Huddle files live, for
// the CLI, the bridge and the hooks. One home per user holds the server's files (the home
// package): config.toml (with the port), channels/, sessions/, run/. A project keeps only its
// settings file, .agents/huddle/huddle.json (kept out of git):
//
//	huddle.json   {"channel": "shop", "as": "api", "role": "builds the API", "autostart": true}
//
// Environment first (HUDDLE_URL, HUDDLE_CHANNEL, HUDDLE_AS, …; an empty variable counts as
// unset), then the nearest .agents/huddle/huddle.json walking up from the project dir
// (CLAUDE_PROJECT_DIR) or the cwd (a .agents/.huddle.json is read too; huddle.json wins).
// Worktrees share the main checkout's file: one nested under the repo finds it walking up;
// one elsewhere finds it through git (the main checkout of its common .git dir). A worktree's
// own file, if any, wins. Last, what this session joined with
// `huddle join <host:port> --token …` (creds.go) fills in what is still unset.
//
// The port: 18860 by default, walked to 18869 then to the OS's pick when another program holds
// it (the server package's ChoosePort), saved in the home's config.toml ("port";
// "port_auto" = true when huddle picked it itself, so it may move when another program takes
// it); a server that runs from the home without a saved port has its port read from the
// process once and saved. No saved port and nothing running: no url yet, and the first
// `huddle up` picks one (serve.go).
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Identity is what a session is in a Huddle: where its server answers, in which channel, as
// whom, and what the hooks bring in. Every setting has an environment variable (which wins) and
// a key in huddle.json (used when the variable is unset).
type Identity struct {
	URL       string   `json:"url"`
	Source    string   `json:"source"` // env, config, port, credential, saved, running or none: no url yet
	Channel   string   `json:"channel"`
	As        string   `json:"as"`
	Role      string   `json:"role,omitempty"`
	Push      []string `json:"push"`   // topic globs pushed to an idle session
	Listen    []string `json:"listen"` // more channels whose messages the hooks bring in
	Wait      int      `json:"wait"`   // seconds a hook's long-poll waits
	Context   string   `json:"context"`
	Autostart bool     `json:"autostart"` // the SessionStart hook runs `huddle up` when nothing answers
	Detail    string   `json:"detail"`    // digest: messages in full, other events counted; all: everything
	File      string   `json:"file,omitempty"`
}

// FileRel is the project's settings file, under its .agents/huddle.
const FileRel = ".agents/huddle/huddle.json"

// LegacyRel is the settings file's earlier spelling, one folder up.
const LegacyRel = ".agents/.huddle.json"

// gitdirLine reads the "gitdir: <path>" line of a worktree's .git file.
var gitdirLine = regexp.MustCompile(`(?m)^gitdir:\s*(.+)$`)

// Config is one found settings file: its path, its JSON (every key, in file order) and the
// project root it belongs to.
type Config struct {
	File string
	Cfg  *jsfmt.Value
	Root string
}

// readConfig loads one settings file; ok is false when it is missing or not readable.
func readConfig(path string) (*jsfmt.Value, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		return nil, false
	}
	return value, true
}

// FindConfig walks up from from (the project dir, else the world's working directory) looking
// for the project's settings: huddle.json first, the legacy .huddle.json beside it, in each
// directory on the way up; nothing there means the main checkout of the repo around from. A
// file that exists but cannot be read stops the walk with no config: a torn file is never
// stepped over for an older one.
func FindConfig(ctx context.Context, w *World, from string) *Config {
	if from == "" {
		from = firstNonEmpty(w.getenv("CLAUDE_PROJECT_DIR"), w.dir())
	}
	at := func(dir string) (*Config, bool) {
		for _, rel := range []string{FileRel, LegacyRel} {
			path := filepath.Join(dir, rel)
			if _, err := os.Stat(path); err != nil {
				continue
			}
			cfg, ok := readConfig(path)
			if !ok {
				return nil, true // unreadable: this directory's answer is "none"
			}
			return &Config{File: path, Cfg: cfg, Root: dir}, true
		}
		return nil, false
	}
	for dir := from; ; dir = filepath.Dir(dir) {
		if found, stop := at(dir); stop {
			return found
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if main := MainCheckout(ctx, w, from); main != "" {
		if found, stop := at(main); stop {
			return found
		}
	}
	return nil
}

// MainCheckout answers the main checkout of the git repo around dir, read from the files
// (spawning git costs seconds on a machine whose antivirus scans every process): a .git
// directory marks the main checkout; a .git file ("gitdir: <main>/.git/worktrees/<name>") marks
// a worktree and names its main checkout. Empty means no repo.
func MainCheckout(ctx context.Context, w *World, dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		git := filepath.Join(d, ".git")
		info, err := os.Stat(git)
		if err == nil {
			if info.IsDir() {
				return d
			}
			if raw, err := os.ReadFile(git); err == nil {
				return mainCheckoutOf(d, string(raw))
			}
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// mainCheckoutOf is the main checkout one .git file's content names: a worktree's gitdir points
// into the main checkout's .git, and everything before that marker is the checkout itself; a
// submodule or another layout leaves the checkout the file sits in.
func mainCheckoutOf(dir, content string) string {
	match := gitdirLine.FindStringSubmatch(content)
	if match == nil {
		return dir
	}
	gd := strings.TrimSpace(match[1])
	at := strings.LastIndex(gd, "/.git/worktrees/")
	if at <= 0 {
		return dir
	}
	return gd[:at]
}

// HomeOf answers where this user's Huddle files live: the home package's one resolution
// (HUDDLE_HOME, else $XDG_CONFIG_HOME/huddle, else ~/.config/huddle). Empty when the environment
// names none; the entries that need a home Ensure it themselves.
func HomeOf(ctx context.Context, w *World) string {
	h, err := home.Resolve(w.getenv)
	if err != nil {
		return ""
	}
	return h.Root
}

// TheHome is the home as the home package sees it, for the entries that lay it out.
func TheHome(ctx context.Context, w *World) (home.Home, bool) {
	h, err := home.Resolve(w.getenv)
	if err != nil {
		return home.Home{}, false
	}
	return h, true
}

// PortChoice is a saved port: the number and whether huddle picked it (auto) or someone chose it.
type PortChoice struct {
	Port int
	Auto bool
}

// SavedPort reads the port saved in the home's config.toml: a number in range, or nothing.
func SavedPort(root string) (*PortChoice, bool) {
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Port <= 0 || cfg.Port >= 65536 {
		return nil, false
	}
	return &PortChoice{Port: cfg.Port, Auto: cfg.PortAuto}, true
}

// SavePort writes the port into the home's config.toml, keeping every other key: port_auto
// records that huddle picked it, and leaves with a chosen port. It answers the file.
func SavePort(ctx context.Context, w *World, port int, auto bool, root string) string {
	file, err := home.SavePort(home.Home{Root: root}, port, auto)
	if err != nil {
		return ""
	}
	return file
}

// Local is the loopback url of a port.
func Local(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

var (
	slugNoise   = regexp.MustCompile(`[^a-z0-9-]+`)
	slugLeading = regexp.MustCompile(`^[^a-z]+`)
	slugTrails  = regexp.MustCompile(`-+$`)
)

// Slug answers a name made from a folder's (a project's default channel and session name):
// lowercase, a letter first, at most 32 characters of letters, digits and - (valid for a channel
// and a session); or when nothing is left.
func Slug(dir, or string) string {
	parts := strings.Split(dir, "/")
	last := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			last = parts[i]
			break
		}
	}
	name := slugTrails.ReplaceAllString(
		jsfmt.UTF16Slice(slugLeading.ReplaceAllString(slugNoise.ReplaceAllString(strings.ToLower(last), "-"), ""), 0, 32),
		"")
	if name == "" {
		return or
	}
	return name
}

// RunningPid answers the pid in <home>/run/huddle.pid when that process is still a Huddle server
// (a stale pid may belong to anything): 0 when it is not.
func RunningPid(ctx context.Context, w *World, root string) int {
	raw, err := os.ReadFile(home.Home{Root: root}.PidFile())
	if err != nil {
		return 0
	}
	pid := jsNumber(strings.TrimSpace(string(raw)))
	if pid != float64(int(pid)) || pid <= 0 {
		return 0
	}
	command, ok := w.proc().command(int(pid))
	if !ok || !huddleServe.MatchString(command) {
		return 0
	}
	return int(pid)
}

// ListeningPort answers the loopback port a running process listens on, read from the process
// (as each OS lists its sockets); 0 when it cannot be read.
func ListeningPort(ctx context.Context, w *World, pid int) int {
	port, ok := w.proc().listenOf(pid)
	if !ok {
		return 0
	}
	return port
}

// homeURL is this home's server: its saved port, else (once, then saved) the port its running
// server listens on. ok is false when there is no url yet.
func homeURL(ctx context.Context, w *World, home string) (url, source string, ok bool) {
	if choice, has := SavedPort(home); has {
		return Local(choice.Port), "saved", true
	}
	pid := RunningPid(ctx, w, home)
	if pid <= 0 {
		return "", "", false
	}
	port := ListeningPort(ctx, w, pid)
	if port <= 0 {
		return "", "", false
	}
	_ = SavePort(ctx, w, port, false, home)
	return Local(port), "running", true
}

// Identify answers this session's identity. sid is the session id a hook knows its caller by;
// what this session joined (creds.go) fills in what the environment and the settings leave
// unset.
func Identify(ctx context.Context, w *World, sid string) Identity {
	found := FindConfig(ctx, w, "")
	var cfg *jsfmt.Value
	if found != nil {
		cfg = found.Cfg
	}
	fileURL, _ := strField(cfg, "url")
	given := trimSlash(firstNonEmpty(w.getenv("HUDDLE_URL"), fileURL))
	url := given
	if url == "" {
		if port := jsNumber(w.getenv("HUDDLE_PORT")); port == float64(int(port)) && port > 0 && port < 65536 {
			url = Local(int(port))
		}
	}
	joined := LoadCred(ctx, w, url, sid)
	homeFallback := ""
	homeSource := ""
	if url == "" && (joined == nil || joined.URL == "") {
		homeFallback, homeSource, _ = homeURL(ctx, w, HomeOf(ctx, w))
	}
	finalURL := firstNonEmpty(url, credURL(joined), homeFallback)
	source := "none"
	switch {
	case given != "":
		source = "config"
		if w.getenv("HUDDLE_URL") != "" {
			source = "env"
		}
	case url != "":
		source = "port"
	case joined != nil && joined.URL != "":
		source = "credential"
	case homeSource != "":
		source = homeSource
	}
	channel := firstNonEmpty(w.getenv("HUDDLE_CHANNEL"),
		memberField(joined, "channel"), cfgString(cfg, "channel"), joinedField(joined, "channel"))
	as := firstNonEmpty(w.getenv("HUDDLE_AS"),
		memberField(joined, "as"), cfgString(cfg, "as"), joinedField(joined, "as"))
	role, _ := strField(cfg, "role")
	if envRole := w.getenv("HUDDLE_ROLE"); envRole != "" {
		role = envRole
	}
	file := ""
	if found != nil {
		file = found.File
	}
	return Identity{
		URL:     finalURL,
		Source:  source,
		Channel: channel,
		As:      as,
		Role:    role,
		Push:    splitList(firstNonEmpty(w.getenv("HUDDLE_PUSH"), listField(cfg, "push"), "task.ready,turn.pass,ask,msg,control.*")),
		Listen:  splitList(firstNonEmpty(w.getenv("HUDDLE_LISTEN"), listField(cfg, "listen"), "")),
		Wait:    int(jsNumber(firstNonEmpty(w.getenv("HUDDLE_WAIT"), scalarField(cfg, "wait"), "1500"))),
		Context: firstNonEmpty(w.getenv("HUDDLE_CONTEXT"), scalarField(cfg, "context"), "auto"),
		Detail:  detailOf(firstNonEmpty(w.getenv("HUDDLE_LISTEN_DETAIL"), scalarField(cfg, "listen_detail"), "")),
		File:    file,
		Autostart: autostartOf(firstNonEmpty(
			w.getenv("HUDDLE_AUTOSTART"),
			boolOrScalarField(cfg, "autostart"),
		)),
	}
}

// ContextFor answers the join context for a SessionStart hook: a setting of sync or fresh wins;
// with auto, a resumed session syncs (it continues where it was), a cleared or compacted one
// starts fresh (its context window was just emptied or summarized), and a new one (startup)
// leaves it to the server (empty).
func ContextFor(source, setting string) string {
	if setting == "sync" || setting == "fresh" {
		return setting
	}
	switch source {
	case "resume":
		return "sync"
	case "clear", "compact":
		return "fresh"
	}
	return ""
}

// memberField answers what a joined member's credential carries for one key: a member's
// credential is bound to the channel its invite named and the name it joined as (huddle join
// may have made the project's name unique for it), so that one wins over the project file's; a
// root credential acts as anyone.
func memberField(joined *Cred, key string) string {
	if joined == nil || joined.Root {
		return ""
	}
	if key == "channel" {
		return joined.Channel
	}
	return joined.As
}

// joinedField answers the credential's value for one key, whatever kind it holds.
func joinedField(joined *Cred, key string) string {
	if joined == nil {
		return ""
	}
	if key == "channel" {
		return joined.Channel
	}
	return joined.As
}

// cfgString answers the settings file's value for one key.
func cfgString(cfg *jsfmt.Value, key string) string {
	value, _ := strField(cfg, key)
	return value
}

// credURL is the credential's server address.
func credURL(joined *Cred) string {
	if joined != nil {
		return joined.URL
	}
	return ""
}

// detailOf maps the listen-detail setting to its two values.
func detailOf(value string) string {
	if value == "all" {
		return "all"
	}
	return "digest"
}

// autostartOf answers whether the autostart setting is on: it is on unless turned off, because
// a fresh install must start its server from the first session with no setup; "0", "false",
// "off" or "no", whatever the case, turns it off.
func autostartOf(value string) bool {
	switch strings.ToLower(value) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// splitList parses a comma list: trimmed, empty entries dropped.
func splitList(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// fieldOf answers one object field's value and whether it is present.
func fieldOf(value *jsfmt.Value, key string) (*jsfmt.Value, bool) {
	if value == nil || value.Kind() != jsfmt.KindObject {
		return nil, false
	}
	for i, name := range value.Keys() {
		if name == key {
			return value.Fields()[i], true
		}
	}
	return nil, false
}

// strField answers one string field of an object.
func strField(value *jsfmt.Value, key string) (string, bool) {
	field, ok := fieldOf(value, key)
	if !ok || field.Kind() != jsfmt.KindString {
		return "", false
	}
	return field.Text(), true
}

// listField answers one field as a comma list: an array joined, a scalar as it is.
func listField(value *jsfmt.Value, key string) string {
	field, ok := fieldOf(value, key)
	if !ok {
		return ""
	}
	switch field.Kind() {
	case jsfmt.KindArray:
		parts := make([]string, 0, len(field.Items()))
		for _, item := range field.Items() {
			if item.Kind() == jsfmt.KindString {
				parts = append(parts, item.Text())
			} else {
				parts = append(parts, item.EncodeJSON())
			}
		}
		return strings.Join(parts, ",")
	case jsfmt.KindString:
		return field.Text()
	default:
		return field.EncodeJSON()
	}
}

// scalarField answers one field as the text JavaScript's String() would print: a quoted string
// unquoted, anything else its JSON.
func scalarField(value *jsfmt.Value, key string) string {
	field, ok := fieldOf(value, key)
	if !ok {
		return ""
	}
	if field.Kind() == jsfmt.KindString {
		return field.Text()
	}
	return field.EncodeJSON()
}

// boolOrScalarField answers a boolean field as its word, for settings that may be either.
func boolOrScalarField(value *jsfmt.Value, key string) string {
	field, ok := fieldOf(value, key)
	if !ok {
		return ""
	}
	if field.Kind() == jsfmt.KindBool {
		return field.EncodeJSON()
	}
	return scalarField(value, key)
}

// numberValue reads a numeric value's double.
func numberValue(value *jsfmt.Value) float64 {
	port, err := strconv.ParseFloat(value.EncodeJSON(), 64)
	if err != nil {
		return 0
	}
	return port
}

// jsNumber parses a number the way JavaScript's Number() does: leading and trailing space is
// fine, anything unparsable is NaN (here 0, its JSON spelling).
func jsNumber(text string) float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0
	}
	return value
}

// indexOf answers the position of key in keys, or -1.
func indexOf(keys []string, key string) int {
	for i, name := range keys {
		if name == key {
			return i
		}
	}
	return -1
}

// firstNonEmpty answers the first argument that is not empty.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// indentJSON renders a value the way JSON.stringify(v, null, 2) does: two spaces per level, a
// newline after every opening and before every closing brace, empty containers inline, no
// trailing newline (the callers that carry one add it).
func indentJSON(value *jsfmt.Value) string { return indentJSONWidth(value, 2) }

// indentJSONWidth renders a value indented width spaces per level, the way JSON.stringify's
// third argument does.
func indentJSONWidth(value *jsfmt.Value, width int) string {
	var out strings.Builder
	indentValue(&out, value, 0, width)
	return out.String()
}

// indentValue writes one value at depth, with a newline already open before it.
func indentValue(out *strings.Builder, value *jsfmt.Value, depth, width int) {
	pad := strings.Repeat(" ", depth*width)
	inner := strings.Repeat(" ", (depth+1)*width)
	switch value.Kind() {
	case jsfmt.KindObject:
		if len(value.Keys()) == 0 {
			out.WriteString("{}")
			return
		}
		out.WriteString("{\n")
		for i, key := range value.Keys() {
			out.WriteString(inner)
			out.WriteString(jsfmt.String(key).EncodeJSON())
			out.WriteString(": ")
			indentValue(out, value.Fields()[i], depth+1, width)
			if i < len(value.Keys())-1 {
				out.WriteString(",")
			}
			out.WriteString("\n")
		}
		out.WriteString(pad + "}")
	case jsfmt.KindArray:
		if len(value.Items()) == 0 {
			out.WriteString("[]")
			return
		}
		out.WriteString("[\n")
		for i, item := range value.Items() {
			out.WriteString(inner)
			indentValue(out, item, depth+1, width)
			if i < len(value.Items())-1 {
				out.WriteString(",")
			}
			out.WriteString("\n")
		}
		out.WriteString(pad + "]")
	default:
		out.WriteString(value.EncodeJSON())
	}
}
