// The hook entries: every harness with a lifecycle mechanism of its own gets huddle's hook
// lines installed beside its MCP server, so a session start, a prompt, a tool call and a
// session end reach the hub the way Claude Code's do. Each edit is a pure edit over the
// harness's config bytes — the same discipline as the MCP entries — and every hook runs the
// one hidden word, `huddle hook external --harness <name> --event <event>`, with the agent's
// own payload on stdin. The facts per agent (config keys, payload fields, citations) live in
// .agents/scratch/harness-hooks.md.
package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// HookEvent is one lifecycle event a harness reports: its own event name, and the report
// event name the hub's states read (BeforeAgent fires when the user submits a prompt, so it
// reports as UserPromptSubmit).
type HookEvent struct {
	Event  string
	Report string
}

// The events per harness, in install order. Only events whose payload carries a session id
// are installed; an agent whose other events name none (opencode's tool events) gets none.
var (
	codexHookEvents = []HookEvent{
		{"SessionStart", "SessionStart"}, {"UserPromptSubmit", "UserPromptSubmit"},
		{"PreToolUse", "PreToolUse"}, {"PostToolUse", "PostToolUse"},
		{"Stop", "Stop"}, {"SessionEnd", "SessionEnd"},
	}
	geminiHookEvents = []HookEvent{
		{"SessionStart", "SessionStart"}, {"BeforeAgent", "UserPromptSubmit"},
		{"BeforeTool", "PreToolUse"}, {"AfterTool", "PostToolUse"},
		{"AfterAgent", "Stop"}, {"SessionEnd", "SessionEnd"},
	}
	qwenHookEvents = []HookEvent{
		{"SessionStart", "SessionStart"}, {"UserPromptSubmit", "UserPromptSubmit"},
		{"PreToolUse", "PreToolUse"}, {"PostToolUse", "PostToolUse"},
		{"Stop", "Stop"}, {"SessionEnd", "SessionEnd"},
	}
	crushHookEvents    = []HookEvent{{"PreToolUse", "PreToolUse"}}
	opencodeHookEvents = []HookEvent{{"session.created", "SessionStart"}, {"session.idle", "Stop"}}
)

// HookEvents answers the events the named harness reports to the hub, mapped to the report
// names; an unknown harness is the UnknownHarnessError ByName answers with.
func HookEvents(name string) ([]HookEvent, error) {
	h := hookFor(name)
	if h == nil {
		return nil, UnknownHarnessError{Name: name}
	}
	return h.events(), nil
}

// hookEdit is one harness's hook config: bytes in, bytes out, only huddle's own hook entries
// changed. shim is the plugin file path the config should name when the harness loads hooks
// from a file (opencode); the other edits ignore it.
type hookEdit interface {
	events() []HookEvent
	install(cfg []byte, bin, name, shim string) ([]byte, error)
	remove(cfg []byte, bin, name, shim string) ([]byte, error)
	contains(cfg []byte, bin, name, shim string) bool
}

// hookEdits is the hook edit per harness. Every harness this package wires has one, so a
// name ByName answers always finds its edit here; omp's and pi's are noHooks.
var hookEdits = map[string]hookEdit{
	"codex":    tomlHooks{},
	"gemini":   jsonHooks{name: "gemini", parent: "hooks", timeout: 3000, hookEvents: geminiHookEvents, grouped: true},
	"qwen":     jsonHooks{name: "qwen", parent: "hooks", timeout: 3, hookEvents: qwenHookEvents, grouped: true},
	"crush":    jsonHooks{name: "crush", parent: "hooks", timeout: 3, hookEvents: crushHookEvents},
	"opencode": pluginHooks{},
	"omp":      noHooks{},
	"pi":       noHooks{},
	"zcode":    noHooks{},
}

// hookFor answers the named harness's hook edit.
func hookFor(name string) hookEdit { return hookEdits[name] }

// shimName is the file name of the opencode plugin shim, under the huddle home's harness
// directory. A config entry naming this file is huddle's, wherever the home sits.
const shimName = "opencode-hook.js"

// shimPath is where the opencode plugin shim lives: inside the huddle home, which is
// huddle's own data, never the user's.
func shimPath(hudHome string) string { return filepath.Join(hudHome, "harness", shimName) }

