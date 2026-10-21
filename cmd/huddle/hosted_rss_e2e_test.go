package main

// The hub-hosted router end to end: a hub with a one-megabyte RSS ceiling keeps serving the
// router itself across several check intervals. A hosted router's memory is the hub's, so it
// never spawns a `router serve --takeover` child for memory — only a standalone daemon hands
// itself over. Every process here is this test's child and every home a temp one.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

func TestE2EHostedRSSTheHubKeepsServingItsRouter(t *testing.T) {
	root := t.TempDir()
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
		"HUDDLE_TOKEN=hosted-rss-e2e-root",
		"HUDDLE_NO_PROJECT_CRED=1",
		"PORT="+strconv.Itoa(hubPort),
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_LEGACY_PORTS=",
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=hosted-rss-e2e-key",
		"HUDDLE_ROUTER_RSS_CEILING_MB=1",
		"HUDDLE_ROUTER_RSS_INTERVAL_MS=200",
	))
	logFile, err := os.OpenFile(filepath.Join(routerHome, "e2e.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	cmd := exec.Command(huddleBin, "serve")
	cmd.Env = argvEntries(env)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the hub: %v", err)
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

	until(t, 15*time.Second, func() bool { return routerHealthy(routerPort) }, "the hub's router to answer its health path")

	// the rss check rides the providers.json poll tick (2 s), so the wait covers one poll plus
	// two rss intervals of 200 ms: a handover would have happened and moved the port by now
	time.Sleep(4 * time.Second)
	if !routerHealthy(routerPort) {
		t.Fatal("the router stopped answering within the check intervals")
	}
	if held := listenPid(t, routerPort); held != hubPID {
		t.Errorf("the router port is held by pid %d, want the hub's own %d", held, hubPID)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "huddle-home", "run", "serve.lock")); err != nil {
		t.Fatalf("read the hub's pid file: %v", err)
	} else if vouched, err := strconv.Atoi(strings.TrimSpace(string(raw))); err != nil || vouched != hubPID {
		t.Errorf("the hub's pid file names %d (%v), want the hub's own %d", vouched, err, hubPID)
	}
	if serving := routerServeProcesses(t); len(serving) != 0 {
		t.Errorf("takeover children run beside the hub:\n%s", strings.Join(serving, "\n"))
	}
}

// listenPid answers the pid holding the port's LISTEN, as lsof reports it.
func listenPid(t *testing.T, port int) int {
	t.Helper()
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		t.Fatalf("lsof the router port: %v", err)
	}
	for _, line := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(line); err == nil {
			return pid
		}
	}
	t.Fatalf("lsof named no listener: %q", string(out))
	return 0
}

// routerServeProcesses names every running `router serve` of this test's own binary: a hosted
// hub spawns none, so a hit is a memory handover the ceiling should never have started.
func routerServeProcesses(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, huddleBin) && strings.Contains(line, "router serve") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}
