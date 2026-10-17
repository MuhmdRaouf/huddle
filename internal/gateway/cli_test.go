package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The command line: bare serves in the foreground and cleans its files up on the way out; start
// follows a detached child's files; stop signals only a process that is a gateway; status reads
// the record. The lifecycle's effects are fakes here; the health probe is the real one, against
// a loopback server the test started.

// fakeWorld is a run's effects, with the calls the test wants to look at.
type fakeWorld struct {
	env       domain.EnvLookup
	spawned   [][]string
	delivered []int64
	// spawn runs inside the fake detached start, as the child would: writing the files it writes.
	spawn   func()
	command string
	alive   bool
}

func (f *fakeWorld) options() RunOptions {
	return RunOptions{
		Env: f.env,
		SpawnDetached: func(args []string) int64 {
			f.spawned = append(f.spawned, args)
			if f.spawn != nil {
				f.spawn()
			}
			return 4242
		},
		Command:     func(int64) (string, bool) { return f.command, f.command != "" },
		IsAlive:     func(int64) bool { return f.alive },
		Deliver:     func(pid int64) bool { f.delivered = append(f.delivered, pid); return true },
		FetchHealth: fetchHealth,
	}
}

// healthServer answers /health the way the gateway does, on a loopback port of its own.
func healthServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// portOf is a loopback server's port.
func portOf(server *httptest.Server) int {
	trimmed := strings.TrimPrefix(server.URL, "http://")
	port, err := strconv.Atoi(trimmed[strings.LastIndex(trimmed, ":")+1:])
	if err != nil {
		return 0
	}
	return port
}

// deadPort is a port the machine held for a moment and holds no longer: nothing answers there.
func deadPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func TestRunUsageAndHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"bogus"}, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_HOME": t.TempDir()}}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), usage) {
		t.Errorf("stderr = %q, want the usage line", stderr.String())
	}
	stdout, stderr = bytes.Buffer{}, bytes.Buffer{}
	if code := Run([]string{"help"}, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_HOME": t.TempDir()}}); code != 0 {
		t.Errorf("help exit = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "gateway.port") {
		t.Errorf("help stdout = %q, want it to name the port file", stdout.String())
	}
}

func TestStatusAndStopWithoutARun(t *testing.T) {
	env := domain.EnvLookup{"HUDDLE_HOME": t.TempDir()}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"status"}, &stdout, &stderr, RunOptions{Env: env}); code != 3 {
		t.Errorf("status exit = %d, want 3", code)
	}
	if strings.TrimSpace(stdout.String()) != "gateway: not running" {
		t.Errorf("status stdout = %q, want the not-running line", stdout.String())
	}

	stdout, stderr = bytes.Buffer{}, bytes.Buffer{}
	if code := Run([]string{"stop"}, &stdout, &stderr, RunOptions{Env: env}); code != 0 {
		t.Errorf("stop exit = %d, want 0", code)
	}
	if strings.TrimSpace(stdout.String()) != "gateway: not running" {
		t.Errorf("stop stdout = %q, want the not-running line", stdout.String())
	}
}

func TestStartFollowsTheDetachedChild(t *testing.T) {
	health := healthServer(t)
	world := &fakeWorld{env: domain.EnvLookup{"HUDDLE_HOME": t.TempDir()}}
	world.spawn = func() {
		// the child: serve, write its files, listen
		if err := writeState(world.env["HUDDLE_HOME"], portOf(health)); err != nil {
			t.Error(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"start"}, &stdout, &stderr, world.options()); code != 0 {
		t.Fatalf("start exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "gateway: "+health.URL {
		t.Errorf("start stdout = %q, want %q", got, "gateway: "+health.URL)
	}
	if len(world.spawned) != 1 || world.spawned[0][0] != "gateway" {
		t.Errorf("spawned %v, want one bare gateway run", world.spawned)
	}

	// a second start reuses the healthy record and spawns nothing
	world.spawn = nil
	world.alive = true
	stdout = bytes.Buffer{}
	if code := Run([]string{"start"}, &stdout, &stderr, world.options()); code != 0 {
		t.Fatalf("second start exit = %d, want 0", code)
	}
	if len(world.spawned) != 1 {
		t.Errorf("second start spawned %d children, want none: the record was healthy", len(world.spawned))
	}
}

func TestStartFailsWhenTheChildNeverListens(t *testing.T) {
	world := &fakeWorld{env: domain.EnvLookup{"HUDDLE_HOME": t.TempDir()}}
	world.spawn = func() {} // the child writes nothing and dies
	opts := world.options()
	opts.Sleep = func(int64) {} // no real waiting in the test
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"start"}, &stdout, &stderr, opts); code != 1 {
		t.Errorf("start exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "healthy") {
		t.Errorf("stderr = %q, want the failed start named", stderr.String())
	}
}

func TestStartRefusesAnInvalidConfig(t *testing.T) {
	home := t.TempDir()
	bad := filepath.Join(home, "gateway.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"start"}, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_HOME": home}})
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), bad) {
		t.Errorf("stderr = %q, want it to name %s", stderr.String(), bad)
	}
}

