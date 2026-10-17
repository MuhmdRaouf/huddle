package store

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
)

var (
	ts0   = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC).UnixMilli()
	ts1   = ts0 + 1_000
	ts2   = ts0 + 2_000
	ts3   = ts0 + 3_000
	ts9   = ts0 + 9_000
	dayMs = int64(86_400_000)
)

// isoTime renders a moment the way the hook stamps its lines.
func isoTime(ms int64) string { return jsfmt.ISOTime(time.UnixMilli(ms)) }

// newTestStore is a store whose clock stands still, so every read names its own moment.
func newTestStore() *Store {
	return CreateStore(func() time.Time { return time.UnixMilli(ts9) })
}

// line is the suite's base spool line, each case reshaping through shape.
func line(shape func(*shared.SpoolLine)) shared.SpoolLine {
	l := shared.SpoolLine{
		Ts:        isoTime(ts1),
		Event:     "UserPromptSubmit",
		SessionID: str("s1"),
	}
	if shape != nil {
		shape(&l)
	}
	return l
}

// messageLine carries its text in the unknown-field bag, where a Notification line carries it.
func messageLine(text string) shared.SpoolLine {
	return shared.SpoolLine{
		Ts:        isoTime(ts1),
		Event:     "Notification",
		SessionID: str("s1"),
		Extra:     map[string]json.RawMessage{"message": []byte(strconv.Quote(text))},
	}
}

func boolPtr(b bool) *bool                         { return &b }
func kindPtr(k shared.AgentKind) *shared.AgentKind { return &k }

// strPtrPtr lifts an optional value one pointer up, for the upsert fields that tell an
// explicit null from an absent one.
func strPtrPtr(value *string) **string { return &value }

func eqStr(got *string, want string) bool { return got != nil && *got == want }
func eqInt(got *int64, want int64) bool   { return got != nil && *got == want }

func requestIDs(records []shared.RequestRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids
}

func toolIDs(tools []shared.ToolCallRecord) []string {
	ids := make([]string, 0, len(tools))
	for _, tool := range tools {
		ids = append(ids, tool.ID)
	}
	return ids
}

func itemIDs(items []SessionListItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func treeIDs(nodes []SessionTreeItem) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func eventLabels(events []shared.EventRecord) []string {
	labels := make([]string, 0, len(events))
	for _, event := range events {
		if event.Label == nil {
			labels = append(labels, "<null>")
		} else {
			labels = append(labels, *event.Label)
		}
	}
	return labels
}

func eventTimes(events []shared.EventRecord) []int64 {
	times := make([]int64, 0, len(events))
	for _, event := range events {
		times = append(times, event.Ts)
	}
	return times
}

func findItem(items []SessionListItem, id string) *SessionListItem {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

func findAgent(view *shared.SessionView, id string) *shared.AgentView {
	if view == nil {
		return nil
	}
	for i := range view.Agents {
		if view.Agents[i].ID == id {
			return &view.Agents[i]
		}
	}
	return nil
}

// detailOf is sessionDetail with a failure instead of a nil, so a missing session reads
// as the test's own message.
func detailOf(t *testing.T, store *Store, id string) *shared.SessionView {
	t.Helper()
	detail := store.SessionDetail(id)
	if detail == nil {
		t.Fatalf("sessionDetail(%s): got nil, want the session", id)
	}
	return detail
}

func TestAddSpoolLine(t *testing.T) {
	t.Run("creates sessions, keeps first-writer meta and orders the list by recency", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts0)
			l.Cwd = str("/w/app")
			l.BaseURL = str("http://127.0.0.1:8787")
			l.CcVersion = str("2.0.0")
		}))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts1)
			l.ModelEnv = map[string]string{"ANTHROPIC_MODEL": "glm-5.3"}
		}))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Cwd = str("/later") })) // meta only fills holes
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts2)
			l.SessionID = str("s2")
			l.Cwd = str("/w/other")
		}))
		detail := detailOf(t, store, "s1")
		if detail == nil {
			t.Fatal("sessionDetail(s1): got nil, want the session")
		}
		if detail.ID != "s1" || !eqStr(detail.Cwd, "/w/app") || !eqStr(detail.Project, "app") ||
			detail.Upstream != "http://127.0.0.1:8787" || !eqStr(detail.CcVersion, "2.0.0") ||
			!eqStr(detail.Model, "glm-5.3") || !eqInt(detail.StartedAt, ts0) || detail.Live || detail.External {
			t.Errorf("sessionDetail(s1): got %+v", detail)
		}
		list := store.SessionList()
		if want := []string{"s2", "s1"}; !slices.Equal(itemIDs(list), want) {
			t.Errorf("sessionList ids: got %v, want %v", itemIDs(list), want)
		}
		if len(list) == 0 {
			t.Fatal("sessionList: got no items, want s2 and s1")
		}
		first := list[0]
		if first.ID != "s2" || !eqStr(first.Project, "other") || first.LastAt != ts2 || first.Live {
			t.Errorf("list[0]: got %+v, want s2 / other / lastAt %d / not live", first, ts2)
		}
	})

	t.Run("records sessionless lines as events without inventing a session", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(shared.SpoolLine{Ts: isoTime(ts1), Event: "Notification"})
		if list := store.SessionList(); len(list) != 0 {
			t.Errorf("sessionList: got %d items, want none", len(list))
		}
		events := store.Events(EventsFilter{})
		if len(events) != 1 {
			t.Fatalf("events: got %d, want 1", len(events))
		}
		first := events[0]
		if first.Kind != "Notification" || first.SessionID != nil || first.AgentID != nil || first.Label != nil {
			t.Errorf("event: got %+v, want a sessionless Notification with no label", first)
		}
	})

	t.Run("labels every line from the first field it recognises", func(t *testing.T) {
		store := newTestStore()
		cases := []struct {
			line shared.SpoolLine
			want *string
		}{
			{line(func(l *shared.SpoolLine) { l.Prompt = str(strings.Repeat("x", 200)) }), str(strings.Repeat("x", 120))},
			{
				shared.SpoolLine{
					Ts: isoTime(ts1), Event: "route", SessionID: str("s1"),
					Model: str("glm-5.3"), Upstream: str("http://127.0.0.1:8787"),
				},
				str("glm-5.3 → http://127.0.0.1:8787"),
			},
			{line(func(l *shared.SpoolLine) {
				l.Event = "PreToolUse"
				l.ToolName = str("Read")
				l.ToolInput = map[string]any{"file_path": "/a/b.ts"}
			}), str("Read /a/b.ts")},
			{line(func(l *shared.SpoolLine) {
				l.Event = "PreToolUse"
				l.ToolName = str("Edit")
				l.ToolInput = map[string]any{"file_path": strings.Repeat("f", 100)}
			}), str("Edit " + strings.Repeat("f", 90))},
			{line(func(l *shared.SpoolLine) {
				l.Event = "PreToolUse"
				l.ToolName = str("Bash")
				l.ToolInput = "raw"
			}), str("Bash")},
			{line(func(l *shared.SpoolLine) {
				l.Event = "PreToolUse"
				l.ToolName = str("Edit")
				l.ToolInput = map[string]any{"file_path": ""}
			}), str("Edit")},
			{line(func(l *shared.SpoolLine) { l.Event = "SubagentStart"; l.AgentType = str("researcher") }), str("researcher")},
			{line(func(l *shared.SpoolLine) { l.Event = "Stop"; l.Reason = str("end_turn") }), str("end_turn")},
			{line(func(l *shared.SpoolLine) { l.Event = "SessionStart"; l.Source = str("startup") }), str("startup")},
			{messageLine(strings.Repeat("m", 200)), str(strings.Repeat("m", 120))},
			{
				shared.SpoolLine{
					Ts: isoTime(ts1), Event: "Notice", SessionID: str("s1"),
					Message: str("Claude Code notice: error"),
				},
				str("Claude Code notice: error"),
			},
			{line(func(l *shared.SpoolLine) { l.Event = "PreCompact"; l.Trigger = str("auto") }), str("auto")},
			{line(func(l *shared.SpoolLine) { l.Event = "Notification" }), nil},
		}
		for _, c := range cases {
			store.AddSpoolLine(c.line)
			events := store.Events(EventsFilter{Limit: i64(1)})
			if len(events) != 1 {
				t.Fatalf("events after %s: got %d, want 1", c.line.Event, len(events))
			}
			if !sameOptStr(events[0].Label, c.want) {
				t.Errorf("label of %s: got %v, want %v", c.line.Event, events[0].Label, c.want)
			}
		}
	})

	t.Run("filters events newest-first by session, since and limit", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Prompt = str("a") }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Prompt = str("b") }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts2)
			l.SessionID = str("s2")
			l.Prompt = str("c")
		}))
		newest := store.Events(EventsFilter{})
		if want := []string{"c", "b", "a"}; !slices.Equal(eventLabels(newest), want) {
			t.Errorf("labels: got %v, want %v", eventLabels(newest), want)
		}
		if len(newest) == 0 {
			t.Fatal("events: got none, want three")
		}
		first := newest[0]
		if first.Kind != "UserPromptSubmit" || !eqStr(first.SessionID, "s2") || first.AgentID != nil ||
			first.Ts != ts2 || first.Seq != 3 {
			t.Errorf("newest[0]: got %+v, want the s2 prompt at seq 3", first)
		}
		payload, ok := first.Payload.(shared.SpoolLine)
		if !ok || !eqStr(payload.Prompt, "c") {
			t.Errorf("payload: got %#v, want prompt c", first.Payload)
		}
		if want := []int64{ts1, ts0}; !slices.Equal(eventTimes(store.Events(EventsFilter{Session: str("s1")})), want) {
			t.Errorf("by session: got %v, want %v", eventTimes(store.Events(EventsFilter{Session: str("s1")})), want)
		}
		if want := []int64{ts2, ts1}; !slices.Equal(eventTimes(store.Events(EventsFilter{Since: i64(ts1)})), want) {
			t.Errorf("since ts1: got %v, want %v", eventTimes(store.Events(EventsFilter{Since: i64(ts1)})), want)
		}
		if want := []int64{ts2, ts1}; !slices.Equal(eventTimes(store.Events(EventsFilter{Limit: i64(2)})), want) {
			t.Errorf("limit 2: got %v, want %v", eventTimes(store.Events(EventsFilter{Limit: i64(2)})), want)
		}
	})
	t.Run("marshals an event's payload back to the line's own bytes, unknown keys in place", func(t *testing.T) {
		raw := `{"ts":"` + isoTime(ts1) + `","event":"PreToolUse","session_id":"s1",` +
			`"tool_name":"Bash","tool_use_id":"toolu_01A","tool_input":{"command":"git status","timeout":1200},` +
			`"future_field":{"nested":[1,2]},"pid":4242,"ppid":4241}`
		var parsed shared.SpoolLine
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		store := newTestStore()
		store.AddSpoolLine(parsed)
		events := store.Events(EventsFilter{Limit: i64(1)})
		if len(events) != 1 {
			t.Fatalf("events: got %d, want 1", len(events))
		}
		payload, err := json.Marshal(events[0].Payload)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != raw {
			t.Errorf("payload = %s, want the line as read: %s", payload, raw)
		}
	})
}

