package board

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMoveRepairsTheBoard(t *testing.T) {
	f := newFixture(t)
	f.card("in-progress", "REP-1", "id: REP-1\ntitle: \"r\"\ntype: docs\nclaimed_by: w1")

	out := mustLines(t, f.move(Args{Actor: "lead", ID: "REP-1", Column: "backlog", Note: "filed wrongly"}))
	lastEndsWith(t, out, "backlog", "REP-1")
	last := f.lastLog()
	if last["event"] != "move" || last["ticket"] != "REP-1" || last["forced"] != true {
		t.Errorf("move LOG = %v", last)
	}
	// the holder field moves with the file; move stamps nothing
	contains(t, f.fm("backlog", "REP-1"), "claimed_by: w1", "fm")

	// a bad column name is refused, the ticket stays put
	msg := mustRefuse(t, f.move(Args{Actor: "lead", ID: "REP-1", Column: "doing"}))
	contains(t, msg, "not a column", "move")
	if !f.exists("backlog", "REP-1") {
		t.Error("REP-1 moved to a column that does not exist")
	}
	// a missing ticket is named
	contains(t, mustRefuse(t, f.move(Args{Actor: "lead", ID: "NOPE-1", Column: "ready"})),
		"not on the board", "move")
}

func TestMoveGuardsDoneForImplementationTickets(t *testing.T) {
	f := newFixture(t)

	// into done without a test_note: refused for an implementation ticket
	f.card("review", "IMP-1", "id: IMP-1\ntitle: \"i\"\ntype: fix\ncluster: c\nsummary: \""+longSummary+"\"",
		fullBody("IMP-1"))
	contains(t, mustRefuse(t, f.move(Args{Actor: "lead", ID: "IMP-1", Column: "done", Note: "skip gates"})),
		"test_note", "move")
	if !f.exists("review", "IMP-1") {
		t.Error("IMP-1 moved into done without a test_note")
	}

	// with a test_note the lead may move it
	f.card("review", "IMP-2", "id: IMP-2\ntitle: \"i\"\ntype: fix\ncluster: c\n"+
		"test_note: \"pytest exit 0\"\nsummary: \""+longSummary+"\"", fullBody("IMP-2"))
	mustLines(t, f.move(Args{Actor: "lead", ID: "IMP-2", Column: "done"}))
	if !f.exists("done", "IMP-2") {
		t.Error("IMP-2 did not move")
	}
	// out of done without a test_note: refused too
	f.card("done", "IMP-3", "id: IMP-3\ntitle: \"i\"\ntype: feature\nsummary: \""+longSummary+"\"")
	contains(t, mustRefuse(t, f.move(Args{Actor: "lead", ID: "IMP-3", Column: "testing"})),
		"test_note", "move")

	// a prose ticket moves freely in and out of done
	f.card("done", "DOC-9", "id: DOC-9\ntitle: \"d\"\ntype: docs")
	mustLines(t, f.move(Args{Actor: "lead", ID: "DOC-9", Column: "ready"}))
	if !f.exists("ready", "DOC-9") {
		t.Error("DOC-9 did not leave done")
	}
}

func TestRecordHandMove(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("ready", "HAND-1", "id: HAND-1\ntitle: \"h\"\ntype: docs")

	// the watcher sees a file move between columns and records it
	if err := os.Rename(f.path("ready", "HAND-1"), f.path("in-progress", "HAND-1")); err != nil {
		t.Fatal(err)
	}
	if err := f.b.RecordHandMove(ctx, "HAND-1", "ready", "in-progress"); err != nil {
		t.Fatalf("RecordHandMove: %v", err)
	}
	contains(t, f.fm("in-progress", "HAND-1"), `moved_by_hand: "ready -> in-progress at `, "fm")
	last := f.lastLog()
	if last["event"] != "moved-by-hand" || last["ticket"] != "HAND-1" ||
		last["from"] != "ready" || last["to"] != "in-progress" {
		t.Errorf("hand-move LOG = %v", last)
	}

	// the next flow command on the ticket clears the mark
	mustLines(t, f.review(Args{Actor: "w1", ID: "HAND-1", Note: "look"}))
	notContains(t, f.fm("review", "HAND-1"), "moved_by_hand", "fm")

	// a hand move of a ticket that is nowhere is an error, not a refusal
	if err := f.b.RecordHandMove(ctx, "NOPE-9", "ready", "done"); err == nil {
		t.Error("recording a hand move of a missing ticket answers no error")
	}
}

