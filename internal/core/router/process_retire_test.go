// The stop and retire helpers' behaviour: only a pid running one of our routers is ever
// signalled, a stale record just goes, and a retired record keeps its line while its process
// lives.
package router

import (
	"context"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// retireEnv is one throwaway state root and the env the stop reads.
func retireEnv(t *testing.T) (domain.EnvLookup, string) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "zai-state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	return domain.EnvLookup{
		"HOME":               root,
		"XDG_CONFIG_HOME":    filepath.Join(root, "xdg-config"),
		"XDG_STATE_HOME":     filepath.Join(root, "xdg-state"),
		"HUDDLE_ROUTER_PORT": strconv.Itoa(ports.Free(t)),
		"ZAI_STATE_DIR":      state,
	}, state
}

// sleeper runs `sh <script> args...`, a live process whose ps command line ends in the given
// words; Cleanup ends it whatever the test concludes.
func sleeper(t *testing.T, args ...string) (pid int64, done <-chan struct{}) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "sleeper.sh")
	if err := os.WriteFile(script, []byte("trap 'exit 0' TERM\nwhile :; do sleep 0.2; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	go func() { _ = cmd.Wait(); close(finished) }()
	t.Cleanup(func() {
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

func TestStopRouterWithoutRecords(t *testing.T) {
	env, state := retireEnv(t)
	line := StopRouter(context.Background(), StopOptions{Provider: routerProvider, Env: env, StateRoot: state})
	if line != "stopped (was not running)" {
		t.Fatalf("line = %q", line)
	}
}

func TestStopRouterStaleRecordGoes(t *testing.T) {
	env, state := retireEnv(t)
	record := PidRecord{Pid: 999999999, Port: int64(ports.Free(t)), StartedAt: "t", Node: "n",
		Version: "v", FrontVersion: 3, Mode: PidModeRouter, Token: "x"}
	if err := writeTestPidRecord(state, record); err != nil {
		t.Fatal(err)
	}
	line := StopRouter(context.Background(), StopOptions{Provider: routerProvider, Env: env, StateRoot: state})
	if !strings.Contains(line, "not our router") {
		t.Fatalf("line = %q, want the stale-record line", line)
	}
	if _, err := os.Stat(filepath.Join(state, "router.pid")); !os.IsNotExist(err) {
		t.Errorf("the stale record is still there: %v", err)
	}
}

func TestStopRouterStopsALiveRouterOfOurs(t *testing.T) {
	env, state := retireEnv(t)
	port := ports.Free(t)
	pid, done := sleeper(t, "zai", "router", "run")
	if err := writeTestPidRecord(state, PidRecord{Pid: pid, Port: int64(port),
		StartedAt: "t", Node: "n", Version: "v", FrontVersion: 3, Mode: PidModeRouter, Token: "x"}); err != nil {
		t.Fatal(err)
	}
	line := StopRouter(context.Background(), StopOptions{Provider: routerProvider, Env: env, StateRoot: state})
	if want := "stopped (pid " + itoa(pid) + ")"; line != want {
		t.Fatalf("line = %q, want %q", line, want)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("pid %d is still running", pid)
	}
}

func TestStopRouterLeavesAForeignPidAlone(t *testing.T) {
	env, state := retireEnv(t)
	port := ports.Free(t)
	pid, done := sleeper(t, "sleep", "30")
	if err := writeTestPidRecord(state, PidRecord{Pid: pid, Port: int64(port),
		StartedAt: "t", Node: "n", Version: "v", FrontVersion: 3, Mode: PidModeRouter, Token: "x"}); err != nil {
		t.Fatal(err)
	}
	line := StopRouter(context.Background(), StopOptions{Provider: routerProvider, Env: env, StateRoot: state})
	if !strings.Contains(line, "not our router") {
		t.Fatalf("line = %q, want the foreign-pid line", line)
	}
	select {
	case <-done:
		t.Fatalf("the foreign pid %d was signalled", pid)
	case <-time.After(700 * time.Millisecond):
	}
}

func TestRetireRouterRetiresALiveRouterOfOurs(t *testing.T) {
	env, state := retireEnv(t)
	// The fake 0.0.1 router answers its retire control endpoint with the record's token, and a
	// live process of ours (this test's own sleeper) is the pid the record names.
	retireCalls := make(chan bool, 1)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == routerProvider.Router.HealthPath+"/retire" {
			if r.Header.Get(TokenHeader) == "watch-me-retire" {
				retireCalls <- true
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(old.Close)
	oldURL, err := url.Parse(old.URL)
	if err != nil {
		t.Fatal(err)
	}
	oldPort, err := strconv.Atoi(oldURL.Port())
	if err != nil {
		t.Fatal(err)
	}
	pid, done := sleeper(t, "zai", "router")
	if err := writeTestPidRecord(state, PidRecord{Pid: pid, Port: int64(oldPort),
		StartedAt: "t", Node: "n", Version: "v", FrontVersion: 3, Mode: PidModeRouter,
		Token: "watch-me-retire"}); err != nil {
		t.Fatal(err)
	}
	line := RetireRouter(context.Background(), RetireOptions{StopOptions{Provider: routerProvider, Env: env, StateRoot: state}})
	select {
	case <-retireCalls:
	case <-time.After(5 * time.Second):
		t.Fatalf("the retire call never went out; line = %q", line)
	}
	if !strings.Contains(line, "retired (pid "+itoa(pid)+")") {
		t.Fatalf("line = %q, want the retired line", line)
	}
	if _, err := os.Stat(filepath.Join(state, "router.pid")); !os.IsNotExist(err) {
		t.Errorf("router.pid is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "router.retired")); err != nil {
		t.Errorf("router.retired: %v", err)
	}
	select {
	case <-done:
		t.Fatalf("the retired router pid %d was stopped, want it left running", pid)
	case <-time.After(700 * time.Millisecond):
	}
}

// itoa is strconv.FormatInt for the stop lines.
func itoa(value int64) string { return strconv.FormatInt(value, 10) }
