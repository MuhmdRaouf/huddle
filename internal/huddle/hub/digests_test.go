package hub

// The digests table (SPEC 8): each Stop report upserts its session's newest digest, a peer
// holds at most one pending digest per sender (the next one drops the one before it), and a
// digest message names the session it came from.

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestAReportWithADigestKeepsTheNewest(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	stop := func(digest string) Report {
		return Report{SessionID: "s-1", LastEvent: "Stop", LastEventAt: isoPerm(permNow), Digest: digest}
	}
	if err := db.Report(ctx, stop("first")); err != nil {
		t.Fatalf("the first digest report: %v", err)
	}
	text, at, ok, err := db.DigestOf(ctx, "s-1")
	if err != nil || !ok || text != "first" || at != isoPerm(permNow) {
		t.Fatalf("DigestOf = (%q, %q, %v, %v), want the first digest", text, at, ok, err)
	}
	if err := db.Report(ctx, stop("second")); err != nil {
		t.Fatalf("the second digest report: %v", err)
	}
	if text, _, _, _ = db.DigestOf(ctx, "s-1"); text != "second" {
		t.Fatalf("DigestOf = %q, want the newest digest", text)
	}
	// a report without a digest never erases the one the session already stored
	if err := db.Report(ctx, Report{SessionID: "s-1", LastEvent: "PreToolUse", LastEventAt: isoPerm(permNow)}); err != nil {
		t.Fatalf("the report without a digest: %v", err)
	}
	if text, _, _, _ = db.DigestOf(ctx, "s-1"); text != "second" {
		t.Errorf("DigestOf after a digestless report = %q, want second kept", text)
	}
	if _, _, ok, err = db.DigestOf(ctx, "s-2"); err != nil || ok {
		t.Errorf("DigestOf of a session with none = (%v, %v), want no digest and no error", ok, err)
	}
}

func TestDropPendingDigestsLeavesOtherMessages(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	at := isoPerm(permNow)
	queue := func(in MessageIn) {
		if _, err := db.QueueMessage(ctx, in, at); err != nil {
			t.Fatalf("queue %v: %v", in, err)
		}
	}
	queue(MessageIn{SessionID: "s-b", FromKind: FromDigest, FromName: "s-a", Text: "old from a"})
	queue(MessageIn{SessionID: "s-b", FromKind: FromOwner, Text: "the owner's"})
	queue(MessageIn{SessionID: "s-c", FromKind: FromDigest, FromName: "s-a", Text: "for someone else"})
	queue(MessageIn{SessionID: "s-b", FromKind: FromDigest, FromName: "s-z", Text: "from another peer"})
	if err := db.DropPendingDigests(ctx, "s-a", "s-b"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	taken, err := db.TakeMessages(ctx, "s-b", at)
	if err != nil {
		t.Fatalf("take: %v", err)
	}
	if len(taken) != 2 {
		t.Fatalf("s-b took %d messages (%v), want the two that are not s-a's digest", len(taken), taken)
	}
	for _, line := range taken {
		if strings.Contains(line, "old from a") {
			t.Errorf("the older digest from s-a survived the drop: %v", taken)
		}
	}
	// another session's digest from s-a, and s-b's digest from another sender, are untouched
	// by the first drop
	takenC, err := db.TakeMessages(ctx, "s-c", at)
	if err != nil || !slices.Contains(takenC, "Digest from session s-a via huddle: for someone else") {
		t.Errorf("the drop reached s-c's message (%v, %v)", takenC, err)
	}
	// the next digest from s-a drops first and arrives after: s-b is left holding the newest
	if err := db.DropPendingDigests(ctx, "s-a", "s-b"); err != nil {
		t.Fatalf("drop again: %v", err)
	}
	queue(MessageIn{SessionID: "s-b", FromKind: FromDigest, FromName: "s-a", Text: "fresh"})
	taken, err = db.TakeMessages(ctx, "s-b", at)
	if err != nil || !slices.Contains(taken, "Digest from session s-a via huddle: fresh") {
		t.Errorf("the fresh digest never reached s-b (%v, %v)", taken, err)
	}
}

func TestADigestMessageNamesItsSession(t *testing.T) {
	in := MessageIn{SessionID: "s-b", FromKind: FromDigest, FromName: "s-a", Text: "the digest"}
	if got := in.Header(); got != "Digest from session s-a via huddle: the digest" {
		t.Errorf("the header = %q, want the session named", got)
	}
}

// TestDigestRoutesNameAFailedStatement pins the torn-database refusals of the digests table.
func TestDigestRoutesNameAFailedStatement(t *testing.T) {
	ctx := context.Background()
	db := dbWithout(t, "digests")
	if err := db.SaveDigest(ctx, "s-1", "x", isoPerm(permNow)); err == nil {
		t.Fatal("a write to a broken digests table succeeded")
	}
	if _, _, _, err := db.DigestOf(ctx, "s-1"); err == nil {
		t.Fatal("a read from a broken digests table succeeded")
	}
	if err := db.Report(ctx, Report{SessionID: "s-1", LastEvent: "Stop",
		LastEventAt: isoPerm(permNow), Digest: "x"}); err == nil {
		t.Fatal("a digest upsert inside a report on a broken digests table succeeded")
	}
	msgs := dbWithout(t, "messages")
	if err := msgs.DropPendingDigests(ctx, "s-a", "s-b"); err == nil {
		t.Fatal("a drop on a broken messages table succeeded")
	}
}
