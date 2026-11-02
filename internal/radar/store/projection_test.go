package store

import (
	"slices"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The projection points at the stored records instead of copying them: one record, one
// address, read through the session slice, the agent slice and the store's own map.

// requestAt is the slice's held record named by id, or nil when it holds none.
func requestAt(records []*shared.RequestRecord, id string) *shared.RequestRecord {
	for _, held := range records {
		if held.ID == id {
			return held
		}
	}
	return nil
}

func TestProjectionPointsAtStoredRecords(t *testing.T) {
	s := newTestStore()
	s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID = "req-1"
		r.SessionID = "s1"
		r.AgentID = "main"
	}), nil)
	s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID = "req-2"
		r.SessionID = "s1"
		r.AgentID = "sub-1"
	}), nil)
	s.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) {
		tool.ID = "tool-1"
		tool.SessionID = "s1"
		tool.AgentID = str("sub-1")
	}))

	s.mu.Lock()
	proj := s.projectionLocked()
	s.mu.Unlock()
	slice := proj["s1"]
	if slice == nil {
		t.Fatal("the projection holds no slice for s1")
	}

	inSession := requestAt(slice.requests, "req-2")
	inAgent := requestAt(slice.byAgent["sub-1"].requests, "req-2")
	if inSession == nil || inAgent == nil {
		t.Fatalf("req-2 missing from the projection: session %v, agent %v", inSession, inAgent)
	}
	if inSession != inAgent {
		t.Fatal("the session slice and the agent slice hold different copies of req-2")
	}
	held := s.requests["req-2"]
	if inSession != &held.record {
		t.Fatal("the projection's req-2 is a copy, not the stored record")
	}
	mainHeld := s.requests["req-1"]
	if got := requestAt(slice.requests, "req-1"); got != &mainHeld.record {
		t.Fatal("the projection's req-1 is a copy, not the stored record")
	}
	toolHeld := s.tools["tool-1"]
	if len(slice.tools) != 1 || slice.tools[0] != &toolHeld.record {
		t.Fatal("the session slice's tool is a copy, not the stored record")
	}
	if len(slice.byAgent["sub-1"].tools) != 1 || slice.byAgent["sub-1"].tools[0] != &toolHeld.record {
		t.Fatal("the agent slice's tool is a copy, not the stored record")
	}

	s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID = "req-1"
		r.SessionID = "s1"
		r.AgentID = "main"
		r.Tokens = shared.Tokens{Input: 999, Output: 5}
	}), nil)
	s.mu.Lock()
	proj = s.projectionLocked()
	s.mu.Unlock()
	refreshed := requestAt(proj["s1"].requests, "req-1")
	if refreshed == nil {
		t.Fatal("the rebuilt projection lost req-1")
	}
	if refreshed.Tokens.Input != 999 {
		t.Fatalf("the refreshed record reads %d input tokens through the projection, want 999", refreshed.Tokens.Input)
	}
}

// EachSession hands each session its own requests, tool calls and events in arrival order,
// skips a session that holds none, and follows the store as it changes.
func TestEachSessionHandsOutEachSessionsRecords(t *testing.T) {
	s := newTestStore()
	s.UpsertSession(SessionUpsert{ID: "s1"})
	s.UpsertSession(SessionUpsert{ID: "s2"})
	s.UpsertSession(SessionUpsert{ID: "empty"})
	for _, id := range []string{"a", "b"} {
		s.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.SessionID = id, "s1" }), nil)
	}
	s.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.SessionID = "c", "s2" }), nil)
	s.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.ID, tool.SessionID = "t1", "s2" }))
	session := "s2"
	s.AddSpoolLine(shared.SpoolLine{Ts: "2026-10-15T12:00:00Z", Event: "UserPromptSubmit", SessionID: &session})

	seen := map[string]string{}
	var order []string
	collect := func() {
		seen, order = map[string]string{}, nil
		s.EachSession(func(id string, records SessionRecords) {
			order = append(order, id)
			seen[id] = ""
			for _, request := range records.Requests {
				seen[id] += request.ID
			}
			for _, tool := range records.Tools {
				seen[id] += "/" + tool.ID
			}
			for _, event := range records.Events {
				seen[id] += "/" + event.Kind
			}
		})
	}
	collect()
	if want := []string{"s1", "s2"}; !slices.Equal(order, want) {
		t.Errorf("sessions visited = %v, want %v (the empty one is skipped)", order, want)
	}
	if seen["s1"] != "ab" || seen["s2"] != "c/t1/UserPromptSubmit" {
		t.Errorf("records = %v, want s1 ab and s2 c/t1/UserPromptSubmit", seen)
	}

	s.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.SessionID = "d", "s1" }), nil)
	s.AddSpoolLine(shared.SpoolLine{Ts: "2026-10-15T12:01:00Z", Event: "Stop", SessionID: &session})
	collect()
	if seen["s1"] != "abd" || seen["s2"] != "c/t1/UserPromptSubmit/Stop" {
		t.Errorf("after more records = %v, want s1 abd and s2 c/t1/UserPromptSubmit/Stop", seen)
	}
}
