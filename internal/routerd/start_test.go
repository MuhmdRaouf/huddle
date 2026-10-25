package routerd

// The start's decisions on their own: the words a script reads, the probe's verdicts, the key
// check setup waits for, and the records the daemon publishes. Every spawn here is refused —
// the suite's binary is never the router — so a row proves the refusal, never a daemon. The
// daemon-level behaviour is the scenario files'; the real binary's spawns are the e2e's.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// startEnv is one temp home and a testkit port, the world every start row reads.
func startEnv(t *testing.T) domain.EnvLookup {
	t.Helper()
	root := t.TempDir()
	return domain.EnvLookup{
		"HOME":               root,
		"HUDDLE_ROUTER_HOME": filepath.Join(root, "router-home"),
		"RADAR_HOME":         filepath.Join(root, "radar"),
		"HUDDLE_ROUTER_PORT": strconv.Itoa(ports.Free(t)),
	}
}

// healthFake answers the daemon's health path with the given body and status, and points env's
// port at it, so the probe reads it as the daemon.
func healthFake(t *testing.T, env domain.EnvLookup, status int, body string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	env["HUDDLE_ROUTER_PORT"] = parsed.Port()
}

// healthBody is the daemon's health answer naming the given providers.
func healthBody(providers ...string) string {
	raw, _ := json.Marshal(HealthAnswer{Name: router.MultiName, Providers: providers})
	return string(raw)
}

func TestStartAnswersWithoutSpawningWhereItCan(t *testing.T) {
	t.Parallel()

	t.Run("no provider set up is a quiet line and no spawn", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		line, err := Start(context.Background(), StartOptions{Env: env})
		if err != nil || line != "huddle-router: no provider is set up" {
			t.Fatalf("Start = %q, %v, want the quiet line", line, err)
		}
	})

	t.Run("a port that is no port is an error", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		env["HUDDLE_ROUTER_PORT"] = "not a port"
		if _, err := Start(context.Background(), StartOptions{Env: env}); err == nil {
			t.Fatal("Start with a bad port = nil error, want one")
		}
	})

	t.Run("a daemon that answers is named and not started over", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		line, err := Start(context.Background(), StartOptions{Env: env})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if !strings.Contains(line, "http://127.0.0.1:") || !strings.Contains(line, "zai") {
			t.Errorf("Start = %q, want the address and the provider", line)
		}
	})

	t.Run("a record naming this binary is the same quiet line", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		writeStartRecord(t, env, record{PID: os.Getpid(), Port: 1, Executable: selfExecutable(env), Started: "now"})
		line, err := Start(context.Background(), StartOptions{Env: env})
		if err != nil || !strings.Contains(line, "zai") {
			t.Fatalf("Start = %q, %v, want the quiet line", line, err)
		}
	})

	t.Run("a record naming another binary is a takeover this suite refuses", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		writeStartRecord(t, env, record{PID: 1, Port: 1, Executable: "/usr/local/bin/huddle", Started: "now"})
		_, err := Start(context.Background(), StartOptions{Env: env})
		if err == nil || !strings.Contains(err.Error(), "test binary") {
			t.Fatalf("Start over another binary = %v, want the test-binary refusal", err)
		}
		if _, err := os.Stat(filepath.Join(router.HomeOf(env), "router.log")); err != nil {
			t.Errorf("the takeover's log never opened: %v", err)
		}
	})

	t.Run("nothing answering spawns and names the refusal", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		_, err := Start(context.Background(), StartOptions{Env: env})
		if err == nil || !strings.Contains(err.Error(), "test binary") {
			t.Fatalf("Start with no daemon = %v, want the test-binary refusal", err)
		}
	})
}

