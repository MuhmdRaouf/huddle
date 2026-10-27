package hub

import (
	"context"
	"maps"
	"path/filepath"
	"testing"
)

// TestMemberSessionsAnswersTheChannelsRoster pins the registry's answer to the channel's
// identity question: every session registered in the channel with a member, ended ones
// included, each subagent folded onto the top-level member it works for, and nothing from
// another channel or from a session without a member.
func TestMemberSessionsAnswersTheChannelsRoster(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	for _, rep := range []Report{
		{SessionID: "s-a", Channel: "ch", Member: "a",
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-27T09:00:00.000Z"},
		{SessionID: "s-sub", Channel: "ch", Member: "a.worker",
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-27T09:00:01.000Z"},
		{SessionID: "s-b", Channel: "ch", Member: "b",
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-27T09:00:02.000Z"},
		{SessionID: "s-end", Channel: "ch", Member: "c",
			LastEvent: "SessionEnd", LastEventAt: "2026-10-27T09:00:03.000Z"},
		{SessionID: "s-nobody", Channel: "ch",
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-27T09:00:04.000Z"},
		{SessionID: "s-other", Channel: "other", Member: "a",
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-27T09:00:05.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	got, err := db.MemberSessions(ctx, "ch")
	if err != nil {
		t.Fatalf("member sessions: %v", err)
	}
	want := map[string]string{"s-a": "a", "s-sub": "a", "s-b": "b", "s-end": "c"}
	if !maps.Equal(got, want) {
		t.Fatalf("member sessions = %v, want %v", got, want)
	}
}
