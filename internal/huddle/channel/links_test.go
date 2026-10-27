package channel

import (
	"context"
	"maps"
	"testing"
)

// fakeSessions answers MemberSessions from a copy of its map: the registry wired and able to
// answer.
type fakeSessions map[string]string

func (f fakeSessions) MemberSessions(ctx context.Context, channel string) (map[string]string, bool) {
	out := make(map[string]string, len(f))
	maps.Copy(out, f)
	return out, true
}

// muteSessions is the registry wired but unable to answer.
type muteSessions struct{}

func (muteSessions) MemberSessions(ctx context.Context, channel string) (map[string]string, bool) {
	return nil, false
}

// carriedID reads the Claude session id a member's row carries, "" when none is.
func carriedID(t *testing.T, ch *Channel, name string) string {
	t.Helper()
	s, err := ch.Session(bg, name)
	if err != nil || s == nil {
		t.Fatalf("session %s: %v", name, err)
	}
	if s.SessionID == nil {
		return ""
	}
	return *s.SessionID
}

// TestLinkPutsTheClaudeSessionOnTheMemberRow pins Link's rule: the id lands on the member's own
// row, a subagent's link moves its parent's row to the newest id, and an id or name that is not
// fit to store, the owner and a member without a row change nothing.
func TestLinkPutsTheClaudeSessionOnTheMemberRow(t *testing.T) {
	ctx := bg
	ch := joinedChannel(t)

	Link(ctx, ch, "a", "c-1")
	if got := carriedID(t, ch, "a"); got != "c-1" {
		t.Fatalf("a carries %q, want c-1", got)
	}
	links, err := Links(ctx, ch)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if !maps.Equal(links, map[string]string{"c-1": "a"}) {
		t.Fatalf("links = %v, want c-1 → a", links)
	}

	Link(ctx, ch, "a.sub", "c-2")
	if got := carriedID(t, ch, "a"); got != "c-2" {
		t.Fatalf("a carries %q after its subagent linked, want c-2", got)
	}
	links, err = Links(ctx, ch)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if !maps.Equal(links, map[string]string{"c-2": "a"}) {
		t.Fatalf("links = %v, want the latest id c-2 → a", links)
	}

	Link(ctx, ch, "a", "not a fit!")
	Link(ctx, ch, Owner, "c-9")
	Link(ctx, ch, "nobody", "c-9")
	if got := carriedID(t, ch, "a"); got != "c-2" {
		t.Fatalf("a carries %q after the refused links, want c-2", got)
	}
	links, err = Links(ctx, ch)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if !maps.Equal(links, map[string]string{"c-2": "a"}) {
		t.Fatalf("links = %v, want c-2 → a unchanged", links)
	}
}

// TestLinksUnionsTheRowsAndTheRegistry pins Links' rule: the member rows' own ids sit over the
// registry's answer, so a row's own id wins on a clash, and a registry that cannot answer
// leaves the rows alone.
func TestLinksUnionsTheRowsAndTheRegistry(t *testing.T) {
	ctx := bg
	ch := joinedChannel(t)
	Link(ctx, ch, "a", "c-1")

	ch.MemberSessions = fakeSessions{"old-1": "a", "c-1": "b"}
	links, err := Links(ctx, ch)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	want := map[string]string{"old-1": "a", "c-1": "a"}
	if !maps.Equal(links, want) {
		t.Fatalf("links = %v, want %v: the row wins the c-1 clash", links, want)
	}

	ch.MemberSessions = muteSessions{}
	links, err = Links(ctx, ch)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if !maps.Equal(links, map[string]string{"c-1": "a"}) {
		t.Fatalf("links = %v, want the rows only when the registry cannot answer", links)
	}
}

// TestChannelFileKeepsNoLinkTable pins where the map lives: the member row, never a side table
// of its own.
func TestChannelFileKeepsNoLinkTable(t *testing.T) {
	ctx := bg
	ch := joinedChannel(t)
	Link(ctx, ch, "a", "c-1")
	r, err := ch.Store.Get(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name='x_session_links'")
	if err != nil {
		t.Fatalf("read the schema: %v", err)
	}
	if r != nil {
		t.Fatalf("x_session_links exists in a fresh channel file")
	}
}
