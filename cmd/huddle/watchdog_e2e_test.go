package main

// The session watchdog end to end: while a `huddle mcp` bridge serves a session, a killed
// huddle-router daemon is back within 10 s, from a new pid, and the bridge ends with the
// session. Every process here is this test's child and every home a temp one.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

func TestRouterWatchdogE2E(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "user")
	routerHome := filepath.Join(root, "router-home")
	routerPort := ports.Free(t)
	shim := fakeBrowserShim(t)
	env := isoenv.Lookup(isoenv.Environ(t, home,
		"PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+root,
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_NO_PROJECT_CRED=1",
		"ZAI_API_KEY=watchdog-e2e-key",
	))
	lookup := domain.EnvLookup(env)
	if err := router.SetEnabled(lookup, "zai", true); err != nil {
		t.Fatal(err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)

	// the daemon, detached the way setup starts it
	if code, out, errOut := argvRun(t, env, root, "router", "start"); code != 0 {
		t.Fatalf("huddle router start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	daemonPID := func(t *testing.T) int {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json"))
		if err != nil {
			return 0
		}
		var record struct {
			PID int `json:"pid"`
		}
		_ = json.Unmarshal(raw, &record)
		return record.PID
	}
	firstPID := daemonPID(t)
	if firstPID <= 0 {
		t.Fatal("no daemon record after huddle router start")
	}
	healthy := func() bool {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(baseURL + router.MultiHealthPath)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return false
		}
		body, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(body), "huddle-router")
	}
	if !pollFor(5*time.Second, healthy) {
		t.Fatal("the daemon never came up")
	}
	t.Cleanup(func() {
		if pid := daemonPID(t); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		pollFor(2*time.Second, func() bool { return !healthy() })
	})

	// the session: one huddle mcp with its stdin held open
	cmd := exec.Command(huddleBin, "mcp")
	cmd.Env = argvEntries(env)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = io.Discard
	var errOut strings.Builder
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	bridge := bufio.NewWriter(stdin)
	for _, line := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	} {
		if _, err := bridge.WriteString(line + "\n"); err != nil {
			t.Fatalf("write %s: %v", line, err)
		}
	}
	if err := bridge.Flush(); err != nil {
		t.Fatal(err)
	}

	// the kill: the daemon is gone, the session's watchdog owes a new one
	if err := syscall.Kill(firstPID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the daemon: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if pid := daemonPID(t); pid > 0 && pid != firstPID && healthy() {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("no healthy daemon from a new pid within 10 s of the kill; mcp stderr:\n%s", errOut.String())
		}
		time.Sleep(200 * time.Millisecond)
	}

	// the bridge ends with the session
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatalf("huddle mcp outlived its session; mcp stderr:\n%s", errOut.String())
	}
}
