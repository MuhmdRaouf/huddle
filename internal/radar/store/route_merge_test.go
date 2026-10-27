package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

var routeT = time.Date(2026, 10, 8, 15, 31, 38, 587_000_000, time.UTC).UnixMilli()

// route is a router's spool line as the watcher hands it over: upstream host, route kind,
// latency, maybe usage.
func route(shape func(*shared.RequestRecord)) shared.RequestRecord {
	return makeRequest(func(r *shared.RequestRecord) {
		r.ID = fmt.Sprintf("route:s1:%d:200", routeT)
		r.AgentID = ""
		r.Model = "claude-opus-5-5"
		r.Upstream = "api.anthropic.com"
		r.Ts = routeT
		r.LatencyMs = i64(1951)
		r.Tokens = shared.ZeroTokens
		r.StopReason = str("200")
		r.Provider = "Anthropic"
		r.Route = str("anthropic")
		if shape != nil {
			shape(r)
		}
	})
}

// transcriptCopy is the transcript's copy of the same call: its agent, tokens and stop
// reason, no upstream.
func transcriptCopy(shape func(*shared.RequestRecord)) shared.RequestRecord {
	return makeRequest(func(r *shared.RequestRecord) {
		r.ID = "req_011"
		r.AgentID = "agent-a31cd3de580148dc3"
		r.Model = "claude-opus-5-5"
		r.Upstream = ""
		r.Ts = routeT + 40
		r.LatencyMs = i64(1951)
		r.Tokens = tokens(3, 120, 9_000, 400)
		r.StopReason = str("tool_use")
		r.Provider = "Anthropic"
		if shape != nil {
			shape(r)
		}
	})
}

// findRequest returns the stored record with the id, or nil when the store holds none.
func findRequest(records []shared.RequestRecord, id string) *shared.RequestRecord {
	for i := range records {
		if records[i].ID == id {
			return &records[i]
		}
	}
	return nil
}