// writeStartRecord lays one daemon record in the home's run dir.
func writeStartRecord(t *testing.T, env domain.EnvLookup, entry record) {
	t.Helper()
	path := filepath.Join(router.HomeOf(env), "run", "router.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRouterKeyCheckWaitsForTheProvidersLane(t *testing.T) {
	t.Parallel()
	zai, ok := providers.ByName("zai")
	if !ok {
		t.Fatal("no provider named zai")
	}
	kimi, ok := providers.ByName("kimi")
	if !ok {
		t.Fatal("no provider named kimi")
	}

	t.Run("the lane served is the line setup reads", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		line, err := RouterKeyCheck(context.Background(), zai, env, 0)
		if err != nil || line != "huddle-router serves "+zai.Display {
			t.Fatalf("RouterKeyCheck = %q, %v, want the served line", line, err)
		}
	})

	t.Run("a lane that never comes up names what is served instead", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		_, err := RouterKeyCheck(context.Background(), kimi, env, 150)
		if err == nil || !strings.Contains(err.Error(), "serves zai, not kimi") {
			t.Fatalf("RouterKeyCheck = %v, want the served-instead error", err)
		}
	})

	t.Run("no daemon at all names the port", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		_, err := RouterKeyCheck(context.Background(), zai, env, 150)
		if err == nil || !strings.Contains(err.Error(), "not running on port") {
			t.Fatalf("RouterKeyCheck = %v, want the not-running error", err)
		}
	})

	t.Run("a port that is no port is an error", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		env["HUDDLE_ROUTER_PORT"] = "nope"
		if _, err := RouterKeyCheck(context.Background(), zai, env, 0); err == nil {
			t.Fatal("RouterKeyCheck with a bad port = nil error, want one")
		}
	})
}

func TestHealthReadsTheDaemonsOwnAnswer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"the daemon's answer", http.StatusOK, healthBody("zai", "kimi"), true},
		{"another name is not the daemon", http.StatusOK, `{"name":"someone-else","providers":[]}`, false},
		{"an error status is not the daemon", http.StatusInternalServerError, healthBody("zai"), false},
		{"a body that is no answer is not the daemon", http.StatusOK, "not json", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := startEnv(t)
			healthFake(t, env, tt.status, tt.body)
			port, err := router.PortOf(env)
			if err != nil {
				t.Fatal(err)
			}
			answer, ok := Health(context.Background(), port)
			if ok != tt.want {
				t.Fatalf("Health ok = %t, want %t", ok, tt.want)
			}
			if ok && len(answer.Providers) != 2 {
				t.Errorf("Health = %+v, want both providers", answer)
			}
		})
	}

	t.Run("a port nothing answers is not the daemon", func(t *testing.T) {
		t.Parallel()
		_, ok := Health(context.Background(), ports.Free(t))
		if ok {
			t.Fatal("Health on a free port = ok, want false")
		}
	})
}

func TestWatchHealthyProbesTheDaemon(t *testing.T) {
	t.Parallel()
	t.Run("the daemon's answer is healthy", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		if !watchHealthy(context.Background(), env) {
			t.Fatal("watchHealthy = false, want true")
		}
	})
	t.Run("a free port is not", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		if watchHealthy(context.Background(), env) {
			t.Fatal("watchHealthy on a free port = true, want false")
		}
	})
	t.Run("a port that is no port is not", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		env["HUDDLE_ROUTER_PORT"] = "nope"
		if watchHealthy(context.Background(), env) {
			t.Fatal("watchHealthy with a bad port = true, want false")
		}
	})
	t.Run("an answer that is an error is not", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusInternalServerError, healthBody("zai"))
		if watchHealthy(context.Background(), env) {
			t.Fatal("watchHealthy on an error answer = true, want false")
		}
	})
}

