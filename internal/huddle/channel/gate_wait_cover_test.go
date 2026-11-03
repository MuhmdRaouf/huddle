package channel

import "testing"

// TestAWaitIsMovedOnlyByAnEventItCouldRead: a topic wait wakes for an ask it may have to answer
// and for an event on its topics addressed to it or to everyone, never for its own events or for
// one addressed to someone else.
func TestAWaitIsMovedOnlyByAnEventItCouldRead(t *testing.T) {
	topics := []string{"task.*"}
	for _, c := range []struct {
		name string
		ev   Ev
		want bool
	}{
		{"its own event", Ev{From: "a", Topic: "task.done"}, false},
		{"an ask to everyone, on any topic", Ev{From: "b", Topic: "ask", NeedsReply: true}, true},
		{"an ask to it", Ev{From: "b", To: sp("a"), Topic: "ask", NeedsReply: true}, true},
		{"an ask to someone else", Ev{From: "b", To: sp("c"), Topic: "ask", NeedsReply: true}, false},
		{"a broadcast on its topic", Ev{From: "b", Topic: "task.done"}, true},
		{"a broadcast on another topic", Ev{From: "b", Topic: "note"}, false},
		{"a message to it on its topic", Ev{From: "b", To: sp("a"), Topic: "task.done"}, true},
		{"a message to someone else on its topic", Ev{From: "b", To: sp("c"), Topic: "task.done"}, false},
	} {
		if got := waitMatch("a", topics, c.ev); got != c.want {
			t.Errorf("%s: waitMatch = %v, want %v", c.name, got, c.want)
		}
	}
}
