package channel

import (
	"slices"
	"strconv"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// askedByLead is a channel with lead, a, b and c joined and one ask from lead to a, b and c; it
// answers the channel and the ask's group.
func askedByLead(t *testing.T) (*Channel, int64) {
	t.Helper()
	ch := threeMembers(t)
	if _, err := ch.Join(bg, "lead", JoinOpts{Role: sp("plans")}); err != nil {
		t.Fatalf("join lead: %v", err)
	}
	q, err := ch.Send(bg, "lead", []string{"a", "b", "c"}, "which port?", SendOpts{Ask: true})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	return ch, q.Seq
}

// answerAsk makes the session reply to its own copy of the ask.
func answerAsk(t *testing.T, ch *Channel, name, msg string) Ev {
	t.Helper()
	inbox, err := ch.Inbox(bg, name)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox(%s) = %v, %v", name, inbox, err)
	}
	ev, err := ch.Reply(bg, name, inbox[0].Seq, msg)
	if err != nil {
		t.Fatalf("reply %s: %v", name, err)
	}
	return ev
}

func askView(t *testing.T, ch *Channel, group int64) AskView {
	t.Helper()
	v, err := ch.AskStatus(bg, group)
	if err != nil {
		t.Fatalf("ask status: %v", err)
	}
	return v
}