// TestWatchWithItsRealStartProbesAndRefusesTheSpawn runs the watchdog's default path: the real
// health probe, the real start, the real log. The start this suite reaches is always refused —
// the test binary is never the router — and the watch comes back for the next tick.
func TestWatchWithItsRealStartProbesAndRefusesTheSpawn(t *testing.T) {
	t.Parallel()
	env := startEnv(t)
	if err := router.SetEnabled(env, "zai", true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	Watch(ctx, WatchOptions{Env: env, Every: 20 * time.Millisecond, Silence: 60 * time.Millisecond})
	raw, err := os.ReadFile(filepath.Join(router.HomeOf(env), "router.log"))
	if err != nil {
		t.Fatalf("the watch never wrote the router log: %v", err)
	}
	log := string(raw)
	if !strings.Contains(log, "restarted by the session watchdog") {
		t.Errorf("the log = %q, want the restart line", log)
	}
	if !strings.Contains(log, "test binary") {
		t.Errorf("the log = %q, want the spawn's refusal", log)
	}
}

func TestUpLineNamesTheAddressAndTheProviders(t *testing.T) {
	t.Parallel()
	line := upLine(20000, HealthAnswer{Providers: []string{"zai", "kimi"}})
	if want := "huddle-router: http://127.0.0.1:20000 (zai, kimi)"; line != want {
		t.Errorf("upLine = %q, want %q", line, want)
	}
}

func TestEnvironCarriesTheLookupToTheChild(t *testing.T) {
	t.Parallel()
	out := environ(domain.EnvLookup{"HUDDLE_ROUTER_PORT": "20000", "ZAI_API_KEY": "k"})
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "HUDDLE_ROUTER_PORT=20000") || !strings.Contains(joined, "ZAI_API_KEY=k") {
		t.Errorf("environ = %v, want both entries", out)
	}
}

func TestRecordFileRoundTrips(t *testing.T) {
	t.Parallel()
	env := startEnv(t)

	t.Run("no record is no record", func(t *testing.T) {
		if _, ok := readRecord(env); ok {
			t.Fatal("readRecord with no file = ok, want false")
		}
	})
	t.Run("a record that is no record is no record", func(t *testing.T) {
		path := filepath.Join(router.HomeOf(env), "run", "router.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := readRecord(env); ok {
			t.Fatal("readRecord of garbage = ok, want false")
		}
	})
	t.Run("the written record reads back", func(t *testing.T) {
		path := filepath.Join(router.HomeOf(env), "run", "elsewhere.json")
		if err := writeRecord(context.Background(), path, 20000, "test", "self"); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var parsed record
		if json.Unmarshal(raw, &parsed) != nil {
			t.Fatalf("the record = %s, not json", raw)
		}
		if parsed.Port != 20000 || parsed.Version != "test" || parsed.Executable != "self" {
			t.Errorf("the record = %+v, want what was written", parsed)
		}
	})
	t.Run("a record behind a file is an error", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, []byte("a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeRecord(context.Background(), filepath.Join(blocker, "router.json"), 1, "test", "self"); err == nil {
			t.Fatal("writeRecord behind a file = nil error, want one")
		}
	})
}

