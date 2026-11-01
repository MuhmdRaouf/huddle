package channel

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheOwnerMessagesTwoSessions(t *testing.T) {
	bdd.Feature(t, "a message carries recipients", func(f *bdd.F) {
		f.Scenario("A message to two sessions reaches only them", func(s *bdd.S) {
			var ch *Channel
			s.Given("a, b and c joined", func(t *testing.T) { ch = threeMembers(s.T) })
			s.When("the owner sends one message to a and b", func(t *testing.T) {
				if _, err := ch.Send(bg, Owner, []string{"a", "b"}, "stand by", SendOpts{}); err != nil {
					t.Fatalf("send: %v", err)
				}
			})
			s.Then("a and b each have it unread", func(t *testing.T) {
				for _, name := range []string{"a", "b"} {
					un, err := ch.Unread(bg, name)
					if err != nil || len(messages(un)) != 1 {
						t.Fatalf("unread(%s) = %v, %v", name, messages(un), err)
					}
				}
			})
			s.And("c has nothing unread", func(t *testing.T) {
				un, err := ch.Unread(bg, "c")
				if err != nil || len(messages(un)) != 0 {
					t.Fatalf("unread(c) = %v, %v", messages(un), err)
				}
			})
			s.And("the two events share one group", func(t *testing.T) {
				evs, err := ch.Events(bg, EventsFilter{Topic: "msg"})
				if err != nil || len(evs) != 2 {
					t.Fatalf("events = %v, %v", evs, err)
				}
				if g0, g1 := dataField(evs[0], "group").EncodeJSON(), dataField(evs[1], "group").EncodeJSON(); g0 != g1 {
					t.Fatalf("groups differ: %s and %s", g0, g1)
				}
			})
		})
	})
}