func TestLifecycle(t *testing.T) {
	t.Run("ends, restarts and re-parents sessions and subagents from hook events alone", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionEnd" }))
		detail := detailOf(t, store, "s1")
		if detail == nil {
			t.Fatal("sessionDetail(s1): got nil, want the session")
		}
		if detail.Live || !eqInt(detail.EndedAt, ts0) || !eqInt(detail.StartedAt, ts0) {
			t.Errorf("after SessionEnd: got live %v endedAt %v startedAt %v, want ended at ts0", detail.Live, detail.EndedAt, detail.StartedAt)
		}
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts1)
			l.Event = "SessionStart"
			l.Cwd = str("/w/app")
		}))
		detail = detailOf(t, store, "s1")
		if !detail.Live || detail.EndedAt != nil || !eqInt(detail.StartedAt, ts0) || !eqStr(detail.Cwd, "/w/app") {
			t.Errorf("after SessionStart: got live %v endedAt %v startedAt %v cwd %v", detail.Live, detail.EndedAt, detail.StartedAt, detail.Cwd)
		}

		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts2)
			l.Event = "SubagentStart"
			l.AgentID = str("w1")
			l.AgentType = str("researcher")
			l.Prompt = str(strings.Repeat("p", 100))
		}))
		detail = detailOf(t, store, "s1")
		agents := detail.Agents
		if len(agents) != 2 {
			t.Fatalf("agents: got %d, want 2", len(agents))
		}
		first := agents[0]
		wantName := strings.Repeat("p", 80)
		if first.ID != "w1" || !eqStr(first.ParentID, "main") || first.Kind != shared.KindSubagent ||
			!eqStr(first.Name, wantName) || !first.Live || !eqInt(first.LastAt, ts2) {
			t.Errorf("agents[0]: got %+v, want w1 under main, titled by its prompt's head, live", first)
		}
		listed := store.SessionList()
		if len(listed) == 0 {
			t.Fatal("sessionList: got no items, want s1")
		}
		if title := listed[0].Title; !eqStr(title, wantName) {
			t.Errorf("list title: got %v, want %q", title, wantName)
		}

		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts3); l.Event = "SubagentStop"; l.AgentID = str("w1") }))
		if agent := findAgent(detailOf(t, store, "s1"), "w1"); agent == nil || agent.Live {
			t.Error("after SubagentStop: got w1 live, want ended")
		}
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts3); l.Event = "SessionEnd" }))
		for _, agent := range detailOf(t, store, "s1").Agents {
			if agent.Live {
				t.Errorf("after SessionEnd: agent %s still live", agent.ID)
			}
		}
	})

	t.Run("pairs hook tool events by tool_use_id, else by a time-keyed fallback id", func(t *testing.T) {
		store := newTestStore()
		var seen []shared.ToolCallRecord
		off := store.OnUpdate(func(change Change) { seen = append(seen, change.Tools...) })
		defer off()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts0)
			l.Event = "PreToolUse"
			l.ToolName = str("Read")
			l.ToolUseID = str("tu1")
			l.AgentID = str("w1")
		}))
		if len(seen) != 0 {
			t.Errorf("after the start alone: got %d tool calls, want none", len(seen))
		}
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts2)
			l.Event = "PostToolUse"
			l.ToolName = str("Read")
			l.ToolUseID = str("tu1")
			l.AgentID = str("w1")
		}))
		paired := shared.ToolCallRecord{
			ID: "tu1", SessionID: "s1", AgentID: str("w1"), Name: "Read",
			StartedAt: ts0, DurationMs: i64(2_000), Ok: true,
		}
		if !reflect.DeepEqual(seen, []shared.ToolCallRecord{paired}) {
			t.Errorf("paired call: got %+v, want %+v", seen, paired)
		}
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts3)
			l.Event = "PostToolUseFailure"
			l.ToolName = str("Bash")
		}))
		orphan := shared.ToolCallRecord{
			ID: fmt.Sprintf("hook:s1:Bash:%d", ts3), SessionID: "s1", AgentID: nil, Name: "Bash",
			StartedAt: ts3, DurationMs: nil, Ok: false,
		}
		if len(seen) < 2 {
			t.Fatalf("tool calls: got %d, want the paired one and the fallback", len(seen))
		}
		if !reflect.DeepEqual(seen[1], orphan) {
			t.Errorf("fallback call: got %+v, want %+v", seen[1], orphan)
		}
	})
}

// registryOver is what one registry test varies about an entry; the rest reads as null.
type registryOver struct {
	name       *string
	nameSource *string
	status     *string
	cwd        *string
	startedAt  *int64
}

func entry(sessionID string, over registryOver) shared.RegistrySession {
	return shared.RegistrySession{
		PID:        100,
		SessionID:  sessionID,
		Name:       over.name,
		NameSource: over.nameSource,
		Status:     over.status,
		Cwd:        over.cwd,
		StartedAt:  over.startedAt,
	}
}

