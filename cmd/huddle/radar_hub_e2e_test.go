package main

// radar's end to end in the hub: with one healthy hub running, a session
// start runs the merged hook, the radar part names the hub's radar page, and
// no standalone radar dashboard is started — the hub is the only place radar
// is served. Every process here is this test's child, every home a temp one,
// and nothing of the real machine is ever reached.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

func TestE2ERadarLivesInTheHub(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	hubHome := filepath.Join(root, "hub-home")
	radarHome := filepath.Join(user, "radar") // the RADAR_HOME hookEnv hands the hook
	const token = "e2e-radar-root-token"

	hubPort := pinFreePort(t, hubHome)
	startServe(t, t, huddleBin, serveEnv{
		home: hubHome, user: user, port: hubPort, token: token,
		data: filepath.Join(user, "data"),
		// the hub serve serves radar in process and records itself in its own
		// RADAR_HOME: it must not share the session's, or the radar part would
		// read the hub's server.json as a standalone dashboard already running
		extra: map[string]string{"RADAR_HOME": filepath.Join(root, "hub-radar")},
	})
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	until(t, 30*time.Second, func() bool { return healthy(hubURL) }, "the hub to answer /health")
	// the radar part finds the hub the way kanban's flow client does: the home's port file
	if err := os.WriteFile(home.Home{Root: hubHome}.PortFile(), []byte(strconv.Itoa(hubPort)), 0o600); err != nil {
		t.Fatalf("write the port file: %v", err)
	}
	// a regression that starts a standalone dashboard must not leave it behind
	t.Cleanup(func() { stopRecordedRadar(t, radarHome) })

	env := hookEnv(hubHome, user, root, root, user, hubURL, token,
		map[string]string{"RADAR_AUTOSTART": "1"})
	code, stdout, stderr := runSh(t, 30*time.Second, env, user,
		strconv.Quote(huddleBin)+" hook session-start",
		`{"hook_event_name":"SessionStart","session_id":"s-radar","source":"startup","cwd":`+strconv.Quote(user)+`}`)
	if code != 0 {
		t.Fatalf("the session-start hook = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	want := fmt.Sprintf("radar: in the huddle hub at http://127.0.0.1:%d/#/radar", hubPort)
	if !strings.Contains(stdout, want) {
		t.Fatalf("the hook printed %q, want it to name %s", stdout, want)
	}

	// a standalone dashboard that got started writes its server.json as soon as it
	// listens: within two seconds none may be there
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(radarHome, "server.json")); err == nil {
			t.Fatal("a standalone radar wrote server.json; the hub alone must serve radar")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stopRecordedRadar stops the standalone radar server.json names, if one was
// started at all: a signal first, then the hard end once the wait runs out.
func stopRecordedRadar(t *testing.T, radarHome string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(radarHome, "server.json"))
	if err != nil {
		return // nothing recorded: nothing to stop
	}
	var info struct {
		PID int64 `json:"pid"`
	}
	if err := json.Unmarshal(raw, &info); err != nil || info.PID <= 0 {
		return
	}
	if !process.IsAlive(info.PID) {
		return
	}
	process.SignalGroup(info.PID, syscall.SIGTERM)
	deadline := time.Now().Add(10 * time.Second)
	for process.IsAlive(info.PID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if process.IsAlive(info.PID) {
		process.SignalGroup(info.PID, syscall.SIGKILL)
	}
}
