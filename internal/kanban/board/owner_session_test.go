package board

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readyFrom writes a card body that clears the ticket bar and creates a ready card from the
// session, then answers its id.
func (f *fixture) readyFrom(session, id string) {
	f.t.Helper()
	body := filepath.Join(f.t.TempDir(), id+".md")
	if err := os.WriteFile(body, []byte(fullBody(id)), 0o644); err != nil {
		f.t.Fatal(err)
	}
	mustLines(f.t, f.new(NewTicket{
		Actor: "lead", ID: id, Title: "t", Cluster: "login", Summary: longSummary,
		Type: "docs", Ready: true, BodyFile: body, Session: session,
	}))
}

// ownerOf answers the card's owner session as the card JSON carries it.
func (f *fixture) ownerOf(id string) string {
	f.t.Helper()
	for _, card := range mustCards(f.t, f.list("")) {
		if card.ID != id {
			continue
		}
		raw, err := json.Marshal(card)
		if err != nil {
			f.t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			f.t.Fatal(err)
		}
		owner, ok := doc["ownerSession"].(string)
		if !ok {
			f.t.Fatalf("card JSON %s has no ownerSession string", raw)
		}
		return owner
	}
	f.t.Fatalf("no card %s", id)
	return ""
}

// The session that created a card owns it, and no claim change moves that: a pull by another
// actor and session, a reject, an unblock and a re-pull by a third keep the creator's session.
func TestTheOwnerSessionSurvivesClaimChanges(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.readyFrom("s1", "LOGIN-1")
	if got := f.ownerOf("LOGIN-1"); got != "s1" {
		t.Fatalf("owner after new = %q, want s1", got)
	}
	if !strings.Contains(f.fm("ready", "LOGIN-1"), "owner_session: s1\n") {
		t.Fatalf("the card file lacks a bare owner_session key:\n%s", f.fm("ready", "LOGIN-1"))
	}

	mustLines(t, f.pull(Args{Actor: "w2", ID: "LOGIN-1", Session: "s2"}))
	steps := []struct {
		name string
		run  func() ([]string, error)
	}{
		{"review", f.review(Args{Actor: "w2", ID: "LOGIN-1", Note: "ready for a look"})},
		{"reject", f.reject(Args{Actor: "rev", ID: "LOGIN-1", Note: "fix the edge"})},
		{"block", f.block(Args{Actor: "w2", ID: "LOGIN-1", Note: "waits on a key"})},
		{"unblock", f.unblock(Args{Actor: "lead", ID: "LOGIN-1", Note: "key arrived"})},
		{"re-pull", f.pull(Args{Actor: "w3", ID: "LOGIN-1", Session: "s3"})},
		{"set", f.set("lead", "LOGIN-1", []string{"est=L"})},
	}
	for _, step := range steps {
		mustLines(t, step.run)
		if got := f.ownerOf("LOGIN-1"); got != "s1" {
			t.Fatalf("owner after %s = %q, want s1", step.name, got)
		}
	}
	_, _ = f.b.Move(ctx, Args{Actor: "lead", ID: "LOGIN-1", Column: "backlog", Note: "repair"})
	if got := f.ownerOf("LOGIN-1"); got != "s1" {
		t.Fatalf("owner after the lead's move = %q, want s1", got)
	}
}

// A card made with no session id has no owner: the JSON carries an empty string and the file
// carries no key.
func TestACardMadeWithoutASessionHasNoOwner(t *testing.T) {
	f := newFixture(t)
	f.readyFrom("", "LOGIN-2")
	if got := f.ownerOf("LOGIN-2"); got != "" {
		t.Errorf("owner of a card made with no session = %q, want none", got)
	}
	if strings.Contains(f.fm("ready", "LOGIN-2"), "owner_session") {
		t.Errorf("the card file carries an owner_session key:\n%s", f.fm("ready", "LOGIN-2"))
	}
}

// A pull stamps the owner only when the card has none: the first session to take a card made
// with no session owns it, and later pulls keep that.
func TestAPullStampsTheOwnerOfACardWithNone(t *testing.T) {
	f := newFixture(t)
	f.readyFrom("", "LOGIN-3")
	mustLines(t, f.pull(Args{Actor: "w2", ID: "LOGIN-3", Session: "s2"}))
	if got := f.ownerOf("LOGIN-3"); got != "s2" {
		t.Fatalf("owner after the first pull = %q, want s2", got)
	}
	mustLines(t, f.block(Args{Actor: "w2", ID: "LOGIN-3", Note: "waits"}))
	mustLines(t, f.unblock(Args{Actor: "lead", ID: "LOGIN-3"}))
	mustLines(t, f.pull(Args{Actor: "w3", ID: "LOGIN-3", Session: "s3"}))
	if got := f.ownerOf("LOGIN-3"); got != "s2" {
		t.Errorf("owner after a re-pull by s3 = %q, want s2", got)
	}
}