func TestApplyRegistry(t *testing.T) {
	t.Run("makes a registered session live with its busy/idle state, and the user's name wins", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts0)
			l.Event = "SessionStart"
			l.Cwd = str("/w/app")
		}))
		store.UpsertSession(SessionUpsert{ID: "s1", Slug: str("from-the-transcript")})
		store.ApplyRegistry([]shared.RegistrySession{
			entry("s1", registryOver{name: str("ProxBeam"), nameSource: str("user"), status: str("busy")}),
		}, ts9)
		detail := detailOf(t, store, "s1")
		if !detail.Live || detail.Status == nil || *detail.Status != shared.StatusWorking ||
			detail.EndedAt != nil || !eqStr(detail.Name, "ProxBeam") {
			t.Errorf("registered: got live %v status %v endedAt %v name %v", detail.Live, detail.Status, detail.EndedAt, detail.Name)
		}
		// a name Claude Code did not get from the user never outranks the transcript's own titles
		store.ApplyRegistry([]shared.RegistrySession{
			entry("s1", registryOver{name: str("Auto name"), nameSource: str("ai"), status: str("idle")}),
		}, ts9)
		detail = detailOf(t, store, "s1")
		if !detail.Live || detail.Status == nil || *detail.Status != shared.StatusIdle {
			t.Errorf("re-registered: got live %v status %v, want live idle", detail.Live, detail.Status)
		}
		if !eqStr(detail.Name, "from-the-transcript") {
			t.Errorf("name: got %v, want from-the-transcript", detail.Name)
		}
	})

	t.Run("ends a session the registry no longer lists, at its last activity, hook or no hook", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1)
		if detail := detailOf(t, store, "s1"); !detail.Live {
			t.Error("while listed: got not live, want live")
		}
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts2 }), nil)
		store.ApplyRegistry(nil, ts3)
		detail := detailOf(t, store, "s1")
		if detail.Live || !eqInt(detail.EndedAt, ts2) || detail.Status != nil {
			t.Errorf("unlisted: got live %v endedAt %v status %v, want ended at its last request", detail.Live, detail.EndedAt, detail.Status)
		}
	})

	t.Run("keeps jobs running on their own and never makes a live session of the unattached traffic", func(t *testing.T) {
		store := newTestStore()
		store.UpsertSession(SessionUpsert{ID: "zai:j1", External: boolPtr(true)})
		store.UpsertAgent(AgentUpsert{SessionID: "zai:j1", ID: "j1", Kind: kindPtr(shared.KindExternal)})
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r9"
			r.SessionID = shared.OutsideSession
			r.Ts = ts1
		}), nil)
		store.ApplyRegistry(nil, ts9)
		job := detailOf(t, store, "zai:j1")
		if job == nil || !job.Live || !job.External {
			t.Errorf("job session: got %+v, want live and external", job)
		}
		unattached := detailOf(t, store, shared.OutsideSession)
		if unattached == nil || unattached.Live || !eqStr(unattached.Name, shared.UnattachedName) || !eqInt(unattached.EndedAt, ts1) {
			t.Errorf("unattached: got %+v, want ended at its last activity under its own name", unattached)
		}
	})

	t.Run("counts live agents on the list item, the same rule the session view's agents carry", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Event = "SubagentStart"; l.AgentID = str("w1") }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Event = "SubagentStart"; l.AgentID = str("w2") }))
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts1; r.AgentID = "w1" }), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r2"; r.Ts = ts1; r.AgentID = "w2" }), nil)
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+4*60_000)
		// main plus two fresh subagents: every one of them live, and counted
		detail := detailOf(t, store, "s1")
		lives := make([]bool, 0, len(detail.Agents))
		for _, agent := range detail.Agents {
			lives = append(lives, agent.Live)
		}
		if !slices.Equal(lives, []bool{true, true, true}) {
			t.Errorf("agents live: got %v, want all three", lives)
		}
		listed := findItem(store.SessionList(), "s1")
		if listed == nil || listed.AgentCount != 3 || listed.LiveAgentCount != 3 {
			t.Errorf("listed: got %+v, want 3 agents, 3 live", listed)
		}
		// five minutes of silence retires the subagents; the total stays its own number
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+6*60_000)
		listed = findItem(store.SessionList(), "s1")
		if listed == nil || listed.AgentCount != 3 || listed.LiveAgentCount != 1 {
			t.Errorf("listed after silence: got %+v, want 3 agents, 1 live", listed)
		}
	})

	t.Run("reads the same live words on the list item's count and the session view's rows", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Event = "SubagentStart"; l.AgentID = str("w1") }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Event = "SubagentStart"; l.AgentID = str("w2") }))
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts1; r.AgentID = "w1" }), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r2"; r.Ts = ts1; r.AgentID = "w2" }), nil)
		// at every pass — fresh work and stale — the count on the card is exactly the rows that say Live
		for _, moment := range []int64{ts1 + 4*60_000, ts1 + 6*60_000} {
			store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, moment)
			detail := detailOf(t, store, "s1")
			listed := findItem(store.SessionList(), "s1")
			if detail == nil || listed == nil {
				t.Fatal("missing the session in the detail or the list")
			}
			var live int64
			for _, agent := range detail.Agents {
				if agent.Live {
					live++
				}
			}
			if listed.LiveAgentCount != live {
				t.Errorf("at %d: listed %d live, detail %d live", moment, listed.LiveAgentCount, live)
			}
		}
	})

	t.Run("carries the main agent's now line and context fullness for live sessions, and nothing once ended", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r1"
			r.Ts = ts1
			r.AgentID = "main"
			r.What = str("↳ prompt: fix the rail")
			r.Tokens = tokens(10_000, 0, 5_000, 1_000)
		}), nil)
		// a subagent's newer call never speaks for the session, and a run total is no one call's context
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r2"
			r.Ts = ts2
			r.AgentID = "w1"
			r.What = str("subagent's call")
		}), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r3"
			r.Ts = ts3
			r.AgentID = "main"
			r.Totals = true
			r.What = str("the run's total")
			r.Tokens = tokens(500_000, 0, 0, 0)
		}), nil)
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{status: str("busy")})}, ts9)
		listed := findItem(store.SessionList(), "s1")
		if listed == nil || !listed.Live {
			t.Fatal("listed: got no live s1, want one")
		}
		if listed.Now == nil || !eqStr(listed.Now.What, "↳ prompt: fix the rail") || listed.Now.Ts != ts1 {
			t.Errorf("now: got %+v, want the fix-the-rail call at ts1", listed.Now)
		}
		// that same call's context: input + cacheRead + cacheWrite, in the 200k window the run total did not widen
		if listed.Context == nil || listed.Context.Used != 16_000 || listed.Context.Window != 200_000 {
			t.Errorf("context: got %+v, want 16,000 of 200,000", listed.Context)
		}
		// the newest main call wins, and a model id that says 1M widens the window
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r4"
			r.Ts = ts9
			r.AgentID = "main"
			r.Model = "claude-opus-5[1m]"
			r.What = str("↳ prompt: think big")
			r.Tokens = tokens(900_000, 0, 0, 0)
		}), nil)
		widened := findItem(store.SessionList(), "s1")
		if widened.Now == nil || !eqStr(widened.Now.What, "↳ prompt: think big") || widened.Now.Ts != ts9 {
			t.Errorf("now: got %+v, want the think-big call at ts9", widened.Now)
		}
		if widened.Context == nil || widened.Context.Used != 900_000 || widened.Context.Window != 1_000_000 {
			t.Errorf("context: got %+v, want 900,000 of 1,000,000", widened.Context)
		}
		// once the registry no longer lists the session, both fields are null
		store.ApplyRegistry(nil, ts9+dayMs)
		ended := findItem(store.SessionList(), "s1")
		if ended == nil || ended.Live || ended.Now != nil || ended.Context != nil {
			t.Errorf("ended: got %+v, want no live session and no now or context", ended)
		}
	})

	t.Run("keeps a subagent live under a live parent while its last request is under five minutes old", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Event = "SubagentStart"; l.AgentID = str("w1") }))
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts1; r.AgentID = "w1" }), nil)
		live := func() bool {
			agent := findAgent(detailOf(t, store, "s1"), "w1")
			return agent != nil && agent.Live
		}
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+4*60_000)
		if !live() {
			t.Error("four minutes: got not live, want fresh work under a live parent")
		}
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+6*60_000)
		if live() {
			t.Error("six minutes: got live, want stale now")
		}
	})

	t.Run("counts a subagent's start as its work until its first request, and never undoes an end", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Event = "SubagentStart"; l.AgentID = str("w1") }))
		live := func(id string) bool {
			agent := findAgent(detailOf(t, store, "s1"), id)
			return agent != nil && agent.Live
		}
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+4*60_000)
		if !live("w1") {
			t.Error("started, not yet working: got not live, want the start to count")
		}
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+6*60_000)
		if live("w1") {
			t.Error("five minutes of nothing: got live, want retired")
		}
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts1 + 60_000)
			l.Event = "SubagentStart"
			l.AgentID = str("w2")
		}))
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts1; r.AgentID = "w2" }), nil)
		store.AddSpoolLine(line(func(l *shared.SpoolLine) {
			l.Ts = isoTime(ts1 + 60_000)
			l.Event = "SubagentStop"
			l.AgentID = str("w2")
		}))
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+61_000)
		if live("w2") {
			t.Error("an explicit end stands, however fresh its last request is: got live")
		}
	})

	t.Run("ends a stale never-ended subagent with its parent when the parent's pid is gone", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts1); l.Event = "SubagentStart"; l.AgentID = str("w1") }))
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts1; r.AgentID = "w1" }), nil)
		agents := func() []shared.AgentView { return detailOf(t, store, "s1").Agents }
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{})}, ts1+30*60_000) // half an hour stale
		if agent := findAgent(detailOf(t, store, "s1"), "w1"); agent == nil || agent.Live {
			t.Error("half an hour stale: got live, want retired with no end recorded")
		}
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts2); l.Event = "SessionEnd" }))
		store.ApplyRegistry(nil, ts2) // the parent's pid is gone too
		if detail := detailOf(t, store, "s1"); detail.Live {
			t.Error("after the end and an empty registry: got live, want ended")
		}
		for _, agent := range agents() {
			if agent.Live {
				t.Errorf("agent %s still live", agent.ID)
			}
		}
	})

	t.Run("displays a registry session's start as the registry's startedAt, not the records' earliest", func(t *testing.T) {
		store := newTestStore()
		// the transcript reaches back three days through resumes; the open session began a day ago
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts0 - 3*dayMs }), nil)
		if detail := detailOf(t, store, "s1"); !eqInt(detail.StartedAt, ts0-3*dayMs) {
			t.Errorf("startedAt: got %v, want the record's own ts", detail.StartedAt)
		}
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{startedAt: i64(ts0 - dayMs)})}, ts9)
		if detail := detailOf(t, store, "s1"); !eqInt(detail.StartedAt, ts0-dayMs) {
			t.Errorf("startedAt: got %v, want the registry's own", detail.StartedAt)
		}
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r2"; r.Ts = ts0 - 2*dayMs }), nil) // another old record cannot drag it back
		if detail := detailOf(t, store, "s1"); !eqInt(detail.StartedAt, ts0-dayMs) {
			t.Errorf("startedAt after another old record: got %v, want the registry's own", detail.StartedAt)
		}
	})

	t.Run("notifies once, and only when something moved", func(t *testing.T) {
		store := newTestStore()
		store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Ts = isoTime(ts0); l.Event = "SessionStart" }))
		var seen []Change
		off := store.OnUpdate(func(change Change) { seen = append(seen, change) })
		defer off()
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{status: str("busy")})}, ts1)
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{status: str("busy")})}, ts2) // the same registry again: silence
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{status: str("idle")})}, ts3)
		if len(seen) != 2 {
			t.Fatalf("changes: got %d, want 2", len(seen))
		}
		if !seen[0].Sessions || !slices.Equal(seen[0].Touched, []string{"s1"}) {
			t.Errorf("first change: got sessions %v touched %v, want s1", seen[0].Sessions, seen[0].Touched)
		}
	})

	t.Run("creates a session the registry lists that ingest has not discovered yet", func(t *testing.T) {
		store := newTestStore()
		// an idle session from days ago, outside every since window ingest walks: the registry is enough
		store.ApplyRegistry([]shared.RegistrySession{
			entry("s9", registryOver{
				status: str("idle"), name: str("playground-56"), nameSource: str("derived"),
				cwd: str("/w/app"), startedAt: i64(ts1),
			}),
		}, ts9)
		detail := detailOf(t, store, "s9")
		if detail == nil || detail.ID != "s9" || !detail.Live || detail.Status == nil || *detail.Status != shared.StatusIdle ||
			!eqStr(detail.Cwd, "/w/app") || !eqInt(detail.StartedAt, ts1) || detail.EndedAt != nil ||
			!eqStr(detail.Name, "playground-56") {
			t.Errorf("detail: got %+v, want the registry's own idle session named playground-56", detail)
		}
		// and a registry that no longer lists it ends it like any other session
		store.ApplyRegistry(nil, ts9+5_000)
		if detail := detailOf(t, store, "s9"); detail.Live {
			t.Error("after an empty registry: got live, want ended")
		}
	})

	t.Run("names a session by rank: the user's registry name, the titles, the derived name", func(t *testing.T) {
		store := newTestStore()
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{name: str("app-56"), nameSource: str("derived")})}, ts9)
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "app-56") {
			t.Errorf("name: got %v, want the derived name standing alone", name)
		}
		store.UpsertSession(SessionUpsert{ID: "s1", Slug: str("from-the-transcript")})
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "from-the-transcript") {
			t.Errorf("name: got %v, want the transcript's title to outrank it", name)
		}
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{name: str("Hers"), nameSource: str("user")})}, ts9)
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "Hers") {
			t.Errorf("name: got %v, want the user's choice to outrank everything", name)
		}
		// a user name that goes away falls back down the same order, never to a derived name first
		store.ApplyRegistry([]shared.RegistrySession{entry("s1", registryOver{name: str("app-56"), nameSource: str("derived")})}, ts9)
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "from-the-transcript") {
			t.Errorf("name: got %v, want back to the transcript's title", name)
		}
	})

	t.Run("names a session by the repo's own name, never a path, when nothing else names it", func(t *testing.T) {
		store := newTestStore()
		path := "/Users/raouf/.work/onthegosystems/wpml-org/app"
		store.UpsertSession(SessionUpsert{ID: "s1", Cwd: str(path), Repo: str(path)})
		detail := detailOf(t, store, "s1")
		if !eqStr(detail.Name, "app") {
			t.Errorf("name: got %v, want the repo's own name", detail.Name)
		}
		// the path stays where the workspace is shown: the repo field, the card's tooltip
		if !eqStr(detail.Repo, path) {
			t.Errorf("repo: got %v, want the workspace path", detail.Repo)
		}
	})
}

