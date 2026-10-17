// The record cases: parsing, model-env collection, line building, the run, the real IO and
// spool retention.
package hook_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/hook"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// now is the fixed clock every case injects.
var now = time.Date(2026, 1, 15, 6, 30, 0, 0, time.UTC)

// testEnv is a whole temp world: HOME, RADAR_HOME, CLAUDE_CONFIG_DIR and ZAI_STATE_DIR all
// point into one throwaway directory, so no test can read or write anything real — not the
// user's transcripts, not the zai jobs dir, not a live server.json. Built from scratch (never
// copied from the process environment) so a stray variable set in the surrounding shell cannot
// leak a real path in.
type testEnv struct {
	environ []string
	env     domain.EnvLookup
	home    string
	state   string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	state := filepath.Join(home, "state")
	environ := []string{
		"HOME=" + home,
		"RADAR_HOME=" + state,
		"CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude"),
		"ZAI_STATE_DIR=" + filepath.Join(home, "zai"),
	}
	return testEnv{environ: environ, env: envOf(environ), home: home, state: state}
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

// withEnv returns the environment with one key's value replaced.
func withEnv(e testEnv, key, value string) []string {
	env := make([]string, 0, len(e.environ)+1)
	replaced := false
	for _, entry := range e.environ {
		if k, _, _ := strings.Cut(entry, "="); k == key {
			env = append(env, key+"="+value)
			replaced = true
		} else {
			env = append(env, entry)
		}
	}
	if !replaced {
		env = append(env, key+"="+value)
	}
	return env
}

