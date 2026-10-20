package routerd

// The session watchdog's decisions, over fakes: what starts a daemon, what replaces a holder,
// and what never touches either. The clock is bent through small Every and Silence windows.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/adapters/singleton"
	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// watchSpy records what one Watch run did.
type watchSpy struct {
	mu     sync.Mutex
	health int
	starts []time.Time
	repl   []int
	lines  []string
	// healthy is what Healthy answers; startErr what Start returns; startPanics makes Start
	// panic; replaceOK is what the faked Replace answers.
	healthy     bool
	startErr    error
	startPanics bool
	replaceOK   bool
}

func (s *watchSpy) healthyCall(context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health++
	return s.healthy
}

func (s *watchSpy) startCall(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, time.Now())
	if s.startPanics {
		panic("the start exploded")
	}
	return s.startErr
}

func (s *watchSpy) replaceCall(_ context.Context, pid int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repl = append(s.repl, pid)
	return s.replaceOK
}

func (s *watchSpy) logCall(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

func (s *watchSpy) startCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.starts)
}

func (s *watchSpy) startTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.starts...)
}

func (s *watchSpy) replaced() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.repl...)
}

func (s *watchSpy) logged() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "\n")
}

// watchEnv is a temp router home and the env that reads it.
func watchEnv(t *testing.T) domain.EnvLookup {
	t.Helper()
	return domain.EnvLookup{
		"HOME":               t.TempDir(),
		"HUDDLE_ROUTER_HOME": filepath.Join(t.TempDir(), "router-home"),
		"HUDDLE_ROUTER_PORT": "0",
	}
}

// holderLockEnv names the lock a re-exec'd test binary takes when it serves as the router lock's
// holder; the argv the spawner appends decides whose command line the holder reads as.
const holderLockEnv = "HUDDLE_WATCHDOG_HOLDER_LOCK"

// TestWatchdogHolderProcess is never run as a test: the suite re-exec's the test binary with
// holderLockEnv set, and it takes the lock at that path and holds it, silent, until signalled.
func TestWatchdogHolderProcess(t *testing.T) {
	path := os.Getenv(holderLockEnv)
	if path == "" {
		return
	}
	release, err := singleton.Lock(path, "holder")
	if err != nil {
		fmt.Fprintln(os.Stderr, "holder:", err)
		os.Exit(3)
	}
	defer release()
	time.Sleep(60 * time.Second)
}

