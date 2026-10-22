package channel

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// removedEvents answers the session.removed events on the timeline, oldest first.
func removedEvents(t *testing.T, ch *Channel) []Ev {
	t.Helper()
	evs, err := ch.Events(bg, EventsFilter{Topic: "session.removed"})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return evs
}

func TestTheOwnerRemovesASession(t *testing.T) {
	bdd.Feature(t, "the owner removes a session", func(f *bdd.F) {
		f.Scenario("the removed session marks left, one event, reservations released", func(s *bdd.S) {
			var ch *Channel
			s.Given("a and b joined and a holds a reservation", func(t *testing.T) {
				ch = joinedChannel(s.T)
				if _, err := Reserve(bg, ch, "a", ReserveArgs{Paths: []string{"/tmp/a-only.txt"}}); err != nil {
					t.Fatalf("reserve: %v", err)
				}
			})
			var out []PresenceView
			s.When("the owner removes a", func(t *testing.T) {
				views, err := ch.Remove(bg, Owner, "a", "")
				if err != nil {
					t.Fatalf("remove: %v", err)
				}
				out = views
			})
			s.Then("a reads left, removed by the owner", func(t *testing.T) {
				p, err := ch.PresenceOf(bg, "a")
				if err != nil {
					t.Fatalf("presence: %v", err)
				}
				if p == nil || p.State != "left" || deref(p.Task) != "removed by the owner" {
					t.Fatalf("a's presence: %+v", p)
				}
			})
			s.And("the timeline carries one session.removed event from the owner", func(t *testing.T) {
				evs := removedEvents(t, ch)
				if len(evs) != 1 {
					t.Fatalf("session.removed events: %d, want 1", len(evs))
				}
				if evs[0].From != Owner || deref(evs[0].Msg) != "a removed by the owner" {
					t.Fatalf("event: from=%s msg=%q", evs[0].From, deref(evs[0].Msg))
				}
			})
			s.And("a's reservations are gone", func(t *testing.T) {
				v, err := Reservations(bg, ch, "a")
				if err != nil {
					t.Fatalf("reservations: %v", err)
				}
				if len(v.Reservations) != 0 {
					t.Fatalf("a still holds %d reservations", len(v.Reservations))
				}
			})
			s.And("the removed presence comes back in the order removed", func(t *testing.T) {
				if len(out) != 1 || out[0].Name != "a" || out[0].State != "left" {
					t.Fatalf("returned views: %+v", out)
				}
			})
		})

		f.Scenario("a why rides the event's message", func(s *bdd.S) {
			var ch *Channel
			s.Given("a joined", func(t *testing.T) {
				ch = openChannel(s.T, "t")
				if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds")}); err != nil {
					t.Fatalf("join a: %v", err)
				}
			})
			s.When("the owner removes a for idling", func(t *testing.T) {
				if _, err := ch.Remove(bg, Owner, "a", "idle for 13 days"); err != nil {
					t.Fatalf("remove: %v", err)
				}
			})
			s.Then("the event says why", func(t *testing.T) {
				evs := removedEvents(t, ch)
				if len(evs) != 1 || deref(evs[0].Msg) != "a removed by the owner: idle for 13 days" {
					t.Fatalf("event: from=%s msg=%q", evs[0].From, deref(evs[0].Msg))
				}
			})
		})

		f.Scenario("the subagents go with their parent, each with its own event", func(s *bdd.S) {
			var ch *Channel
			s.Given("a joined with the subagents a.scan and a.plan", func(t *testing.T) {
				ch = openChannel(s.T, "t")
				if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds")}); err != nil {
					t.Fatalf("join a: %v", err)
				}
				for _, sub := range []string{"a.plan", "a.scan"} {
					if _, err := ch.Join(bg, sub, JoinOpts{Role: sp("helps")}); err != nil {
						t.Fatalf("join %s: %v", sub, err)
					}
				}
			})
			var out []PresenceView
			s.When("the owner removes a", func(t *testing.T) {
				views, err := ch.Remove(bg, Owner, "a", "")
				if err != nil {
					t.Fatalf("remove: %v", err)
				}
				out = views
			})
			s.Then("every subagent reads left", func(t *testing.T) {
				for _, name := range []string{"a.plan", "a.scan"} {
					p, err := ch.PresenceOf(bg, name)
					if err != nil {
						t.Fatalf("presence %s: %v", name, err)
					}
					if p == nil || p.State != "left" {
						t.Fatalf("%s's presence: %+v", name, p)
					}
				}
			})
			s.And("each removal has its own event", func(t *testing.T) {
				evs := removedEvents(t, ch)
				if len(evs) != 3 {
					t.Fatalf("session.removed events: %d, want 3", len(evs))
				}
				want := []string{"a removed by the owner", "a.plan removed by the owner", "a.scan removed by the owner"}
				for i, w := range want {
					if deref(evs[i].Msg) != w {
						t.Fatalf("event %d msg: %q, want %q", i, deref(evs[i].Msg), w)
					}
				}
			})
			s.And("the views answer in the order removed: the parent, then its subagents", func(t *testing.T) {
				names := []string{}
				for _, v := range out {
					names = append(names, v.Name)
				}
				if strings.Join(names, ",") != "a,a.plan,a.scan" {
					t.Fatalf("order removed: %v", names)
				}
			})
		})

		f.Scenario("a turn the removed held lands with the owner", func(s *bdd.S) {
			var ch *Channel
			s.Given("a joined and holds the turn", func(t *testing.T) {
				ch = openChannel(s.T, "t")
				if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds")}); err != nil {
					t.Fatalf("join a: %v", err)
				}
				if _, err := ch.TakeTurn(bg, "a", ""); err != nil {
					t.Fatalf("take: %v", err)
				}
			})
			s.When("the owner removes a", func(t *testing.T) {
				if _, err := ch.Remove(bg, Owner, "a", ""); err != nil {
					t.Fatalf("remove: %v", err)
				}
			})
			s.Then("the owner holds the turn", func(t *testing.T) {
				turn, err := ch.Turn(bg)
				if err != nil {
					t.Fatalf("turn: %v", err)
				}
				if turn.Holder == nil || *turn.Holder != Owner {
					t.Fatalf("turn holder: %v", turn.Holder)
				}
			})
		})

		f.Scenario("a non-owner is refused and nothing changes", func(s *bdd.S) {
			var ch *Channel
			s.Given("a and b joined", func(t *testing.T) {
				ch = joinedChannel(s.T)
			})
			s.When("a tries to remove b", func(t *testing.T) {
				if _, err := ch.Remove(bg, "a", "b", ""); err == nil {
					t.Fatal("a's remove ran")
				} else if got := errStatus(err); got != 403 {
					t.Fatalf("status: %d, want 403", got)
				}
			})
			s.Then("b is still working and no event landed", func(t *testing.T) {
				p, err := ch.PresenceOf(bg, "b")
				if err != nil || p == nil || p.State != "working" {
					t.Fatalf("b's presence: %+v %v", p, err)
				}
				if evs := removedEvents(t, ch); len(evs) != 0 {
					t.Fatalf("session.removed events: %d, want 0", len(evs))
				}
			})
		})

		f.Scenario("the owner itself and unknown names are refused", func(s *bdd.S) {
			var ch *Channel
			s.Given("a joined", func(t *testing.T) {
				ch = openChannel(s.T, "t")
				if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds")}); err != nil {
					t.Fatalf("join a: %v", err)
				}
			})
			s.When("the owner tries to remove the owner", func(t *testing.T) {
				_, err := ch.Remove(bg, Owner, Owner, "")
				if got := errStatus(err); got != 400 {
					t.Fatalf("status: %d, want 400", got)
				}
			})
			s.Then("an unknown name gets the not-found error a need gives", func(t *testing.T) {
				_, wantErr := ch.need(bg, "ghost")
				_, err := ch.Remove(bg, Owner, "ghost", "")
				if err == nil || err.Error() != wantErr.Error() {
					t.Fatalf("error: %v, want %v", err, wantErr)
				}
			})
		})

		f.Scenario("a session that already left is refused", func(s *bdd.S) {
			var ch *Channel
			s.Given("a joined and left", func(t *testing.T) {
				ch = openChannel(s.T, "t")
				if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds")}); err != nil {
					t.Fatalf("join a: %v", err)
				}
				if _, err := ch.Leave(bg, "a", "done"); err != nil {
					t.Fatalf("leave: %v", err)
				}
			})
			s.When("the owner removes a", func(t *testing.T) {
				_, err := ch.Remove(bg, Owner, "a", "")
				if got := errStatus(err); got != 400 {
					t.Fatalf("status: %d, want 400", got)
				} else if !strings.Contains(err.Error(), "a already left") {
					t.Fatalf("message: %q", err.Error())
				}
			})
		})
	})
}
