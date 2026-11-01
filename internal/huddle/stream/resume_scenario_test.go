package stream

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// A client whose stream dropped reconnects with the last seq it saw. The broker replays what it missed
// when the ring still holds it, and answers a snapshot of each topic when the ring cannot.

// seqsOf lists the seqs of frames in order.
func seqsOf(frames []domain.StreamFrame) []uint64 {
	var seqs []uint64
	for _, frame := range frames {
		seqs = append(seqs, frame.Message.Seq)
	}
	return seqs
}

// opsOf lists "<topic>:<op>" for each frame in order.
func opsOf(frames []domain.StreamFrame) []string {
	var ops []string
	for _, frame := range frames {
		ops = append(ops, frame.Message.Topic+":"+string(frame.Message.Op))
	}
	return ops
}

// resumeBroker is a broker booted at microsecond 1, so its first seq is 1, with one snapshotter per topic.
func resumeBroker(t *testing.T) *Broker {
	b, _ := newTestBroker(1)
	rows := []Row{{ID: "s1", Data: json.RawMessage(`{"state":"busy"}`)}}
	b.Register("sessions", staticSnapshotter{rows: rows})
	b.Register("needs", staticSnapshotter{rows: nil})
	return b
}

func TestADroppedStreamResumes(t *testing.T) {
	bdd.Feature(t, "a dropped stream resumes", func(f *bdd.F) {
		f.Scenario("a client at seq 10 reconnects and gets only 11 onward", func(s *bdd.S) {
			var b *Broker
			var sub *Subscription
			s.Given("a hub that published 20 frames, half of them for a topic the client did not ask for", func(t *testing.T) {
				b = resumeBroker(t)
				for i := 1; i <= 20; i++ {
					topic := "sessions"
					if i%2 == 0 {
						topic = "needs"
					}
					mustPublish(t, b, topic, domain.OpUpsert, "k", `{}`)
				}
			})
			s.When("the client reconnects for sessions after seq 10", func(t *testing.T) {
				sub = mustSubscribe(t, b, Request{Topics: []string{"sessions"}, After: 10})
			})
			s.Then("it gets the sessions frames from 11 onward and nothing else", func(t *testing.T) {
				if got, want := seqsOf(sub.Initial()), []uint64{11, 13, 15, 17, 19}; !slices.Equal(got, want) {
					t.Fatalf("replayed seqs = %v, want %v", got, want)
				}
			})
			s.And("no topic is gone", func(t *testing.T) {
				if gone := sub.Gone(); len(gone) != 0 {
					t.Fatalf("gone = %v, want none", gone)
				}
			})
			s.And("the next publish reaches it live with no gap", func(t *testing.T) {
				mustPublish(t, b, "sessions", domain.OpUpsert, "k", `{}`)
				if frame := receive(t, sub); frame.Message.Seq != 21 {
					t.Fatalf("live seq = %d, want 21", frame.Message.Seq)
				}
			})
		})

		f.Scenario("a client whose seq the ring passed gets a snapshot of each topic", func(s *bdd.S) {
			var b *Broker
			var sub *Subscription
			s.Given("a hub that published 3,000 frames, so seq 10 left the ring", func(t *testing.T) {
				b = resumeBroker(t)
				for i := 0; i < 3000; i++ {
					mustPublish(t, b, "sessions", domain.OpUpsert, "k", `{}`)
				}
			})
			s.When("the client reconnects for sessions and needs after seq 10", func(t *testing.T) {
				sub = mustSubscribe(t, b, Request{Topics: []string{"sessions", "needs"}, After: 10})
			})
			s.Then("it gets one snapshot frame per topic, in the order it asked", func(t *testing.T) {
				if got, want := opsOf(sub.Initial()), []string{"sessions:snapshot", "needs:snapshot"}; !slices.Equal(got, want) {
					t.Fatalf("frames = %v, want %v", got, want)
				}
			})
			s.And("each snapshot carries the head seq and the topic's rows", func(t *testing.T) {
				first := sub.Initial()[0]
				if first.Message.Seq != b.Head() {
					t.Fatalf("snapshot seq = %d, want the head %d", first.Message.Seq, b.Head())
				}
				if want := `{"rows":[{"id":"s1","data":{"state":"busy"}}]}`; string(first.Message.Data) != want {
					t.Fatalf("snapshot data = %s, want %s", first.Message.Data, want)
				}
				if want := `{"rows":[]}`; string(sub.Initial()[1].Message.Data) != want {
					t.Fatalf("empty snapshot data = %s, want %s", sub.Initial()[1].Message.Data, want)
				}
			})
			s.And("both topics are listed as gone for the hello frame", func(t *testing.T) {
				if got, want := sub.Gone(), []string{"sessions", "needs"}; !slices.Equal(got, want) {
					t.Fatalf("gone = %v, want %v", got, want)
				}
				if sub.Head() != b.Head() {
					t.Fatalf("subscription head = %d, want %d", sub.Head(), b.Head())
				}
			})
		})

		f.Scenario("a client after a hub restart (seq above head) gets a snapshot", func(s *bdd.S) {
			var b *Broker
			var sub *Subscription
			s.Given("a restarted hub that published 5 frames", func(t *testing.T) {
				b = resumeBroker(t)
				for i := 0; i < 5; i++ {
					mustPublish(t, b, "sessions", domain.OpUpsert, "k", `{}`)
				}
			})
			s.When("the client reconnects with the old process's seq", func(t *testing.T) {
				sub = mustSubscribe(t, b, Request{Topics: []string{"sessions"}, After: 1_770_000_000_000_000})
			})
			s.Then("it gets a snapshot of the topic", func(t *testing.T) {
				if got, want := opsOf(sub.Initial()), []string{"sessions:snapshot"}; !slices.Equal(got, want) {
					t.Fatalf("frames = %v, want %v", got, want)
				}
			})
			s.And("the topic is gone", func(t *testing.T) {
				if got := sub.Gone(); !slices.Equal(got, []string{"sessions"}) {
					t.Fatalf("gone = %v, want [sessions]", got)
				}
			})
		})

		f.Scenario("a topic added to a live resume gets a snapshot while the others replay", func(s *bdd.S) {
			var b *Broker
			var sub *Subscription
			s.Given("a hub that published 4 frames", func(t *testing.T) {
				b = resumeBroker(t)
				for i := 0; i < 4; i++ {
					mustPublish(t, b, "sessions", domain.OpUpsert, "k", `{}`)
				}
			})
			s.When("the client resumes after seq 2 and asks for a needs snapshot", func(t *testing.T) {
				sub = mustSubscribe(t, b, Request{Topics: []string{"sessions", "needs"}, After: 2, Snapshot: []string{"needs"}})
			})
			s.Then("it gets the needs snapshot and the missed sessions frames", func(t *testing.T) {
				if got, want := opsOf(sub.Initial()), []string{"needs:snapshot", "sessions:upsert", "sessions:upsert"}; !slices.Equal(got, want) {
					t.Fatalf("frames = %v, want %v", got, want)
				}
			})
			s.And("only needs is gone", func(t *testing.T) {
				if got := sub.Gone(); !slices.Equal(got, []string{"needs"}) {
					t.Fatalf("gone = %v, want [needs]", got)
				}
			})
		})
	})
}
