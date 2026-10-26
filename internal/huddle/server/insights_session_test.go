package server

// The session's answer at /api/insights?session=<id>: tips to my sessions gates it before
// anything is read, the needs-you history and the asks count that session's rows alone, and the
// answer keeps that session's tips. Without a session the owner's answer answers as before.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/insights"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestTheSessionInsightsAnswerKeepsItsOwnTips(t *testing.T) {
	srv := startServer(t, nil)

	// tips to my sessions off: the session's answer is empty before anything is read
	if _, _, out := srv.get(t, "/api/insights?session=s1", nil); out != `{"tips":[]}` {
		t.Fatalf("the session's answer with the switch off = %s, want {\"tips\":[]}", out)
	}
	without := map[string]string{"x-huddle-token": ""}
	if code, _, _ := srv.get(t, "/api/insights?session=s1", without); code != 401 {
		t.Fatalf("an uncredentialed session read = %d, want 401", code)
	}

	if code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_to_sessions":true}`); code != 200 {
		t.Fatalf("PUT = %d %s", code, out)
	}

	// needs-you history: one turn in s1 answered an hour after it rose (the longest_wait tip),
	// and five allowed asks in s2 (the always_allowed tip)
	now := time.Now()
	st, err := store.Open(context.Background(), home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the hub store: %v", err)
	}
	raised, decided := now.Add(-2*time.Hour), now.Add(-time.Hour)
	// a turn item, because a permission ask older than 10 minutes reads as expired and refuses
	// its answer: the long waits are the turns' and the questions' to hold
	if err := st.Run(context.Background(),
		"INSERT INTO items (key, session_id, kind, summary, created_at, state, decided_at)"+
			" VALUES (?, ?, ?, ?, ?, 'answered', ?)",
		"turn:insights-session:s1", "s1", hub.KindTurn, "a finished turn",
		jsfmt.ISOTime(raised), jsfmt.ISOTime(decided)); err != nil {
		t.Fatalf("seed the s1 turn: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close the hub store: %v", err)
	}

	reg, err := hub.Open(context.Background(), home.Home{Root: srv.home}.HubDB())
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	for i := 0; i < 5; i++ {
		at := now.Add(-time.Duration(5-i) * time.Hour)
		id, err := reg.CreatePermissionItem(context.Background(), "s2", "Bash",
			[]byte(`{"command":"go test ./internal/kanban/..."}`), at)
		if err != nil {
			t.Fatalf("create the s2 ask: %v", err)
		}
		answered := at.Add(2 * time.Minute)
		if err := reg.TouchPoll(context.Background(), id, jsfmt.ISOTime(answered)); err != nil {
			t.Fatalf("poll the s2 ask: %v", err)
		}
		if won, err := reg.DecidePermission(context.Background(), id, "allow", "", jsfmt.ISOTime(answered), answered); err != nil || !won {
			t.Fatalf("decide the s2 ask = (%v, %v), want the decision to win", won, err)
		}
	}
	reg.Close()

	// the session's answer: the s1 tip alone, s2's ask kept out
	code, _, out := srv.get(t, "/api/insights?session=s1", nil)
	if code != 200 {
		t.Fatalf("the session's answer = %d %s", code, out)
	}
	tips, _ := bodyMap(t, out)["tips"].([]any)
	if len(tips) != 1 {
		t.Fatalf("the session's tips = %s, want the s1 tip alone", out)
	}
	tip, _ := tips[0].(map[string]any)
	if tip["rule"] != insights.RuleLongestWait || tip["session"] != "s1" {
		t.Fatalf("the session's tip = %v, want the longest_wait tip about s1", tip)
	}

	// the owner's answer, no session: both tips
	if code, _, out = srv.get(t, "/api/insights", nil); code != 200 {
		t.Fatalf("the owner's answer = %d %s", code, out)
	}
	tips, _ = bodyMap(t, out)["tips"].([]any)
	rules := map[string]bool{}
	for _, held := range tips {
		if named, ok := held.(map[string]any); ok {
			if rule, ok := named["rule"].(string); ok {
				rules[rule] = true
			}
		}
	}
	if len(tips) != 2 || !rules[insights.RuleAlwaysAllowed] || !rules[insights.RuleLongestWait] {
		t.Fatalf("the owner's tips = %s, want the always_allowed and the longest_wait tips", out)
	}
}