// listItem is the suite's base list item, each case reshaping through shape.
func listItem(id string, shape func(*SessionListItem)) SessionListItem {
	item := SessionListItem{ID: id, Activity: shared.Activity{BucketMs: 1}}
	if shape != nil {
		shape(&item)
	}
	return item
}

func TestSessionTree(t *testing.T) {
	t.Run("keeps jobs off the top level: a job hangs under the session that submitted it", func(t *testing.T) {
		tree := SessionTree([]SessionListItem{
			listItem("s1", func(i *SessionListItem) { i.Live = true; i.LastAt = 9 }),
			listItem("zai:j1", func(i *SessionListItem) {
				i.External = true
				i.ParentSessionID = str("s1")
				i.Live = true
				i.LastAt = 8
			}),
			listItem("zai:j0", func(i *SessionListItem) { i.External = true; i.ParentSessionID = str("s1"); i.LastAt = 7 }),
		})
		if want := []string{"s1"}; !slices.Equal(treeIDs(tree), want) {
			t.Errorf("top level: got %v, want %v", treeIDs(tree), want)
		}
		if len(tree) == 0 {
			t.Fatal("tree: got no nodes, want s1")
		}
		if want := []string{"zai:j1", "zai:j0"}; !slices.Equal(treeIDs(tree[0].Jobs), want) {
			t.Errorf("jobs: got %v, want %v", treeIDs(tree[0].Jobs), want)
		}
	})

	t.Run("hangs a job whose parent is itself a job under their first main ancestor", func(t *testing.T) {
		tree := SessionTree([]SessionListItem{
			listItem("s1", func(i *SessionListItem) { i.LastAt = 9 }),
			listItem("zai:j1", func(i *SessionListItem) { i.External = true; i.ParentSessionID = str("s1"); i.LastAt = 8 }),
			listItem("zai:j2", func(i *SessionListItem) { i.External = true; i.ParentSessionID = str("zai:j1"); i.LastAt = 7 }),
		})
		if len(tree) != 1 {
			t.Fatalf("tree: got %d nodes, want 1", len(tree))
		}
		if want := []string{"zai:j1", "zai:j2"}; !slices.Equal(treeIDs(tree[0].Jobs), want) {
			t.Errorf("jobs: got %v, want %v", treeIDs(tree[0].Jobs), want)
		}
	})

	t.Run("sends a job with no origin, a lost parent or itself as parent to the Unattached group", func(t *testing.T) {
		tree := SessionTree([]SessionListItem{
			listItem("s1", func(i *SessionListItem) { i.LastAt = 9 }),
			listItem("zai:loose", func(i *SessionListItem) { i.External = true; i.Live = true; i.LastAt = 8 }),
			listItem("kimi:gone", func(i *SessionListItem) { i.External = true; i.ParentSessionID = str("not-listed"); i.LastAt = 7 }),
			listItem("zai:self", func(i *SessionListItem) { i.External = true; i.ParentSessionID = str("zai:self"); i.LastAt = 6 }),
			listItem(shared.OutsideSession, func(i *SessionListItem) { i.Name = str(shared.UnattachedName); i.LastAt = 1 }),
		})
		for _, node := range tree {
			if strings.Contains(node.ID, ":") {
				t.Errorf("top level holds the job %s", node.ID)
			}
		}
		var unattached *SessionTreeItem
		for i := range tree {
			if tree[i].ID == shared.OutsideSession {
				unattached = &tree[i]
			}
		}
		if unattached == nil {
			t.Fatal("unattached group: got none, want one")
		}
		if want := []string{"zai:loose", "kimi:gone", "zai:self"}; !slices.Equal(treeIDs(unattached.Jobs), want) {
			t.Errorf("unattached jobs: got %v, want %v", treeIDs(unattached.Jobs), want)
		}
	})
}

