package main

// The launcher keeps three builds under <home>/src/<commit>/huddle and deletes older ones: a
// session that outlives three plugin updates watches with a binary that no longer exists. Its
// bridge still brings the router back — it starts the launcher's `current` link, which names a
// build on disk. Every process here is this test's child and every home a temp one.

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

func TestRouterWatchdogGCE2E(t *testing.T) {
	// short root: the router's unix sockets live under it
	root, err := os.MkdirTemp("/tmp", "wg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	// the launcher home: two builds, `current` at the newest
	raw, err := os.ReadFile(huddleBin)
	if err != nil {
		t.Fatal(err)
	}
	old1 := filepath.Join(root, "src", "old1", "huddle")
	new1 := filepath.Join(root, "src", "new1", "huddle")
	for _, path := range []string{old1, new1} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("src/new1/huddle", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}

	home := filepath.Join(root, "user")
	routerHome := filepath.Join(root, "router-home")
	routerPort := ports.Free(t)
	shim := fakeBrowserShim(t)
	env := isoenv.Lookup(isoenv.Environ(t, home,
		"PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMPDIR="+root,
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_DATA="+filepath.Join(root, "data"),
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_LEGACY_PORTS=",
		"AGENTS_HUDDLE_HOME="+root,
		"HUDDLE_NO_PROJECT_CRED=1",
		"ZAI_API_KEY=watchdog-gc-e2e-key",
	))
	if err := router.SetEnabled(domain.EnvLookup(env), "zai", true); err != nil {
		t.Fatal(err)
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)
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
	// theListener names the pid listening on the port and its command, but only when the
	// command is the `current` build: anything else is not the answer this test owes.
	theListener := func() (int, string) {
		out, err := exec.Command("lsof", "-nP", "-t", "-iTCP:"+strconv.Itoa(routerPort), "-sTCP:LISTEN").Output()
		if err != nil {
			return 0, ""
		}
		for _, field := range strings.Fields(string(out)) {
			pid, err := strconv.Atoi(field)
			if err != nil {
				continue
			}
			line, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
			if err != nil {
				continue
			}
			command := strings.TrimSpace(string(line))
			if strings.Contains(command, filepath.Join("src", "new1", "huddle")) {
				return pid, command
			}
		}
		return 0, ""
	}

	// the daemon: the session's own build, a direct child of this test
	serve := exec.Command(old1, "router", "serve")
	serve.Env = argvEntries(env)
	serve.Dir = root
	var serveOut strings.Builder
	serve.Stdout, serve.Stderr = &serveOut, &serveOut
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	servePID := serve.Process.Pid
	t.Cleanup(func() {
		_ = syscall.Kill(servePID, syscall.SIGKILL)
		if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
			var record struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(raw, &record) == nil && record.PID > 0 {
				_ = syscall.Kill(record.PID, syscall.SIGKILL)
			}
		}
		pollFor(2*time.Second, func() bool { return !healthy() })
		_ = serve.Wait()
	})
	if !pollFor(20*time.Second, healthy) {
		t.Fatalf("the old build's router never came up; serve output:\n%s", serveOut.String())
	}

	// the session: one huddle mcp from the same build, stdin held open
	mcp := exec.Command(old1, "mcp")
	mcp.Env = argvEntries(env)
	mcp.Dir = root
	stdin, err := mcp.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := mcp.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var mcpErr strings.Builder
	mcp.Stderr = &mcpErr
	if err := mcp.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _, _ = mcp.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = mcp.Process.Kill()
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
	// The build goes only once the bridge has answered: macOS SIGKILLs a process whose binary
	// is deleted while it is still starting, which no long-lived session ever is.
	answered := make(chan bool, 1)
	go func() {
		lines := bufio.NewScanner(stdout)
		lines.Buffer(make([]byte, 0, 64*1024), 1<<20)
		said := false
		for lines.Scan() {
			if !said && strings.Contains(lines.Text(), `"id":1`) {
				said = true
				answered <- true
			}
		}
		if !said {
			answered <- false
		}
	}()
	select {
	case ok := <-answered:
		if !ok {
			t.Fatalf("the bridge ended before it answered initialize; stderr:\n%s", mcpErr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the bridge never answered initialize; stderr:\n%s", mcpErr.String())
	}

	// the build goes the way of the launcher's retention, then the daemon dies: the bridge
	// owes a restart from a binary that exists
	if err := os.RemoveAll(filepath.Join(root, "src", "old1")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(servePID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill the daemon: %v", err)
	}
	// 30 s, not the 15 s a quiet machine needs: the full suite under -race runs this beside
	// the other e2e builds, and the silence clock alone owes 5 s before the restart starts.
	deadline := time.Now().Add(30 * time.Second)
	for {
		pid, command := theListener()
		if command != "" && healthy() {
			t.Logf("the router is back from %s (pid %d)", command, pid)
			break
		}
		if !time.Now().Before(deadline) {
			routerLog, _ := os.ReadFile(filepath.Join(routerHome, "router.log"))
			raw, _ := exec.Command("lsof", "-nP", "-iTCP:"+strconv.Itoa(routerPort)).CombinedOutput()
			t.Fatalf("no router from the current build within 30 s of the kill; listener: %d (%q); healthy: %v; lsof: %s\nrouter.log:\n%s\nmcp stderr:\n%s\nserve output:\n%s",
				pid, command, healthy(), raw, routerLog, mcpErr.String(), serveOut.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}
