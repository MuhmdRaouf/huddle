package harness

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The hook entries: every harness that has a lifecycle mechanism gets huddle's hook entries
// through the same install and remove the MCP entry rides, one ledger record, byte-exact
// round trips, and a user's own edit left alone. Payload facts per agent live in the
// discovery file (.agents/scratch/harness-hooks.md).

// hookCheck is what assertHooks wants one harness's installed config to carry.
type hookCheck struct {
	events  []string // the agent's own event names, each present exactly once
	timeout int      // every event's timeout, in the agent's own unit; 0 is none
}

// hookChecks are the assertions per harness: the events huddle installs and the timeout each
// carries, in the agent's own unit.
func hookChecks(name string) hookCheck {
	switch name {
	case "codex":
		return hookCheck{events: []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SessionEnd"}, timeout: 3}
	case "gemini":
		return hookCheck{events: []string{"SessionStart", "BeforeAgent", "BeforeTool", "AfterTool", "AfterAgent", "SessionEnd"}, timeout: 3000}
	case "qwen":
		return hookCheck{events: []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SessionEnd"}, timeout: 3}
	case "crush":
		return hookCheck{events: []string{"PreToolUse"}, timeout: 3}
	default: // opencode
		return hookCheck{events: []string{"session.created", "session.idle"}}
	}
}

// assertHooks parses cfg and wants huddle's hook entry per event of the named harness,
// each running its own event's command and carrying the check's timeout.
func assertHooks(t *testing.T, name string, cfg []byte, check hookCheck, home, shim string) {
	bin := filepath.Join(home, "bin", "huddle")
	t.Helper()
	switch name {
	case "codex":
		var doc struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Type    string `toml:"type"`
					Command string `toml:"command"`
					Timeout int    `toml:"timeout"`
				} `toml:"hooks"`
			} `toml:"hooks"`
		}
		if _, err := toml.Decode(string(cfg), &doc); err != nil {
			t.Fatalf("the config does not parse: %v\n%s", err, cfg)
		}
		for _, event := range check.events {
			groups := doc.Hooks[event]
			if len(groups) != 1 {
				t.Fatalf("the %s event carries %d matcher groups, want 1: %s", event, len(groups), cfg)
			}
			handlers := groups[0].Hooks
			if len(handlers) != 1 {
				t.Fatalf("the %s group holds %d handlers, want 1: %s", event, len(handlers), cfg)
			}
			if handlers[0].Type != "command" {
				t.Errorf("the %s handler type = %q, want command", event, handlers[0].Type)
			}
			if got := handlers[0].Command; got != hookCommand(bin, name, event) {
				t.Errorf("the %s command = %q, want %q", event, got, hookCommand(bin, name, event))
			}
			if handlers[0].Timeout != check.timeout {
				t.Errorf("the %s timeout = %d, want %d", event, handlers[0].Timeout, check.timeout)
			}
		}
		if len(doc.Hooks) != len(check.events) {
			t.Errorf("the hooks object holds %d events, want %d: %s", len(doc.Hooks), len(check.events), cfg)
		}
	case "opencode":
		value, err := jsfmt.DecodeValue(cfg)
		if err != nil {
			t.Fatalf("the config does not parse: %v\n%s", err, cfg)
		}
		plugins, ok := field(value, "plugin")
		if !ok || plugins.Kind() != jsfmt.KindArray {
			t.Fatalf("the config holds no plugin array: %s", cfg)
		}
		count := 0
		for _, item := range plugins.Items() {
			if item.Text() == "file://"+shim {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("the plugin array names the shim %d times, want 1: %s", count, cfg)
		}
	default:
		value, err := jsfmt.DecodeValue(cfg)
		if err != nil {
			t.Fatalf("the config does not parse: %v\n%s", err, cfg)
		}
		hooks, ok := field(value, "hooks")
		if !ok || hooks.Kind() != jsfmt.KindObject {
			t.Fatalf("the config holds no hooks object: %s", cfg)
		}
		for _, event := range check.events {
			array, ok := field(hooks, event)
			if !ok || array.Kind() != jsfmt.KindArray {
				t.Fatalf("the hooks object holds no %s array: %s", event, cfg)
			}
			if len(array.Items()) != 1 {
				t.Fatalf("the %s array holds %d entries, want 1: %s", event, len(array.Items()), cfg)
			}
			one := array.Items()[0]
			var handler *jsfmt.Value
			if name == "crush" {
				handler = one
			} else {
				inner, ok := field(one, "hooks")
				if !ok || inner.Kind() != jsfmt.KindArray || len(inner.Items()) != 1 {
					t.Fatalf("the %s group holds no single hook: %s", event, cfg)
				}
				handler = inner.Items()[0]
			}
			if got := textField(handler, "name"); got != "huddle" {
				t.Errorf("the %s hook name = %q, want huddle", event, got)
			}
			if got := textField(handler, "type"); got != "command" {
				t.Errorf("the %s hook type = %q, want command", event, got)
			}
			if got := textField(handler, "command"); got != hookCommand(bin, name, event) {
				t.Errorf("the %s command = %q, want %q", event, got, hookCommand(bin, name, event))
			}
			if got := int(numberText(handler, "timeout")); got != check.timeout {
				t.Errorf("the %s timeout = %d, want %d", event, got, check.timeout)
			}
		}
		if len(hooks.Keys()) != len(check.events) {
			t.Errorf("the hooks object holds %d events, want %d: %s", len(hooks.Keys()), len(check.events), cfg)
		}
	}
}

