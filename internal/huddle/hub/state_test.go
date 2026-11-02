package hub

// The registry's state rules: the state is computed when read, never stored. The
// table walks every rule with a fixed clock — the ages are counted from it, so nothing here
// reads the wall.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// stateNow is the clock every case ages its events against.
var stateNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// ago answers a time age before the fixed clock.
func ago(age time.Duration) time.Time { return stateNow.Add(-age) }

func TestStateOfFollowsTheRulesOfTheSpec(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want string
	}{
		// ended outranks everything: a SessionEnd seen, or the machine reports the pid gone
		{"a young SessionEnd", Facts{LastEvent: "SessionEnd", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone under a young working event", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone with an open item", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(time.Minute), OpenItem: true, Ended: true}, StateEnded},
		{"a pid gone over an idle session", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},
		{"a pid gone over a permission request", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(time.Minute), Ended: true}, StateEnded},

		// needs-you: an open needs-you item for the session
		{"an open item with a young working event", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(5 * time.Minute), OpenItem: true}, StateNeedsYou},
		{"an open item over an idle session", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute), OpenItem: true}, StateNeedsYou},
		{"an open item over a session quiet for an hour", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(time.Hour), OpenItem: true}, StateNeedsYou},

		// needs-you: the last event itself waits on the owner — a permission request, or a
		// Notification that asks — with no later prompt or tool event
		{"a PermissionRequest just now", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(2 * time.Second)}, StateNeedsYou},
		{"a PermissionRequest half an hour old with nothing after", Facts{LastEvent: "PermissionRequest", LastEventAt: ago(30 * time.Minute)}, StateNeedsYou},
		{"a Notification that asks", Facts{LastEvent: "Notification", LastEventAt: ago(time.Minute), Asking: true}, StateNeedsYou},
		{"a Notification that asked an hour ago and was never answered", Facts{LastEvent: "Notification", LastEventAt: ago(time.Hour), Asking: true}, StateNeedsYou},
		{"an asking Notification over an idle session", Facts{LastEvent: "Notification", LastEventAt: ago(time.Minute), Asking: true, OpenItem: true}, StateNeedsYou},

		// working: a live session whose last event is young — a prompt or tool call keeps it
		// working, and so does any other young sign of life
		{"UserPromptSubmit five minutes ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(5 * time.Minute)}, StateWorking},
		{"PreToolUse just now", Facts{LastEvent: "PreToolUse", LastEventAt: ago(time.Second)}, StateWorking},
		{"PostToolUse under the limit", Facts{LastEvent: "PostToolUse", LastEventAt: ago(10*time.Minute - time.Second)}, StateWorking},
		{"a SessionStart half a minute ago", Facts{LastEvent: "SessionStart", LastEventAt: ago(30 * time.Second)}, StateWorking},
		{"a Notification a minute ago that asks nothing", Facts{LastEvent: "Notification", LastEventAt: ago(time.Minute)}, StateWorking},

		// idle: last event is Stop or SubagentStop with nothing after
		{"Stop", Facts{LastEvent: "Stop", LastEventAt: ago(time.Minute)}, StateIdle},
		{"SubagentStop", Facts{LastEvent: "SubagentStop", LastEventAt: ago(3 * time.Minute)}, StateIdle},
		{"a Stop two hours old stays idle, never stuck", Facts{LastEvent: "Stop", LastEventAt: ago(2 * time.Hour)}, StateIdle},

		// stuck: working, but no event for 10 min
		{"a UserPromptSubmit exactly ten minutes old is no longer young", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(10 * time.Minute)}, StateStuck},
		{"a UserPromptSubmit eleven minutes ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(11 * time.Minute)}, StateStuck},
		{"a SessionStart fifteen minutes ago", Facts{LastEvent: "SessionStart", LastEventAt: ago(15 * time.Minute)}, StateStuck},
		{"a Notification an hour ago that asks nothing", Facts{LastEvent: "Notification", LastEventAt: ago(time.Hour)}, StateStuck},
		{"a torn timestamp reads as long ago", Facts{LastEvent: "UserPromptSubmit", LastEventAt: time.Time{}}, StateStuck},

		// hooks stale: the pid is alive and the transcript is newer than the last report by more
		// than 2 minutes, so the transcript is the session's clock — working while it moves,
		// idle once it is quiet, never stuck
		{"pid alive and hooks stale under a moving transcript", Facts{LastEvent: "PostToolUse", LastEventAt: ago(30 * time.Minute), TranscriptAt: ago(10 * time.Second), Running: true}, StateWorking},
		{"pid alive and hooks stale after a Stop", Facts{LastEvent: "Stop", LastEventAt: ago(time.Hour), TranscriptAt: ago(time.Minute), Running: true}, StateWorking},
		{"pid alive and hooks stale over a transcript quiet past the limit", Facts{LastEvent: "PostToolUse", LastEventAt: ago(time.Hour), TranscriptAt: ago(20 * time.Minute), Running: true}, StateIdle},
		{"a transcript that never reported, moving", Facts{TranscriptAt: ago(5 * time.Second), Running: true}, StateWorking},
		{"an open item holds a hooks-stale session", Facts{LastEvent: "PostToolUse", LastEventAt: ago(30 * time.Minute), TranscriptAt: ago(10 * time.Second), Running: true, OpenItem: true}, StateNeedsYou},
		{"a pid gone under a moving transcript", Facts{LastEvent: "PostToolUse", LastEventAt: ago(30 * time.Minute), TranscriptAt: ago(10 * time.Second), Ended: true}, StateEnded},
		{"a dead pid under a moving transcript keeps the reports' reading", Facts{LastEvent: "PostToolUse", LastEventAt: ago(30 * time.Minute), TranscriptAt: ago(10 * time.Second)}, StateStuck},
		{"a transcript ahead by under the limit keeps the reports' reading", Facts{LastEvent: "UserPromptSubmit", LastEventAt: ago(11 * time.Minute), TranscriptAt: ago(10 * time.Minute), Running: true}, StateStuck},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StateOf(tc.f, stateNow); got != tc.want {
				t.Errorf("StateOf(%+v) = %q, want %q", tc.f, got, tc.want)
			}
		})
	}
}

