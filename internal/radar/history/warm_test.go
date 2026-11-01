package history

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The warm reads: what an earlier run kept, in the orders and limits the store's
// WarmSource documents.

// warmFile writes two sessions, a subagent and a job, with requests, tools and events,
// through the History API.
func warmFile(t *testing.T) *History {
	t.Helper()
	h := openHistoryAt(t)
	nodes := []NodeUpsert{
		{
			ID: "s1/main", Kind: shared.NodeMain, SessionID: "s1", AgentID: "main", Cwd: sp("/work/app"),
			Name: sp("the app"), Branch: sp("main"), StartedAt: i64p(100), LastAt: i64p(900),
		},
		{
			ID: "s1/agent-a", Kind: shared.NodeSubagent, SessionID: "s1", AgentID: "agent-a",
			ParentID: sp("s1/main"), AgentType: sp("Explore"), Description: sp("find it"),
			Model: sp("claude-haiku-5-5"), LastAt: i64p(800),
		},
		{
			ID: "s2/main", Kind: shared.NodeMain, SessionID: "s2", AgentID: "main", StartedAt: i64p(50),
			LastAt: i64p(60), EndedAt: EndAt(70),
		},
		{
			ID: "job:zai:j1", Kind: shared.NodeJob, SessionID: "zai:j1", AgentID: "j1", ParentID: sp("s1/main"),
			ParentSessionID: sp("s1"), StartedAt: i64p(300), LastAt: i64p(400),
		},
	}
	for _, node := range nodes {
		if err := h.UpsertNode(node); err != nil {
			t.Fatalf("upsert %s: %v", node.ID, err)
		}
	}
	for i := 1; i <= 7; i++ {
		request := requestRecord()
		request.ID = fmt.Sprintf("r%d", i)
		request.Ts = int64(i) * 100
		if err := h.PutRequest("s1/main", request); err != nil {
			t.Fatalf("put %s: %v", request.ID, err)
		}
	}
	for i := 1; i <= 4; i++ {
		tool := shared.ToolCallRecord{
			ID: fmt.Sprintf("t%d", i), SessionID: "s1", AgentID: sp("main"), Name: "Bash", StartedAt: int64(i) * 100,
			DurationMs: i64p(5), Ok: true,
		}
		if err := h.PutTool("s1/main", tool); err != nil {
			t.Fatalf("put %s: %v", tool.ID, err)
		}
	}
	for seq := int64(1); seq <= 6; seq++ {
		event := shared.EventRecord{
			Seq: seq, Ts: seq * 100, Kind: "UserPromptSubmit", SessionID: sp("s1"),
			Payload: shared.SpoolLine{Ts: "2026-01-01T00:00:00.000Z", Event: "UserPromptSubmit", SessionID: sp("s1")},
		}
		if err := h.AddEvent(sp("s1/main"), event); err != nil {
			t.Fatalf("add event %d: %v", seq, err)
		}
	}
	return h
}

