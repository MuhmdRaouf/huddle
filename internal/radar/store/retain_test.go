package store

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The per-session record indexes: every record the store holds is filed under its
// session, so dropping a session touches its own records and no others.

// assertIndexed rebuilds the id-per-session grouping from the store's own records and
// requires the store's two indexes to match it exactly: the same sessions, the same ids,
// and no empty files.
func assertIndexed(t *testing.T, s *Store) {
	t.Helper()
	wantRequests := map[string]map[string]struct{}{}
	for id, held := range s.requests {
		ids, ok := wantRequests[held.record.SessionID]
		if !ok {
			ids = map[string]struct{}{}
			wantRequests[held.record.SessionID] = ids
		}
		ids[id] = struct{}{}
	}
	if !reflect.DeepEqual(wantRequests, s.requestsBySession) {
		t.Errorf("requestsBySession is %s, want %s", groupingText(s.requestsBySession), groupingText(wantRequests))
	}
	wantTools := map[string]map[string]struct{}{}
	for id, held := range s.tools {
		ids, ok := wantTools[held.record.SessionID]
		if !ok {
			ids = map[string]struct{}{}
			wantTools[held.record.SessionID] = ids
		}
		ids[id] = struct{}{}
	}
	if !reflect.DeepEqual(wantTools, s.toolsBySession) {
		t.Errorf("toolsBySession is %s, want %s", groupingText(s.toolsBySession), groupingText(wantTools))
	}
}

// groupingText renders a grouping with its sessions and ids sorted, so a failure names
// the difference.
func groupingText(grouping map[string]map[string]struct{}) string {
	sessions := make([]string, 0, len(grouping))
	for session := range grouping {
		sessions = append(sessions, session)
	}
	slices.Sort(sessions)
	var out strings.Builder
	for _, session := range sessions {
		ids := make([]string, 0, len(grouping[session]))
		for id := range grouping[session] {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		fmt.Fprintf(&out, "%s:[%s] ", session, strings.Join(ids, ","))
	}
	return out.String()
}

func TestSessionIndexWrites(t *testing.T) {
	t.Run("a plain request and tool call land filed under their session", func(t *testing.T) {
		s := newTestStore()
		for i, session := range []string{"s1", "s2", "s3"} {
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("req-%s", session)
				r.SessionID = session
				r.Ts = ts0 + int64(i)
			}), nil)
			assertIndexed(t, s)
			s.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) {
				tool.ID = fmt.Sprintf("tool-%s", session)
				tool.SessionID = session
				tool.StartedAt = ts0 + int64(i)
			}))
			assertIndexed(t, s)
		}
	})

	t.Run("a tool call's duration update stays filed under its session", func(t *testing.T) {
		s := newTestStore()
		s.AddToolCall(makeTool(nil))
		s.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.DurationMs = i64(250) }))
		if got := len(s.tools); got != 1 {
			t.Fatalf("tools: got %d, want the one updated call", got)
		}
		assertIndexed(t, s)
	})

	t.Run("a route line and its transcript copy pair into one record", func(t *testing.T) {
		s := newTestStore()
		s.AddRequest(route(func(r *shared.RequestRecord) {
			r.AgentID = "a31cd3de580148dc3"
			r.Status = str("200")
			r.StopReason = nil
		}), nil)
		s.AddRequest(transcriptCopy(nil), nil)
		rows := s.Requests(RequestFilter{})
		if len(rows) != 1 {
			t.Fatalf("requests: got %d, want the one paired call", len(rows))
		}
		assertIndexed(t, s)
	})

	t.Run("a merge whose session differs moves the record to the copy's session", func(t *testing.T) {
		s := newTestStore()
		s.AddRequest(route(func(r *shared.RequestRecord) {
			r.SessionID = "route-a"
			r.RequestID = "req_shared_call"
			r.AgentID = ""
		}), nil)
		s.AddRequest(transcriptCopy(func(r *shared.RequestRecord) {
			r.SessionID = "copy-b"
			r.RequestID = "req_shared_call"
		}), nil)
		rows := s.Requests(RequestFilter{})
		if len(rows) != 1 {
			t.Fatalf("requests: got %d, want the one merged call", len(rows))
		}
		if rows[0].SessionID != "copy-b" {
			t.Fatalf("merged session: got %q, want the copy's %q", rows[0].SessionID, "copy-b")
		}
		assertIndexed(t, s)
	})

	t.Run("a record eviction pass unfiles every record it sheds", func(t *testing.T) {
		s := newTestStore()
		s.maxRecords, s.evictBatch = 100, 10
		for i := 0; i < 150; i++ {
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("bulk-%03d", i)
				r.SessionID = fmt.Sprintf("s%d", i%3)
				r.Ts = ts0 + int64(i)
			}), nil)
		}
		if got := len(s.requests); got != 100 {
			t.Fatalf("requests kept: got %d, want %d", got, 100)
		}
		assertIndexed(t, s)
	})
}

func TestDropIsExact(t *testing.T) {
	s := newTestStore()
	for i := 0; i <= shared.MaxSessions; i++ {
		session := fmt.Sprintf("s%d", i)
		for j := 0; j < 2; j++ {
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("%s-req-%d", session, j)
				r.SessionID = session
				r.Ts = ts0 + int64(i)*1_000 + int64(j)
			}), nil)
		}
		s.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) {
			tool.ID = session + "-tool"
			tool.SessionID = session
			tool.StartedAt = ts0 + int64(i)*1_000 + 2
		}))
	}
	// the upsert on the newest session is what fires the pass, as a live feed's own line would
	s.UpsertSession(SessionUpsert{ID: fmt.Sprintf("s%d", shared.MaxSessions)})
	if got := s.SessionCount(); got != shared.MaxSessions {
		t.Fatalf("sessions: got %d, want %d", got, shared.MaxSessions)
	}
	if findItem(s.SessionList(), "s0") != nil {
		t.Error("the stalest session survived")
	}
	requests := s.Raw().Requests
	if len(requests) != 2*int(shared.MaxSessions) {
		t.Fatalf("requests: got %d, want %d", len(requests), 2*int(shared.MaxSessions))
	}
	for _, record := range requests {
		if record.SessionID == "s0" {
			t.Errorf("request %s outlived its dropped session", record.ID)
		}
	}
	tools := s.Raw().Tools
	if len(tools) != int(shared.MaxSessions) {
		t.Fatalf("tools: got %d, want %d", len(tools), int(shared.MaxSessions))
	}
	for _, tool := range tools {
		if tool.SessionID == "s0" {
			t.Errorf("tool %s outlived its dropped session", tool.ID)
		}
	}
	assertIndexed(t, s)
}

func TestOldSessionLines(t *testing.T) {
	s := newTestStore()
	for i := 0; i < shared.MaxSessions; i++ {
		s.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts0 + int64(i))
			l.SessionID = str(fmt.Sprintf("s%d", i))
		}))
	}
	stale := isoTime(ts0 - dayMs)
	for i := 0; i < 50; i++ {
		s.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = stale
			l.SessionID = str("old")
			l.Prompt = str(fmt.Sprintf("old prompt %d", i))
		}))
		if got := s.SessionCount(); got != shared.MaxSessions {
			t.Fatalf("after old line %d: sessions: got %d, want %d", i+1, got, shared.MaxSessions)
		}
	}
	assertIndexed(t, s)
}
