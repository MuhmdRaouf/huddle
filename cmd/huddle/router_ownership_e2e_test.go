package main

// The hub keeps its router (HR-ROUTER-OWNERSHIP): a start that finds a hub-hosted router never
// spawns a takeover beside it — the same build under two names is not an update, and a start
// must never take a router away from the hub that hosts it (the hub's own `huddle up` replaces
// an older hub, and the router moves with it). A standalone router is still updated the old way.
// `huddle down` stops a hub the launcher started through its `current` link. Every process here
// is this test's child, every home a temp one.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

func TestRouterOwnership(t *testing.T) {
	bdd.Feature(t, "the hub keeps its router", func(f *bdd.F) {
		f.Scenario("a start from the resolved build leaves the hub's router alone", func(s *bdd.S) {
			// the world lives on the scenario's own T: a step's TempDir is gone by the next step
			root := s.T.TempDir()
			launcher := filepath.Join(root, "huddle-bin")
			b1 := filepath.Join(launcher, "src", "b1", "huddle")
			copyBin(t, b1)
			if err := os.Symlink(filepath.Join("src", "b1", "huddle"), filepath.Join(launcher, "current")); err != nil {
				t.Fatal(err)
			}
			w := startHostedHub(t, root, filepath.Join(launcher, "current"))
			env, hubPID, routerPort := w.env, w.hubPID, w.routerPort
			hubURL := fmt.Sprintf("http://127.0.0.1:%d", w.hubPort)
			s.Given("a launcher home whose current link names build one, and a hub started through it", func(t *testing.T) {
				until(t, 15*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
				until(t, 15*time.Second, func() bool { return routerHealthy(routerPort) }, "the hub's router to answer its health path")
			})
			var line string
			s.When("the resolved build runs router start", func(t *testing.T) {
				code, out, errOut := runBin(t, b1, env, root, "router", "start")
				if code != 0 {
					t.Fatalf("router start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				line = out
			})
			s.Then("the start answers the quiet up line, not an update", func(t *testing.T) {
				if !strings.Contains(line, fmt.Sprintf("huddle-router: http://127.0.0.1:%d (zai)", routerPort)) {
					t.Errorf("router start = %q, want the up line on port %d", line, routerPort)
				}
				if strings.Contains(line, "updated (pid") {
					t.Errorf("router start = %q, want no takeover of the hub's own router", line)
				}
			})
			s.Then("the hub still holds the router's port", func(t *testing.T) {
				if held := listenPid(t, routerPort); held != hubPID {
					t.Errorf("the router port is held by pid %d, want the hub's own %d", held, hubPID)
				}
			})
			s.Then("the record names the resolved build", func(t *testing.T) {
				resolved, err := filepath.EvalSymlinks(b1)
				if err != nil {
					t.Fatal(err)
				}
				record := awaitHandoverRecord(t, env["HUDDLE_ROUTER_HOME"])
				if record.Executable != resolved {
					t.Errorf("the record names %q, want the resolved build %q", record.Executable, resolved)
				}
			})
		})

		f.Scenario("a start from another build still leaves the hub's router alone", func(s *bdd.S) {
			root := s.T.TempDir()
			launcher := filepath.Join(root, "huddle-bin")
			b1 := filepath.Join(launcher, "src", "b1", "huddle")
			b2 := filepath.Join(launcher, "src", "b2", "huddle")
			copyBin(t, b1)
			copyBin(t, b2)
			if err := os.Symlink(filepath.Join("src", "b1", "huddle"), filepath.Join(launcher, "current")); err != nil {
				t.Fatal(err)
			}
			w := startHostedHub(t, root, filepath.Join(launcher, "current"))
			env, hubPID, routerPort := w.env, w.hubPID, w.routerPort
			hubURL := fmt.Sprintf("http://127.0.0.1:%d", w.hubPort)
			s.Given("a hub started through the launcher's current link, and a second build on disk", func(t *testing.T) {
				until(t, 15*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
				until(t, 15*time.Second, func() bool { return routerHealthy(routerPort) }, "the hub's router to answer its health path")
			})
			var line string
			s.When("the second build runs router start", func(t *testing.T) {
				code, out, errOut := runBin(t, b2, env, root, "router", "start")
				if code != 0 {
					t.Fatalf("router start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				line = out
			})
			s.Then("the start answers the quiet up line, not an update", func(t *testing.T) {
				if strings.Contains(line, "updated (pid") {
					t.Errorf("router start = %q, want no takeover of the hub's own router", line)
				}
				if !strings.Contains(line, fmt.Sprintf("http://127.0.0.1:%d", routerPort)) {
					t.Errorf("router start = %q, want the router's address", line)
				}
			})
			s.Then("the hub still holds the router's port", func(t *testing.T) {
				if held := listenPid(t, routerPort); held != hubPID {
					t.Errorf("the router port is held by pid %d, want the hub's own %d", held, hubPID)
				}
			})
		})

		f.Scenario("a standalone router from another build is still updated", func(s *bdd.S) {
			root := s.T.TempDir()
			launcher := filepath.Join(root, "huddle-bin")
			b1 := filepath.Join(launcher, "src", "b1", "huddle")
			b2 := filepath.Join(launcher, "src", "b2", "huddle")
			copyBin(t, b1)
			copyBin(t, b2)
			w := startStandaloneRouter(t, root, b1)
			env, standalonePID := w.env, w.routerPID
			s.Given("a standalone router serving from build one", func(t *testing.T) {
				until(t, 15*time.Second, func() bool { return routerHealthy(w.routerPort) }, "the standalone router to answer its health path")
			})
			var line string
			s.When("the second build runs router start", func(t *testing.T) {
				code, out, errOut := runBin(t, b2, env, root, "router", "start")
				if code != 0 {
					t.Fatalf("router start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				line = out
			})
			s.Then("the start takes the socket over and names the update", func(t *testing.T) {
				if !strings.Contains(line, fmt.Sprintf("updated (pid %d → ", standalonePID)) {
					t.Errorf("router start = %q, want the updated line naming pid %d", line, standalonePID)
				}
				// the taker is a detached daemon of this test's making: watch it for the cleanup
				record := awaitHandoverRecord(t, env["HUDDLE_ROUTER_HOME"])
				if record.PID == standalonePID {
					t.Errorf("the record still names the old daemon's pid %d", standalonePID)
				}
				s.T.Cleanup(func() { _ = syscall.Kill(record.PID, syscall.SIGKILL) })
			})
		})
	})
}

// ownedHub is one scenario's hub started through a launcher path: its pid, the environment
// every later command shares, and the ports it and its router answer on.
type ownedHub struct {
	env                 map[string]string
	hubPID              int
	hubPort, routerPort int
}

// startHostedHub lays the homes, starts `<launcher path> serve` as this test's child, and
// answers once both health paths are the hub's to answer.
func startHostedHub(t *testing.T, root, launcherBin string) ownedHub {
	t.Helper()
	routerHome := shortRouterHome(t)
	routerPort := ports.Free(t)
	hubPort := ports.Free(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_TOKEN=router-ownership-e2e-root",
		"HUDDLE_NO_PROJECT_CRED=1",
		"PORT="+strconv.Itoa(hubPort),
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_LEGACY_PORTS=",
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=router-ownership-e2e-key",
	))
	logFile, err := os.OpenFile(filepath.Join(routerHome, "e2e.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	cmd := exec.Command(launcherBin, "serve")
	cmd.Env = argvEntries(env)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the hub through %s: %v", launcherBin, err)
	}
	hubPID := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(hubPID, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(hubPID, syscall.SIGKILL)
			<-done
		}
	})
	return ownedHub{env: env, hubPID: hubPID, hubPort: hubPort, routerPort: routerPort}
}

// ownedRouter is one standalone daemon started as this test's child: the environment it serves
// from, its port, and the pid the start's takeover names.
type ownedRouter struct {
	env        map[string]string
	routerPort int
	routerPID  int
}

// startStandaloneRouter lays the homes and runs `<build> router serve` as this test's child.
func startStandaloneRouter(t *testing.T, root, bin string) ownedRouter {
	t.Helper()
	routerHome := shortRouterHome(t)
	routerPort := ports.Free(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_LEGACY_PORTS=",
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=router-ownership-e2e-key",
	))
	logFile, err := os.OpenFile(filepath.Join(routerHome, "e2e.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	cmd := exec.Command(bin, "router", "serve")
	cmd.Env = argvEntries(env)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the standalone router from %s: %v", bin, err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(pid, syscall.SIGKILL)
			<-done
		}
	})
	// the takeover line names the pid the record publishes, the serving daemon's own; a daemon
	// that already handed over names its taker, and both are this test's to stop
	record := awaitHandoverRecord(t, routerHome)
	if record.PID != pid {
		t.Cleanup(func() { _ = syscall.Kill(record.PID, syscall.SIGTERM) })
	}
	return ownedRouter{env: env, routerPort: routerPort, routerPID: record.PID}
}

// TestE2EDownStopsAHubTheLauncherStarted pins the `down` word against a hub the launcher's
// `current` link started: the hub the pid file names is the down word's to stop, whatever the
// binary is named on disk.
func TestE2EDownStopsAHubTheLauncherStarted(t *testing.T) {
	root := t.TempDir()
	launcher := filepath.Join(root, "huddle-bin")
	b1 := filepath.Join(launcher, "src", "b1", "huddle")
	copyBin(t, b1)
	if err := os.Symlink(filepath.Join("src", "b1", "huddle"), filepath.Join(launcher, "current")); err != nil {
		t.Fatal(err)
	}
	hubPort := ports.Free(t)
	routerPort := ports.Free(t)
	routerHome := shortRouterHome(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_TOKEN=router-ownership-e2e-root",
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_LEGACY_PORTS=",
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=router-ownership-e2e-key",
	))
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	code, out, errOut := runBin(t, filepath.Join(launcher, "current"), env, root, "up", "--port", strconv.Itoa(hubPort))
	if code != 0 {
		t.Fatalf("up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "huddle is up at "+hubURL) {
		t.Errorf("up = %q, want the up line at %s", out, hubURL)
	}
	until(t, 15*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
	// the serve child is the hub, named in the pid file: a cleanup that reaches it whether or
	// not the down word below gets to it (a refused down takes the pid file with it)
	servePID := 0
	if raw, err := os.ReadFile(filepath.Join(root, "huddle-home", "run", "huddle.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 1 {
			servePID = pid
		}
	}
	if servePID == 0 {
		t.Fatal("the up word left no pid naming the hub")
	}
	t.Cleanup(func() { _ = syscall.Kill(servePID, syscall.SIGTERM) })

	code, out, errOut = runBin(t, b1, env, root, "down")
	if code != 0 {
		t.Fatalf("down = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(strings.ToLower(out), "huddle stopped") {
		t.Errorf("down = %q, want the stopped line", out)
	}
	until(t, 10*time.Second, func() bool { return !argvHealthy(hubURL) }, "the hub to stop answering")
}
