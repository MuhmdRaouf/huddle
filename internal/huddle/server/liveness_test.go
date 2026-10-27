package server

import (
	"context"
	"maps"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// TestRegistryLivenessReadsTheMembersSession pins the registry's answer to the channel's state
// question: a member whose session works reads working, one whose last report ran quiet past
// the limit reads stuck, a member no session registered is not known, and neither is a
// registry that cannot answer.
func TestRegistryLivenessReadsTheMembersSession(t *testing.T) {
	ctx := context.Background()
	db, err := hub.Open(ctx, filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	now := time.Date(2026, 10, 16, 9, 0, 30, 0, time.UTC)
	for _, rep := range []hub.Report{
		{SessionID: "s-work", Channel: "huddle", Member: "lead",
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:28.000Z"},
		{SessionID: "s-stuck", Channel: "huddle", Member: "helper",
			LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T08:45:30.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	live := registryLiveness{db: db}
	if state, known := live.MemberState(ctx, "huddle", "lead", now); !known || state != hub.StateWorking {
		t.Fatalf("lead = %q, %v, want %q and known", state, known, hub.StateWorking)
	}
	if state, known := live.MemberState(ctx, "huddle", "helper", now); !known || state != hub.StateStuck {
		t.Fatalf("helper = %q, %v, want %q and known", state, known, hub.StateStuck)
	}
	if state, known := live.MemberState(ctx, "huddle", "nobody", now); known || state != "" {
		t.Fatalf("nobody = %q, %v, want not known and no state", state, known)
	}
	if state, known := (registryLiveness{}).MemberState(ctx, "huddle", "lead", now); known || state != "" {
		t.Fatalf("nil db = %q, %v, want not known and no state", state, known)
	}
	sessions, known := live.MemberSessions(ctx, "huddle")
	if !known || !maps.Equal(sessions, map[string]string{"s-work": "lead", "s-stuck": "helper"}) {
		t.Fatalf("member sessions = %v, %v, want the two members and known", sessions, known)
	}
	if got, known := (registryLiveness{}).MemberSessions(ctx, "huddle"); known || got != nil {
		t.Fatalf("nil db member sessions = %v, %v, want not known and none", got, known)
	}
}
