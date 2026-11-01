package store

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// A restart reads the spool files again from their start, so a warm start feeds the store the
// same lines a cold start does. Each case below is one history of lines, cut at a restart: the
// lines before it are what the warm load held, and the whole history is read again after it.
// The warm store must end up where the cold store, fed the history once, ends up.

// lineAt is a spool line of the given event at the offset ms after ts1; extra is the rest of
// its JSON members, without braces.
func lineAt(t *testing.T, offset int64, event, extra string) shared.SpoolLine {
	t.Helper()
	text := fmt.Sprintf(`{"ts":%q,"event":%q,"session_id":"s1"`, isoTime(ts1+offset), event)
	if extra != "" {
		text += "," + extra
	}
	return spoolLineFrom(t, text+"}")
}

// storeAfter is every view of a store that a line can change, as one comparable text.
func storeAfter(t *testing.T, store *Store) string {
	t.Helper()
	views := map[string]any{
		"tools":  store.Tools(ToolsFilter{}),
		"events": store.Events(EventsFilter{}),
	}
	for _, item := range store.SessionList() {
		views["session:"+item.ID] = store.SessionDetail(item.ID)
		views["list:"+item.ID] = item
	}
	encoded, err := json.MarshalIndent(views, "", " ")
	if err != nil {
		t.Fatalf("encode the views: %v", err)
	}
	return string(encoded)
}

// restartedStore is a store warmed with the first held lines of the history as the events a
// previous run kept, then fed the whole history.
func restartedStore(t *testing.T, history []shared.SpoolLine, held int) *Store {
	t.Helper()
	source := &fakeWarmSource{}
	for at, line := range history[:held] {
		ts, _ := tsToMs(line.Ts)
		source.events = append(source.events, shared.EventRecord{
			Seq: int64(at + 1), Ts: ts, Kind: line.Event, SessionID: line.SessionID, AgentID: line.AgentID,
			Label: labelFor(&line), Payload: line,
		})
	}
	store := newTestStore()
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	for _, line := range history {
		store.AddSpoolLine(line)
	}
	return store
}

// firstDifference quotes the first line where two texts part, with the line before it.
func firstDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	for at := range min(len(wantLines), len(gotLines)) {
		if wantLines[at] != gotLines[at] {
			from := max(at-2, 0)
			return fmt.Sprintf("line %d\n cold: %s\n warm: %s\n (context: %s)",
				at+1, strings.TrimSpace(wantLines[at]), strings.TrimSpace(gotLines[at]),
				strings.TrimSpace(strings.Join(wantLines[from:at], " ")))
		}
	}
	return fmt.Sprintf("the texts have %d and %d lines", len(wantLines), len(gotLines))
}

func coldStore(history []shared.SpoolLine) *Store {
	store := newTestStore()
	for _, line := range history {
		store.AddSpoolLine(line)
	}
	return store
}

func TestAWarmStartTakesTheSpoolLinesLikeAColdStart(t *testing.T) {
	cases := map[string]func(t *testing.T) []shared.SpoolLine{
		"a tool that starts before a restart and ends after it is one paired tool call": func(t *testing.T) []shared.SpoolLine {
			return []shared.SpoolLine{
				lineAt(t, 0, "SessionStart", `"cwd":"/w/app"`),
				lineAt(t, 100, "PreToolUse", `"agent_id":"main","tool_name":"Bash","tool_use_id":"tu1","tool_input":{"command":"npm test"}`),
				lineAt(t, 900, "PostToolUse", `"agent_id":"main","tool_name":"Bash","tool_use_id":"tu1"`),
			}
		},
		"a failed tool that started before a restart keeps its start": func(t *testing.T) []shared.SpoolLine {
			return []shared.SpoolLine{
				lineAt(t, 100, "PreToolUse", `"tool_name":"Read","tool_use_id":"tu2"`),
				lineAt(t, 600, "PostToolUseFailure", `"tool_name":"Read","tool_use_id":"tu2"`),
			}
		},
		"a subagent started before a restart is named, titled and live after it": func(t *testing.T) []shared.SpoolLine {
			return []shared.SpoolLine{
				lineAt(t, 0, "SessionStart", ""),
				lineAt(t, 100, "SubagentStart", `"agent_id":"a1","agent_type":"Explore","prompt":"find the flaky test"`),
				lineAt(t, 800, "UserPromptSubmit", `"prompt":"and then"`),
			}
		},
		"a subagent that stops after a restart ends after it": func(t *testing.T) []shared.SpoolLine {
			return []shared.SpoolLine{
				lineAt(t, 100, "SubagentStart", `"agent_id":"a1","agent_type":"Explore"`),
				lineAt(t, 900, "SubagentStop", `"agent_id":"a1"`),
			}
		},
		"a session that ends after a restart ends after it": func(t *testing.T) []shared.SpoolLine {
			return []shared.SpoolLine{
				lineAt(t, 0, "SessionStart", `"cwd":"/w/app"`),
				lineAt(t, 700, "SessionEnd", `"reason":"clear"`),
			}
		},
		"the facts a session start carries stand after a restart": func(t *testing.T) []shared.SpoolLine {
			return []shared.SpoolLine{
				lineAt(t, 0, "SessionStart",
					`"cwd":"/w/app","base_url":"https://api.z.ai/api/anthropic","cc_version":"2.1.0","model_env":{"ANTHROPIC_MODEL":"glm-5.3"}`),
				lineAt(t, 700, "UserPromptSubmit", `"prompt":"go"`),
			}
		},
		"the notices a run counted are counted after a restart": func(t *testing.T) []shared.SpoolLine {
			return []shared.SpoolLine{
				lineAt(t, 0, "SessionStart", ""),
				lineAt(t, 100, shared.NoticeEvent, `"agent_id":"main","message":"usage limit reached"`),
				lineAt(t, 200, shared.NoticeEvent, `"message":"rate limited"`),
				lineAt(t, 900, shared.NoticeEvent, `"agent_id":"main","message":"overloaded"`),
			}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			history := build(t)
			want := storeAfter(t, coldStore(history))
			// every cut: the restart can fall between any two lines
			for held := 1; held < len(history); held++ {
				if got := storeAfter(t, restartedStore(t, history, held)); got != want {
					t.Errorf("a restart after %d of %d lines differs from a cold start:\n%s", held, len(history), firstDifference(want, got))
				}
			}
		})
	}
}

