package main

// One router and one hub per user (ADR-4), end to end on the built binary: `huddle up` starts
// huddle-router and then the hub as two processes, status and the hub's health answer name both
// pids, and a Claude request through the router still answers once the hub is stopped. Every
// process here is this test's child or a daemon it started, every home a temp one, and the
// router home a short one: the handover socket is a unix socket, and macOS caps its path.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

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

// hubHealthPids is the part of the hub's health answer that names the two processes.
type hubHealthPids struct {
	PID    int `json:"pid"`
	Router struct {
		Running bool `json:"running"`
		PID     int  `json:"pid"`
		Port    int  `json:"port"`
	} `json:"router"`
}

// hubHealthPidsAt reads the hub's health answer at url.
func hubHealthPidsAt(t *testing.T, url string) hubHealthPids {
	t.Helper()
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(url + "/health")
	if err != nil {
		t.Fatalf("the hub's health answer: %v", err)
	}
	defer resp.Body.Close()
	var answer hubHealthPids
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("the hub's health answer: %v", err)
	}
	return answer
}

func TestE2EUpStartsARouterAndAHub(t *testing.T) {
	root := t.TempDir()
	routerHome := shortRouterHome(t)
	hubPort := ports.Free(t)
	routerPort := ports.Free(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	hubHome := filepath.Join(root, "huddle-home")
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+hubHome,
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_TOKEN=two-processes-e2e-root",
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_LEGACY_PORTS=",
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=two-processes-e2e-key",
	))
	pids := newPidWatch(t)
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)

	code, upOut, errOut := runBin(t, huddleBin, env, root, "up", "--port", strconv.Itoa(hubPort))
	// both daemons are this test's to stop, whatever the checks below find
	if raw, err := os.ReadFile(filepath.Join(hubHome, "run", "huddle.pid")); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
			pids.see(pid)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
		var record handoverRecord
		if json.Unmarshal(raw, &record) == nil {
			pids.see(record.PID)
		}
	}
	if code != 0 {
		t.Fatalf("up = %d\nstdout:\n%s\nstderr:\n%s", code, upOut, errOut)
	}

	hubPID := hubPid(t, hubHome)
	if !routerHealthy(routerPort) {
		t.Fatalf("no router answers on port %d after up\nstdout:\n%s", routerPort, upOut)
	}
	record := awaitHandoverRecord(t, routerHome)
	pids.see(record.PID)
	routerPID := record.PID

	t.Run("up starts two processes", func(t *testing.T) {
		if routerPID == hubPID {
			t.Fatalf("the router and the hub share pid %d, want two processes", hubPID)
		}
		if held := listenPid(t, routerPort); held != routerPID {
			t.Errorf("the router port is held by pid %d, want the router's own %d", held, routerPID)
		}
		if !argvHealthy(hubURL) {
			t.Errorf("the hub does not answer its health path at %s", hubURL)
		}
	})

	t.Run("up names both pids", func(t *testing.T) {
		for _, want := range []string{
			fmt.Sprintf("huddle is up at %s · pid %d", hubURL, hubPID),
			fmt.Sprintf("huddle-router is up at http://127.0.0.1:%d · pid %d", routerPort, routerPID),
		} {
			if !strings.Contains(upOut, want) {
				t.Errorf("up printed %q, want the line %q", upOut, want)
			}
		}
	})

	t.Run("status names both pids", func(t *testing.T) {
		code, out, errOut := runBin(t, huddleBin, env, root, "status")
		if code != 0 {
			t.Fatalf("status = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		for _, want := range []string{
			"pid " + strconv.Itoa(hubPID),
			fmt.Sprintf("huddle-router is up at http://127.0.0.1:%d · pid %d", routerPort, routerPID),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("status printed %q, want %q", out, want)
			}
		}
	})

	t.Run("the hub's health answer names both pids", func(t *testing.T) {
		answer := hubHealthPidsAt(t, hubURL)
		if answer.PID != hubPID {
			t.Errorf("health names the hub's pid %d, want %d", answer.PID, hubPID)
		}
		if !answer.Router.Running || answer.Router.PID != routerPID || answer.Router.Port != routerPort {
			t.Errorf("health names the router %+v, want running, pid %d, port %d", answer.Router, routerPID, routerPort)
		}
	})

	t.Run("a Claude request still answers with the hub stopped", func(t *testing.T) {
		code, out, errOut := runBin(t, huddleBin, env, root, "down")
		if code != 0 {
			t.Fatalf("down = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		until(t, 10*time.Second, func() bool { return !argvHealthy(hubURL) }, "the hub to stop answering")
		if !routerHealthy(routerPort) {
			t.Fatal("the router stopped answering with the hub")
		}
		before := anthropic.requests()
		if status, _ := postClaude(t, routerPort); status != 200 {
			t.Errorf("a Claude request with the hub stopped = %d, want 200", status)
		}
		if anthropic.requests() != before+1 {
			t.Errorf("the Anthropic fake saw %d requests, want %d", anthropic.requests(), before+1)
		}
		if fresh := awaitHandoverRecord(t, routerHome); fresh.PID != routerPID {
			t.Errorf("the router's pid moved from %d to %d across the hub's stop", routerPID, fresh.PID)
		}
	})
}