func TestRequestSeenByRouterAndTranscript(t *testing.T) {
	t.Run("is one request when the spool line comes first", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) {
			r.AgentID = "a31cd3de580148dc3"
			r.Status = str("200")
			r.StopReason = nil
		}), nil)
		store.AddRequest(transcriptCopy(nil), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 1 {
			t.Fatalf("requests: got %d, want 1", len(rows))
		}
		first := rows[0]
		if first.AgentID != "agent-a31cd3de580148dc3" || first.Upstream != "api.anthropic.com" ||
			!eqInt(first.LatencyMs, 1951) || !eqStr(first.StopReason, "tool_use") ||
			first.Route == nil || *first.Route != "anthropic" {
			t.Errorf("merged row: got %+v, want the transcript's agent under the route's facts", first)
		}
		// the route line's HTTP status survives the pairing with the transcript's copy
		if !eqStr(first.Status, "200") {
			t.Errorf("merged status: got %v, want 200", first.Status)
		}
		if got := shared.TotalTokens(first.Tokens); got != 9523 {
			t.Errorf("tokens: got %d, want 9523", got)
		}
		if summary := store.Summary(); summary.Requests != 1 {
			t.Errorf("summary requests: got %d, want 1", summary.Requests)
		}
	})

	t.Run("names both the router it went through and where the router sent it", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.Via = str("127.0.0.1:17787") }), nil)
		store.AddRequest(route(func(r *shared.RequestRecord) { r.Via = str("zai") }), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) == 0 {
			t.Fatal("requests: got none, want the paired call")
		}
		first := rows[0]
		if first.Upstream != "api.anthropic.com" || first.Via == nil || *first.Via != "zai" {
			t.Errorf("row: got upstream %q via %v, want api.anthropic.com via zai", first.Upstream, first.Via)
		}
	})

	t.Run("replaces the transcript's anthropic default with the upstream the router actually used", func(t *testing.T) {
		// the watcher now writes the default upstream on a claude-* transcript call it cannot place
		stores := []*Store{newTestStore(), newTestStore()}
		stores[0].AddRequest(route(func(r *shared.RequestRecord) { r.Upstream = "https://api.z.ai"; r.Via = str("zai") }), nil)
		stores[0].AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
			r.Upstream = shared.DefaultUpstream
			r.Via = str("127.0.0.1:17787")
		}), nil)
		stores[1].AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
			r.Upstream = shared.DefaultUpstream
			r.Via = str("127.0.0.1:17787")
		}), nil)
		stores[1].AddRequest(route(func(r *shared.RequestRecord) { r.Upstream = "https://api.z.ai"; r.Via = str("zai") }), nil)
		for at, store := range stores {
			rows := store.Requests(RequestFilter{})
			if len(rows) != 1 {
				t.Fatalf("store %d: got %d requests, want 1", at, len(rows))
			}
			if rows[0].Upstream != "https://api.z.ai" || rows[0].Via == nil || *rows[0].Via != "zai" {
				t.Errorf("store %d: got upstream %q via %v, want https://api.z.ai via zai", at, rows[0].Upstream, rows[0].Via)
			}
		}
	})

	t.Run("is one request when the transcript comes first, and its streamed copies stay merged", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.Tokens = tokens(3, 1, 9_000, 400) }), nil)
		store.AddRequest(route(nil), nil)
		store.AddRequest(transcriptCopy(nil), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 1 {
			t.Fatalf("requests: got %d, want 1", len(rows))
		}
		first := rows[0]
		if first.AgentID != "agent-a31cd3de580148dc3" || first.Upstream != "api.anthropic.com" {
			t.Errorf("merged row: got agent %q upstream %q", first.AgentID, first.Upstream)
		}
		if first.Tokens.Output != 120 {
			t.Errorf("output tokens: got %d, want 120", first.Tokens.Output)
		}
	})

	t.Run("keeps the route line's final usage over the transcript's streamed-opening copy, either order", func(t *testing.T) {
		// the provider's own final numbers, which the router tracked before any rewrite of its stream
		real := tokens(148_503, 902, 12_115, 3_022)
		// what Claude Code's transcript records when the router rewrote the stream's zero message_start
		streamed := tokens(148_500, 902, 0, 0)
		copyLine := func(shape func(*shared.RequestRecord)) shared.RequestRecord {
			return transcriptCopy(func(r *shared.RequestRecord) {
				r.Model = "glm-5.3"
				r.Provider = "Z.ai"
				r.Tokens = streamed
				if shape != nil {
					shape(r)
				}
			})
		}
		routeLine := func() shared.RequestRecord {
			return route(func(r *shared.RequestRecord) {
				r.Model = "glm-5.3"
				r.Provider = "Z.ai"
				r.Route = str("provider")
				r.Upstream = "api.z.ai"
				r.Tokens = real
			})
		}
		routeFirst := newTestStore()
		routeFirst.AddRequest(routeLine(), nil)
		routeFirst.AddRequest(copyLine(nil), nil)
		routeFirst.AddRequest(copyLine(nil), nil) // Claude Code's final entry repeats the opening numbers
		transcriptFirst := newTestStore()
		transcriptFirst.AddRequest(copyLine(nil), nil)
		transcriptFirst.AddRequest(routeLine(), nil)
		transcriptFirst.AddRequest(copyLine(nil), nil)
		for at, store := range []*Store{routeFirst, transcriptFirst} {
			rows := store.Requests(RequestFilter{})
			if len(rows) != 1 {
				t.Fatalf("store %d: got %d requests, want 1", at, len(rows))
			}
			if rows[0].Tokens != real {
				t.Errorf("store %d tokens: got %+v, want %+v", at, rows[0].Tokens, real)
			}
		}
	})

	t.Run("takes tokens from the spool when the transcript has none, and the model's provider for cost", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) {
			r.Model = "glm-5.3"
			r.Upstream = "api.z.ai"
			r.Provider = "Z.ai"
			r.Route = str("provider")
			r.AgentID = "a77"
			r.Tokens = tokens(1000, 200, 0, 0)
		}), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
			r.Model = "glm-5.3"
			r.AgentID = "agent-a77"
			r.Provider = "Z.ai"
			r.Tokens = shared.ZeroTokens
		}), nil)
		models := store.Models(nil)
		if len(models.Models) != 1 {
			t.Fatalf("models: got %d rows, want 1", len(models.Models))
		}
		row := models.Models[0]
		if row.Model != "glm-5.3" || row.Provider != "Z.ai" || row.Requests != 1 || row.CostUsd == nil {
			t.Errorf("model row: got %+v, want glm-5.3 by Z.ai, one priced request", row)
		}
		if got := shared.TotalTokens(row.Tokens); got != 1200 {
			t.Errorf("tokens: got %d, want 1200", got)
		}
		upstreams := models.Upstreams
		if len(upstreams) != 1 || upstreams[0].Host != "api.z.ai" || upstreams[0].Requests != 1 {
			t.Errorf("upstreams: got %+v, want one api.z.ai row", upstreams)
		}
	})

	t.Run("keeps a result-filled request's run-total flag, whichever copy arrives first", func(t *testing.T) {
		runTotals := tokens(62_639, 10_446, 857_600, 0)
		stores := []*Store{newTestStore(), newTestStore()}
		stores[0].AddRequest(route(nil), nil)
		stores[0].AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.Totals = true; r.Tokens = runTotals }), nil)
		stores[1].AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.Totals = true; r.Tokens = runTotals }), nil)
		stores[1].AddRequest(route(nil), nil)
		for at, store := range stores {
			rows := store.Requests(RequestFilter{})
			if len(rows) != 1 || !rows[0].Totals {
				t.Errorf("store %d: got %+v, want one run-total request", at, rows)
			}
		}
	})

	t.Run("keeps the run-total flag when the result lands as a later write of a held request", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.Tokens = shared.ZeroTokens }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
			r.Totals = true
			r.Tokens = tokens(62_639, 10_446, 857_600, 0)
		}), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 1 || rows[0].ID != "req_011" || !rows[0].Totals {
			t.Errorf("rows: got %+v, want req_011 flagged as a run total", rows)
		}
	})

	t.Run("does not pair calls of another agent, another model or far apart in time", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) { r.AgentID = "a99" }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "other-model"; r.Model = "claude-sonnet-5-5" }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "too-late"; r.Ts = routeT + 60_000 }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "other-agent" }), nil)
		if rows := store.Requests(RequestFilter{}); len(rows) != 4 {
			t.Errorf("requests: got %d, want 4 unpaired ones", len(rows))
		}
	})

	t.Run("pairs each spool line with the closest transcript call once", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "first"; r.Ts = routeT - 3_000; r.LatencyMs = i64(1230) }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "second"; r.Ts = routeT + 20; r.LatencyMs = i64(2910) }), nil)
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = routeT - 3_010; r.LatencyMs = i64(1230) }), nil)
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "r2"; r.Ts = routeT; r.LatencyMs = i64(2910) }), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 2 {
			t.Fatalf("requests: got %d, want 2", len(rows))
		}
		for _, row := range rows {
			if row.Upstream != "api.anthropic.com" {
				t.Errorf("row %s: got upstream %q, want api.anthropic.com", row.ID, row.Upstream)
			}
		}
	})

	t.Run("counts a spool line with no transcript copy on its own, under the main agent", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) { r.Tokens = tokens(14, 16, 0, 0) }), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 1 || rows[0].AgentID != "main" || rows[0].Upstream != "api.anthropic.com" {
			t.Errorf("rows: got %+v, want one unpaired line under main", rows)
		}
	})
}

