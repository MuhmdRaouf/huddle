package main

// The handover end to end on the real binary: a start from another binary takes the running
// daemon's listening socket over while requests keep landing on the port, the old daemon
// drains and exits, a daemon over its RSS ceiling hands itself over to a fresh copy, and the
// full edition's hub takes a standalone daemon's socket over. Every process here is this
// test's child, every home a temp one, and the router home a short one: the handover socket is
// a unix socket, and macOS caps a socket path at 104 bytes.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// shortRouterHome is a router home under the system's temp location, removed at cleanup: its
// run dir holds the handover unix socket, whose path must stay under the OS's sun_path limit,
// so the name stays terse.
func shortRouterHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "hr-ho-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// copyBin copies the built binary to path, so a start from the copy is a start from another
// binary with the same behaviour.
func copyBin(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(huddleBin)
	if err != nil {
		t.Fatalf("read the built binary: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
}

// runBin runs one binary once in env and dir, answering its exit code and both outputs.
func runBin(t *testing.T, bin string, env map[string]string, dir string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = argvEntries(env)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		exited, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %s %v: %v", bin, args, err)
		}
		return exited.ExitCode(), out.String(), errOut.String()
	}
	return 0, out.String(), errOut.String()
}

// hammer dials the router port every 20 ms until halted, counting every dial that fails: a
// handover must keep the port open, so the count has to stay at zero.
type hammer struct {
	mu       sync.Mutex
	failures int
	stop     chan struct{}
	done     chan struct{}
}

func startHammer(port int) *hammer {
	h := &hammer{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(h.done)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-ticker.C:
			}
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
			if err != nil {
				h.mu.Lock()
				h.failures++
				h.mu.Unlock()
				continue
			}
			_ = conn.Close()
		}
	}()
	return h
}

// halt stops the hammer and answers how many dials failed.
func (h *hammer) halt() int {
	close(h.stop)
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failures
}

// pidWatch collects every daemon pid the test learns of, for the cleanup's kills.
type pidWatch struct {
	mu   sync.Mutex
	pids map[int]bool
}

func newPidWatch(t *testing.T) *pidWatch {
	w := &pidWatch{pids: make(map[int]bool)}
	t.Cleanup(w.killAll)
	return w
}

func (w *pidWatch) see(pid int) {
	if pid <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pids[pid] = true
}

