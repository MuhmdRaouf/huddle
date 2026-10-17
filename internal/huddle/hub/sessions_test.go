package hub

// The registry's reads: every session with its state computed when read, one session with the
// events that led to it, and the background pid check that ends sessions whose process died
// without a SessionEnd.

import (
	"context"
	"testing"
	"time"
)

// readNow is the clock the reads compute their states against: after every event in the
// fixtures, which carry 2026-10-16T09:00:xx timestamps.
var readNow = time.Date(2026, 10, 16, 9, 0, 30, 0, time.UTC)

func TestSessionsComputesTheStateWhenRead(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-work", Machine: "box", Project: "/shop", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-idle", Machine: "box", Project: "/shop", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:10.000Z"},
		{SessionID: "s-old", Machine: "laptop", Project: "/site", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T08:00:00.000Z"},
		{SessionID: "s-done", Machine: "laptop", Project: "/site", LastEvent: "SessionEnd", LastEventAt: "2026-10-16T09:00:25.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	got, err := db.Sessions(ctx, SessionsFilter{}, readNow)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	states := map[string]string{}
	for _, row := range got {
		states[row.SessionID] = row.State
	}
	for id, want := range map[string]string{
		"s-work": StateWorking, "s-idle": StateIdle, "s-old": StateStuck, "s-done": StateEnded,
	} {
		if states[id] != want {
			t.Errorf("%s state = %q, want %q", id, states[id], want)
		}
	}
	// newest activity first
	order := []string{}
	for _, row := range got {
		order = append(order, row.SessionID)
	}
	want := []string{"s-done", "s-work", "s-idle", "s-old"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
	// the row carries the stored fields beside the computed one
	var work *SessionRow
	for i, row := range got {
		if row.SessionID == "s-work" {
			work = &got[i]
		}
	}
	if work == nil {
		t.Fatal("no row for s-work")
	}
	if work.Machine != "box" || work.Project != "/shop" || work.LastEvent != "UserPromptSubmit" ||
		work.LastEventAt != "2026-10-16T09:00:20.000Z" {
		t.Errorf("s-work row = %+v", *work)
	}
}

func TestSessionsFiltersByMachineProjectAndState(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-1", Machine: "box", Project: "/shop", Channel: "shop", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-2", Machine: "box", Project: "/site", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:10.000Z"},
		{SessionID: "s-3", Machine: "laptop", Project: "/shop", LastEvent: "SessionEnd", LastEventAt: "2026-10-16T09:00:25.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	cases := []struct {
		name   string
		filter SessionsFilter
		want   []string
	}{
		{"machine", SessionsFilter{Machine: "box"}, []string{"s-1", "s-2"}},
		{"project", SessionsFilter{Project: "/shop"}, []string{"s-3", "s-1"}},
		{"state", SessionsFilter{State: StateEnded}, []string{"s-3"}},
		{"state idle", SessionsFilter{State: StateIdle}, []string{"s-2"}},
		{"machine and project", SessionsFilter{Machine: "box", Project: "/site"}, []string{"s-2"}},
		{"a filter nothing matches", SessionsFilter{Machine: "nowhere"}, nil},
		{"no filter at all", SessionsFilter{}, []string{"s-3", "s-1", "s-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.Sessions(ctx, tc.filter, readNow)
			if err != nil {
				t.Fatalf("sessions: %v", err)
			}
			ids := []string{}
			for _, row := range got {
				ids = append(ids, row.SessionID)
			}
			if len(ids) != len(tc.want) {
				t.Fatalf("sessions = %v, want %v", ids, tc.want)
			}
			for i := range ids {
				if ids[i] != tc.want[i] {
					t.Fatalf("sessions = %v, want %v", ids, tc.want)
				}
			}
		})
	}
}

func TestSessionAnswersTheRowAndItsLastEvents(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "s-1", Machine: "box", Project: "/repo",
		Title: "Fix the checkout", LastEvent: "SessionStart", LastEventAt: "2026-10-16T09:00:00.000Z"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	for range 250 {
		if err := db.Report(ctx, Report{SessionID: "s-1", Task: "step", LastEvent: "PostToolUse",
			LastEventAt: "2026-10-16T09:00:01.000Z"}); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	detail, err := db.Session(ctx, "s-1", readNow)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	if detail == nil {
		t.Fatal("no detail for s-1")
	}
	if detail.Title != "Fix the checkout" || detail.Project != "/repo" {
		t.Errorf("the row = %+v", detail.SessionRow)
	}
	if detail.State != StateWorking { // its last event is a PostToolUse 29 s before the clock
		t.Errorf("state = %q, want working", detail.State)
	}
	// the last 200 of the 251 events, oldest of them first: seqs 52..251, the SessionStart cut
	if len(detail.Events) != 200 {
		t.Fatalf("events = %d, want the last 200 of 251", len(detail.Events))
	}
	if detail.Events[0].Seq != 52 || detail.Events[199].Seq != 251 {
		t.Errorf("events run seq %d..%d, want 52..251", detail.Events[0].Seq, detail.Events[199].Seq)
	}
	if detail.Events[0].Report.SessionID != "s-1" || detail.Events[0].Report.LastEvent != "PostToolUse" {
		t.Errorf("first event = %+v, want a PostToolUse report of s-1", detail.Events[0])
	}
	missing, err := db.Session(ctx, "no-such", readNow)
	if err != nil || missing != nil {
		t.Errorf("an unknown session = %v, %v; want nil, nil", missing, err)
	}
}

func TestReportsThatEndAndRestartASession(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	state := func(id string) string {
		t.Helper()
		detail, err := db.Session(ctx, id, readNow)
		if err != nil {
			t.Fatalf("session %s: %v", id, err)
		}
		if detail == nil {
			t.Fatal("no session " + id)
		}
		return detail.State
	}
	// a SessionEnd ends the session, and stays ended under an older report replayed late
	for _, rep := range []Report{
		{SessionID: "s-1", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"},
		{SessionID: "s-1", LastEvent: "SessionEnd", LastEventAt: "2026-10-16T09:00:05.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.LastEvent, err)
		}
	}
	if got := state("s-1"); got != StateEnded {
		t.Fatalf("after the SessionEnd = %q, want ended", got)
	}
	if err := db.Report(ctx, Report{SessionID: "s-1", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T09:00:02.000Z"}); err != nil {
		t.Fatalf("replayed report: %v", err)
	}
	if got := state("s-1"); got != StateEnded {
		t.Fatalf("after a late replay over the end = %q, want ended", got)
	}
	// a SessionStart after the end brings the session back
	if err := db.Report(ctx, Report{SessionID: "s-1", LastEvent: "SessionStart", LastEventAt: "2026-10-16T09:00:20.000Z"}); err != nil {
		t.Fatalf("restart report: %v", err)
	}
	if got := state("s-1"); got != StateWorking {
		t.Fatalf("after the restart = %q, want working", got)
	}
}

func TestMarkEndedPidsEndsSessionsWhoseProcessIsGone(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-alive", Pid: 101, LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-gone", Pid: 102, LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-taken", Pid: 103, LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-ended", Pid: 104, LastEvent: "SessionEnd", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-nopid", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	probe := func(ctx context.Context, pid int) (string, bool) {
		switch pid {
		case 101: // the claude process, at work
			return "/usr/local/bin/claude", true
		case 103: // alive, but the pid now runs something else
			return "/usr/bin/top", true
		default: // 102 is gone; 104 is never probed again (already ended)
			return "", false
		}
	}
	marked, err := db.MarkEndedPids(ctx, "2026-10-16T09:01:00.000Z", probe)
	if err != nil {
		t.Fatalf("mark: %v", err)
	}
	if len(marked) != 2 || marked[0] != "s-gone" || marked[1] != "s-taken" {
		t.Fatalf("marked = %v, want s-gone and s-taken", marked)
	}
	got, err := db.Sessions(ctx, SessionsFilter{State: StateEnded}, readNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	ids := map[string]bool{}
	for _, row := range got {
		ids[row.SessionID] = true
	}
	if !ids["s-gone"] || !ids["s-taken"] || !ids["s-ended"] {
		t.Errorf("ended sessions = %v, want s-gone, s-taken and s-ended", ids)
	}
	if ids["s-alive"] || ids["s-nopid"] {
		t.Errorf("ended sessions = %v, want s-alive and s-nopid left alone", ids)
	}
	// the second pass finds nothing new to mark
	if marked, err = db.MarkEndedPids(ctx, "2026-10-16T09:01:30.000Z", probe); err != nil || len(marked) != 0 {
		t.Errorf("second pass = %v, %v; want nothing marked", marked, err)
	}
}

func TestKnownStateNamesTheFiveStates(t *testing.T) {
	for _, s := range []string{StateNeedsYou, StateWorking, StateIdle, StateStuck, StateEnded} {
		if !KnownState(s) {
			t.Errorf("KnownState(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "working hard", "WORKING", "paused"} {
		if KnownState(s) {
			t.Errorf("KnownState(%q) = true, want false", s)
		}
	}
	want := "needs-you, working, idle, stuck, ended"
	if got := StateNames(); got != want {
		t.Errorf("StateNames() = %q, want %q", got, want)
	}
}

func TestAMalformedTimestampNeverReadsYoung(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "s-bent", LastEvent: "UserPromptSubmit",
		LastEventAt: "not a time"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	got, err := db.Sessions(ctx, SessionsFilter{}, readNow)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(got) != 1 || got[0].State != StateStuck {
		t.Fatalf("a session with a malformed timestamp = %+v, want stuck", got)
	}
}

// The state rules read the stored events too: a Notification that asks the owner holds the
// session in needs-you until a later event answers it, and one that asks nothing does not.
func TestAReportedNotificationHoldsTheStateItsWordsAsk(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	reports := []Report{
		{SessionID: "s-ask", LastEvent: "Notification",
			Notification: &Notification{Type: "agent_needs_input", Message: "Which database?"},
			LastEventAt:  "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-tell", LastEvent: "Notification",
			Notification: &Notification{Type: "auth_success", Message: "Signed in"},
			LastEventAt:  "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-bare", LastEvent: "Notification", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-wordless", LastEvent: "Notification",
			Notification: &Notification{Type: "elicitation_dialog"}, LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-permit", LastEvent: "PermissionRequest", LastEventAt: "2026-10-16T09:00:20.000Z"},
	}
	for _, rep := range reports {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	// s-answered asked once, then a tool event answered it: the newer event wins
	if err := db.Report(ctx, Report{SessionID: "s-answered", LastEvent: "Notification",
		Notification: &Notification{Type: "elicitation_dialog"}, LastEventAt: "2026-10-16T09:00:20.000Z"}); err != nil {
		t.Fatalf("report the ask: %v", err)
	}
	if err := db.Report(ctx, Report{SessionID: "s-answered", LastEvent: "PostToolUse",
		LastEventAt: "2026-10-16T09:00:25.000Z"}); err != nil {
		t.Fatalf("report the tool event: %v", err)
	}
	got, err := db.Sessions(ctx, SessionsFilter{}, readNow)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	states := map[string]string{}
	for _, row := range got {
		states[row.SessionID] = row.State
	}
	for id, want := range map[string]string{
		"s-ask":  StateNeedsYou,
		"s-tell": StateWorking,
		"s-bare": StateWorking,
		// an ask with no words to grow an item from is the event rule's own entry
		"s-wordless": StateNeedsYou,
		"s-permit":   StateNeedsYou,
		"s-answered": StateWorking,
	} {
		if states[id] != want {
			t.Errorf("%s state = %q, want %q", id, states[id], want)
		}
	}
}

func TestATornEventRowRefusesTheRead(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "s-1", LastEvent: "SessionStart",
		LastEventAt: "2026-10-16T09:00:00.000Z"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := db.st.Run(ctx, "INSERT INTO events (session_id, at, kind, json) VALUES (?, ?, ?, ?)",
		"s-1", "2026-10-16T09:00:01.000Z", "UserPromptSubmit", "{torn"); err != nil {
		t.Fatalf("plant the torn row: %v", err)
	}
	if _, err := db.Session(ctx, "s-1", readNow); err == nil {
		t.Fatal("a torn event row read back, want the refusal")
	}
}

func TestABrokenDatabaseRefusesTheReport(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.st.Run(ctx, "DROP TABLE events"); err != nil {
		t.Fatalf("break the database: %v", err)
	}
	if err := db.Report(ctx, Report{SessionID: "s-1", LastEvent: "Stop",
		LastEventAt: "2026-10-16T09:00:00.000Z"}); err == nil {
		t.Fatal("a report landed on a broken database, want the refusal")
	}
}

func TestAReportTagsItsSessionWithItsHarnessAndKeepsTheTag(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{
		SessionID: "s-hud", Machine: "box", Project: "/shop", Harness: "codex",
		LastEvent: "PreToolUse", LastEventAt: "2026-10-16T09:00:10.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	row, err := db.SessionRow(ctx, "s-hud", readNow)
	if err != nil || row == nil {
		t.Fatalf("row: %v (%v)", row, err)
	}
	if row.Harness != "codex" {
		t.Errorf("harness = %q, want codex", row.Harness)
	}
	// a report that names no harness — Claude Code's own — leaves the tag alone
	if err := db.Report(ctx, Report{
		SessionID: "s-hud", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:20.000Z",
	}); err != nil {
		t.Fatalf("second report: %v", err)
	}
	row, err = db.SessionRow(ctx, "s-hud", readNow)
	if err != nil || row == nil {
		t.Fatalf("row after: %v (%v)", row, err)
	}
	if row.Harness != "codex" {
		t.Errorf("harness after a plain report = %q, want codex", row.Harness)
	}
	// a Claude Code session of its own carries no tag at all
	if err := db.Report(ctx, Report{
		SessionID: "s-cc", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:15.000Z",
	}); err != nil {
		t.Fatalf("claude report: %v", err)
	}
	row, err = db.SessionRow(ctx, "s-cc", readNow)
	if err != nil || row == nil {
		t.Fatalf("row: %v (%v)", row, err)
	}
	if row.Harness != "" {
		t.Errorf("harness = %q, want none", row.Harness)
	}
}
