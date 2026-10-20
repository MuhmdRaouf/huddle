// The path-resolution, ensureStateDirs and fileExists cases, plus the port and autostart
// files' own cases (their behaviour lives in this package too).
package paths_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/adapters"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// testEnv is a whole temp world: HOME, RADAR_HOME, CLAUDE_CONFIG_DIR and ZAI_STATE_DIR all
// point into one throwaway directory, so no test can read or write anything real — not the
// user's transcripts, not the zai jobs dir, not a live server.json. Built from scratch (never
// copied from the process environment) so a stray variable set in the surrounding shell cannot
// leak a real path in.
type testEnv struct {
	env    domain.EnvLookup
	home   string
	state  string
	config string
	zai    string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{
		env: domain.EnvLookup{
			"HOME":              home,
			"RADAR_HOME":        filepath.Join(home, "state"),
			"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
			"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
		},
		home:   home,
		state:  filepath.Join(home, "state"),
		config: filepath.Join(home, "claude"),
		zai:    filepath.Join(home, "zai"),
	}
}

// cloneEnv copies a map-built env so one case's overrides never leak into the next.
func cloneEnv(env domain.EnvLookup) domain.EnvLookup {
	copied := make(domain.EnvLookup, len(env)+2)
	for key, value := range env {
		copied[key] = value
	}
	return copied
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

func pathExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestPathResolution(t *testing.T) {
	t.Run("puts everything under RADAR_HOME when it is set", func(t *testing.T) {
		e := makeEnv(t)
		if got := paths.StateDir(t.Context(), e.env); got != e.state {
			t.Fatalf("StateDir = %q, want %q", got, e.state)
		}
		if got := paths.SpoolDir(t.Context(), e.env); got != filepath.Join(e.state, "spool") {
			t.Fatalf("SpoolDir = %q, want %q", got, filepath.Join(e.state, "spool"))
		}
		if got := paths.ServerInfoPath(t.Context(), e.env); got != filepath.Join(e.state, "server.json") {
			t.Fatalf("ServerInfoPath = %q, want %q", got, filepath.Join(e.state, "server.json"))
		}
		if got := paths.HookErrorLog(t.Context(), e.env); got != filepath.Join(e.state, "hook-errors.log") {
			t.Fatalf("HookErrorLog = %q, want %q", got, filepath.Join(e.state, "hook-errors.log"))
		}
	})

	t.Run("puts the history database in the state dir, with process.env as the default", func(t *testing.T) {
		e := makeEnv(t)
		if got := paths.HistoryPath(t.Context(), e.env); got != filepath.Join(e.state, "history.db") {
			t.Fatalf("HistoryPath = %q, want %q", got, filepath.Join(e.state, "history.db"))
		}
		process := domain.EnvLookup{}
		for _, entry := range os.Environ() {
			key, value, _ := strings.Cut(entry, "=")
			process[key] = value
		}
		// A temp home, so the call cannot touch the caller's real state dirs.
		process["HOME"] = e.home
		delete(process, "RADAR_HOME")
		delete(process, "XDG_STATE_HOME")
		if got := paths.HistoryPath(t.Context(), process); !strings.HasSuffix(got, "history.db") {
			t.Fatalf("HistoryPath(process env) = %q, want a path ending in history.db", got)
		}
	})

	t.Run("falls back to ~/.agents paths under HOME", func(t *testing.T) {
		e := makeEnv(t)
		bare := domain.EnvLookup{"HOME": e.home}
		if got := paths.StateDir(t.Context(), bare); got != filepath.Join(e.home, ".agents", "radar") {
			t.Fatalf("StateDir = %q, want %q", got, filepath.Join(e.home, ".agents", "radar"))
		}
		if got := paths.ClaudeConfigDir(bare); got != filepath.Join(e.home, ".claude") {
			t.Fatalf("ClaudeConfigDir = %q, want %q", got, filepath.Join(e.home, ".claude"))
		}
		if got := paths.ZaiStateDir(t.Context(), bare); got != filepath.Join(e.home, ".agents", "zai") {
			t.Fatalf("ZaiStateDir = %q, want %q", got, filepath.Join(e.home, ".agents", "zai"))
		}
	})

	t.Run("an env without HOME reads the OS's home for the config dir", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("the OS has no home to read: %v", err)
		}
		if got := paths.ClaudeConfigDir(domain.EnvLookup{}); got != filepath.Join(home, ".claude") {
			t.Fatalf("ClaudeConfigDir(no HOME) = %q, want %q", got, filepath.Join(home, ".claude"))
		}
	})

	t.Run("honours CLAUDE_CONFIG_DIR and ZAI_STATE_DIR overrides", func(t *testing.T) {
		e := makeEnv(t)
		if got := paths.ClaudeConfigDir(e.env); got != e.config {
			t.Fatalf("ClaudeConfigDir = %q, want %q", got, e.config)
		}
		if got := paths.ZaiStateDir(t.Context(), e.env); got != e.zai {
			t.Fatalf("ZaiStateDir = %q, want %q", got, e.zai)
		}
	})

	t.Run("honours CODEX_HOME, OPENCODE_DATA and XDG_DATA_HOME overrides", func(t *testing.T) {
		e := makeEnv(t)
		if got := paths.CodexHome(e.env); got != filepath.Join(e.home, ".codex") {
			t.Fatalf("CodexHome = %q, want the default under HOME", got)
		}
		if got := paths.OpencodeDBPath(e.env); got != filepath.Join(e.home, ".local", "share", "opencode", "opencode.db") {
			t.Fatalf("OpencodeDBPath = %q, want the default under HOME", got)
		}
		codex := filepath.Join(e.home, "codex")
		env := cloneEnv(e.env)
		env["CODEX_HOME"] = codex
		if got := paths.CodexHome(env); got != codex {
			t.Fatalf("CodexHome = %q, want the override %q", got, codex)
		}
		db := filepath.Join(e.home, "opencode-fixture.db")
		env["OPENCODE_DATA"] = db
		if got := paths.OpencodeDBPath(env); got != db {
			t.Fatalf("OpencodeDBPath = %q, want the override %q", got, db)
		}
		xdg := cloneEnv(e.env)
		xdg["XDG_DATA_HOME"] = filepath.Join(e.home, "xdg")
		if got := paths.OpencodeDBPath(xdg); got != filepath.Join(e.home, "xdg", "opencode", "opencode.db") {
			t.Fatalf("OpencodeDBPath = %q, want the database under XDG_DATA_HOME", got)
		}
		// an override that names nothing counts as unset
		empty := cloneEnv(e.env)
		empty["CODEX_HOME"], empty["OPENCODE_DATA"] = "", ""
		if got := paths.CodexHome(empty); got != filepath.Join(e.home, ".codex") {
			t.Fatalf("CodexHome = %q, want the default for an empty override", got)
		}
		if got := paths.OpencodeDBPath(empty); got != filepath.Join(e.home, ".local", "share", "opencode", "opencode.db") {
			t.Fatalf("OpencodeDBPath = %q, want the default for an empty override", got)
		}
	})
}