// numberText answers one number field, 0 when it is not one.
func numberText(value *jsfmt.Value, key string) float64 {
	one, ok := field(value, key)
	if !ok || one.Kind() != jsfmt.KindNumber {
		return 0
	}
	n, err := strconv.ParseFloat(one.EncodeJSON(), 64)
	if err != nil {
		return 0
	}
	return n
}

func TestInstallWritesTheHookEntriesBesideTheServer(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			check := hookChecks(name)
			shim := shimPath(home)
			path := a.ConfigPath(env)
			writeConfig(t, path, fixturesFor(t, a)[2].body)
			if _, err := Install(context.Background(), home, env, name, testEntry(home, name)); err != nil {
				t.Fatalf("install: %v", err)
			}
			cfg := readConfig(t, path)
			assertEntry(t, a, cfg, testEntry(home, name), true)
			assertHooks(t, name, cfg, check, home, shim)
			// the file's own bytes survive: the fixture's other server is still named
			if !strings.Contains(string(cfg), "other") {
				t.Errorf("install dropped the file's own content:\n%s", cfg)
			}
			// the list says the hooks are in
			for _, one := range List(env) {
				if one.Name == name && !one.Hooks {
					t.Errorf("list says hooks = false, want true")
				}
			}
		})
	}
}

func TestRemoveTakesTheHookEntriesBackByteForByte(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			path := a.ConfigPath(env)
			fixtures := fixturesFor(t, a)
			writeConfig(t, path, fixtures[2].body)
			if _, err := Install(context.Background(), home, env, name, testEntry(home, name)); err != nil {
				t.Fatalf("install: %v", err)
			}
			if _, err := Remove(context.Background(), home, env, name, testEntry(home, name)); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if raw := readConfig(t, path); !bytes.Equal(raw, []byte(fixtures[2].body)) {
				t.Errorf("remove left hooks behind:\nwant %q\ngot  %q", fixtures[2].body, raw)
			}
			for _, one := range List(env) {
				if one.Name == name && one.Hooks {
					t.Errorf("list says hooks = true after remove")
				}
			}
		})
	}
}

func TestInstallOfTheHooksTwiceChangesNothing(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			path := a.ConfigPath(env)
			writeConfig(t, path, fixturesFor(t, a)[2].body)
			if _, err := Install(context.Background(), home, env, name, testEntry(home, name)); err != nil {
				t.Fatalf("first install: %v", err)
			}
			once := readConfig(t, path)
			if _, err := Install(context.Background(), home, env, name, testEntry(home, name)); err != nil {
				t.Fatalf("second install: %v", err)
			}
			if twice := readConfig(t, path); !bytes.Equal(once, twice) {
				t.Errorf("the second install rewrote the hooks:\nfirst  %q\nsecond %q", once, twice)
			}
		})
	}
}

