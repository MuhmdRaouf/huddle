package main

// one spool line per hook event: on the machine the hub serves, a Claude Code
// event's line is written once — by the hook process's radar part, into the
// spool the hub's radar tab serves. The hub's record of the same report must
// not add a second line. Reports from another harness or another machine keep
// the hub's write, because no hook on this machine ran for them.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

func TestE2EOneSpoolLinePerHookEvent(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	hubHome := filepath.Join(root, "hub-home")
	radarHome := filepath.Join(user, "radar") // the RADAR_HOME hookEnv hands the hook
	const token = "e2e-spool-once-token"

	hubPort := pinFreePort(t, hubHome)
	startServe(t, t, huddleBin, serveEnv{
		home: hubHome, user: user, port: hubPort, token: token,
		data: filepath.Join(user, "data"),
		// the hub's radar tab and the session's hook share this one spool: the
		// hook process on this machine is the writer, and the hub serves what
		// it wrote
		extra: map[string]string{"RADAR_HOME": radarHome},
	})
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	until(t, 30*time.Second, func() bool { return healthy(hubURL) }, "the hub to answer /health")
	// the hook's parts find the hub through the home's port file
	if err := os.WriteFile(home.Home{Root: hubHome}.PortFile(), []byte(strconv.Itoa(hubPort)), 0o600); err != nil {
		t.Fatalf("write the port file: %v", err)
	}

	// the marker base url lands only in the hook's line: each spool line names the
	// base url of the env of the process that wrote it, so the hook's writes and
	// the hub's are told apart
	env := hookEnv(hubHome, user, root, root, user, hubURL, token,
		map[string]string{"ANTHROPIC_BASE_URL": "https://hook-marker.example"})
	code, stdout, stderr := runSh(t, 30*time.Second, env, user,
		strconv.Quote(huddleBin)+" hook session-start",
		`{"hook_event_name":"SessionStart","session_id":"s-once","source":"startup","cwd":`+strconv.Quote(user)+`}`)
	if code != 0 {
		t.Fatalf("the session-start hook = %d, stdout %q, stderr %q", code, stdout, stderr)
	}

	spool := filepath.Join(radarHome, "spool")
	lines := func() int {
		return spoolLines(t, spool, `"session_id":"s-once"`, `SessionStart`)
	}
	until(t, 10*time.Second, func() bool { return lines() >= 1 }, "the hook's radar part to write the spool line")
	time.Sleep(time.Second) // a second writer for the same event has had its turn

	if got := lines(); got != 1 {
		t.Fatalf("%d spool lines for one hook event, want 1", got)
	}
	// the line that stayed is the hook's own write: it alone carries the marker
	// base url the hook's env named
	if hook := spoolLines(t, spool, `"session_id":"s-once"`, `"base_url":"https://hook-marker.example"`); hook != 1 {
		t.Fatalf("%d spool lines from the hook's own write, want 1", hook)
	}

	// the lock record under this radar home is the hub's own: it names the hub's
	// port, so no standalone dashboard ever took this home
	raw, err := os.ReadFile(filepath.Join(radarHome, "server.json"))
	if err != nil {
		t.Fatalf("read the radar home's server.json: %v", err)
	}
	var info struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("parse server.json: %v", err)
	}
	if info.Port != hubPort {
		t.Fatalf("server.json names port %d, want the hub's %d", info.Port, hubPort)
	}
}

// spoolLines counts the lines across every spool file that carry all fragments.
func spoolLines(t *testing.T, dir string, fragments ...string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0 // no spool yet: no lines
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			every := true
			for _, fragment := range fragments {
				if !strings.Contains(line, fragment) {
					every = false
					break
				}
			}
			if every {
				count++
			}
		}
	}
	return count
}
