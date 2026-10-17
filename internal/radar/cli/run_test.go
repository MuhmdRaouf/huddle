// The cli-run cases, same names as the its: parsing, dispatch, and every command's lines and
// exit codes, the effects injected so nothing here needs processes or sockets — except the one
// foreground case, which runs the real dashboard in a temp world.
package cli_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/cli"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/server"
)

const (
	usageLine   = "usage: radar start|stop|status|url|open|watch [--port N] [--since 24h]"
	notRunning  = "radar: not running — /radar:start (or /radar:open) starts it"
	testVersion = "test"
)

const dayMs = int64(86_400_000)

// testEnv is a whole throwaway world: HOME, RADAR_HOME, CLAUDE_CONFIG_DIR and ZAI_STATE_DIR all
// point into one temp directory, so no test can read or write anything real.
type testEnv struct {
	env   domain.EnvLookup
	home  string
	state string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	env := domain.EnvLookup{
		"HOME":              home,
		"RADAR_HOME":        filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
		"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
	}
	return testEnv{env: env, home: home, state: filepath.Join(home, "state")}
}

// envWith copies the environment with one key replaced, the way a spawned child would meet it.
func envWith(env domain.EnvLookup, key, value string) domain.EnvLookup {
	copied := make(domain.EnvLookup, len(env)+1)
	for name, item := range env {
		copied[name] = item
	}
	copied[key] = value
	return copied
}

// overrides are the answers one case injects.
type overrides struct {
	// responses answers one fetched url; nil answers failure everywhere.
	responses func(url string) cli.FetchResult
	// alive says whether one pid names a live process; nil answers false everywhere.
	alive func(pid int64) bool
	// command answers a pid's command line as ps prints it; nil answers a radar server's.
	command func(pid int64) (string, bool)
	// onSpawn runs when the detached child is spawned.
	onSpawn func()
	// now is the clock; nil never moves off zero.
	now func() int64
	// stopAfter ends the watch loop once this many polls of /api/alerts have run; 0 never ends it.
	stopAfter int
}

// recording is every side effect the injected deps left behind.
type recording struct {
	deps      cli.Deps
	out       []string
	errLines  []string
	spawned   [][]string
	signalled []int64
	opened    []string
	slept     []int64
	env       domain.EnvLookup
	state     string
}

// makeDeps builds a Deps with every side effect recorded, a fresh temp state dir, and the
// answers the case injected. The recording is shared with the deps' closures, so it answers
// what they saw.
func makeDeps(t *testing.T, over overrides) *recording {
	t.Helper()
	world := makeEnv(t)
	rec := &recording{env: world.env, state: world.state}
	alertPolls := 0
	alive := func(int64) bool { return false }
	if over.alive != nil {
		alive = over.alive
	}
	now := func() int64 { return 0 }
	if over.now != nil {
		now = over.now
	}
	command := func(int64) (string, bool) {
		return "/Users/you/.agents/radar-bin/0.0.1/radar start --foreground --since 1d\n", true
	}
	if over.command != nil {
		command = over.command
	}
	rec.deps = cli.Deps{
		Env:     world.env,
		Version: testVersion,
		Out:     func(line string) { rec.out = append(rec.out, line) },
		Err:     func(line string) { rec.errLines = append(rec.errLines, line) },
		FetchJSON: func(url string) cli.FetchResult {
			if strings.HasSuffix(url, "/api/alerts") {
				alertPolls++
			}
			if over.responses != nil {
				return over.responses(url)
			}
			return cli.FetchResult{}
		},
		Sleep:      func(ms int64) { rec.slept = append(rec.slept, ms) },
		ShouldStop: func() bool { return over.stopAfter != 0 && alertPolls >= over.stopAfter },
		SpawnDetached: func(args []string) int64 {
			rec.spawned = append(rec.spawned, args)
			if over.onSpawn != nil {
				over.onSpawn()
			}
			return 999
		},
		Signal:      func(pid int64) bool { rec.signalled = append(rec.signalled, pid); return true },
		Command:     command,
		IsAlive:     alive,
		OpenBrowser: func(url string) { rec.opened = append(rec.opened, url) },
		Now:         now,
		SinceMs:     dayMs,
	}
	return rec
}

