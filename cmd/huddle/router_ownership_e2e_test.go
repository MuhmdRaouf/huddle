package main

// Who owns the router (ADR-4): huddle-router is its own process, so a start from another build
// takes the running router's socket over and names the update, and `huddle down` stops the hub
// a launcher started through its `current` link while the router beside it keeps serving.
// Every process here is this test's child or a daemon it started, every home a temp one.

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
	bdd.Feature(t, "the router is its own process", func(f *bdd.F) {
		f.Scenario("a router from another build is updated", func(s *bdd.S) {
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
// binary is named on disk, and the router up started beside it keeps serving.
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
	// up starts the router beside the hub: a daemon of this test's making
	routerPID := awaitHandoverRecord(t, routerHome).PID
	t.Cleanup(func() { _ = syscall.Kill(routerPID, syscall.SIGTERM) })

	code, out, errOut = runBin(t, b1, env, root, "down")
	if code != 0 {
		t.Fatalf("down = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	if !strings.Contains(strings.ToLower(out), "huddle stopped") {
		t.Errorf("down = %q, want the stopped line", out)
	}
	until(t, 10*time.Second, func() bool { return !argvHealthy(hubURL) }, "the hub to stop answering")
	if !routerHealthy(routerPort) {
		t.Error("the router stopped answering with the hub, want it serving on")
	}
}