func TestRemoveLeavesAUserEditedHookEntryAlone(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			path := a.ConfigPath(env)
			writeConfig(t, path, fixturesFor(t, a)[2].body)
			if _, err := Install(context.Background(), home, env, name, testEntry(home, name)); err != nil {
				t.Fatalf("install: %v", err)
			}
			edited := userEditedHook(t, a, readConfig(t, path))
			writeConfig(t, path, edited)
			got, err := Remove(context.Background(), home, env, name, testEntry(home, name))
			if err != nil {
				t.Fatalf("remove: %v", err)
			}
			if !got.Kept {
				t.Errorf("kept = false, want true (%+v)", got)
			}
			if raw := readConfig(t, path); !bytes.Equal(raw, []byte(edited)) {
				t.Errorf("remove touched the edited hook entry:\nwant %q\ngot  %q", edited, raw)
			}
		})
	}
}

// userEditedHook answers the installed config with one huddle hook command rewritten, the
// way a user's hand would change it.
func userEditedHook(t *testing.T, a Adapter, cfg []byte) string {
	t.Helper()
	name := a.Name()
	h := hookFor(name)
	if h == nil {
		t.Fatalf("%s has no hook edit", name)
	}
	// opencode's entry is the plugin URL, not a command line; the user's hand points it
	// somewhere else
	if _, isPlugin := h.(pluginHooks); isPlugin {
		edited := strings.Replace(string(cfg), shimName, "rewritten.js", 1)
		if edited == string(cfg) {
			t.Fatalf("no plugin entry names the shim:\n%s", cfg)
		}
		return edited
	}
	events := h.events()
	if len(events) == 0 {
		t.Fatalf("%s installs no hook events", name)
	}
	// every installed command ends `hook external --harness <name> --event <event>`; the
	// user's hand rewrote the event the first one reports
	old := "--event " + events[0].Event
	edited := strings.Replace(string(cfg), old, "--event rewritten", 1)
	if edited == string(cfg) {
		t.Fatalf("no hook command names %q:\n%s", old, cfg)
	}
	return edited
}

func TestHookEventsMapToTheReportNames(t *testing.T) {
	wants := map[string]map[string]string{
		"codex":    {"SessionStart": "SessionStart", "UserPromptSubmit": "UserPromptSubmit", "PreToolUse": "PreToolUse", "PostToolUse": "PostToolUse", "Stop": "Stop", "SessionEnd": "SessionEnd"},
		"gemini":   {"SessionStart": "SessionStart", "BeforeAgent": "UserPromptSubmit", "BeforeTool": "PreToolUse", "AfterTool": "PostToolUse", "AfterAgent": "Stop", "SessionEnd": "SessionEnd"},
		"qwen":     {"SessionStart": "SessionStart", "UserPromptSubmit": "UserPromptSubmit", "PreToolUse": "PreToolUse", "PostToolUse": "PostToolUse", "Stop": "Stop", "SessionEnd": "SessionEnd"},
		"crush":    {"PreToolUse": "PreToolUse"},
		"opencode": {"session.created": "SessionStart", "session.idle": "Stop"},
	}
	for name, want := range wants {
		events, err := HookEvents(name)
		if err != nil {
			t.Fatalf("HookEvents(%q): %v", name, err)
		}
		got := map[string]string{}
		for _, one := range events {
			got[one.Event] = one.Report
		}
		if len(got) != len(want) {
			t.Errorf("%s installs %d events, want %d (%v)", name, len(got), len(want), got)
		}
		for event, report := range want {
			if got[event] != report {
				t.Errorf("%s %s maps to %q, want %q", name, event, got[event], report)
			}
		}
	}
	if _, err := HookEvents("nosuch"); err == nil {
		t.Error("HookEvents of an unknown harness answered no error")
	}
}

