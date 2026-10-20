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
	"github.com/MuhmdRaouf/huddle/internal/radar/server"
)

const (
	usageLine   = "usage: radar start|stop|status|url|open|watch [--port N] [--since 24h]"
	notRunning  = "radar: not running"
	notOnHub    = "radar: the huddle hub is not running"
	hubBase     = "http://127.0.0.1:41860"
	hubPage     = "radar: in the huddle hub at " + hubBase + "/#/radar"
	notSetUpMsg = "the huddle hub is not set up; run /huddle:setup"
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

// overrides are the answers one case injects.
type overrides struct {
	// responses answers one fetched url; nil answers failure everywhere.
	responses func(url string) cli.FetchResult
	// alive says whether one pid names a live process; nil answers false everywhere.
	alive func(pid int64) bool
	// hub answers the hub lookup; nil answers no hub.
	hub func() (string, bool)
	// ensure answers the EnsureHub effect; nil answers the hub page with its budget recorded.
	ensure func(budgetMS int) (string, bool, string)
	// now is the clock; nil never moves off zero.
	now func() int64
	// stopAfter ends the watch loop once this many polls of /api/alerts have run; 0 never ends it.
	stopAfter int
}

// recording is every side effect the injected deps left behind.
type recording struct {
	deps     cli.Deps
	out      []string
	errLines []string
	opened   []string
	slept    []int64
	ensured  []int
	hubAsked int
	env      domain.EnvLookup
	state    string
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
	hub := func() (string, bool) { return "", false }
	if over.hub != nil {
		hub = func() (string, bool) { rec.hubAsked++; return over.hub() }
	}
	ensure := func(budgetMS int) (string, bool, string) {
		rec.ensured = append(rec.ensured, budgetMS)
		return hubBase, true, ""
	}
	if over.ensure != nil {
		ensure = func(budgetMS int) (string, bool, string) {
			rec.ensured = append(rec.ensured, budgetMS)
			return over.ensure(budgetMS)
		}
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
		Sleep:       func(ms int64) { rec.slept = append(rec.slept, ms) },
		ShouldStop:  func() bool { return over.stopAfter != 0 && alertPolls >= over.stopAfter },
		IsAlive:     alive,
		OpenBrowser: func(url string) { rec.opened = append(rec.opened, url) },
		Now:         now,
		SinceMs:     dayMs,
		Hub:         hub,
		EnsureHub:   ensure,
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
				"start names the huddle hub's radar page, starting the hub when the huddle home is set up",
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

func TestStartAndOpen(t *testing.T) {
	t.Run("start names the hub's radar page and starts nothing itself", func(t *testing.T) {
		rec := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"start"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{hubPage})
		assertLines(t, rec.opened, nil)
		if len(rec.ensured) != 1 {
			t.Fatalf("EnsureHub ran %d times, want once", len(rec.ensured))
		}
		if rec.ensured[0] != 35_000 {
			t.Errorf("EnsureHub budget = %d, want 35000", rec.ensured[0])
		}
	})

	t.Run("start with no hub to start is exit 6 with the message on stderr", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			ensure: func(int) (string, bool, string) { return "", false, notSetUpMsg },
		})
		if code := cli.RunCli(t.Context(), []string{"start"}, rec.deps); code != 6 {
			t.Fatalf("runCli = %d, want 6", code)
		}
		assertLines(t, rec.errLines, []string{"radar: " + notSetUpMsg})
		assertLines(t, rec.out, nil)
	})

	t.Run("open names the hub's radar page and hands it to the browser", func(t *testing.T) {
		rec := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"open"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{hubPage})
		assertLines(t, rec.opened, []string{hubBase + "/#/radar"})
	})

	t.Run("open with no hub to start opens nothing and exits 6", func(t *testing.T) {
		rec := makeDeps(t, overrides{
			ensure: func(int) (string, bool, string) { return "", false, notSetUpMsg },
		})
		if code := cli.RunCli(t.Context(), []string{"open"}, rec.deps); code != 6 {
			t.Fatalf("runCli = %d, want 6", code)
		}
		assertLines(t, rec.errLines, []string{"radar: " + notSetUpMsg})
		assertLines(t, rec.opened, nil)
	})

	t.Run("a recorded server of an older edition is never reused, signalled or started again", func(t *testing.T) {
		rec := makeDeps(t, overrides{})
		writeInfo(t, rec.env, info)
		if code := cli.RunCli(t.Context(), []string{"start"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{hubPage})
	})
}