func TestWarmRequestsAnswerTheNewestFirstUnderTheLimit(t *testing.T) {
	h := warmFile(t)
	var ids []string
	err := h.WarmRequests(0, 5, func(r shared.RequestRecord, _ int64) error {
		ids = append(ids, r.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("warm requests: %v", err)
	}
	if want := []string{"r7", "r6", "r5", "r4", "r3"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("the requests: got %v, want %v", ids, want)
	}
}

func TestWarmRequestsReportTheOrderTheFileWroteThemIn(t *testing.T) {
	h := openHistoryAt(t)
	if err := h.UpsertNode(nodeUpsert()); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	// written newest ts first, then an older one, then the first again with new totals
	for _, row := range []struct {
		id string
		ts int64
	}{{"late", 900}, {"early", 100}, {"middle", 500}, {"late", 900}} {
		request := requestRecord()
		request.ID, request.Ts = row.id, row.ts
		if err := h.PutRequest("s1/main", request); err != nil {
			t.Fatalf("put %s: %v", row.id, err)
		}
	}
	written := map[string]int64{}
	if err := h.WarmRequests(0, 10, func(r shared.RequestRecord, at int64) error {
		written[r.ID] = at
		return nil
	}); err != nil {
		t.Fatalf("warm requests: %v", err)
	}
	if !(written["late"] < written["early"] && written["early"] < written["middle"]) {
		t.Errorf("the write positions: got %v, want late < early < middle (a rewrite keeps its place)", written)
	}
}

func TestWarmRequestsPageThroughMoreRowsThanOneBatch(t *testing.T) {
	h := openHistoryAt(t)
	if err := h.UpsertNode(nodeUpsert()); err != nil {
		t.Fatalf("upsert the node: %v", err)
	}
	const rows = 2_300
	for i := 1; i <= rows; i++ {
		request := requestRecord()
		request.ID = fmt.Sprintf("r%05d", i)
		request.Ts = int64(i / 3) // three rows share each ts: the id breaks the tie
		if err := h.PutRequest("s1/main", request); err != nil {
			t.Fatalf("put %s: %v", request.ID, err)
		}
	}
	var got []string
	if err := h.WarmRequests(0, rows, func(r shared.RequestRecord, _ int64) error {
		got = append(got, r.ID)
		return nil
	}); err != nil {
		t.Fatalf("warm requests: %v", err)
	}
	if len(got) != rows {
		t.Fatalf("the requests: got %d, want %d", len(got), rows)
	}
	for i, id := range got {
		if want := fmt.Sprintf("r%05d", rows-i); id != want {
			t.Fatalf("request %d: got %s, want %s (ts then id, newest first, none repeated or skipped)", i, id, want)
		}
	}
}

func TestWarmRequestsStopOnTheCallbacksError(t *testing.T) {
	h := warmFile(t)
	stop := errors.New("enough")
	var seen int
	err := h.WarmRequests(0, 7, func(shared.RequestRecord, int64) error {
		seen++
		return stop
	})
	if !errors.Is(err, stop) || seen != 1 {
		t.Errorf("a callback error: got %v after %d requests, want it back after 1", err, seen)
	}
}

func TestWarmToolsAnswerTheNewestFirst(t *testing.T) {
	h := warmFile(t)
	var ids, nodes []string
	if err := h.WarmTools(0, func(nodeID string, tool shared.ToolCallRecord) error {
		ids, nodes = append(ids, tool.ID), append(nodes, nodeID)
		return nil
	}); err != nil {
		t.Fatalf("warm tools: %v", err)
	}
	if want := []string{"t4", "t3", "t2", "t1"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("the tools: got %v, want %v", ids, want)
	}
	if nodes[0] != "s1/main" {
		t.Errorf("the tool's node: got %s, want s1/main", nodes[0])
	}
}

func TestWarmEventsAnswerTheNewestLimitOldestFirst(t *testing.T) {
	h := warmFile(t)
	var seqs []int64
	var payloads []any
	if err := h.WarmEvents(0, 4, func(e shared.EventRecord) error {
		seqs, payloads = append(seqs, e.Seq), append(payloads, e.Payload)
		return nil
	}); err != nil {
		t.Fatalf("warm events: %v", err)
	}
	if want := []int64{3, 4, 5, 6}; !reflect.DeepEqual(seqs, want) {
		t.Errorf("the events: got seqs %v, want %v", seqs, want)
	}
	line, ok := payloads[0].(shared.SpoolLine)
	if !ok || line.Event != "UserPromptSubmit" || line.SessionID == nil || *line.SessionID != "s1" {
		t.Errorf("the payload: got %#v, want the spool line the event carried", payloads[0])
	}
	encoded, err := line.MarshalJSON()
	if err != nil || len(encoded) == 0 {
		t.Errorf("the payload marshals as its own bytes: got %q, %v", encoded, err)
	}
}

func TestWarmEventsWithNoEventsCallNothing(t *testing.T) {
	h := openHistoryAt(t)
	if err := h.WarmEvents(0, 10, func(shared.EventRecord) error {
		t.Error("an empty file handed over an event")
		return nil
	}); err != nil {
		t.Fatalf("warm events: %v", err)
	}
}

func TestWarmNodesAnswerSessionsAndAgentsAsStoreInputs(t *testing.T) {
	h := warmFile(t)
	var nodes []store.WarmNode
	if err := h.WarmNodes(0, func(n store.WarmNode) error {
		nodes = append(nodes, n)
		return nil
	}); err != nil {
		t.Fatalf("warm nodes: %v", err)
	}
	byAgent := map[string]store.WarmNode{}
	for _, node := range nodes {
		byAgent[node.Agent.SessionID+"/"+node.Agent.ID] = node
	}
	if len(nodes) != 4 {
		t.Fatalf("the nodes: got %d, want 4", len(nodes))
	}
	for i, node := range nodes[:3] {
		if node.Session == nil {
			t.Errorf("node %d (%s/%s): got no session, want the session nodes before the subagents", i,
				node.Agent.SessionID, node.Agent.ID)
		}
	}
	if last := nodes[3]; last.Session != nil || last.Agent.ID != "agent-a" {
		t.Errorf("the last node: got %+v, want the subagent after every session", last)
	}

	main := byAgent["s1/main"]
	if main.Session == nil || main.Session.ID != "s1" || main.Session.Cwd == nil || *main.Session.Cwd != "/work/app" ||
		main.Session.StartedAt == nil || *main.Session.StartedAt != 100 || main.Session.Branch == nil ||
		*main.Session.Branch != "main" || main.Session.AgentName == nil || *main.Session.AgentName != "the app" {
		t.Errorf("s1's session: got %+v", main.Session)
	}
	if main.Agent.Kind == nil || *main.Agent.Kind != shared.KindMain || main.LastAt == nil || *main.LastAt != 900 {
		t.Errorf("s1's lead agent: got %+v lastAt %v, want main at 900", main.Agent, main.LastAt)
	}
	if ended := byAgent["s2/main"]; ended.EndedAt == nil || *ended.EndedAt != 70 {
		t.Errorf("s2's end: got %v, want 70", ended.EndedAt)
	}

	sub := byAgent["s1/agent-a"]
	if sub.Agent.Kind == nil || *sub.Agent.Kind != shared.KindSubagent || sub.Agent.AgentType == nil ||
		*sub.Agent.AgentType != "Explore" || sub.Agent.Description == nil || *sub.Agent.Description != "find it" ||
		sub.Agent.Model == nil || *sub.Agent.Model != "claude-haiku-5-5" {
		t.Errorf("the subagent: got %+v", sub.Agent)
	}
	if sub.Agent.ParentID == nil || *sub.Agent.ParentID == nil || **sub.Agent.ParentID != "main" {
		t.Errorf("the subagent's parent: got %v, want the agent id main", sub.Agent.ParentID)
	}

	job := byAgent["zai:j1/j1"]
	if job.Session == nil || job.Session.External == nil || !*job.Session.External ||
		job.Session.ParentSessionID == nil || *job.Session.ParentSessionID != "s1" {
		t.Errorf("the job's session: got %+v, want an external one under s1", job.Session)
	}
	if job.Agent.Kind == nil || *job.Agent.Kind != shared.KindExternal {
		t.Errorf("the job's agent: got %+v, want the external kind", job.Agent)
	}
}

func TestWarmNodesStopOnTheCallbacksError(t *testing.T) {
	h := warmFile(t)
	stop := errors.New("enough")
	if err := h.WarmNodes(0, func(store.WarmNode) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("a callback error: got %v, want it back", err)
	}
}

// windowFile is a history of three days that ends at now: a session with requests, tools
// and events one hour, 23 hours, 25 hours and 50 hours back, and a session that went quiet
// two days back. The events carry seqs 1 to 4, oldest first.
func windowFile(t *testing.T, now time.Time) *History {
	t.Helper()
	h := openHistoryAt(t)
	ago := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	for _, node := range []NodeUpsert{
		{ID: "live/main", Kind: shared.NodeMain, SessionID: "live", AgentID: "main", StartedAt: i64p(ago(50 * time.Hour)), LastAt: i64p(ago(time.Hour))},
		{ID: "quiet/main", Kind: shared.NodeMain, SessionID: "quiet", AgentID: "main", StartedAt: i64p(ago(52 * time.Hour)), LastAt: i64p(ago(50 * time.Hour))},
	} {
		if err := h.UpsertNode(node); err != nil {
			t.Fatalf("upsert %s: %v", node.ID, err)
		}
	}
	for i, back := range []time.Duration{50 * time.Hour, 25 * time.Hour, 23 * time.Hour, time.Hour} {
		request := requestRecord()
		request.ID, request.SessionID, request.Ts = fmt.Sprintf("r%d", i+1), "live", ago(back)
		if err := h.PutRequest("live/main", request); err != nil {
			t.Fatalf("put %s: %v", request.ID, err)
		}
		tool := shared.ToolCallRecord{
			ID: fmt.Sprintf("t%d", i+1), SessionID: "live", AgentID: sp("main"), Name: "Bash", StartedAt: ago(back),
			DurationMs: i64p(5), Ok: true,
		}
		if err := h.PutTool("live/main", tool); err != nil {
			t.Fatalf("put %s: %v", tool.ID, err)
		}
		event := shared.EventRecord{
			Seq: int64(i + 1), Ts: ago(back), Kind: "UserPromptSubmit", SessionID: sp("live"),
			Payload: shared.SpoolLine{Ts: "2026-01-01T00:00:00.000Z", Event: "UserPromptSubmit", SessionID: sp("live")},
		}
		if err := h.AddEvent(sp("live/main"), event); err != nil {
			t.Fatalf("add event %d: %v", event.Seq, err)
		}
	}
	quiet := requestRecord()
	quiet.ID, quiet.SessionID, quiet.Ts = "rq", "quiet", ago(50*time.Hour)
	if err := h.PutRequest("quiet/main", quiet); err != nil {
		t.Fatalf("put %s: %v", quiet.ID, err)
	}
	return h
}

func TestAWarmStartLoadsOnlyTheStoreWindow(t *testing.T) {
	now := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	h := windowFile(t, now)
	st := store.CreateStore(func() time.Time { return now })

	stats, err := st.Warm(t.Context(), h)
	if err != nil {
		t.Fatalf("warm: %v", err)
	}
	if stats.Requests != 2 || stats.Tools != 2 || stats.Events != 2 || stats.Sessions != 1 {
		t.Errorf("the warm stats: got %+v, want 2 requests, 2 tools, 2 events and 1 session", stats)
	}
	cut := now.Add(-shared.StoreWindow).UnixMilli()
	for _, request := range st.RequestsFor(store.RequestFilter{}) {
		if request.Ts < cut {
			t.Errorf("request %s at %d is older than the window start %d", request.ID, request.Ts, cut)
		}
	}
	for _, tool := range st.Tools(store.ToolsFilter{}) {
		if tool.StartedAt < cut {
			t.Errorf("tool %s at %d is older than the window start %d", tool.ID, tool.StartedAt, cut)
		}
	}
	for _, event := range st.Events(store.EventsFilter{}) {
		if event.Ts < cut {
			t.Errorf("event %d at %d is older than the window start %d", event.Seq, event.Ts, cut)
		}
	}
	list := st.SessionList()
	if len(list) != 1 || list[0].ID != "live" || list[0].RequestCount != 2 {
		t.Errorf("the sessions: got %+v, want only live with 2 requests", list)
	}

	st.AddSpoolLine(shared.SpoolLine{Ts: now.Format("2006-01-02T15:04:05.000Z07:00"), Event: "Stop", SessionID: sp("live")})
	if next := st.Events(store.EventsFilter{Limit: i64p(1)}); len(next) != 1 || next[0].Seq != 5 {
		t.Errorf("the next live event: got %+v, want seq 5 after the file's last seq 4", next)
	}
}

func TestAWarmStartNumbersOnFromTheFilesLastSeqWhenTheWindowHoldsNoEvent(t *testing.T) {
	now := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	h := windowFile(t, now)
	if _, err := h.db.Run("DELETE FROM events WHERE seq > 2"); err != nil {
		t.Fatalf("drop the newer events: %v", err)
	}
	st := store.CreateStore(func() time.Time { return now })

	if _, err := st.Warm(t.Context(), h); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if held := st.Events(store.EventsFilter{}); len(held) != 0 {
		t.Errorf("the events held: got %+v, want none, both are older than the window", held)
	}
	st.AddSpoolLine(shared.SpoolLine{Ts: now.Format("2006-01-02T15:04:05.000Z07:00"), Event: "Stop", SessionID: sp("live")})
	if next := st.Events(store.EventsFilter{}); len(next) != 1 || next[0].Seq != 3 {
		t.Errorf("the next live event: got %+v, want seq 3 after the file's last seq 2", next)
	}
}

func TestWarmNodesKeepOnlySessionsActiveInTheWindow(t *testing.T) {
	h := openHistoryAt(t)
	for _, node := range []NodeUpsert{
		{ID: "recent/main", Kind: shared.NodeMain, SessionID: "recent", AgentID: "main", LastAt: i64p(900)},
		{ID: "old/main", Kind: shared.NodeMain, SessionID: "old", AgentID: "main", LastAt: i64p(100)},
		{ID: "named/main", Kind: shared.NodeMain, SessionID: "named", AgentID: "main"},
		{ID: "unnamed/main", Kind: shared.NodeMain, SessionID: "unnamed", AgentID: "main"},
	} {
		if err := h.UpsertNode(node); err != nil {
			t.Fatalf("upsert %s: %v", node.ID, err)
		}
	}
	request := requestRecord()
	request.ID, request.SessionID, request.Ts = "r1", "named", 800
	if err := h.PutRequest("named/main", request); err != nil {
		t.Fatalf("put the request: %v", err)
	}
	var sessions []string
	if err := h.WarmNodes(500, func(n store.WarmNode) error {
		sessions = append(sessions, n.Agent.SessionID)
		return nil
	}); err != nil {
		t.Fatalf("warm nodes: %v", err)
	}
	slices.Sort(sessions)
	if want := []string{"named", "recent"}; !reflect.DeepEqual(sessions, want) {
		t.Errorf("the sessions: got %v, want %v", sessions, want)
	}
}

func TestWarmEndAnswersTheNewestEventWhateverItsAge(t *testing.T) {
	h := warmFile(t)
	if end, err := h.WarmEnd(); err != nil || end != (store.WarmEnd{Seq: 6, Ts: 600}) {
		t.Errorf("the end: got %+v (%v), want seq 6 at 600", end, err)
	}
	if end, err := openHistoryAt(t).WarmEnd(); err != nil || end != (store.WarmEnd{}) {
		t.Errorf("the end of an empty file: got %+v (%v), want zeros", end, err)
	}
}
