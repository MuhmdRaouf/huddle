package server

// A Claude Code hook on the hub's machine reports the machine's name from the home's
// config.toml, so the hub tells its own machine's reports by that same name, not by the
// operating system's hostname: the two differ wherever the hostname carries a domain
// (a Mac's "box.local") or the home names the machine something else.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheHubTellsItsMachineByTheNameTheHomeConfigHolds(t *testing.T) {
	home := tempDir("huddle-named-home-")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("machine = \"workbench\"\n"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	srv := startServer(t, map[string]string{"HUDDLE_HOME": home})
	spool := filepath.Join(srv.radarHome, "spool", time.Now().UTC().Format("2006-01-02")+".jsonl")
	spoolLines := func() int {
		raw, err := os.ReadFile(spool)
		if err != nil {
			return 0
		}
		return strings.Count(string(raw), "\n")
	}
	report := func(session, machine string) {
		t.Helper()
		body := `{"session_id":"` + session + `","machine":"` + machine + `","harness":"claude","cwd":"/w/one",` +
			`"last_event":"SessionStart","last_event_at":"` + isoAgo(time.Second) + `"}`
		if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
			t.Fatalf("post a report: %d %s", code, out)
		}
	}

	report("s-here", "workbench")
	if got := spoolLines(); got != 0 {
		t.Fatalf("the spool holds %d lines after a Claude Code report from this machine, want 0: its hook wrote the line", got)
	}

	report("s-away", "elsewhere")
	if got := spoolLines(); got != 1 {
		t.Fatalf("the spool holds %d lines after a report from another machine, want 1: no hook here ran for it", got)
	}
}