func TestAnAskToThreeSessionsCollectsOneReplyEach(t *testing.T) {
	bdd.Feature(t, "an ask to many sessions", func(f *bdd.F) {
		f.Scenario("An ask to three sessions collects one reply each", func(s *bdd.S) {
			var ch *Channel
			var group int64
			s.Given("lead asked a, b and c", func(t *testing.T) { ch, group = askedByLead(s.T) })
			s.When("a and then b reply", func(t *testing.T) {
				answerAsk(t, ch, "a", "1")
				answerAsk(t, ch, "b", "2")
			})
			s.Then("the ask waits for c alone", func(t *testing.T) {
				v := askView(t, ch, group)
				if !slices.Equal(v.Replied, []string{"a", "b"}) || !slices.Equal(v.Waiting, []string{"c"}) || v.Closed {
					t.Fatalf("status = %+v, want replied [a b], waiting [c], open", v)
				}
			})
			s.When("c replies", func(t *testing.T) { answerAsk(t, ch, "c", "3") })
			s.Then("the ask is closed", func(t *testing.T) {
				v := askView(t, ch, group)
				if len(v.Waiting) != 0 || !v.Closed {
					t.Fatalf("status = %+v, want closed with nobody waiting", v)
				}
			})
			s.And("a second reply from a is refused with 409", func(t *testing.T) {
				events, err := ch.Events(bg, EventsFilter{Topic: "ask"})
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if deref(e.To) != "a" {
						continue
					}
					if _, err := ch.Reply(bg, "a", e.Seq, "again"); errStatus(err) != 409 {
						t.Fatalf("second reply = %v, want 409", err)
					}
				}
			})
		})

		f.Scenario("The owner closes an ask early", func(s *bdd.S) {
			var ch *Channel
			var group int64
			s.Given("lead asked a, b and c and a and b replied", func(t *testing.T) {
				ch, group = askedByLead(s.T)
				answerAsk(t, ch, "a", "1")
				answerAsk(t, ch, "b", "2")
			})
			s.When("the owner closes the ask", func(t *testing.T) {
				if _, err := ch.CloseAsk(bg, Owner, group); err != nil {
					t.Fatalf("close: %v", err)
				}
			})
			s.Then("the ask is closed with c still waiting", func(t *testing.T) {
				v := askView(t, ch, group)
				if !v.Closed || !slices.Equal(v.Waiting, []string{"c"}) {
					t.Fatalf("status = %+v, want closed, waiting [c]", v)
				}
			})
			s.And("c's inbox no longer lists it", func(t *testing.T) {
				inbox, err := ch.Inbox(bg, "c")
				if err != nil || len(inbox) != 0 {
					t.Fatalf("inbox(c) = %v, %v", inbox, err)
				}
			})
			s.And("a reply from c is refused with 409", func(t *testing.T) {
				events, err := ch.Events(bg, EventsFilter{Topic: "ask"})
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if deref(e.To) == "c" {
						if _, err := ch.Reply(bg, "c", e.Seq, "late"); errStatus(err) != 409 {
							t.Fatalf("late reply = %v, want 409", err)
						}
					}
				}
			})
			s.And("closing it again is refused with 409", func(t *testing.T) {
				if _, err := ch.CloseAsk(bg, Owner, group); errStatus(err) != 409 {
					t.Fatalf("second close = %v, want 409", err)
				}
			})
			s.And("a session cannot close an ask", func(t *testing.T) {
				if _, err := ch.CloseAsk(bg, "lead", group); errStatus(err) != 403 {
					t.Fatalf("close by lead = %v, want 403", err)
				}
			})
		})

		f.Scenario("The ask answers who replied", func(s *bdd.S) {
			var ch *Channel
			var group int64
			s.Given("lead asked a, b and c", func(t *testing.T) { ch, group = askedByLead(s.T) })
			s.When("a and c reply", func(t *testing.T) {
				answerAsk(t, ch, "a", "1")
				answerAsk(t, ch, "c", "3")
			})
			s.Then("replied is [a c] and waiting is [b]", func(t *testing.T) {
				v := askView(t, ch, group)
				if !slices.Equal(v.Replied, []string{"a", "c"}) || !slices.Equal(v.Waiting, []string{"b"}) {
					t.Fatalf("status = %+v, want replied [a c], waiting [b]", v)
				}
				if !slices.Equal(v.To, []string{"a", "b", "c"}) || v.From != "lead" {
					t.Fatalf("status = %+v, want from lead to [a b c]", v)
				}
			})
		})

		f.Scenario("ask_wait returns each reply as it arrives", func(s *bdd.S) {
			var ch *Channel
			var first, second AskWaitResult
			s.Given("lead, a, b and c joined", func(t *testing.T) {
				ch = threeMembers(s.T)
				if _, err := ch.Join(bg, "lead", JoinOpts{Role: sp("plans")}); err != nil {
					t.Fatal(err)
				}
			})
			s.When("lead asks all three and a replies", func(t *testing.T) {
				done := make(chan AskWaitResult, 1)
				go func() {
					r, err := RunOp(bg, "ask_wait", ch, "lead", vv(t, `{"to": ["a", "b", "c"], "msg": "which port?", "timeout": 5}`))
					if err != nil {
						t.Errorf("ask_wait: %v", err)
						done <- AskWaitResult{}
						return
					}
					done <- r.Result.(AskWaitResult)
				}()
				until(t, "a asked", func() bool {
					ib, err := ch.Inbox(bg, "a")
					return err == nil && len(ib) == 1
				})
				answerAsk(t, ch, "a", "1")
				first = <-done
			})
			s.Then("ask_wait returned a's reply with b and c pending", func(t *testing.T) {
				if len(first.Replies) != 1 || first.Replies[0].From != "a" || first.Replies[0].Answer != "1" ||
					!slices.Equal(first.Pending, []string{"b", "c"}) || first.Timeout {
					t.Fatalf("first = %+v, want a's reply and pending [b c]", first)
				}
			})
			s.When("lead waits again after that reply and b replies", func(t *testing.T) {
				done := make(chan AskWaitResult, 1)
				go func() {
					args := vv(t, `{"asked": `+strconv.FormatInt(first.Asked, 10)+`, "after": `+strconv.FormatInt(first.Replies[0].Seq, 10)+`, "timeout": 5}`)
					r, err := RunOp(bg, "ask_wait", ch, "lead", args)
					if err != nil {
						t.Errorf("ask_wait again: %v", err)
						done <- AskWaitResult{}
						return
					}
					done <- r.Result.(AskWaitResult)
				}()
				answerAsk(t, ch, "b", "2")
				second = <-done
			})
			s.Then("it returned b's reply alone with c pending", func(t *testing.T) {
				if len(second.Replies) != 1 || second.Replies[0].From != "b" || !slices.Equal(second.Pending, []string{"c"}) {
					t.Fatalf("second = %+v, want b's reply and pending [c]", second)
				}
			})
		})
	})
}