func TestStopSignalsOnlyAGateway(t *testing.T) {
	home := t.TempDir()
	env := domain.EnvLookup{"HUDDLE_HOME": home}
	health := healthServer(t)
	if err := writeState(home, portOf(health)); err != nil {
		t.Fatal(err)
	}

	world := &fakeWorld{env: env, alive: true, command: "/usr/local/bin/huddle gateway"}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"stop"}, &stdout, &stderr, world.options()); code != 0 {
		t.Fatalf("stop exit = %d, want 0", code)
	}
	// writeState records the writing process, here the test's own
	if len(world.delivered) != 1 || world.delivered[0] != int64(os.Getpid()) {
		t.Errorf("signalled %v, want the recorded pid %d once", world.delivered, os.Getpid())
	}
	if _, err := os.Stat(filepath.Join(home, stateFile)); !os.IsNotExist(err) {
		t.Errorf("the state file outlived the stop (err %v)", err)
	}

	// a pid that names another program is never signalled, but the record still goes
	if err := writeState(home, portOf(health)); err != nil {
		t.Fatal(err)
	}
	world = &fakeWorld{env: env, alive: true, command: "/usr/sbin/sshd -D"}
	stdout, stderr = bytes.Buffer{}, bytes.Buffer{}
	if code := Run([]string{"stop"}, &stdout, &stderr, world.options()); code != 0 {
		t.Fatalf("stop exit = %d, want 0", code)
	}
	if len(world.delivered) != 0 {
		t.Errorf("signalled %v, want nothing: the pid runs another program", world.delivered)
	}
	if _, err := os.Stat(filepath.Join(home, stateFile)); !os.IsNotExist(err) {
		t.Errorf("the state file outlived the stop (err %v)", err)
	}
}

func TestStatusReadsAHealthyRecord(t *testing.T) {
	home := t.TempDir()
	health := healthServer(t)
	if err := writeState(home, portOf(health)); err != nil {
		t.Fatal(err)
	}
	world := &fakeWorld{env: domain.EnvLookup{"HUDDLE_HOME": home}, alive: true}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"status"}, &stdout, &stderr, world.options()); code != 0 {
		t.Fatalf("status exit = %d, want 0", code)
	}
	if got := strings.TrimSpace(stdout.String()); got != "gateway: "+health.URL {
		t.Errorf("status stdout = %q, want %q", got, "gateway: "+health.URL)
	}

	// a record whose port nothing answers is not running, alive record or not
	dead := t.TempDir()
	world = &fakeWorld{env: domain.EnvLookup{"HUDDLE_HOME": dead}, alive: true}
	if err := writeState(dead, deadPort(t)); err != nil {
		t.Fatal(err)
	}
	stdout = bytes.Buffer{}
	if code := Run([]string{"status"}, &stdout, &stderr, world.options()); code != 3 {
		t.Errorf("status with a silent port exit = %d, want 3", code)
	}
}