// info is the server.json a running dashboard leaves behind.
var info = server.ServerInfo{Pid: 4242, Port: 8787, URL: "http://127.0.0.1:8787", StartedAt: 1}

// writeInfo publishes one server.json.
func writeInfo(t *testing.T, env domain.EnvLookup, value server.ServerInfo) {
	t.Helper()
	if !server.WriteServerInfo(t.Context(), env, value) {
		t.Fatalf("write server.json")
	}
}

// okBody is one green answer with the given body.
func okBody(body map[string]any) cli.FetchResult { return cli.FetchResult{OK: true, Body: body} }

// assertLines pins a recorded output, both content and order.
func assertLines[T any](t *testing.T, got, want []T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %v, want %v", got, want)
	}
}

func TestParseSince(t *testing.T) {
	t.Run("takes Ns/Nm/Nh/Nd case-insensitively, a bare number as hours", func(t *testing.T) {
		cases := []struct {
			raw  string
			want int64
		}{
			{"30m", 1_800_000},
			{"24h", 86_400_000},
			{"7d", 604_800_000},
			{"45s", 45_000},
			{"5", 18_000_000},
			{"2H", 7_200_000},
			{"2 hrs", 7_200_000},
			{" 10m ", 600_000},
		}
		for _, tc := range cases {
			got, ok := cli.ParseSince(tc.raw)
			if !ok || got != tc.want {
				t.Errorf("ParseSince(%q) = (%d, %t), want (%d, true)", tc.raw, got, ok, tc.want)
			}
		}
	})

	t.Run("rejects the unusable", func(t *testing.T) {
		for _, raw := range []string{"", "m", "0h", "-5m", "1.5h", "forever"} {
			if got, ok := cli.ParseSince(raw); ok {
				t.Errorf("ParseSince(%q) = %d, want not ok", raw, got)
			}
		}
	})
}

func TestParseArgs(t *testing.T) {
	t.Run("reads the command and every flag", func(t *testing.T) {
		cases := []struct {
			name string
			argv []string
			want cli.ParsedArgs
		}{
			{"start", []string{"start"}, cli.ParsedArgs{Command: "start", SinceMs: dayMs}},
			{"--port", []string{"start", "--port", "8080"}, cli.ParsedArgs{Command: "start", Port: 8080, SinceMs: dayMs}},
			{"the low edge", []string{"start", "--port", "1024"}, cli.ParsedArgs{Command: "start", Port: 1024, SinceMs: dayMs}},
			{"--foreground", []string{"start", "--foreground"}, cli.ParsedArgs{Command: "start", SinceMs: dayMs, Foreground: true}},
			{"--since", []string{"start", "--since", "30m"}, cli.ParsedArgs{Command: "start", SinceMs: 1_800_000, SinceGiven: true}},
			{"a bare number is hours", []string{"stop", "--since", "2"}, cli.ParsedArgs{Command: "stop", SinceMs: 7_200_000, SinceGiven: true}},
			{"no argv at all", nil, cli.ParsedArgs{Command: "", SinceMs: dayMs}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if got := cli.ParseArgs(tc.argv, dayMs); !reflect.DeepEqual(got, tc.want) {
					t.Errorf("ParseArgs(%q) = %+v, want %+v", tc.argv, got, tc.want)
				}
			})
		}
	})

	t.Run("stops at the first bad flag with the exact complaint", func(t *testing.T) {
		cases := []struct {
			name string
			argv []string
			want string
		}{
			{"--port without a value", []string{"start", "--port"}, "--port needs a number in 1024..65535, got: nothing"},
			{"--port below the range", []string{"start", "--port", "80"}, "--port needs a number in 1024..65535, got: 80"},
			{"--port above the range", []string{"start", "--port", "70000"}, "--port needs a number in 1024..65535, got: 70000"},
			{"--port not a number", []string{"start", "--port", "abc"}, "--port needs a number in 1024..65535, got: abc"},
			{"--since without a value", []string{"start", "--since"}, "--since needs a duration like 30m, 24h or 7d, got: nothing"},
			{"--since not a duration", []string{"start", "--since", "forever"}, "--since needs a duration like 30m, 24h or 7d, got: forever"},
			{"an unknown flag", []string{"start", "--wat"}, "unknown argument: --wat"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				parsed := cli.ParseArgs(tc.argv, dayMs)
				if parsed.Bad == nil || *parsed.Bad != tc.want {
					t.Errorf("ParseArgs(%q).Bad = %v, want %q", tc.argv, parsed.Bad, tc.want)
				}
			})
		}
	})
}

