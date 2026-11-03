package hub

// The transcript's word on a registry row, the three link levels, and the message of a refused
// answer.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// reportedRow answers the row of a session whose last report was a Stop at 09:00:25.
func reportedRow(t *testing.T) SessionRow {
	t.Helper()
	db := openForTest(t)
	if err := db.Report(context.Background(),
		Report{SessionID: "s-quiet", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:25.000Z"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	row, err := db.SessionRow(context.Background(), "s-quiet", readNow)
	if err != nil || row == nil {
		t.Fatalf("the session row: %v", err)
	}
	return *row
}

// TestSeeTranscriptReadsAMovingTranscriptAheadOfItsReports: a running session whose transcript
// is far ahead of its last report has stale hooks and reads working, not idle.
func TestSeeTranscriptReadsAMovingTranscriptAheadOfItsReports(t *testing.T) {
	row := reportedRow(t)
	if row.State != StateIdle || row.HooksStale {
		t.Fatalf("the row before the transcript = %q stale %v, want idle and fresh", row.State, row.HooksStale)
	}
	at := time.Date(2026, 10, 16, 9, 10, 0, 0, time.UTC)
	asked := 0
	row.SeeTranscript(at, func() bool { asked++; return true }, at.Add(time.Second))
	if !row.HooksStale || row.State != StateWorking {
		t.Errorf("the row = %q stale %v, want working with stale hooks", row.State, row.HooksStale)
	}
	if asked != 1 {
		t.Errorf("the process was asked %d times, want once", asked)
	}
}

// TestSeeTranscriptTrustsTheReportsWhenTheProcessIsGone: a transcript far ahead of the reports
// changes nothing when the session's process does not live.
func TestSeeTranscriptTrustsTheReportsWhenTheProcessIsGone(t *testing.T) {
	row := reportedRow(t)
	at := time.Date(2026, 10, 16, 9, 10, 0, 0, time.UTC)
	row.SeeTranscript(at, func() bool { return false }, at.Add(time.Second))
	if row.HooksStale || row.State != StateIdle {
		t.Errorf("the row = %q stale %v, want idle and fresh", row.State, row.HooksStale)
	}
}

// TestSeeTranscriptNeverAsksTheProcessForATranscriptThatKeepsUp: the process is asked only when
// the transcript runs ahead of the last report by more than the limit.
func TestSeeTranscriptNeverAsksTheProcessForATranscriptThatKeepsUp(t *testing.T) {
	row := reportedRow(t)
	at := time.Date(2026, 10, 16, 9, 0, 30, 0, time.UTC)
	row.SeeTranscript(at, func() bool { t.Error("the process was asked"); return true }, at)
	if row.HooksStale || row.State != StateIdle {
		t.Errorf("the row = %q stale %v, want idle and fresh", row.State, row.HooksStale)
	}
}

// TestValidLevelNamesTheThreeLinkLevels: overhear, digest and on-request are levels; anything
// else, an empty word and a different case included, is not.
func TestValidLevelNamesTheThreeLinkLevels(t *testing.T) {
	for _, level := range []string{LevelOverhear, LevelDigest, LevelOnRequest} {
		if !ValidLevel(level) {
			t.Errorf("ValidLevel(%q) = false, want true", level)
		}
	}
	for _, level := range []string{"", "Digest", "always", "on_request"} {
		if ValidLevel(level) {
			t.Errorf("ValidLevel(%q) = true, want false", level)
		}
	}
}

// TestAnswersErrorReadsAsItsCalmLine: the refusal's text is the one line the dashboard shows.
func TestAnswersErrorReadsAsItsCalmLine(t *testing.T) {
	err := CheckAnswers("Bash", `{"command":"ls"}`, map[string]string{"x": "y"})
	var answers *AnswersError
	if !errors.As(err, &answers) {
		t.Fatalf("CheckAnswers = %v, want an *AnswersError", err)
	}
	if err.Error() != "answers: only a question takes answers" {
		t.Errorf("Error() = %q, want the refusal's line", err.Error())
	}
}
