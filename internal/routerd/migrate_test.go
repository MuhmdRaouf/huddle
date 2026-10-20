package routerd

// The 0.0.1 migration's unit rows: a state root laid out exactly like a real 0.0.1 setup's
// (~/.agents/zai) is moved by one Migrate001 call, job state stays, and a second call is a no-op.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// gonePid is a pid no live process can hold: past every macOS and Linux pid limit, so the
// migration reads a 0.0.1 record whose router is long dead.
const gonePid = 999999999

// migrateEnv is the throwaway world one migration runs in: every home and state dir under one
// temp root, the daemon's port a testkit one.
func migrateEnv(t *testing.T) (domain.EnvLookup, string) {
	t.Helper()
	root := t.TempDir()
	env := domain.EnvLookup{
		"HOME":               root,
		"TMPDIR":             root,
		"XDG_CONFIG_HOME":    filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME":      filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":     filepath.Join(root, "xdg-cache"),
		"XDG_STATE_HOME":     filepath.Join(root, "xdg-state"),
		"HUDDLE_KEYS":        "files",
		"HUDDLE_ROUTER_HOME": filepath.Join(root, "router-home"),
		"HUDDLE_ROUTER_PORT": strconv.Itoa(ports.Free(t)),
		"ZAI_STATE_DIR":      filepath.Join(root, "zai-state"),
	}
	return env, root
}

// layout001 lays one provider's state root out the way 0.0.1 left it: the setup mark, a pid
// record naming the given pid, the binary copy, the logs, the ledger naming the old router, two
// watch guards and one job file.
func layout001(t *testing.T, env domain.EnvLookup, pid int64) string {
	t.Helper()
	root := env["ZAI_STATE_DIR"]
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	write("setup-done", "")
	write("router.pid", `{"pid":`+strconv.FormatInt(pid, 10)+`,"port":18787,`+
		`"startedAt":"2026-01-01T00:00:00Z","node":"`+root+`/router/plugin-core",`+
		`"version":"abc123def456","frontVersion":3,"mode":"router","token":"token001"}`+"\n")
	write("router/plugin-core", "the 0.0.1 binary copy")
	write("router/router.log", "the old router's log\n")
	write("router-crash.log", "a crash line\n")
	write("ledger.json", `{
  "version": 1,
  "plugin": "zai",
  "routerUrl": "http://127.0.0.1:18787",
  "settingsPath": "`+env["XDG_CONFIG_HOME"]+`/claude/settings.json",
  "settings": {
    "optionsAdded": ["glm-5.3"],
    "availableAppended": [],
    "effortsAdded": {}
  },
  "keystore": {"created": false},
  "routerFiles": ["`+root+`/router"],
  "marker": "`+env["XDG_CONFIG_HOME"]+`/claude/plugins/zai-plugin-cc-data/installed",
  "agents": {"glm": "sonnet"},
  "agentsDir": "`+env["XDG_CONFIG_HOME"]+`/claude/plugins/zai-plugin-cc",
  "stateRoot": "`+root+`"
}`+"\n")
	write("watch."+strconv.FormatInt(gonePid-1, 10), strconv.FormatInt(gonePid-1, 10)+"\n")
	write("watch."+strconv.FormatInt(gonePid-2, 10), strconv.FormatInt(gonePid-2, 10)+"\n")
	write("jobs/j-1.json", `{"id":"j-1","brief":"keep me"}`)
	return root
}

// snapshot walks a directory and answers every file's content hash by relative path, so a test
// can prove a run changed nothing.
func snapshot(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(body)
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			out[rel] = hex.EncodeToString(sum[:])
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestMigrate001MovesA001Setup(t *testing.T) {
	env, root := migrateEnv(t)
	state := layout001(t, env, gonePid)
	jobBefore, err := os.ReadFile(filepath.Join(state, "jobs", "j-1.json"))
	if err != nil {
		t.Fatal(err)
	}
	moved := Migrate001(context.Background(), env, func(string) {})

	if len(moved) != 1 || !strings.Contains(moved[0], "zai") {
		t.Errorf("Migrate001 = %v, want one line naming zai", moved)
	}
	enabled, err := router.ReadEnabled(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0] != "zai" {
		t.Errorf("providers.json = %v, want [zai]", enabled)
	}
	ledger, ok := router.ReadLedger(context.Background(), filepath.Join(state, "ledger.json"))
	if !ok {
		t.Fatal("the ledger no longer reads")
	}
	if want := domain.HuddleRouterURL(env); ledger.RouterURL != want {
		t.Errorf("ledger routerUrl = %q, want %q", ledger.RouterURL, want)
	}
	raw, err := os.ReadFile(filepath.Join(state, "ledger.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "{\n  \"version\": 1,\n  \"plugin\": \"zai\",") {
		t.Errorf("the rewritten ledger lost its key order: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(state, "router", "plugin-core")); !os.IsNotExist(err) {
		t.Errorf("router/plugin-core still there: %v", err)
	}
	for _, name := range []string{"watch." + strconv.FormatInt(gonePid-1, 10), "watch." + strconv.FormatInt(gonePid-2, 10)} {
		if _, err := os.Stat(filepath.Join(state, name)); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", name, err)
		}
	}
	jobAfter, err := os.ReadFile(filepath.Join(state, "jobs", "j-1.json"))
	if err != nil || string(jobAfter) != string(jobBefore) {
		t.Errorf("jobs/j-1.json changed: %q (%v)", jobAfter, err)
	}
	_ = root
}

func TestMigrate001SecondRunIsANoop(t *testing.T) {
	env, _ := migrateEnv(t)
	state := layout001(t, env, gonePid)
	Migrate001(context.Background(), env, func(string) {})
	before := snapshot(t, state, env["HUDDLE_ROUTER_HOME"])
	var lines []string
	moved := Migrate001(context.Background(), env, func(line string) { lines = append(lines, line) })
	if len(moved) != 0 {
		t.Errorf("the second run moved %v, want nothing", moved)
	}
	if len(lines) != 0 {
		t.Errorf("the second run logged %v, want silence", lines)
	}
	after := snapshot(t, state, env["HUDDLE_ROUTER_HOME"])
	if len(before) != len(after) {
		t.Errorf("the second run changed the file count: %d entries before, %d after", len(before), len(after))
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("the second run changed %s", path)
		}
	}
}

func TestMigrate001SkipsProvidersNeverSetUp(t *testing.T) {
	env, _ := migrateEnv(t)
	var lines []string
	moved := Migrate001(context.Background(), env, func(line string) { lines = append(lines, line) })
	if len(moved) != 0 || len(lines) != 0 {
		t.Errorf("a machine with no 0.0.1 setup moved %v and logged %v, want silence", moved, lines)
	}
	enabled, err := router.ReadEnabled(env)
	if err != nil || len(enabled) != 0 {
		t.Errorf("providers.json = %v (%v), want none", enabled, err)
	}
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if !ok {
			t.Fatalf("no provider named %s", name)
		}
		if _, err := os.Stat(adapters.StateRoot(context.Background(), provider, env)); !os.IsNotExist(err) {
			t.Errorf("%s's state root sprang up: %v", name, err)
		}
	}
}