func TestRunCliDispatch(t *testing.T) {
	t.Run("prints help for help, --help and -h", func(t *testing.T) {
		for _, command := range []string{"help", "--help", "-h"} {
			rec := makeDeps(t, overrides{})
			if code := cli.RunCli(t.Context(), []string{command}, rec.deps); code != 0 {
				t.Fatalf("runCli(%s) = %d, want 0", command, code)
			}
			assertLines(t, rec.out, []string{
				usageLine,
				"start runs the dashboard server on 127.0.0.1 on a random port unless --port is given",
			})
		}
	})

	t.Run("rejects unknown commands and bad flags with usage on stderr", func(t *testing.T) {
		unknown := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"frob"}, unknown.deps); code != 1 {
			t.Fatalf("runCli(frob) = %d, want 1", code)
		}
		assertLines(t, unknown.errLines, []string{"radar: unknown command: frob", usageLine})

		empty := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), nil, empty.deps); code != 1 {
			t.Fatalf("runCli(nothing) = %d, want 1", code)
		}
		if empty.errLines[0] != "radar: unknown command: (none)" {
			t.Errorf("first line = %q, want the (none) one", empty.errLines[0])
		}

		badFlag := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"start", "--wat"}, badFlag.deps); code != 1 {
			t.Fatalf("runCli(start --wat) = %d, want 1", code)
		}
		if badFlag.errLines[0] != "radar: unknown argument: --wat" {
			t.Errorf("first line = %q, want the unknown-argument one", badFlag.errLines[0])
		}
	})
}

