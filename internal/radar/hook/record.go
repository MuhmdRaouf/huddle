// Package hook turns one Claude Code payload into one spool line. It must never break Claude
// Code: any failure is swallowed (logged to the state dir, capped), nothing is printed, and
// the process exits 0 fast. The only write is a single append of one JSON line — O_APPEND
// keeps concurrent sessions from clobbering each other.
package hook

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// HookIo is the hook's whole view of the world, injected so tests can fake it.
type HookIo struct {
	// Env resolves paths and the session-start extras.
	Env domain.EnvLookup
	// Environ is the environment in arrival order: model-id vars keep that order in the line.
	Environ []string
	Now     func() time.Time
	PID     int64
	Ppid    int64
	// AppendLine appends one line to a path, creating the spool dir first; an error is caught
	// by RunHook.
	AppendLine func(ctx context.Context, path, line string) error
	// LogError is the best-effort error log; implementations swallow their own failures.
	LogError func(ctx context.Context, path, line string)
}

// ModelEnvVar is one model-id env var and its masked value.
type ModelEnvVar struct {
	Key   string
	Value string
}

// Payload is a parsed hook payload: an object's fields in arrival order, each value kept as
// the JSON value it parses to.
type Payload struct {
	object *jsfmt.Value
}

// ParsePayload reads one hook payload: blank stdin and any JSON shape that is not an object
// parse as an empty payload, unparseable text as nil.
func ParsePayload(raw string) *Payload {
	text := jsTrim(raw)
	if text == "" {
		return &Payload{object: jsfmt.Object(nil, nil)}
	}
	value, err := jsfmt.DecodeValue([]byte(text))
	if err != nil {
		return nil
	}
	if value.Kind() != jsfmt.KindObject {
		return &Payload{object: jsfmt.Object(nil, nil)}
	}
	return &Payload{object: dedupeKeys(value)}
}

// jsTrim cuts what JavaScript's trim cuts: Go's space set minus NEL, plus the BOM.
func jsTrim(raw string) string {
	return strings.TrimFunc(raw, func(r rune) bool {
		return r == 0xFEFF || (unicode.IsSpace(r) && r != 0x85)
	})
}

// dedupeKeys keeps each key once — its first position and its last value, the way JSON.parse
// keeps a repeated key.
func dedupeKeys(object *jsfmt.Value) *jsfmt.Value {
	keys := object.Keys()
	fields := object.Fields()
	seen := make(map[string]int, len(keys))
	var outKeys []string
	var outFields []*jsfmt.Value
	for i, key := range keys {
		if at, ok := seen[key]; ok {
			outFields[at] = fields[i]
			continue
		}
		seen[key] = len(outKeys)
		outKeys = append(outKeys, key)
		outFields = append(outFields, fields[i])
	}
	return jsfmt.Object(outKeys, outFields)
}

// Len counts the payload's fields.
func (p *Payload) Len() int {
	return len(p.object.Keys())
}

// Field returns one field's JSON value.
func (p *Payload) Field(key string) (*jsfmt.Value, bool) {
	keys := p.object.Keys()
	for i, existing := range keys {
		if existing == key {
			return p.object.Fields()[i], true
		}
	}
	return nil, false
}

// strField reads a field that is a JSON string; anything else reads as absent.
func (p *Payload) strField(key string) (string, bool) {
	field, ok := p.Field(key)
	if !ok || field.Kind() != jsfmt.KindString {
		return "", false
	}
	return field.Text(), true
}

// modelEnvKey names an env var whose value is a model id worth recording on session start.
var modelEnvKey = regexp.MustCompile(`^(ANTHROPIC|CLAUDE_CODE)_[A-Z0-9_]*MODEL[A-Z0-9_]*$`)

// CollectModelEnv lists the model-id env vars in arrival order, their values masked like any
// other string; nil when the environment names none.
func CollectModelEnv(environ []string) []ModelEnvVar {
	var found []ModelEnvVar
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		if !modelEnvKey.MatchString(key) || value == "" {
			continue
		}
		masked, _ := shared.ScrubText(value, shared.FieldLimit)
		found = append(found, ModelEnvVar{Key: key, Value: masked})
	}
	return found
}

// ownedKeys are the keys the hook sets itself; unknown payload keys ride along scrubbed, these
// never duplicate.
var ownedKeys = map[string]struct{}{
	"ts": {}, "event": {}, "session_id": {}, "transcript_path": {}, "cwd": {},
	"agent_id": {}, "agent_type": {}, "tool_name": {}, "tool_use_id": {}, "tool_input": {},
	"tool_response": {}, "prompt": {}, "source": {}, "reason": {}, "trigger": {},
	"pid": {}, "ppid": {}, "base_url": {}, "model_env": {}, "cc_version": {},
	"hook_event_name": {},
}

