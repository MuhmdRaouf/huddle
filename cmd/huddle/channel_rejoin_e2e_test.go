package main

// A session's bridge keeps it on its channel's list, and tells a lost member from a leave. When
// the hub holds no row for the session (here a hub restarted on a data file that no longer has
// it), the bridge joins again by itself and the next ask reaches the session once. When the row
// says the session left, which is a person's act, the bridge leaves it alone. Every process is
// this test's child and every home a temp one.

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// twoReposCheckEvery is the membership check pause the bridges of these tests run with.
const twoReposCheckEvery = 500 * time.Millisecond

// twoReposRejoinEnv makes the bridges check the channel's list every twoReposCheckEvery.
var twoReposRejoinEnv = map[string]string{"HUDDLE_TEST_MEMBER_CHECK_MS": "500"}

// twoReposSessionState reads the state the channel lists for one session ("" when it lists no
// row), through the CLI's sessions command run as proxbeam's repo.
func twoReposSessionState(t *testing.T, w *twoReposWorld, name string) string {
	t.Helper()
	code, out, errOut := runBin(t, huddleBin, w.proxbeamEnv, w.proxbeam, "sessions")
	if code != 0 {
		t.Fatalf("sessions = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	var list []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode the sessions answer %q: %v", out, err)
	}
	for _, session := range list {
		if session.Name == name {
			return session.State
		}
	}
	return ""
}

// forgetSession deletes a session's row from the channel's database file in the test's own data
// dir, the state of a hub that restarts on a store that no longer holds the member. The hub is
// down when this runs.
func forgetSession(t *testing.T, w *twoReposWorld, channel, name string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(w.hubData, "channels", channel+".db"))
	if err != nil {
		t.Fatalf("open the %s database: %v", channel, err)
	}
	defer db.Close()
	result, err := db.Exec("DELETE FROM sessions WHERE name = ?", name)
	if err != nil {
		t.Fatalf("delete %s from the %s sessions: %v", name, channel, err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		t.Fatalf("deleting %s removed %d rows, want 1", name, n)
	}
}

// TestE2EASessionThatLeftOnPurposeStaysLeft is the guard for a leave that sticks: the proxbeam
// session leaves while its stream is up, and across three checks its bridge does not list it again.
func TestE2EASessionThatLeftOnPurposeStaysLeft(t *testing.T) {
	w := startTwoReposWith(t, twoReposRejoinEnv)
	code, out, errOut := runBin(t, huddleBin, w.proxbeamEnv, w.proxbeam, "leave", "done for today")
	if code != 0 {
		t.Fatalf("leave = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	deadline := time.Now().Add(3 * twoReposCheckEvery * 2) // six checks
	for time.Now().Before(deadline) {
		if state := twoReposSessionState(t, w, "proxbeam"); state != "left" {
			t.Fatalf("the channel lists proxbeam as %q after its leave, want left to stick; stderr:\n%s", state, w.proxbeamBridge.errText())
		}
		time.Sleep(twoReposCheckEvery / 2)
	}
}

// TestE2EASessionTheHubLostJoinsAgainAndHearsTheNextAsk is the guard for a hub that loses a
// member: the hub stops, the proxbeam row leaves its channel database, the hub starts on the same
// port, and the proxbeam bridge lists its session again and pushes the next ask once.
func TestE2EASessionTheHubLostJoinsAgainAndHearsTheNextAsk(t *testing.T) {
	w := startTwoReposWith(t, twoReposRejoinEnv)
	w.restartHubWith(t, 0, func() { forgetSession(t, w, twoReposChannel, "proxbeam") })

	until(t, 30*time.Second, func() bool { return twoReposSessionState(t, w, "proxbeam") != "" },
		"the proxbeam bridge to list its session again")

	ask := "homelab to proxbeam: the hub lost you, are you back?"
	w.homelabBridge.send("proxbeam", ask)
	if _, ok := w.proxbeamBridge.push(10*time.Second, ask); !ok {
		t.Fatalf("the proxbeam bridge wrote no channel push for %q; stderr:\n%s", ask, w.proxbeamBridge.errText())
	}
	time.Sleep(2 * time.Second) // a duplicate would land within these seconds
	if got := w.proxbeamBridge.pushes(ask); got != 1 {
		t.Errorf("the proxbeam bridge wrote %d pushes for %q, want 1", got, ask)
	}
	if got := w.homelabBridge.pushes(ask); got != 0 {
		t.Errorf("the homelab bridge was pushed its own ask %d times, want 0", got)
	}
	if state := twoReposSessionState(t, w, "proxbeam"); state == "left" || state == "" {
		t.Errorf("the channel lists proxbeam as %q, want it in", state)
	}
}
