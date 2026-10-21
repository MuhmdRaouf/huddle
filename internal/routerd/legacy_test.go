package routerd

// The legacy ports' rows: the map legacyPorts answers, and the stops stopLegacy makes on a port
// a 0.0.1 router or one of its watchers holds — over the fake router program the tests build.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestLegacyPortsRows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  domain.EnvLookup
		want map[string]int
	}{
		{"the five 0.0.1 ports on the default port", domain.EnvLookup{},
			map[string]int{"zai": 18787, "kimi": 18788, "deepseek": 18789, "minimax": 18790, "qwen": 18791}},
		{"no legacy port on another port", domain.EnvLookup{"HUDDLE_ROUTER_PORT": "22222"}, nil},
		{"the override is the map", domain.EnvLookup{"HUDDLE_ROUTER_LEGACY_PORTS": "zai=40001,kimi=40002"},
			map[string]int{"zai": 40001, "kimi": 40002}},
		{"an empty override turns adoption off", domain.EnvLookup{"HUDDLE_ROUTER_LEGACY_PORTS": ""}, nil},
		{"a malformed pair is skipped", domain.EnvLookup{"HUDDLE_ROUTER_LEGACY_PORTS": "zai=40001,bogus,qwen=abc"},
			map[string]int{"zai": 40001}},
		{"the override wins on another port", domain.EnvLookup{"HUDDLE_ROUTER_PORT": "22222", "HUDDLE_ROUTER_LEGACY_PORTS": "zai=40001"},
			map[string]int{"zai": 40001}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := legacyPorts(test.env)
			if len(got) != len(test.want) {
				t.Fatalf("legacyPorts = %v, want %v", got, test.want)
			}
			for name, port := range test.want {
				if got[name] != port {
					t.Errorf("legacyPorts[%q] = %d, want %d", name, got[name], port)
				}
			}
		})
	}
}

// buildFakeLegacy builds the fake 0.0.1 router program the tests start.
func buildFakeLegacy(t *testing.T) string {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("testdata", "fakelegacy", "main.go"))
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

// fakeLegacyProcess is one fake 0.0.1 process the test owns: its command line names it and done
// closes once it was reaped, whatever ended it.
type fakeLegacyProcess struct {
	pid  int64
	done <-chan struct{}
}