func TestStatOfAnswersTheFingerprint(t *testing.T) {
	t.Parallel()
	absent := filepath.Join(t.TempDir(), "absent")
	if got := statOf(absent); got.there {
		t.Error("statOf an absent file = there, want the empty fingerprint")
	}
	path := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(path, []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := statOf(path); !got.there || got.size == 0 {
		t.Errorf("statOf = %+v, want the file's fingerprint", got)
	}
}

func TestAnthropicOfReadsTheOverrideOrTheDefault(t *testing.T) {
	t.Parallel()
	if got, err := anthropicOf(domain.EnvLookup{}); err != nil || got.Host != "api.anthropic.com" {
		t.Errorf("anthropicOf = %v, %v, want Anthropic itself", got, err)
	}
	if got, err := anthropicOf(domain.EnvLookup{"HUDDLE_ROUTER_ANTHROPIC_URL": "http://127.0.0.1:1"}); err != nil || got.Host != "127.0.0.1:1" {
		t.Errorf("anthropicOf override = %v, %v, want the override", got, err)
	}
	if _, err := anthropicOf(domain.EnvLookup{"HUDDLE_ROUTER_ANTHROPIC_URL": "http://x/\u007f"}); err == nil {
		t.Error("anthropicOf of an unparsable url = nil error, want one")
	}
}

func TestLanesOfSkipsWhatItCannotBuild(t *testing.T) {
	t.Parallel()
	var lines []string
	log := func(line string) { lines = append(lines, line) }

	t.Run("a name no compiled provider owns is skipped", func(t *testing.T) {
		lanes := lanesOf(domain.EnvLookup{}, []string{"ghost", "zai"}, log)
		if len(lanes) != 1 || lanes[0].Provider.Name != "zai" {
			t.Errorf("lanesOf = %d lanes, want zai's alone", len(lanes))
		}
		if !strings.Contains(strings.Join(lines, "\n"), "no provider named ghost") {
			t.Errorf("the log = %v, want the unknown name", lines)
		}
	})

	t.Run("a lane whose endpoint does not parse is skipped", func(t *testing.T) {
		t.Parallel()
		env := domain.EnvLookup{"ZAI_ROUTER_URL": "http://x/\u007f"}
		lanes := lanesOf(env, []string{"zai"}, log)
		if len(lanes) != 0 {
			t.Errorf("lanesOf = %d lanes, want none", len(lanes))
		}
		if !strings.Contains(strings.Join(lines, "\n"), "skipping zai") {
			t.Errorf("the log = %v, want the skip", lines)
		}
	})
}

func TestCheckRSSHandsOverOnlyOverTheCeiling(t *testing.T) {
	t.Parallel()

	t.Run("under the ceiling nothing happens", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		spawned := checkRSS(1<<40, time.Time{}, env, func(string) {})
		if !spawned.IsZero() {
			t.Errorf("checkRSS answered %v, want the spawn time unchanged", spawned)
		}
	})

	t.Run("over the ceiling the suite refuses its own spawn and says so", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		var lines []string
		spawned := checkRSS(1, time.Time{}, env, func(line string) { lines = append(lines, line) })
		if spawned.IsZero() {
			t.Error("checkRSS answered a zero spawn time, want now")
		}
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, "over the ceiling") || !strings.Contains(joined, "test binary") {
			t.Errorf("the log = %q, want the handover and the refusal", joined)
		}
	})

	t.Run("a try no takeover answered is logged before the next", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		var lines []string
		stale := time.Now().Add(-2 * rssInterval)
		checkRSS(1, stale, env, func(line string) { lines = append(lines, line) })
		if !strings.Contains(strings.Join(lines, "\n"), "no takeover arrived within") {
			t.Errorf("the log = %q, want the stale try", strings.Join(lines, "\n"))
		}
	})

	t.Run("a log that cannot open still answers now", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		blocker := filepath.Join(root, "blocker")
		if err := os.WriteFile(blocker, []byte("a file"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := domain.EnvLookup{"HUDDLE_ROUTER_HOME": filepath.Join(blocker, "router-home")}
		var lines []string
		spawned := checkRSS(1, time.Time{}, env, func(line string) { lines = append(lines, line) })
		if spawned.IsZero() {
			t.Error("checkRSS answered a zero spawn time, want now")
		}
		if !strings.Contains(strings.Join(lines, "\n"), "over the ceiling") {
			t.Errorf("the log = %q, want the handover line before the failure", strings.Join(lines, "\n"))
		}
	})
}

// TestStartNamesItsOwnStartFailures covers the failure rows a start answers before any spawn:
// a providers.json that does not hold up, and a home that cannot take the router log.
func TestStartNamesItsOwnStartFailures(t *testing.T) {
	t.Parallel()

	t.Run("a providers.json that does not parse is an error", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		if err := os.MkdirAll(router.HomeOf(env), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(router.EnabledFile(env), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Start(context.Background(), StartOptions{Env: env}); err == nil {
			t.Fatal("Start over garbage providers.json = nil error, want one")
		}
	})

	t.Run("a router log that cannot open is an error", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(router.HomeOf(env), 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(router.HomeOf(env), 0o700) })
		if _, err := Start(context.Background(), StartOptions{Env: env}); err == nil {
			t.Fatal("Start in a read-only home = nil error, want one")
		}
	})
}