func TestHookCommandQuotesTheBinaryForAShell(t *testing.T) {
	got := hookCommand("/users/my tools/huddle", "codex", "PreToolUse")
	if !strings.HasPrefix(got, `"`) || !strings.Contains(got, `" hook external`) {
		t.Errorf("the binary is not quoted: %q", got)
	}
	if !strings.Contains(got, ` hook external --harness codex --event PreToolUse`) {
		t.Errorf("the command does not name the word, harness and event: %q", got)
	}
}

func TestTheOpencodeShimRidesTheInstallAndTheRemove(t *testing.T) {
	env, home := testEnv(t, nil)
	a, err := ByName("opencode")
	if err != nil {
		t.Fatal(err)
	}
	path := a.ConfigPath(env)
	writeConfig(t, path, fixturesFor(t, a)[1].body)
	if _, err := Install(context.Background(), home, env, "opencode", testEntry(home, "opencode")); err != nil {
		t.Fatalf("install: %v", err)
	}
	shim := shimPath(home)
	raw, err := os.ReadFile(shim)
	if err != nil {
		t.Fatalf("install wrote no shim: %v", err)
	}
	if info, err := os.Stat(shim); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the shim's mode is %v, want 0600 (err %v)", info.Mode().Perm(), err)
	}
	if !strings.Contains(string(raw), "hook external --harness opencode") {
		t.Errorf("the shim does not call the external word:\n%s", raw)
	}
	if !strings.Contains(string(raw), testEntry(home, "opencode").Command) {
		t.Errorf("the shim does not name the binary:\n%s", raw)
	}
	if _, err := Remove(context.Background(), home, env, "opencode", testEntry(home, "opencode")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(shim); !os.IsNotExist(err) {
		t.Errorf("remove left the shim behind: %v", err)
	}
}

// — the edits' own edges, the way codex_test.go pins the TOML adapter's: each edit is a pure
// function over the bytes, and every branch it answers is exercised here directly.

func TestEveryHarnessHasAHookEdit(t *testing.T) {
	for _, name := range Names() {
		h := hookFor(name)
		if h == nil {
			t.Errorf("%s has no hook edit", name)
			continue
		}
		if len(h.events()) == 0 {
			t.Errorf("%s installs no hook events", name)
		}
	}
	_, err := HookEvents("nosuch")
	var unknown UnknownHarnessError
	if !errors.As(err, &unknown) || unknown.Name != "nosuch" {
		t.Fatalf("HookEvents of an unknown harness = %v, want an UnknownHarnessError", err)
	}
}

// hookEditCases are the malformed and wrong-shaped configs one edit must refuse, per edit
// kind: the bytes come back untouched, the error says why.
func TestJSONHookEditRefusesBytesItCannotRead(t *testing.T) {
	gemini := hookFor("gemini").(jsonHooks)
	shim := ""
	bin := "/bin/huddle"
	cases := []struct {
		label string
		cfg   string
		want  string
	}{
		{"not JSON", "{", "not valid JSON"},
		{"not an object", "[1]", "not a JSON object"},
		{"the hooks member is not an object", `{"hooks": 3}`, "not a JSON object"},
		{"an event that is not an array", `{"hooks": {"SessionStart": 3}}`, "not a JSON array"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if _, err := gemini.install([]byte(tc.cfg), bin, "gemini", shim); err == nil {
				t.Fatal("install took the bytes")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to name %q", err, tc.want)
			}
			if _, err := gemini.remove([]byte(tc.cfg), bin, "gemini", shim); err == nil {
				t.Fatal("remove took the bytes")
			}
		})
	}
	// a file that is not there yet: the fresh document holds every event's entry
	fresh, err := gemini.install(nil, bin, "gemini", "")
	if err != nil {
		t.Fatalf("install on nothing: %v", err)
	}
	assertHooks(t, "gemini", fresh, hookChecks("gemini"), "/", "")
}

