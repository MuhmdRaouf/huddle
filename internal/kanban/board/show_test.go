package board

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Show answers one ticket whole, wherever it sits: the card view, every
// front-matter field, the body and the file exactly as it is on disk.
func TestShowAnswersOneTicketWhole(t *testing.T) {
	f := newFixture(t)
	f.card("review", "SHO-1",
		"id: SHO-1\ntitle: \"show me\"\ntype: docs\ncluster: kanban\nsummary: \""+longSummary+"\"\nclaimed_by: w1",
		fullBody("SHO-1"))
	sh, err := f.b.Show(context.Background(), "SHO-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if sh.Card.ID != "SHO-1" || sh.Card.Column != "review" || sh.Card.Holder != "w1" {
		t.Errorf("card = %+v", sh.Card)
	}
	if sh.Fields["cluster"] != "kanban" {
		t.Errorf("fields = %v", sh.Fields)
	}
	if !strings.Contains(sh.Body, "## Description") {
		t.Errorf("body = %q", sh.Body)
	}
	if want := f.fm("review", "SHO-1"); sh.Text != want {
		t.Errorf("text = %q, want the file %q", sh.Text, want)
	}
	if want := f.path("review", "SHO-1"); sh.Path != want {
		t.Errorf("path = %q, want %q", sh.Path, want)
	}
}

// Show refuses a ticket that is nowhere on the board.
func TestShowRefusesAMissingTicket(t *testing.T) {
	f := newFixture(t)
	_, err := f.b.Show(context.Background(), "NOPE-1")
	var refused *Refused
	if !errors.As(err, &refused) {
		t.Fatalf("Show error = %v, want a refusal", err)
	}
	contains(t, refused.Message, "not on the board", "message")
}

// Show still answers a ticket that is not UTF-8: the card carries the failure,
// and the text keeps the bytes as they are.
func TestShowFlagsATicketThatIsNotUTF8(t *testing.T) {
	f := newFixture(t)
	raw := []byte("---\nid: LAT-9\ntitle: \"caf\xe9\"\ntype: docs\n---\n\n# LAT-9\n")
	if err := os.WriteFile(f.path("ready", "LAT-9"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sh, err := f.b.Show(context.Background(), "LAT-9")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	found := false
	for _, failure := range sh.Card.Lint.Failures {
		if strings.Contains(failure, "not UTF-8") {
			found = true
		}
	}
	if !found {
		t.Errorf("card failures = %v, want the not-UTF-8 one", sh.Card.Lint.Failures)
	}
	if sh.Text != string(raw) {
		t.Errorf("text = %q, want the raw bytes", sh.Text)
	}
}