// TestUpdatedOrUpNamesALogItCannotOpen covers the takeover's log row: a healthy daemon whose
// record names another binary, and a home that cannot take the router log.
func TestUpdatedOrUpNamesALogItCannotOpen(t *testing.T) {
	t.Parallel()
	env := startEnv(t)
	healthFake(t, env, http.StatusOK, healthBody("zai"))
	writeStartRecord(t, env, record{PID: 1, Port: 1, Executable: "/usr/local/bin/huddle", Started: "now"})
	if err := os.MkdirAll(router.HomeOf(env), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(router.HomeOf(env), 0o700) })
	if _, err := Start(context.Background(), StartOptions{Env: env}); err == nil {
		t.Fatal("Start = nil error, want the log's failure")
	}
}

// spawnBinary builds the real huddle binary once, for the rows that spawn a real daemon. The
// sweep is the last cleanup: it stops every process of this binary, including a takeover taker
// that finished after the record stopped naming it.
func spawnBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "huddle")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/MuhmdRaouf/huddle/cmd/huddle").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	t.Cleanup(func() { stopProcessesOf(t, bin) })
	return bin
}

// spawnEnv is one child environment through isoenv, with the daemon's state at a short home (the
// handover socket lives under the run dir, and macOS caps a socket path at 104 bytes — under
// tmpguard the system temp itself is deep, so nothing may nest here), the port a testkit one and
// both upstreams the test's fakes.
func spawnEnv(t *testing.T) (domain.EnvLookup, string) {
	t.Helper()
	routerHome := shortHome(t)
	env := domain.EnvLookup{}
	for _, entry := range isoenv.Environ(t, t.TempDir()) {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	env["HUDDLE_ROUTER_HOME"] = routerHome
	env["RADAR_HOME"] = filepath.Join(t.TempDir(), "radar")
	env["HUDDLE_ROUTER_PORT"] = strconv.Itoa(ports.Free(t))
	env["HUDDLE_ROUTER_ANTHROPIC_URL"] = newDaemonUpstream(t, "anthropic").server.URL
	env["ZAI_ROUTER_URL"] = newDaemonUpstream(t, "zai").server.URL
	env["ZAI_API_KEY"] = "test-key"
	env["HUDDLE_KEYS"] = "files"
	if err := router.SetEnabled(env, "zai", true); err != nil {
		t.Fatal(err)
	}
	// the recorded daemon is this test's child: the stop signal is the test's to send, and it
	// waits for the exit, so no daemon of this run outlives the test
	t.Cleanup(func() { stopRecordedDaemon(t, env) })
	return env, routerHome
}

// stopRecordedDaemon signals the daemon the home's record names, waits for its exit, and names
// no pid but the record's: a record that is gone, stale or unread costs nothing.
func stopRecordedDaemon(t *testing.T, env domain.EnvLookup) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(router.HomeOf(env), "run", "router.json"))
	if err != nil {
		return
	}
	var dead record
	if json.Unmarshal(raw, &dead) != nil || dead.PID <= 1 || dead.PID == os.Getpid() {
		return
	}
	_ = syscall.Kill(dead.PID, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(dead.PID, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(dead.PID, syscall.SIGKILL)
}

// stopProcessesOf stops every process whose command line is the binary or the binary with
// arguments, whatever pid a record remembered: a takeover taker that finished late is no
// longer in the record, and nothing else would stop it. pid ≤ 1 and this process are never
// signaled, and each stop is logged so a leak shows in the test's output.
func stopProcessesOf(t *testing.T, bin string) {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		pidText, command, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(pidText))
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		command = strings.TrimSpace(command)
		if command != bin && !strings.HasPrefix(command, bin+" ") {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGTERM)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if gone(pid) {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if !gone(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Logf("stopped a leftover daemon pid %d from %s", pid, bin)
	}
}

// gone answers whether the process is over, reaping it when it was this process's child: a
// zombie answers kill with the alive side until someone waits for it.
func gone(pid int) bool {
	var status syscall.WaitStatus
	if reaped, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); err == nil {
		return reaped == pid
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}

// TestStartSpawnsTheRealBinaryAndWaitsForItsHealth runs the start's whole happy path against
// the real binary: the detached spawn, the poll for its health answer, the record it writes —
// and the takeover a start from another record's binary asks for, waited out to the fresh pid.
// The port is a testkit one, the child's environment a sandbox home, and the cleanup signals
// the pid the record names.
func TestStartSpawnsTheRealBinaryAndWaitsForItsHealth(t *testing.T) {
	bin := spawnBinary(t)
	env, _ := spawnEnv(t)
	// A launcher home whose `current` names another build: the daemon's record must still name
	// the binary it runs from, or a start from the newer build would see no update to make.
	launcher := t.TempDir()
	newer := filepath.Join(launcher, "src", "newer", "huddle")
	if err := os.MkdirAll(filepath.Dir(newer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("src", "newer", "huddle"), filepath.Join(launcher, "current")); err != nil {
		t.Fatal(err)
	}
	env["AGENTS_HUDDLE_HOME"] = launcher

	t.Run("a start with no daemon spawns one and names what it serves", func(t *testing.T) {
		var line string
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			line, err = Start(context.Background(), StartOptions{Env: env, Executable: bin, BudgetMs: 10000})
			if err == nil {
				break
			}
			// Another process's test took the testkit port between the draw and the
			// daemon's bind (a testkit port is one process's reservation): draw a fresh
			// one and spawn again. A daemon that could not bind has already exited.
			env["HUDDLE_ROUTER_PORT"] = strconv.Itoa(ports.Free(t))
		}
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		if !strings.Contains(line, "zai") {
			t.Errorf("Start = %q, want zai served", line)
		}
		spawned := awaitRecord(t, env)
		if spawned.PID == os.Getpid() {
			t.Fatalf("the record = %+v, want the daemon's own", spawned)
		}
		if spawned.Executable != mustResolved(t, bin) {
			t.Errorf("the record names %q, want the spawned binary resolved", spawned.Executable)
		}
	})

	t.Run("a start whose record names another binary takes the socket over", func(t *testing.T) {
		old := awaitRecord(t, env)
		stale := old
		stale.Executable = "/usr/local/bin/huddle-from-another-build"
		var line string
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			writeStartRecord(t, env, stale)
			line, err = Start(context.Background(), StartOptions{Env: env, Executable: bin, BudgetMs: 10000})
			if err == nil && strings.Contains(line, "updated (pid") {
				break
			}
			// Under a loaded machine the taker can miss its five-second window and fall
			// back to a normal start, which the draining holder's lock refuses: the taker
			// exits, the old daemon keeps serving, and the takeover is asked again.
			err = fmt.Errorf("the takeover = %q, %v", line, err)
		}
		if err != nil {
			log, _ := os.ReadFile(filepath.Join(router.HomeOf(env), "router.log"))
			t.Fatalf("the takeover never finished: %v\nthe daemon's log:\n%s", err, log)
		}
		fresh, ok := readRecord(env)
		if !ok || fresh.PID == old.PID {
			t.Errorf("the record still names pid %d, want the taker's own", old.PID)
		}
	})
}

