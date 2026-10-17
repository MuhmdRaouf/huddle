package hub

// The needs-you items (SPEC 6.1): a Notification that asks the owner grows a question item, a
// Stop grows a turn item when nobody opened the session since its last prompt, and the channels'
// tasks blocked on the owner grow blocked items through SyncBlocked. Snooze and dismiss move an
// item's state; opening the session answers its turn items.

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestANotificationThatAsksTheOwnerGrowsAQuestionItem(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	cases := []struct {
		name string
		n    *Notification
		want bool
	}{
		{"an elicitation dialog", &Notification{Type: "elicitation_dialog", Message: "Pick a database"}, true},
		{"an elicitation url dialog", &Notification{Type: "elicitation_url_dialog", Message: "Sign in"}, true},
		{"an agent that needs input", &Notification{Type: "agent_needs_input", Message: "the worker waits"}, true},
		{"a message that ends in a question", &Notification{Message: "Should I run the migration?"}, true},
		{"an unknown type whose message asks", &Notification{Type: "custom_thing", Message: "Go on?"}, true},
		{"a permission prompt", &Notification{Type: "permission_prompt", Message: "Claude needs your permission to use Bash"}, false},
		{"a statement", &Notification{Message: "Claude needs your permission to use Bash"}, false},
		{"no notification at all", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.Report(ctx, Report{SessionID: "s-q", LastEvent: "Notification", LastEventAt: "2026-10-16T09:00:00.000Z", Notification: tc.n}); err != nil {
				t.Fatalf("report: %v", err)
			}
		})
	}
	items, err := db.Items(ctx, readNow)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	// the message-less cases and the statements grow nothing; the five asks grow one item each
	if len(items) != 5 {
		t.Fatalf("items = %d, want the five questions", len(items))
	}
	for _, item := range items {
		if item.Kind != KindQuestion || item.SessionID != "s-q" || item.State != StateOpen {
			t.Errorf("item = %+v, want an open question of s-q", item)
		}
	}
	first := items[len(items)-1] // newest last: the elicitation dialog, first reported
	if first.Summary != "Pick a database" {
		t.Errorf("summary = %q, want the notification's message", first.Summary)
	}
	// a session with an open item reads needs-you
	row, err := db.SessionRow(ctx, "s-q", readNow)
	if err != nil || row == nil {
		t.Fatalf("the row: %v %v", row, err)
	}
	if row.State != StateNeedsYou {
		t.Errorf("state = %q, want needs-you", row.State)
	}
	// the same notification replayed grows no second item
	if err := db.Report(ctx, Report{SessionID: "s-q", LastEvent: "Notification", LastEventAt: "2026-10-16T09:00:00.000Z",
		Notification: &Notification{Type: "elicitation_dialog", Message: "Pick a database"}}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if items, _ = db.Items(ctx, readNow); len(items) != 5 {
		t.Errorf("a replayed notification grew a sixth item: %+v", items)
	}
}

func TestAQuestionSummaryIsOneCleanedLine(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	// a control character, a bidi override and two lines in one message
	if err := db.Report(ctx, Report{SessionID: "s-c", LastEvent: "Notification", LastEventAt: "2026-10-16T09:00:00.000Z",
		Notification: &Notification{Type: "elicitation_dialog",
			Message: "first‬ line‮\x00second\r\nline\ttabbed"}}); err != nil {
		t.Fatalf("report: %v", err)
	}
	items, err := db.Items(ctx, readNow)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want the one question", items)
	}
	for _, bad := range []string{"\x00", "\r", "\n", "\t", "‬", "‮"} {
		if strings.Contains(items[0].Summary, bad) {
			t.Errorf("summary %q holds %q", items[0].Summary, bad)
		}
	}
	if items[0].Summary != "first line second line tabbed" {
		t.Errorf("summary = %q", items[0].Summary)
	}
}