func TestAddRequest(t *testing.T) {
	t.Run("merges by id, keeping the first model and upstream, taking new facts only", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r1"
			r.Ts = ts0
			r.Model = "m1"
			r.Upstream = "u1"
			r.LatencyMs = nil
			r.Tokens = shared.ZeroTokens
			r.StopReason = nil
		}), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r1"
			r.Ts = ts1
			r.Model = ""
			r.Upstream = ""
			r.LatencyMs = i64(5)
			r.Tokens = tokens(1, 1, 0, 0)
			r.StopReason = str("end_turn")
		}), nil)
		list := store.Requests(RequestFilter{Session: str("s1")})
		if len(list) != 1 {
			t.Fatalf("requests: got %d, want 1", len(list))
		}
		got := list[0]
		if got.ID != "r1" || got.Ts != ts0 || got.Model != "m1" || got.Upstream != "u1" ||
			!eqInt(got.LatencyMs, 5) || !eqStr(got.StopReason, "end_turn") {
			t.Errorf("merged: got %+v, want the first facts with the new latency and stop reason", got)
		}
		if want := tokens(1, 1, 0, 0); got.Tokens != want {
			t.Errorf("tokens: got %+v, want %+v", got.Tokens, want)
		}
		detail := detailOf(t, store, "s1")
		if !eqInt(detail.StartedAt, ts0) || !eqStr(detail.Model, "m1") {
			t.Errorf("session: got startedAt %v model %v, want ts0 and m1", detail.StartedAt, detail.Model)
		}
		if agent := findAgent(detail, "main"); agent == nil || !eqStr(agent.Model, "m1") {
			t.Errorf("main agent model: got %v, want m1", agent)
		}
	})

	t.Run("filters, sorts newest-first and slices to the limit", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = 100; r.Model = "a" }), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r2"; r.Ts = 300; r.Model = "b" }), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r3"; r.Ts = 200; r.SessionID = "s2" }), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r4"; r.Ts = 50; r.AgentID = "w1" }), nil)
		check := func(name string, got []string, want []string) {
			t.Helper()
			if !slices.Equal(got, want) {
				t.Errorf("%s: got %v, want %v", name, got, want)
			}
		}
		check("all", requestIDs(store.Requests(RequestFilter{})), []string{"r2", "r3", "r1", "r4"})
		check("limit 2", requestIDs(store.Requests(RequestFilter{Limit: i64(2)})), []string{"r2", "r3"})
		check("session s1", requestIDs(store.Requests(RequestFilter{Session: str("s1")})), []string{"r2", "r1", "r4"})
		check("model a", requestIDs(store.Requests(RequestFilter{Model: str("a")})), []string{"r1"})
		check("agent w1", requestIDs(store.Requests(RequestFilter{Agent: str("w1")})), []string{"r4"})
		check("since 150", requestIDs(store.Requests(RequestFilter{Since: i64(150)})), []string{"r2", "r3"})
		check("requestsFor", requestIDs(store.RequestsFor(RequestFilter{Session: str("s1")})), []string{"r1", "r2", "r4"})
	})

	t.Run("answers the list reads off a cached newest-first order, fresh again after the next change", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = 100 }), nil)
		store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.ID = "t1"; tool.StartedAt = 200 }))
		store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) {
			tool.ID = "t2"
			tool.StartedAt = 300
			tool.SessionID = "s2"
			tool.Ok = false
		}))
		requests := func() []string {
			return requestIDs(store.Requests(RequestFilter{Limit: i64(10)}))
		}
		tools := func() []string { return toolIDs(store.Tools(ToolsFilter{Limit: i64(10)})) }
		// a burst of reads within one version all see the same newest-first answer, without re-sorting
		check := func(name string, got []string, want []string) {
			t.Helper()
			if !slices.Equal(got, want) {
				t.Errorf("%s: got %v, want %v", name, got, want)
			}
		}
		check("requests", requests(), []string{"r1"})
		check("requests twice", requests(), requests())
		wantTool := makeTool(func(tool *shared.ToolCallRecord) {
			tool.ID = "t2"
			tool.StartedAt = 300
			tool.SessionID = "s2"
			tool.Ok = false
		})
		if got := store.Tools(ToolsFilter{Limit: i64(1)}); !reflect.DeepEqual(got, []shared.ToolCallRecord{wantTool}) {
			t.Errorf("tools limit 1: got %+v, want %+v", got, wantTool)
		}
		check("tools", tools(), []string{"t2", "t1"})
		check("failed tools", toolIDs(store.Tools(ToolsFilter{Failed: true})), []string{"t2"})
		check("session tools", toolIDs(store.Tools(ToolsFilter{Session: str("s1")})), []string{"t1"})
		if count := store.SessionCount(); count != 2 {
			t.Errorf("sessionCount: got %d, want 2", count)
		}
		// the next change moves the version, and the cached answers move with it
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r2"; r.Ts = 400 }), nil)
		store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.ID = "t3"; tool.StartedAt = 500; tool.Ok = false }))
		check("requests after", requests(), []string{"r2", "r1"})
		check("tools after", tools(), []string{"t3", "t2", "t1"})
		if count := store.SessionCount(); count != 2 {
			t.Errorf("sessionCount after: got %d, want 2", count)
		}
	})

	t.Run("names the session after its main agent's model, never a subagent's latest one", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = 100; r.Model = "claude-opus-5-5" }), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r2"
			r.Ts = 200
			r.AgentID = "w1"
			r.Model = "zai:glm-5.3-flash"
		}), nil)
		if detail := detailOf(t, store, "s1"); !eqStr(detail.Model, "claude-opus-5-5") {
			t.Errorf("detail model: got %v, want claude-opus-5-5", detail.Model)
		}
		listed := store.SessionList()
		if len(listed) == 0 {
			t.Fatal("sessionList: got no items, want s1")
		}
		if !eqStr(listed[0].Model, "claude-opus-5-5") {
			t.Errorf("list model: got %v, want claude-opus-5-5", listed[0].Model)
		}
	})

	t.Run("falls back to the latest model until the main agent has a request", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r1"
			r.Ts = 100
			r.AgentID = "w1"
			r.Model = "zai:glm-5.3-flash"
		}), nil)
		if detail := detailOf(t, store, "s1"); !eqStr(detail.Model, "zai:glm-5.3-flash") {
			t.Errorf("model: got %v, want the subagent's until main speaks", detail.Model)
		}
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r2"; r.Ts = 200; r.Model = "claude-opus-5-5" }), nil)
		if detail := detailOf(t, store, "s1"); !eqStr(detail.Model, "claude-opus-5-5") {
			t.Errorf("model: got %v, want the main agent's", detail.Model)
		}
	})

	t.Run("reads a job session's model from the job's own agent, not its subagents", func(t *testing.T) {
		store := newTestStore()
		store.UpsertSession(SessionUpsert{ID: "s1", External: boolPtr(true)})
		store.UpsertAgent(AgentUpsert{
			SessionID: "s1", ID: "job-1", Kind: kindPtr(shared.KindExternal), Model: str("zai:glm-5.3"),
		})
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r1"
			r.Ts = 100
			r.AgentID = "job-1"
			r.Model = "zai:glm-5.3"
		}), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r2"
			r.Ts = 200
			r.AgentID = "sub-1"
			r.SessionID = "s1"
			r.Model = "claude-opus-5-5"
		}), nil)
		if detail := detailOf(t, store, "s1"); !eqStr(detail.Model, "zai:glm-5.3") {
			t.Errorf("model: got %v, want the job's own agent's", detail.Model)
		}
	})
}

func TestAddToolCall(t *testing.T) {
	t.Run("merges duration and success as they firm up", func(t *testing.T) {
		store := newTestStore()
		var seen []shared.ToolCallRecord
		off := store.OnUpdate(func(change Change) { seen = append(seen, change.Tools...) })
		defer off()
		store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.ID = "t1"; tool.DurationMs = nil; tool.Ok = false }))
		store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.ID = "t1"; tool.DurationMs = i64(50); tool.Ok = false }))
		store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.ID = "t1"; tool.DurationMs = i64(60); tool.Ok = true }))
		if len(seen) != 3 {
			t.Fatalf("tool calls: got %d, want 3", len(seen))
		}
		if !eqInt(seen[1].DurationMs, 50) || seen[1].Ok {
			t.Errorf("second call: got duration %v ok %v, want 50 and false", seen[1].DurationMs, seen[1].Ok)
		}
		if seen[2].ID != "t1" || !eqInt(seen[2].DurationMs, 60) || !seen[2].Ok {
			t.Errorf("third call: got %+v, want t1, 60, ok", seen[2])
		}
		want := []ToolCountRow{{Name: "Bash", Count: 1, Failures: 0}}
		if got := store.Models(nil).Tools; !reflect.DeepEqual(got, want) {
			t.Errorf("models tools: got %+v, want %+v", got, want)
		}
		if detail := detailOf(t, store, "s1"); detail.ToolCount != 1 || detail.ErrorCount != 0 {
			t.Errorf("session: got %d tools %d errors, want 1 and 0", detail.ToolCount, detail.ErrorCount)
		}
	})
}

