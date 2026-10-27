package server

// The spinner tips file: the owner's answer at /api/insights refreshes the file the
// spinnerTipsOverride entry names in the home the server was started with, tips for me off
// empties it with the answer, and a session's answer leaves the file as it is.

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/insights"
)

// childHome answers the user home the child server was started with: the home the owner's
// answer writes the spinner tips file under.
func childHome(t *testing.T, srv *childServer) string {
	t.Helper()
	for _, entry := range srv.cmd.Env {
		if home, ok := strings.CutPrefix(entry, "HOME="); ok {
			return home
		}
	}
	t.Fatalf("the child server's environment names no HOME")
	return ""
}

// spinnerTipIDRe names the entry id's shape: huddle- plus 16 hex characters.
var spinnerTipIDRe = regexp.MustCompile(`^huddle-[0-9a-f]{16}$`)

func TestTheOwnersAnswerRefreshesTheSpinnerTipsFile(t *testing.T) {
	srv := startServer(t, nil)
	seedAllowedAsks(t, srv, 5)

	if code, _, out := srv.get(t, "/api/insights", nil); code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	}
	path := insights.SpinnerTipsFile(childHome(t, srv))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the spinner tips file: %v", err)
	}
	var file struct {
		Tips []insights.SpinnerTip `json:"tips"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	// the one whole-huddle tip the answer holds is the file's one entry
	if len(file.Tips) != 1 {
		t.Fatalf("the file holds %s, want the one always_allowed tip", raw)
	}
	if !spinnerTipIDRe.MatchString(file.Tips[0].ID) {
		t.Errorf("the entry's id = %q, want huddle- plus 16 hex", file.Tips[0].ID)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the spinner tips file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestTipsForMeOffEmptiesTheSpinnerTipsFile(t *testing.T) {
	srv := startServer(t, nil)
	seedAllowedAsks(t, srv, 5)

	if code, _, out := srv.get(t, "/api/insights", nil); code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	}
	if code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_for_me":false}`); code != 200 {
		t.Fatalf("PUT = %d %s", code, out)
	}
	if _, _, out := srv.get(t, "/api/insights", nil); out != `{"tips":[]}` {
		t.Fatalf("insights answers %s with tips for me off, want {\"tips\":[]}", out)
	}
	raw, err := os.ReadFile(insights.SpinnerTipsFile(childHome(t, srv)))
	if err != nil {
		t.Fatalf("read the spinner tips file: %v", err)
	}
	if string(raw) != `{"tips":[]}` {
		t.Errorf("the file holds %s, want {\"tips\":[]}", raw)
	}
}

func TestTheSessionAnswerLeavesTheSpinnerTipsFileAlone(t *testing.T) {
	srv := startServer(t, nil)
	seedAllowedAsks(t, srv, 5)

	// the owner's answer writes the file once, and the test ages its modification time so a
	// second write cannot hide inside the clock's granularity
	if code, _, out := srv.get(t, "/api/insights", nil); code != 200 {
		t.Fatalf("insights = %d %s", code, out)
	}
	path := insights.SpinnerTipsFile(childHome(t, srv))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the spinner tips file: %v", err)
	}
	past := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatalf("age the spinner tips file: %v", err)
	}

	if code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_to_sessions":true}`); code != 200 {
		t.Fatalf("PUT = %d %s", code, out)
	}
	if code, _, out := srv.get(t, "/api/insights?session=s-seed", nil); code != 200 {
		t.Fatalf("the session's answer = %d %s", code, out)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the spinner tips file after the session's answer: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the file's bytes moved under the session's answer: %s then %s", before, after)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat the spinner tips file after the session's answer: %v", err)
	}
	if !info.ModTime().Equal(past) {
		t.Errorf("the file's modification time = %s, want the %s the test left it at", info.ModTime(), past)
	}
}