func TestUpstreamRowsOverStore(t *testing.T) {
	t.Run("list every host once with its requests, tokens and cost, and name transcript-only calls plainly", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "a"; r.Upstream = "api.anthropic.com" }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
			r.ID = "b"
			r.Upstream = "https://api.anthropic.com"
			r.Ts = routeT + 600_000
		}), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "c"; r.Upstream = ""; r.Ts = routeT + 900_000 }), nil)
		store.AddRequest(route(func(r *shared.RequestRecord) {
			r.ID = "d"
			r.Model = "kimi-k3"
			r.Upstream = "api.moonshot.ai"
			r.Provider = "Moonshot"
			r.Route = str("provider")
			r.Tokens = tokens(10, 10, 0, 0)
		}), nil)
		rows := store.Models(nil).Upstreams
		got := make(map[string]int64, len(rows))
		for _, row := range rows {
			got[row.Host] = row.Requests
		}
		want := map[string]int64{
			"api.anthropic.com":   2,
			shared.DirectUpstream: 1,
			"api.moonshot.ai":     1,
		}
		if len(rows) != 3 {
			t.Fatalf("upstreams: got %d rows (%v), want 3", len(rows), got)
		}
		for host, requests := range want {
			if got[host] != requests {
				t.Errorf("host %s: got %d requests, want %d", host, got[host], requests)
			}
		}
		for _, row := range rows {
			if row.Host == "api.moonshot.ai" && row.CostUsd == nil {
				t.Errorf("moonshot row: got no cost, want one")
			}
		}
	})
}