// startFakeLegacy starts the fake as the test's child: `<bin> <name> router <role> [port]`,
// holding port when one is given. Its environment is a temp home of its own, so no process of
// this suite ever carries the real one.
func startFakeLegacy(tb testing.TB, bin, name, role string, port int) fakeLegacyProcess {
	tb.Helper()
	args := []string{name, "router", role}
	if port != 0 {
		args = append(args, strconv.Itoa(port))
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = []string{"HOME=" + tb.TempDir()}
	if err := cmd.Start(); err != nil {
		tb.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	tb.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return fakeLegacyProcess{pid: int64(cmd.Process.Pid), done: done}
}

// awaitExit fails once the process is still running after ten seconds.
func awaitExit(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s is still running", what)
	}
}

// awaitListening fails once nothing answers on the port within five seconds.
func awaitListening(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if portAccepts(port) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listens on %d", port)
}

// writeLegacyRecord writes a 0.0.1 pid-shaped record into state under name.
func writeLegacyRecord(t *testing.T, state, name string, pid int64, port int) {
	t.Helper()
	body := fmt.Sprintf(`{"pid":%d,"port":%d,"token":"legacy","mode":"router"}`+"\n", pid, port)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeGuard writes a watcher guard naming pid, the way a 0.0.1 watch writes one.
func writeGuard(t *testing.T, state string, pid int64) string {
	t.Helper()
	path := filepath.Join(state, "watch."+strconv.FormatInt(pid+7, 10))
	if err := os.WriteFile(path, []byte(strconv.FormatInt(pid, 10)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStopLegacyRows(t *testing.T) {
	t.Parallel()
	t.Run("a 0.0.1 router named in the pid record is stopped and its port freed", func(t *testing.T) {
		t.Parallel()
		bin := buildFakeLegacy(t)
		port := daemonFreePort(t)
		fake := startFakeLegacy(t, bin, "zai", "run", port)
		awaitListening(t, port)
		state := t.TempDir()
		writeLegacyRecord(t, state, "router.pid", fake.pid, port)
		var lines []string
		stopped := stopLegacy(context.Background(), domain.EnvLookup{"ZAI_STATE_DIR": state}, "zai", port,
			func(line string) { lines = append(lines, line) })
		if !stopped {
			t.Fatal("stopLegacy = false, want true")
		}
		awaitExit(t, fake.done, "the fake router")
		if portAccepts(port) {
			t.Error("the port still answers")
		}
		want := fmt.Sprintf("huddle-router: stopped the 0.0.1 zai router (pid %d) and took port %d", fake.pid, port)
		if !contains(lines, want) {
			t.Errorf("the log says %q, want it to name %q", lines, want)
		}
	})

	t.Run("a retired record stops the router it names", func(t *testing.T) {
		t.Parallel()
		bin := buildFakeLegacy(t)
		port := daemonFreePort(t)
		fake := startFakeLegacy(t, bin, "zai", "run", port)
		awaitListening(t, port)
		state := t.TempDir()
		writeLegacyRecord(t, state, "router.retired", fake.pid, port)
		stopped := stopLegacy(context.Background(), domain.EnvLookup{"ZAI_STATE_DIR": state}, "zai", port, func(string) {})
		if !stopped {
			t.Fatal("stopLegacy = false, want true")
		}
		awaitExit(t, fake.done, "the fake router")
	})

	t.Run("a pid record naming an unrelated process stops nothing", func(t *testing.T) {
		t.Parallel()
		port := daemonFreePort(t)
		state := t.TempDir()
		foreign, foreignDone := sleeperWith(t, "sleep", "30")
		writeLegacyRecord(t, state, "router.pid", foreign, port)
		stopped := stopLegacy(context.Background(), domain.EnvLookup{"ZAI_STATE_DIR": state}, "zai", port, func(string) {})
		if stopped {
			t.Error("stopLegacy = true, want false")
		}
		select {
		case <-foreignDone:
			t.Fatalf("the unrelated pid %d was signalled", foreign)
		case <-time.After(500 * time.Millisecond):
		}
		if !processIsAlive(foreign) {
			t.Fatalf("the unrelated pid %d did not survive", foreign)
		}
	})

	t.Run("a record naming another port stops nothing", func(t *testing.T) {
		t.Parallel()
		bin := buildFakeLegacy(t)
		port := daemonFreePort(t)
		fake := startFakeLegacy(t, bin, "zai", "run", port)
		awaitListening(t, port)
		state := t.TempDir()
		writeLegacyRecord(t, state, "router.pid", fake.pid, port+1)
		stopped := stopLegacy(context.Background(), domain.EnvLookup{"ZAI_STATE_DIR": state}, "zai", port, func(string) {})
		if stopped {
			t.Error("stopLegacy = true, want false")
		}
		select {
		case <-fake.done:
			t.Fatalf("the router pid %d was signalled over another port's record", fake.pid)
		case <-time.After(500 * time.Millisecond):
		}
	})

	t.Run("a stale pid record stops nothing", func(t *testing.T) {
		t.Parallel()
		port := daemonFreePort(t)
		state := t.TempDir()
		gone := exec.Command("true")
		if err := gone.Start(); err != nil {
			t.Fatal(err)
		}
		_ = gone.Wait()
		writeLegacyRecord(t, state, "router.pid", int64(gone.Process.Pid), port)
		stopped := stopLegacy(context.Background(), domain.EnvLookup{"ZAI_STATE_DIR": state}, "zai", port, func(string) {})
		if stopped {
			t.Error("stopLegacy = true, want false")
		}
	})

	t.Run("a watcher named in a guard file is signalled and the file removed", func(t *testing.T) {
		t.Parallel()
		bin := buildFakeLegacy(t)
		fake := startFakeLegacy(t, bin, "zai", "watch", 0)
		state := t.TempDir()
		guard := writeGuard(t, state, fake.pid)
		var lines []string
		stopped := stopLegacy(context.Background(), domain.EnvLookup{"ZAI_STATE_DIR": state}, "zai", daemonFreePort(t),
			func(line string) { lines = append(lines, line) })
		if !stopped {
			t.Fatal("stopLegacy = false, want true")
		}
		awaitExit(t, fake.done, "the fake watcher")
		if _, err := os.Stat(guard); !os.IsNotExist(err) {
			t.Errorf("the guard file is still there: %v", err)
		}
		want := fmt.Sprintf("huddle-router: stopped the 0.0.1 zai router watch (pid %d)", fake.pid)
		if !contains(lines, want) {
			t.Errorf("the log says %q, want it to name %q", lines, want)
		}
	})

	t.Run("a guard naming a foreign pid is removed and its process kept", func(t *testing.T) {
		t.Parallel()
		state := t.TempDir()
		foreign, foreignDone := sleeperWith(t, "sleep", "30")
		guard := writeGuard(t, state, foreign)
		stopped := stopLegacy(context.Background(), domain.EnvLookup{"ZAI_STATE_DIR": state}, "zai", daemonFreePort(t), func(string) {})
		if stopped {
			t.Error("stopLegacy = true, want false")
		}
		select {
		case <-foreignDone:
			t.Fatalf("the foreign pid %d was signalled", foreign)
		case <-time.After(500 * time.Millisecond):
		}
		if !processIsAlive(foreign) {
			t.Fatalf("the foreign pid %d did not survive", foreign)
		}
		if _, err := os.Stat(guard); !os.IsNotExist(err) {
			t.Errorf("the guard file is still there: %v", err)
		}
	})
}

// contains is whether lines holds want.
func contains(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