// callsAfter is the requests a store holds, in a form that tells a joined call from two.
func callsAfter(t *testing.T, store *Store) string {
	t.Helper()
	records := store.RequestsFor(RequestFilter{})
	for at := range records {
		// the order a record's keys are written in is a property of the file it was read
		// from, which a history file does not keep
		records[at].EarlyKeys, records[at].TailKeys = nil, nil
	}
	slices.SortFunc(records, func(a, b shared.RequestRecord) int { return strings.Compare(a.ID, b.ID) })
	encoded, err := json.MarshalIndent(records, "", " ")
	if err != nil {
		t.Fatalf("encode the requests: %v", err)
	}
	return string(encoded)
}

// routeLineOf is a router's line for a call, as the watcher hands it to the store.
func routeLineOf(ts int64, requestID string) shared.RequestRecord {
	return makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.Ts, r.Model, r.AgentID = "route:s1:main:glm-5.3:"+requestID, requestID, ts, "glm-5.3", "main"
		r.Route, r.Via, r.Status, r.LatencyMs = str("provider"), str("zai"), str("200"), i64(1_400)
		r.Tokens = shared.Tokens{Input: 900, Output: 210}
	})
}

// copyLineOf is the transcript's copy of a call.
func copyLineOf(ts int64, requestID string) shared.RequestRecord {
	return makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.Ts, r.Model, r.AgentID = requestID, requestID, ts, "glm-5.3", "main"
		r.What = str("→ text")
		r.Tokens = shared.Tokens{Input: 900, Output: 210}
	})
}

// persistedAs is a request as a history file hands it back: the request id is not kept.
func persistedAs(record shared.RequestRecord) shared.RequestRecord {
	record.RequestID = ""
	return record
}

// requestRestart runs one history of request lines cut at a restart: the first held lines were
// stored by the earlier run, and the whole history is read again after the restart.
func requestRestart(t *testing.T, name string, history []shared.RequestRecord, held int) {
	t.Helper()
	cold := newTestStore()
	for _, record := range history {
		cold.AddRequest(record, nil)
	}
	// the earlier run's file holds the calls its own pairing made of the lines it read
	earlier := newTestStore()
	for _, record := range history[:held] {
		earlier.AddRequest(record, nil)
	}
	var rows []shared.RequestRecord
	for _, record := range earlier.RequestsFor(RequestFilter{}) {
		rows = append(rows, persistedAs(record))
	}
	slices.SortFunc(rows, func(a, b shared.RequestRecord) int { return cmp.Compare(b.Ts, a.Ts) })
	warm := warmedWith(t, rows...)
	for _, record := range history {
		warm.AddRequest(record, nil)
	}
	if got, want := callsAfter(t, warm), callsAfter(t, cold); got != want {
		t.Errorf("%s, a restart after %d of %d lines differs from a cold start:\n%s", name, held, len(history), firstDifference(want, got))
	}
}

func TestACallPairsAcrossARestartLikeItDoesWithout(t *testing.T) {
	cases := map[string][]shared.RequestRecord{
		"a transcript copy whose route line came before the restart still pairs into one call after it": {
			routeLineOf(ts2, "req_A"),
			copyLineOf(ts2+300, "req_A"),
		},
		"a route line after the restart whose copy came before it still pairs into one call": {
			copyLineOf(ts2, "req_A"),
			routeLineOf(ts2+300, "req_A"),
		},
		"a copy a request id names pairs with its route line however far apart they are": {
			routeLineOf(ts2, "req_A"),
			copyLineOf(ts2+30_000, "req_A"),
		},
		"a route line a request id names pairs with its copy however far apart they are": {
			copyLineOf(ts2, "req_A"),
			routeLineOf(ts2+30_000, "req_A"),
		},
		"two calls in one window pair each with its own request id": {
			routeLineOf(ts2, "req_A"),
			routeLineOf(ts2+1_000, "req_B"),
			copyLineOf(ts2+1_200, "req_B"),
			copyLineOf(ts2+200, "req_A"),
		},
	}
	for name, history := range cases {
		t.Run(name, func(t *testing.T) {
			for held := 1; held < len(history); held++ {
				requestRestart(t, name, history, held)
			}
		})
	}
}
