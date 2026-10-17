package gateway

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
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
