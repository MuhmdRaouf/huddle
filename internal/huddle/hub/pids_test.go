package hub

import (
	"context"
	"reflect"
	"testing"
)

func TestLivePidsNamesOpenSessionsWithAPid(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-live", Pid: 4100, LastEvent: "SessionStart", LastEventAt: "2026-10-25T08:00:00.000Z"},
		{SessionID: "s-nopid", LastEvent: "SessionStart", LastEventAt: "2026-10-25T08:00:00.000Z"},
		{SessionID: "s-ended", Pid: 4200, LastEvent: "SessionStart", LastEventAt: "2026-10-25T08:00:00.000Z"},
		{SessionID: "s-ended", LastEvent: "SessionEnd", LastEventAt: "2026-10-25T08:10:00.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	pids, err := db.LivePids(ctx)
	if err != nil {
		t.Fatalf("live pids: %v", err)
	}
	want := []SessionPid{{SessionID: "s-live", Pid: 4100}}
	if !reflect.DeepEqual(pids, want) {
		t.Errorf("live pids = %+v, want %+v", pids, want)
	}
}