// spawnHolder starts one live holder of the router lock: ours is this binary with the daemon's
// words in its argv, foreign a renamed copy whose command line names neither the binary nor the
// words. It answers the pid, the liveness probe — the holder is the test's own child, so its
// exit must be reaped before the pid reads dead — and the stop the caller registers as cleanup.
func spawnHolder(t *testing.T, lockPath string, ours bool) (int, func() bool, func()) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !ours {
		renamed := filepath.Join(t.TempDir(), "someone-elses-program")
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(renamed, data, 0o755); err != nil {
			t.Fatal(err)
		}
		self = renamed
	}
	args := []string{"-test.run=^TestWatchdogHolderProcess$"}
	if ours {
		args = append(args, "router", "serve")
	}
	cmd := exec.Command(self, args...)
	// The holder leads its own process group, the way the daemon is spawned, so the
	// replacement's group signals reach it and nothing else.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), holderLockEnv+"="+lockPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(reaped) }()
	stop := func() {
		process.TerminateGroup(context.Background(), int64(cmd.Process.Pid), 2000)
		select {
		case <-reaped:
		case <-time.After(5 * time.Second):
		}
	}
	pid := cmd.Process.Pid
	alive := func() bool {
		select {
		case <-reaped:
			return false
		default:
			return process.IsAlive(int64(pid))
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if holder, held := singleton.Holder(lockPath); held && holder == pid {
			return pid, alive, stop
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the holder never took the lock at %s", lockPath)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lockPathOf is the router lock the watchdog reads.
func lockPathOf(env domain.EnvLookup) string {
	return filepath.Join(router.HomeOf(env), "run", "router.lock")
}

func TestSessionWatchdogRows(t *testing.T) {
	t.Parallel()

	t.Run("with no provider enabled nothing is probed and nothing starts", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		spy := &watchSpy{healthy: true}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: 50 * time.Millisecond,
			Healthy: spy.healthyCall, Start: spy.startCall, Replace: spy.replaceCall, Log: spy.logCall,
		})
		if spy.startCount() != 0 {
			t.Errorf("starts = %d, want none", spy.startCount())
		}
		if spy.health != 0 {
			t.Errorf("health probes = %d, want none: nothing enabled is nothing to watch", spy.health)
		}
	})

	t.Run("a healthy router is never started over", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		spy := &watchSpy{healthy: true}
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: 50 * time.Millisecond,
			Healthy: spy.healthyCall, Start: spy.startCall, Replace: spy.replaceCall, Log: spy.logCall,
		})
		if spy.startCount() != 0 {
			t.Errorf("starts = %d, want none while health answers", spy.startCount())
		}
		if spy.health == 0 {
			t.Errorf("health probes = 0, want the ticks' probes")
		}
	})

	t.Run("silence starts one daemon, the second only after another silence", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		spy := &watchSpy{healthy: false}
		const silence = 80 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: silence,
			Healthy: spy.healthyCall, Start: spy.startCall, Replace: spy.replaceCall, Log: spy.logCall,
		})
		times := spy.startTimes()
		if len(times) < 2 {
			t.Fatalf("starts = %d, want at least two across the run", len(times))
		}
		for i, at := range times {
			if i == 0 {
				continue
			}
			if gap := at.Sub(times[i-1]); gap < silence-20*time.Millisecond {
				t.Errorf("start %d came %s after start %d, want at least the silence %s", i, gap, i-1, silence)
			}
		}
		if got := spy.logged(); !strings.Contains(got, "restarted by the session watchdog") {
			t.Errorf("log = %q, want one line per restart", got)
		}
	})

	t.Run("a live holder that is not the daemon is left alone and not started over", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		_, holderAlive, stop := spawnHolder(t, lockPathOf(env), false)
		t.Cleanup(stop)
		spy := &watchSpy{healthy: false}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: 50 * time.Millisecond,
			Healthy: spy.healthyCall, Start: spy.startCall, Log: spy.logCall,
		})
		if spy.startCount() != 0 {
			t.Errorf("starts = %d, want none while a program of the user's holds the port", spy.startCount())
		}
		if !holderAlive() {
			t.Errorf("the foreign holder was signalled")
		}
		if got := spy.logged(); !strings.Contains(got, "not replaced") {
			t.Errorf("log = %q, want the refusal line", got)
		}
	})

	t.Run("a live hung daemon of ours is replaced and then started", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		_, holderAlive, stop := spawnHolder(t, lockPathOf(env), true)
		t.Cleanup(stop)
		spy := &watchSpy{healthy: false}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: 50 * time.Millisecond,
			Healthy: spy.healthyCall, Start: spy.startCall, Log: spy.logCall,
		})
		if spy.startCount() == 0 {
			t.Fatalf("starts = 0, want one after the hung daemon was replaced")
		}
		if holderAlive() {
			t.Errorf("the hung daemon survived the replacement")
		}
	})

	t.Run("a holder a faked Replace refuses skips the start and names the pid", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		pid, _, stop := spawnHolder(t, lockPathOf(env), true)
		t.Cleanup(stop)
		spy := &watchSpy{healthy: false, replaceOK: false}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: 50 * time.Millisecond,
			Healthy: spy.healthyCall, Start: spy.startCall, Replace: spy.replaceCall, Log: spy.logCall,
		})
		if got := spy.replaced(); len(got) == 0 || got[0] != pid {
			t.Errorf("Replace got %v, want the holder's pid %d first", got, pid)
		}
		if spy.startCount() != 0 {
			t.Errorf("starts = %d, want none after a refused replace", spy.startCount())
		}
	})

	t.Run("a panicking Start ends neither the watch nor the session", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		spy := &watchSpy{healthy: false, startPanics: true}
		const silence = 60 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: silence,
			Healthy: spy.healthyCall, Start: spy.startCall, Replace: spy.replaceCall, Log: spy.logCall,
		})
		if spy.startCount() < 2 {
			t.Errorf("starts = %d, want the watch to come back after each panic", spy.startCount())
		}
	})

	t.Run("a start that fails is retried after another silence, not hammered", func(t *testing.T) {
		t.Parallel()
		env := watchEnv(t)
		if err := router.SetEnabled(env, "zai", true); err != nil {
			t.Fatal(err)
		}
		spy := &watchSpy{healthy: false, startErr: errors.New("no daemon")}
		const silence = 80 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		Watch(ctx, WatchOptions{
			Env: env, Every: 10 * time.Millisecond, Silence: silence,
			Healthy: spy.healthyCall, Start: spy.startCall, Replace: spy.replaceCall, Log: spy.logCall,
		})
		times := spy.startTimes()
		if len(times) < 2 {
			t.Fatalf("starts = %d, want the retry", len(times))
		}
		if gap := times[1].Sub(times[0]); gap < silence-20*time.Millisecond {
			t.Errorf("the retry came %s after the first start, want a fresh silence", gap)
		}
	})
}