// TestProviderStateDirMatchesStateRoot pins providerStateDir to the state root plugin-core
// resolves: radar reads the same dir the provider's hooks, commands and router write, env
// override, plugin-data leftovers and all.
func TestProviderStateDirMatchesStateRoot(t *testing.T) {
	zai := domain.Provider{Name: "zai", EnvPrefix: "ZAI"}
	home := t.TempDir()
	for name, env := range map[string]domain.EnvLookup{
		"plain home": {"HOME": home},
		"the env override": {
			"HOME":          home,
			"ZAI_STATE_DIR": filepath.Join(home, "own"),
		},
		"an empty env override": {
			"HOME":          home,
			"ZAI_STATE_DIR": "",
		},
		"plugin data and an xdg home": {
			"HOME":               home,
			"CLAUDE_PLUGIN_DATA": filepath.Join(home, "zai-plugin-cc-m"),
			"XDG_STATE_HOME":     filepath.Join(home, "xdg"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			want := adapters.ResolveStateRoot(t.Context(), env, adapters.NewStateRootRule(zai), func(string) {})
			if got := paths.ProviderStateDir(t.Context(), env, "zai"); got != want {
				t.Fatalf("ProviderStateDir = %q, want the state root %q", got, want)
			}
		})
	}
}

func TestEnsureStateDirs(t *testing.T) {
	t.Run("creates the state dir and the spool dir, and is idempotent", func(t *testing.T) {
		e := makeEnv(t)
		if !paths.EnsureStateDirs(t.Context(), e.env) {
			t.Fatal("EnsureStateDirs = false, want true")
		}
		if !pathExists(t, e.state) {
			t.Fatalf("%s does not exist", e.state)
		}
		if !pathExists(t, filepath.Join(e.state, "spool")) {
			t.Fatalf("%s does not exist", filepath.Join(e.state, "spool"))
		}
		if !paths.EnsureStateDirs(t.Context(), e.env) {
			t.Fatal("EnsureStateDirs (again) = false, want true")
		}
	})

	t.Run("marks a state dir it created, or one holding only its own files, and nothing else", func(t *testing.T) {
		e := makeEnv(t)
		if !paths.EnsureStateDirs(t.Context(), e.env) {
			t.Fatal("EnsureStateDirs = false, want true")
		}
		if !pathExists(t, paths.StateMarkerPath(t.Context(), e.env)) {
			t.Fatal("a state dir it created carries no marker")
		}

		legacy := cloneEnv(e.env)
		legacy["RADAR_HOME"] = filepath.Join(e.home, "legacy") // unmarked: ours by contents
		writeText(t, filepath.Join(e.home, "legacy", "server.json"), "{}")
		writeText(t, filepath.Join(e.home, "legacy", "spool", "2026-01-01.jsonl"), "{}\n")
		if !paths.EnsureStateDirs(t.Context(), legacy) {
			t.Fatal("EnsureStateDirs (legacy) = false, want true")
		}
		if !pathExists(t, paths.StateMarkerPath(t.Context(), legacy)) {
			t.Fatal("a dir holding only radar's own files carries no marker")
		}

		foreign := cloneEnv(e.env)
		foreign["RADAR_HOME"] = e.home // $HOME itself: holds state/, legacy/ and more
		if !paths.EnsureStateDirs(t.Context(), foreign) {
			t.Fatal("EnsureStateDirs (foreign) = false, want true")
		}
		if pathExists(t, paths.StateMarkerPath(t.Context(), foreign)) {
			t.Fatal("a dir holding files radar never wrote carries a marker")
		}
		if !pathExists(t, e.state) {
			t.Fatalf("%s does not exist", e.state)
		}
	})

	t.Run("returns false instead of throwing when the location cannot be made", func(t *testing.T) {
		// A file where a directory is wanted makes mkdir fail: the hook must survive that.
		e := makeEnv(t)
		writeText(t, filepath.Join(e.home, "blocker"), "file")
		blocked := cloneEnv(e.env)
		blocked["RADAR_HOME"] = filepath.Join(e.home, "blocker")
		if paths.EnsureStateDirs(t.Context(), blocked) {
			t.Fatal("EnsureStateDirs = true over a file, want false")
		}
	})

	t.Run("returns false when the spool dir's place is taken by a file", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, filepath.Join(e.state, "spool"), "file")
		if paths.EnsureStateDirs(t.Context(), e.env) {
			t.Fatal("EnsureStateDirs = true with a file at spool, want false")
		}
		if !pathExists(t, paths.StateMarkerPath(t.Context(), e.env)) {
			t.Fatal("the state dir itself is fine and ours, so it carries the marker")
		}
	})
}