func TestAStopGrowsATurnItemWhenNobodyOpenedTheSession(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	report := func(event, at string) {
		t.Helper()
		if err := db.Report(ctx, Report{SessionID: "s-t", LastEvent: event, LastEventAt: at}); err != nil {
			t.Fatalf("report %s: %v", event, err)
		}
	}
	kinds := func() map[string]int {
		t.Helper()
		items, err := db.Items(ctx, readNow)
		if err != nil {
			t.Fatalf("items: %v", err)
		}
		out := map[string]int{}
		for _, item := range items {
			out[item.Kind]++
		}
		return out
	}
	// a stop before any prompt grows nothing: no turn finished for the owner
	report("Stop", "2026-10-16T09:00:00.000Z")
	if kinds()[KindTurn] != 0 {
		t.Fatalf("a stop without a prompt grew a turn item: %+v", kinds())
	}
	report("UserPromptSubmit", "2026-10-16T09:01:00.000Z")
	report("Stop", "2026-10-16T09:02:00.000Z")
	if kinds()[KindTurn] != 1 {
		t.Fatalf("a finished turn the owner never opened grew no item: %+v", kinds())
	}
	// the owner opens the session: the turn item is answered, and a stop after that grows none
	if err := db.OpenTheSession(ctx, "s-t", "2026-10-16T09:03:00.000Z"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if kinds()[KindTurn] != 0 {
		t.Fatalf("the opened session's turn item stayed: %+v", kinds())
	}
	report("Stop", "2026-10-16T09:04:00.000Z")
	if kinds()[KindTurn] != 0 {
		t.Fatalf("a stop after the owner opened the session grew an item: %+v", kinds())
	}
	// a new prompt starts a new turn, and its stop grows a new item (nobody opened it since)
	report("UserPromptSubmit", "2026-10-16T09:05:00.000Z")
	report("Stop", "2026-10-16T09:06:00.000Z")
	if kinds()[KindTurn] != 1 {
		t.Fatalf("the second turn grew no item: %+v", kinds())
	}
	// a prompt withdraws the open turn item: the owner is talking to the session again
	report("UserPromptSubmit", "2026-10-16T09:07:00.000Z")
	if kinds()[KindTurn] != 0 {
		t.Fatalf("a new prompt left the old turn item open: %+v", kinds())
	}
	// a resume is the owner opening the session too
	report("Stop", "2026-10-16T09:08:00.000Z")
	report("SessionStart", "2026-10-16T09:09:00.000Z")
	if kinds()[KindTurn] != 0 {
		t.Fatalf("a resume left the turn item open: %+v", kinds())
	}
}

func TestSnoozeAndDismissMoveAnItem(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "s-s", LastEvent: "Notification", LastEventAt: "2026-10-16T09:00:00.000Z",
		Notification: &Notification{Type: "elicitation_dialog", Message: "Pick one"}}); err != nil {
		t.Fatalf("report: %v", err)
	}
	items, err := db.Items(ctx, readNow)
	if err != nil || len(items) != 1 {
		t.Fatalf("items: %v (%v)", items, err)
	}
	id := items[0].ID
	// a snooze hides the item until its time passes
	snoozedUntil := readNow.Add(15 * time.Minute)
	if err := db.SnoozeItem(ctx, id, snoozedUntil); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	if items, _ = db.Items(ctx, readNow); len(items) != 1 || items[0].State != StateSnoozed {
		t.Fatalf("the snoozed item = %+v, want state snoozed", items)
	}
	// a snoozed item does not make its session read needs-you
	if row, _ := db.SessionRow(ctx, "s-s", readNow); row.State != StateWorking {
		t.Errorf("a snoozed item left the session %q, want working", row.State)
	}
	later := snoozedUntil.Add(time.Second)
	if items, _ = db.Items(ctx, later); len(items) != 1 || items[0].State != StateOpen {
		t.Fatalf("the woken item = %+v, want state open", items)
	}
	// a dismiss ends the item for good
	if err := db.DismissItem(ctx, id); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if items, _ = db.Items(ctx, later); len(items) != 0 {
		t.Fatalf("the dismissed item stayed: %+v", items)
	}
	if err := db.SnoozeItem(ctx, id, later); err == nil {
		t.Error("snoozing a dismissed item worked, want the refusal")
	}
}

func TestAnsweringTheQuestionEndsTheItem(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, msg := range []string{"First question?", "Second question?"} {
		if err := db.Report(ctx, Report{SessionID: "s-a", LastEvent: "Notification", LastEventAt: "2026-10-16T09:00:00.000Z",
			Notification: &Notification{Type: "elicitation_dialog", Message: msg}}); err != nil {
			t.Fatalf("report: %v", err)
		}
	}
	if err := db.CloseQuestions(ctx, "s-a"); err != nil {
		t.Fatalf("close: %v", err)
	}
	if items, _ := db.Items(ctx, readNow); len(items) != 0 {
		t.Fatalf("the answered questions stayed: %+v", items)
	}
	if row, _ := db.SessionRow(ctx, "s-a", readNow); row.State == StateNeedsYou {
		t.Errorf("the session still reads needs-you after its answer")
	}
}

func TestSyncBlockedGrowsAndWithdrawsTheOwnerBlockedItems(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "s-b", Channel: "shop", Member: "api", LastEvent: "UserPromptSubmit",
		LastEventAt: "2026-10-16T09:00:00.000Z"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	// one task of s-b waits on a task the owner owns, one waits on another session's
	if err := db.SyncBlocked(ctx, []BlockedIn{
		{Key: "blocked:shop:t-1", SessionID: "s-b", Summary: "t-1 checkout waits on you"},
	}, "2026-10-16T09:01:00.000Z"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	items, err := db.Items(ctx, readNow)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v (%v), want the one blocked item", items, err)
	}
	if items[0].Kind != KindBlocked || items[0].SessionID != "s-b" || items[0].Summary != "t-1 checkout waits on you" {
		t.Errorf("the blocked item = %+v", items[0])
	}
	// the task moves: the item withdraws, and a dismissed one is not reopened while it stays
	if err := db.SyncBlocked(ctx, nil, "2026-10-16T09:02:00.000Z"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if items, _ = db.Items(ctx, readNow); len(items) != 0 {
		t.Fatalf("the unblocked task's item stayed: %+v", items)
	}
	if err := db.SyncBlocked(ctx, []BlockedIn{
		{Key: "blocked:shop:t-1", SessionID: "s-b", Summary: "t-1 checkout waits on you"},
	}, "2026-10-16T09:03:00.000Z"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	items, _ = db.Items(ctx, readNow)
	if len(items) != 1 {
		t.Fatalf("the regrown item = %+v, want it back", items)
	}
	if err := db.DismissItem(ctx, items[0].ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if err := db.SyncBlocked(ctx, []BlockedIn{
		{Key: "blocked:shop:t-1", SessionID: "s-b", Summary: "t-1 checkout waits on you"},
	}, "2026-10-16T09:04:00.000Z"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if items, _ = db.Items(ctx, readNow); len(items) != 0 {
		t.Fatalf("a sync reopened the dismissed item: %+v", items)
	}
}
