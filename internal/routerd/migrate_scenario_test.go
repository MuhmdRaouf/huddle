package routerd

// The 0.0.1 migration as scenarios: one daemon start moves a 0.0.1 setup onto huddle-router, a
// live 0.0.1 router retires and keeps answering its open session, and nothing of the job state
// or the keys is ever touched.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// huddleURL is the daemon's base URL in a rig.
func huddleURL(rig *daemonRig) string {
	return fmt.Sprintf("http://127.0.0.1:%d", rig.port)
}

// portOf is the loopback port a server URL names.
func portOf(t *testing.T, raw string) int {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatalf("%s names no port: %v", raw, err)
	}
	return port
}

// processIsAlive is whether pid names a live process.
func processIsAlive(pid int64) bool {
	return process.IsAlive(pid)
}

// readFile is a small file's text, failing the test when it cannot be read.
func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// oldRouterSeen is one request the fake 0.0.1 router saw.
type oldRouterSeen struct {
	method string
	path   string
	token  string
}

// oldRouter is a fake 0.0.1 per-provider router: it answers its health path, its retire control
// endpoint (recording the call, answering ok, and going on serving — a retired router keeps its
// open sessions) and the session path a Claude Code session posts to.
type oldRouter struct {
	server  *httptest.Server
	token   string
	mu      sync.Mutex
	seen    []oldRouterSeen
	retired bool
}

func newOldRouter(t *testing.T, healthPath string) *oldRouter {
	t.Helper()
	fake := &oldRouter{token: "watch-me-retire"}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = body
		fake.mu.Lock()
		fake.seen = append(fake.seen, oldRouterSeen{method: r.Method, path: r.URL.Path, token: r.Header.Get(router.TokenHeader)})
		isRetire := r.Method == http.MethodPost && r.URL.Path == healthPath+"/retire"
		if isRetire {
			fake.retired = true
		}
		fake.mu.Unlock()
		if isRetire {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: old-router-1\n\n")
		_, _ = io.WriteString(w, "data: old-router-2\n\n")
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (o *oldRouter) requests() []oldRouterSeen {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]oldRouterSeen(nil), o.seen...)
}

func (o *oldRouter) retireCalls() int {
	count := 0
	for _, seen := range o.requests() {
		if strings.HasSuffix(seen.path, "/retire") {
			count++
		}
	}
	return count
}

// sleeperWith runs `sh <script> args...` — a live process whose ps command line ends in the
// given words, the way a 0.0.1 `zai router run` or `zai router watch` names itself. The test
// owns the process: Cleanup, on the caller's own testing.TB (a scenario's, never one step's,
// so the process outlives the step that started it), ends it for sure.
func sleeperWith(tb testing.TB, args ...string) (pid int64, done <-chan struct{}) {
	tb.Helper()
	script := filepath.Join(tb.TempDir(), "sleeper.sh")
	body := "trap 'exit 0' TERM\nwhile :; do sleep 0.2; done\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		tb.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	if err := cmd.Start(); err != nil {
		tb.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { _ = cmd.Wait(); close(finished) }()
	tb.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-finished
		}
	})
	return int64(cmd.Process.Pid), finished
}

