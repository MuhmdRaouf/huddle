package server

// The hub's idle cost: `huddle serve` runs all day, so with no client and no request its
// timers must cost next to nothing. The whole hub runs in this process the way serve starts
// it (the hub, the registry's watchers and the radar tab) over a temp home that holds days of
// transcripts, and the process's own CPU time is sampled while
// nothing talks to it. The gate is the best of three windows.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

const (
	// idleCPUBudget is the most of one core the idle daemon may use.
	idleCPUBudget = 0.005
	idleSample    = 10 * time.Second
	// idleWindows is how many windows the gate samples: noise lands in some windows, a
	// regression in all of them.
	idleWindows = 3
)

// idleVerdict answers whether the idle hub holds its budget over the windows sampled so far: it
// holds as soon as one window's share is under budget, and fails only when every one of
// idleWindows windows is at or over it.
func idleVerdict(shares []float64, budget float64) (held, done bool) {
	for _, share := range shares {
		if share < budget {
			return true, true
		}
	}
	return false, len(shares) >= idleWindows
}

// seedTranscripts lays out the transcript history a working machine keeps under
// <claude>/projects: projects × sessions days-old transcripts, each session with its
// subagents folder, plus one transcript per project from today.
func seedTranscripts(t *testing.T, claude string, projects, sessions int) {
	t.Helper()
	line := []byte(`{"type":"user","sessionId":"s","cwd":"/work","message":{"role":"user","content":"hi"},"timestamp":"2026-01-01T00:00:00Z"}` + "\n")
	old := time.Now().Add(-72 * time.Hour)
	stamp := func(path string, at time.Time) {
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, at time.Time) {
		if err := os.WriteFile(path, line, 0o600); err != nil {
			t.Fatal(err)
		}
		stamp(path, at)
	}
	for p := range projects {
		dir := filepath.Join(claude, "projects", fmt.Sprintf("-work-project-%d", p))
		for s := range sessions {
			subagents := filepath.Join(dir, fmt.Sprintf("session-%d", s), "subagents")
			if err := os.MkdirAll(subagents, 0o700); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(dir, fmt.Sprintf("session-%d.jsonl", s)), old)
			write(filepath.Join(subagents, "agent-1.jsonl"), old)
			write(filepath.Join(subagents, "agent-2.jsonl"), old)
			stamp(subagents, old)
			stamp(filepath.Dir(subagents), old)
		}
		write(filepath.Join(dir, "today.jsonl"), time.Now())
	}
}

// cpuTime is this process's user plus system time so far.
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

func TestIdleWindows(t *testing.T) {
	for _, tt := range []struct {
		shares     []float64
		held, done bool
	}{
		{shares: []float64{0.004}, held: true, done: true},
		{shares: []float64{0.006}, held: false, done: false},
		{shares: []float64{0.006, 0.004}, held: true, done: true},
		{shares: []float64{0.006, 0.007, 0.006}, held: false, done: true},
		{shares: []float64{0.005, 0.005, 0.005}, held: false, done: true},
	} {
		held, done := idleVerdict(tt.shares, 0.005)
		if held != tt.held || done != tt.done {
			t.Errorf("idleVerdict(%v, 0.005) = %t, %t; want %t, %t",
				tt.shares, held, done, tt.held, tt.done)
		}
	}
}

func TestHubIdleCPU(t *testing.T) {
	if testkit.RaceEnabled {
		t.Skip("the race detector multiplies every instruction's cost; the plain build gates the idle share")
	}
	root := t.TempDir()
	huddleHome := filepath.Join(root, "huddle-home")
	if err := os.MkdirAll(huddleHome, 0o700); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(root, "claude")
	seedTranscripts(t, claude, 20, 25)
	// the process's HOME is the temp root too, so the push keys never reach the OS keystore
	t.Setenv("HOME", root)
	environ := []string{
		"HOME=" + root,
		"HUDDLE_HOME=" + huddleHome,
		"RADAR_HOME=" + filepath.Join(root, "radar"),
		"CLAUDE_CONFIG_DIR=" + claude,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "xdg-data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "xdg-cache"),
	}
	port := ports.Free(t)
	o := Options{
		Version: "0.0.0", Home: huddleHome, Data: huddleHome, Token: "idle-root",
		Port: port, PortNamed: true, Stdout: io.Discard, Stderr: io.Discard,
		Environ: environ, AutoCreate: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the hub stopped with %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the hub never stopped")
		}
	})
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(15 * time.Second)
	for !answers(base) {
		if time.Now().After(deadline) {
			t.Fatalf("the hub never came up at %s", base)
		}
		time.Sleep(25 * time.Millisecond)
	}
	// The start's own work (the registry, the radar tab's first ingest of today's transcripts)
	// settles before the sample.
	time.Sleep(3 * time.Second)

	var shares []float64
	held, settled := false, false
	for window := 1; !settled && window <= idleWindows; window++ {
		cpuBefore, wallBefore := cpuTime(t), time.Now()
		time.Sleep(idleSample)
		used, wall := cpuTime(t)-cpuBefore, time.Since(wallBefore)
		share := float64(used) / float64(wall)
		shares = append(shares, share)
		t.Logf("window %d: idle hub: %v CPU over %v = %.3f%% of one core (budget %.1f%%)",
			window, used, wall.Round(time.Millisecond), share*100, idleCPUBudget*100)
		held, settled = idleVerdict(shares, idleCPUBudget)
	}
	if !held {
		named := make([]string, len(shares))
		for i, share := range shares {
			named[i] = fmt.Sprintf("window %d: %.3f%%", i+1, share*100)
		}
		t.Errorf("the idle hub is at or over its %.1f%% budget in every window: %s",
			idleCPUBudget*100, strings.Join(named, ", "))
	}
}
