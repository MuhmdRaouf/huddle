package channel

import (
	"slices"
	"strconv"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// threeMembers is a channel with a, b and c joined, all caught up on the join events.
func threeMembers(t *testing.T) *Channel {
	t.Helper()
	ch := joinedChannel(t)
	if _, err := ch.Join(bg, "c", JoinOpts{Role: sp("docs")}); err != nil {
		t.Fatalf("join c: %v", err)
	}
	last, err := ch.LastSeq(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if _, err := ch.Ack(bg, name, last); err != nil {
			t.Fatalf("ack %s: %v", name, err)
		}
	}
	return ch
}

// dataField reads one field of an event's data object; nil when the event has none.
func dataField(e Ev, key string) *jsfmt.Value {
	if e.Data == nil {
		return nil
	}
	for i, k := range e.Data.Keys() {
		if k == key {
			return e.Data.Fields()[i]
		}
	}
	return nil
}

// messages keeps the msg and ask events of a list.
func messages(evs []Ev) []Ev {
	out := []Ev{}
	for _, e := range evs {
		if e.Topic == "msg" || e.Topic == "ask" {
			out = append(out, e)
		}
	}
	return out
}

func toNames(evs []Ev) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = deref(e.To)
	}
	return out
}

func TestAMessageToTwoSessionsReachesOnlyThem(t *testing.T) {
	t.Run("the owner's message to a and b is a's and b's alone, tied by one group", func(t *testing.T) {
		ch := threeMembers(t)
		first, err := ch.Send(bg, Owner, []string{"a", "b"}, "stand by", SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		var groups []string
		for name, want := range map[string]int{"a": 1, "b": 1, "c": 0} {
			un, err := ch.Unread(bg, name)
			if err != nil {
				t.Fatal(err)
			}
			got := messages(un)
			if len(got) != want {
				t.Fatalf("unread(%s) holds %d messages, want %d", name, len(got), want)
			}
			for _, e := range got {
				if deref(e.To) != name {
					t.Errorf("unread(%s) holds a message to %s", name, deref(e.To))
				}
				groups = append(groups, dataField(e, "group").EncodeJSON())
			}
		}
		for _, g := range groups {
			if g != strconv.FormatInt(first.Seq, 10) {
				t.Errorf("data.group = %s, want the first seq %d", g, first.Seq)
			}
		}
		if got := dataField(first, "to"); got == nil || !slices.Equal(strsOf(got), []string{"a", "b"}) {
			t.Errorf("data.to = %v, want [a b]", got)
		}
	})

	t.Run("an ask to a and b waits in a's and b's inbox, not c's", func(t *testing.T) {
		ch := threeMembers(t)
		if _, err := ch.Send(bg, "c", []string{"a", "b"}, "which gcc?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]int{"a": 1, "b": 1, "c": 0} {
			in, err := ch.Inbox(bg, name)
			if err != nil {
				t.Fatal(err)
			}
			if len(in) != want {
				t.Errorf("inbox(%s) holds %d, want %d", name, len(in), want)
			}
		}
	})

	t.Run("each recipient answers its own copy", func(t *testing.T) {
		ch := threeMembers(t)
		if _, err := ch.Send(bg, "c", []string{"a", "b"}, "which gcc?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		in, err := ch.Inbox(bg, "a")
		if err != nil || len(in) != 1 {
			t.Fatalf("inbox(a) = %v, %v", in, err)
		}
		if _, err := ch.Reply(bg, "a", in[0].Seq, "gcc 14"); err != nil {
			t.Fatal(err)
		}
		if in, _ := ch.Inbox(bg, "a"); len(in) != 0 {
			t.Errorf("inbox(a) after its reply = %d, want 0", len(in))
		}
		if in, _ := ch.Inbox(bg, "b"); len(in) != 1 {
			t.Errorf("inbox(b) after a's reply = %d, want 1", len(in))
		}
	})

	t.Run("all reaches every member in one event addressed to no one", func(t *testing.T) {
		for _, to := range [][]string{{"all"}, nil} {
			ch := threeMembers(t)
			ev, err := ch.Send(bg, Owner, to, "all hands", SendOpts{})
			if err != nil {
				t.Fatal(err)
			}
			if ev.To != nil || dataField(ev, "group") != nil {
				t.Errorf("to %v: event to=%v data=%v, want an event to no one and no group", to, ev.To, ev.Data)
			}
			for _, name := range []string{"a", "b", "c"} {
				un, _ := ch.Unread(bg, name)
				if len(messages(un)) != 1 {
					t.Errorf("unread(%s) holds %d messages, want 1", name, len(messages(un)))
				}
			}
		}
	})

	t.Run("one name is the plain message it always was", func(t *testing.T) {
		ch := threeMembers(t)
		ev, err := ch.Send(bg, Owner, []string{"a"}, "just you", SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if deref(ev.To) != "a" || dataField(ev, "group") != nil {
			t.Errorf("event to=%v data=%v, want to a and no group", ev.To, ev.Data)
		}
	})

	t.Run("a repeated name sends once", func(t *testing.T) {
		ch := threeMembers(t)
		if _, err := ch.Send(bg, Owner, []string{"a", "b", "a"}, "m", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		evs, _ := ch.Events(bg, EventsFilter{Topic: "msg"})
		if !slices.Equal(toNames(evs), []string{"a", "b"}) {
			t.Errorf("recipients = %v, want [a b]", toNames(evs))
		}
	})

	t.Run("an unknown name fails and writes no event for anyone", func(t *testing.T) {
		ch := threeMembers(t)
		before, _ := ch.LastSeq(bg)
		_, err := ch.Send(bg, "c", []string{"a", "ghost"}, "m", SendOpts{})
		if errStatus(err) != 404 {
			t.Fatalf("status = %d (%v), want 404", errStatus(err), err)
		}
		if after, _ := ch.LastSeq(bg); after != before {
			t.Errorf("last seq moved %d to %d", before, after)
		}
	})

	t.Run("all with names, or the owner among several, is refused", func(t *testing.T) {
		ch := threeMembers(t)
		for _, to := range [][]string{{"all", "a"}, {"a", Owner}, {"a", ""}} {
			before, _ := ch.LastSeq(bg)
			if _, err := ch.Send(bg, "c", to, "m", SendOpts{}); errStatus(err) != 400 {
				t.Errorf("to %v: status = %d (%v), want 400", to, errStatus(err), err)
			}
			if after, _ := ch.LastSeq(bg); after != before {
				t.Errorf("to %v: last seq moved", to)
			}
		}
	})

	t.Run("the owner is a recipient when named alone", func(t *testing.T) {
		ch := threeMembers(t)
		ev, err := ch.Send(bg, "a", []string{Owner}, "which option?", SendOpts{Ask: true})
		if err != nil || deref(ev.To) != Owner {
			t.Fatalf("send to owner = %+v, %v", ev, err)
		}
	})

	t.Run("a repeated key is a duplicate for each recipient", func(t *testing.T) {
		ch := threeMembers(t)
		e1, err := ch.Send(bg, "c", []string{"a", "b"}, "m", SendOpts{Key: sp("k")})
		if err != nil {
			t.Fatal(err)
		}
		before, _ := ch.LastSeq(bg)
		e2, err := ch.Send(bg, "c", []string{"a", "b"}, "m", SendOpts{Key: sp("k")})
		if err != nil {
			t.Fatal(err)
		}
		if e2.Seq != e1.Seq || !e2.Duplicate {
			t.Errorf("retry = %+v, want %d duplicate", e2, e1.Seq)
		}
		if after, _ := ch.LastSeq(bg); after != before {
			t.Errorf("retry wrote events: last seq %d to %d", before, after)
		}
	})

	t.Run("the send op takes to as a name or a list", func(t *testing.T) {
		for _, body := range []string{`{"to": ["a", "b"], "msg": "m"}`, `{"to": "a", "msg": "m"}`, `{"to": "all", "msg": "m"}`, `{"msg": "m"}`} {
			ch := threeMembers(t)
			if _, err := RunOp(bg, "message", ch, "c", vv(t, body)); err != nil {
				t.Errorf("send %s: %v", body, err)
			}
		}
		ch := threeMembers(t)
		if _, err := RunOp(bg, "message", ch, "c", vv(t, `{"to": ["a", "b"], "msg": "m"}`)); err != nil {
			t.Fatal(err)
		}
		evs, _ := ch.Events(bg, EventsFilter{Topic: "msg"})
		if !slices.Equal(toNames(evs), []string{"a", "b"}) {
			t.Errorf("recipients = %v, want [a b]", toNames(evs))
		}
	})
}