func TestHooksStaleReadsTheTranscriptAgainstTheReports(t *testing.T) {
	cases := []struct {
		name string
		f    Facts
		want bool
	}{
		{"pid alive, transcript three minutes ahead", Facts{LastEventAt: ago(4 * time.Minute), TranscriptAt: ago(time.Minute), Running: true}, true},
		{"pid alive, a transcript that never reported", Facts{TranscriptAt: ago(time.Second), Running: true}, true},
		{"pid alive, transcript exactly two minutes ahead", Facts{LastEventAt: ago(3 * time.Minute), TranscriptAt: ago(time.Minute), Running: true}, false},
		{"pid alive, a session that keeps reporting", Facts{LastEventAt: ago(time.Second), TranscriptAt: ago(2 * time.Second), Running: true}, false},
		{"pid alive, no transcript", Facts{LastEventAt: ago(time.Hour), Running: true}, false},
		{"pid dead, transcript far ahead", Facts{LastEventAt: ago(time.Hour), TranscriptAt: ago(time.Second)}, false},
		{"an ended session, transcript far ahead", Facts{LastEventAt: ago(time.Hour), TranscriptAt: ago(time.Second), Running: true, Ended: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := HooksStale(tc.f); got != tc.want {
				t.Errorf("HooksStale(%+v) = %v, want %v", tc.f, got, tc.want)
			}
		})
	}
}

// TestTheRowCarriesTheTimeOfItsFirstEvent: the row's StartedAt is its earliest stored event's
// time, the moment the session entered the registry — not its last event's — and the json
// names it started_at. A session the registry only knows from its transcript has no stored
// event and carries none.
func TestTheRowCarriesTheTimeOfItsFirstEvent(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-live", LastEvent: "SessionStart", LastEventAt: "2026-10-16T08:55:00.000Z"},
		{SessionID: "s-live", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-live", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:25.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.LastEvent, err)
		}
	}
	const first = "2026-10-16T08:55:00.000Z"
	rows, err := db.Sessions(ctx, SessionsFilter{}, readNow)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("sessions = %d rows, want 1", len(rows))
	}
	if got := rows[0].StartedAt; got != first {
		t.Errorf("the listed row's StartedAt = %q, want the first event's time %q", got, first)
	}
	row, err := db.SessionRow(ctx, "s-live", readNow)
	if err != nil || row == nil {
		t.Fatalf("the session row: %v", err)
	}
	if row.StartedAt != first {
		t.Errorf("the read row's StartedAt = %q, want %q", row.StartedAt, first)
	}
	encoded, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatalf("encode the row: %v", err)
	}
	if !strings.Contains(string(encoded), `"started_at":"`+first+`"`) {
		t.Errorf("the row's json = %s, want started_at carrying %q", encoded, first)
	}
	ghost := TranscriptRow("s-ghost", "/shop", ago(time.Second), readNow)
	ghostJSON, err := json.Marshal(ghost)
	if err != nil {
		t.Fatalf("encode the transcript row: %v", err)
	}
	if ghost.StartedAt != "" || strings.Contains(string(ghostJSON), "started_at") {
		t.Errorf("a transcript-only row = %+v as %s, want no started_at", ghost, ghostJSON)
	}
}

// TestALaterReportNeverMovesStartedAt: the start is the first report's stamp, and no later
// report, however much later, moves it.
func TestALaterReportNeverMovesStartedAt(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-live", LastEvent: "SessionStart", LastEventAt: "2026-10-16T08:55:00.000Z"},
		{SessionID: "s-live", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:20.000Z"},
		{SessionID: "s-live", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:25.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.LastEvent, err)
		}
	}
	row, err := db.SessionRow(ctx, "s-live", readNow)
	if err != nil || row == nil {
		t.Fatalf("the session row: %v", err)
	}
	if row.StartedAt != "2026-10-16T08:55:00.000Z" {
		t.Errorf("StartedAt after two later reports = %q, want the first report's 2026-10-16T08:55:00.000Z", row.StartedAt)
	}
}