func TestUpserts(t *testing.T) {
	t.Run("keeps the first session facts, the earliest start, and lets external flip freely", func(t *testing.T) {
		store := newTestStore()
		store.UpsertSession(SessionUpsert{
			ID: "s1", Cwd: str("/a"), Upstream: str("u1"), CcVersion: str("1.0"), StartedAt: i64(100), External: boolPtr(false),
		})
		store.UpsertSession(SessionUpsert{
			ID: "s1", Cwd: str("/b"), Upstream: str("u2"), CcVersion: str("2.0"), StartedAt: i64(50), External: boolPtr(true),
		})
		detail := detailOf(t, store, "s1")
		if !eqStr(detail.Cwd, "/a") || detail.Upstream != "u1" || !eqStr(detail.CcVersion, "1.0") ||
			!eqInt(detail.StartedAt, 50) || !detail.External {
			t.Errorf("detail: got cwd %v upstream %v cc %v startedAt %v external %v, want /a u1 1.0 50 true",
				detail.Cwd, detail.Upstream, detail.CcVersion, detail.StartedAt, detail.External)
		}
	})

	t.Run("ends a session and every agent in it", func(t *testing.T) {
		store := newTestStore()
		store.UpsertSession(SessionUpsert{ID: "s1"})
		store.UpsertAgent(AgentUpsert{SessionID: "s1", ID: "w1"})
		store.EndSession("s1", 900)
		detail := detailOf(t, store, "s1")
		if detail.Live || !eqInt(detail.EndedAt, 900) {
			t.Errorf("detail: got live %v endedAt %v, want ended at 900", detail.Live, detail.EndedAt)
		}
		for _, agent := range detail.Agents {
			if agent.Live {
				t.Errorf("agent %s still live", agent.ID)
			}
		}
	})

	t.Run("upserts external agents with their own shape and retires them on end", func(t *testing.T) {
		store := newTestStore()
		store.UpsertAgent(AgentUpsert{
			SessionID: "s1", ID: "w1", ParentID: strPtrPtr(nil), Kind: kindPtr(shared.KindExternal),
			Name: str("zai job"), Model: str("zai:glm-5.3"), Title: strPtrPtr(str("Fix tests")),
		})
		store.UpsertAgent(AgentUpsert{SessionID: "s1", ID: "w1", Model: str("zai:other")}) // model seeds once
		w1 := findAgent(detailOf(t, store, "s1"), "w1")
		if w1 == nil || w1.ID != "w1" || w1.ParentID != nil || w1.Kind != shared.KindExternal ||
			!eqStr(w1.Name, "Fix tests") || !eqStr(w1.Model, "zai:glm-5.3") || !w1.Live {
			t.Errorf("w1: got %+v, want the external shape named by its title", w1)
		}
		store.EndAgent("s1", "w1", 500)
		if w1 = findAgent(detailOf(t, store, "s1"), "w1"); w1 == nil || w1.Live {
			t.Error("after endAgent: got live, want retired")
		}
	})

	t.Run("carries an agent's meta type and task, naming it type and task ahead of its prompt", func(t *testing.T) {
		store := newTestStore()
		store.UpsertAgent(AgentUpsert{SessionID: "s1", ID: "a1", AgentType: str("Explore"), Description: str("find it")})
		a1 := findAgent(detailOf(t, store, "s1"), "a1")
		if a1 == nil || !eqStr(a1.Name, "Explore — find it") || !eqStr(a1.AgentType, "Explore") || !eqStr(a1.Description, "find it") {
			t.Errorf("a1: got %+v, want Explore — find it", a1)
		}
		store.UpsertAgent(AgentUpsert{SessionID: "s1", ID: "a1", Title: strPtrPtr(str("The search"))})
		if a1 = findAgent(detailOf(t, store, "s1"), "a1"); a1 == nil || !eqStr(a1.Name, "Explore — find it") {
			t.Errorf("a1 name: got %v, want the meta file's to outrank the prompt head", a1.Name)
		}
	})
}

func TestNamesBranchesRepos(t *testing.T) {
	t.Run("takes the first non-empty of custom title, agent name, AI title and slug, latest value of each", func(t *testing.T) {
		store := newTestStore()
		store.UpsertSession(SessionUpsert{ID: "s1", Slug: str("first-slug")})
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "first-slug") {
			t.Errorf("name: got %v, want first-slug", name)
		}
		store.UpsertSession(SessionUpsert{ID: "s1", CustomTitle: str("Port the router")})
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "Port the router") {
			t.Errorf("name: got %v, want the custom title", name)
		}
		store.UpsertSession(SessionUpsert{ID: "s1", AgentName: str("Scout"), AiTitle: str("Porting")})
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "Port the router") {
			t.Errorf("name: got %v, want the custom title to keep rank", name)
		}
		store.UpsertSession(SessionUpsert{ID: "s1", CustomTitle: str(""), Slug: str("second-slug")}) // empty values change nothing
		store.UpsertSession(SessionUpsert{ID: "s1", CustomTitle: str("Port the router, part two")})
		if name := detailOf(t, store, "s1").Name; !eqStr(name, "Port the router, part two") {
			t.Errorf("name: got %v, want the newer custom title", name)
		}
		store.UpsertSession(SessionUpsert{ID: "s2", Slug: str("a")})
		store.UpsertSession(SessionUpsert{ID: "s2", Slug: str("b")})
		if name := detailOf(t, store, "s2").Name; !eqStr(name, "b") { // latest slug wins within its own key
			t.Errorf("name: got %v, want b", name)
		}
	})

	t.Run("never leaves a session unnamed: slug, then repo, then the id's first eight characters", func(t *testing.T) {
		store := newTestStore()
		// a session nothing named — no titles, no registry name, no repo — still reads as its id
		store.UpsertSession(SessionUpsert{ID: "13fbb7a3-aaaa-bbbb"})
		detail := detailOf(t, store, "13fbb7a3-aaaa-bbbb")
		if !eqStr(detail.Name, "Session 13fbb7a3") {
			t.Errorf("name: got %v, want Session 13fbb7a3", detail.Name)
		}
		if listed := findItem(store.SessionList(), "13fbb7a3-aaaa-bbbb"); listed == nil || !eqStr(listed.Name, "Session 13fbb7a3") {
			t.Errorf("list name: got %v, want Session 13fbb7a3", listed)
		}
		// a repo outranks the id, and the transcript's slug outranks the repo
		store.UpsertSession(SessionUpsert{ID: "13fbb7a3-aaaa-bbbb", Repo: str("proxbeam")})
		if name := detailOf(t, store, "13fbb7a3-aaaa-bbbb").Name; !eqStr(name, "proxbeam") {
			t.Errorf("name: got %v, want proxbeam", name)
		}
		store.UpsertSession(SessionUpsert{ID: "13fbb7a3-aaaa-bbbb", Slug: str("fix-the-valve")})
		if name := detailOf(t, store, "13fbb7a3-aaaa-bbbb").Name; !eqStr(name, "fix-the-valve") {
			t.Errorf("name: got %v, want fix-the-valve", name)
		}
	})

	t.Run("keeps the latest branch and a job's repo and parent session", func(t *testing.T) {
		store := newTestStore()
		store.UpsertSession(SessionUpsert{
			ID: "j1", Branch: str("zai/one"), Repo: str("/w/main"), ParentSessionID: str("s9"),
		})
		store.UpsertSession(SessionUpsert{ID: "j1", Branch: str("zai/two")})
		detail := detailOf(t, store, "j1")
		if !eqStr(detail.Branch, "zai/two") || !eqStr(detail.Repo, "/w/main") || !eqStr(detail.ParentSessionID, "s9") {
			t.Errorf("detail: got branch %v repo %v parent %v, want zai/two, /w/main, s9",
				detail.Branch, detail.Repo, detail.ParentSessionID)
		}
		if listed := findItem(store.SessionList(), "j1"); listed == nil || !eqStr(listed.Branch, "zai/two") ||
			!eqStr(listed.Repo, "/w/main") || !eqStr(listed.ParentSessionID, "s9") {
			t.Errorf("list item: got %+v, want the same three", listed)
		}
	})

	t.Run("derives a plain session's repo from its cwd and caches it there", func(t *testing.T) {
		repo := t.TempDir()
		worktree := t.TempDir()
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		pointer := fmt.Sprintf("gitdir: %s\n", filepath.Join(repo, ".git", "worktrees", "wt"))
		if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte(pointer), 0o600); err != nil {
			t.Fatal(err)
		}
		store := newTestStore()
		store.UpsertSession(SessionUpsert{ID: "s1", Cwd: str(filepath.Join(repo, "src"))})
		store.UpsertSession(SessionUpsert{ID: "s2", Cwd: str(worktree)})
		if detail := detailOf(t, store, "s1"); !eqStr(detail.Repo, repo) {
			t.Errorf("s1 repo: got %v, want %v", detail.Repo, repo)
		}
		if detail := detailOf(t, store, "s2"); !eqStr(detail.Repo, repo) { // the worktree folds into its main checkout
			t.Errorf("s2 repo: got %v, want %v", detail.Repo, repo)
		}
		if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
			t.Fatal(err)
		}
		if detail := detailOf(t, store, "s1"); !eqStr(detail.Repo, repo) { // answered from the cache, not walked again
			t.Errorf("s1 repo after the .git is gone: got %v, want %v", detail.Repo, repo)
		}
		if detail := store.SessionDetail("nope"); detail != nil {
			t.Errorf("unknown session: got %+v, want nil", detail)
		}
	})
}

