package board

import (
	"os"
	"testing"
)

// The ticket bar (the spec's lint rules) gates promote and the ready-writing
// forms of new; --force overrides and the LOG line says forced.
func TestPromoteRefusesATicketUnderTheBar(t *testing.T) {
	f := newFixture(t)
	// a skeleton ticket: every section holds its fill comment
	mustLines(t, f.new(NewTicket{Actor: "lead", ID: "SKEL-1",
		Title: "skeleton", Cluster: "c", Summary: longSummary}))

	msg := mustRefuse(t, f.promote(Args{Actor: "lead", ID: "SKEL-1"}))
	contains(t, msg, "SKEL-1", "refusal names the ticket")
	contains(t, msg, "fill", "refusal lists the failures")
	if f.exists("ready", "SKEL-1") || !f.exists("backlog", "SKEL-1") {
		t.Error("the refused promote moved the ticket")
	}

	// --force moves it and the LOG line says forced
	mustLines(t, f.promote(Args{Actor: "lead", ID: "SKEL-1", Force: true}))
	if !f.exists("ready", "SKEL-1") {
		t.Error("the forced promote did not move the ticket")
	}
	last := f.lastLog()
	if last["event"] != "promote" || last["forced"] != true {
		t.Errorf("forced promote LOG = %v", last)
	}
}

func TestPromoteRefusesATicketWithAMissingDependency(t *testing.T) {
	f := newFixture(t)
	f.card("backlog", "DEP-BAD", "id: DEP-BAD\ntitle: \"d\"\ntype: docs\ncluster: c\n"+
		"priority: P2\nest: M\ndepends_on: [NOPE-9]\nsummary: \""+longSummary+"\"", fullBody("DEP-BAD"))
	msg := mustRefuse(t, f.promote(Args{Actor: "lead", ID: "DEP-BAD"}))
	contains(t, msg, "NOPE-9", "refusal names the missing dependency")
}

func TestNewReadyFormsRefuseTheSkeleton(t *testing.T) {
	f := newFixture(t)

	for _, tc := range []struct {
		name string
		n    NewTicket
	}{
		{"ready", NewTicket{Actor: "lead", ID: "RDY-1", Title: "t", Cluster: "c",
			Summary: longSummary, Ready: true}},
		{"expedite", NewTicket{Actor: "lead", ID: "HOT-9", Title: "t", Cluster: "c",
			Summary: longSummary, Expedite: true}},
	} {
		msg := mustRefuse(t, f.new(tc.n))
		contains(t, msg, tc.n.ID, tc.name)
		if f.exists("ready", tc.n.ID) {
			t.Errorf("%s: the ticket was written", tc.name)
		}
		// --force overrides and logs it
		forced := tc.n
		forced.Force = true
		mustLines(t, f.new(forced))
		if !f.exists("ready", tc.n.ID) {
			t.Errorf("%s: the forced new wrote nothing", tc.name)
		}
		last := f.lastLog()
		if last["event"] != "new" || last["forced"] != true {
			t.Errorf("%s: forced new LOG = %v", tc.name, last)
		}
		if err := os.Remove(f.path("ready", tc.n.ID)); err != nil {
			t.Fatal(err)
		}
	}

	// a complete body passes the bar without --force
	mustLines(t, f.new(NewTicket{Actor: "lead", ID: "FULL-1", Title: "t", Cluster: "c",
		Summary: longSummary, Ready: true, BodyFile: f.bodyFile(t, "full-body.md", fullBody("FULL-1"))}))
	if !f.exists("ready", "FULL-1") {
		t.Error("the complete ready ticket was refused")
	}
	// a plain new lands in backlog with no bar check
	mustLines(t, f.new(NewTicket{Actor: "lead", ID: "BACK-1", Title: "t", Cluster: "c",
		Summary: longSummary}))
	if !f.exists("backlog", "BACK-1") {
		t.Error("the plain new did not land in backlog")
	}
}
