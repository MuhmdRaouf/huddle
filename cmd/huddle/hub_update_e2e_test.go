package main

// The hub update end to end on the real binary (D2): an older binary's hub stands beside a
// standalone huddle-router — the 0.0.1 machine's shape — and up from a newer binary replaces
// the hub, says so, and the new hub takes the router's socket over, so the router port never
// stops answering and one huddle serve is left. Every process here is this test's child, every
// home a temp one, and the router home a short one: the handover socket is a unix socket, and
// macOS caps a socket path at 104 bytes.

import (
	"encoding/json"
	"fmt"
	"io"
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

// hubHealthAnswer is the part of a hub's health answer the update test reads.
type hubHealthAnswer struct {
	Executable string `json:"executable"`
}

// hubExecutableAt reads the executable the hub answering at port names.
func hubExecutableAt(t *testing.T, port int) string {
	t.Helper()
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
	if err != nil {
		t.Fatalf("the hub's health answer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("the hub's health answer = %d", resp.StatusCode)
	}
	var answer hubHealthAnswer
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("the hub's health answer: %v", err)
	}
	return answer.Executable
}

// hubPid reads the pid the home's run directory names.
func hubPid(t *testing.T, home string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "run", "huddle.pid"))
	if err != nil {
		t.Fatalf("the home's pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		t.Fatalf("the home's pid file names %q", raw)
	}
	return pid
}

// healthHammer polls a router's health path every 50 ms until halted, counting every probe
// that failed: a switch that keeps the router up answers them all.
type healthHammer struct {
	mu       sync.Mutex
	failures int
	stop     chan struct{}
	done     chan struct{}
}

func startHealthHammer(port int) *healthHammer {
	h := &healthHammer{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(h.done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		client := &http.Client{Timeout: 2 * time.Second}
		for {
			select {
			case <-h.stop:
				return
			case <-ticker.C:
			}
			resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/huddle-router/health", port))
			if err != nil {
				h.count()
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				h.count()
			}
		}
	}()
	return h
}

func (h *healthHammer) count() {
	h.mu.Lock()
	h.failures++
	h.mu.Unlock()
}

// halt stops the hammer and answers how many probes failed.
func (h *healthHammer) halt() int {
	close(h.stop)
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failures
}

// startChild starts one child of the test whose output lands on log. It answers the child's pid
// and an exited channel a reaper closes: the pid's own liveness outlives its exit as a zombie,
// so the exit is the test's evidence. Cleanup signals a survivor.
func startChild(t *testing.T, bin string, env map[string]string, dir string, log io.Writer, args ...string) (int, <-chan struct{}) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = argvEntries(env)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
			return
		default:
		}
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
			<-exited
		}
	})
	return cmd.Process.Pid, exited
}

// servesOf counts this machine's huddle serve processes started from either binary.
func servesOf(t *testing.T, binA, binB string) int {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "command=").Output()
	if err != nil {
		t.Fatalf("list the processes: %v", err)
	}
	serves := 0
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, " serve") && (strings.Contains(line, binA) || strings.Contains(line, binB)) {
			serves++
		}
	}
	return serves
}

func TestE2EHubUpdateUpFromANewBinaryReplacesTheHubAndKeepsTheRouter(t *testing.T) {
	root := t.TempDir()
	routerHome := shortRouterHome(t)
	hubPort := ports.Free(t)
	routerPort := ports.Free(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	binA := filepath.Join(root, "bin-a", "huddle")
	binB := filepath.Join(root, "bin-b", "huddle")
	copyBin(t, binA)
	copyBin(t, binB)
	hubHome := filepath.Join(root, "huddle-home")
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	base := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+hubHome,
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_TOKEN=hub-update-e2e-root",
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_CHANNEL=hub-update",
		"HUDDLE_AS=session",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=hub-update-e2e-key",
	))
	// the old build's hub hosts no router (the 0.0.1 machine's shape): its process names a
	// router home with no providers in it, so its serve waits for providers that never come
	// and the standalone keeps the router
	envA := argvLayer(base, map[string]string{"HUDDLE_ROUTER_HOME": filepath.Join(root, "router-home-a")})
	pids := newPidWatch(t)
	routerLog, err := os.OpenFile(filepath.Join(routerHome, "e2e.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = routerLog.Close() })
	hubLog := func() string {
		raw, err := os.ReadFile(filepath.Join(hubHome, "run", "huddle.log"))
		if err != nil {
			return ""
		}
		return string(raw)
	}

	// the standalone router beside the hub, the way the owner's machine runs it
	standalone, standaloneExited := startChild(t, binA, base, root, routerLog, "router", "serve")
	pids.see(standalone)
	until(t, 15*time.Second, func() bool { return routerHealthy(routerPort) },
		"the standalone router to answer its health path")

	// the old binary's hub; the hub's answer names its executable resolved, so the comparison
	// resolves the copies too
	code, out, errOut := runBin(t, binA, envA, root, "up", "--port", strconv.Itoa(hubPort))
	if code != 0 {
		t.Fatalf("up from the old binary = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	exeA, err := filepath.EvalSymlinks(binA)
	if err != nil {
		t.Fatal(err)
	}
	exeB, err := filepath.EvalSymlinks(binB)
	if err != nil {
		t.Fatal(err)
	}
	oldPID := hubPid(t, hubHome)
	pids.see(oldPID)
	if got := hubExecutableAt(t, hubPort); got != exeA {
		t.Fatalf("the hub names executable %q, want %q", got, exeA)
	}

	hammered := startHealthHammer(routerPort)

	// up from the new binary
	code, out, errOut = runBin(t, binB, base, root, "up", "--port", strconv.Itoa(hubPort))
	if code != 0 {
		t.Fatalf("up from the new binary = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	updated := fmt.Sprintf("Huddle updated at http://127.0.0.1:%d (pid %d → ", hubPort, oldPID)
	if !strings.Contains(out, updated) {
		t.Errorf("up from the new binary = %q, want it to say %q", out, updated)
	}
	newPID := hubPid(t, hubHome)
	if newPID == oldPID {
		t.Fatalf("the pid file still names %d after the update", oldPID)
	}
	pids.see(newPID)
	if syscall.Kill(newPID, 0) != nil {
		t.Errorf("the new hub (pid %d) is not running", newPID)
	}
	if syscall.Kill(oldPID, 0) == nil {
		t.Errorf("the old hub (pid %d) still runs 0 ms after the update said it went", oldPID)
	}
	if got := hubExecutableAt(t, hubPort); got != exeB {
		t.Errorf("the hub names executable %q, want the new binary %q", got, exeB)
	}

	// the new hub takes the standalone's socket over: one line in the hub's log, then the
	// standalone drains and exits
	until(t, 15*time.Second, func() bool {
		return strings.Contains(hubLog(), fmt.Sprintf("huddle-router: taken over from pid %d", standalone))
	}, "the new hub to take the router over")
	select {
	case <-standaloneExited:
	case <-time.After(15 * time.Second):
		t.Errorf("the standalone router (pid %d) still runs 15 s after the takeover", standalone)
		_ = syscall.Kill(standalone, syscall.SIGKILL)
		<-standaloneExited
	}
	if failures := hammered.halt(); failures != 0 {
		t.Errorf("%d router health probes failed across the switch", failures)
	}
	if status := postModel(t, routerPort); status != 200 {
		t.Errorf("a glm request after the switch = %d, want 200", status)
	}
	if serves := servesOf(t, binA, binB); serves != 1 {
		t.Errorf("%d huddle serve processes remain for the two binaries, want exactly one", serves)
	}
}
