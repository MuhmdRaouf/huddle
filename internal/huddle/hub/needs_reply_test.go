package hub

// A session's reply to an ask the owner sent grows one reply item: keyed by the reply's event so
// a replayed one grows nothing, one line cut to 120 characters, open until the owner answers or
// dismisses it.

import (
	"context"
	"strings"
	"testing"
)

func TestAReplyToAnOwnerAskMakesOneNeed(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	long := strings.Repeat("port ", 40)
	in := ItemIn{Key: "reply:team:7", SessionID: "s-alice", Summary: long}

	for range 2 {
		if err := db.AddReplyItem(ctx, in, "2026-10-16T09:00:00.000Z"); err != nil {
			t.Fatalf("add the reply item: %v", err)
		}
	}

	items, err := db.Items(ctx, readNow)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v, want the one reply item for two identical adds", items)
	}
	item := items[0]
	if item.Kind != KindReply || item.SessionID != "s-alice" || item.State != StateOpen {
		t.Errorf("item = %+v, want an open reply item of s-alice", item)
	}
	if len([]rune(item.Summary)) > 120 || !strings.HasPrefix(item.Summary, "port port") {
		t.Errorf("summary = %q, want the first 120 characters of the reply on one line", item.Summary)
	}

	if err := db.DismissItem(ctx, item.ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if err := db.AddReplyItem(ctx, in, "2026-10-16T09:05:00.000Z"); err != nil {
		t.Fatalf("add after the dismiss: %v", err)
	}
	if items, _ = db.Items(ctx, readNow); len(items) != 0 {
		t.Errorf("items after a dismiss and a replay = %+v, want none: a dismissed reply stays closed", items)
	}
}
