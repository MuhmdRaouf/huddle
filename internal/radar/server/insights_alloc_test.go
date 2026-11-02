package server

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

const (
	allocSessions    = 200
	allocBuildRounds = 5
)

// alertBuildCost is the least heap one alert build allocates over a store holding the given
// number of requests across allocSessions sessions, each with a tool call per request and an
// event per ten. The store's own caches are warm, so the number is the build's alone.
func alertBuildCost(t *testing.T, requests int) (bytes, mallocs uint64) {
	t.Helper()
	st := store.CreateStore(clock)
	for s := 0; s < allocSessions; s++ {
		cwd := "/w/app"
		st.UpsertSession(store.SessionUpsert{ID: fmt.Sprintf("s%d", s), Cwd: &cwd})
	}
	at := nowAt.UnixMilli() - int64(requests)
	for n := 0; n < requests; n++ {
		session := fmt.Sprintf("s%d", n%allocSessions)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts, r.Model = fmt.Sprintf("r%d", n), session, at+int64(n), "glm-5.3"
		}), nil)
		st.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
			c.ID, c.SessionID, c.StartedAt = fmt.Sprintf("t%d", n), session, at+int64(n)
		}))
		if n%10 == 0 {
			st.AddSpoolLine(shared.SpoolLine{
				Ts:        time.UnixMilli(at + int64(n)).UTC().Format(time.RFC3339Nano),
				Event:     "UserPromptSubmit",
				SessionID: &session,
			})
		}
	}
	created := CreateInsights(context.Background(), InsightsOptions{
		Env: makeEnv(t).env, Store: st, Now: clock, TickMs: 0,
	})
	t.Cleanup(created.Stop)

	build := func() {
		created.mu.Lock()
		defer created.mu.Unlock()
		created.alertCache = nil
		created.allAlertsLocked()
	}
	build()
	bytes = ^uint64(0)
	for round := 0; round < allocBuildRounds; round++ {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		build()
		runtime.ReadMemStats(&after)
		if spent := after.TotalAlloc - before.TotalAlloc; spent < bytes {
			bytes, mallocs = spent, after.Mallocs-before.Mallocs
		}
	}
	return bytes, mallocs
}

// An alert build reads the store's own per-session records: what it allocates does not grow
// with the number of records held.
func TestAlertBuildAllocatesNothingPerRecord(t *testing.T) {
	smallBytes, smallMallocs := alertBuildCost(t, 2_000)
	largeBytes, largeMallocs := alertBuildCost(t, 20_000)
	t.Logf("2,000 requests: %d B, %d allocs; 20,000 requests: %d B, %d allocs",
		smallBytes, smallMallocs, largeBytes, largeMallocs)
	if largeBytes > 2*smallBytes {
		t.Errorf("a build over 20,000 requests allocated %d B, over 2,000 %d B: want under twice, flat in the record count",
			largeBytes, smallBytes)
	}
}
