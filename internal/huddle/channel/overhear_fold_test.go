package channel

import (
	"slices"
	"testing"
)

// foursome is a channel with a, b, c and d joined and caught up: c speaks to a and b, d overhears.
func foursome(t *testing.T) *Channel {
	t.Helper()
	ch := threeMembers(t)
	if _, err := ch.Join(bg, "d", JoinOpts{Role: sp("review")}); err != nil {
		t.Fatalf("join d: %v", err)
	}
	last, err := ch.LastSeq(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Ack(bg, "d", last); err != nil {
		t.Fatalf("ack d: %v", err)
	}
	return ch
}

func seqs(evs []Ev) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

func TestAThirdSessionSeesAGroupMessageOnce(t *testing.T) {
	t.Run("unread, events and the cast's unread count name the group once", func(t *testing.T) {
		ch := foursome(t)
		first, err := ch.Send(bg, "c", []string{"a", "b"}, "stand by", SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		un, err := ch.Unread(bg, "d")
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(messages(un)); !slices.Equal(got, []int64{first.Seq}) {
			t.Errorf("unread(d) holds messages %v, want only the first copy %d", got, first.Seq)
		}
		evs, err := ch.Events(bg, EventsFilter{Topic: "msg", Reader: "d"})
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(evs); !slices.Equal(got, []int64{first.Seq}) {
			t.Errorf("events read by d = %v, want only the first copy %d", got, first.Seq)
		}
		cast, err := ch.Sessions(bg)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range cast {
			if s.Name == "d" && s.Unread != 1 {
				t.Errorf("unread count of d = %d, want 1", s.Unread)
			}
		}
	})

	t.Run("wait skips the group once", func(t *testing.T) {
		ch := foursome(t)
		if _, err := ch.Send(bg, "c", []string{"a", "b"}, "stand by", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "c", nil, "all hands", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		hit, err := ch.Wait(bg, "d", nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		if hit.Kind != "event" || deref(hit.Msg) != "all hands" {
			t.Fatalf("wait = %+v, want the message to all", hit)
		}
		if hit.SkippedTotal != 1 || len(hit.Skipped) != 1 {
			t.Errorf("wait skipped %d (%d listed), want the group once", hit.SkippedTotal, len(hit.Skipped))
		}
	})

	t.Run("the recipients keep their own copies", func(t *testing.T) {
		ch := foursome(t)
		if _, err := ch.Send(bg, "c", []string{"a", "b"}, "stand by", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b"} {
			un, _ := ch.Unread(bg, name)
			own := 0
			for _, e := range messages(un) {
				if deref(e.To) == name {
					own++
				}
			}
			if own != 1 {
				t.Errorf("unread(%s) holds %d copies addressed to it, want 1", name, own)
			}
		}
	})

	t.Run("the digest names an ask to several sessions once", func(t *testing.T) {
		ch := foursome(t)
		if _, err := ch.Send(bg, "c", []string{"a", "b"}, "which gcc?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		dg, err := Digest(bg, ch, "24h", DigestOpts{SkipCost: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(dg.Questions) != 1 || dg.Totals.Questions != 1 {
			t.Errorf("digest holds %d questions (total %d), want 1", len(dg.Questions), dg.Totals.Questions)
		}
	})

	t.Run("the digest keeps the copy still open once the first is answered", func(t *testing.T) {
		ch := foursome(t)
		if _, err := ch.Send(bg, "c", []string{"a", "b"}, "which gcc?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		in, _ := ch.Inbox(bg, "a")
		if _, err := ch.Reply(bg, "a", in[0].Seq, "gcc 14"); err != nil {
			t.Fatal(err)
		}
		dg, err := Digest(bg, ch, "24h", DigestOpts{SkipCost: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(dg.Questions) != 1 {
			t.Fatalf("digest holds %d questions, want 1", len(dg.Questions))
		}
	})
}

func TestFoldedCopy(t *testing.T) {
	for _, c := range []struct {
		name   string
		reader string
		seq    int64
		data   string
		want   bool
	}{
		{"no data", "d", 6, `null`, false},
		{"a message to one session", "d", 6, `{"task":"x"}`, false},
		{"the first copy stands for the group", "d", 5, `{"to":["a","b"],"group":5}`, false},
		{"a later copy folds for a third session", "d", 6, `{"to":["a","b"],"group":5}`, true},
		{"a recipient keeps every copy", "b", 6, `{"to":["a","b"],"group":5}`, false},
		{"the first recipient keeps a later copy too", "a", 6, `{"to":["a","b"],"group":5}`, false},
	} {
		if got := FoldedCopy(c.reader, c.seq, vv(t, c.data)); got != c.want {
			t.Errorf("%s: FoldedCopy(%s, %d, %s) = %v, want %v", c.name, c.reader, c.seq, c.data, got, c.want)
		}
	}
	if FoldedCopy("d", 6, nil) {
		t.Error("a nil data folded")
	}
}
