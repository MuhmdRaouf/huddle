package hub

// ItemHistory's read-only history for the insights route: every needs-you item the window
// covers, oldest first, each Bash permission ask carrying the command its input holds.

import (
	"context"
	"testing"
	"time"
)

// insightNow is one fixed moment the case's window builds around.
func insightNow() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }

func TestItemHistoryAnswersAsksWithTheirCommand(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()

	// decide one open permission item two minutes after it was raised: the hook's poll
	// keeps it fresh, then the answer lands
	decide := func(id int64, raised time.Time) {
		t.Helper()
		at := raised.Add(2 * time.Minute)
		if err := db.TouchPoll(ctx, id, isoPerm(at)); err != nil {
			t.Fatalf("poll item %d: %v", id, err)
		}
		if won, err := db.DecidePermission(ctx, id, "allow", "", isoPerm(at), at); err != nil || !won {
			t.Fatalf("decide item %d = (%v, %v), want the decision to win", id, won, err)
		}
	}

	// two Bash asks answered allow, one Read ask still open, one question, and one Bash ask
	// from eight days before the window
	bashID := growPermission(t, db, "s-1", "Bash", `{"command":"go test ./internal/kanban/..."}`, insightNow().Add(1*time.Hour))
	decide(bashID, insightNow().Add(1*time.Hour))
	vetID := growPermission(t, db, "s-2", "Bash", `{"command":"go vet ./..."}`, insightNow().Add(2*time.Hour))
	decide(vetID, insightNow().Add(2*time.Hour))
	growPermission(t, db, "s-1", "Read", `{"file_path":"/tmp/x"}`, insightNow().Add(3*time.Hour))
	if err := db.addItem(ctx, ItemIn{
		Key: "insights-question", SessionID: "s-3", Kind: KindQuestion, Summary: "Pick a database",
	}, isoPerm(insightNow().Add(4*time.Hour))); err != nil {
		t.Fatalf("add the question: %v", err)
	}
	oldID := growPermission(t, db, "s-1", "Bash", `{"command":"npm test"}`, insightNow().Add(-8*24*time.Hour))
	decide(oldID, insightNow().Add(-8*24*time.Hour).Add(2*time.Minute))

	records, err := db.ItemHistory(ctx, insightNow())
	if err != nil {
		t.Fatalf("read the history: %v", err)
	}
	want := []ItemRecord{
		{
			SessionID: "s-1", Kind: KindPermission, Tool: "Bash",
			Command: "go test ./internal/kanban/...", Decision: "allow",
			CreatedAt: insightNow().Add(1 * time.Hour), DecidedAt: insightNow().Add(62 * time.Minute),
		},
		{
			SessionID: "s-2", Kind: KindPermission, Tool: "Bash",
			Command: "go vet ./...", Decision: "allow",
			CreatedAt: insightNow().Add(2 * time.Hour), DecidedAt: insightNow().Add(122 * time.Minute),
		},
		{
			SessionID: "s-1", Kind: KindPermission, Tool: "Read",
			CreatedAt: insightNow().Add(3 * time.Hour),
		},
		{
			SessionID: "s-3", Kind: KindQuestion,
			CreatedAt: insightNow().Add(4 * time.Hour),
		},
	}
	if len(records) != len(want) {
		t.Fatalf("the history holds %d records, want %d: %+v", len(records), len(want), records)
	}
	for i, got := range records {
		if got != want[i] {
			t.Errorf("record %d = %+v, want %+v", i, got, want[i])
		}
	}
}