func TestTheSessionInsightsAnswerLeavesOutTheToolFailingTip(t *testing.T) {
	// the child's radar tab ingests the transcripts under its own CLAUDE_CONFIG_DIR, so the
	// seed lands through the tab's watcher, the way a live session feeds it
	config := t.TempDir()
	if err := os.MkdirAll(filepath.Join(config, "projects", "huddle"), 0o755); err != nil {
		t.Fatalf("make the projects dir: %v", err)
	}
	srv := startServer(t, map[string]string{"CLAUDE_CONFIG_DIR": config})

	if code, out := putJSON(t, srv, "/api/insights/settings", `{"tips_to_sessions":true}`); code != 200 {
		t.Fatalf("PUT = %d %s", code, out)
	}

	// s1's transcript: three failed vet runs and four build runs of one input, a one-off test
	// run splitting the builds. The three failures start the streak themselves, so its
	// sentence counts three, and the sentence needs the error text radar does not record; the
	// four builds give same_command its only span, the three vets staying under its four
	now := time.Now().UTC()
	toolUse := func(id, command string, at time.Time) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"model":"test-model",`+
			`"content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":%q}}]}}`,
			jsfmt.ISOTime(at), id, command)
	}
	toolResult := func(id string, failed bool, at time.Time) string {
		return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[`+
			`{"type":"tool_result","tool_use_id":%q,"is_error":%t}]}}`, jsfmt.ISOTime(at), id, failed)
	}
	run := func(n int, command string, failed bool, at time.Time) string {
		id := fmt.Sprintf("tu-%d", n)
		return toolUse(id, command, at) + "\n" + toolResult(id, failed, at.Add(time.Second))
	}
	minutesAgo := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	lines := []string{
		run(0, "go vet ./...", true, minutesAgo(6)),
		run(1, "go vet ./...", true, minutesAgo(5)),
		run(2, "go vet ./...", true, minutesAgo(4)),
		run(3, "go build ./...", false, minutesAgo(3)),
		run(4, "go test ./internal/insights/", false, minutesAgo(2)),
		run(5, "go build ./...", false, minutesAgo(1)),
		run(6, "go build ./...", false, now.Add(-30*time.Second)),
		run(7, "go build ./...", false, now.Add(-15*time.Second)),
	}
	// the rename lands the whole transcript at once: a watcher pass that caught the write
	// halfway could count a boundary run twice
	path := filepath.Join(config, "projects", "huddle", "s1.jsonl")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write the transcript: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("land the transcript: %v", err)
	}

	// the watcher's pass lands the runs in the tab's store within a few seconds; the
	// same_command tip is the proof the four runs arrived
	deadline := time.Now().Add(20 * time.Second)
	var tips []any
	var answer string
	for {
		code, _, out := srv.get(t, "/api/insights?session=s1", nil)
		if code != 200 {
			t.Fatalf("the session's answer = %d %s", code, out)
		}
		answer = out
		tips, _ = bodyMap(t, out)["tips"].([]any)
		arrived := false
		for _, held := range tips {
			if named, ok := held.(map[string]any); ok && named["rule"] == insights.RuleSameCommand {
				arrived = true
			}
		}
		if arrived {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the s1 runs never landed; the session's answer = %s", answer)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// the tool runs add same_command alone: tool_failing needs the error text radar does not
	// record, so the session's answer leaves its tip out
	if len(tips) != 1 {
		t.Fatalf("the session's tips = %s, want the same_command tip alone", answer)
	}
	tip, _ := tips[0].(map[string]any)
	if tip["rule"] != insights.RuleSameCommand || tip["session"] != "s1" {
		t.Fatalf("the session's tip = %v, want the same_command tip about s1", tip)
	}
}