func TestFileExists(t *testing.T) {
	t.Run("mirrors the filesystem", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, filepath.Join(e.home, "a.txt"), "x")
		if !paths.FileExists(t.Context(), path) {
			t.Fatalf("FileExists(%s) = false, want true", path)
		}
		if paths.FileExists(t.Context(), filepath.Join(e.home, "missing.txt")) {
			t.Fatal("FileExists(missing.txt) = true, want false")
		}
	})
}

func TestSavedPort(t *testing.T) {
	t.Run("returns the port the dashboard last listened on", func(t *testing.T) {
		e := makeEnv(t)
		paths.SavePort(t.Context(), e.env, 9001)
		port, ok := paths.SavedPort(t.Context(), e.env)
		if !ok || port != 9001 {
			t.Fatalf("SavedPort = %d, %t; want 9001, true", port, ok)
		}
	})

	t.Run("returns none when there is no file yet", func(t *testing.T) {
		e := makeEnv(t)
		if port, ok := paths.SavedPort(t.Context(), e.env); ok {
			t.Fatalf("SavedPort = %d, true; want none", port)
		}
	})

	t.Run("returns none for content that is not a live port", func(t *testing.T) {
		e := makeEnv(t)
		for name, content := range map[string]string{
			"junk":        "not-a-port\n",
			"empty":       "\n",
			"a float":     "12.5\n",
			"zero":        "0\n",
			"negative":    "-1\n",
			"too large":   "70000\n",
			"underscores": "1_0\n",
			"bad hex":     "0xzz\n",
		} {
			t.Run(name, func(t *testing.T) {
				writeText(t, paths.PortPath(t.Context(), e.env), content)
				if port, ok := paths.SavedPort(t.Context(), e.env); ok {
					t.Fatalf("SavedPort(%q) = %d, true; want none", content, port)
				}
			})
		}
	})

	t.Run("reads a hex port the way a JavaScript number reads it", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, paths.PortPath(t.Context(), e.env), "0x1f90\n")
		port, ok := paths.SavedPort(t.Context(), e.env)
		if !ok || port != 8080 {
			t.Fatalf("SavedPort(0x1f90) = %d, %t; want 8080, true", port, ok)
		}
	})

	t.Run("trims the file the way JavaScript's trim does", func(t *testing.T) {
		// Number(text.trim()): the BOM goes and NEL stays, so it is not a number.
		e := makeEnv(t)
		writeText(t, paths.PortPath(t.Context(), e.env), "\ufeff9002\n")
		if port, ok := paths.SavedPort(t.Context(), e.env); !ok || port != 9002 {
			t.Fatalf(`SavedPort("\ufeff9002\n") = %d, %t; want 9002, true`, port, ok)
		}
		writeText(t, paths.PortPath(t.Context(), e.env), "\u00859003")
		if port, ok := paths.SavedPort(t.Context(), e.env); ok {
			t.Fatalf(`SavedPort("\u00859003") = %d, true; want none`, port)
		}
	})

	t.Run("keeps quiet when the state dir cannot be made", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, filepath.Join(e.home, "blocker"), "file")
		blocked := cloneEnv(e.env)
		blocked["RADAR_HOME"] = filepath.Join(e.home, "blocker")
		paths.SavePort(t.Context(), blocked, 9001)
		if pathExists(t, filepath.Join(e.home, "blocker", "port")) {
			t.Fatal("SavePort wrote a port file into a location that cannot be made")
		}
		if port, ok := paths.SavedPort(t.Context(), blocked); ok {
			t.Fatalf("SavedPort over a blocker = %d, true; want none", port)
		}
	})
}

func TestWantsAutostart(t *testing.T) {
	t.Run("RADAR_AUTOSTART decides when it is set", func(t *testing.T) {
		e := makeEnv(t)
		one := cloneEnv(e.env)
		one["RADAR_AUTOSTART"] = "1"
		if !paths.WantsAutostart(t.Context(), one) {
			t.Fatal("WantsAutostart(RADAR_AUTOSTART=1) = false, want true")
		}
		for _, value := range []string{"0", "yes"} {
			set := cloneEnv(e.env)
			set["RADAR_AUTOSTART"] = value
			if paths.WantsAutostart(t.Context(), set) {
				t.Fatalf("WantsAutostart(RADAR_AUTOSTART=%s) = true, want false", value)
			}
		}
	})

	t.Run("unset means yes: the hub line shows by default", func(t *testing.T) {
		e := makeEnv(t)
		if !paths.WantsAutostart(t.Context(), e.env) {
			t.Fatal("WantsAutostart with RADAR_AUTOSTART unset = false, want true")
		}
	})
}
