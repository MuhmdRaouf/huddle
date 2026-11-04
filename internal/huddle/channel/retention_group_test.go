package channel

import (
	"testing"
	"time"
)

// groupCopiesLeft counts the stored copies of the message to several sessions that starts at seq.
func groupCopiesLeft(t *testing.T, ch *Channel, first int64) int {
	t.Helper()
	evs, err := ch.Events(bg, EventsFilter{After: ip(first - 1)})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if g := argIntPtr(e.Data, "group"); g != nil && *g == first {
			n++
		}
	}
	return n
}

func TestRetentionNeverSplitsAGroupMessage(t *testing.T) {
	// a prune 100 days on: every event is past the 90-day TTL unless retention keeps it
	later := time.Now().Add(100 * 24 * time.Hour)

	t.Run("an ask one recipient answered stays while the other copy waits for its reply", func(t *testing.T) {
		ch := foursome(t)
		first, err := ch.Send(bg, "c", []string{"a", "b"}, "which gcc?", SendOpts{Ask: true})
		if err != nil {
			t.Fatal(err)
		}
		in, _ := ch.Inbox(bg, "a")
		if _, err := ch.Reply(bg, "a", in[0].Seq, "gcc 14"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Store.PruneEvents(bg, later); err != nil {
			t.Fatal(err)
		}
		if n := groupCopiesLeft(t, ch, first.Seq); n != 2 {
			t.Errorf("%d copies left, want both", n)
		}
		un, err := ch.Unread(bg, "d")
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(messages(un)); len(got) != 1 || got[0] != first.Seq {
			t.Errorf("unread(d) holds messages %v after the prune, want the group once as %d", got, first.Seq)
		}
	})

	t.Run("copies whose timestamps straddle the cutoff stay together", func(t *testing.T) {
		ch := foursome(t)
		first, err := ch.Send(bg, "c", []string{"a", "b"}, "stand by", SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		young := later.Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
		if err := ch.dbRun(bg, "UPDATE events SET ts = ? WHERE seq = ?", young, first.Seq+1); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Store.PruneEvents(bg, later); err != nil {
			t.Fatal(err)
		}
		if n := groupCopiesLeft(t, ch, first.Seq); n != 2 {
			t.Errorf("%d copies left, want both", n)
		}
		un, _ := ch.Unread(bg, "d")
		if got := seqs(messages(un)); len(got) != 1 {
			t.Errorf("unread(d) holds messages %v after the prune, want the group once", got)
		}
	})

	t.Run("a group with no copy to keep goes whole", func(t *testing.T) {
		ch := foursome(t)
		first, err := ch.Send(bg, "c", []string{"a", "b"}, "stand by", SendOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Store.PruneEvents(bg, later); err != nil {
			t.Fatal(err)
		}
		if n := groupCopiesLeft(t, ch, first.Seq); n != 0 {
			t.Errorf("%d copies left, want none", n)
		}
	})
}
