package history

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The upsert of a request: a later write for a call the file already holds never empties
// what the file knows about its usage.

// pricedRequest is a request with every usage fact set.
func pricedRequest() shared.RequestRecord {
	cost, thinking, cacheWrite1h := 0.0042, int64(7), int64(5)
	record := requestRecord()
	record.Tokens = shared.Tokens{Input: 100, Output: 30, CacheRead: 20, CacheWrite: 10, Thinking: &thinking}
	record.CostUSD = &cost
	record.CacheWrite1h = &cacheWrite1h
	record.Speed = sp("fast")
	record.Geo = sp("us")
	record.ServiceTier = sp("priority")
	return record
}

// putAndRead stores the records in order under one node and answers the row the file holds.
func putAndRead(t *testing.T, records ...shared.RequestRecord) shared.RequestRecord {
	t.Helper()
	h := openHistoryAt(t)
	if err := h.UpsertNode(nodeUpsert()); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	for _, record := range records {
		if err := h.PutRequest("s1/main", record); err != nil {
			t.Fatalf("put the request: %v", err)
		}
	}
	rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
	if err != nil {
		t.Fatalf("read the requests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the file holds %d rows, want 1: %+v", len(rows), rows)
	}
	return rows[0]
}

func TestAWriteWithNoUsageKeepsTheUsageTheRowHolds(t *testing.T) {
	known := pricedRequest()
	routeOnly := requestRecord()
	routeOnly.Tokens = shared.Tokens{}
	routeOnly.Status = sp("200")
	routeOnly.Route = sp("zai")

	got := putAndRead(t, known, routeOnly)

	want := known
	want.Status = routeOnly.Status
	want.Route = routeOnly.Route
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the row after the empty write = %+v, want the usage of %+v with the new status and route", got, want)
	}
}

func TestAWriteWithUsageReplacesTheUsageTheRowHolds(t *testing.T) {
	known := pricedRequest()
	final := requestRecord()
	final.Tokens = shared.Tokens{Input: 120, Output: 30}

	got := putAndRead(t, known, final)

	if !reflect.DeepEqual(got, final) {
		t.Errorf("the row after the write with usage = %+v, want %+v", got, final)
	}
}

func TestAFirstWriteWithNoUsageStoresNone(t *testing.T) {
	empty := requestRecord()
	empty.Tokens = shared.Tokens{}

	got := putAndRead(t, empty)

	if !reflect.DeepEqual(got, empty) {
		t.Errorf("the row after a first empty write = %+v, want %+v", got, empty)
	}
}

// A subagent's call, paired from its transcript copy and its route line, as an earlier run
// kept it.
func pairedSubagentCall() shared.RequestRecord {
	record := pricedRequest()
	record.AgentID = "ag1"
	record.Harness = shared.HarnessCodex
	record.StopReason = sp("end_turn")
	record.What = sp("reads the spec")
	record.Totals = true
	return record
}

// A spool line read again alone after a restart: it names no usage, and it names the agent
// "main" because it carries no agent tag.
func lonelyRouteLine() shared.RequestRecord {
	latency, ttfb, ttft := int64(99), int64(11), int64(22)
	record := requestRecord()
	record.Tokens = shared.Tokens{}
	record.Upstream = "https://api.z.ai/v2"
	record.LatencyMs = &latency
	record.TtfbMs = &ttfb
	record.TtftMs = &ttft
	record.Provider = "anthropic"
	record.Status = sp("502")
	record.Route = sp("router")
	record.Via = sp("huddle-router")
	record.Fallback = sp("glm-5.3")
	record.Error = sp("upstream closed")
	record.ParentAgentID = sp("lead")
	return record
}

func TestAWriteWithNoUsageKeepsWhatOnlyTheTranscriptKnows(t *testing.T) {
	h := openHistoryAt(t)
	held := pairedSubagentCall()
	if err := h.PutRequest("s1/ag1", held); err != nil {
		t.Fatalf("put the paired call: %v", err)
	}
	if err := h.PutRequest("s1/main", lonelyRouteLine()); err != nil {
		t.Fatalf("put the route line: %v", err)
	}

	rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/ag1"), Limit: 10})
	if err != nil {
		t.Fatalf("read the subagent's requests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the subagent's node holds %d rows, want its 1 call", len(rows))
	}

	route := lonelyRouteLine()
	want := held
	want.Upstream, want.LatencyMs, want.TtfbMs, want.TtftMs = route.Upstream, route.LatencyMs, route.TtfbMs, route.TtftMs
	want.Provider, want.Status, want.Route, want.Via = route.Provider, route.Status, route.Route, route.Via
	want.Fallback, want.Error, want.ParentAgentID = route.Fallback, route.Error, route.ParentAgentID
	want.EarlyKeys, want.TailKeys = rows[0].EarlyKeys, rows[0].TailKeys
	if !reflect.DeepEqual(rows[0], want) {
		t.Errorf("the row after the route line = %+v, want the transcript's facts of %+v under the route line's own", rows[0], want)
	}
}

func TestAWriteWithNoUsageFillsAStopReasonTheRowLacks(t *testing.T) {
	held := pairedSubagentCall()
	held.StopReason = nil
	failed := lonelyRouteLine()
	failed.AgentID = held.AgentID
	failed.StopReason = sp("502")

	got := putAndRead(t, held, failed)

	if got.StopReason == nil || *got.StopReason != "502" {
		t.Errorf("stop reason = %v, want the route line's 502 for a row that had none", got.StopReason)
	}
}

// runRecordingDB wraps a database, remembering every statement Run executes.
type runRecordingDB struct {
	DB
	statements []string
}

func (r *runRecordingDB) Run(query string, params ...SqlValue) (int64, error) {
	r.statements = append(r.statements, query)
	return r.DB.Run(query, params...)
}

// upsertStatements lists the statements a put of each record ran against the requests table.
func upsertStatements(t *testing.T, records ...shared.RequestRecord) []string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "history.db")
	underlying, err := OpenDB(file)
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	recording := &runRecordingDB{DB: underlying}
	h, err := OpenHistory(file, recording)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := h.UpsertNode(nodeUpsert()); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	recording.statements = nil
	var upserts []string
	for _, record := range records {
		recording.statements = nil
		if err := h.PutRequest("s1/main", record); err != nil {
			t.Fatalf("put the request: %v", err)
		}
		for _, statement := range recording.statements {
			if strings.HasPrefix(statement, "INSERT INTO requests") {
				upserts = append(upserts, statement)
			}
		}
	}
	return upserts
}

// A statement is parsed on every Run, so the keeping CASE list costs a write that brings usage
// for nothing: only a write with no usage runs it.
func TestAWriteWithUsageRunsTheShortUpsert(t *testing.T) {
	routeOnly := requestRecord()
	routeOnly.Tokens = shared.Tokens{}

	upserts := upsertStatements(t, pricedRequest(), pricedRequest(), routeOnly)

	if len(upserts) != 3 {
		t.Fatalf("the puts ran %d requests upserts, want 3", len(upserts))
	}
	for i, wantKeeping := range []bool{false, false, true} {
		if got := strings.Contains(upserts[i], "CASE WHEN"); got != wantKeeping {
			t.Errorf("put %d: the upsert keeps what the row holds = %v, want %v", i+1, got, wantKeeping)
		}
	}
}