// write001PidRecord writes the 0.0.1 router.pid record naming pid, port and token.
func write001PidRecord(t *testing.T, state string, pid int64, port int, token string) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"port":%d,"startedAt":"2026-01-01T00:00:00Z",`+
		`"node":"%s/router/plugin-core","version":"abc123def456","frontVersion":3,`+
		`"mode":"router","token":%q}`, pid, port, state, token)
	path := filepath.Join(state, "router.pid")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// awaitMigrated waits until the daemon's migration has taken the named files away.
func awaitMigrated(t *testing.T, state string, gone ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		left := false
		for _, name := range gone {
			if _, err := os.Stat(filepath.Join(state, name)); err == nil {
				left = true
			}
		}
		if !left {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the migration never moved %v", gone)
}

func TestMigrate001Scenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "moving a 0.0.1 setup to huddle-router", func(f *bdd.F) {
		f.Scenario("a 0.0.1 zai setup is served by huddle-router after the first start", func(s *bdd.S) {
			var rig *daemonRig
			var state string
			s.Given("a 0.0.1 zai setup and the session hook that enables it", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				state = filepath.Join(rig.env["HOME"], ".agents", "zai")
				rig.env["ZAI_STATE_DIR"] = state
				layout001(t, rig.env, gonePid)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
				rig.start()
			})
			s.Then("the daemon serves zai", func(t *testing.T) {
				rig.awaitHealth(t, "zai")
			})
			s.And("the migration moved the 0.0.1 leftovers", func(t *testing.T) {
				awaitMigrated(t, state, filepath.Join("router", "plugin-core"),
					"watch."+strconv.FormatInt(gonePid-1, 10))
			})
			s.And("the ledger names huddle-router", func(t *testing.T) {
				ledger, ok := router.ReadLedger(context.Background(), filepath.Join(state, "ledger.json"))
				if !ok {
					t.Fatal("the ledger no longer reads")
				}
				if want := huddleURL(rig); ledger.RouterURL != want {
					t.Errorf("ledger routerUrl = %q, want %q", ledger.RouterURL, want)
				}
			})
			s.And("a glm request reaches zai through huddle-router", func(t *testing.T) {
				if status, body := rig.postModel(t, "glm-5.3"); status != 200 {
					t.Fatalf("glm-5.3 = %d %q", status, body)
				}
				if seen := rig.zai.requests(); len(seen) != 1 {
					t.Errorf("the zai upstream saw %d requests, want 1", len(seen))
				}
			})
		})

		f.Scenario("a live 0.0.1 router is retired and keeps answering its open session", func(s *bdd.S) {
			var rig *daemonRig
			var state string
			var old *oldRouter
			var oldPid int64
			var logLines []string
			s.Given("a live 0.0.1 zai router, its pid record and two watchdog guards", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				state = filepath.Join(rig.env["HOME"], ".agents", "zai")
				rig.env["ZAI_STATE_DIR"] = state
				old = newOldRouter(s.T, "/zai-router/health")
				oldPort := portOf(t, old.server.URL)
				// The process the pid record names is the test's own child, its ps command line
				// ending in `zai router` the way a 0.0.1 router's does.
				oldPid, _ = sleeperWith(s.T, "zai", "router")
				layout001(t, rig.env, oldPid)
				write001PidRecord(t, state, oldPid, oldPort, old.token)
				if err := router.SetEnabled(rig.env, "zai", true); err != nil {
					t.Fatal(err)
				}
			})
			s.When("the migration runs", func(t *testing.T) {
				Migrate001(context.Background(), rig.env, func(line string) { logLines = append(logLines, line) })
			})
			s.Then("the old router was retired, not stopped", func(t *testing.T) {
				joined := strings.Join(logLines, "\n")
				want := fmt.Sprintf("retired the 0.0.1 zai router (pid %d); it exits when its sessions close", oldPid)
				if !strings.Contains(joined, want) {
					t.Errorf("the log says %q, want it to name %q", joined, want)
				}
				if old.retireCalls() != 1 {
					t.Errorf("the retire control endpoint saw %d calls, want 1", old.retireCalls())
				}
				retired, ok := router.ReadRetiredFile(context.Background(), state)
				if !ok {
					t.Fatal("no retired record where the pid record was")
				}
				if retired.Pid != oldPid {
					t.Errorf("the retired record names pid %d, want %d", retired.Pid, oldPid)
				}
				if _, err := os.Stat(filepath.Join(state, "router.pid")); !os.IsNotExist(err) {
					t.Errorf("router.pid is still there: %v", err)
				}
			})
			s.And("the open session it serves still answers", func(t *testing.T) {
				client := &http.Client{Timeout: 5 * time.Second}
				resp, err := client.Post(old.server.URL+"/v1/messages", "application/json",
					strings.NewReader(`{"model":"glm-5.3","messages":[]}`))
				if err != nil {
					t.Fatalf("the session's request failed: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Errorf("the session's request = %d, want 200", resp.StatusCode)
				}
			})
			s.And("the old router process is still alive to finish its sessions", func(t *testing.T) {
				if !processIsAlive(oldPid) {
					t.Errorf("pid %d is gone", oldPid)
				}
			})
			s.And("the binary copy stays while the retired router runs", func(t *testing.T) {
				if _, err := os.Stat(filepath.Join(state, "router", "plugin-core")); err != nil {
					t.Errorf("router/plugin-core: %v", err)
				}
			})
		})

		f.Scenario("a live 0.0.1 watchdog is signalled and its guard file removed", func(s *bdd.S) {
			var rig *daemonRig
			var state string
			var watchPid int64
			var watchDone <-chan struct{}
			s.Given("a 0.0.1 zai setup whose watchdog runs", func(t *testing.T) {
				rig = newDaemonRig(s.T)
				state = filepath.Join(rig.env["HOME"], ".agents", "zai")
				rig.env["ZAI_STATE_DIR"] = state
				layout001(t, rig.env, gonePid)
				watchPid, watchDone = sleeperWith(s.T, "zai", "router", "watch")
				if err := os.WriteFile(filepath.Join(state, "watch."+strconv.FormatInt(watchPid, 10)),
					[]byte(strconv.FormatInt(watchPid, 10)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.When("the migration runs", func(t *testing.T) {
				Migrate001(context.Background(), rig.env, func(string) {})
			})
			s.Then("the watchdog was signalled and its guard file is gone", func(t *testing.T) {
				select {
				case <-watchDone:
				case <-time.After(5 * time.Second):
					t.Fatalf("the watchdog pid %d is still running", watchPid)
				}
				if _, err := os.Stat(filepath.Join(state, "watch."+strconv.FormatInt(watchPid, 10))); !os.IsNotExist(err) {
					t.Errorf("the guard file is still there: %v", err)
				}
			})
			s.And("a foreign process named in a guard file is never signalled", func(t *testing.T) {
				foreign, foreignDone := sleeperWith(s.T, "sleep", "30")
				if err := os.WriteFile(filepath.Join(state, "watch."+strconv.FormatInt(foreign, 10)),
					[]byte(strconv.FormatInt(foreign, 10)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				Migrate001(context.Background(), rig.env, func(string) {})
				select {
				case <-foreignDone:
					t.Fatalf("the foreign pid %d was signalled", foreign)
				case <-time.After(700 * time.Millisecond):
				}
				if !processIsAlive(foreign) {
					t.Fatalf("the foreign pid %d did not survive the migration", foreign)
				}
				if _, err := os.Stat(filepath.Join(state, "watch."+strconv.FormatInt(foreign, 10))); !os.IsNotExist(err) {
					t.Errorf("the foreign guard file is still there: %v", err)
				}
			})
		})

		f.Scenario("job state and keys are never touched", func(s *bdd.S) {
			var env domain.EnvLookup
			var jobBefore, keyBefore string
			s.Given("a 0.0.1 setup with job state and a key file", func(t *testing.T) {
				env, _ = migrateEnv(s.T)
				layout001(t, env, gonePid)
				for rel, body := range map[string]string{
					"jobs/j-2.json":    `{"id":"j-2"}`,
					"briefs/b-1.md":    "---\nname: b-1\n---\nBody.",
					"worktrees/w/HEAD": "gitdir: elsewhere",
					"engines.json":     `{"engines":{}}`,
					"limiter.json":     `{"slots":2}`,
					"slots/s-1.lock":   "1\n",
				} {
					path := filepath.Join(env["ZAI_STATE_DIR"], rel)
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				keyDir := filepath.Join(env["XDG_CONFIG_HOME"], "zai-plugin-cc")
				if err := os.MkdirAll(keyDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(keyDir, "env"), []byte("ZAI_API_KEY=keep-me\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			})
			s.Given("the files read before the migration", func(t *testing.T) {
				jobBefore = readFile(t, filepath.Join(env["ZAI_STATE_DIR"], "jobs", "j-2.json"))
				keyBefore = readFile(t, filepath.Join(env["XDG_CONFIG_HOME"], "zai-plugin-cc", "env"))
			})
			s.When("the migration runs", func(t *testing.T) {
				Migrate001(context.Background(), env, func(string) {})
			})
			s.Then("the job file is byte for byte what it was", func(t *testing.T) {
				if got := readFile(t, filepath.Join(env["ZAI_STATE_DIR"], "jobs", "j-2.json")); got != jobBefore {
					t.Errorf("jobs/j-2.json = %q, want %q", got, jobBefore)
				}
			})
			s.And("the key file is byte for byte what it was", func(t *testing.T) {
				if got := readFile(t, filepath.Join(env["XDG_CONFIG_HOME"], "zai-plugin-cc", "env")); got != keyBefore {
					t.Errorf("the key file changed: %q, want %q", got, keyBefore)
				}
			})
			s.And("a second run leaves the key and the jobs as they are", func(t *testing.T) {
				Migrate001(context.Background(), env, func(string) {})
				if got := readFile(t, filepath.Join(env["XDG_CONFIG_HOME"], "zai-plugin-cc", "env")); got != keyBefore {
					t.Errorf("the key file changed on the second run")
				}
				if got := readFile(t, filepath.Join(env["ZAI_STATE_DIR"], "jobs", "j-2.json")); got != jobBefore {
					t.Errorf("jobs/j-2.json changed on the second run")
				}
			})
		})

		f.Scenario("running the migration twice changes nothing", func(s *bdd.S) {
			var env domain.EnvLookup
			var state, home string
			var before map[string]string
			s.Given("a 0.0.1 setup the migration already moved", func(t *testing.T) {
				env, _ = migrateEnv(s.T)
				state = layout001(t, env, gonePid)
				home = env["HUDDLE_ROUTER_HOME"]
				Migrate001(context.Background(), env, func(string) {})
				before = snapshot(t, state, home)
			})
			s.When("the migration runs again", func(t *testing.T) {
				var lines []string
				moved := Migrate001(context.Background(), env, func(line string) { lines = append(lines, line) })
				if len(moved) != 0 {
					t.Errorf("the second run moved %v", moved)
				}
				if len(lines) != 0 {
					t.Errorf("the second run logged %v", lines)
				}
			})
			s.Then("no file changed", func(t *testing.T) {
				after := snapshot(t, state, home)
				if len(before) != len(after) {
					t.Fatalf("%d files before the second run, %d after", len(before), len(after))
				}
				for path, sum := range before {
					if after[path] != sum {
						t.Errorf("the second run changed %s", path)
					}
				}
			})
		})
	})
}
