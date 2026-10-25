package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// TestRegistryLivenessReadsTheMembersSession pins the registry's answer to the channel's
// liveness question: a member whose session works is alive, one whose last report ran quiet
// past the limit is not, a member no session registered is not known, and neither is a
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
	if alive, known := live.MemberAlive(ctx, "huddle", "lead", now); !alive || !known {
		t.Fatalf("lead = %v, %v, want alive and known", alive, known)
	}
	if alive, known := live.MemberAlive(ctx, "huddle", "helper", now); alive || !known {
		t.Fatalf("helper = %v, %v, want not alive and known", alive, known)
	}
	if alive, known := live.MemberAlive(ctx, "huddle", "nobody", now); alive || known {
		t.Fatalf("nobody = %v, %v, want not alive and not known", alive, known)
	}
	if alive, known := (registryLiveness{}).MemberAlive(ctx, "huddle", "lead", now); alive || known {
		t.Fatalf("nil db = %v, %v, want not alive and not known", alive, known)
	}
}
