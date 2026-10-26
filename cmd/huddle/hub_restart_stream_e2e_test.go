package main

// A hub restart in the middle of a Claude turn, end to end on the real binary: `huddle up` starts
// huddle-router and the hub, a fake Anthropic streams an answer through the router's decrypted
// CONNECT api.anthropic.com path, and a `huddle down` then `huddle up` mid-stream (a deploy's
// restart) leaves the tunnel whole, because the router is its own process. Every process here is
// this test's child or a daemon it started, every home a temp one, and the router home a short
// one nested in its own directory: the handover socket is a unix socket, macOS caps its path at
// 104 bytes, and the local CA sits beside the router home, where the router loads it from.

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// restartAt is the chunk after which the test restarts the hub.
const restartAt = 40

func TestE2EAHubRestartKeepsAStreamingTurn(t *testing.T) {
	root := t.TempDir()
	parent := shortRouterHome(t)
	routerHome := filepath.Join(parent, "r")
	hubHome := filepath.Join(root, "huddle-home")
	hubPort := ports.Free(t)
	routerPort := ports.Free(t)
	anthropic := updateFake(t)
	zaiUp := newSettingsFake(t, "zai")
	ca, err := pki.LoadOrCreateRouterCA(context.Background(), filepath.Join(parent, "ca"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM()) {
		t.Fatal("the CA's PEM did not parse")
	}
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+hubHome,
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_TOKEN=hub-restart-e2e-root",
		"HUDDLE_NO_PROJECT_CRED=1",
		pki.StoreOffEnv+"=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropic.URL,
		"ZAI_ROUTER_URL="+zaiUp.server.URL,
		"ZAI_API_KEY=hub-restart-e2e-key",
	))
	pids := newPidWatch(t)
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	routerURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)

	// up runs `huddle up` and hands every daemon it names to the cleanup, whatever the checks find.
	up := func(what string) {
		t.Helper()
		code, out, errOut := runBin(t, huddleBin, env, root, "up", "--port", strconv.Itoa(hubPort))
		if raw, err := os.ReadFile(filepath.Join(hubHome, "run", "huddle.pid")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				pids.see(pid)
			}
		}
		if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
			var record handoverRecord
			if json.Unmarshal(raw, &record) == nil {
				pids.see(record.PID)
			}
		}
		if code != 0 {
			t.Fatalf("%s = %d\nstdout:\n%s\nstderr:\n%s", what, code, out, errOut)
		}
	}

	up("the first up")
	hubBefore := hubPid(t, hubHome)
	until(t, 15*time.Second, func() bool { return routerHealthy(routerPort) }, "the router to answer its health path")
	routerBefore := awaitHandoverRecord(t, routerHome).PID
	pids.see(routerBefore)

	long := proxiedClient(routerURL, pool)
	defer long.CloseIdleConnections()
	seen := make(chan int, updateChunks)
	finished := make(chan streamedTurn, 1)
	go func() { finished <- streamTurn(long, updateChunks, seen) }()
	waitChunk := time.After(30 * time.Second)
	for reached := false; !reached; {
		select {
		case n := <-seen:
			reached = n >= restartAt
		case turn := <-finished:
			t.Fatalf("the turn ended before the restart: %d chunks, stop %v, err %v", turn.chunks, turn.stop, turn.err)
		case <-waitChunk:
			t.Fatalf("the turn never reached chunk %d", restartAt)
		}
	}

	if code, out, errOut := runBin(t, huddleBin, env, root, "down"); code != 0 {
		t.Fatalf("down = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 10*time.Second, func() bool { return !argvHealthy(hubURL) }, "the hub to stop answering")
	up("the second up")
	until(t, 15*time.Second, func() bool { return argvHealthy(hubURL) }, "the restarted hub to answer its health path")
	hubAfter := hubPid(t, hubHome)
	if hubAfter == hubBefore {
		t.Fatalf("the hub kept pid %d across down and up, want a new process", hubBefore)
	}

	var turn streamedTurn
	select {
	case turn = <-finished:
	case <-time.After(time.Minute):
		t.Fatal("the turn never ended")
	}
	if turn.err != nil || turn.chunks != updateChunks || !turn.stop {
		t.Fatalf("the turn across the hub restart read %d of %d chunks, message_stop %v, err %v",
			turn.chunks, updateChunks, turn.stop, turn.err)
	}

	if record := awaitHandoverRecord(t, routerHome); record.PID != routerBefore {
		t.Fatalf("the router's pid moved from %d to %d across the hub restart", routerBefore, record.PID)
	}
	if syscall.Kill(routerBefore, 0) != nil {
		t.Fatalf("the router (pid %d) is gone after the hub restart", routerBefore)
	}
	if !argvHealthy(hubURL) {
		t.Fatalf("the restarted hub (pid %d) does not answer at %s", hubAfter, hubURL)
	}
}
