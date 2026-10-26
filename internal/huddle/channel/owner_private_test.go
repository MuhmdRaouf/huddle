package channel

import (
	"testing"
)

// hasMsg answers whether one event list carries from's message text.
func hasMsg(evs []Ev, from, msg string) bool {
	for _, ev := range evs {
		if ev.From == from && ev.Msg != nil && *ev.Msg == msg {
			return true
		}
	}
	return false
}

func TestAnOwnerMessageToOneMemberIsItsAlone(t *testing.T) {
	t.Run("unread keeps it from its addressee and leaves it out for the others", func(t *testing.T) {
		ch := openChannel(t, "t")
		for _, member := range []string{"lead", "lead.builder", "dotfiles"} {
			if _, err := ch.Join(bg, member, JoinOpts{Role: sp("builds")}); err != nil {
				t.Fatalf("join %s: %v", member, err)
			}
		}
		if _, err := ch.Send(bg, Owner, sp("lead"), "Pick the second option", SendOpts{}); err != nil {
			t.Fatalf("owner send: %v", err)
		}
		if _, err := ch.Send(bg, "lead", sp("dotfiles"), "done", SendOpts{}); err != nil {
			t.Fatalf("lead send: %v", err)
		}

		mine, err := ch.Unread(bg, "lead")
		if err != nil {
			t.Fatalf("unread lead: %v", err)
		}
		if !hasMsg(mine, Owner, "Pick the second option") {
			t.Fatalf("the addressee's unread lost the owner's message: %v", topics(mine))
		}
		for _, reader := range []string{"lead.builder", "dotfiles"} {
			others, err := ch.Unread(bg, reader)
			if err != nil {
				t.Fatalf("unread %s: %v", reader, err)
			}
			if hasMsg(others, Owner, "Pick the second option") {
				t.Fatalf("%s's unread holds the owner's message to lead: %v", reader, topics(others))
			}
			if !hasMsg(others, "lead", "done") {
				t.Fatalf("%s's unread lost lead's message to dotfiles: %v", reader, topics(others))
			}
		}
	})

	t.Run("events leaves it out for a reader and holds it without one", func(t *testing.T) {
		ch := openChannel(t, "t")
		for _, member := range []string{"lead", "dotfiles"} {
			if _, err := ch.Join(bg, member, JoinOpts{Role: sp("builds")}); err != nil {
				t.Fatalf("join %s: %v", member, err)
			}
		}
		if _, err := ch.Send(bg, Owner, sp("lead"), "Pick the second option", SendOpts{}); err != nil {
			t.Fatalf("owner send: %v", err)
		}

		forReader, err := ch.Events(bg, EventsFilter{Reader: "dotfiles"})
		if err != nil {
			t.Fatalf("events for dotfiles: %v", err)
		}
		if hasMsg(forReader, Owner, "Pick the second option") {
			t.Fatalf("dotfiles's read holds the owner's message to lead: %v", topics(forReader))
		}
		full, err := ch.Events(bg, EventsFilter{})
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if !hasMsg(full, Owner, "Pick the second option") {
			t.Fatalf("the owner's full timeline lost the message: %v", topics(full))
		}
	})

	t.Run("an owner message with no addressee reaches every unread", func(t *testing.T) {
		ch := openChannel(t, "t")
		for _, member := range []string{"lead", "lead.builder", "dotfiles"} {
			if _, err := ch.Join(bg, member, JoinOpts{Role: sp("builds")}); err != nil {
				t.Fatalf("join %s: %v", member, err)
			}
		}
		if _, err := ch.Send(bg, Owner, nil, "All hands: stand by", SendOpts{}); err != nil {
			t.Fatalf("owner send: %v", err)
		}
		for _, reader := range []string{"lead", "lead.builder", "dotfiles"} {
			evs, err := ch.Unread(bg, reader)
			if err != nil {
				t.Fatalf("unread %s: %v", reader, err)
			}
			if !hasMsg(evs, Owner, "All hands: stand by") {
				t.Fatalf("%s's unread lost the owner's message to everyone: %v", reader, topics(evs))
			}
		}
	})
}