func TestViews(t *testing.T) {
	t.Run("models() reuses the aggregations over everything stored", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.Model = "glm-5.3"
			r.Upstream = "http://127.0.0.1:8787"
			r.Tokens = tokens(10, 0, 0, 0)
			r.StopReason = str("end_turn") // a finished request: an in-flight one is never priced
		}), nil)
		store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) { tool.Ok = false }))
		out := store.Models(nil)
		wantCost := shared.USD(0.000014)
		wantRow := shared.ModelRow{
			Model: "glm-5.3", Provider: "Z.ai", Requests: 1, Errors: 0,
			Tokens: tokens(10, 0, 0, 0), LatencyP50: i64(100), CostUsd: &wantCost,
		}
		if !reflect.DeepEqual(out.Models, []shared.ModelRow{wantRow}) {
			t.Errorf("models: got %+v, want %+v", out.Models, wantRow)
		}
		if len(out.Upstreams) == 0 {
			t.Fatal("upstreams: got none, want the router's row")
		}
		first := out.Upstreams[0]
		if first.Upstream != "http://127.0.0.1:8787" || first.Host != "127.0.0.1:8787" || first.Requests != 1 {
			t.Errorf("upstream row: got %+v", first)
		}
		wantTools := []ToolCountRow{{Name: "Bash", Count: 1, Failures: 1}}
		if !reflect.DeepEqual(out.Tools, wantTools) {
			t.Errorf("tools: got %+v, want %+v", out.Tools, wantTools)
		}
	})

	t.Run("summary() counts live sessions and totals across the board", func(t *testing.T) {
		store := newTestStore()
		empty := store.Summary()
		if empty.Sessions != 0 || empty.LiveSessions != 0 || empty.Requests != 0 || empty.Tokens != shared.ZeroTokens {
			t.Errorf("empty summary: got %+v", empty)
		}
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.Tokens = tokens(1, 2, 3, 4) }), nil)
		store.UpsertSession(SessionUpsert{ID: "s2", StartedAt: i64(5)})
		if summary := store.Summary(); summary.Sessions != 2 || summary.LiveSessions != 0 {
			t.Errorf("summary: got %d sessions %d live, want 2 and 0", summary.Sessions, summary.LiveSessions)
		}
		store.ApplyRegistry([]shared.RegistrySession{
			{PID: 101, SessionID: "s1", Status: str("busy")},
			{PID: 102, SessionID: "s2", Status: str("idle")},
		}, ts9)
		summary := store.Summary()
		if summary.Sessions != 2 || summary.LiveSessions != 2 || summary.Requests != 1 || summary.Tokens != tokens(1, 2, 3, 4) {
			t.Errorf("summary: got %+v, want 2 live sessions over 1 request", summary)
		}
	})

	t.Run("computes latency p95 per agent from its own requests, and a dash under five timed ones", func(t *testing.T) {
		store := newTestStore()
		for i, latency := range []int64{100, 200, 300, 400, 500, 1000} {
			latency := latency
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("r%d", i)
				r.Ts = ts1 + int64(i)
				r.LatencyMs = i64(latency)
			}), nil)
		}
		// six timed requests: the linear-interpolation p95 lands between the two top values
		if agent := findAgent(detailOf(t, store, "s1"), "main"); agent == nil || !eqInt(agent.LatencyP95, 875) {
			t.Errorf("main p95: got %v, want 875", agent)
		}
		// five requests but one untimed: four samples say nothing
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r9"; r.Ts = ts1 + 9; r.LatencyMs = nil }), nil)
		store.UpsertAgent(AgentUpsert{SessionID: "s1", ID: "w1"})
		for i := int64(0); i < 3; i++ {
			i := i
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("w%d", i)
				r.Ts = ts1 + i
				r.AgentID = "w1"
				r.LatencyMs = i64(50 + i)
			}), nil)
		}
		if agent := findAgent(detailOf(t, store, "s1"), "w1"); agent == nil || agent.LatencyP95 != nil {
			t.Errorf("w1 p95: got %v, want null under five samples", agent)
		}
	})

	t.Run("splits a session's cost by family over the very requests the card's estimate sums", func(t *testing.T) {
		store := newTestStore()
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID = "r1"; r.Ts = ts1; r.StopReason = str("end_turn") }), nil) // claude, via anthropic
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r2"
			r.Model = "glm-5.3"
			r.Upstream = "https://api.z.ai"
			r.Ts = ts1
			r.Tokens = tokens(1e6, 0, 0, 0)
			r.StopReason = str("end_turn")
		}), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "r3"
			r.Model = "kimi-k3"
			r.Upstream = "https://api.moonshot.ai"
			r.Ts = ts1
			r.Tokens = tokens(0, 0, 0, 1e6)
			r.StopReason = str("end_turn")
		}), nil)
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { // unpriced: nowhere
			r.ID = "r4"
			r.Model = "not-a-model"
			r.Ts = ts1
			r.StopReason = str("end_turn")
		}), nil)
		listed := findItem(store.SessionList(), "s1")
		if listed == nil || listed.CostSplitUsd == nil {
			t.Fatal("cost split: got none, want one")
		}
		split := listed.CostSplitUsd
		if split.Claude == nil || *split.Claude <= 0 {
			t.Errorf("claude part: got %v, want above zero", split.Claude)
		}
		if split.GLM == nil || *split.GLM <= 0 {
			t.Errorf("glm part: got %v, want above zero", split.GLM)
		}
		if split.Other == nil || *split.Other <= 0 {
			t.Errorf("other part: got %v, want above zero", split.Other)
		}
		parts := cost.AddCost(cost.AddCost(split.Claude, split.GLM), split.Other)
		if listed.CostUsd == nil || math.Abs(float64(*parts-*listed.CostUsd)) >= 0.005 {
			t.Errorf("parts %v vs card %v: want the header's parts to add up to the card's number", parts, listed.CostUsd)
		}
	})
}

// keptOf reports the kept records of one session: how many and the oldest timestamp among them.
func keptOf(store *Store, session string) (count int, oldest int64) {
	oldest = math.MaxInt64
	for _, record := range store.Raw().Requests {
		if record.SessionID != session {
			continue
		}
		count++
		if record.Ts < oldest {
			oldest = record.Ts
		}
	}
	return count, oldest
}

