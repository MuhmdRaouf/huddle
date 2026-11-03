package channel

import (
	"slices"
	"testing"
)

// TestAnAskToEveryoneWaitsOnEveryOtherSessionStillInTheChannel: an ask with no recipient waits on
// each other member in join order, the asker and a session that left excluded, and it closes
// once each of them replied.
func TestAnAskToEveryoneWaitsOnEveryOtherSessionStillInTheChannel(t *testing.T) {
	ch := threeMembers(t)
	if _, err := ch.Join(bg, "lead", JoinOpts{Role: sp("plans")}); err != nil {
		t.Fatalf("join lead: %v", err)
	}
	if _, err := ch.Leave(bg, "c", "done"); err != nil {
		t.Fatalf("leave c: %v", err)
	}
	q, err := ch.Send(bg, "lead", nil, "who has the port?", SendOpts{Ask: true})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}

	view := askView(t, ch, q.Seq)
	if !slices.Equal(view.To, []string{"a", "b"}) || !slices.Equal(view.Waiting, []string{"a", "b"}) || view.Closed {
		t.Fatalf("status = %+v, want a and b asked and waiting, still open", view)
	}
	for _, name := range []string{"a", "b"} {
		if _, err := ch.Reply(bg, name, q.Seq, "mine: "+name); err != nil {
			t.Fatalf("reply %s: %v", name, err)
		}
	}
	if view := askView(t, ch, q.Seq); !view.Closed || len(view.Waiting) != 0 || !slices.Equal(view.Replied, []string{"a", "b"}) {
		t.Errorf("status = %+v, want closed with a and b replied", view)
	}
}

// TestAskStatusRefusesAnEventThatIsNoAsk: an event that is not there answers 404, and one that is
// a plain message answers 400.
func TestAskStatusRefusesAnEventThatIsNoAsk(t *testing.T) {
	ch := joinedChannel(t)
	msg, err := ch.Send(bg, "a", []string{"b"}, "hello", SendOpts{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := ch.AskStatus(bg, msg.Seq); errStatus(err) != 400 {
		t.Errorf("status of a plain message = %v, want a 400", err)
	}
	if _, err := ch.AskStatus(bg, 9999); errStatus(err) != 404 {
		t.Errorf("status of an event that is not there = %v, want a 404", err)
	}
}

// TestWaitRepliesAnswersEveryReplyAboveTheCursor: a wait on an ask that already has replies
// returns at once with the replies past the cursor and the tally behind them.
func TestWaitRepliesAnswersEveryReplyAboveTheCursor(t *testing.T) {
	ch, group := askedByLead(t)
	first := answerAsk(t, ch, "a", "1")
	second := answerAsk(t, ch, "b", "2")

	all, err := ch.WaitReplies(bg, "lead", group, 0, 5)
	if err != nil {
		t.Fatalf("WaitReplies: %v", err)
	}
	if all.Kind != "event" || len(all.Replies) != 2 || all.Replies[0] != (AskReply{From: "a", Seq: first.Seq, Answer: "1"}) ||
		all.Replies[1] != (AskReply{From: "b", Seq: second.Seq, Answer: "2"}) {
		t.Errorf("replies above 0 = %+v, want a's then b's", all)
	}
	if !slices.Equal(all.View.Waiting, []string{"c"}) {
		t.Errorf("the tally = %+v, want c still waiting", all.View)
	}

	later, err := ch.WaitReplies(bg, "lead", group, first.Seq, 5)
	if err != nil {
		t.Fatalf("WaitReplies after a's: %v", err)
	}
	if len(later.Replies) != 1 || later.Replies[0].From != "b" {
		t.Errorf("replies above a's = %+v, want b's alone", later.Replies)
	}
}

// TestWaitRepliesReturnsOnAClosedAskWithNothingNew: a closed ask ends the wait even when no reply
// sits above the cursor.
func TestWaitRepliesReturnsOnAClosedAskWithNothingNew(t *testing.T) {
	ch, group := askedByLead(t)
	last := answerAsk(t, ch, "a", "1")
	if _, err := ch.CloseAsk(bg, Owner, group); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := ch.WaitReplies(bg, "lead", group, last.Seq, 5)
	if err != nil {
		t.Fatalf("WaitReplies: %v", err)
	}
	if got.Kind != "event" || len(got.Replies) != 0 || !got.View.Closed {
		t.Errorf("got %+v, want an event with no new replies on a closed ask", got)
	}
}

// TestWaitRepliesRefusesASessionThatNeverJoined: a stranger's wait is refused with a 404.
func TestWaitRepliesRefusesASessionThatNeverJoined(t *testing.T) {
	ch, group := askedByLead(t)
	if _, err := ch.WaitReplies(bg, "stranger", group, 0, 0); errStatus(err) != 404 {
		t.Errorf("a stranger's wait = %v, want a 404", err)
	}
}