func TestStart(t *testing.T) {
	t.Run("says a running server cannot take --since or a new --port until it stops", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return okBody(map[string]any{"ok": true}) },
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"start", "--since", "7d", "--port", "9100"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{
			"radar: http://127.0.0.1:8787",
			"radar: already running; --since 7d and --port 9100 applies after /radar:stop",
		})
		if len(rec.spawned) != 0 {
			t.Errorf("spawned %q, want nothing", rec.spawned)
		}
	})

	t.Run("rounds the window back to the flag the child re-parses", func(t *testing.T) {
		for _, tc := range []struct{ flag, echo string }{
			{"2h", "--since 2h"},
			{"30m", "--since 30m"},
			{"45s", "--since 45s"},
		} {
			rec := makeDeps(t, overrides{
				alive:     func(pid int64) bool { return pid == 4242 },
				responses: func(string) cli.FetchResult { return okBody(map[string]any{"ok": true}) },
			})
			writeInfo(t, rec.env, info)
			if code := cli.RunCli(t.Context(), []string{"start", "--since", tc.flag}, rec.deps); code != 0 {
				t.Fatalf("runCli(--since %s) = %d, want 0", tc.flag, code)
			}
			assertLines(t, rec.out, []string{
				"radar: http://127.0.0.1:8787",
				"radar: already running; " + tc.echo + " applies after /radar:stop",
			})
		}
	})

	t.Run("says so when the saved port was taken and the server moved", func(t *testing.T) {
		childUp := false
		var rec *recording
		rec = makeDeps(t, overrides{
			responses: func(string) cli.FetchResult {
				return cli.FetchResult{OK: childUp, Body: map[string]any{"ok": true}}
			},
			onSpawn: func() {
				childUp = true
				writeInfo(t, rec.env, server.ServerInfo{Pid: 555, Port: 9003, URL: "http://127.0.0.1:9003", StartedAt: 3})
			},
		})
		paths.SavePort(t.Context(), rec.env, 9001)
		if code := cli.RunCli(t.Context(), []string{"start"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{"radar: http://127.0.0.1:9003", "radar: port 9001 was taken; moved to 9003"})
	})

	t.Run("reuses a recorded healthy server without spawning", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return okBody(map[string]any{"ok": true}) },
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"start"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{"radar: http://127.0.0.1:8787"})
		if len(rec.spawned) != 0 {
			t.Errorf("spawned %q, want nothing", rec.spawned)
		}
	})

	t.Run("spawns a replacement when the recorded pid answers no more, and prints the new url", func(t *testing.T) {
		childUp := false
		var rec *recording
		rec = makeDeps(t, overrides{
			alive: func(pid int64) bool { return pid == 4242 },
			responses: func(url string) cli.FetchResult {
				if childUp && url == "http://127.0.0.1:9001/api/health" {
					return okBody(map[string]any{"ok": true})
				}
				return cli.FetchResult{}
			},
			onSpawn: func() {
				childUp = true // the child binds its own port and writes server.json itself
				writeInfo(t, rec.env, server.ServerInfo{Pid: 555, Port: 9001, URL: "http://127.0.0.1:9001", StartedAt: 2})
			},
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"start"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		want := [][]string{{"start", "--foreground", "--since", "1d"}}
		if !reflect.DeepEqual(rec.spawned, want) {
			t.Errorf("spawned %q, want %q", rec.spawned, want)
		}
		assertLines(t, rec.out, []string{"radar: http://127.0.0.1:9001"})
		if got := server.ReadServerInfo(t.Context(), rec.env); got == nil || got.Port != 9001 {
			t.Errorf("the stale record gave way: %v", got) // the stale record gave way
		}
	})

	t.Run("starts fresh when the recorded pid is dead, polling until the child is healthy", func(t *testing.T) {
		childUp := false
		var rec *recording
		rec = makeDeps(t, overrides{
			responses: func(string) cli.FetchResult {
				return cli.FetchResult{OK: childUp, Body: map[string]any{"ok": true}}
			},
			onSpawn: func() {
				childUp = true
				writeInfo(t, rec.env, server.ServerInfo{Pid: 555, Port: 9002, URL: "http://127.0.0.1:9002", StartedAt: 3})
			},
		})
		writeInfo(t, rec.env, info) // IsAlive answers false everywhere here
		if code := cli.RunCli(t.Context(), []string{"start"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{"radar: http://127.0.0.1:9002"})
	})

	t.Run("gives up after the timeout, passing the window and pinned port to the child", func(t *testing.T) {
		ticks := 0
		rec := makeDeps(t, overrides{
			now: func() int64 {
				ticks++
				if ticks == 1 {
					return 0
				}
				return 16_000 // the deadline sits at 0; the next look is past it, so no real sleep
			},
		})
		if code := cli.RunCli(t.Context(), []string{"start", "--port", "8123"}, rec.deps); code != 1 {
			t.Fatalf("runCli = %d, want 1", code)
		}
		assertLines(t, rec.errLines, []string{"radar: server did not become healthy within 15s"})
		want := [][]string{{"start", "--foreground", "--since", "1d", "--port", "8123"}}
		if !reflect.DeepEqual(rec.spawned, want) {
			t.Errorf("spawned %q, want %q", rec.spawned, want)
		}
	})
}

func TestStop(t *testing.T) {
	t.Run("is a no-op when nothing is recorded", func(t *testing.T) {
		rec := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"stop"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{"radar: not running"})
		if len(rec.signalled) != 0 {
			t.Errorf("signalled %v, want nothing", rec.signalled)
		}
	})

	t.Run("signals a live server and forgets it", func(t *testing.T) {
		rec := makeDeps(t, overrides{alive: func(pid int64) bool { return pid == 4242 }})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"stop"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		if !reflect.DeepEqual(rec.signalled, []int64{4242}) {
			t.Errorf("signalled %v, want [4242]", rec.signalled)
		}
		if paths.FileExists(t.Context(), paths.ServerInfoPath(t.Context(), rec.env)) {
			t.Error("server.json survived the stop")
		}
		assertLines(t, rec.out, []string{"radar: stopped"})
	})

	t.Run("signals nothing when huddle owns the lock", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive: func(pid int64) bool { return pid == 4242 },
			command: func(int64) (string, bool) {
				return "/Users/you/.agents/huddle-bin/0.0.1/huddle serve\n", true
			},
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"stop"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{
			"radar runs inside huddle here; stop it with: huddle down",
		})
		if len(rec.signalled) != 0 {
			t.Errorf("signalled %v, want nothing: the pid is huddle's", rec.signalled)
		}
		if !paths.FileExists(t.Context(), paths.ServerInfoPath(t.Context(), rec.env)) {
			t.Error("the lock huddle owns went with the stop")
		}
	})

	t.Run("signals nothing when the pid belongs to another process", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive:   func(pid int64) bool { return pid == 4242 },
			command: func(int64) (string, bool) { return "/usr/sbin/cupsd\n", true },
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"stop"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		if len(rec.signalled) != 0 {
			t.Errorf("signalled %v, want nothing: the pid is not radar's", rec.signalled)
		}
		if paths.FileExists(t.Context(), paths.ServerInfoPath(t.Context(), rec.env)) {
			t.Error("the stale record survived the stop")
		}
		assertLines(t, rec.out, []string{"radar: stopped"})
	})

	t.Run("still removes the record when the pid is already gone", func(t *testing.T) {
		rec := makeDeps(t, overrides{})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"stop"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		if len(rec.signalled) != 0 {
			t.Errorf("signalled %v, want nothing to signal", rec.signalled)
		}
		if paths.FileExists(t.Context(), paths.ServerInfoPath(t.Context(), rec.env)) {
			t.Error("server.json survived the stop")
		}
		assertLines(t, rec.out, []string{"radar: stopped"})
	})
}

