package radartab

// The project days the owner's insights card reads: the ledger's priced spend, one line per
// project and calendar day, the costs carried over as figures.

import (
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

func TestProjectDays(t *testing.T) {
	t.Run("a tab with no insights answers none", func(t *testing.T) {
		var dead *Tab
		if got := dead.ProjectDays(); got != nil {
			t.Errorf("a nil tab's days = %+v, want nil", got)
		}
		if got := (&Tab{}).ProjectDays(); got != nil {
			t.Errorf("a tab without insights' days = %+v, want nil", got)
		}
	})
	t.Run("the ledger's priced spend reaches the card, one row per project and day", func(t *testing.T) {
		env := tempEnv(t)
		fixed := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
		tab, _, _ := mountMux(t, env, MountOptions{
			Now:            func() time.Time { return fixed },
			IntervalMs:     quietMs,
			InsightsTickMs: quietMs,
		})
		if !tab.Owns {
			t.Fatal("the mount did not take the lock")
		}
		sid, cwd := "s-cost", "/Users/me/playground/huddle"
		tab.Store.AddSpoolLine(shared.SpoolLine{
			Ts:        fixed.UTC().Format("2006-01-02T15:04:05.000Z"),
			Event:     "SessionStart",
			SessionID: &sid,
			Cwd:       &cwd,
		})
		// the same priced call today and three days back; the model prices on its id alone
		tab.Store.AddRequest(shared.RequestRecord{
			ID: "r-today", SessionID: sid, AgentID: "main", Model: "claude-opus-5-5",
			Ts:     fixed.Add(-30 * time.Minute).UnixMilli(),
			Tokens: shared.Tokens{Input: 10_000, Output: 500},
		}, nil)
		tab.Store.AddRequest(shared.RequestRecord{
			ID: "r-old", SessionID: sid, AgentID: "main", Model: "claude-opus-5-5",
			Ts:     fixed.Add(-72 * time.Hour).UnixMilli(),
			Tokens: shared.Tokens{Input: 10_000, Output: 500},
		}, nil)

		got := tab.ProjectDays()
		if len(got) != 2 {
			t.Fatalf("the days = %+v, want 2 rows", got)
		}
		if got[0].Project != "huddle" || got[0].DaysAgo != 0 {
			t.Errorf("first row = %+v, want huddle today", got[0])
		}
		if got[1].Project != "huddle" || got[1].DaysAgo != 3 {
			t.Errorf("second row = %+v, want huddle three days back", got[1])
		}
		if got[1].CostUsd <= 0 {
			t.Errorf("the old day's cost = %v, want the call's price", got[1].CostUsd)
		}
		if got[0].CostUsd != got[1].CostUsd {
			t.Errorf("today's cost %.4f and the old day's %.4f, want the same call priced the same",
				got[0].CostUsd, got[1].CostUsd)
		}
	})
	t.Run("an empty ledger answers no rows", func(t *testing.T) {
		env := tempEnv(t)
		tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
		if got := tab.ProjectDays(); len(got) != 0 {
			t.Errorf("an empty ledger's days = %+v, want none", got)
		}
	})
}