// The bare word serves in the foreground: it takes the first free port of its walk, writes both
// files, answers health, and leaves nothing behind once its signal ends it.
func TestForegroundServesUntilSignalled(t *testing.T) {
	home := t.TempDir()
	env := domain.EnvLookup{"HUDDLE_HOME": home, "RADAR_HOME": t.TempDir()}
	signals := make(chan os.Signal, 1)
	var stdout, stderr bytes.Buffer

	done := make(chan int, 1)
	go func() { done <- Run(nil, &stdout, &stderr, RunOptions{Env: env, Signal: signals}) }()

	deadline := time.Now().Add(5 * time.Second)
	var port int
	for time.Now().Before(deadline) {
		if read, ok := readPortFile(home); ok {
			port = read
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if port == 0 {
		t.Fatalf("no port file appeared (stdout %q, stderr %q)", stdout.String(), stderr.String())
	}
	if port < PortFirst || port > PortLast {
		t.Errorf("port = %d, want one of %d..%d", port, PortFirst, PortLast)
	}
	if state := readState(home); state == nil || state.Port != port {
		t.Errorf("state = %+v, want the run's pid and port %d", state, port)
	}
	response, err := http.Get(gatewayURL(port) + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Errorf("health status = %d, want 200", response.StatusCode)
	}

	signals <- os.Interrupt
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("foreground exit = %d, want 0 (stderr %q)", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the foreground run never ended on its signal")
	}
	for _, name := range []string{portFile, stateFile} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Errorf("%s outlived the run (err %v)", name, err)
		}
	}
}

// A record nothing answers any more is replaced, not reused: the start takes it out and
// brings its own run up in its place.
func TestStartReplacesAStaleRecord(t *testing.T) {
	health := healthServer(t)
	home := t.TempDir()
	world := &fakeWorld{env: domain.EnvLookup{"HUDDLE_HOME": home}}
	world.spawn = func() { // the child: serve, write its files, listen
		if err := writeState(home, portOf(health)); err != nil {
			t.Error(err)
		}
	}
	if err := writeState(home, deadPort(t)); err != nil { // a record whose port holds nothing
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"start"}, &stdout, &stderr, world.options()); code != 0 {
		t.Fatalf("start exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if len(world.spawned) != 1 {
		t.Errorf("spawned %d children, want one: the stale record was replaced", len(world.spawned))
	}
	if state := readState(home); state == nil || state.Port != portOf(health) {
		t.Errorf("state = %+v, want the new run's record", state)
	}
}

