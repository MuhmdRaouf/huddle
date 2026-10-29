package ledger

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The node a request lands on and the way it left when only a transcript saw it: the
// transcript's naming wins, the route's backs it up, and facts that name neither take the
// rule over the route event's own session and agent.

func TestReconcileNamesTheNode(t *testing.T) {
	cases := []struct {
		name string
		in   Sources
		want string
	}{
		{
			name: "the transcript's node wins over the route's",
			in: Sources{
				Route:      &RouteFacts{NodeID: "s1/main"},
				Transcript: &TranscriptFacts{NodeID: "s1/a7"},
			},
			want: "s1/a7",
		},
		{
			name: "the route's node stands when no transcript saw the request",
			in: Sources{
				Route: &RouteFacts{NodeID: "job:zai:1"},
			},
			want: "job:zai:1",
		},
		{
			name: "facts from before the nodes fell back to the rule over the route event",
			in: Sources{
				Route: &RouteFacts{Event: domain.RouteEvent{SessionID: strp("s1")}},
			},
			want: "s1/main",
		},
		{
			name: "facts from before the nodes, a transcript beside the route",
			in: Sources{
				Route:      &RouteFacts{Event: domain.RouteEvent{SessionID: strp("s1")}},
				Transcript: &TranscriptFacts{RequestID: "req_9"},
			},
			want: "s1/main",
		},
		{
			name: "a route event with no session counts outside any session",
			in: Sources{
				Route: &RouteFacts{},
			},
			want: "outside-a-session/main",
		},
		{
			name: "a transcript that names no node names none",
			in: Sources{
				Transcript: &TranscriptFacts{RequestID: "req_1"},
			},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row, ok := Reconcile(c.in)
			if !ok {
				t.Fatal("reconcile refused the sources")
			}
			if row.NodeID != c.want {
				t.Errorf("NodeID = %q, want %q", row.NodeID, c.want)
			}
		})
	}
}

func TestReconcileCarriesTheTranscriptVia(t *testing.T) {
	t.Run("a transcript-only request names the way its call left", func(t *testing.T) {
		row, ok := Reconcile(Sources{
			Transcript: &TranscriptFacts{RequestID: "req_2", Via: strp("127.0.0.1:18999")},
		})
		if !ok {
			t.Fatal("reconcile refused the transcript")
		}
		if row.Via == nil || *row.Via != "127.0.0.1:18999" {
			t.Errorf("Via = %v, want 127.0.0.1:18999", row.Via)
		}
	})

	t.Run("a route line's via wins over the transcript's", func(t *testing.T) {
		row, ok := Reconcile(Sources{
			Route: &RouteFacts{Event: domain.RouteEvent{Plugin: "zai"}},
			Transcript: &TranscriptFacts{
				RequestID: "req_3",
				Via:       strp("127.0.0.1:18999"),
			},
		})
		if !ok {
			t.Fatal("reconcile refused the sources")
		}
		if row.Via == nil || *row.Via != "zai" {
			t.Errorf("Via = %v, want zai (the route wins)", row.Via)
		}
	})

	t.Run("a transcript that names no way names none", func(t *testing.T) {
		row, ok := Reconcile(Sources{
			Transcript: &TranscriptFacts{RequestID: "req_4"},
		})
		if !ok {
			t.Fatal("reconcile refused the transcript")
		}
		if row.Via != nil {
			t.Errorf("Via = %v, want nil", row.Via)
		}
	})
}