// stringKeys are copied straight from the payload when present, in this order.
var stringKeys = []string{
	"session_id", "transcript_path", "cwd", "agent_type", "tool_name", "tool_use_id",
	"source", "reason", "trigger",
}

// BuildSpoolLine builds the spool line for one payload: known fields mapped, everything
// unknown kept but scrubbed.
func BuildSpoolLine(payload *Payload, io *HookIo) string {
	line, _ := buildLine(payload, io, io.Now())
	return line
}

// buildLine lays the fields out in the line's own order (the timestamp, the event, the string
// fields, the scrubbed ones, pid and ppid, the session extras, the unknown keys) and reports
// the event name, the one thing RunHook returns.
func buildLine(payload *Payload, io *HookIo, now time.Time) (string, string) {
	event := eventName(payload)
	var keys []string
	var fields []*jsfmt.Value
	add := func(key string, value *jsfmt.Value) {
		keys = append(keys, key)
		fields = append(fields, value)
	}
	add("ts", jsfmt.String(jsscalar.ISOTime(now)))
	add("event", jsfmt.String(event))
	for _, key := range stringKeys {
		if value, ok := payload.strField(key); ok {
			add(key, jsfmt.String(value))
		}
	}
	if agentID, ok := agentIDOf(payload); ok {
		add("agent_id", jsfmt.String(agentID))
	}
	if prompt, ok := promptOf(payload); ok {
		add("prompt", jsfmt.String(prompt))
	}
	if value, ok := toolField(payload, "tool_input", "toolInput"); ok {
		add("tool_input", shared.ScrubValue(value, shared.FieldLimit))
	}
	if value, ok := toolField(payload, "tool_response", "toolResponse"); ok {
		add("tool_response", shared.ScrubValue(value, shared.FieldLimit))
	}
	add("pid", number(io.PID))
	add("ppid", number(io.Ppid))
	if event == "SessionStart" {
		add("base_url", jsfmt.String(baseURL(io.Env)))
		if modelEnv := CollectModelEnv(io.Environ); modelEnv != nil {
			add("model_env", modelEnvValue(modelEnv))
		}
		if version := io.Env["CLAUDE_CODE_VERSION"]; version != "" {
			add("cc_version", jsfmt.String(version))
		}
	}
	objectKeys := payload.object.Keys()
	objectFields := payload.object.Fields()
	for i, key := range objectKeys {
		if _, owned := ownedKeys[key]; owned {
			continue
		}
		add(key, shared.ScrubValue(objectFields[i], shared.FieldLimit))
	}
	return jsfmt.Object(keys, fields).EncodeJSON(), event
}

// eventName is the payload's hook_event_name, or unknown when it says nothing.
func eventName(payload *Payload) string {
	if name, ok := payload.strField("hook_event_name"); ok {
		return name
	}
	return "unknown"
}

// agentIDOf prefers agent_id and falls back to agentId.
func agentIDOf(payload *Payload) (string, bool) {
	if id, ok := payload.strField("agent_id"); ok {
		return id, true
	}
	return payload.strField("agentId")
}

// promptOf masks and truncates the prompt when the payload carries one as a string.
func promptOf(payload *Payload) (string, bool) {
	value, ok := payload.strField("prompt")
	if !ok {
		return "", false
	}
	return shared.ScrubText(value, shared.FieldLimit)
}

// toolField reads the snake_case field, falling back to the camelCase one the way the nullish
// coalescing does: absent and null both fall through.
func toolField(payload *Payload, snake, camel string) (*jsfmt.Value, bool) {
	for _, key := range []string{snake, camel} {
		if field, ok := payload.Field(key); ok && field.Kind() != jsfmt.KindNull {
			return field, true
		}
	}
	return nil, false
}

// baseURL is where this session's requests go; nothing said means Anthropic itself.
func baseURL(env domain.EnvLookup) string {
	if url, ok := env["ANTHROPIC_BASE_URL"]; ok {
		return url
	}
	return shared.DefaultUpstream
}

// modelEnvValue lays the masked model-id vars out as an object, arrival order kept.
func modelEnvValue(modelEnv []ModelEnvVar) *jsfmt.Value {
	keys := make([]string, 0, len(modelEnv))
	fields := make([]*jsfmt.Value, 0, len(modelEnv))
	for _, entry := range modelEnv {
		keys = append(keys, entry.Key)
		fields = append(fields, jsfmt.String(entry.Value))
	}
	return jsfmt.Object(keys, fields)
}