func TestLastFlowEvent(t *testing.T) {
	f := newFixture(t)
	f.card("backlog", "EV-1", "id: EV-1\ntitle: \"e\"\ntype: docs\ncluster: c\n"+
		"priority: P2\nest: M\nsummary: \""+longSummary+"\"", fullBody("EV-1"))
	ctx := context.Background()

	// no flow event yet
	if _, ok := f.b.LastFlowEvent("EV-1"); ok {
		t.Error("LastFlowEvent answers true on an empty LOG")
	}
	mustLines(t, f.promote(Args{Actor: "lead", ID: "EV-1"}))
	when, ok := f.b.LastFlowEvent("EV-1")
	if !ok || !when.Equal(fixedNow) {
		t.Errorf("LastFlowEvent = %v, %v; want %v", when, ok, fixedNow)
	}

	// a non-flow event does not refresh it
	if err := os.Rename(f.path("ready", "EV-1"), f.path("in-progress", "EV-1")); err != nil {
		t.Fatal(err)
	}
	if err := f.b.RecordHandMove(ctx, "EV-1", "ready", "in-progress"); err != nil {
		t.Fatal(err)
	}
	when2, _ := f.b.LastFlowEvent("EV-1")
	if !when2.Equal(fixedNow) {
		t.Errorf("a moved-by-hand line refreshed the flow clock: %v", when2)
	}

	// an unrelated ticket's events do not answer for this one
	if _, ok := f.b.LastFlowEvent("OTHER-1"); ok {
		t.Error("LastFlowEvent answers for a ticket with no events")
	}
}

func TestLastFlowEventParsesAnyStamp(t *testing.T) {
	f := newFixture(t)
	f.writeFile("LOG", `{"ts":"2026-10-14T09:15:00+0300","event":"pull","ticket":"X-1","actor":"w1"}`+"\n")
	when, ok := f.b.LastFlowEvent("X-1")
	if !ok {
		t.Fatal("LastFlowEvent = false")
	}
	want := time.Date(2026, 10, 14, 9, 15, 0, 0, time.FixedZone("+0300", 3*3600))
	if !when.Equal(want) {
		t.Errorf("LastFlowEvent = %v, want %v", when, want)
	}
}

func TestHandMoveBadgeShowsUntilAFlowCommandTouches(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "BADGE-1", "id: BADGE-1\ntitle: \"b\"\ntype: docs")
	if err := os.Rename(f.path("ready", "BADGE-1"), f.path("blocked", "BADGE-1")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := f.b.RecordHandMove(ctx, "BADGE-1", "ready", "blocked"); err != nil {
		t.Fatal(err)
	}
	cards := mustCards(t, f.list("blocked"))
	if len(cards) != 1 {
		t.Fatalf("list blocked = %d cards, want 1", len(cards))
	}
	found := false
	for _, badge := range cards[0].Badges {
		if badge.Kind == "hand" {
			found = true
		}
	}
	if !found {
		t.Errorf("the hand-move badge is missing: %+v", cards[0].Badges)
	}
	if cards[0].MovedByHand == "" {
		t.Error("MovedByHand is empty")
	}
}

func TestRefusedIsAnExpectedFailure(t *testing.T) {
	var err error = &Refused{Message: "no"}
	var r *Refused
	if !errors.As(err, &r) || r.Error() != "no" {
		t.Errorf("Refused = %v", err)
	}
	if errors.Is(err, ErrNoBoard) {
		t.Error("Refused is not ErrNoBoard")
	}
}

func TestLogAppendsToTheLogSection(t *testing.T) {
	f := newFixture(t)
	f.card("in-progress", "LOG-1", "id: LOG-1\ntitle: \"l\"\ntype: fix\ncluster: c\nsummary: \""+longSummary+"\"",
		fullBody("LOG-1"))

	out := mustLines(t, f.log(Args{Actor: "w1", ID: "LOG-1", Note: "go test ./... exit 0"}))
	lastEndsWith(t, out, "in-progress", "LOG-1")
	s := f.fm("in-progress", "LOG-1")
	contains(t, s, "\n## Log\n- "+fixedNow.Format(time.RFC3339)+" w1: go test ./... exit 0\n", "fm")
	last := f.lastLog()
	if last["event"] != "log" || last["ticket"] != "LOG-1" || last["actor"] != "w1" {
		t.Errorf("log LOG = %v", last)
	}

	// a second entry lands under the first, not beside it
	mustLines(t, f.log(Args{Actor: "w1", ID: "LOG-1", Note: "go vet ./... exit 0"}))
	entries := strings.Count(f.fm("in-progress", "LOG-1"), "\n- "+fixedNow.Format(time.RFC3339)+" w1: ")
	if entries != 2 {
		t.Errorf("%d log entries, want 2", entries)
	}

	// the section is created at the end when the body has none
	f.card("ready", "LOG-2", "id: LOG-2\ntitle: \"l\"\ntype: docs", "# LOG-2\n\nBody text.\n")
	mustLines(t, f.log(Args{Actor: "w1", ID: "LOG-2", Note: "note"}))
	contains(t, f.fm("ready", "LOG-2"), "## Log\n- "+fixedNow.Format(time.RFC3339)+" w1: note\n", "fm")

	// a review note reaches ## Log too, and unblock's does not
	mustLines(t, f.review(Args{Actor: "w1", ID: "LOG-1", Note: "look at the diff"}))
	contains(t, f.fm("review", "LOG-1"), "- "+fixedNow.Format(time.RFC3339)+" w1: look at the diff\n", "fm")
}