// A detached run that could not be started at all fails the start with its own line.
func TestStartFailsWhenNothingCouldBeSpawned(t *testing.T) {
	var stdout, stderr bytes.Buffer
	opts := RunOptions{
		Env:           domain.EnvLookup{"HUDDLE_HOME": t.TempDir()},
		SpawnDetached: func([]string) int64 { return 0 },
		FetchHealth:   fetchHealth,
	}
	if code := Run([]string{"start"}, &stdout, &stderr, opts); code != 1 {
		t.Errorf("start exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "could not start") {
		t.Errorf("stderr = %q, want the failed spawn named", stderr.String())
	}
}

// Every command needs a home: with no HUDDLE_HOME, no XDG_CONFIG_HOME and no HOME, none
// of them can say where its files live, and each says so with exit 1.
func TestEveryCommandNeedsAResolvableHome(t *testing.T) {
	env := domain.EnvLookup{}
	for _, command := range [][]string{nil, {"start"}, {"stop"}, {"status"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(command, &stdout, &stderr, RunOptions{Env: env}); code != 1 {
			t.Errorf("%v: exit = %d, want 1", command, code)
		}
		if !strings.Contains(stderr.String(), "home") {
			t.Errorf("%v: stderr = %q, want the missing home named", command, stderr.String())
		}
	}
}

// The defaults a real run uses, which the tests above swap for fakes: the detached start
// answers nothing without a home, and it runs this binary detached — here with the flags
// that end a test binary before any test runs.
func TestSpawnDetachedWithoutAHomeStartsNothing(t *testing.T) {
	if pid := spawnDetached(domain.EnvLookup{})([]string{"gateway"}); pid != 0 {
		t.Errorf("spawn without a home = pid %d, want 0", pid)
	}
}

func TestSpawnDetachedRunsThisBinaryDetached(t *testing.T) {
	home := t.TempDir()
	pid := spawnDetached(domain.EnvLookup{"HUDDLE_HOME": home})([]string{"-test.run=^$", "-test.count=1"})
	if pid <= 0 {
		t.Fatalf("spawn = pid %d, want a started child", pid)
	}
	if _, err := os.Stat(filepath.Join(home, logFile)); err != nil {
		t.Errorf("the run's log was not opened in the home: %v", err)
	}
	// A detached start never reaps, so this test — the child's parent — does: a reaped
	// child is the proof the flags ended it, and a hung one is killed before it can hold
	// the test to the suite's timeout.
	child, err := os.FindProcess(int(pid))
	if err != nil {
		t.Fatalf("find the child: %v", err)
	}
	waited := make(chan error, 1)
	go func() {
		state, err := child.Wait()
		if err == nil && !state.Success() {
			err = fmt.Errorf("ended by %v", state)
		}
		waited <- err
	}()
	select {
	case err := <-waited:
		if err != nil {
			t.Errorf("the detached child: %v", err)
		}
	case <-time.After(10 * time.Second):
		process.SignalGroup(pid, syscall.SIGKILL)
		<-waited
		t.Errorf("the detached child still runs (pid %d); the flags should have ended it", pid)
	}
}

// The default command probe reads `ps`: it names this process and answers nothing for a
// pid that cannot exist.
func TestCommandOfNamesAProcess(t *testing.T) {
	line, ok := commandOf(int64(os.Getpid()))
	if !ok || strings.TrimSpace(line) == "" {
		t.Errorf("commandOf(%d) = %q, %v; want this process's command line", os.Getpid(), line, ok)
	}
	if line, ok := commandOf(-1); ok || line != "" {
		t.Errorf("commandOf(-1) = %q, %v; want nothing for a pid that cannot exist", line, ok)
	}
}

// The stop's default delivery signals the recorded pid's own group, so the detached
// gateway dies and nothing else does. A child of the test in a group of its own stands
// in for the gateway.
func TestStopSignalsThroughTheDefaultDelivery(t *testing.T) {
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		t.Fatalf("start the stand-in: %v", err)
	}
	pid := int64(child.Process.Pid)
	t.Cleanup(func() {
		process.SignalGroup(pid, syscall.SIGKILL)
		_, _ = child.Process.Wait()
	})
	home := t.TempDir()
	state, err := json.Marshal(runState{Pid: pid, Port: deadPort(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, stateFile), state, 0o600); err != nil {
		t.Fatal(err)
	}
	world := &fakeWorld{env: domain.EnvLookup{"HUDDLE_HOME": home}, alive: true, command: "/usr/local/bin/huddle gateway"}
	opts := world.options()
	opts.Deliver = nil // the default delivery runs
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"stop"}, &stdout, &stderr, opts); code != 0 {
		t.Fatalf("stop exit = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	var exitErr *exec.ExitError
	if err := child.Wait(); !errors.As(err, &exitErr) {
		t.Fatalf("the stand-in did not end to a wait: %v", err)
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
		t.Errorf("the stand-in ended by %v, want a SIGTERM", exitErr)
	}
}

// The bare run's error paths: no home, a port walk with nothing free, and a home whose
// state file cannot be written.
func TestServeReportsWhatFailed(t *testing.T) {
	t.Run("no home", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Run(nil, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{}}); code != 1 {
			t.Errorf("serve exit = %d, want 1", code)
		}
	})
	t.Run("no free port", func(t *testing.T) {
		holdEveryGatewayPort(t)
		var stdout, stderr bytes.Buffer
		if code := Run(nil, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_HOME": t.TempDir()}}); code != 1 {
			t.Errorf("serve exit = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "no free port") {
			t.Errorf("stderr = %q, want the exhausted walk named", stderr.String())
		}
	})
	t.Run("the state file cannot be written", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, stateFile), 0o700); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := Run(nil, &stdout, &stderr, RunOptions{Env: domain.EnvLookup{"HUDDLE_HOME": home}}); code != 1 {
			t.Errorf("serve exit = %d, want 1", code)
		}
	})
}

// holdEveryGatewayPort binds the walk's whole range for the test's duration.
func holdEveryGatewayPort(t *testing.T) {
	t.Helper()
	var held []net.Listener
	t.Cleanup(func() {
		for _, listener := range held {
			_ = listener.Close()
		}
	})
	for port := PortFirst; port <= PortLast; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("port %d is already held by another process: %v", port, err)
		}
		held = append(held, listener)
	}
}