func TestBounds(t *testing.T) {
	t.Run("keeps at most MAX_SESSIONS sessions, dropping the stalest", func(t *testing.T) {
		store := newTestStore()
		for i := 0; i <= shared.MaxSessions+4; i++ {
			i := i
			store.AddSpoolLine(line(func(l *shared.SpoolLine) {
				l.Ts = isoTime(ts0 + int64(i))
				l.SessionID = str(fmt.Sprintf("s%d", i))
			}))
		}
		list := store.SessionList()
		if len(list) != shared.MaxSessions {
			t.Fatalf("sessions: got %d, want %d", len(list), shared.MaxSessions)
		}
		if findItem(list, "s0") != nil {
			t.Error("the stalest session survived")
		}
		if findItem(list, fmt.Sprintf("s%d", shared.MaxSessions+4)) == nil {
			t.Error("the newest session was dropped")
		}
	})

	// promptAt names the boundary records so eviction can be asserted by label.
	promptAt := func(i, last int) string {
		if i == 0 {
			return "first"
		}
		if i == 1 {
			return "second"
		}
		if i == last {
			return "last"
		}
		return "mid"
	}

	t.Run("keeps at most MAX_RECORDS records, shedding the oldest events first", func(t *testing.T) {
		store := newTestStore()
		total := shared.MaxRecords + 2
		for i := 0; i < total; i++ {
			i := i
			store.AddSpoolLine(shared.SpoolLine{
				Ts:        isoTime(ts0 + int64(i)),
				Event:     "UserPromptSubmit",
				SessionID: str("bulk"),
				Prompt:    str(promptAt(i, total-1)),
			})
		}
		events := store.Events(EventsFilter{Limit: i64(1_000_000)})
		if len(events) > shared.MaxRecords { // a batch can leave the store under the cap
			t.Errorf("events: got %d, want at most %d", len(events), shared.MaxRecords)
		}
		if len(events) == 0 {
			t.Fatal("events: got none, want the shed-bulk tail")
		}
		if !eqStr(events[0].Label, "last") { // the newest record is never among the shed
			t.Errorf("newest label: got %v, want last", events[0].Label)
		}
		for _, event := range events {
			if event.Label != nil && (*event.Label == "first" || *event.Label == "second") {
				t.Errorf("the %q record survived the shed", *event.Label)
			}
		}
	})

	// The three cases below run the shedding at the production cap. The race detector slows
	// the same passes 5-20x, so under it they run against a tenth-size cap with every count
	// the same tenth of it — one batch over the cap, one pass, the same order, every
	// assertion still checked.
	shedCap, shedBatch := int64(shared.MaxRecords), int64((shared.MaxRecords+9)/10)
	if testkit.RaceEnabled {
		shedCap, shedBatch = 20_000, 2_000
	}
	shedAt := func(store *Store) { store.maxRecords, store.evictBatch = shedCap, shedBatch }

	t.Run("sheds by timestamp, never by arrival order: an older batch that lands last is the one that goes", func(t *testing.T) {
		store := newTestStore()
		shedAt(store)
		fresh, aged := int(shedCap/2), int(shedCap/2+1)
		for i := 0; i < fresh; i++ {
			i := i
			// the fresh records arrive first — under insertion-order draining they would be the ones shed
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("fresh-%d", i)
				r.SessionID = "fresh"
				r.Ts = ts0 + int64(i)
			}), nil)
		}
		for i := 0; i < aged; i++ {
			i := i
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("aged-%d", i)
				r.SessionID = "aged"
				r.Ts = ts0 - int64(aged) + int64(i)
			}), nil)
		}
		if count, _ := keptOf(store, "fresh"); count != fresh {
			t.Errorf("fresh kept: got %d, want %d", count, fresh)
		}
		count, oldest := keptOf(store, "aged")
		wantKept := aged - int(shedBatch) // exactly one batch over the cap, taken from the oldest by ts
		if count != wantKept {
			t.Errorf("aged kept: got %d, want %d", count, wantKept)
		}
		if oldest != ts0-int64(wantKept) {
			t.Errorf("aged oldest: got %d, want %d", oldest, ts0-int64(wantKept))
		}
	})

	t.Run("keeps a live session's records while younger unprotected records are shed instead", func(t *testing.T) {
		store := newTestStore()
		shedAt(store)
		store.ApplyRegistry([]shared.RegistrySession{
			{PID: 4711, SessionID: "open", Status: str("busy"), StartedAt: i64(ts0)},
		}, ts9)
		openCount := int(shedCap * 3 / 10)
		closedCount := int(shedCap*7/10) + 1
		for i := 0; i < openCount; i++ {
			i := i
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("open-%d", i)
				r.SessionID = "open"
				r.AgentID = "main"
				r.Ts = ts0 + int64(i)
			}), nil)
		}
		for i := 0; i < closedCount; i++ {
			i := i
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("closed-%d", i)
				r.SessionID = "closed"
				r.Ts = ts0 + int64(openCount) + int64(i)
			}), nil)
		}
		count, _ := keptOf(store, "open")
		if count != openCount { // the oldest records held, and every one of them kept
			t.Errorf("open kept: got %d, want %d", count, openCount)
		}
		count, oldest := keptOf(store, "closed")
		wantKept := closedCount - int(shedBatch)
		if count != wantKept { // the shedding took the dead session's oldest, not the live one's
			t.Errorf("closed kept: got %d, want %d", count, wantKept)
		}
		if oldest != ts0+int64(openCount)+int64(shedBatch) {
			t.Errorf("closed oldest: got %d, want %d", oldest, ts0+int64(openCount)+int64(shedBatch))
		}
	})

	t.Run("sheds across collections by timestamp: an old tool call goes before any newer request", func(t *testing.T) {
		store := newTestStore()
		shedAt(store)
		toolsCount := int(shedCap * 3 / 10)
		requestsCount := int(shedCap*7/10) + 1
		for i := 0; i < toolsCount; i++ {
			i := i
			store.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) {
				tool.ID = fmt.Sprintf("tool-%d", i)
				tool.SessionID = "s1"
				tool.StartedAt = ts0 + int64(i)
			}))
		}
		for i := 0; i < requestsCount; i++ {
			i := i
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("req-%d", i)
				r.SessionID = "s1"
				r.Ts = ts0 + int64(toolsCount) + int64(i)
			}), nil)
		}
		if got := len(store.Raw().Tools); got != toolsCount-int(shedBatch) {
			t.Errorf("tools: got %d, want %d", got, toolsCount-int(shedBatch))
		}
		if got := len(store.Raw().Requests); got != requestsCount {
			t.Errorf("requests: got %d, want %d", got, requestsCount)
		}
	})
}

func TestNotifications(t *testing.T) {
	t.Run("delivers deltas to listeners until they unsubscribe, versioning every change", func(t *testing.T) {
		store := newTestStore()
		var seen []Change
		off := store.OnUpdate(func(change Change) { seen = append(seen, change) })
		before := store.Version()
		store.AddSpoolLine(line(nil))
		store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.Ts = ts2 }), nil)
		off()
		store.AddToolCall(makeTool(nil))
		if len(seen) != 2 {
			t.Fatalf("changes: got %d, want 2", len(seen))
		}
		if !seen[0].Sessions || len(seen[0].Events) != 1 || seen[0].Events[0].Kind != "UserPromptSubmit" {
			t.Errorf("first change: got sessions %v events %+v", seen[0].Sessions, seen[0].Events)
		}
		if len(seen[1].Requests) != 1 {
			t.Errorf("second change: got %d requests, want 1", len(seen[1].Requests))
		}
		if after := store.Version(); after != before+3 {
			t.Errorf("version: got %d, want %d", after, before+3)
		}
	})
}

func TestScale(t *testing.T) {
	t.Run("answers sessionDetail and sessionList for 300 agents over 10,000 requests in under 300 ms", func(t *testing.T) {
		// the race detector slows the same work 5-20x, so the shape runs at a tenth of the
		// size with the clock limit off; every count below is still asserted
		agents, requests := 300, 10_000
		if testkit.RaceEnabled {
			agents, requests = 30, 1_000
		}
		store := newTestStore()
		store.UpsertSession(SessionUpsert{ID: "s1", Cwd: str("/w/app")})
		for a := 0; a < agents; a++ {
			a := a
			store.UpsertAgent(AgentUpsert{
				SessionID: "s1",
				ID:        fmt.Sprintf("w%d", a),
				Kind:      kindPtr(shared.KindSubagent),
				Name:      str(fmt.Sprintf("worker %d", a)),
			})
		}
		for i := 0; i < requests; i++ {
			i := i
			store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID = fmt.Sprintf("r%d", i)
				r.SessionID = "s1"
				r.AgentID = fmt.Sprintf("w%d", i%agents)
				r.Ts = ts0 + int64(i)
				r.Tokens = tokens(10, 5, 0, 0)
			}), nil)
		}
		started := time.Now()
		listed := store.SessionList()
		var agentTotal, requestCount int64
		for _, item := range listed {
			view := detailOf(t, store, item.ID)
			if view == nil {
				continue
			}
			agentTotal += int64(len(view.Agents))
			requestCount += view.RequestCount
		}
		elapsed := time.Since(started)
		if len(listed) != 1 {
			t.Errorf("list: got %d sessions, want 1", len(listed))
		}
		if agentTotal != int64(agents)+1 { // the subagents plus the session's own main
			t.Errorf("agents: got %d, want %d", agentTotal, agents+1)
		}
		if requestCount != int64(requests) {
			t.Errorf("requests: got %d, want %d", requestCount, requests)
		}
		if !testkit.RaceEnabled && elapsed >= 300*time.Millisecond {
			t.Errorf("elapsed: got %v, want under 300ms", elapsed)
		}
	})
}