func TestStatus(t *testing.T) {
	t.Run("exits 3 with nothing recorded or when health fails", func(t *testing.T) {
		down := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"status"}, down.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, down.out, []string{notRunning})

		unhealthy := makeDeps(t, overrides{alive: func(pid int64) bool { return pid == 4242 }})
		writeInfo(t, unhealthy.env, info)
		if code := cli.RunCli(t.Context(), []string{"status"}, unhealthy.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, unhealthy.out, []string{notRunning})
	})

	t.Run("reports hours once the uptime passes an hour", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive: func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult {
				return okBody(map[string]any{"sessions": float64(1), "uptimeMs": float64(3_900_000)})
			},
			now: func() int64 { return 3_900_000 },
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"status"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		if want := "  up 1h5m, 1 session tracked, v" + testVersion; rec.out[1] != want {
			t.Errorf("second line = %q, want %q", rec.out[1], want)
		}
	})

	t.Run("reports uptime and sessions from the health body", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive: func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult {
				return okBody(map[string]any{"sessions": float64(2), "uptimeMs": float64(65_000)})
			},
			now: func() int64 { return 70_000 },
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"status"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{
			"radar: http://127.0.0.1:8787",
			"  up 1m5s, 2 sessions tracked, v" + testVersion,
		})
	})

	t.Run("falls back to the recorded startedAt when the body is bare", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return okBody(map[string]any{"ok": true}) },
			now:       func() int64 { return 74_000 },
		})
		writeInfo(t, rec.env, server.ServerInfo{Pid: 4242, Port: 8787, URL: "http://127.0.0.1:8787", StartedAt: 5_000})
		if code := cli.RunCli(t.Context(), []string{"status"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		if want := "  up 1m9s, 0 sessions tracked, v" + testVersion; rec.out[1] != want {
			t.Errorf("second line = %q, want %q (69s from now − startedAt)", rec.out[1], want)
		}
	})
}

