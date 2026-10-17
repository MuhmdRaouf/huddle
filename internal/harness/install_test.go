package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// field answers one field of a JSON object value.
func field(value *jsfmt.Value, key string) (*jsfmt.Value, bool) {
	if value == nil || value.Kind() != jsfmt.KindObject {
		return nil, false
	}
	for i, one := range value.Keys() {
		if one == key {
			return value.Fields()[i], true
		}
	}
	return nil, false
}

// textField answers one string field, empty when the field is not a string.
func textField(value *jsfmt.Value, key string) string {
	one, ok := field(value, key)
	if !ok || one.Kind() != jsfmt.KindString {
		return ""
	}
	return one.Text()
}

// boolText answers one boolean field.
func boolText(value *jsfmt.Value, key string) bool {
	one, ok := field(value, key)
	return ok && one.Kind() == jsfmt.KindBool && one.EncodeJSON() == "true"
}

// testEnv is one test machine: HOME, XDG_CONFIG_HOME and CODEX_HOME point at temp dirs, and the
// PATH search answers only what the test allowed. Before any test touches a file it fails when
// an adapter resolves a config outside those temp dirs, so no real harness directory is ever
// read or written.
func testEnv(t *testing.T, which map[string]bool) (Env, string) {
	t.Helper()
	home := t.TempDir()
	vars := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": t.TempDir(),
		"CODEX_HOME":      "",
	}
	env := Env{
		Lookup: func(key string) string { return vars[key] },
		Which:  func(bin string) bool { return which[bin] },
	}
	for _, name := range Names() {
		adapter, err := ByName(name)
		if err != nil {
			t.Fatal(err)
		}
		path := adapter.ConfigPath(env)
		if !strings.HasPrefix(path, home) && !strings.HasPrefix(path, vars["XDG_CONFIG_HOME"]) {
			t.Fatalf("%s resolves %s outside the temp home %s", name, path, home)
		}
	}
	return env, home
}

// testEntry answers the entry one harness gets: the fake binary at <home>/bin/huddle.
func testEntry(home, name string) Entry {
	return NewEntry(name, filepath.Join(home, "bin", "huddle"))
}

