package hub

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// SessionCheckpoints reads every live session's context and the recency of its last commit
// and last handoff digest, the facts the context_checkpoint tip reads. Exercised over a real
// hub.db in a temp home, written through Report the way the hooks write it.

func TestSessionCheckpointsGatherContextCommitsAndDigests(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-live", LastEvent: "SessionStart", LastEventAt: "2026-10-16T10:00:00.000Z"},
		{SessionID: "s-live", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:01:00.000Z", ContextPercent: 72.5},
		{SessionID: "s-live", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:02:00.000Z", Tool: "Bash", ToolTarget: "echo hi"},
		{SessionID: "s-live", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:03:00.000Z", Tool: "Bash", ToolTarget: "cd repo && git commit -m fix"},
		{SessionID: "s-live", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:04:00.000Z", Tool: "Bash", ToolTarget: "git commit -m second"},
		{SessionID: "s-live", LastEvent: "Stop", LastEventAt: "2026-10-16T10:05:00.000Z", Digest: "wrapped the step"},
		{SessionID: "s-quiet", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T10:01:00.000Z", ContextPercent: 80},
		{SessionID: "s-quiet", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T09:58:00.000Z", Tool: "Bash", ToolTarget: "git commit -m early"},
		{SessionID: "s-quiet", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T10:02:00.000Z", Tool: "Bash", ToolTarget: "go build ./..."},
		{SessionID: "s-ended", LastEvent: "SessionStart", LastEventAt: "2026-10-16T09:00:00.000Z", ContextPercent: 90},
		{SessionID: "s-ended", LastEvent: "SessionEnd", LastEventAt: "2026-10-16T09:30:00.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("Report(%s %s): %v", rep.SessionID, rep.LastEvent, err)
		}
	}
	got, err := db.SessionCheckpoints(ctx, at("2026-10-16T10:00:00.000Z"))
	if err != nil {
		t.Fatalf("SessionCheckpoints: %v", err)
	}
	want := []SessionCheckpoint{
		{
			SessionID:      "s-live",
			ContextPercent: 72.5,
			LastEventAt:    at("2026-10-16T10:05:00.000Z"),
			LastCommitAt:   at("2026-10-16T10:04:00.000Z"),
			LastHandoffAt:  at("2026-10-16T10:05:00.000Z"),
		},
		{
			SessionID:      "s-quiet",
			ContextPercent: 80,
			LastEventAt:    at("2026-10-16T10:02:00.000Z"),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SessionCheckpoints() = %+v, want %+v", got, want)
	}
}

// TestGitCommitCommandShapes pins the command lines SessionCheckpoints counts as a commit:
// the verb at the line's start or after a `&&` or a `;`, with git's flags and env
// assignments allowed before it. `git commit-tree` and a quoted `git commit` are not commits.
func TestGitCommitCommandShapes(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		match bool
	}{
		{"a plain commit", "git commit -m x", true},
		{"git flags before the verb", "git -c a=b commit -S -m x", true},
		{"a commit after a chained cd", "cd repo && git commit -m x", true},
		{"commit-tree is not a commit", "git commit-tree HEAD", false},
		{"an echoed mention is not a commit", "echo git commit", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitCommitRe.MatchString(tc.line); got != tc.match {
				t.Errorf("gitCommitRe.MatchString(%q) = %v, want %v", tc.line, got, tc.match)
			}
		})
	}
}

// at parses one of the test's RFC3339 stamps, failing the test when it does not parse.
func at(stamp string) time.Time {
	tm, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		panic("at(" + stamp + "): " + err.Error())
	}
	return tm
}