func TestUrlAndOpen(t *testing.T) {
	t.Run("prints the url; open additionally launches the browser", func(t *testing.T) {
		url := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return okBody(map[string]any{"ok": true}) },
		})
		writeInfo(t, url.env, info)
		if code := cli.RunCli(t.Context(), []string{"url"}, url.deps); code != 0 {
			t.Fatalf("runCli(url) = %d, want 0", code)
		}
		assertLines(t, url.out, []string{"radar: http://127.0.0.1:8787"})
		if len(url.opened) != 0 {
			t.Errorf("opened %q, want nothing", url.opened)
		}

		open := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return okBody(map[string]any{"ok": true}) },
		})
		writeInfo(t, open.env, info)
		if code := cli.RunCli(t.Context(), []string{"open"}, open.deps); code != 0 {
			t.Fatalf("runCli(open) = %d, want 0", code)
		}
		assertLines(t, open.opened, []string{"http://127.0.0.1:8787"})
	})

	t.Run("sends url away when nothing is recorded or health fails", func(t *testing.T) {
		none := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"url"}, none.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, none.out, []string{notRunning})

		dead := makeDeps(t, overrides{})
		writeInfo(t, dead.env, info) // IsAlive answers false everywhere, so the health check misses
		if code := cli.RunCli(t.Context(), []string{"url"}, dead.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, dead.out, []string{notRunning})

		unhealthy := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return cli.FetchResult{} },
		})
		writeInfo(t, unhealthy.env, info)
		if code := cli.RunCli(t.Context(), []string{"url"}, unhealthy.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, unhealthy.out, []string{notRunning})
	})

	t.Run("starts the server when none runs, then opens it (a first /radar:open just works)", func(t *testing.T) {
		childUp := false
		var rec *recording
		rec = makeDeps(t, overrides{
			responses: func(string) cli.FetchResult {
				return cli.FetchResult{OK: childUp, Body: map[string]any{"ok": true}}
			},
			onSpawn: func() {
				childUp = true
				writeInfo(t, rec.env, server.ServerInfo{Pid: 555, Port: 9003, URL: "http://127.0.0.1:9003", StartedAt: 3})
			},
		})
		if code := cli.RunCli(t.Context(), []string{"open"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		want := [][]string{{"start", "--foreground", "--since", "1d"}}
		if !reflect.DeepEqual(rec.spawned, want) {
			t.Errorf("spawned %q, want %q", rec.spawned, want)
		}
		assertLines(t, rec.out, []string{"radar: http://127.0.0.1:9003"})
		assertLines(t, rec.opened, []string{"http://127.0.0.1:9003"})
	})

	t.Run("opens nothing when the server will not start", func(t *testing.T) {
		ticks := 0
		rec := makeDeps(t, overrides{
			now: func() int64 {
				ticks++
				if ticks == 1 {
					return 0
				}
				return 16_000
			},
		})
		if code := cli.RunCli(t.Context(), []string{"open"}, rec.deps); code != 1 {
			t.Fatalf("runCli = %d, want 1", code)
		}
		if len(rec.opened) != 0 {
			t.Errorf("opened %q, want nothing", rec.opened)
		}
	})
}

func TestWatch(t *testing.T) {
	watching := "radar: watching alerts at http://127.0.0.1:8787"

	stuckAlert := func() map[string]any {
		return map[string]any{
			"id": "stuck:s1:100", "kind": "stuck", "project": "/w/app", "sessionId": "s1",
			"detail": "No request or tool activity for 12 min in the middle of a turn",
		}
	}
	stuckLine := "radar: Stuck · /w/app · No request or tool activity for 12 min in the middle of a turn"
	loopAlert := func() map[string]any {
		return map[string]any{
			"id": "loop:s1:main:t1", "kind": "loop", "project": "/w/app", "sessionId": "s1",
			"detail": "Bash called 5 times in a row with the same input",
		}
	}
	loopLine := "radar: Loop · /w/app · Bash called 5 times in a row with the same input"

	// alerts answers health green and hands the alerts endpoint to the case.
	alerts := func(poll func() cli.FetchResult) func(string) cli.FetchResult {
		return func(url string) cli.FetchResult {
			if strings.HasSuffix(url, "/api/alerts") {
				return poll()
			}
			return okBody(map[string]any{"ok": true})
		}
	}

	t.Run("prints the watching line, then one line per alert, 15 s between polls", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			stopAfter: 2,
			responses: alerts(func() cli.FetchResult {
				return okBody(map[string]any{"alerts": []any{stuckAlert(), loopAlert()}})
			}),
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"watch"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{watching, stuckLine, loopLine})
		if !reflect.DeepEqual(rec.slept, []int64{15_000, 15_000}) {
			t.Errorf("slept %v, want two 15 s polls", rec.slept)
		}
	})

	t.Run("does not reprint an alert it has already shown", func(t *testing.T) {
		poll := 0
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			stopAfter: 2,
			responses: alerts(func() cli.FetchResult {
				poll++
				list := []any{stuckAlert()}
				if poll != 1 {
					list = append(list, loopAlert())
				}
				return okBody(map[string]any{"alerts": list})
			}),
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"watch"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{watching, stuckLine, loopLine})
	})

	t.Run("exits 0 with `server stopped` after three failed polls in a row", func(t *testing.T) {
		polls := 0
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			stopAfter: 99,
			responses: alerts(func() cli.FetchResult {
				polls++
				return cli.FetchResult{}
			}),
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"watch"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		if polls != 3 {
			t.Errorf("polls = %d, want 3 (a fourth poll never happens)", polls)
		}
		assertLines(t, rec.out, []string{watching, "radar: server stopped"})
	})

	t.Run("keeps watching when a miss is isolated, and recovers", func(t *testing.T) {
		script := []cli.FetchResult{
			{},
			{OK: true, Body: map[string]any{"ok": true}}, // an answer that is not an alert list misses too
			{OK: true, Body: map[string]any{"alerts": []any{stuckAlert()}}},
			{},
		}
		polls := 0
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			stopAfter: 4,
			responses: alerts(func() cli.FetchResult {
				answer := cli.FetchResult{}
				if polls < len(script) {
					answer = script[polls]
				}
				polls++
				return answer
			}),
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"watch"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		if polls != 4 {
			t.Errorf("polls = %d, want 4", polls)
		}
		assertLines(t, rec.out, []string{watching, stuckLine}) // no `server stopped`: the good poll reset the count
	})

	t.Run("skips alerts it cannot read and prints an unknown kind as itself", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			stopAfter: 1,
			responses: alerts(func() cli.FetchResult {
				return okBody(map[string]any{"alerts": []any{
					map[string]any{
						"id": "budget:all:1:80", "kind": "budget",
						"detail": "Everything daily budget at 85% ($8.50 of $10.00), warn only",
					},
					map[string]any{
						"id": "retry_storm:router:zai:5", "kind": "retry_storm",
						"detail": "5 rate limits or server errors (429) within 2 min at the Z.ai router",
					},
					map[string]any{"id": "x:short", "kind": "stuck", "detail": "Session went quiet", "sessionId": "abcdefghijkl"},
					map[string]any{"id": "m:1", "kind": "mystery", "detail": "Odd"},
					float64(42),
					map[string]any{"id": "no-detail", "kind": "stuck"},
				}})
			}),
		})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"watch"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{
			watching,
			"radar: Budget · All sessions · Everything daily budget at 85% ($8.50 of $10.00), warn only",
			"radar: Retry storm · Router · 5 rate limits or server errors (429) within 2 min at the Z.ai router",
			"radar: Stuck · abcdefgh · Session went quiet",
			"radar: mystery · Router · Odd",
		})
	})

	t.Run("exits 3 with nothing recorded, a dead pid, or a server that fails health", func(t *testing.T) {
		none := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"watch"}, none.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, none.out, []string{"radar: not running"})

		dead := makeDeps(t, overrides{})
		writeInfo(t, dead.env, info) // IsAlive answers false everywhere
		if code := cli.RunCli(t.Context(), []string{"watch"}, dead.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, dead.out, []string{"radar: not running"})

		unhealthy := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return cli.FetchResult{} },
		})
		writeInfo(t, unhealthy.env, info)
		if code := cli.RunCli(t.Context(), []string{"watch"}, unhealthy.deps); code != 3 {
			t.Fatalf("runCli = %d, want 3", code)
		}
		assertLines(t, unhealthy.out, []string{"radar: not running"})
		assertLines(t, unhealthy.slept, nil) // nothing was polled
	})
}

