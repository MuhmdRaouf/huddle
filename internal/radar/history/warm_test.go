package history

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

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
	err := h.WarmRequests(5, func(r shared.RequestRecord, _ int64) error {
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
	if err := h.WarmRequests(10, func(r shared.RequestRecord, at int64) error {
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
	if err := h.WarmRequests(rows, func(r shared.RequestRecord, _ int64) error {
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
	err := h.WarmRequests(7, func(shared.RequestRecord, int64) error {
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
	if err := h.WarmTools(func(nodeID string, tool shared.ToolCallRecord) error {
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
	if err := h.WarmEvents(4, func(e shared.EventRecord) error {
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
	if err := h.WarmEvents(10, func(shared.EventRecord) error {
		t.Error("an empty file handed over an event")
		return nil
	}); err != nil {
		t.Fatalf("warm events: %v", err)
	}
}

func TestWarmNodesAnswerSessionsAndAgentsAsStoreInputs(t *testing.T) {
	h := warmFile(t)
	var nodes []store.WarmNode
	if err := h.WarmNodes(func(n store.WarmNode) error {
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
	if err := h.WarmNodes(func(store.WarmNode) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("a callback error: got %v, want it back", err)
	}
}