func TestPruneWaiting(t *testing.T) {
	recordOf := func(records map[string]int64) func(string) (int64, bool) {
		return func(id string) (int64, bool) {
			ts, ok := records[id]
			return ts, ok
		}
	}

	t.Run("drops waiting entries older than the match window and records the store evicted", func(t *testing.T) {
		records := map[string]int64{"fresh": 19_000, "stale": 0, "future": 25_000}
		waiting := newWaitingMap()
		for _, id := range []string{"fresh", "stale", "future", "evicted"} {
			waiting.Set(id, "main")
		}
		PruneWaiting(waiting, 20_000, recordOf(records))
		ids := waiting.IDs()
		if !slices.Equal(ids, []string{"fresh", "future"}) {
			t.Errorf("waiting: got %v, want [fresh future]", ids)
		}
		for _, id := range ids {
			if agent, ok := waiting.Get(id); !ok || agent != "main" {
				t.Errorf("waiting[%s]: got %q, want main", id, agent)
			}
		}
	})

	t.Run("empties a Set the same way, leaving nothing for the scan to trip over", func(t *testing.T) {
		records := map[string]int64{"kept": 15_000}
		waiting := newWaitingSet()
		for _, id := range []string{"kept", "gone", "ancient"} {
			waiting.Add(id)
		}
		PruneWaiting(waiting, 20_000, recordOf(records))
		if ids := waiting.IDs(); !slices.Equal(ids, []string{"kept"}) {
			t.Errorf("waiting: got %v, want [kept]", ids)
		}
	})
}

func TestBurstCopiesKeepTheirRouteWindow(t *testing.T) {
	t.Run("keeps an older copy's route window when a newer call of the same session waits too", func(t *testing.T) {
		// the probe: two copies of one (session, model) 26 s apart in one burst, then the
		// first call's route line. The older copy must still be waiting when its line comes.
		store := newTestStore()
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "copy-early" }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "copy-late"; r.Ts = routeT + 26_000 }), nil)
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "route-early"; r.Ts = routeT + 800 }), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 2 {
			t.Fatalf("requests: got %d, want 2", len(rows))
		}
		early := findRequest(rows, "copy-early")
		if early == nil || early.Route == nil || early.Upstream != "api.anthropic.com" {
			t.Errorf("copy-early: got %+v, want the call paired with its route line", early)
		}
		if late := findRequest(rows, "copy-late"); late == nil || late.Route != nil {
			t.Errorf("copy-late: got %+v, want the transcript-only call", late)
		}
		if summary := store.Summary(); summary.Requests != 2 {
			t.Errorf("summary requests: got %d, want 2", summary.Requests)
		}
	})

	t.Run("pairs a route line crossing a pass boundary before the session's next copy waits", func(t *testing.T) {
		// pass 1 tails the copy; pass 2's spool delivers the line before its phase tails the
		// session's next copy. This boundary pairs today and must keep pairing.
		store := newTestStore()
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "copy-early" }), nil)
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "route-early"; r.Ts = routeT + 800 }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "copy-late"; r.Ts = routeT + 26_000 }), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 2 {
			t.Fatalf("requests: got %d, want 2", len(rows))
		}
		early := findRequest(rows, "copy-early")
		if early == nil || early.Route == nil || early.Upstream != "api.anthropic.com" {
			t.Errorf("copy-early: got %+v, want the call paired with its route line", early)
		}
		if late := findRequest(rows, "copy-late"); late == nil || late.Route != nil {
			t.Errorf("copy-late: got %+v, want the transcript-only call", late)
		}
		if summary := store.Summary(); summary.Requests != 2 {
			t.Errorf("summary requests: got %d, want 2", summary.Requests)
		}
	})
}

// storm feeds n unpairable transcript calls of one session and model, spaced past the match
// window.
func storm(store *Store, n int) {
	for at := 1; at <= n; at++ {
		at := int64(at)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
			r.ID = fmt.Sprintf("t%d", at)
			r.Ts = routeT + at*11_000
		}), nil)
	}
}

