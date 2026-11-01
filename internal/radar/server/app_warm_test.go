package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// warmSnapshot is the part of a store a restart must give back: the session cards, the
// request count and the timeline's events, by what they say.
type warmSnapshot struct {
	sessions []string
	requests []string
	events   []string
}

// snapshotOf reads the store the way the dashboard does.
func snapshotOf(app *App) warmSnapshot {
	var snapshot warmSnapshot
	for _, item := range app.Store.SessionList() {
		snapshot.sessions = append(snapshot.sessions, fmt.Sprintf("%s cwd=%v requests=%d agents=%d tokens=%d lastAt=%d",
			item.ID, deref(item.Cwd), item.RequestCount, item.AgentCount, item.Tokens, item.LastAt))
	}
	for _, request := range app.Store.RequestsFor(store.RequestFilter{}) {
		snapshot.requests = append(snapshot.requests, fmt.Sprintf("%s %s %d %v", request.ID, request.Model, request.Ts, request.Tokens))
	}
	limit := int64(1_000)
	for _, event := range app.Store.Events(store.EventsFilter{Limit: &limit}) {
		snapshot.events = append(snapshot.events, fmt.Sprintf("%d %s %s %d", event.Seq, event.Kind, deref(event.SessionID), event.Ts))
	}
	slices.Sort(snapshot.sessions)
	slices.Sort(snapshot.requests)
	slices.Sort(snapshot.events)
	return snapshot
}

func deref(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

// healthStatus asks /api/health for its status code.
func healthStatus(t *testing.T, port int) int {
	t.Helper()
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		t.Fatalf("the health request failed: %v", err)
	}
	_ = response.Body.Close()
	return response.StatusCode
}

// eventRowsOf counts the events the history file holds.
func eventRowsOf(t *testing.T, env domain.EnvLookup) int64 {
	t.Helper()
	db, err := history.OpenDB(paths.HistoryPath(t.Context(), env))
	if err != nil {
		t.Fatalf("open the history file: %v", err)
	}
	defer func() { _ = db.Close() }()
	row, err := db.Get("SELECT COUNT(*) AS c FROM events")
	if err != nil {
		t.Fatalf("count the events: %v", err)
	}
	count, _ := row["c"].(int64)
	return count
}

func TestARestartWarmsTheStoreFromHistory(t *testing.T) {
	world := makeEnv(t)
	now := time.Now().UnixMilli()
	route := fmt.Sprintf(`{"ts":%q,"event":"route","session_id":"s1","agent_id":"main","plugin":"zai",`+
		`"model":"glm-5.3","upstream":"https://api.z.ai/api/anthropic","route":"provider","status":200,`+
		`"latency_ms":900,"usage":{"input_tokens":120,"output_tokens":30}}`, isoOf(now))
	seedSpool(t, world.state,
		spoolStart("s1", "/w/app"), spoolPrompt("s1", "first"), route,
		spoolStart("s2", "/w/other"), spoolPrompt("s2", "second"))
	options := AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs}

	first := mustStart(t, world.env, options)
	waitFor(t, "the first run's pass", 10*time.Second, func() bool { return !first.Watcher.CatchingUp() })
	before := snapshotOf(first)
	if len(before.sessions) != 2 || len(before.requests) != 1 || len(before.events) < 4 {
		t.Fatalf("the first run: got %+v, want two sessions, one request and the prompt events", before)
	}
	first.Stop()
	rowsBefore := eventRowsOf(t, world.env)

	second := mustStart(t, world.env, options)
	defer second.Stop()
	if status := healthStatus(t, second.Port); status != 200 {
		t.Fatalf("/api/health right after the second run started: got %d, want 200", status)
	}
	answered := 0
	for second.Watcher.CatchingUp() {
		if status := healthStatus(t, second.Port); status != 200 {
			t.Fatalf("/api/health while the second run catches up: got %d, want 200", status)
		}
		answered++
	}
	if status := healthStatus(t, second.Port); status != 200 {
		t.Fatalf("/api/health once the second run caught up: got %d, want 200", status)
	}
	after := snapshotOf(second)

	if !slices.Equal(before.sessions, after.sessions) {
		t.Errorf("the session cards after the restart:\n got %s\nwant %s", strings.Join(after.sessions, "\n     "),
			strings.Join(before.sessions, "\n     "))
	}
	if !slices.Equal(before.requests, after.requests) {
		t.Errorf("the requests after the restart:\n got %v\nwant %v", after.requests, before.requests)
	}
	if !slices.Equal(before.events, after.events) {
		t.Errorf("the events after the restart:\n got %v\nwant %v", after.events, before.events)
	}
	second.Stop()
	if rowsAfter := eventRowsOf(t, world.env); rowsAfter != rowsBefore {
		t.Errorf("the history's event rows after the restart: got %d, want the %d it held", rowsAfter, rowsBefore)
	}
	t.Logf("%d health answers while the second run caught up; %d events before the restart, %d after",
		answered, len(before.events), len(after.events))
}
