package hub

// A session's agent-team teammates: a report that names a teammate records its state under the
// session, a later report for the same teammate replaces it, and every read of the session
// carries its teammates, sorted by name.

import (
	"context"
	"testing"
)

func TestATeammateIdleReportListsTheTeammateUnderItsSession(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "lead", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:01.000Z"},
		{SessionID: "lead", LastEvent: "TeammateIdle", LastEventAt: "2026-10-16T09:00:02.000Z",
			Teammate: &Teammate{Name: "tester", State: "idle"}},
		{SessionID: "lead", LastEvent: "TeammateIdle", LastEventAt: "2026-10-16T09:00:03.000Z",
			Teammate: &Teammate{Name: "researcher", State: "idle"}},
		// the same teammate idles again: one row, not two
		{SessionID: "lead", LastEvent: "TeammateIdle", LastEventAt: "2026-10-16T09:00:04.000Z",
			Teammate: &Teammate{Name: "researcher", State: "idle"}},
		{SessionID: "alone", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:05.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	rows, err := db.Sessions(ctx, SessionsFilter{}, readNow)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	got := map[string][]Teammate{}
	for _, row := range rows {
		got[row.SessionID] = row.Teammates
	}
	lead := got["lead"]
	if len(lead) != 2 || lead[0] != (Teammate{Name: "researcher", State: "idle"}) ||
		lead[1] != (Teammate{Name: "tester", State: "idle"}) {
		t.Errorf("lead's teammates = %+v, want researcher and tester, both idle", lead)
	}
	if len(got["alone"]) != 0 {
		t.Errorf("alone's teammates = %+v, want none", got["alone"])
	}
	one, err := db.SessionRow(ctx, "lead", readNow)
	if err != nil || one == nil {
		t.Fatalf("session row: %v %v", one, err)
	}
	if len(one.Teammates) != 2 {
		t.Errorf("the one-row read carries %+v, want both teammates", one.Teammates)
	}
}

func TestAReportWithANamelessTeammateRecordsNoTeammate(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.Report(ctx, Report{SessionID: "lead", LastEvent: "TeammateIdle",
		LastEventAt: "2026-10-16T09:00:02.000Z", Teammate: &Teammate{State: "idle"}}); err != nil {
		t.Fatalf("report: %v", err)
	}
	one, err := db.SessionRow(ctx, "lead", readNow)
	if err != nil || one == nil {
		t.Fatalf("session row: %v %v", one, err)
	}
	if len(one.Teammates) != 0 {
		t.Errorf("teammates = %+v, want none", one.Teammates)
	}
}
