package channel

import (
	"regexp"
	"slices"
	"testing"
)

var oldWords = regexp.MustCompile(`(?i)\b(publish\w*|broadcast\w*)`)

func TestTheToolListNamesMessageAskAndReply(t *testing.T) {
	names := []string{}
	for _, td := range ToolDefs() {
		names = append(names, td.Name)
		if w := oldWords.FindString(td.Description); w != "" {
			t.Errorf("the %s tool description says %q", td.Name, w)
		}
	}
	for _, want := range []string{"message", "ask", "reply"} {
		if !slices.Contains(names, want) {
			t.Errorf("the tool list lacks %s: %v", want, names)
		}
	}
	for _, gone := range []string{"send", "publish"} {
		if slices.Contains(names, gone) {
			t.Errorf("the tool list still names %s", gone)
		}
	}
}

func TestAMessageWithATopicWakesATopicWait(t *testing.T) {
	ch := joinedChannel(t)
	last, err := ch.LastSeq(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Ack(bg, "b", last); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		hit WaitHit
		err error
	}
	res := make(chan outcome, 1)
	go func() {
		h, err := ch.Wait(bg, "b", []string{"finding.*"}, 5)
		res <- outcome{h, err}
	}()
	until(t, "b waiting", waiting(t, ch, "b"))

	if _, err := RunOp(bg, "message", ch, "a", vv(t, `{"msg":"no topic"}`)); err != nil {
		t.Fatalf("message without a topic: %v", err)
	}
	if _, err := RunOp(bg, "message", ch, "a", vv(t, `{"topic":"finding.ready","msg":"the report","ref":"r.md","data":{"n":3}}`)); err != nil {
		t.Fatalf("message with a topic: %v", err)
	}

	got := <-res
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.hit.Kind != "event" || got.hit.Topic != "finding.ready" {
		t.Fatalf("wait = %+v, want the finding.ready message", got.hit)
	}
	if len(got.hit.Skipped) != 1 || got.hit.Skipped[0].Topic != "msg" {
		t.Errorf("skipped = %+v, want the topicless message", got.hit.Skipped)
	}
}

func TestAMessageKeepsRefDataAndTheReservedTopics(t *testing.T) {
	ch := joinedChannel(t)
	r, err := RunOp(bg, "message", ch, "a", vv(t, `{"to":"b","topic":"build.ready","msg":"green","ref":"api","data":{"n":3},"task":"T-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	ev := r.Result.(Ev)
	if ev.Topic != "build.ready" || deref(ev.Ref) != "api" || deref(ev.To) != "b" {
		t.Errorf("event = %+v, want build.ready about api to b", ev)
	}
	if dataNum(ev.Data, "n") != 3 || dataStr(ev.Data, "task") != "T-1" {
		t.Errorf("data = %s, want n and task", ev.Data.EncodeJSON())
	}
	for _, topic := range []string{"task.done", "reply", "control.pause", "build.*"} {
		_, err := RunOp(bg, "message", ch, "a", vv(t, `{"topic":"`+topic+`","msg":"x"}`))
		if errStatus(err) != 400 {
			t.Errorf("topic %s: status %d (%v), want 400", topic, errStatus(err), err)
		}
	}
}

func TestAnAskSendsAndReturnsAtOnce(t *testing.T) {
	ch := joinedChannel(t)
	r, err := RunOp(bg, "ask", ch, "a", vv(t, `{"to":"b","msg":"ready?"}`))
	if err != nil {
		t.Fatal(err)
	}
	ev := r.Result.(Ev)
	if ev.Topic != "ask" || !ev.NeedsReply || deref(ev.To) != "b" {
		t.Errorf("ask = %+v, want an ask for b that needs a reply", ev)
	}
	in, err := ch.Inbox(bg, "b")
	if err != nil || len(in) != 1 {
		t.Fatalf("b's inbox = %v, %v, want the ask", in, err)
	}
}

func TestAnAskWithNoToReachesEveryone(t *testing.T) {
	ch := joinedChannel(t)
	if _, err := ch.Join(bg, "c", JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	r, err := RunOp(bg, "ask", ch, "a", vv(t, `{"msg":"who is free?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ev := r.Result.(Ev); ev.To != nil || !ev.NeedsReply {
		t.Errorf("ask = %+v, want an ask to everyone", ev)
	}
	for _, name := range []string{"b", "c"} {
		if in, err := ch.Inbox(bg, name); err != nil || len(in) != 1 {
			t.Errorf("inbox of %s = %v, %v, want the ask", name, in, err)
		}
	}
}

func TestAPausedSessionStillMessagesAndAsksButCannotSendATopic(t *testing.T) {
	ch := joinedChannel(t)
	if _, err := ch.Control(bg, Owner, "a", "pause", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ op, args string }{
		{"message", `{"to":"b","msg":"I am stuck"}`},
		{"ask", `{"to":"b","msg":"can you unblock me?"}`},
	} {
		if _, err := RunOp(bg, c.op, ch, "a", vv(t, c.args)); err != nil {
			t.Errorf("paused %s: %v, want it accepted", c.op, err)
		}
	}
	_, err := RunOp(bg, "message", ch, "a", vv(t, `{"topic":"build.ready","msg":"green"}`))
	if errStatus(err) != 423 {
		t.Errorf("paused message with a topic: status %d (%v), want 423", errStatus(err), err)
	}
}
