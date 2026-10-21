package main

// The legacy ports end to end on the built binary: a session started before the move keeps its
// proxy pointed at a 0.0.1 port; the daemon stops the 0.0.1 router and its watcher that hold it
// and answers there itself, with claude-* still reaching Anthropic. Every process here is this
// test's child, every home a temp one, and the legacy port a free one from the test override.

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
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

// buildLegacyFakeBin builds the fake 0.0.1 router program the routerd tests use.
func buildLegacyFakeBin(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("..", "..", "internal", "routerd", "testdata", "fakelegacy", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "fakelegacy")
	build := exec.Command("go", "build", "-o", bin, source)
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build the fake legacy router: %v", err)
	}
	return bin
}

// startLegacyChild starts one fake process in env and answers its exit watcher and its output:
// what the fake printed, stable to read once done closes. Cleanup ends whatever the daemon did
// not.
func startLegacyChild(t *testing.T, bin string, env []string, dir string, args ...string) (int, <-chan struct{}, func() string) {
	t.Helper()
	output := &bytes.Buffer{}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the fake %v: %v", args, err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return cmd.Process.Pid, done, output.String
}

// awaitFakeListening fails when the fake dies before the port answers, naming its output.
func awaitFakeListening(t *testing.T, port int, done <-chan struct{}, output func() string, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("%s died before %d answered:\n%s", what, port, output())
		default:
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never made %d answer within five seconds:\n%s", what, port, output())
}

func TestE2ELegacyPortsTheDaemonServesTheOldPorts(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "user")
	state := filepath.Join(home, ".agents", "zai")
	routerHome := shortRouterHome(t)
	routerPort := ports.Free(t)
	legacy := ports.Free(t)
	anthropic := newSettingsFake(t, "anthropic")
	zaiUp := newSettingsFake(t, "zai")
	fakeBin := buildLegacyFakeBin(t)
	env := argvEntries(isoenv.Lookup(isoenv.Environ(t, home,
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_LEGACY_PORTS=zai="+strconv.Itoa(legacy),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.server.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=legacy-e2e-key",
	)))
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}

	// the 0.0.1 zai router holding the legacy port, its pid record beside it, and its watcher
	// named in a guard file — the generation a pre-move session keeps alive
	runPid, runDone, runOut := startLegacyChild(t, fakeBin, env, root, "zai", "router", "run", strconv.Itoa(legacy))
	awaitFakeListening(t, legacy, runDone, runOut, "the 0.0.1 router")
	record := fmt.Sprintf(`{"pid":%d,"port":%d,"token":"legacy-e2e","mode":"router"}`, runPid, legacy)
	if err := os.WriteFile(filepath.Join(state, "router.pid"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	watchPid, watchDone, watchOut := startLegacyChild(t, fakeBin, env, root, "zai", "router", "watch")
	if err := os.WriteFile(filepath.Join(state, "watch."+strconv.Itoa(watchPid)),
		[]byte(strconv.Itoa(watchPid)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// the daemon serves
	logFile, err := os.OpenFile(filepath.Join(routerHome, "e2e.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	serve := exec.Command(huddleBin, "router", "serve")
	serve.Env = env
	serve.Dir = root
	serve.Stdout, serve.Stderr = logFile, logFile
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan struct{})
	go func() {
		_ = serve.Wait()
		close(serveDone)
	}()
	t.Cleanup(func() {
		_ = serve.Process.Signal(syscall.SIGTERM)
		select {
		case <-serveDone:
		case <-time.After(10 * time.Second):
			_ = serve.Process.Kill()
			<-serveDone
		}
		_ = logFile.Close()
	})

	// within twelve seconds the 0.0.1 generation is gone and the daemon answers on the port
	daemonLog := func() string {
		raw, err := os.ReadFile(filepath.Join(routerHome, "router.log"))
		if err != nil {
			return "(" + err.Error() + ")"
		}
		return string(raw)
	}
	deadline := time.Now().Add(12 * time.Second)
	for _, wait := range [...]struct {
		name string
		pid  int
		done <-chan struct{}
		out  func() string
	}{
		{"the 0.0.1 router", runPid, runDone, runOut},
		{"its watcher", watchPid, watchDone, watchOut},
	} {
		select {
		case <-wait.done:
		case <-time.After(time.Until(deadline)):
			t.Fatalf("%s (pid %d) still runs 12 s after the daemon's start; its output:\n%s; the daemon's log:\n%s",
				wait.name, wait.pid, wait.out(), daemonLog())
		}
	}
	awaitLegacyHealthE2E(t, legacy)

	// a session pointed at the legacy port gets its answers: claude-* reaches Anthropic
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/messages", legacy), "application/json",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
	if err != nil {
		t.Fatalf("post claude-sonnet-4-5 to the legacy port: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("claude-sonnet-4-5 on the legacy port = %d %q", resp.StatusCode, string(body))
	}
	if anthropic.requests() == 0 {
		t.Error("the fake Anthropic saw no request")
	}
}

// awaitLegacyHealthE2E waits until the port answers huddle-router's health.
func awaitLegacyHealthE2E(t *testing.T, port int) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/huddle-router/health", port))
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(raw), `"huddle-router"`) {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("port %d never answered huddle-router's health", port)
}