func (w *pidWatch) killAll() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for pid := range w.pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		live := 0
		for pid := range w.pids {
			if syscall.Kill(pid, 0) == nil {
				live++
			}
		}
		if live == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	for pid := range w.pids {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// handoverRecord is what the test reads out of a router.json.
type handoverRecord struct {
	PID        int    `json:"pid"`
	Executable string `json:"executable"`
}

// awaitHandoverRecord waits for the daemon's record within five seconds: the health path can
// answer before the record is written, so a read that follows it races.
func awaitHandoverRecord(t *testing.T, routerHome string) handoverRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
			var record handoverRecord
			if json.Unmarshal(raw, &record) == nil && record.PID > 0 {
				return record
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("no daemon record five seconds after the health answer")
	return handoverRecord{}
}

// routerHealthy says whether a daemon answers the port's health path.
func routerHealthy(port int) bool {
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/huddle-router/health", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// postModel posts one glm request to the router and answers the status.
func postModel(t *testing.T, port int) int {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port), "application/json",
		strings.NewReader(`{"model":"glm-5.3","messages":[]}`))
	if err != nil {
		t.Fatalf("post glm-5.3: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestE2EHandoverAStartFromANewBinaryKeepsTheRouterPortOpen(t *testing.T) {
	root := t.TempDir()
	routerHome := shortRouterHome(t)
	port := ports.Free(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	binA := filepath.Join(root, "bin-a", "huddle")
	binB := filepath.Join(root, "bin-b", "huddle")
	copyBin(t, binA)
	copyBin(t, binB)
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
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(port),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=handover-e2e-key",
	))
	pids := newPidWatch(t)
	runStart := func(t *testing.T, bin string, extra map[string]string) string {
		t.Helper()
		code, out, errOut := runBin(t, bin, argvLayer(env, extra), root, "router", "start")
		if code != 0 {
			t.Fatalf("%s router start = %d\nstdout:\n%s\nstderr:\n%s", bin, code, out, errOut)
		}
		return out
	}

	if code, out, errOut := runBin(t, binA, env, root, "router", "start"); code != 0 {
		t.Fatalf("the first start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 15*time.Second, func() bool { return routerHealthy(port) }, "daemon A to answer its health path")
	recordA := awaitHandoverRecord(t, routerHome)
	if recordA.Executable != binA {
		t.Fatalf("daemon A's record names %q, want %q", recordA.Executable, binA)
	}
	pids.see(recordA.PID)

	hammered := startHammer(port)
	t.Run("a start from another binary takes the socket over", func(t *testing.T) {
		out := runStart(t, binB, nil)
		if !strings.Contains(out, "updated") || !strings.Contains(out, fmt.Sprintf("(pid %d → ", recordA.PID)) {
			t.Errorf("start = %q, want the updated line naming pid %d", out, recordA.PID)
		}
		var record handoverRecord
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil && json.Unmarshal(raw, &record) == nil {
				if record.PID != recordA.PID && record.Executable == binB {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		if record.PID == recordA.PID || record.Executable != binB {
			t.Fatalf("router.json still names %d by %q, want daemon B's record", record.PID, record.Executable)
		}
		pids.see(record.PID)
		deadline = time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) && syscall.Kill(recordA.PID, 0) == nil {
			time.Sleep(50 * time.Millisecond)
		}
		if syscall.Kill(recordA.PID, 0) == nil {
			t.Errorf("daemon A (pid %d) still runs 15 s after the handover", recordA.PID)
			_ = syscall.Kill(recordA.PID, syscall.SIGKILL)
		}
		if !routerHealthy(port) {
			t.Error("the router stopped answering after the handover")
		}
	})
	if failures := hammered.halt(); failures != 0 {
		t.Errorf("%d connections were refused across the update handover", failures)
	}
	if status := postModel(t, port); status != 200 {
		t.Errorf("a glm request after the handover = %d, want 200", status)
	}

	t.Run("a daemon over its RSS ceiling hands itself over", func(t *testing.T) {
		// the running daemon was started without a ceiling: a restart through B carries one
		// small enough (1 MiB) that its first check hands the socket over to a fresh copy
		current := awaitHandoverRecord(t, routerHome)
		_ = syscall.Kill(current.PID, syscall.SIGTERM)
		until(t, 10*time.Second, func() bool { return !routerHealthy(port) }, "daemon B to stop")
		runStart(t, binB, map[string]string{"HUDDLE_ROUTER_RSS_CEILING_MB": "1"})
		until(t, 15*time.Second, func() bool { return routerHealthy(port) }, "the ceiling daemon to answer")
		before := awaitHandoverRecord(t, routerHome)
		pids.see(before.PID)
		h := startHammer(port)
		after := before
		deadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
				var fresh handoverRecord
				if json.Unmarshal(raw, &fresh) == nil && fresh.PID != before.PID && fresh.PID > 0 {
					after = fresh
					break
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		if after.PID == before.PID {
			t.Fatalf("router.json still names %d after 60 s, want a fresh daemon's pid", before.PID)
		}
		pids.see(after.PID)
		if failures := h.halt(); failures != 0 {
			t.Errorf("%d connections were refused across the ceiling handover", failures)
		}
		if !routerHealthy(port) {
			t.Error("the router stopped answering after the ceiling handover")
		}
		// stop the chain: every member carries the same ceiling and hands itself over again at
		// its first check, so the record's daemon is killed until the port stays closed
		deadline = time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
				var serving handoverRecord
				if json.Unmarshal(raw, &serving) == nil && serving.PID > 0 {
					pids.see(serving.PID)
					_ = syscall.Kill(serving.PID, syscall.SIGKILL)
				}
			}
			if !routerHealthy(port) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if routerHealthy(port) {
			t.Error("the ceiling chain kept a daemon on the port")
		}
	})
}

func TestE2EHandoverTheFullEditionTakesAStandaloneRouterOver(t *testing.T) {
	root := t.TempDir()
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
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_TOKEN=handover-e2e-root",
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=handover-e2e-key",
	))
	// both processes' output lands on the router log: the takeover line arrives on the hub's
	// stderr, which this file carries
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(routerHome, "e2e.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	logText := func() string {
		raw, err := os.ReadFile(filepath.Join(routerHome, "e2e.log"))
		if err != nil {
			return ""
		}
		return string(raw)
	}
	// startChild starts the binary once and answers its exit channel, its pid and a stop that
	// kills it and waits. The body may consume the exit itself (the takeover's proof), so the
	// stop checks a flag the reaper sets before it waits on the channel again: a channel a
	// receive already drained never answers a second one.
	startChild := func(t *testing.T, args ...string) (<-chan error, int, func()) {
		t.Helper()
		cmd := exec.Command(huddleBin, args...)
		cmd.Env = argvEntries(env)
		cmd.Dir = root
		cmd.Stdout, cmd.Stderr = logFile, logFile
		if err := cmd.Start(); err != nil {
			t.Fatalf("start %v: %v", args, err)
		}
		exited := make(chan error, 1)
		var mu sync.Mutex
		reaped := false
		go func() {
			err := cmd.Wait()
			mu.Lock()
			reaped = true
			mu.Unlock()
			exited <- err
		}()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				mu.Lock()
				gone := reaped
				mu.Unlock()
				if gone {
					return
				}
				_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
				select {
				case <-exited:
				case <-time.After(10 * time.Second):
					_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
					<-exited
				}
			})
		}
		t.Cleanup(stop)
		return exited, cmd.Process.Pid, stop
	}

	exited, standalonePID, stopStandalone := startChild(t, "router", "serve")
	until(t, 15*time.Second, func() bool { return routerHealthy(routerPort) }, "the standalone daemon to answer its health path")

	hammered := startHammer(routerPort)
	_, _, _ = startChild(t, "serve")
	until(t, 15*time.Second, func() bool {
		return strings.Contains(logText(), fmt.Sprintf("huddle-router: taken over from pid %d", standalonePID))
	}, "the hub to take the router over")
	select {
	case <-exited:
		stopStandalone()
	case <-time.After(15 * time.Second):
		_ = syscall.Kill(standalonePID, syscall.SIGKILL)
		t.Fatalf("the standalone daemon (pid %d) still runs 15 s after the takeover", standalonePID)
	}
	if status := postModel(t, routerPort); status != 200 {
		t.Errorf("a glm request after the takeover = %d, want 200", status)
	}
	if failures := hammered.halt(); failures != 0 {
		t.Errorf("%d connections were refused across the full edition's takeover", failures)
	}
}