func TestAutostart(t *testing.T) {
	t.Run("is remembered by start and forgotten by stop, so the dashboard returns with the next session", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			alive:     func(pid int64) bool { return pid == 4242 },
			responses: func(string) cli.FetchResult { return okBody(map[string]any{"ok": true}) },
		})
		ctx := t.Context()
		if paths.WantsAutostart(ctx, rec.env) {
			t.Fatal("installed, never started: the hooks start nothing")
		}
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(ctx, []string{"start"}, rec.deps); code != 0 {
			t.Fatalf("runCli(start) = %d, want 0", code)
		}
		if !paths.WantsAutostart(ctx, rec.env) {
			t.Error("the start remembered autostart")
		}
		if paths.WantsAutostart(ctx, envWith(rec.env, "RADAR_AUTOSTART", "0")) {
			t.Error("the variable decides when set")
		}
		if code := cli.RunCli(ctx, []string{"stop"}, rec.deps); code != 0 {
			t.Fatalf("runCli(stop) = %d, want 0", code)
		}
		if paths.WantsAutostart(ctx, rec.env) {
			t.Error("the stop forgot autostart")
		}
		if !paths.WantsAutostart(ctx, envWith(rec.env, "RADAR_AUTOSTART", "1")) {
			t.Error("the variable decides when set")
		}
	})
}

