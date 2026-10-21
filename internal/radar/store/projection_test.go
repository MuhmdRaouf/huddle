package store

import (
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