// number builds a number value from an integer; the constructors cover text and shapes, so an
// integer goes through its own literal (always a valid one).
func number(n int64) *jsfmt.Value {
	value, err := jsfmt.DecodeValue([]byte(strconv.FormatInt(n, 10)))
	if err != nil {
		return jsfmt.String("0") // unreachable: a decimal integer always decodes
	}
	return value
}

// RunHook parses, builds and appends. It returns the recorded event name, and ok false when
// nothing reached the spool (the error log saw it instead) — the caller uses the name to
// decide about autostart, and nothing else.
func RunHook(ctx context.Context, raw string, io *HookIo) (string, bool) {
	payload := ParsePayload(raw)
	if payload == nil {
		io.LogError(ctx, paths.HookErrorLog(ctx, io.Env),
			fmt.Sprintf("unparseable stdin (%d bytes)", jsscalar.UTF16Len(raw)))
		return "", false
	}
	now := io.Now()
	line, event := buildLine(payload, io, now)
	path := filepath.Join(paths.SpoolDir(ctx, io.Env), jsscalar.ISOTime(now)[:10]+".jsonl")
	if err := io.AppendLine(ctx, path, line+"\n"); err != nil {
		io.LogError(ctx, paths.HookErrorLog(ctx, io.Env), err.Error())
		return "", false
	}
	return event, true
}

// SpoolCap is the spool's whole budget: past it, the oldest day goes first; a single day past
// it keeps its newer half.
const SpoolCap int64 = 64 << 20

// errorLogCap bounds the error log; errorLogTail is what survives the cut.
const (
	errorLogCap  int64 = 1 << 20
	errorLogTail int64 = 1 << 19
)

// spoolFile is one dated spool file's name.
var spoolFile = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}\.jsonl$`)

// spoolFileStat is one dated spool file and its size.
type spoolFileStat struct {
	path string
	size int64
}

// PruneSpool bounds the spool: while its dated files add up past cap, delete the oldest; when
// one file alone is past it, keep its newer half. A spool that cannot be pruned is still better
// than a hook that fails, so nothing here reports an error.
func PruneSpool(dir string, cap int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, entry := range entries {
		if spoolFile.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	files := make([]spoolFileStat, 0, len(names))
	var total int64
	for _, name := range names {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return
		}
		files = append(files, spoolFileStat{path: filepath.Join(dir, name), size: info.Size()})
		total += info.Size()
	}
	for total > cap && len(files) > 1 {
		oldest := files[0]
		files = files[1:]
		if err := os.Remove(oldest.path); err != nil {
			return
		}
		total -= oldest.size
	}
	if len(files) > 0 && files[0].size > cap {
		keepTail(files[0].path, files[0].size, cap/2)
	}
}

// keepTail rewrites a file to its last keep bytes, cut at a line boundary, so no line is left
// half there.
func keepTail(path string, size, keep int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	start := size - keep
	if start < 0 {
		start = 0
	}
	var tail []byte
	if start <= int64(len(data)) {
		tail = data[start:]
	} // else the file is shorter than the stat said; the tail is empty, as a slice clamps
	if cut := bytes.IndexByte(tail, '\n'); cut >= 0 {
		tail = tail[cut+1:]
	}
	_ = os.WriteFile(path, tail, 0o600)
}

// MakeIo is the real IO: 0700 state dirs, a bounded spool, one 0600 append, and an error log
// capped at 1 MB that keeps its newer half.
func MakeIo(environ []string, now func() time.Time, pid, ppid int64) HookIo {
	env := envOf(environ)
	return HookIo{
		Env:     env,
		Environ: environ,
		Now:     now,
		PID:     pid,
		Ppid:    ppid,
		AppendLine: func(ctx context.Context, path, text string) error {
			paths.EnsureStateDirs(ctx, env)
			if err := appendFile(path, text); err != nil {
				return err
			}
			PruneSpool(paths.SpoolDir(ctx, env), SpoolCap)
			return nil
		},
		LogError: func(ctx context.Context, path, text string) {
			line := jsscalar.ISOTime(now()) + " " + text + "\n"
			if info, err := os.Stat(path); err == nil && info.Size() > errorLogCap {
				keepTail(path, info.Size(), errorLogTail)
			}
			// the error log is a courtesy; if even it fails, silence is correct
			_ = appendFile(path, line)
		},
	}
}

// appendFile appends text to one 0600 file, creating it when it is not there.
func appendFile(path, text string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(text); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// envOf splits an ordered environment into the lookup the path resolvers take.
func envOf(environ []string) domain.EnvLookup {
	env := make(domain.EnvLookup, len(environ))
	for _, entry := range environ {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}