// The port walk takes the first free port; with every port held it says so.
func TestListenPortTakesTheFirstFreePort(t *testing.T) {
	listener, err := listenPort()
	if err != nil {
		t.Fatalf("listenPort: %v", err)
	}
	got := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	if got < PortFirst || got > PortLast {
		t.Fatalf("port = %d, want one of %d..%d", got, PortFirst, PortLast)
	}

	holdEveryGatewayPort(t)
	if l, err := listenPort(); err == nil {
		l.Close()
		t.Error("listenPort found a port in a walk with nothing free")
	} else if !strings.Contains(err.Error(), "no free port") {
		t.Errorf("error = %v, want the exhausted walk named", err)
	}
}

// The two files a run keeps read as absent when they hold anything else.
func TestRunFilesThatHoldGarbageReadAsAbsent(t *testing.T) {
	home := t.TempDir()
	state := filepath.Join(home, stateFile)
	port := filepath.Join(home, portFile)
	cases := []struct{ name, body string }{
		{"not json", "{not json"},
		{"no port", `{"pid":7,"port":0}`},
		{"an empty file", ""},
	}
	for _, tc := range cases {
		if err := os.WriteFile(state, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if readState(home) != nil {
			t.Errorf("%s: readState saw a run in %q", tc.name, tc.body)
		}
	}
	for _, tc := range cases {
		if err := os.WriteFile(port, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := readPortFile(home); ok {
			t.Errorf("%s: readPortFile took %q as a port", tc.name, tc.body)
		}
	}
	if err := os.WriteFile(port, []byte(" 18877 \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := readPortFile(home); !ok || got != 18877 {
		t.Errorf("readPortFile = %d, %v; want 18877, true", got, ok)
	}
}

// A home that cannot be made, or one whose state file's name is taken by a directory,
// fails the write that lays both files down.
func TestWriteStateRefusesAnUnwritableHome(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeState(filepath.Join(blocker, "home"), PortFirst); err == nil {
		t.Error("writeState made a home under a file")
	}
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, stateFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeState(home, PortFirst); err == nil {
		t.Error("writeState wrote over a directory named as its state file")
	}
}

// The foreground run records its route lines the way the detached one does: one spool
// line per request under $RADAR_HOME, read back by radar's own ingester. This one's
// upstream is a dead port, so the line carries the failure.
func TestForegroundRecordsTheRoute(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "gateway.json"), []byte(`{"anthropic":"http://127.0.0.1:1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := domain.EnvLookup{"HUDDLE_HOME": home, "RADAR_HOME": t.TempDir()}
	signals := make(chan os.Signal, 1)
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- Run(nil, &stdout, &stderr, RunOptions{Env: env, Signal: signals}) }()

	deadline := time.Now().Add(5 * time.Second)
	var port int
	for time.Now().Before(deadline) {
		if read, ok := readPortFile(home); ok {
			port = read
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if port == 0 {
		t.Fatalf("no port file appeared (stdout %q, stderr %q)", stdout.String(), stderr.String())
	}
	response, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port), "application/json", strings.NewReader(messageBody))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502: the upstream is a dead port", response.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spoolDeadline := time.Now().Add(3 * time.Second)
	var lines []shared.SpoolLine
	for time.Now().Before(spoolDeadline) {
		lines = ingest.ReadSpoolLines(ingest.ListSpoolFiles(ctx, env, time.Now().UnixMilli(), 30))
		if len(lines) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(lines) != 1 {
		t.Fatalf("radar's spool holds %d lines, want the one route line", len(lines))
	}
	line := lines[0]
	if line.Event != "route" || line.Model == nil || *line.Model != "glm-5.3" {
		t.Fatalf("line = event %q model %v, want a route line on glm-5.3", line.Event, line.Model)
	}
	if line.Error == nil || !strings.Contains(*line.Error, "upstream unreachable") {
		t.Errorf("error = %v, want the dead upstream named", line.Error)
	}

	signals <- os.Interrupt
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("foreground exit = %d, want 0 (stderr %q)", code, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the foreground run never ended on its signal")
	}
}
