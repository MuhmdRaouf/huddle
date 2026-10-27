package hub

import (
	"context"
	"reflect"
	"testing"
)

// OpenTurns reads every live session's open turn: the turn its newest prompt in the window
// started, with the files it edited and the commits it ran since that start. Exercised over a
// real hub.db in a temp home, written through Report the way the hooks write it.
//
// The fixture walks one window from 10:00: s-turn's edits belong to its first turn, its second
// prompt starts a new one; s-stopped stopped; s-ended ended; s-commit committed after its
// prompt; s-oldcommit committed before its second prompt; s-noprompt never prompted in the
// window; s-early prompted before the window.

func TestOpenTurnsReadsLiveTurns(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-turn", LastEvent: "SessionStart", LastEventAt: "2026-10-16T10:00:00.000Z"},
		{SessionID: "s-turn", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:05:00.000Z"},
		{SessionID: "s-turn", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:06:00.000Z", Tool: "Edit", ToolTarget: "a.go"},
		{SessionID: "s-turn", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:07:00.000Z", Tool: "Edit", ToolTarget: "a.go"},
		{SessionID: "s-turn", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:08:00.000Z", Tool: "Write", ToolTarget: "b.go"},
		{SessionID: "s-turn", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:20:00.000Z"},
		{SessionID: "s-turn", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:21:00.000Z", Tool: "Bash", ToolTarget: "go build ./..."},
		{SessionID: "s-turn", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:22:00.000Z", Tool: "Bash", ToolTarget: "git commit -m wip"},
		{SessionID: "s-edit", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:10:00.000Z"},
		{SessionID: "s-edit", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:11:00.000Z", Tool: "Edit", ToolTarget: "a.go"},
		{SessionID: "s-edit", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:12:00.000Z", Tool: "Edit", ToolTarget: "a.go"},
		{SessionID: "s-edit", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:13:00.000Z", Tool: "Write", ToolTarget: "b.go"},
		{SessionID: "s-stopped", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:05:00.000Z"},
		{SessionID: "s-stopped", LastEvent: "Stop", LastEventAt: "2026-10-16T10:30:00.000Z"},
		{SessionID: "s-ended", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:05:00.000Z"},
		{SessionID: "s-ended", LastEvent: "SessionEnd", LastEventAt: "2026-10-16T10:15:00.000Z"},
		{SessionID: "s-commit", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:05:00.000Z"},
		{SessionID: "s-commit", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:06:00.000Z", Tool: "Bash", ToolTarget: "echo hi"},
		{SessionID: "s-commit", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:07:00.000Z", Tool: "Bash", ToolTarget: "git commit -m fix"},
		{SessionID: "s-oldcommit", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:02:00.000Z"},
		{SessionID: "s-oldcommit", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:10:00.000Z", Tool: "Bash", ToolTarget: "git commit -m earlier"},
		{SessionID: "s-oldcommit", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:20:00.000Z"},
		{SessionID: "s-noprompt", LastEvent: "SessionStart", LastEventAt: "2026-10-16T10:00:00.000Z"},
		{SessionID: "s-noprompt", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:30:00.000Z", Tool: "Edit", ToolTarget: "c.go"},
		{SessionID: "s-early", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:55:00.000Z"},
		{SessionID: "s-early", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:10:00.000Z", Tool: "Edit", ToolTarget: "d.go"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("Report(%s %s): %v", rep.SessionID, rep.LastEvent, err)
		}
	}
	got, err := db.OpenTurns(ctx, at("2026-10-16T10:00:00.000Z"))
	if err != nil {
		t.Fatalf("OpenTurns: %v", err)
	}
	want := []OpenTurn{
		{SessionID: "s-commit", StartedAt: at("2026-10-16T10:05:00.000Z"), LastCommitAt: at("2026-10-16T10:07:00.000Z")},
		{SessionID: "s-edit", StartedAt: at("2026-10-16T10:10:00.000Z"), Edits: 2},
		{SessionID: "s-oldcommit", StartedAt: at("2026-10-16T10:20:00.000Z")},
		{SessionID: "s-turn", StartedAt: at("2026-10-16T10:20:00.000Z"), LastCommitAt: at("2026-10-16T10:22:00.000Z")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OpenTurns() = %+v, want %+v", got, want)
	}
}