// TestStartForeground runs the real dashboard in-process: the foreground start prints its url
// and holds the caller until the context ends, then the whole app is gone.
func TestStartForeground(t *testing.T) {
	t.Run("start --foreground serves the dashboard until its context ends", func(t *testing.T) {
		world := makeEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		var mu sync.Mutex
		lines := []string{}
		deps := cli.Deps{
			Env:           world.env,
			Version:       testVersion,
			SinceMs:       dayMs,
			Out:           func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() },
			Err:           func(string) {},
			FetchJSON:     func(string) cli.FetchResult { return cli.FetchResult{} },
			Sleep:         func(int64) {},
			ShouldStop:    func() bool { return false },
			SpawnDetached: func([]string) int64 { return 0 },
			Signal:        func(int64) bool { return true },
			IsAlive:       func(int64) bool { return false },
			OpenBrowser:   func(string) {},
			Now:           func() int64 { return 0 },
		}
		code := make(chan int, 1)
		go func() { code <- cli.RunCli(ctx, []string{"start", "--foreground"}, deps) }()
		deadline := time.Now().Add(10 * time.Second)
		for {
			mu.Lock()
			count := len(lines)
			mu.Unlock()
			if count > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the foreground start printed no url")
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		if got := <-code; got != 0 {
			t.Fatalf("runCli = %d, want 0", got)
		}
		mu.Lock()
		first := lines[0]
		mu.Unlock()
		if !strings.HasPrefix(first, "radar: http://127.0.0.1:") {
			t.Errorf("first line = %q, want the dashboard url", first)
		}
		if left := server.ReadServerInfo(t.Context(), world.env); left != nil {
			t.Errorf("server.json went with the stop, still holds %+v", left)
		}
	})
}