// writeConfig writes one config file the test owns.
func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readConfig reads one config file, failing when it is not there.
func readConfig(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

// fixture answers the config text a test starts an adapter from: the file body, and whether it
// already holds a huddle entry.
type fixture struct {
	label   string
	body    string
	exists  bool
	hasHud  bool
	created bool // install answers created
}

// jsonFixtures are the three starting states one JSON harness is tested from; parent is the
// adapter's parent key and other a server entry that is not huddle's.
func jsonFixtures(parent, other string) []fixture {
	return []fixture{
		{label: "absent", exists: false, created: true},
		{label: "without huddle", exists: true, body: fmt.Sprintf("{\n  %q: \"dark\"\n}\n", "theme")},
		{label: "with another server", exists: true, hasHud: false,
			body: fmt.Sprintf("{\n  \"theme\": \"dark\",\n  %q: {\n    %q: {\"command\": \"other-cmd\"}\n  }\n}\n", parent, other)},
	}
}

// codexFixtures are the three starting states the Codex config is tested from.
func codexFixtures() []fixture {
	return []fixture{
		{label: "absent", exists: false, created: true},
		{label: "without huddle", exists: true, body: "model = \"gpt-5\"\n"},
		{label: "with another server", exists: true,
			body: "model = \"gpt-5\"\n\n[mcp_servers.other]\ncommand = \"other-cmd\"\n"},
	}
}

// fixturesFor answers the starting states one adapter is tested from.
func fixturesFor(t *testing.T, a Adapter) []fixture {
	t.Helper()
	if _, ok := a.(tomlAdapter); ok {
		return codexFixtures()
	}
	parent := "mcpServers"
	if a.Name() == "opencode" || a.Name() == "crush" {
		parent = "mcp"
	}
	return jsonFixtures(parent, "other")
}

// assertEntry parses cfg and wants exactly one huddle entry whose command, args and env are the
// ones install wrote, and that any other server is still there.
func assertEntry(t *testing.T, a Adapter, cfg []byte, entry Entry, otherThere bool) {
	t.Helper()
	if _, ok := a.(tomlAdapter); ok {
		assertCodexEntry(t, cfg, entry, otherThere)
		return
	}
	value, err := jsfmt.DecodeValue(cfg)
	if err != nil {
		t.Fatalf("the config does not parse: %v\n%s", err, cfg)
	}
	parent := "mcpServers"
	if a.Name() == "opencode" || a.Name() == "crush" {
		parent = "mcp"
	}
	servers, ok := field(value, parent)
	if !ok || servers.Kind() != jsfmt.KindObject {
		t.Fatalf("the config holds no %q object: %s", parent, cfg)
	}
	count, others := 0, 0
	huddle := ""
	for i, key := range servers.Keys() {
		if key == entry.Name {
			count++
			huddle = servers.Fields()[i].EncodeJSON()
			continue
		}
		others++
	}
	if count != 1 {
		t.Fatalf("the config holds %d %q entries, want 1: %s", count, entry.Name, cfg)
	}
	if otherThere != (others > 0) {
		t.Fatalf("other servers present = %v, want %v: %s", others > 0, otherThere, cfg)
	}
	got, err := jsfmt.DecodeValue([]byte(huddle))
	if err != nil {
		t.Fatalf("the huddle entry does not parse: %v", err)
	}
	// the shapes the card pins: opencode keeps the whole argv in command and the env in
	// environment; crush marks stdio; the rest read command, args and env
	envKey, argvCommand := "env", false
	switch a.Name() {
	case "opencode":
		envKey, argvCommand = "environment", true
		if typ := textField(got, "type"); typ != "local" {
			t.Errorf("type = %q, want local", typ)
		}
	case "crush":
		if typ := textField(got, "type"); typ != "stdio" {
			t.Errorf("type = %q, want stdio", typ)
		}
	}
	if argvCommand {
		command, ok := field(got, "command")
		if !ok || command.Kind() != jsfmt.KindArray {
			t.Fatalf("opencode command is not an argv array: %s", huddle)
		}
		var argv []string
		for _, item := range command.Items() {
			argv = append(argv, item.Text())
		}
		want := append([]string{entry.Command}, entry.Args...)
		if strings.Join(argv, " ") != strings.Join(want, " ") {
			t.Errorf("command = %v, want %v", argv, want)
		}
	} else if command := textField(got, "command"); command != entry.Command {
		t.Errorf("command = %q, want %q", command, entry.Command)
		argsField, _ := field(got, "args")
		args := []string{}
		if argsField != nil && argsField.Kind() == jsfmt.KindArray {
			for _, item := range argsField.Items() {
				args = append(args, item.Text())
			}
		}
		if strings.Join(args, " ") != strings.Join(entry.Args, " ") {
			t.Errorf("args = %v, want %v", args, entry.Args)
		}
	}
	envField, ok := field(got, envKey)
	if !ok || envField.Kind() != jsfmt.KindObject {
		t.Fatalf("the entry holds no %s object: %s", envKey, huddle)
	}
	for i, key := range envField.Keys() {
		if envField.Fields()[i].Text() != entry.Env[key] {
			t.Errorf("env[%s] = %q, want %q", key, envField.Fields()[i].Text(), entry.Env[key])
		}
	}
	if len(envField.Keys()) != len(entry.Env) {
		t.Errorf("env holds %d keys, want %d", len(envField.Keys()), len(entry.Env))
	}
}

// assertCodexEntry parses the TOML config and wants the huddle table with the entry's parts.
func assertCodexEntry(t *testing.T, cfg []byte, entry Entry, otherThere bool) {
	t.Helper()
	var doc struct {
		MCPServers map[string]struct {
			Command string            `toml:"command"`
			Args    []string          `toml:"args"`
			Env     map[string]string `toml:"env"`
		} `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(cfg), &doc); err != nil {
		t.Fatalf("the config does not parse: %v\n%s", err, cfg)
	}
	servers := doc.MCPServers
	mine, ok := servers[entry.Name]
	if !ok {
		t.Fatalf("the config holds no %q table: %s", entry.Name, cfg)
	}
	if mine.Command != entry.Command {
		t.Errorf("command = %q, want %q", mine.Command, entry.Command)
	}
	if strings.Join(mine.Args, " ") != strings.Join(entry.Args, " ") {
		t.Errorf("args = %v, want %v", mine.Args, entry.Args)
	}
	if len(mine.Env) != len(entry.Env) {
		t.Fatalf("env = %v, want %v", mine.Env, entry.Env)
	}
	for key, want := range entry.Env {
		if mine.Env[key] != want {
			t.Errorf("env[%s] = %q, want %q", key, mine.Env[key], want)
		}
	}
	if _, there := servers["other"]; there != otherThere {
		t.Errorf("other server present = %v, want %v", there, otherThere)
	}
}

func TestInstallAddsTheMCPServer(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			entry := testEntry(home, name)
			path := a.ConfigPath(env)
			for _, fx := range fixturesFor(t, a) {
				t.Run(fx.label, func(t *testing.T) {
					if fx.exists {
						writeConfig(t, path, fx.body)
					}
					got, err := Install(context.Background(), home, env, name, entry)
					if err != nil {
						t.Fatalf("install: %v", err)
					}
					if fx.created != got.Created {
						t.Errorf("created = %v, want %v", got.Created, fx.created)
					}
					assertEntry(t, a, readConfig(t, path), entry, fx.label == "with another server")
				})
			}
		})
	}
}

func TestRemoveRestoresTheBytes(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			entry := testEntry(home, name)
			path := a.ConfigPath(env)
			for _, fx := range fixturesFor(t, a) {
				t.Run(fx.label, func(t *testing.T) {
					if fx.exists {
						writeConfig(t, path, fx.body)
					}
					if _, err := Install(context.Background(), home, env, name, entry); err != nil {
						t.Fatalf("install: %v", err)
					}
					got, err := Remove(context.Background(), home, env, name, entry)
					if err != nil {
						t.Fatalf("remove: %v", err)
					}
					if !got.Removed {
						t.Errorf("removed = false, want true (%+v)", got)
					}
					if fx.exists {
						if raw := readConfig(t, path); !bytes.Equal(raw, []byte(fx.body)) {
							t.Errorf("the config did not come back byte for byte:\nwant %q\ngot  %q", fx.body, raw)
						}
						return
					}
					if !got.FileGone {
						t.Errorf("file gone = false, want true (%+v)", got)
					}
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Errorf("the created file is still there: %v", err)
					}
				})
			}
		})
	}
}

func TestInstallTwiceIsOneEntry(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			entry := testEntry(home, name)
			path := a.ConfigPath(env)
			writeConfig(t, path, fixturesFor(t, a)[2].body)
			if _, err := Install(context.Background(), home, env, name, entry); err != nil {
				t.Fatalf("first install: %v", err)
			}
			once := readConfig(t, path)
			got, err := Install(context.Background(), home, env, name, entry)
			if err != nil {
				t.Fatalf("second install: %v", err)
			}
			if !got.Already {
				t.Errorf("already = false, want true (%+v)", got)
			}
			twice := readConfig(t, path)
			if !bytes.Equal(once, twice) {
				t.Errorf("the second install rewrote the file:\nfirst  %q\nsecond %q", once, twice)
			}
			assertEntry(t, a, twice, entry, true)
		})
	}
}

func TestRemoveLeavesAUserEditedEntryAlone(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			entry := testEntry(home, name)
			path := a.ConfigPath(env)
			writeConfig(t, path, fixturesFor(t, a)[1].body)
			if _, err := Install(context.Background(), home, env, name, entry); err != nil {
				t.Fatalf("install: %v", err)
			}
			edited := userEditedConfig(t, a, readConfig(t, path), filepath.Join(home, "elsewhere", "huddle"))
			writeConfig(t, path, edited)
			got, err := Remove(context.Background(), home, env, name, entry)
			if err != nil {
				t.Fatalf("remove: %v", err)
			}
			if !got.Kept {
				t.Errorf("kept = false, want true (%+v)", got)
			}
			if raw := readConfig(t, path); !bytes.Equal(raw, []byte(edited)) {
				t.Errorf("remove touched the edited entry:\nwant %q\ngot  %q", edited, raw)
			}
		})
	}
}

// userEditedConfig answers the config with its huddle entry's binary moved, the way a user's
// hand would change it after an install.
func userEditedConfig(t *testing.T, a Adapter, cfg []byte, bin string) string {
	t.Helper()
	if _, ok := a.(tomlAdapter); ok {
		return strings.Replace(string(cfg), "bin/huddle", bin, 1)
	}
	parent := "mcpServers"
	if a.Name() == "opencode" || a.Name() == "crush" {
		parent = "mcp"
	}
	value, err := jsfmt.DecodeValue(cfg)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	servers, _ := field(value, parent)
	for i, key := range servers.Keys() {
		if key != "huddle" {
			continue
		}
		text := servers.Fields()[i].EncodeJSON()
		text = strings.Replace(text, "bin/huddle", bin, 1)
		servers.Fields()[i], _ = jsfmt.DecodeValue([]byte(text))
	}
	return value.EncodeJSON()
}

func TestMalformedConfigIsRefused(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			entry := testEntry(home, name)
			path := a.ConfigPath(env)
			broken := "{\"theme\": }"
			if _, ok := a.(tomlAdapter); ok {
				broken = "model = [1\ncommand ="
			}
			writeConfig(t, path, broken)
			before := readConfig(t, path)
			if _, err := Install(context.Background(), home, env, name, entry); err == nil {
				t.Fatal("install took a malformed config")
			} else if !strings.Contains(err.Error(), path) {
				t.Errorf("the error does not name the file: %v", err)
			}
			if _, err := Remove(context.Background(), home, env, name, entry); err == nil {
				t.Fatal("remove took a malformed config")
			}
			if raw := readConfig(t, path); !bytes.Equal(raw, before) {
				t.Errorf("a refused edit changed the file:\nwant %q\ngot  %q", before, raw)
			}
		})
	}
}

func TestListDetectsInstalledHarnesses(t *testing.T) {
	env, home := testEnv(t, map[string]bool{"codex": true})
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o700); err != nil {
		t.Fatal(err)
	}
	status := List(env)
	byName := map[string]Status{}
	for _, one := range status {
		byName[one.Name] = one
	}
	if len(byName) != len(Names()) {
		t.Fatalf("list answered %d harnesses, want %d", len(byName), len(Names()))
	}
	if got := byName["codex"].State(); got != "available" {
		t.Errorf("codex = %q, want available (its binary is on PATH)", got)
	}
	if got := byName["gemini"].State(); got != "available" {
		t.Errorf("gemini = %q, want available (its config dir exists)", got)
	}
	for _, name := range []string{"qwen", "opencode", "crush"} {
		if got := byName[name].State(); got != "absent" {
			t.Errorf("%s = %q, want absent", name, got)
		}
	}
	entry := testEntry(home, "opencode")
	if _, err := Install(context.Background(), home, env, "opencode", entry); err != nil {
		t.Fatal(err)
	}
	status = List(env)
	for _, one := range status {
		if one.Name == "opencode" && one.State() != "installed" {
			t.Errorf("opencode = %q, want installed", one.State())
		}
	}
}

func TestTheLedgerRecordsEveryChange(t *testing.T) {
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			env, home := testEnv(t, nil)
			a, err := ByName(name)
			if err != nil {
				t.Fatal(err)
			}
			path := a.ConfigPath(env)
			writeConfig(t, path, fixturesFor(t, a)[2].body)
			before := readConfig(t, path)
			entry := testEntry(home, name)
			if _, err := Install(context.Background(), home, env, name, entry); err != nil {
				t.Fatal(err)
			}
			ledgerPath := filepath.Join(home, "harness-ledger.json")
			info, err := os.Stat(ledgerPath)
			if err != nil {
				t.Fatalf("install wrote no ledger: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("the ledger's mode is %v, want 0600", info.Mode().Perm())
			}
			sum := sha256.Sum256(before)
			want := hex.EncodeToString(sum[:])
			assertLedger(t, ledgerPath, ledgerCheck{name: name, path: path, created: false, sha: want})
			if _, err := Remove(context.Background(), home, env, name, entry); err != nil {
				t.Fatal(err)
			}
			assertLedger(t, ledgerPath, ledgerCheck{name: name, gone: true})
			// the created case: install on a file that was not there records created, and the
			// sha of what was there before is the sha of nothing
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if _, err := Install(context.Background(), home, env, name, entry); err != nil {
				t.Fatal(err)
			}
			empty := sha256.Sum256(nil)
			assertLedger(t, ledgerPath, ledgerCheck{name: name, path: path, created: true, sha: hex.EncodeToString(empty[:])})
		})
	}
}

// ledgerCheck is what assertLedger wants one harness's ledger line to say.
type ledgerCheck struct {
	name    string
	path    string
	created bool
	sha     string
	gone    bool
}

// assertLedger parses the ledger and wants the named harness's entry as described.
func assertLedger(t *testing.T, path string, want ledgerCheck) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		t.Fatalf("the ledger does not parse: %v", err)
	}
	entries, ok := field(value, "entries")
	if !ok || entries.Kind() != jsfmt.KindArray {
		t.Fatalf("the ledger holds no entries array: %s", raw)
	}
	found := false
	for _, item := range entries.Items() {
		if textField(item, "harness") != want.name {
			continue
		}
		found = true
		if want.gone {
			t.Errorf("the ledger still records %s: %s", want.name, raw)
			return
		}
		if textField(item, "path") != want.path {
			t.Errorf("path = %q, want %q", textField(item, "path"), want.path)
		}
		if got := boolText(item, "created"); got != want.created {
			t.Errorf("created = %v, want %v", got, want.created)
		}
		if got := textField(item, "before_sha256"); got != want.sha {
			t.Errorf("before_sha256 = %q, want %q", got, want.sha)
		}
	}
	if !found && !want.gone {
		t.Errorf("the ledger records no %s entry: %s", want.name, raw)
	}
}
