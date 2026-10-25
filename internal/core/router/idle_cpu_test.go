package router_test

// The router's idle cost: huddle-router runs inside every session's machine all day, so with no
// request in flight its run loop must cost next to nothing. The daemon runs in this process with
// a temp home, and the process's own CPU time is sampled while nothing talks to it.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/router"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

const (
	// idleCPUBudget is the most of one core the idle run loop may use.
	idleCPUBudget = 0.005
	idleSample    = 10 * time.Second
)

// cpuTime is this process's user plus system time so far.
func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

func TestRouterIdleCPU(t *testing.T) {
	root := t.TempDir()
	// The handover socket lives under the router home, so the home stays short enough for a
	// socket path.
	home, err := os.MkdirTemp(os.TempDir(), "hr-idle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	port := ports.Free(t)
	env := domain.EnvLookup{
		"HOME":                        root,
		"HUDDLE_HOME":                 filepath.Join(root, "huddle-home"),
		"RADAR_HOME":                  filepath.Join(root, "radar"),
		"HUDDLE_ROUTER_HOME":          home,
		"HUDDLE_ROUTER_PORT":          strconv.Itoa(port),
		"HUDDLE_ROUTER_ANTHROPIC_URL": "http://127.0.0.1:1",
		"ZAI_ROUTER_URL":              "http://127.0.0.1:1",
		"ZAI_API_KEY":                 "test-key",
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- routerd.Run(ctx, routerd.Options{Env: env, Log: func(string) {}}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("the daemon stopped with %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("the daemon never stopped")
		}
	})
	health := fmt.Sprintf("http://127.0.0.1:%d%s", port, router.MultiHealthPath)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(health)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon never answered its health path")
		}
		time.Sleep(25 * time.Millisecond)
	}
	client.CloseIdleConnections()
	// The start's own work (the record, the one-time migration) settles before the sample.
	time.Sleep(time.Second)

	cpuBefore, wallBefore := cpuTime(t), time.Now()
	time.Sleep(idleSample)
	used, wall := cpuTime(t)-cpuBefore, time.Since(wallBefore)
	share := float64(used) / float64(wall)
	t.Logf("idle run loop: %v CPU over %v = %.3f%% of one core (budget %.1f%%)",
		used, wall.Round(time.Millisecond), share*100, idleCPUBudget*100)
	if share >= idleCPUBudget {
		t.Errorf("the idle run loop uses %.3f%% of one core, budget %.1f%%", share*100, idleCPUBudget*100)
	}
}