// awaitRecord waits for the daemon's own record: the daemon publishes it after the serve loop
// is up, and a loaded machine can open that gap past the start's answer, while the daemon
// serves without it (the record is status's and start's to miss, never the router's).
func awaitRecord(t *testing.T, env domain.EnvLookup) record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if spawned, ok := readRecord(env); ok && spawned.PID > 1 {
			return spawned
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the daemon left no record")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestStopProcessesOfStopsEveryProcessOfTheBinary starts two processes from the test's own
// binary path beside one from another path, sweeps, and proves the sweep stopped exactly the
// two. The sleeper is a tiny Go build: a copied Apple platform binary is killed by the kernel
// at once on macOS.
func TestStopProcessesOfStopsEveryProcessOfTheBinary(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "huddle")
	src := filepath.Join(t.TempDir(), "sleeper.go")
	body := "package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(30 * time.Second) }\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	copies := []*exec.Cmd{exec.Command(bin, "30"), exec.Command(bin, "30")}
	for i, cmd := range copies {
		if err := cmd.Start(); err != nil {
			t.Fatalf("copy %d: %v", i, err)
		}
	}
	original := exec.Command("/bin/sleep", "30")
	if err := original.Start(); err != nil {
		t.Fatalf("the original sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = original.Process.Signal(syscall.SIGTERM)
		_ = original.Wait()
	})

	stopProcessesOf(t, bin)

	for i, cmd := range copies {
		if err := cmd.Wait(); err == nil {
			t.Errorf("copy %d from %s outlived the sweep", i, bin)
		}
	}
	if err := syscall.Kill(original.Process.Pid, 0); err != nil {
		t.Errorf("the original /bin/sleep pid %d did not survive the sweep: %v", original.Process.Pid, err)
	}
}

