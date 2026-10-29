package ledger_test

import (
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
)

// TestMigrateSettleFollowsTheSpoolWindow: the settle age is the spool's re-read window —
// SpoolWindowDays plus the day before it — plus the window a line pairs with a copy
// inside, so past it no spool line a hub restart re-feeds can still pair with a row.
func TestMigrateSettleFollowsTheSpoolWindow(t *testing.T) {
	want := time.Duration(ingest.SpoolWindowDays+1)*24*time.Hour + 10*time.Second
	if ledger.MigrateSettle != want {
		t.Errorf("MigrateSettle = %v, want %v", ledger.MigrateSettle, want)
	}
}