func TestWaitingMapsACatchupFeeds(t *testing.T) {
	t.Run("still pairs a call with its spool line inside the window, however many pairs came before", func(t *testing.T) {
		store := newTestStore()
		storm(store, 50)
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "late-route"; r.Ts = routeT + 600_000 }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "late-copy"; r.Ts = routeT + 600_000 + 40 }), nil)
		rows := store.Requests(RequestFilter{})
		if len(rows) != 51 {
			t.Fatalf("requests: got %d, want 51", len(rows))
		}
		late := findRequest(rows, "late-route")
		if late == nil || late.Upstream != "api.anthropic.com" || !eqStr(late.StopReason, "tool_use") {
			t.Errorf("late-route: got %+v, want the paired call", late)
		}
	})

	t.Run("stops pairing a spool line once a call more than the window newer has arrived", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "early-route"; r.Ts = routeT }), nil)
		storm(store, 3) // the second of these is already past the early line's window
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "late-echo"; r.Ts = routeT + 5_000 }), nil)
		rows := store.Requests(RequestFilter{Limit: i64(5_000)})
		early := findRequest(rows, "early-route")
		if early == nil || early.AgentID != "main" {
			t.Errorf("early-route: got %+v, want unpaired under the main agent", early)
		}
		// the echo stands on its own: its router twin left the window before it arrived
		if echo := findRequest(rows, "late-echo"); echo == nil || echo.Route != nil {
			t.Errorf("late-echo: got %+v, want no route on it", echo)
		}
	})

	t.Run("forgets a transcript copy's alias once newer pairs have pushed it out of the window", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "first-route" }), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "first-copy" }), nil)
		for at := 1; at <= 600; at++ {
			at := int64(at)
			// the alias map holds a bounded window of the newest pairs
			store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = fmt.Sprintf("rr%d", at); r.Ts = routeT + at*1_000 }), nil)
			store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("tc%d", at)
				r.Ts = routeT + at*1_000 + 40
			}), nil)
		}
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.ID = "first-copy"; r.Ts = routeT + 2_000 }), nil)
		rows := store.Requests(RequestFilter{Limit: i64(5_000)})
		// the copy no longer folds into the held record: its alias was dropped with the oldest pairs
		count := 0
		for _, row := range rows {
			if row.ID == "first-route" || row.ID == "first-copy" {
				count++
			}
		}
		if count != 2 {
			t.Errorf("first route and copy: got %d rows, want 2", count)
		}
	})
}

func TestPairingLooksTheRequestIDUpFirst(t *testing.T) {
	t.Run("the request id wins over a closer fit in the window", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(route(func(r *shared.RequestRecord) { r.ID = "route:a"; r.RequestID = "req_A" }), nil)
		store.AddRequest(route(func(r *shared.RequestRecord) {
			r.ID = "route:b"
			r.RequestID = "req_B"
			r.Ts = routeT + 30
		}), nil)
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.RequestID = "req_A"; r.Ts = routeT + 35 }), nil)
		rows := store.Requests(RequestFilter{})
		paired, alone := findRequest(rows, "route:a"), findRequest(rows, "route:b")
		if len(rows) != 2 || paired == nil || alone == nil {
			t.Fatalf("requests: got %+v, want route:a paired and route:b alone", rows)
		}
		if paired.Tokens != tokens(3, 120, 9_000, 400) || shared.TotalTokens(alone.Tokens) != 0 {
			t.Errorf("tokens: route:a %+v, route:b %+v; want the copy's on route:a only", paired.Tokens, alone.Tokens)
		}
	})

	t.Run("a copy that left the window still meets its route line by request id", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) { r.RequestID = "req_011"; r.Ts = routeT }), nil)
		for call := 1; call <= 20; call++ {
			store.AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("req_later%02d", call)
				r.RequestID = r.ID
				r.Ts = routeT + int64(call)*1_000
			}), nil)
		}
		store.AddRequest(route(func(r *shared.RequestRecord) {
			r.AgentID = "a31cd3de580148dc3"
			r.RequestID = "req_011"
			r.Ts = routeT + 12
		}), nil)
		rows := store.Requests(RequestFilter{})
		merged := findRequest(rows, "req_011")
		if len(rows) != 21 || merged == nil || merged.Route == nil || merged.Tokens != tokens(3, 120, 9_000, 400) {
			t.Fatalf("requests: got %d rows, req_011 = %+v; want 21 rows and req_011 carrying the route", len(rows), merged)
		}
	})
}

// A merged call keeps the route line's body keys: the inspector fetches the body the
// router kept by the id the record carries, so they must survive the pairing and close
// the tail.
func TestMergedRequestKeepsRouteBodies(t *testing.T) {
	store := newTestStore()
	store.AddRequest(route(func(r *shared.RequestRecord) {
		r.AgentID = "a31cd3de580148dc3"
		r.Status = str("200")
		r.StopReason = nil
		r.Via = str("zai")
		r.BodyID = str("b-1")
		r.Body = str("kept")
	}), nil)
	store.AddRequest(transcriptCopy(nil), nil)
	rows := store.Requests(RequestFilter{})
	if len(rows) != 1 {
		t.Fatalf("requests: got %d, want 1", len(rows))
	}
	payload, err := json.Marshal(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	const tail = `"status":"200","route":"anthropic","via":"zai","bodyId":"b-1","body":"kept"}`
	if !strings.HasSuffix(string(payload), tail) {
		t.Errorf("merged record = %s, want the tail ending in %s", payload, tail)
	}
}