func TestJSONHookEditReplacesAChangedEntryAndRefusesAChangedRemove(t *testing.T) {
	crush := hookFor("crush").(jsonHooks)
	bin := "/bin/huddle"
	cfg := []byte(`{"theme":"dark"}`)
	installed, err := crush.install(cfg, bin, "crush", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// the user's hand rewrites the command (its bytes are JSON-escaped); install writes ours
	// back over it
	edited := []byte(strings.Replace(string(installed), jsonString(hookCommand(bin, "crush", "PreToolUse")),
		jsonString(hookCommand("/elsewhere/huddle", "crush", "PreToolUse")), 1))
	if bytes.Equal(edited, installed) {
		t.Fatal("the edit changed nothing")
	}
	back, err := crush.install(edited, bin, "crush", "")
	if err != nil {
		t.Fatalf("install over the edit: %v", err)
	}
	if !bytes.Equal(back, installed) {
		t.Errorf("install did not make it ours again:\nwant %q\ngot  %q", installed, back)
	}
	// the same hand edits, and remove refuses: the entry is no longer ours
	if _, err := crush.remove(edited, bin, "crush", ""); !errors.Is(err, ErrEntryChanged) {
		t.Errorf("remove = %v, want ErrEntryChanged", err)
	}
	// a config without the hooks object, and one without ours in the array, refuse too
	for _, cfg := range []string{`{"theme":"dark"}`, `{"hooks":{"PreToolUse":[{"name":"other"}]}}`} {
		if _, err := crush.remove([]byte(cfg), bin, "crush", ""); !errors.Is(err, ErrEntryChanged) {
			t.Errorf("remove of %s = %v, want ErrEntryChanged", cfg, err)
		}
	}
	if crush.contains(cfg, bin, "crush", "") {
		t.Error("contains saw hooks in a config that holds none")
	}
}

func TestJSONHookEditCutsAnEmptiedParentAndAnswersContains(t *testing.T) {
	gemini := hookFor("gemini").(jsonHooks)
	bin := "/bin/huddle"
	// one event's config: the entry was the array's only item, so the event and the parent
	// go with the cut
	one, err := gemini.install([]byte(`{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"name":"huddle","type":"command","command":"changed"}]}]}}`), bin, "gemini", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	out, err := gemini.remove(one, bin, "gemini", "")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(string(out), "hooks") {
		t.Errorf("remove left the emptied hooks object behind:\n%s", out)
	}
	if !gemini.contains(one, bin, "gemini", "") {
		t.Error("contains missed the installed entry")
	}
}

func TestPluginHookEditInstallsRemovesAndAnswersContains(t *testing.T) {
	oc := hookFor("opencode").(pluginHooks)
	shim := "/home/.config/huddle/harness/opencode-hook.js"
	mine := `{"plugin":["file://` + shim + `"]}`
	// a file that is not there yet
	fresh, err := oc.install(nil, "", "opencode", shim)
	if err != nil {
		t.Fatalf("install on nothing: %v", err)
	}
	if string(fresh) != mine+"\n" {
		t.Errorf("fresh = %q, want %q", fresh, mine+"\n")
	}
	// beside a plugin of the user's, comma separated; a second install changes nothing
	withOther := []byte("{\"theme\":\"dark\",\"plugin\":[\"file:///Users/x/own.js\"]}\n")
	installed, err := oc.install(withOther, "", "opencode", shim)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(string(installed), "own.js") || !strings.Contains(string(installed), shim) {
		t.Errorf("install lost a plugin:\n%s", installed)
	}
	again, err := oc.install(installed, "", "opencode", shim)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !bytes.Equal(again, installed) {
		t.Errorf("the second install rewrote the file:\nfirst  %q\nsecond %q", installed, again)
	}
	// the user points the entry somewhere else: remove refuses, and a plugin naming our shim
	// under another home still counts as ours
	if _, err := oc.remove(withOther, "", "opencode", shim); !errors.Is(err, ErrEntryChanged) {
		t.Errorf("remove = %v, want ErrEntryChanged", err)
	}
	if !oc.contains([]byte(`{"plugin":["file:///elsewhere/harness/opencode-hook.js"]}`), "", "opencode", "") {
		t.Error("contains missed a shim under another home")
	}
	if oc.contains([]byte(`{"plugin":["file:///elsewhere/other.js"]}`), "", "opencode", "") {
		t.Error("contains claimed a plugin that is not ours")
	}
	// remove cuts ours out of a shared array, and the whole member when it is the only one
	cut, err := oc.remove(installed, "", "opencode", shim)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if strings.Contains(string(cut), shim) {
		t.Errorf("remove left our entry behind:\n%s", cut)
	}
	if !strings.Contains(string(cut), "own.js") {
		t.Errorf("remove took the user's plugin with it:\n%s", cut)
	}
	cutAll, err := oc.remove([]byte(mine), "", "opencode", shim)
	if err != nil {
		t.Fatalf("remove of the only entry: %v", err)
	}
	if strings.Contains(string(cutAll), "plugin") {
		t.Errorf("remove left the emptied plugin member behind:\n%s", cutAll)
	}
	// a plugin member that is not an array is refused
	for _, cfg := range []string{`{"plugin": 3}`, "{"} {
		if _, err := oc.install([]byte(cfg), "", "opencode", shim); err == nil {
			t.Errorf("install took %s", cfg)
		}
		if _, err := oc.remove([]byte(cfg), "", "opencode", shim); err == nil {
			t.Errorf("remove took %s", cfg)
		}
	}
}

func TestTOMLHookEditInstallsReplacesAndRemovesItsBlock(t *testing.T) {
	codex := hookFor("codex").(tomlHooks)
	bin := "/bin/huddle"
	// a file that is not there yet: the block alone, markers around it
	fresh, err := codex.install(nil, bin, "codex", "")
	if err != nil {
		t.Fatalf("install on nothing: %v", err)
	}
	assertHooks(t, "codex", fresh, hookChecks("codex"), "/", "")
	// appended after the file's own bytes, one newline ahead
	cfg := []byte("model = \"gpt-5\"\n")
	appended, err := codex.install(cfg, bin, "codex", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.HasPrefix(string(appended), "model = \"gpt-5\"\n\n"+tomlHookBegin+"\n") {
		t.Errorf("the block did not land after the file's own bytes:\n%s", appended)
	}
	// a block that drifted from ours is replaced in place
	drifting := strings.Replace(string(appended), "timeout = 3", "timeout = 600", 1)
	replaced, err := codex.install([]byte(drifting), bin, "codex", "")
	if err != nil {
		t.Fatalf("install over the drift: %v", err)
	}
	if !bytes.Equal(replaced, appended) {
		t.Errorf("install did not make the block ours again:\nwant %q\ngot  %q", appended, replaced)
	}
	// a second install of the same bytes changes nothing
	same, err := codex.install(appended, bin, "codex", "")
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !bytes.Equal(same, appended) {
		t.Errorf("the second install rewrote the block")
	}
	// a config that does not parse is refused, a file without the block refuses the remove,
	// and a drifted block refuses too
	if _, err := codex.install([]byte("model = [1\n"), bin, "codex", ""); err == nil {
		t.Error("install took a malformed config")
	}
	if _, err := codex.remove(cfg, bin, "codex", ""); !errors.Is(err, ErrEntryChanged) {
		t.Errorf("remove without a block = %v, want ErrEntryChanged", err)
	}
	if _, err := codex.remove([]byte(drifting), bin, "codex", ""); !errors.Is(err, ErrEntryChanged) {
		t.Errorf("remove of a drifted block = %v, want ErrEntryChanged", err)
	}
	cut, err := codex.remove(appended, bin, "codex", "")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if string(cut) != string(cfg) {
		t.Errorf("remove did not give the bytes back:\nwant %q\ngot  %q", cfg, cut)
	}
	if codex.contains(cfg, bin, "codex", "") {
		t.Error("contains saw a block in a config that holds none")
	}
	if !codex.contains(appended, bin, "codex", "") {
		t.Error("contains missed the block")
	}
	// a begin marker with no end runs the block to the end of the file
	noEnd := "model = \"gpt-5\"\n" + tomlHookBegin + "\n[[hooks.SessionStart]]\n"
	block, found := findMarked([]byte(noEnd), tomlHookBegin, tomlHookEnd)
	if !found {
		t.Fatal("findMarked saw no block")
	}
	if block.cutEnd != len(noEnd) {
		t.Errorf("cutEnd = %d, want the end of the file (%d)", block.cutEnd, len(noEnd))
	}
}

func TestShellQuoteEscapesWhatADoubleQuotedStringWouldEat(t *testing.T) {
	got := shellQuote("a\"b\\c$d`e")
	want := "\"a\\\"b\\\\c\\$d\\`e\""
	if got != want {
		t.Errorf("shellQuote = %q, want %q", got, want)
	}
}

func TestJSONStringQuotesEscapes(t *testing.T) {
	got := jsonString("a\"b\nc")
	if got != "\"a\\\"b\\nc\"" {
		t.Errorf("jsonString = %q, want the escaped form", got)
	}
}

func TestJSONArrayWalkRefusesBytesThatAreNotAnArray(t *testing.T) {
	data := []byte(`{"a": 1}`)
	if _, _, err := jsonArrayItems(data, 0); err == nil {
		t.Error("jsonArrayItems took an object")
	}
	if _, _, err := jsonArrayItems([]byte("[1,"), 0); err == nil {
		t.Error("jsonArrayItems took a truncated array")
	}
	if _, _, err := jsonArrayItems(data, 1); err == nil {
		t.Error("jsonArrayItems took a number")
	}
	// an honest array answers its items' spans and its closing offset
	array := []byte(`[{"x": 1} , 2]`)
	items, closeOff, err := jsonArrayItems(array, 0)
	if err != nil {
		t.Fatalf("jsonArrayItems: %v", err)
	}
	if len(items) != 2 || closeOff != len(array)-1 {
		t.Errorf("items = %v, closeOff = %d, want 2 items and the last offset", items, closeOff)
	}
	// an insert into something that is not an array is refused
	if _, err := jsonInsertArrayItem(data, 1, `"x"`); err == nil {
		t.Error("jsonInsertArrayItem took an object")
	}
}

func TestListSaysHooksWithoutTheServer(t *testing.T) {
	env, home := testEnv(t, nil)
	gemini, err := ByName("gemini")
	if err != nil {
		t.Fatal(err)
	}
	path := gemini.ConfigPath(env)
	// a config a hand wrote with only the hook entries in it: the state is hooks, not installed
	geminiEdit := hookFor("gemini").(jsonHooks)
	installed, err := geminiEdit.install([]byte("{\n  \"theme\": \"dark\"\n}\n"), filepath.Join(home, "bin", "huddle"), "gemini", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	writeConfig(t, path, string(installed))
	for _, one := range List(env) {
		if one.Name != "gemini" {
			continue
		}
		if one.Installed {
			t.Error("installed = true, want false: no MCP entry is in the file")
		}
		if !one.Hooks || one.State() != "hooks" {
			t.Errorf("state = %q, want hooks", one.State())
		}
	}
}

// A user's own entry ahead of ours in the same array: install appends after it, remove cuts
// only ours, and the file's bytes come back.
func TestJSONHookEditLeavesAUserEntryAheadOfOurs(t *testing.T) {
	crush := hookFor("crush").(jsonHooks)
	bin := "/bin/huddle"
	before := []byte(`{"hooks": {"PreToolUse": [{"name": "mine", "command": "./own.sh"}]}}`)
	installed, err := crush.install(before, bin, "crush", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(string(installed), "own.sh") {
		t.Fatalf("install lost the user's entry:\n%s", installed)
	}
	out, err := crush.remove(installed, bin, "crush", "")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if string(out) != string(before) {
		t.Errorf("remove did not give the bytes back:\nwant %q\ngot  %q", before, out)
	}
}
