package channel

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

func TestChannel(t *testing.T) {
	t.Run("every member sees every event but its own, also those addressed to another session", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.all", Msg: "1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.b", Msg: "2", To: sp("b")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "c", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.c", Msg: "3", To: sp("c")}); err != nil {
			t.Fatal(err)
		}
		// b joined fresh (a first join), after a's session.joined: it reads on from there
		ub, err := ch.Unread(bg, "b")
		if err != nil {
			t.Fatal(err)
		}
		if got := topics(ub); !slices.Equal(got, []string{"x.all", "x.b", "session.joined", "x.c"}) {
			t.Errorf("unread(b) topics = %v", got)
		}
		ua, err := ch.Unread(bg, "a")
		if err != nil {
			t.Fatal(err)
		}
		if got := topics(ua); !slices.Equal(got, []string{"session.joined", "session.joined"}) {
			t.Errorf("unread(a) topics = %v", got)
		}
		uc, err := ch.Unread(bg, "c")
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(topics(uc), "x.c") {
			t.Errorf("unread(c) topics = %v, want x.c in it", topics(uc))
		}
	})

	t.Run("a message to another session never wakes this one: it waits on, and sees it in skipped", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Join(bg, "c", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		last, err := ch.LastSeq(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Ack(bg, "b", last); err != nil {
			t.Fatal(err)
		}
		type hit struct {
			h   WaitHit
			err error
		}
		res := make(chan hit, 1)
		go func() { h, err := ch.Wait(bg, "b", []string{"msg"}, 5); res <- hit{h, err} }()
		until(t, "b waiting", waiting(t, ch, "b"))
		if _, err := ch.Send(bg, "a", sp("c"), "for c only", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		select {
		case <-res:
			t.Fatal("a message to another session woke the waiter")
		default:
		}
		if _, err := ch.Send(bg, "a", sp("b"), "for b", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		o := <-res
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.h.Kind != "event" || o.h.Msg == nil || *o.h.Msg != "for b" {
			t.Errorf("hit = %+v, want the event for b", o.h)
		}
		want := [][2]*string{{sp("c"), sp("for c only")}}
		if len(o.h.Skipped) != len(want) {
			t.Fatalf("skipped = %+v", o.h.Skipped)
		}
		for i, s := range o.h.Skipped {
			if s.To == nil || *s.To != *want[i][0] || s.Msg == nil || *s.Msg != *want[i][1] {
				t.Errorf("skipped[%d] = %+v, want %s %s", i, s, *want[i][0], *want[i][1])
			}
		}
		if _, err := ch.Ack(bg, "b", o.h.Seq); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("wait returns the first match with the skipped events, wakes on publish, and ack never moves back", func(t *testing.T) {
		ch := joinedChannel(t)
		last, err := ch.LastSeq(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Ack(bg, "b", last); err != nil {
			t.Fatal(err)
		}
		type hit struct {
			h   WaitHit
			err error
		}
		res := make(chan hit, 1)
		go func() { h, err := ch.Wait(bg, "b", []string{"build.*"}, 5); res <- hit{h, err} }()
		until(t, "b waiting", waiting(t, ch, "b"))
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "note.x", Msg: "skip me"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "build.ready", Msg: "go"}); err != nil {
			t.Fatal(err)
		}
		o := <-res
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.h.Kind != "event" || o.h.Topic != "build.ready" {
			t.Errorf("hit = %+v, want the build.ready event", o.h)
		}
		if len(o.h.Skipped) != 1 || o.h.Skipped[0].Topic != "note.x" {
			t.Errorf("skipped = %+v, want note.x", o.h.Skipped)
		}
		ack, err := ch.Ack(bg, "b", o.h.Seq)
		if err != nil {
			t.Fatal(err)
		}
		if ack.Cursor != o.h.Seq {
			t.Errorf("ack cursor = %d, want %d", ack.Cursor, o.h.Seq)
		}
		ack, err = ch.Ack(bg, "b", 1)
		if err != nil {
			t.Fatal(err)
		}
		if ack.Cursor != o.h.Seq {
			t.Errorf("ack(1) cursor = %d, want it to stay %d", ack.Cursor, o.h.Seq)
		}
	})

	t.Run("an ask wakes the addressee's wait as a message until answered; answering twice is refused", func(t *testing.T) {
		ch := joinedChannel(t)
		ask, err := ch.Send(bg, "a", sp("b"), "ready?", SendOpts{Ask: true})
		if err != nil {
			t.Fatal(err)
		}
		h, err := ch.Wait(bg, "b", []string{"nothing"}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if h.Kind != "message" || h.Seq != ask.Seq {
			t.Errorf("hit = %+v, want the ask at %d", h, ask.Seq)
		}
		if _, err := ch.Reply(bg, "b", ask.Seq, "yes"); err != nil {
			t.Fatal(err)
		}
		ib, err := ch.Inbox(bg, "b")
		if err != nil {
			t.Fatal(err)
		}
		if len(ib) != 0 {
			t.Errorf("inbox(b) = %d entries, want 0", len(ib))
		}
		if _, err := ch.Reply(bg, "b", ask.Seq, "again"); errStatus(err) != 409 {
			t.Errorf("second reply status = %d (%v), want 409", errStatus(err), err)
		}
		if _, err := ch.Reply(bg, "a", ask.Seq, "mine"); errStatus(err) != 400 {
			t.Errorf("own reply status = %d (%v), want 400", errStatus(err), err)
		}
	})

	t.Run("a broadcast ask needs a reply from each session", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Join(bg, "c", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		ask, err := ch.Send(bg, "a", nil, "all ok?", SendOpts{Ask: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Reply(bg, "b", ask.Seq, "ok"); err != nil {
			t.Fatal(err)
		}
		ib, err := ch.Inbox(bg, "b")
		if err != nil || len(ib) != 0 {
			t.Fatalf("inbox(b) = %d entries (%v), want 0", len(ib), err)
		}
		ic, err := ch.Inbox(bg, "c")
		if err != nil || len(ic) != 1 {
			t.Fatalf("inbox(c) = %d entries (%v), want 1", len(ic), err)
		}
	})

	t.Run("dependencies: doing is refused until deps finish; finishing releases task.ready to the owner", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "build", Title: "build", Owner: sp("a")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "test", Title: "test", Owner: sp("b"), After: []string{"build"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "b", "test", "doing", ""); errStatus(err) != 409 {
			t.Fatalf("doing before deps status = %d (%v), want 409", errStatus(err), err)
		}
		last, err := ch.LastSeq(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Ack(bg, "b", last); err != nil {
			t.Fatal(err)
		}
		type hit struct {
			h   WaitHit
			err error
		}
		res := make(chan hit, 1)
		go func() { h, err := ch.Wait(bg, "b", []string{"task.ready"}, 5); res <- hit{h, err} }()
		until(t, "b waiting", waiting(t, ch, "b"))
		if _, err := ch.SetStatus(bg, "a", "build", "done", "ok"); err != nil {
			t.Fatal(err)
		}
		o := <-res
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.h.Topic != "task.ready" || o.h.To == nil || *o.h.To != "b" || dataStr(o.h.Data, "task") != "test" {
			t.Errorf("hit = %+v, want task.ready to b for test", o.h)
		}
		r, err := ch.SetStatus(bg, "b", "test", "doing", "")
		if err != nil || !r.OK {
			t.Fatalf("doing after deps: %+v %v", r, err)
		}
	})

	t.Run("a task waiting on two releases only when both finish", func(t *testing.T) {
		ch := joinedChannel(t)
		for _, id := range []string{"x", "y"} {
			if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: id, Title: id}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "z", Title: "z", Owner: sp("b"), After: []string{"x", "y"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "a", "x", "done", ""); err != nil {
			t.Fatal(err)
		}
		countReady := func() int {
			ub, err := ch.Unread(bg, "b")
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, e := range ub {
				if e.Topic == "task.ready" {
					n++
				}
			}
			return n
		}
		if n := countReady(); n != 0 {
			t.Fatalf("task.ready events after x done = %d, want 0", n)
		}
		if _, err := ch.SetStatus(bg, "a", "y", "skipped", ""); err != nil {
			t.Fatal(err)
		}
		if n := countReady(); n != 1 {
			t.Fatalf("task.ready events after y skipped = %d, want 1", n)
		}
	})

	t.Run("cycles and unknown dependencies are refused", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "p", Title: "p"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "q", Title: "q", After: []string{"p"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.UpdateTask(bg, "a", "p", UpdateTaskInput{After: []string{"q"}}); err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Errorf("cycle error = %v, want a cycle", err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "r", Title: "r", After: []string{"nope"}}); err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Errorf("unknown dep error = %v, want unknown", err)
		}
	})

	t.Run("wait_task resolves when another session finishes the task", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "k", Title: "k", Owner: sp("a")}); err != nil {
			t.Fatal(err)
		}
		type hit struct {
			h   WaitHit
			err error
		}
		res := make(chan hit, 1)
		go func() { h, err := ch.WaitTask(bg, "b", "k", 5); res <- hit{h, err} }()
		until(t, "b waiting", waiting(t, ch, "b"))
		if _, err := ch.SetStatus(bg, "a", "k", "done", "built"); err != nil {
			t.Fatal(err)
		}
		o := <-res
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.h.Status != "done" {
			t.Errorf("wait_task hit = %+v, want done", o.h)
		}
	})

	t.Run("pause: any member pauses another; its writes are refused; gate resolves on resume", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Control(bg, "b", "a", "pause", "hold on"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.y", Msg: "m"}); errStatus(err) != 423 {
			t.Fatalf("paused publish status = %d (%v), want 423", errStatus(err), err)
		}
		type gate struct {
			r   GateResult
			err error
		}
		res := make(chan gate, 1)
		go func() { r, err := ch.Gate(bg, "a", 5); res <- gate{r, err} }()
		until(t, "a paused", func() bool {
			p, err := ch.PresenceOf(bg, "a")
			return err == nil && p != nil && p.State == "paused"
		})
		select {
		case <-res:
			t.Fatal("gate resolved while still paused")
		default:
		}
		if _, err := ch.Control(bg, Owner, "a", "resume", ""); err != nil {
			t.Fatal(err)
		}
		g := <-res
		if g.err != nil {
			t.Fatal(g.err)
		}
		if g.r.Control != "run" {
			t.Errorf("gate = %+v, want run", g.r)
		}
		ev, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.y", Msg: "m"})
		if err != nil {
			t.Fatal(err)
		}
		if ev.Topic != "x.y" {
			t.Errorf("publish topic = %s", ev.Topic)
		}
	})

	t.Run("turn: handover topics and explicit pass move it; the materialized turn matches a replay", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Configure(bg, ConfigureArgs{
			Start:    sp("a"),
			Handover: vv(t, `{"a": {"to": "b", "topics": ["finding.ready"]}, "b": {"to": "a", "topics": ["build.ready"]}}`),
		}); err != nil {
			t.Fatal(err)
		}
		turn, err := ch.Turn(bg)
		if err != nil {
			t.Fatal(err)
		}
		if turn.Holder == nil || *turn.Holder != "a" {
			t.Fatalf("turn holder = %v, want a", turn.Holder)
		}
		holder := func() string {
			turn, err := ch.Turn(bg)
			if err != nil {
				t.Fatal(err)
			}
			if turn.Holder == nil {
				return ""
			}
			return *turn.Holder
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "finding.ready", Msg: "f"}); err != nil {
			t.Fatal(err)
		}
		if h := holder(); h != "b" {
			t.Errorf("holder after handover topic = %q, want b", h)
		}
		if _, err := ch.Publish(bg, "b", PublishArgs{Topic: "note.x", Msg: "n"}); err != nil {
			t.Fatal(err)
		}
		if h := holder(); h != "b" {
			t.Errorf("holder after an unrelated topic = %q, want b", h)
		}
		if _, err := ch.PassTurn(bg, "b", "a", "your go"); err != nil {
			t.Fatal(err)
		}
		if h := holder(); h != "a" {
			t.Errorf("holder after pass = %q, want a", h)
		}
		before := holder()
		if err := ch.ReplayTurn(bg); err != nil {
			t.Fatal(err)
		}
		if h := holder(); h != before {
			t.Errorf("holder after replay = %q, want %q", h, before)
		}
	})

	t.Run("idempotency keys make a retried publish return the first event, also when retried concurrently", func(t *testing.T) {
		ch := joinedChannel(t)
		key := sp("k1")
		e1, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.y", Msg: "m", Key: key})
		if err != nil {
			t.Fatal(err)
		}
		e2, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.y", Msg: "m", Key: key})
		if err != nil {
			t.Fatal(err)
		}
		if e2.Seq != e1.Seq || !e2.Duplicate {
			t.Errorf("retry = %+v, want the first event %d marked duplicate", e2, e1.Seq)
		}
		const n = 20
		key2 := sp("k2")
		seqs := make(chan int64, n)
		errs := make(chan error, n)
		for range n {
			go func() {
				e, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.z", Msg: "m", Key: key2})
				if err == nil {
					seqs <- e.Seq
				} else {
					errs <- err
				}
			}()
		}
		seen := map[int64]bool{}
		for range n {
			select {
			case err := <-errs:
				t.Fatal(err)
			case s := <-seqs:
				seen[s] = true
			}
		}
		if len(seen) != 1 {
			t.Errorf("concurrent retries hit %d distinct events, want 1", len(seen))
		}
		evs, err := ch.Events(bg, EventsFilter{Topic: "x.z"})
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 1 {
			t.Errorf("events for x.z = %d, want 1", len(evs))
		}
	})

	t.Run("subagents: join under a joined parent, start at the present, and only the parent may act as them", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "old.news", Msg: "before"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "zz.sub", JoinOpts{}); err == nil || !strings.Contains(err.Error(), "must join") {
			t.Fatalf("orphan subagent error = %v, want must join", err)
		}
		if _, err := ch.Join(bg, "b.explore", JoinOpts{Role: sp("reads the code")}); err != nil {
			t.Fatal(err)
		}
		ub, err := ch.Unread(bg, "b.explore")
		if err != nil || len(ub) != 0 {
			t.Fatalf("unread(b.explore) = %d entries (%v), want 0", len(ub), err)
		}
		if id, err := Identity("b", sp("b.explore")); err != nil || id != "b.explore" {
			t.Errorf("identity(b, b.explore) = %q %v", id, err)
		}
		if _, err := Identity("a", sp("b.explore")); errStatus(err) != 403 {
			t.Errorf("identity(a, b.explore) status = %d (%v), want 403", errStatus(err), err)
		}
		r, err := RunOp(bg, "state", ch, "b", vv(t, `{"state": "working", "task": "reading", "as": "b.explore"}`))
		if err != nil {
			t.Fatal(err)
		}
		p, _ := r.Result.(PresenceView)
		if p.Name != "b.explore" {
			t.Errorf("state as b.explore = %+v", r.Result)
		}
		if _, err := ch.Leave(bg, "b.explore", "found it"); err != nil {
			t.Fatal(err)
		}
		ss, err := ch.Sessions(bg)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range ss {
			if s.Name == "b.explore" && s.State != "left" {
				t.Errorf("b.explore state = %s, want left", s.State)
			}
		}
	})

	t.Run("members: when set, only members (and their subagents) may join", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Configure(bg, ConfigureArgs{Members: vv(t, `["a", "b"]`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "c", JoinOpts{}); errStatus(err) != 403 {
			t.Fatalf("non-member join status = %d (%v), want 403", errStatus(err), err)
		}
		snap, err := ch.Join(bg, "a.helper", JoinOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Me != "a.helper" {
			t.Errorf("join(a.helper).me = %q", snap.Me)
		}
	})

	t.Run("knowledge: remember, recall by words, supersede, read with hits", func(t *testing.T) {
		ch := joinedChannel(t)
		k1, err := ch.Remember(bg, "a", RememberInput{Kind: "lesson", Title: "ugrep alias recurses", Body: "use /usr/bin/grep in pipes", Tags: []string{"shell"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "b", RememberInput{Kind: "fact", Title: "Valkey runs on 6379", Body: "container cache-1"}); err != nil {
			t.Fatal(err)
		}
		recallIDs := func(q string) []int64 {
			items, err := ch.Recall(bg, q, RecallOpts{})
			if err != nil {
				t.Fatal(err)
			}
			out := make([]int64, len(items))
			for i, k := range items {
				out[i] = k.ID
			}
			return out
		}
		if got := recallIDs("grep"); len(got) != 1 || got[0] != k1.ID {
			t.Fatalf("recall(grep) = %v, want [%d]", got, k1.ID)
		}
		facts, err := ch.Recall(bg, "", RecallOpts{Kind: "fact"})
		if err != nil || len(facts) != 1 {
			t.Fatalf("recall(fact) = %d entries (%v), want 1", len(facts), err)
		}
		k3, err := ch.Remember(bg, "a", RememberInput{Kind: "lesson", Title: "grep in pipes", Body: "/usr/bin/grep, always", Supersedes: ip(k1.ID)})
		if err != nil {
			t.Fatal(err)
		}
		if got := recallIDs("grep"); len(got) != 1 || got[0] != k3.ID {
			t.Fatalf("recall(grep) after supersede = %v, want [%d]", got, k3.ID)
		}
		old, err := ch.KB(bg, k1.ID)
		if err != nil {
			t.Fatal(err)
		}
		if old.SupersededBy == nil || *old.SupersededBy != k3.ID {
			t.Errorf("kb(%d).superseded_by = %v, want %d", k1.ID, old.SupersededBy, k3.ID)
		}
		fresh, err := ch.KB(bg, k3.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fresh.Hits != 1 {
			t.Errorf("kb(%d).hits = %d, want 1", k3.ID, fresh.Hits)
		}
		items, err := ch.Recall(bg, "grep", RecallOpts{})
		if err != nil || len(items) != 1 {
			t.Fatalf("recall(grep) = %d entries (%v), want 1", len(items), err)
		}
		if !strings.Contains(items[0].Hit, "«") {
			t.Errorf("recall hit = %q, want a snippet", items[0].Hit)
		}
	})

	t.Run("search finds tasks by prefix with « » snippets, and sees owner edits", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "s1", Title: "Configure the firewall", Body: vv(t, `{"what": "open port 443 for the proxy"}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "s2", Title: "Write docs", Body: vv(t, `{"what": "explain everything"}`)}); err != nil {
			t.Fatal(err)
		}
		hits, err := ch.Search(bg, "firew")
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 1 || hits[0].ID != "s1" {
			t.Fatalf("search(firew) = %+v, want s1", hits)
		}
		hits, err = ch.Search(bg, "proxy")
		if err != nil || len(hits) == 0 {
			t.Fatalf("search(proxy) = %+v (%v)", hits, err)
		}
		if !strings.Contains(hits[0].Hit, "«prox") {
			t.Errorf("search hit = %q, want a «prox» snippet", hits[0].Hit)
		}
		if _, err := ch.UpdateTask(bg, Owner, "s2", UpdateTaskInput{Field: "what", Value: vv(t, `"document the proxy too"`)}); err != nil {
			t.Fatal(err)
		}
		hits, err = ch.Search(bg, "proxy")
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, h := range hits {
			ids = append(ids, h.ID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"s1", "s2"}) {
			t.Errorf("search(proxy) after edit = %v, want s1 and s2", ids)
		}
	})

	t.Run("plan import keeps state by id, reports moved titles, and next respects owners", func(t *testing.T) {
		ch := joinedChannel(t)
		plan := vv(t, `{"phases": [{"n": 0, "title": "base", "steps": [{"id": "0.01", "title": "one"}, {"id": "0.02", "title": "two", "depends": ["0.01"]}]}]}`)
		if _, err := ch.ImportPlan(bg, Owner, planValue(t, plan), ImportOpts{Owner: sp("a")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "a", "0.01", "done", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Note(bg, Owner, "0.02", "change", "do it differently"); err != nil {
			t.Fatal(err)
		}
		renamed := vv(t, `{"phases": [{"n": 0, "title": "base", "steps": [{"id": "0.01", "title": "one"}, {"id": "0.02", "title": "two, renamed", "depends": ["0.01"]}]}]}`)
		r, err := ch.ImportPlan(bg, Owner, planValue(t, renamed), ImportOpts{Owner: sp("a")})
		if err != nil {
			t.Fatal(err)
		}
		tk, err := ch.Task(bg, "0.01")
		if err != nil || tk == nil {
			t.Fatalf("task 0.01: %v %v", tk, err)
		}
		if tk.Status != "done" {
			t.Errorf("0.01 status = %s, want kept done", tk.Status)
		}
		if len(r.Moved) != 1 || r.Moved[0].ID != "0.02" {
			t.Errorf("moved = %+v, want 0.02", r.Moved)
		}
		n, done, err := ch.Next(bg, "a")
		if err != nil || done {
			t.Fatalf("next(a): %v %v", n, err)
		}
		if n.ID != "0.02" || !n.Ready || len(n.OpenNotes) != 1 {
			t.Errorf("next(a) = %+v, want 0.02 ready with one open note", n)
		}
		if _, done, err := ch.Next(bg, "b"); err != nil || !done {
			t.Errorf("next(b) = %v %v, want done", done, err)
		}
	})

	t.Run("a cancelled wait releases its waiter", func(t *testing.T) {
		ch := joinedChannel(t)
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		base := runtimeNumGoroutine()
		type hit struct {
			h   WaitHit
			err error
		}
		res := make(chan hit, 1)
		go func() { h, err := ch.Wait(ctx, "b", []string{"never"}, 0); res <- hit{h, err} }()
		until(t, "b waiting", waiting(t, ch, "b"))
		cancel()
		o := <-res
		if o.err != nil {
			t.Fatal(o.err)
		}
		if o.h.Kind != "cancelled" {
			t.Errorf("hit kind = %q, want cancelled", o.h.Kind)
		}
		// the waiter's goroutine is gone: no leaks
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if runtimeNumGoroutine() <= base {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("goroutines did not settle: %d > %d", runtimeNumGoroutine(), base)
	})

	t.Run("attention gathers what waits on the owner; approve clears a gate and tells the task owner", func(t *testing.T) {
		ch := joinedChannel(t)
		plan := vv(t, `{"phases": [{"n": 0, "title": "p", "steps": [{"id": "g1", "title": "needs a yes", "gate": "ask-first"}, {"id": "g2", "title": "later", "gate": "owner", "depends": ["g1"]}]}]}`)
		if _, err := ch.ImportPlan(bg, Owner, planValue(t, plan), ImportOpts{Owner: sp("a")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "b", sp(Owner), "which option?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Control(bg, Owner, "b", "pause", ""); err != nil {
			t.Fatal(err)
		}
		at, err := ch.Attention(bg)
		if err != nil {
			t.Fatal(err)
		}
		if len(at.Asks) != 1 || at.Asks[0].Msg != "which option?" {
			t.Errorf("asks = %+v, want which option?", at.Asks)
		}
		if len(at.Gates) != 1 || at.Gates[0].ID != "g1" {
			t.Errorf("gates = %+v, want g1 only (g2 still waits on g1)", at.Gates)
		}
		if len(at.Paused) != 1 || at.Paused[0].Name != "b" {
			t.Errorf("paused = %+v, want b", at.Paused)
		}
		if _, err := ch.Approve(bg, "g1", "go", Owner); err != nil {
			t.Fatal(err)
		}
		ua, err := ch.Unread(bg, "a")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range ua {
			if dataBool(e.Data, "approved") && dataStr(e.Data, "task") == "g1" {
				found = true
			}
		}
		if !found {
			t.Errorf("no approval message in a's unread: %+v", ua)
		}
		at, err = ch.Attention(bg)
		if err != nil {
			t.Fatal(err)
		}
		if len(at.Gates) != 0 {
			t.Errorf("gates after approve = %+v, want none", at.Gates)
		}
	})

	t.Run("workflows: start, depend (blocks until the other session finishes), finish (shares the result, releases)", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "mine", Title: "integrate", Owner: sp("a")}); err != nil {
			t.Fatal(err)
		}
		r1, err := RunOp(bg, "start", ch, "a", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !r1.Result.(StartResult).Started {
			t.Fatalf("start = %+v", r1.Result)
		}
		tk, err := ch.Task(bg, "mine")
		if err != nil || tk.Status != "doing" {
			t.Fatalf("mine = %v %v, want doing", tk, err)
		}
		last, err := ch.LastSeq(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Ack(bg, "a", last); err != nil {
			t.Fatal(err)
		}
		type opOut struct {
			r   OpResult
			err error
		}
		depRes := make(chan opOut, 1)
		go func() {
			r, err := RunOp(bg, "depend", ch, "a", vv(t, `{"owner": "b", "title": "ship the API", "timeout": 5}`))
			depRes <- opOut{r, err}
		}()
		// depend blocks the task before it waits: poll for it (a loaded machine can take longer than a fixed sleep)
		until(t, "mine blocked", func() bool {
			tk, err := ch.Task(bg, "mine")
			return err == nil && tk != nil && tk.Status == "blocked"
		})
		list, err := ch.Tasks(bg, TasksFilter{Owner: sp("b")})
		if err != nil || len(list) != 1 {
			t.Fatalf("b's tasks = %+v (%v)", list, err)
		}
		theirs := list[0]
		if _, err := RunOp(bg, "start", ch, "b", vv(t, `{"id": "`+theirs.ID+`"}`)); err != nil {
			t.Fatal(err)
		}
		r2, err := RunOp(bg, "finish", ch, "b", vv(t, `{"note": "curl /api 200", "result": "base url http://api:8080"}`))
		if err != nil {
			t.Fatal(err)
		}
		fin := r2.Result.(FinishResult)
		if len(fin.Released) != 1 || fin.Released[0].Task == nil || *fin.Released[0].Task != "mine" {
			t.Errorf("released = %+v, want mine", fin.Released)
		}
		dep := <-depRes
		if dep.err != nil {
			t.Fatal(dep.err)
		}
		woke := dep.r.Result.(DependResult).Woke
		if woke == nil || woke.Topic != "task.ready" || woke.Ref == nil || *woke.Ref != "mine" {
			t.Errorf("depend woke = %+v, want task.ready for mine", woke)
		}
		items, err := ch.Recall(bg, "base url", RecallOpts{})
		if err != nil || len(items) == 0 || items[0].Kind != "result" {
			t.Fatalf("recall(base url) = %+v (%v), want a result", items, err)
		}
		r3, err := RunOp(bg, "start", ch, "a", vv(t, `{"id": "mine"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !r3.Result.(StartResult).Started {
			t.Errorf("restart = %+v", r3.Result)
		}
	})

	t.Run("workflows: start refuses a task that still waits; ask_wait returns the answer; handoff passes the turn", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "x", Title: "x", Owner: sp("b")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "y", Title: "y", Owner: sp("a"), After: []string{"x"}}); err != nil {
			t.Fatal(err)
		}
		r1, err := RunOp(bg, "start", ch, "a", vv(t, `{"id": "y"}`))
		if err != nil {
			t.Fatal(err)
		}
		s := r1.Result.(StartResult)
		if s.Started || !strings.Contains(s.Hint, "waits on x") {
			t.Fatalf("start = %+v, want a refusal waiting on x", s)
		}
		type opOut struct {
			r   OpResult
			err error
		}
		qRes := make(chan opOut, 1)
		go func() {
			r, err := RunOp(bg, "ask_wait", ch, "a", vv(t, `{"to": "b", "msg": "port?", "timeout": 5}`))
			qRes <- opOut{r, err}
		}()
		until(t, "b asked", func() bool {
			ib, err := ch.Inbox(bg, "b")
			return err == nil && len(ib) > 0
		})
		ib, err := ch.Inbox(bg, "b")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Reply(bg, "b", ib[0].Seq, "8080"); err != nil {
			t.Fatal(err)
		}
		q := <-qRes
		if q.err != nil {
			t.Fatal(q.err)
		}
		if ans := q.r.Result.(AskWaitResult); ans.Answer != "8080" {
			t.Errorf("ask_wait answer = %q, want 8080", ans.Answer)
		}
		r2, err := RunOp(bg, "handoff", ch, "a", vv(t, `{"to": "b", "msg": "your go", "title": "deploy", "wait": false}`))
		if err != nil {
			t.Fatal(err)
		}
		h := r2.Result.(HandoffResult)
		turn, err := ch.Turn(bg)
		if err != nil {
			t.Fatal(err)
		}
		if turn.Holder == nil || *turn.Holder != "b" {
			t.Errorf("turn holder = %v, want b", turn.Holder)
		}
		if h.Task == nil {
			t.Fatal("handoff created no task")
		}
		tk, err := ch.Task(bg, *h.Task)
		if err != nil || tk == nil || tk.Owner == nil || *tk.Owner != "b" {
			t.Fatalf("handoff task = %v %v, want owner b", tk, err)
		}
	})
}

// The exported accessors the server-side extras read and write through: meta under the write
// lock, one event append, and the hub's get hook.
func TestExportedSurface(t *testing.T) {
	t.Run("meta, serial and append from outside the package", func(t *testing.T) {
		ch := openChannel(t, "t")
		if got := ch.Meta("approval_rules"); got != "" {
			t.Errorf("unset meta = %q, want empty", got)
		}
		v := `{"git_push":true}`
		if err := ch.SetMeta(bg, "approval_rules", &v); err != nil {
			t.Fatal(err)
		}
		if got := ch.Meta("approval_rules"); got != v {
			t.Errorf("meta = %q, want %q", got, v)
		}
		err := ch.Serial(bg, func(ctx context.Context) error {
			if ch.Meta("approval_rules") != v {
				t.Error("meta is not readable inside the write")
			}
			deleted := "gone"
			return ch.SetMeta(ctx, "approval_rules", &deleted)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := ch.SetMeta(bg, "approval_rules", nil); err != nil {
			t.Fatal(err)
		}
		if got := ch.Meta("approval_rules"); got != "" {
			t.Errorf("meta after delete = %q, want empty", got)
		}
		msg := "an event"
		ev, err := ch.AppendEvent(bg, "a", EventIn{Topic: "custom.progress", Msg: &msg,
			Data: jsfmt.Object([]string{"step"}, []*jsfmt.Value{jsfmt.String("one")})})
		if err != nil {
			t.Fatal(err)
		}
		if ev.Topic != "custom.progress" || ev.Msg == nil || *ev.Msg != msg {
			t.Errorf("the appended event = %+v", ev)
		}
		if last, err := ch.LastSeq(bg); err != nil || last != ev.Seq {
			t.Errorf("last seq = %d (%v), want the event's %d", last, err, ev.Seq)
		}
	})

	t.Run("the hub's get hook sees every answered channel", func(t *testing.T) {
		hub := NewHub(t.TempDir(), true)
		t.Cleanup(func() { _ = hub.Close(bg) })
		var seen []*Channel
		hub.OnGet(func(c *Channel) { seen = append(seen, c) })
		first, err := hub.Get(bg, "one")
		if err != nil {
			t.Fatal(err)
		}
		if again, err := hub.GetExisting(bg, "one"); err != nil || again != first {
			t.Fatalf("get existing = %v (%v)", again, err)
		}
		if _, err := hub.Get(bg, "two"); err != nil {
			t.Fatal(err)
		}
		if len(seen) != 3 {
			t.Fatalf("the hook saw %d channels, want one per answer", len(seen))
		}
	})
}
