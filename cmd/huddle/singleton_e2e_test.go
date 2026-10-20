package main

// One home, one server: five `huddle serve` starts race for one temp home, exactly one serves
// and the other four say so and exit 0. Every process here is this test's child, every home a
// temp one, and the survivor dies with the test.

import (
	"bytes"
	"errors"
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
)

// TestE2EOneHuddleServerPerHome starts five servers on one home at once: the serve lock leaves
// one serving and the four losers answer `already serves this home` on stderr with exit 0.
func TestE2EOneHuddleServerPerHome(t *testing.T) {
	root := t.TempDir()
	homeDir := filepath.Join(root, "home")
	dataDir := filepath.Join(root, "data")
	ports := freePorts(t, 5)
	env := map[string]string{
		"PATH":                   os.Getenv("PATH"),
		"HOME":                   filepath.Join(root, "user"),
		"CLAUDE_CONFIG_DIR":      filepath.Join(root, "claude"),
		"XDG_CONFIG_HOME":        filepath.Join(root, "xdg-config"),
		"XDG_STATE_HOME":         filepath.Join(root, "xdg-state"),
		"XDG_DATA_HOME":          filepath.Join(root, "xdg-data"),
		"RADAR_HOME":             filepath.Join(root, "radar"),
		"HUDDLE_HOME":            homeDir,
		"HUDDLE_DATA":            dataDir,
		"HUDDLE_TOKEN":           "e2e-singleton-root",
		"HUDDLE_NO_PROJECT_CRED": "1",
	}
	starts := 5
	cmds := make([]*exec.Cmd, starts)
	exited := make([]*startExit, starts)
	stderr := make([]*bytes.Buffer, starts)
	for i := 0; i < starts; i++ {
		cmd := exec.Command(huddleBin, "serve")
		cmd.Env = argvEntries(argvLayer(env, map[string]string{"PORT": strconv.Itoa(ports[i])}))
		out := &bytes.Buffer{}
		errOut := &bytes.Buffer{}
		cmd.Stdout = out
		cmd.Stderr = errOut
		stderr[i] = errOut
		state := &startExit{}
		exited[i] = state
		cmds[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatalf("start server %d: %v", i, err)
		}
		go func(cmd *exec.Cmd, state *startExit) {
			err := cmd.Wait()
			code := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = exit.ExitCode()
			} else if err != nil {
				code = -1
			}
			state.set(code)
		}(cmd, state)
	}
	t.Cleanup(func() {
		for i, cmd := range cmds {
			if exited[i].done() {
				continue
			}
			_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
			deadline := time.Now().Add(5 * time.Second)
			for !exited[i].done() && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			if !exited[i].done() {
				_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
				for !exited[i].done() {
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
	})

	healthy := func(port int) bool {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode < 300
	}
	survivor := -1
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		alive, last := 0, -1
		for i := range exited {
			if !exited[i].done() {
				alive, last = alive+1, i
			}
		}
		if alive == 1 && healthy(ports[last]) {
			survivor = last
			break
		}
		if alive == 0 {
			t.Fatalf("every start exited; the losers said:\n%s", allStderr(stderr))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if survivor < 0 {
		t.Fatalf("no single server left standing within 10 s; the losers said:\n%s", allStderr(stderr))
	}
	if !healthy(ports[survivor]) {
		t.Errorf("the survivor does not answer health on port %d", ports[survivor])
	}
	for i := range exited {
		if i == survivor {
			continue
		}
		if code := exited[i].get(); code != 0 {
			t.Errorf("start %d exited %d, want 0", i, code)
		}
		if !strings.Contains(stderr[i].String(), "already serves this home") {
			t.Errorf("start %d stderr = %q, want `already serves this home`", i, stderr[i].String())
		}
	}
}

// startExit records one child's end: its exit code once Wait has reaped it.
type startExit struct {
	mu   sync.Mutex
	code int
	over bool
}

func (s *startExit) set(code int) { s.mu.Lock(); s.code, s.over = code, true; s.mu.Unlock() }

func (s *startExit) done() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.over }

func (s *startExit) get() int { s.mu.Lock(); defer s.mu.Unlock(); return s.code }

// allStderr joins the starts' stderr for a failure message.
func allStderr(stderr []*bytes.Buffer) string {
	lines := make([]string, 0, len(stderr))
	for i, buf := range stderr {
		lines = append(lines, strconv.Itoa(i)+": "+strings.TrimSpace(buf.String()))
	}
	return strings.Join(lines, "\n")
}

// freePorts answers n distinct loopback ports free at once: all n are bound together, so the OS
// never hands one out twice, then released for the children.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	listeners := make([]net.Listener, n)
	ports := make([]int, n)
	for i := range listeners {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("find a free port: %v", err)
		}
		listeners[i] = listener
		ports[i] = listener.Addr().(*net.TCPAddr).Port
	}
	for _, listener := range listeners {
		_ = listener.Close()
	}
	return ports
}