// hookCommand answers the command line one harness's hook runs: the binary quoted for a
// shell, the hidden word, and the agent's own event name.
func hookCommand(bin, name, event string) string {
	return shellQuote(bin) + " hook external --harness " + name + " --event " + event
}

// shellQuote wraps s in the double quotes a shell reads back as s: the characters a double
// quoted string would eat ride as escapes.
func shellQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\', '$', '`':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// — the JSON hook configs: Gemini's and Qwen's grouped "hooks" object, Crush's flat one,
// and opencode's plugin array.

// jsonHooks is a JSON harness's hooks object: one parent member keyed by event name, each
// event an array whose members are matcher groups around one handler (gemini, qwen) or the
// bare handlers themselves (crush). Huddle's entries are the ones whose handler is named
// huddle.
type jsonHooks struct {
	name       string
	parent     string
	timeout    int // in the agent's own unit: seconds for qwen and crush, milliseconds for gemini
	hookEvents []HookEvent
	grouped    bool
}

// hookHandler is the command hook record gemini, qwen and crush all read.
type hookHandler struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func (j jsonHooks) events() []HookEvent { return j.hookEvents }

// handler answers one event's handler as the bytes install writes. The record holds only
// strings and one int, so the marshal cannot fail.
func (j jsonHooks) handler(bin, event string) []byte {
	raw, _ := json.Marshal(hookHandler{
		Name: "huddle", Type: "command", Command: hookCommand(bin, j.name, event), Timeout: j.timeout,
	})
	return raw
}

// snippet answers one event's array member: the bare handler (crush) or the group around it
// (gemini, qwen).
func (j jsonHooks) snippet(bin string, e HookEvent) []byte {
	handler := j.handler(bin, e.Event)
	if !j.grouped {
		return handler
	}
	return fmt.Appendf(nil, "{%q:[%s]}", "hooks", handler)
}

// mine answers whether one array item is one of huddle's entries, by the handler's name.
func (j jsonHooks) mine(cfg []byte, item span) bool {
	value, err := jsfmt.DecodeValue(cfg[item.start:item.end])
	if err != nil {
		return false
	}
	handler := value
	if j.grouped {
		inner, ok := memberOf(value, "hooks")
		if !ok || inner.Kind() != jsfmt.KindArray || len(inner.Items()) == 0 {
			return false
		}
		handler = inner.Items()[0]
	}
	name, ok := memberOf(handler, "name")
	return ok && name.Kind() == jsfmt.KindString && name.Text() == "huddle"
}

// memberOf answers one field of a jsfmt object value.
func memberOf(v *jsfmt.Value, key string) (*jsfmt.Value, bool) {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return nil, false
	}
	for i, one := range v.Keys() {
		if one == key {
			return v.Fields()[i], true
		}
	}
	return nil, false
}

func (j jsonHooks) install(cfg []byte, bin, _, _ string) ([]byte, error) {
	if cfg == nil {
		return j.fresh(bin), nil
	}
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	for _, e := range j.hookEvents {
		next, err := j.installOne(cfg, e, string(j.snippet(bin, e)))
		if err != nil {
			return nil, err
		}
		cfg = next
	}
	return cfg, nil
}

// installOne answers the config with one event's entry in it: the parent object and the
// event's array made when either is missing, a changed entry of ours replaced, ours appended
// when neither is there.
func (j jsonHooks) installOne(cfg []byte, e HookEvent, snippet string) ([]byte, error) {
	parent, err := jsonFind(cfg, 0, j.parent)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		full := fmt.Sprintf("%q:{%q:[%s]}", j.parent, e.Event, snippet)
		return jsonInsertMember(cfg, 0, full)
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return nil, fmt.Errorf("the %s member is not a JSON object", j.parent)
	}
	event := jsonMemberNamed(members, e.Event)
	if event == nil {
		return jsonInsertMember(cfg, parent.value.start, fmt.Sprintf("%q:[%s]", e.Event, snippet))
	}
	items, _, err := jsonArrayItems(cfg, event.value.start)
	if err != nil {
		return nil, fmt.Errorf("the %s member is not a JSON array", e.Event)
	}
	for _, item := range items {
		if !j.mine(cfg, item) {
			continue
		}
		if jsonEqual(cfg[item.start:item.end], []byte(snippet)) {
			return cfg, nil
		}
		// an entry of ours that is no longer ours: take it out, then write ours in
		next := jsonCutMember(cfg, jsonMember{span: item})
		start, err := j.arrayStartOf(next, e)
		if err != nil {
			return nil, err
		}
		return jsonInsertArrayItem(next, start, snippet)
	}
	return jsonInsertArrayItem(cfg, event.value.start, snippet)
}

// arrayStartOf finds one event's array again in edited bytes: every cut moves the offsets.
func (j jsonHooks) arrayStartOf(cfg []byte, e HookEvent) (int, error) {
	parent, err := jsonFind(cfg, 0, j.parent)
	if err != nil || parent == nil {
		return 0, fmt.Errorf("the %s member is gone", j.parent)
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return 0, err
	}
	event := jsonMemberNamed(members, e.Event)
	if event == nil {
		return 0, fmt.Errorf("the %s member is gone", e.Event)
	}
	return event.value.start, nil
}

func (j jsonHooks) remove(cfg []byte, bin, _, _ string) ([]byte, error) {
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	found := false
	for _, e := range j.hookEvents {
		// the spans move with every cut: the parent and its members are found again per event
		parent, err := jsonFind(cfg, 0, j.parent)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			break
		}
		members, _, err := jsonMembers(cfg, parent.value.start)
		if err != nil {
			return nil, fmt.Errorf("the %s member is not a JSON object", j.parent)
		}
		event := jsonMemberNamed(members, e.Event)
		if event == nil {
			continue
		}
		items, _, err := jsonArrayItems(cfg, event.value.start)
		if err != nil {
			return nil, fmt.Errorf("the %s member is not a JSON array", e.Event)
		}
		for _, item := range items {
			if !j.mine(cfg, item) {
				continue
			}
			if !jsonEqual(cfg[item.start:item.end], j.snippet(bin, e)) {
				return cfg, ErrEntryChanged
			}
			next := jsonCutMember(cfg, jsonMember{span: item})
			// the entry was the array's only item: the event goes with it, and an empty
			// parent with it, so the splice is the exact inverse of an insert
			cfg = j.cutEmptied(next, e)
			found = true
			break
		}
	}
	if !found {
		return cfg, ErrEntryChanged
	}
	return cfg, nil
}

// cutEmptied answers the config with the event member gone when its array is now empty, and
// the parent member gone when nothing is left under it.
func (j jsonHooks) cutEmptied(cfg []byte, e HookEvent) []byte {
	parent, err := jsonFind(cfg, 0, j.parent)
	if err != nil || parent == nil {
		return cfg
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return cfg
	}
	event := jsonMemberNamed(members, e.Event)
	if event != nil {
		if items, _, err := jsonArrayItems(cfg, event.value.start); err != nil || len(items) > 0 {
			return cfg
		}
		cfg = jsonCutMember(cfg, *event)
	}
	if inner, _, err := jsonMembers(cfg, parent.value.start); err != nil || len(inner) == 0 {
		if table, err := jsonFind(cfg, 0, j.parent); err == nil && table != nil {
			cfg = jsonCutMember(cfg, *table)
		}
	}
	return cfg
}

func (j jsonHooks) contains(cfg []byte, bin, _, _ string) bool {
	parent, err := jsonFind(cfg, 0, j.parent)
	if err != nil || parent == nil {
		return false
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return false
	}
	for _, e := range j.hookEvents {
		event := jsonMemberNamed(members, e.Event)
		if event == nil {
			continue
		}
		items, _, err := jsonArrayItems(cfg, event.value.start)
		if err != nil {
			continue
		}
		for _, item := range items {
			if j.mine(cfg, item) {
				return true
			}
		}
	}
	return false
}

// fresh answers the document install creates when the file is not there yet: the hooks
// object with every event's entry in it.
func (j jsonHooks) fresh(bin string) []byte {
	members := make([]string, 0, len(j.hookEvents))
	for _, e := range j.hookEvents {
		members = append(members, fmt.Sprintf("%q:[%s]", e.Event, j.snippet(bin, e)))
	}
	doc := map[string]json.RawMessage{
		j.parent: json.RawMessage("{" + strings.Join(members, ",") + "}"),
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Appendf(nil, "{%q:{%s}}\n", j.parent, strings.Join(members, ","))
	}
	return append(out, '\n')
}

// noHooks is the edit of a harness huddle wires no hooks into: omp and pi run their hooks as
// TypeScript extensions, and huddle ships none for them.
type noHooks struct{}

func (noHooks) events() []HookEvent                                { return nil }
func (noHooks) install(cfg []byte, _, _, _ string) ([]byte, error) { return cfg, nil }
func (noHooks) remove(cfg []byte, _, _, _ string) ([]byte, error)  { return cfg, nil }
func (noHooks) contains([]byte, string, string, string) bool       { return false }

// pluginHooks is opencode's hook config: a JS plugin module the config's plugin array names
// by file:// URL. The module itself is huddle's own file in the huddle home, rewritten by
// every install; the config only names it.
type pluginHooks struct{}

func (p pluginHooks) events() []HookEvent { return opencodeHookEvents }

// entry answers the plugin array item that names the shim.
func entryOf(shim string) string { return "file://" + shim }

func (p pluginHooks) install(cfg []byte, _, _, shim string) ([]byte, error) {
	if cfg == nil {
		return fmt.Appendf(nil, "{%q:[%s]}\n", "plugin", jsonString(entryOf(shim))), nil
	}
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	mine := jsonString(entryOf(shim))
	parent, err := jsonFind(cfg, 0, "plugin")
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return jsonInsertMember(cfg, 0, fmt.Sprintf("%q:[%s]", "plugin", mine))
	}
	items, _, err := jsonArrayItems(cfg, parent.value.start)
	if err != nil {
		return nil, fmt.Errorf("the plugin member is not a JSON array")
	}
	for _, item := range items {
		if jsonEqual(cfg[item.start:item.end], []byte(mine)) {
			return cfg, nil
		}
	}
	return jsonInsertArrayItem(cfg, parent.value.start, mine)
}

func (p pluginHooks) remove(cfg []byte, _, _, shim string) ([]byte, error) {
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	parent, err := jsonFind(cfg, 0, "plugin")
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return cfg, ErrEntryChanged
	}
	mine := jsonString(entryOf(shim))
	items, _, err := jsonArrayItems(cfg, parent.value.start)
	if err != nil {
		return nil, fmt.Errorf("the plugin member is not a JSON array")
	}
	for _, item := range items {
		if !jsonEqual(cfg[item.start:item.end], []byte(mine)) {
			continue
		}
		next := jsonCutMember(cfg, jsonMember{span: item})
		if left, _, err := jsonArrayItems(next, parent.value.start); err == nil && len(left) == 0 {
			if table, err := jsonFind(next, 0, "plugin"); err == nil && table != nil {
				next = jsonCutMember(next, *table)
			}
		}
		return next, nil
	}
	return cfg, ErrEntryChanged
}

// contains answers whether the config names a plugin that is huddle's shim: the exact URL
// when the home is known, else any item naming the shim's file under a harness directory.
func (p pluginHooks) contains(cfg []byte, _, _, shim string) bool {
	parent, err := jsonFind(cfg, 0, "plugin")
	if err != nil || parent == nil {
		return false
	}
	items, _, err := jsonArrayItems(cfg, parent.value.start)
	if err != nil {
		return false
	}
	for _, item := range items {
		if shim != "" && jsonEqual(cfg[item.start:item.end], []byte(jsonString(entryOf(shim)))) {
			return true
		}
		// the item's span carries its quotes: the decoded text is what names the shim
		if value, err := jsfmt.DecodeValue(cfg[item.start:item.end]); err == nil && value.Kind() == jsfmt.KindString {
			if strings.HasSuffix(value.Text(), "/harness/"+shimName) {
				return true
			}
		}
	}
	return false
}

// jsonString answers s as one JSON string literal. A string's marshal cannot fail.
func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// — the array side of the byte surgery, the mirror of the object side in jsonfile.go.

// jsonArrayItems walks the array whose `[` sits at start and answers its items' spans in
// order and the offset of its closing `]`.
func jsonArrayItems(data []byte, start int) ([]span, int, error) {
	dec := json.NewDecoder(bytes.NewReader(data[start:]))
	if tok, err := dec.Token(); err != nil {
		return nil, 0, err
	} else if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		return nil, 0, errors.New("not a JSON array")
	}
	var items []span
	for dec.More() {
		// More has read ahead to the peeked byte, which is the comma that separated the
		// previous item: the span starts past it (and past any whitespace), so an item's
		// bytes are its own
		itemStart := start + int(dec.InputOffset())
		for itemStart < len(data) && (isJSONSpace(data[itemStart]) || data[itemStart] == ',') {
			itemStart++
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, err
		}
		itemEnd := start + int(dec.InputOffset())
		for itemEnd > itemStart && isJSONSpace(data[itemEnd-1]) {
			itemEnd--
		}
		items = append(items, span{itemStart, itemEnd})
	}
	if _, err := dec.Token(); err != nil {
		return nil, 0, err
	}
	return items, start + int(dec.InputOffset()) - 1, nil
}

// jsonInsertArrayItem answers the document with one more item in the array that opens at
// start, comma separated from the items already there. The item lands before the array's
// trailing whitespace, so a later cut of it gives the byte back exactly.
func jsonInsertArrayItem(data []byte, arrStart int, snippet string) ([]byte, error) {
	items, closeOff, err := jsonArrayItems(data, arrStart)
	if err != nil {
		return nil, err
	}
	return jsonInsertAt(data, arrStart, closeOff, len(items), snippet), nil
}

// — the Codex hook block: the same marker discipline as the MCP tables, a pair of its own.

// The markers around huddle's hook block, one per line.
const (
	tomlHookBegin = "# huddle:hooks-begin"
	tomlHookEnd   = "# huddle:hooks-end"
)

// codexHookTimeout is the seconds one codex hook may run: the deadline the card grants an
// external hook, in codex's own unit.
const codexHookTimeout = 3

// tomlHooks is codex's hook config: one marker-delimited block of its own beside the MCP
// block, holding one matcher group and one command handler per event. The file's own bytes
// outside the block stay byte for byte, whatever TOML they hold.
type tomlHooks struct{}

func (tomlHooks) events() []HookEvent { return codexHookEvents }

func (tomlHooks) install(cfg []byte, bin, name, _ string) ([]byte, error) {
	if cfg == nil {
		return tomlHookBlock(bin, name, ""), nil
	}
	if err := (tomlAdapter{}).check(cfg); err != nil {
		return nil, err
	}
	block, found := findMarked(cfg, tomlHookBegin, tomlHookEnd)
	if !found {
		return append(cfg, tomlHookBlock(bin, name, "\n")...), nil
	}
	want := tomlHookInner(bin, name)
	if block.inner == want {
		return cfg, nil
	}
	out := make([]byte, 0, len(cfg)-len(block.inner)+len(want))
	out = append(out, cfg[:block.innerStart]...)
	out = append(out, want...)
	out = append(out, cfg[block.innerEnd:]...)
	return out, nil
}

func (tomlHooks) remove(cfg []byte, bin, name, _ string) ([]byte, error) {
	if err := (tomlAdapter{}).check(cfg); err != nil {
		return nil, err
	}
	block, found := findMarked(cfg, tomlHookBegin, tomlHookEnd)
	if !found {
		return cfg, ErrEntryChanged
	}
	if block.inner != tomlHookInner(bin, name) {
		return cfg, ErrEntryChanged
	}
	return append(append([]byte{}, cfg[:block.cutStart]...), cfg[block.cutEnd:]...), nil
}

func (tomlHooks) contains(cfg []byte, _, _, _ string) bool {
	_, found := findMarked(cfg, tomlHookBegin, tomlHookEnd)
	return found
}

// tomlHookBlock answers the bytes install appends: the marker lines around the per-event
// tables, preceded by the lead ("" for a file that is not there, one newline for one that is).
func tomlHookBlock(bin, name, lead string) []byte {
	lines := []string{tomlHookBegin}
	for _, e := range codexHookEvents {
		lines = append(lines,
			"[[hooks."+e.Event+"]]",
			"[[hooks."+e.Event+".hooks]]",
			"type = \"command\"",
			"command = "+tomlString(hookCommand(bin, name, e.Event)),
			"timeout = "+strconv.Itoa(codexHookTimeout),
		)
	}
	lines = append(lines, tomlHookEnd)
	return []byte(lead + strings.Join(lines, "\n") + "\n")
}

// tomlHookInner answers the lines install writes between the two markers.
func tomlHookInner(bin, name string) string {
	full := tomlHookBlock(bin, name, "")
	return string(full[len(tomlHookBegin)+1 : len(full)-len(tomlHookEnd)-1])
}