// writeText mkdir -p's the parent, then writes with 0600 — the mode every spool/state file
// has.
func writeText(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

type recorded struct{ path, text string }

// fakeIo records appends and error lines instead of writing, so RunHook stays pure enough to
// test every branch.
type fakeIo struct {
	appended []recorded
	logged   []recorded
}

// fakeIoFor builds the recording IO with the fixed clock; fail makes every append fail with it.
func fakeIoFor(t *testing.T, e testEnv, fail error) (hook.HookIo, *fakeIo) {
	t.Helper()
	f := &fakeIo{}
	io := hook.HookIo{
		Env:     e.env,
		Environ: e.environ,
		Now:     func() time.Time { return now },
		PID:     4242,
		Ppid:    4243,
	}
	if fail != nil {
		io.AppendLine = func(context.Context, string, string) error { return fail }
	} else {
		io.AppendLine = func(_ context.Context, path, text string) error {
			f.appended = append(f.appended, recorded{path, text})
			return nil
		}
	}
	io.LogError = func(_ context.Context, path, text string) {
		f.logged = append(f.logged, recorded{path, text})
	}
	return io, f
}

func TestParsePayload(t *testing.T) {
	t.Run("treats blank stdin as an empty payload", func(t *testing.T) {
		for _, raw := range []string{"", "  \n "} {
			payload := hook.ParsePayload(raw)
			if payload == nil {
				t.Fatalf("ParsePayload(%q) = nil, want an empty payload", raw)
			}
			if payload.Len() != 0 {
				t.Errorf("ParsePayload(%q) holds %d fields, want 0", raw, payload.Len())
			}
		}
	})

	t.Run("returns JSON objects as-is and folds other JSON shapes into {}", func(t *testing.T) {
		payload := hook.ParsePayload(`{"a":1}`)
		if payload == nil || payload.Len() != 1 {
			t.Fatalf(`ParsePayload({"a":1}) = %v (len %d), want one field`, payload, lenOf(payload))
		}
		if value, ok := payload.Field("a"); !ok || value.EncodeJSON() != "1" {
			t.Errorf(`Field("a") = %v, %t; want 1, true`, value, ok)
		}
		// A repeated key keeps its first position and its last value, the way JSON.parse keeps it.
		dupes := hook.ParsePayload(`{"a":1,"a":2}`)
		if dupes == nil || dupes.Len() != 1 {
			t.Fatalf(`ParsePayload({"a":1,"a":2}) holds %d fields, want 1`, lenOf(dupes))
		}
		if value, ok := dupes.Field("a"); !ok || value.EncodeJSON() != "2" {
			t.Errorf(`Field("a") = %v, %t; want 2, true`, value, ok)
		}
		for _, raw := range []string{`[1,2]`, `"text"`, `42`} {
			if payload := hook.ParsePayload(raw); payload == nil || payload.Len() != 0 {
				t.Errorf("ParsePayload(%q) holds %d fields, want 0", raw, lenOf(payload))
			}
		}
	})

	t.Run("returns null only for unparseable input", func(t *testing.T) {
		if hook.ParsePayload("{oops") != nil {
			t.Error(`ParsePayload("{oops") is parseable, want nil`)
		}
	})
}

// lenOf keeps nil payloads readable in failure messages.
func lenOf(payload *hook.Payload) int {
	if payload == nil {
		return -1
	}
	return payload.Len()
}

func TestCollectModelEnv(t *testing.T) {
	t.Run("keeps model-id vars only, masking their values like any string", func(t *testing.T) {
		got := hook.CollectModelEnv([]string{
			"ANTHROPIC_MODEL=sk-abcd12345",
			"ANTHROPIC_SMALL_FAST_MODEL=haiku",
			"CLAUDE_CODE_SUBAGENT_MODEL=glm-5.3",
			"ANTHROPIC_BASE_URL=http://127.0.0.1:8787",
			"ANTHROPIC_API_KEY=nope",
		})
		want := []hook.ModelEnvVar{
			{Key: "ANTHROPIC_MODEL", Value: "[redacted]"},
			{Key: "ANTHROPIC_SMALL_FAST_MODEL", Value: "haiku"},
			{Key: "CLAUDE_CODE_SUBAGENT_MODEL", Value: "glm-5.3"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("CollectModelEnv = %v, want %v", got, want)
		}
	})

	t.Run("returns undefined when nothing matches or values are empty", func(t *testing.T) {
		if got := hook.CollectModelEnv([]string{"ANTHROPIC_BASE_URL=http://x", "HOME=/h"}); got != nil {
			t.Errorf("CollectModelEnv = %v, want nil", got)
		}
		if got := hook.CollectModelEnv([]string{"ANTHROPIC_MODEL="}); got != nil {
			t.Errorf("CollectModelEnv = %v, want nil", got)
		}
	})
}

func TestBuildSpoolLine(t *testing.T) {
	t.Run("maps every known field, scrubs the fuzzy ones and stamps pid/ppid", func(t *testing.T) {
		io, _ := fakeIoFor(t, makeEnv(t), nil)
		got := hook.BuildSpoolLine(hook.ParsePayload(`{
			"hook_event_name": "PreToolUse",
			"session_id": "s1",
			"transcript_path": "/t/s1.jsonl",
			"cwd": "/w/app",
			"agent_type": "general-purpose",
			"tool_name": "Bash",
			"tool_use_id": "tu1",
			"source": "startup",
			"reason": "clear",
			"trigger": "prompt",
			"prompt": "hello Bearer abc123 token",
			"tool_input": {"command": "ls", "api_key": "zzz"},
			"tool_response": {"ok": true, "token": "Bearer sk-abcdefgh12"}
		}`), &io)
		want := `{"ts":"2026-01-15T06:30:00.000Z","event":"PreToolUse","session_id":"s1",` +
			`"transcript_path":"/t/s1.jsonl","cwd":"/w/app","agent_type":"general-purpose",` +
			`"tool_name":"Bash","tool_use_id":"tu1","source":"startup","reason":"clear",` +
			`"trigger":"prompt","prompt":"hello Bearer [redacted] token",` +
			`"tool_input":{"command":"ls","api_key":"[redacted]"},` +
			`"tool_response":{"ok":true,"token":"[redacted]"},"pid":4242,"ppid":4243}`
		if got != want {
			t.Errorf("BuildSpoolLine =\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("defaults the event to unknown and prefers agent_id over agentId", func(t *testing.T) {
		io, _ := fakeIoFor(t, makeEnv(t), nil)
		if got := hook.BuildSpoolLine(hook.ParsePayload(`{}`), &io); got != `{"ts":"2026-01-15T06:30:00.000Z","event":"unknown","pid":4242,"ppid":4243}` {
			t.Errorf("BuildSpoolLine({}) = %s", got)
		}
		if got := hook.BuildSpoolLine(hook.ParsePayload(`{"agentId":"w1"}`), &io); got != `{"ts":"2026-01-15T06:30:00.000Z","event":"unknown","agent_id":"w1","pid":4242,"ppid":4243,"agentId":"w1"}` {
			t.Errorf("BuildSpoolLine({agentId}) = %s", got)
		}
		if got := hook.BuildSpoolLine(hook.ParsePayload(`{"agent_id":"first","agentId":"second"}`), &io); got != `{"ts":"2026-01-15T06:30:00.000Z","event":"unknown","agent_id":"first","pid":4242,"ppid":4243,"agentId":"second"}` {
			t.Errorf("BuildSpoolLine({agent_id, agentId}) = %s", got)
		}
	})

	t.Run("adds upstream, model env and version on SessionStart only", func(t *testing.T) {
		bare, _ := fakeIoFor(t, makeEnv(t), nil)
		if got := hook.BuildSpoolLine(hook.ParsePayload(`{"hook_event_name":"SessionStart"}`), &bare); got != `{"ts":"2026-01-15T06:30:00.000Z","event":"SessionStart","pid":4242,"ppid":4243,"base_url":"https://api.anthropic.com"}` {
			t.Errorf("bare SessionStart = %s", got)
		}
		rich := testEnv{environ: []string{
			"ANTHROPIC_BASE_URL=http://127.0.0.1:8787",
			"CLAUDE_CODE_VERSION=2.1.0",
			"CLAUDE_CODE_MODEL=glm-5.3",
		}}
		rich.env = envOf(rich.environ)
		richIO, _ := fakeIoFor(t, rich, nil)
		if got := hook.BuildSpoolLine(hook.ParsePayload(`{"hook_event_name":"SessionStart"}`), &richIO); got != `{"ts":"2026-01-15T06:30:00.000Z","event":"SessionStart","pid":4242,"ppid":4243,`+
			`"base_url":"http://127.0.0.1:8787","model_env":{"CLAUDE_CODE_MODEL":"glm-5.3"},"cc_version":"2.1.0"}` {
			t.Errorf("rich SessionStart = %s", got)
		}
		if got := hook.BuildSpoolLine(hook.ParsePayload(`{"hook_event_name":"SessionEnd"}`), &richIO); got != `{"ts":"2026-01-15T06:30:00.000Z","event":"SessionEnd","pid":4242,"ppid":4243}` {
			t.Errorf("SessionEnd = %s", got)
		}
	})

	t.Run("rides unknown keys along scrubbed and never duplicates its own", func(t *testing.T) {
		io, _ := fakeIoFor(t, makeEnv(t), nil)
		got := hook.BuildSpoolLine(hook.ParsePayload(`{
			"event": "hijack",
			"hook_event_name": "Notification",
			"pid": 999,
			"base_url": "http://elsewhere",
			"surprise": "sk-abcdefgh99",
			"nested": {"password": "hunter2", "safe": 1}
		}`), &io)
		want := `{"ts":"2026-01-15T06:30:00.000Z","event":"Notification","pid":4242,"ppid":4243,` +
			`"surprise":"[redacted]","nested":{"password":"[redacted]","safe":1}}`
		if got != want {
			t.Errorf("BuildSpoolLine =\n%s\nwant\n%s", got, want)
		}
	})
}

func TestRunHook(t *testing.T) {
	ctx := context.Background()

	t.Run("appends one spool line for the payload's date and returns the event name", func(t *testing.T) {
		e := makeEnv(t)
		io, f := fakeIoFor(t, e, nil)
		event, ok := hook.RunHook(ctx,
			`{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"hi"}`, &io)
		if !ok || event != "UserPromptSubmit" {
			t.Fatalf("RunHook = (%q, %t), want (UserPromptSubmit, true)", event, ok)
		}
		if len(f.appended) != 1 {
			t.Fatalf("appended %d lines, want 1", len(f.appended))
		}
		wantPath := filepath.Join(paths.SpoolDir(ctx, e.env), "2026-01-15.jsonl")
		if f.appended[0].path != wantPath {
			t.Errorf("append path = %s, want %s", f.appended[0].path, wantPath)
		}
		want := `{"ts":"2026-01-15T06:30:00.000Z","event":"UserPromptSubmit","session_id":"s1","prompt":"hi","pid":4242,"ppid":4243}` + "\n"
		if f.appended[0].text != want {
			t.Errorf("appended text = %s, want %s", f.appended[0].text, want)
		}
	})

	t.Run("records blank stdin as an unknown event", func(t *testing.T) {
		e := makeEnv(t)
		io, f := fakeIoFor(t, e, nil)
		event, ok := hook.RunHook(ctx, "   ", &io)
		if !ok || event != "unknown" {
			t.Fatalf("RunHook = (%q, %t), want (unknown, true)", event, ok)
		}
		want := `{"ts":"2026-01-15T06:30:00.000Z","event":"unknown","pid":4242,"ppid":4243}` + "\n"
		if f.appended[0].text != want {
			t.Errorf("appended text = %s, want %s", f.appended[0].text, want)
		}
	})

	t.Run("logs unparseable stdin instead of appending", func(t *testing.T) {
		e := makeEnv(t)
		io, f := fakeIoFor(t, e, nil)
		event, ok := hook.RunHook(ctx, "{oops", &io)
		if ok || event != "" {
			t.Fatalf("RunHook = (%q, %t), want (\"\", false)", event, ok)
		}
		if len(f.appended) != 0 {
			t.Errorf("appended %d lines, want none", len(f.appended))
		}
		want := []recorded{{path: paths.HookErrorLog(ctx, e.env), text: "unparseable stdin (5 bytes)"}}
		if !reflect.DeepEqual(f.logged, want) {
			t.Errorf("logged = %v, want %v", f.logged, want)
		}
	})

	t.Run("survives a failing append by logging the reason", func(t *testing.T) {
		e := makeEnv(t)
		io, f := fakeIoFor(t, e, errors.New("disk full"))
		event, ok := hook.RunHook(ctx, "{}", &io)
		if ok || event != "" {
			t.Fatalf("RunHook = (%q, %t), want (\"\", false)", event, ok)
		}
		want := []recorded{{path: paths.HookErrorLog(ctx, e.env), text: "disk full"}}
		if !reflect.DeepEqual(f.logged, want) {
			t.Errorf("logged = %v, want %v", f.logged, want)
		}
	})
}

func TestMakeIo(t *testing.T) {
	ctx := context.Background()

	t.Run("appends through real IO: dirs 0700, files 0600, one parseable line", func(t *testing.T) {
		e := makeEnv(t)
		io := hook.MakeIo(e.environ, func() time.Time { return now }, 11, 12)
		path := filepath.Join(paths.SpoolDir(ctx, e.env), "2026-01-15.jsonl")
		if err := io.AppendLine(ctx, path, "x\n"); err != nil {
			t.Fatalf("AppendLine: %v", err)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "x\n" {
			t.Fatalf("spool holds %q (%v), want \"x\\n\"", data, err)
		}
		if info, err := os.Stat(path); err != nil ||
			(info.Mode().Perm() != 0o600) {
			t.Errorf("spool mode = %v (%v), want 0600", info.Mode().Perm(), err)
		}
		if info, err := os.Stat(paths.SpoolDir(ctx, e.env)); err != nil ||
			(info.Mode().Perm() != 0o700) {
			t.Errorf("spool dir mode = %v (%v), want 0700", info.Mode().Perm(), err)
		}
		if event, ok := hook.RunHook(ctx, `{"hook_event_name":"PreToolUse","session_id":"s1"}`, &io); !ok || event != "PreToolUse" {
			t.Fatalf("RunHook = (%q, %t), want (PreToolUse, true)", event, ok)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "x\n" + `{"ts":"2026-01-15T06:30:00.000Z","event":"PreToolUse","session_id":"s1","pid":11,"ppid":12}` + "\n"
		if string(data) != want {
			t.Errorf("spool holds %s, want %s", data, want)
		}
	})

	t.Run("appends timestamped error lines", func(t *testing.T) {
		e := makeEnv(t)
		if err := os.MkdirAll(e.state, 0o700); err != nil {
			t.Fatal(err)
		}
		io := hook.MakeIo(e.environ, func() time.Time { return now }, 1, 2)
		path := paths.HookErrorLog(ctx, e.env)
		io.LogError(ctx, path, "boom")
		io.LogError(ctx, path, "again")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "2026-01-15T06:30:00.000Z boom\n2026-01-15T06:30:00.000Z again\n"
		if string(data) != want {
			t.Errorf("error log holds %q, want %q", data, want)
		}
	})

	t.Run("stays silent when even the error log cannot be written", func(t *testing.T) {
		e := makeEnv(t) // no state dir exists yet
		io := hook.MakeIo(e.environ, func() time.Time { return now }, 1, 2)
		io.LogError(ctx, paths.HookErrorLog(ctx, e.env), "x")
		if _, err := os.Stat(paths.HookErrorLog(ctx, e.env)); !os.IsNotExist(err) {
			t.Errorf("error log exists (err %v), want nothing written", err)
		}
	})

	t.Run("shrinks an oversized error log to its newer half, cut at a line boundary", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, paths.HookErrorLog(ctx, e.env), strings.Repeat(strings.Repeat("a", 50)+"\n", 21_000))
		io := hook.MakeIo(e.environ, func() time.Time { return now }, 1, 2)
		io.LogError(ctx, path, "boom")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if len(content) <= 500_000 {
			t.Errorf("error log shrank to %d bytes, want more than 500000", len(content))
		}
		if len(content) >= 525_000 {
			t.Errorf("error log kept %d bytes, want fewer than 525000", len(content))
		}
		if !strings.HasPrefix(content, strings.Repeat("a", 50)) {
			t.Error("error log starts mid-line, want a whole line first")
		}
		if !strings.HasSuffix(content, "2026-01-15T06:30:00.000Z boom\n") {
			t.Errorf("error log ends with %q, want the new line", content[len(content)-40:])
		}
	})
}

func TestPruneSpool(t *testing.T) {
	t.Run("drops the oldest days first until the spool fits its cap", func(t *testing.T) {
		e := makeEnv(t)
		dir := paths.SpoolDir(context.Background(), e.env)
		for _, day := range []string{"2026-01-12", "2026-01-13", "2026-01-14", "2026-01-15"} {
			writeText(t, filepath.Join(dir, day+".jsonl"), strings.Repeat(strings.Repeat("x", 99)+"\n", 10)) // 1000 bytes each
		}
		writeText(t, filepath.Join(dir, "notes.txt"), strings.Repeat("y", 5000)) // not a spool file: never counted, never deleted
		hook.PruneSpool(dir, 2500)
		assertDirEntries(t, dir, []string{"2026-01-14.jsonl", "2026-01-15.jsonl", "notes.txt"})
	})

	t.Run("keeps the newer half of a single day that is past the cap, cut at a line boundary", func(t *testing.T) {
		e := makeEnv(t)
		dir := paths.SpoolDir(context.Background(), e.env)
		path := writeText(t, filepath.Join(dir, "2026-01-15.jsonl"), strings.Repeat(strings.Repeat("z", 9)+"\n", 100))
		hook.PruneSpool(dir, 400)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		left := string(data)
		if len(left) > 200 || len(left) == 0 {
			t.Fatalf("prune left %d bytes, want (0, 200]", len(left))
		}
		for _, line := range strings.Split(left, "\n") {
			if line != "" && line != strings.Repeat("z", 9) {
				t.Errorf("prune left a partial line %q", line)
			}
		}
	})

	t.Run("never throws: a missing spool dir is simply nothing to prune", func(t *testing.T) {
		e := makeEnv(t)
		hook.PruneSpool(filepath.Join(paths.SpoolDir(context.Background(), e.env), "missing"), hook.SpoolCap)
	})

	t.Run("runs after every real append, so the spool cannot grow without bound", func(t *testing.T) {
		e := makeEnv(t)
		dir := paths.SpoolDir(context.Background(), e.env)
		writeText(t, filepath.Join(dir, "2020-01-01.jsonl"), strings.Repeat("x", 70*1_048_576)) // an old day past the whole budget
		io := hook.MakeIo(e.environ, func() time.Time { return now }, 1, 2)
		if event, ok := hook.RunHook(context.Background(), `{"hook_event_name":"Stop","session_id":"s1"}`, &io); !ok || event != "Stop" {
			t.Fatalf("RunHook = (%q, %t), want (Stop, true)", event, ok)
		}
		assertDirEntries(t, dir, []string{"2026-01-15.jsonl"})
	})
}

// assertDirEntries compares a directory's sorted names.
func assertDirEntries(t *testing.T, dir string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s holds %v, want %v", dir, got, want)
	}
}
