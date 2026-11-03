package server

// A reply to an ask the owner sent lands in Needs you and pushes once; a reply to a session's
// ask makes no item. The hub watches each channel it opens, so no poll scans for replies.

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

func TestAReplyToTheOwnerPushesOnce(t *testing.T) {
	ctx := context.Background()
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	keys := newBrowserKeys(t)
	subscribeDevice(t, s, "phone", service.srv.URL, keys, hub.NotifyNeeds)
	s.watchChannels(ctx)
	if err := s.registry.Report(ctx, hub.Report{
		SessionID: "s-alice", Channel: "team", Member: "alice",
		LastEvent: "SessionStart", LastEventAt: jsscalar.ISOTime(pushClock),
	}); err != nil {
		t.Fatalf("report alice: %v", err)
	}

	ch, err := s.hub.Get(ctx, "team")
	if err != nil {
		t.Fatalf("open the channel: %v", err)
	}
	for _, name := range []string{"alice", "lead"} {
		if _, err := ch.Join(ctx, name, channel.JoinOpts{}); err != nil {
			t.Fatalf("join %s: %v", name, err)
		}
	}
	answer := func(from, to, question, reply string) {
		t.Helper()
		if _, err := ch.Send(ctx, from, []string{to}, question, channel.SendOpts{Ask: true}); err != nil {
			t.Fatalf("%s asks %s: %v", from, to, err)
		}
		inbox, err := ch.Inbox(ctx, to)
		if err != nil || len(inbox) == 0 {
			t.Fatalf("inbox of %s = %v, %v", to, inbox, err)
		}
		if _, err := ch.Reply(ctx, to, inbox[len(inbox)-1].Seq, reply); err != nil {
			t.Fatalf("%s replies: %v", to, err)
		}
	}

	// a reply to a session's ask is the session's own business
	answer("lead", "alice", "which port?", "4835")
	if items, _ := s.registry.Items(ctx, pushClock); len(items) != 0 {
		t.Fatalf("items after a reply to a session = %+v, want none", items)
	}

	answer(channel.Owner, "alice", "ship it?", "yes, after the tests")
	items, err := s.registry.Items(ctx, pushClock)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 1 || items[0].Kind != hub.KindReply || items[0].SessionID != "s-alice" ||
		items[0].Summary != "yes, after the tests" {
		t.Fatalf("items after a reply to the owner = %+v, want one reply item of s-alice", items)
	}

	s.sweepPushes(ctx)
	s.sweepPushes(ctx)
	if service.count() != 1 {
		t.Fatalf("pushes for one reply over two sweeps = %d, want 1", service.count())
	}

	if err := s.registry.DismissItem(ctx, items[0].ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if items, _ = s.registry.Items(ctx, pushClock); len(items) != 0 {
		t.Fatalf("items after the dismiss = %+v, want none", items)
	}
}

// The words an agent uses for it, ask and reply, reach the same item.
func TestAReplyToAnOwnerAskMadeWithTheToolsGrowsOneItem(t *testing.T) {
	ctx := context.Background()
	service := newFakePush(t)
	s := pushServerForTest(t, service.srv.URL, func() time.Time { return pushClock })
	s.watchChannels(ctx)
	if err := s.registry.Report(ctx, hub.Report{
		SessionID: "s-alice", Channel: "team", Member: "alice",
		LastEvent: "SessionStart", LastEventAt: jsscalar.ISOTime(pushClock),
	}); err != nil {
		t.Fatalf("report alice: %v", err)
	}
	ch, err := s.hub.Get(ctx, "team")
	if err != nil {
		t.Fatalf("open the channel: %v", err)
	}
	if _, err := ch.Join(ctx, "alice", channel.JoinOpts{}); err != nil {
		t.Fatalf("join alice: %v", err)
	}
	asked, err := channel.RunOp(ctx, "ask", ch, channel.Owner, jsfmt.Object([]string{"to", "msg"},
		[]*jsfmt.Value{jsfmt.String("alice"), jsfmt.String("ship it?")}))
	if err != nil {
		t.Fatalf("the owner asks: %v", err)
	}
	if _, err := channel.RunOp(ctx, "reply", ch, "alice", jsfmt.Object([]string{"seq", "msg"},
		[]*jsfmt.Value{jsfmt.Number(float64(asked.Result.(channel.Ev).Seq)), jsfmt.String("yes")})); err != nil {
		t.Fatalf("alice replies: %v", err)
	}
	items, err := s.registry.Items(ctx, pushClock)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 1 || items[0].Kind != hub.KindReply || items[0].Summary != "yes" {
		t.Fatalf("items = %+v, want one reply item saying yes", items)
	}
}