func TestStop(t *testing.T) {
	t.Run("names the hub that serves radar and stops nothing", func(t *testing.T) {
		rec := makeDeps(t, overrides{})
		writeInfo(t, rec.env, info) // a record of an older edition: still nothing to signal
		if code := cli.RunCli(t.Context(), []string{"stop"}, rec.deps); code != 0 {
			t.Fatalf("runCli = %d, want 0", code)
		}
		assertLines(t, rec.out, []string{"radar: runs inside the huddle hub; /huddle:stop stops it"})
		assertLines(t, rec.errLines, nil)
	})
}

func TestStatusAndURL(t *testing.T) {
	t.Run("status and url name the hub's radar page while a hub answers", func(t *testing.T) {
		status := makeDeps(t, overrides{hub: func() (string, bool) { return hubBase, true }})
		if code := cli.RunCli(t.Context(), []string{"status"}, status.deps); code != 0 {
			t.Fatalf("runCli(status) = %d, want 0", code)
		}
		assertLines(t, status.out, []string{hubPage})
		if status.hubAsked != 1 {
			t.Errorf("status asked for the hub %d times, want once", status.hubAsked)
		}

		url := makeDeps(t, overrides{hub: func() (string, bool) { return hubBase, true }})
		if code := cli.RunCli(t.Context(), []string{"url"}, url.deps); code != 0 {
			t.Fatalf("runCli(url) = %d, want 0", code)
		}
		assertLines(t, url.out, []string{"radar: " + hubBase + "/#/radar"})
		if url.hubAsked != 1 {
			t.Errorf("url asked for the hub %d times, want once", url.hubAsked)
		}
	})

	t.Run("status and url are exit 6 when no hub answers", func(t *testing.T) {
		for _, command := range []string{"status", "url"} {
			rec := makeDeps(t, overrides{})
			if code := cli.RunCli(t.Context(), []string{command}, rec.deps); code != 6 {
				t.Fatalf("runCli(%s) = %d, want 6", command, code)
			}
			assertLines(t, rec.out, []string{notOnHub})
			assertLines(t, rec.errLines, nil)
		}
	})

	t.Run("status and url never start the hub", func(t *testing.T) {
		rec := makeDeps(t, overrides{})
		if code := cli.RunCli(t.Context(), []string{"status"}, rec.deps); code != 6 {
			t.Fatalf("runCli = %d, want 6", code)
		}
		if len(rec.ensured) != 0 {
			t.Errorf("EnsureHub ran %d times, want none: status reports, it does not start", len(rec.ensured))
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

// TestStartForeground runs the real dashboard in-process: the foreground start prints its url
// and holds the caller until the context ends, then the whole app is gone. The hub's own radar
// tab is the only caller left that serves one; no code path spawns this word any more.
func TestStartForeground(t *testing.T) {
	t.Run("start --foreground serves the dashboard until its context ends", func(t *testing.T) {
		world := makeEnv(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		var mu sync.Mutex
		lines := []string{}
		deps := cli.Deps{
			Env:         world.env,
			Version:     testVersion,
			SinceMs:     dayMs,
			Out:         func(line string) { mu.Lock(); lines = append(lines, line); mu.Unlock() },
			Err:         func(string) {},
			FetchJSON:   func(string) cli.FetchResult { return cli.FetchResult{} },
			Sleep:       func(int64) {},
			ShouldStop:  func() bool { return false },
			IsAlive:     func(int64) bool { return false },
			OpenBrowser: func(string) {},
			Now:         func() int64 { return 0 },
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