// TestRunAnswersBeforeItServes covers the rows a Run answers before any lane is mounted: the
// defaults it fills, the records it cannot read, the ports it cannot take.
func TestRunAnswersBeforeItServes(t *testing.T) {
	t.Parallel()

	t.Run("the defaults are the daemon's own poll and log", func(t *testing.T) {
		env := startEnv(t)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		if err := Run(ctx, Options{Env: env, Port: ports.Free(t)}); err != nil {
			t.Fatalf("Run with nothing enabled = %v, want nil", err)
		}
		if raw, err := os.ReadFile(filepath.Join(router.HomeOf(env), "router.log")); err != nil ||
			!strings.Contains(string(raw), "no provider is enabled yet") {
			t.Errorf("the router log = %q (%v), want the waiting line", raw, err)
		}
	})

	t.Run("a providers.json that does not hold up is logged and waited out", func(t *testing.T) {
		env := startEnv(t)
		if err := os.MkdirAll(router.HomeOf(env), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(router.EnabledFile(env), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		var lines []string
		if err := Run(ctx, Options{Env: env, Port: ports.Free(t), Log: func(line string) { lines = append(lines, line) }, Poll: 30 * time.Millisecond}); err != nil {
			t.Fatalf("Run over garbage providers.json = %v, want nil", err)
		}
		if !strings.Contains(strings.Join(lines, "\n"), "not valid providers.json") {
			t.Errorf("the log = %q, want the record's failure", strings.Join(lines, "\n"))
		}
	})

	t.Run("a port that is no port is an error", func(t *testing.T) {
		env := startEnv(t)
		env["HUDDLE_ROUTER_PORT"] = "nope"
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := Run(ctx, Options{Env: env, Log: func(string) {}}); err == nil {
			t.Fatal("Run with a bad port = nil error, want one")
		}
	})

	t.Run("a port another listener holds is an error", func(t *testing.T) {
		env := startEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		held := ports.Listen(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := Run(ctx, Options{Env: env, Port: held.Addr().(*net.TCPAddr).Port, Log: func(string) {}}); err == nil {
			t.Fatal("Run on a held port = nil error, want one")
		}
	})

	t.Run("an Anthropic that is no url ends the serve at once", func(t *testing.T) {
		env := startEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		env["HUDDLE_ROUTER_ANTHROPIC_URL"] = "http://x/\u007f"
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := Run(ctx, Options{Env: env, Port: ports.Free(t), Log: func(string) {}}); err == nil {
			t.Fatal("Run with an unparsable Anthropic = nil error, want one")
		}
	})
}
