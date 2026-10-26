package ingest_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

var (
	watchT0  = time.Date(2026, 1, 15, 6, 30, 0, 0, time.UTC).UnixMilli()
	watchT1  = watchT0 + 1_000
	watchDay = int64(86_400_000)
)

// iso renders epoch ms the way the transcripts and spool lines stamp their timestamps.
func iso(ms int64) string { return jsfmt.ISOTime(time.UnixMilli(ms)) }

// jsonl marshals entries to one jsonl document, trailing newline included.
func jsonl(t *testing.T, entries ...any) string {
	t.Helper()
	var document strings.Builder
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		document.Write(data)
		document.WriteByte('\n')
	}
	return document.String()
}

// oneLine is jsonl for a single entry, for the lines an append adds.
func oneLine(t *testing.T, entry any) string {
	t.Helper()
	return jsonl(t, entry)
}

func userLine(ts int64) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": iso(ts), "cwd": "/w/app", "version": "2.0.0",
		"message": map[string]any{"content": "go"},
	}
}

// assistantLine is one assistant entry; the model defaults to claude-sonnet-5-5.
func assistantLine(ts int64, requestID string, model ...string) map[string]any {
	chosen := "claude-sonnet-5-5"
	if len(model) > 0 {
		chosen = model[0]
	}
	return map[string]any{
		"type": "assistant", "timestamp": iso(ts), "requestId": requestID,
		"message": map[string]any{
			"model":       chosen,
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
			"stop_reason": "end_turn",
			"content":     []any{},
		},
	}
}

func spoolFilePath(t *testing.T, env domain.EnvLookup) string {
	t.Helper()
	return filepath.Join(paths.SpoolDir(t.Context(), env), "2026-01-15.jsonl")
}

func todaySpoolPath(t *testing.T, env domain.EnvLookup) string {
	t.Helper()
	return filepath.Join(paths.SpoolDir(t.Context(), env), time.Now().UTC().Format("2006-01-02")+".jsonl")
}

// setMtime pins a file's times, the way utimesSync does.
func setMtime(t *testing.T, path string, ms int64) {
	t.Helper()
	at := time.UnixMilli(ms)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// ownPidAlive counts only this test process as alive: no signal ever leaves it.
func ownPidAlive(pid int64) bool { return pid == int64(os.Getpid()) }

func newStore() *store.Store { return store.CreateStore(time.Now) }

// waitFor polls probe every few milliseconds until it holds or the deadline passes.
func waitFor(t *testing.T, label string, timeout time.Duration, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if probe() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// fakeTicker is a poll clock the test fires by hand: no real time passes between ticks.
type fakeTicker struct {
	ticks chan time.Time
}

func newFakeTicker() *fakeTicker {
	return &fakeTicker{ticks: make(chan time.Time, 16)}
}

// new is the NewTicker the watcher options take.
func (f *fakeTicker) new(time.Duration) (<-chan time.Time, func()) {
	return f.ticks, func() {}
}

// fire delivers one tick; a watcher not reading it drops it, never blocks the test.
func (f *fakeTicker) fire() {
	select {
	case f.ticks <- time.Now():
	default:
	}
}

// A watcher the test drives by hand: the interval never fires, the clock moves only on a tick.
type manualWatch struct {
	watcher *ingest.Watcher
	tick    func(advanceMs int64)
	tracked func() int
}

func manualWatcher(t *testing.T, env domain.EnvLookup, st *store.Store) manualWatch {
	t.Helper()
	var clock atomic.Int64
	clock.Store(watchT0)
	w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env:        env,
		Store:      st,
		SinceMs:    watchDay,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        clock.Load,
		Alive:      ownPidAlive,
	})
	t.Cleanup(w.Stop)
	waitFor(t, "initial ingest", 5*time.Second, func() bool { return !w.CatchingUp() })
	return manualWatch{
		watcher: w,
		tick: func(advanceMs int64) {
			clock.Add(advanceMs)
			w.PollOnce()
			waitFor(t, "ingest completion", 5*time.Second, func() bool { return !w.CatchingUp() })
		},
		tracked: w.Tracked,
	}
}

func sessionFilter(id string) store.RequestFilter { return store.RequestFilter{Session: &id} }

func requestIDs(t *testing.T, st *store.Store, filter store.RequestFilter) []string {
	t.Helper()
	var ids []string
	for _, request := range st.Requests(filter) {
		ids = append(ids, request.ID)
	}
	return ids
}

func sessionIDs(t *testing.T, st *store.Store) []string {
	t.Helper()
	var ids []string
	for _, session := range st.SessionList() {
		ids = append(ids, session.ID)
	}
	return ids
}

func eventKinds(t *testing.T, st *store.Store, limit int64) []string {
	t.Helper()
	var kinds []string
	for _, event := range st.Events(store.EventsFilter{Limit: &limit}) {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

func findRequest(t *testing.T, st *store.Store, id string) *shared.RequestRecord {
	t.Helper()
	all := st.Requests(store.RequestFilter{})
	for i := range all {
		if all[i].ID == id {
			return &all[i]
		}
	}
	t.Fatalf("request %s not found among %v", id, requestIDs(t, st, store.RequestFilter{}))
	return nil
}

func findAgent(t *testing.T, view *shared.SessionView, id string) *shared.AgentView {
	t.Helper()
	for i := range view.Agents {
		if view.Agents[i].ID == id {
			return &view.Agents[i]
		}
	}
	t.Fatalf("agent %s not found in session %s", id, view.ID)
	return nil
}

// writeJobDir makes a job dir under one provider's state dir; the state dir defaults to the test
// home.
func writeJobDir(t *testing.T, env domain.EnvLookup, home, plugin, id string, job any, attempts ...string) string {
	t.Helper()
	root := filepath.Join(home, plugin)
	if dir, ok := env[strings.ToUpper(plugin)+"_STATE_DIR"]; ok {
		root = dir
	}
	dir := filepath.Join(root, "jobs", id)
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	writeText(t, filepath.Join(dir, "job.json"), string(data))
	for _, name := range attempts {
		writeText(t, filepath.Join(dir, name), "")
	}
	return dir
}

// registryEnv is a temp Claude config dir with an empty sessions/ registry beside its projects/.
func registryEnv(t *testing.T) (testEnv, string) {
	t.Helper()
	e := makeEnv(t)
	sessions := filepath.Join(e.config, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", sessions, err)
	}
	return e, sessions
}

func TestSpoolIngestion(t *testing.T) {
	t.Run("replays the spool on start and picks up only new lines on later polls", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, spoolFilePath(t, e.env), jsonl(t,
			map[string]any{"ts": iso(watchT0), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "a"},
			map[string]any{"ts": iso(watchT1), "event": "Notification"},
		))
		st := newStore()
		w := manualWatcher(t, e.env, st)
		if got := eventKinds(t, st, 10); !slices.Equal(got, []string{"Notification", "UserPromptSubmit"}) {
			t.Errorf("event kinds = %v, want [Notification UserPromptSubmit]", got)
		}
		if got := sessionIDs(t, st); !slices.Equal(got, []string{"s1"}) {
			t.Errorf("session ids = %v, want [s1]", got)
		}
		appendText(t, path, oneLine(t, map[string]any{
			"ts": iso(watchT1), "event": "Stop", "session_id": "s1", "reason": "end_turn",
		}))
		w.tick(1_000)
		if got := eventKinds(t, st, 10); !slices.Equal(got, []string{"Stop", "Notification", "UserPromptSubmit"}) {
			t.Errorf("event kinds after append = %v, want [Stop Notification UserPromptSubmit]", got)
		}
	})

	t.Run("turns route events into requests and drops malformed ones", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t,
			map[string]any{
				"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
				"upstream": "http://127.0.0.1:8787", "status": "ok", "latency_ms": 42,
				"usage": map[string]any{
					"input_tokens": 3, "output_tokens": 4,
					"cache_read_input_tokens": 5, "cache_creation_input_tokens": 6,
				},
			},
			map[string]any{"ts": iso(watchT1), "event": "route", "session_id": "s9"},
			map[string]any{"ts": "nope", "event": "route", "session_id": "s9", "model": "glm-5.3"},
			map[string]any{"ts": iso(watchT1), "event": "route", "model": "glm-5.3"},
			map[string]any{"ts": iso(watchT1), "event": "route", "session_id": "s10", "model": "kimi-k2"},
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		routed := st.Requests(sessionFilter("s9"))
		if len(routed) != 1 {
			t.Fatalf("routed requests = %+v, want one", routed)
		}
		want := shared.RequestRecord{
			ID:         fmt.Sprintf("route:s9::glm-5.3:%d:ok", watchT1),
			SessionID:  "s9",
			AgentID:    "main",
			Model:      "glm-5.3",
			Upstream:   "http://127.0.0.1:8787",
			Ts:         watchT1,
			LatencyMs:  ptr(int64(42)),
			Tokens:     shared.Tokens{Input: 3, Output: 4, CacheRead: 5, CacheWrite: 6},
			StopReason: ptr("ok"),
			Provider:   "Z.ai",
			Route:      ptr("router"),
			Via:        ptr("router"),
			Status:     ptr("ok"),
			TailKeys:   []string{"status", "route", "via"},
		}
		if !reflect.DeepEqual(routed[0], want) {
			t.Errorf("routed request = %+v, want %+v", routed[0], want)
		}
		other := st.Requests(sessionFilter("s10"))
		if len(other) != 1 {
			t.Fatalf("s10 requests = %+v, want one", other)
		}
		if other[0].Upstream != shared.DefaultUpstream || other[0].LatencyMs != nil || other[0].StopReason != nil {
			t.Errorf("s10 request = %+v, want the default upstream and no latency or stop reason", other[0])
		}
		// a router request no Claude Code session sent still counts, outside any session
		outside := st.Requests(sessionFilter(shared.OutsideSession))
		if len(outside) != 1 || outside[0].Model != "glm-5.3" || outside[0].AgentID != "main" ||
			outside[0].Provider != "Z.ai" {
			t.Errorf("outside-session requests = %+v, want one glm-5.3 call under main from Z.ai", outside)
		}
		if got := eventKinds(t, st, 100); len(got) != 0 {
			t.Errorf("events = %v, want none", got)
		}
	})

	t.Run("keeps the extra usage a route line carries: thinking tokens and billing conditions", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
			"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
			"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200, "latency_ms": 42,
			"usage": map[string]any{
				"input_tokens": 3, "output_tokens": 400,
				"cache_read_input_tokens": 5, "cache_creation_input_tokens": 6,
				"cache_creation":        map[string]any{"ephemeral_1h_input_tokens": 6, "ephemeral_5m_input_tokens": 0},
				"output_tokens_details": map[string]any{"thinking_tokens": 120, "rejected_tokens": 0},
				"speed":                 "fast", "inference_geo": "us", "service_tier": "priority",
			},
		}))
		st := newStore()
		manualWatcher(t, e.env, st)
		first := st.Requests(sessionFilter("s9"))
		if len(first) != 1 {
			t.Fatalf("routed requests = %+v, want one", first)
		}
		got := first[0]
		if got.Tokens.Input != 3 || got.Tokens.Output != 400 || got.Tokens.CacheRead != 5 || got.Tokens.CacheWrite != 6 {
			t.Errorf("tokens = %+v, want the four counters 3/400/5/6", got.Tokens)
		}
		if got.Tokens.Thinking == nil || *got.Tokens.Thinking != 120 {
			t.Errorf("tokens = %+v, want thinking 120", got.Tokens)
		}
		if got.CacheWrite1h == nil || *got.CacheWrite1h != 6 {
			t.Errorf("cacheWrite1h = %v, want 6", got.CacheWrite1h)
		}
		if got.Speed == nil || *got.Speed != "fast" {
			t.Errorf("speed = %v, want fast", got.Speed)
		}
		if got.Geo == nil || *got.Geo != "us" {
			t.Errorf("geo = %v, want us", got.Geo)
		}
		if got.ServiceTier == nil || *got.ServiceTier != "priority" {
			t.Errorf("serviceTier = %v, want priority", got.ServiceTier)
		}
	})

	t.Run("carries a route line's error reason onto the failed request, and none onto a success", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t,
			map[string]any{
				"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
				"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 502,
				"error": "connection failed (ECONNREFUSED)", "latency_ms": 12,
			},
			map[string]any{
				"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
				"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200, "latency_ms": 12,
			},
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		routed := st.Requests(sessionFilter("s9"))
		var failed, succeeded *shared.RequestRecord
		for i := range routed {
			if routed[i].StopReason != nil && *routed[i].StopReason == "502" {
				failed = &routed[i]
			}
			if routed[i].StopReason == nil {
				succeeded = &routed[i]
			}
		}
		if failed == nil {
			t.Fatalf("no failed request among %+v", routed)
		}
		if failed.Error == nil || *failed.Error != "connection failed (ECONNREFUSED)" {
			t.Errorf("failed error = %v, want the connection failure", failed.Error)
		}
		if succeeded == nil {
			t.Fatalf("no successful request among %+v", routed)
		}
		if succeeded.Error != nil {
			t.Errorf("success error = %v, want none", succeeded.Error)
		}
	})

	t.Run("reads the router's pass-through rows: metadata only, a cut stream as a failure", func(t *testing.T) {
		e := makeEnv(t)
		cut := "stream cut (INTERNAL_ERROR)"
		session := "s9"
		writeText(t, spoolFilePath(t, e.env), jsonl(t,
			domain.RouteEvent{
				Ts: iso(watchT0), Event: domain.EventRoute, Plugin: "huddle-router", Upstream: "api.anthropic.com",
				Route: "anthropic-pass", Status: 200, LatencyMs: 8, SessionID: &session,
			},
			domain.RouteEvent{
				Ts: iso(watchT1), Event: domain.EventRoute, Plugin: "huddle-router", Upstream: "api.anthropic.com",
				Route: "anthropic-pass", Status: 502, LatencyMs: 30, Error: &cut, SessionID: &session,
			},
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		routed := st.Requests(sessionFilter("s9"))
		if len(routed) != 2 {
			t.Fatalf("pass-through requests = %+v, want two", routed)
		}
		for _, row := range routed {
			if row.Route == nil || *row.Route != "anthropic-pass" || row.Upstream != "api.anthropic.com" || row.Model != "" {
				t.Errorf("pass-through row = %+v, want a model-less anthropic-pass row from api.anthropic.com", row)
			}
		}
		failed, answered := routed[0], routed[1]
		if failed.Status == nil || *failed.Status != "502" || failed.Error == nil || *failed.Error != cut ||
			failed.LatencyMs == nil || *failed.LatencyMs != 30 {
			t.Errorf("cut row = %+v, want 502 with the cut's reason after 30 ms", failed)
		}
		if answered.Status == nil || *answered.Status != "200" || answered.Error != nil {
			t.Errorf("answered row = %+v, want 200 and no error", answered)
		}
		errors := st.APIErrors()
		if len(errors) != 1 || errors[0].Status != 502 || errors[0].Source != shared.SourceRoute {
			t.Errorf("api errors = %+v, want the cut as one route 502", errors)
		}
	})

	t.Run("carries the route line's status onto the request, success or failure", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t,
			map[string]any{
				"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
				"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200, "latency_ms": 12,
			},
			map[string]any{
				"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
				"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 429, "latency_ms": 12,
			},
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		routed := st.Requests(sessionFilter("s9"))
		var ok, limited bool
		for i := range routed {
			if routed[i].Status != nil && *routed[i].Status == "200" {
				ok = true
			}
			if routed[i].Status != nil && *routed[i].Status == "429" {
				limited = true
			}
		}
		if !ok || !limited {
			t.Fatalf("statuses of %+v, want one 200 and one 429", routed)
		}
		// the success keeps its empty stop reason: the status is the wire's answer, not the model's
		for i := range routed {
			if routed[i].Status != nil && *routed[i].Status == "200" && routed[i].StopReason != nil {
				t.Errorf("200 request stop reason = %q, want none", *routed[i].StopReason)
			}
		}
	})

	t.Run("keeps same-millisecond completions of parallel subagents as separate records, under their parent", func(t *testing.T) {
		e := makeEnv(t)
		route := func(agentID, parent string) map[string]any {
			return map[string]any{
				"ts": iso(watchT1), "event": "route", "session_id": "s9", "agent_id": agentID,
				"parent_agent_id": parent, "model": "glm-5.3", "upstream": "http://127.0.0.1:8787",
				"route": "provider", "status": 200, "latency_ms": 900,
			}
		}
		writeText(t, spoolFilePath(t, e.env), jsonl(t, route("agent-w1", "a31cd"), route("agent-w2", "a31cd")))
		st := newStore()
		manualWatcher(t, e.env, st)
		routed := st.Requests(sessionFilter("s9"))
		if len(routed) != 2 {
			t.Fatalf("routed requests = %+v, want two", routed)
		}
		ids := requestIDs(t, st, sessionFilter("s9"))
		slices.Sort(ids)
		want := []string{
			fmt.Sprintf("route:s9:agent-w1:glm-5.3:%d:200", watchT1),
			fmt.Sprintf("route:s9:agent-w2:glm-5.3:%d:200", watchT1),
		}
		if !slices.Equal(ids, want) {
			t.Errorf("request ids = %v, want %v", ids, want)
		}
		for _, request := range routed {
			if request.ParentAgentID == nil || *request.ParentAgentID != "a31cd" {
				t.Errorf("request %s parent = %v, want a31cd", request.ID, request.ParentAgentID)
			}
		}
	})

	t.Run("keeps the model a route line fell back from, and prints it before the model", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
			"ts": iso(watchT1), "event": "route", "plugin": "zai", "session_id": "s9", "agent_id": "a1",
			"model": "glm-5.3-flash", "fallback": "glm-5.3", "upstream": "api.z.ai",
			"route": "provider", "status": 200, "latency_ms": 900,
		}))
		st := newStore()
		manualWatcher(t, e.env, st)
		routed := st.Requests(sessionFilter("s9"))
		if len(routed) != 1 {
			t.Fatalf("routed requests = %+v, want one", routed)
		}
		if routed[0].Fallback == nil || *routed[0].Fallback != "glm-5.3" || routed[0].Model != "glm-5.3-flash" {
			t.Fatalf("request = model %q fallback %v, want glm-5.3-flash from glm-5.3", routed[0].Model, routed[0].Fallback)
		}
		printed, err := json.Marshal(routed[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(printed), `"agentId":"a1","fallback":"glm-5.3","model":"glm-5.3-flash"`) {
			t.Errorf("printed = %s, want fallback between agentId and model", printed)
		}
	})

	t.Run("reads a success status as no stop reason and names an unknown model after its plugin", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
			"ts": iso(watchT1), "event": "route", "plugin": "kimi", "route": "provider",
			"model": "moonshot-preview", "upstream": "api.moonshot.ai", "status": 200,
			"agent_id": "a7", "session_id": "s1",
		}))
		st := newStore()
		manualWatcher(t, e.env, st)
		all := st.Requests(store.RequestFilter{})
		if len(all) != 1 {
			t.Fatalf("requests = %+v, want one", all)
		}
		if all[0].AgentID != "a7" || all[0].Provider != "Kimi" || all[0].StopReason != nil {
			t.Errorf("request = %+v, want agent a7, provider Kimi and no stop reason", all[0])
		}
		if all[0].Route == nil || *all[0].Route != "provider" {
			t.Errorf("route = %v, want provider", all[0].Route)
		}
	})

	t.Run("links a route line's prompt hash and headers, and stores each capture once in the history", func(t *testing.T) {
		e := makeEnv(t)
		hash := strings.Repeat("a", 64)
		payload, err := json.Marshal(struct {
			System string                  `json:"system"`
			Tools  []struct{ Name string } `json:"tools"`
		}{System: "be brief", Tools: []struct{ Name string }{{Name: "Bash"}}})
		if err != nil {
			t.Fatalf("marshal capture payload: %v", err)
		}
		var packed bytes.Buffer
		gzWriter := gzip.NewWriter(&packed)
		if _, err := gzWriter.Write(payload); err != nil {
			t.Fatalf("gzip capture payload: %v", err)
		}
		if err := gzWriter.Close(); err != nil {
			t.Fatalf("close gzip: %v", err)
		}
		gz := base64.StdEncoding.EncodeToString(packed.Bytes())
		route := map[string]any{
			"ts": iso(watchT1), "event": "route", "session_id": "s9", "model": "glm-5.3",
			"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200, "latency_ms": 42,
			"prompt_hash": hash,
			"headers":     map[string]any{"request-id": "req_1", "retry-after": "7", "set-cookie": "nope"},
		}
		writeText(t, spoolFilePath(t, e.env), jsonl(t,
			route,
			map[string]any{"ts": iso(watchT0), "event": "capture", "plugin": "zai", "prompt_hash": hash, "gz": gz},
		))
		st := newStore()
		var captures []shared.CaptureRecord
		st.OnUpdate(func(change store.Change) {
			captures = slices.Concat(captures, change.Captures)
		})
		w := manualWatcher(t, e.env, st)

		// the request carries the hash and only the allow-listed headers its line named
		routed := findRequest(t, st, fmt.Sprintf("route:s9::glm-5.3:%d:200", watchT1))
		if routed.PromptHash == nil || *routed.PromptHash != hash {
			t.Errorf("prompt hash = %v, want %s", routed.PromptHash, hash)
		}
		wantHeaders := &shared.Headers{{Name: "request-id", Value: "req_1"}, {Name: "retry-after", Value: "7"}}
		if !reflect.DeepEqual(routed.Headers, wantHeaders) {
			t.Errorf("headers = %v, want %v", routed.Headers, wantHeaders)
		}
		// the request-id header is the key the store pairs the transcript's copy on
		if routed.RequestID != "req_1" {
			t.Errorf("request id = %q, want req_1 from the request-id header", routed.RequestID)
		}
		// the capture reaches the history writer's feed once per sighting, bytes whole
		if len(captures) != 1 || captures[0] != (shared.CaptureRecord{Hash: hash, Gz: gz, FirstTs: watchT0}) {
			t.Errorf("captures = %+v, want the one prompt under its first sighting", captures)
		}

		// the same capture line again (a second router saw the prompt, say) passes through with
		// its own sighting time; the first row is the writer's to keep
		appendText(t, spoolFilePath(t, e.env), oneLine(t, map[string]any{
			"ts": iso(watchT1), "event": "capture", "plugin": "zai", "prompt_hash": hash, "gz": gz,
		}))
		w.tick(1_000)
		if len(captures) != 2 || captures[1].FirstTs != watchT1 {
			t.Errorf("captures after the repeat = %+v, want the second sighting at T1", captures)
		}
	})
}

func TestWatcherTranscripts(t *testing.T) {
	t.Run("discovers recent project files, honours the since window and tails spool-referenced paths", func(t *testing.T) {
		e := makeEnv(t)
		projects := filepath.Join(e.config, "projects", "-w-app")
		writeText(t, filepath.Join(projects, "sess-a.jsonl"), jsonl(t, userLine(watchT0), assistantLine(watchT1, "req-a")))
		stale := writeText(t, filepath.Join(projects, "stale.jsonl"), jsonl(t, assistantLine(watchT1, "req-stale")))
		setMtime(t, stale, watchT0-10*watchDay)
		referenced := writeText(t, filepath.Join(e.home, "elsewhere", "referenced.jsonl"), jsonl(t, assistantLine(watchT1, "req-ref")))
		setMtime(t, referenced, watchT0-10*watchDay)
		writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
			"ts": iso(watchT0), "event": "SessionStart", "session_id": "sess-a", "source": "startup",
			"base_url": "http://127.0.0.1:8787", "transcript_path": referenced,
		}))
		st := newStore()
		manualWatcher(t, e.env, st)
		ids := requestIDs(t, st, store.RequestFilter{})
		if !slices.Contains(ids, "req-a") {
			t.Errorf("requests = %v, want req-a", ids)
		}
		if !slices.Contains(ids, "req-ref") {
			t.Errorf("requests = %v, want req-ref: referenced paths bypass the mtime window", ids)
		}
		if slices.Contains(ids, "req-stale") {
			t.Errorf("requests = %v, want req-stale left out: unreferenced stale ones are", ids)
		}
		reqA := findRequest(t, st, "req-a")
		if reqA.SessionID != "sess-a" || reqA.AgentID != "main" || reqA.Model != "claude-sonnet-5-5" {
			t.Errorf("req-a = %+v, want sess-a under main on claude-sonnet-5-5", reqA)
		}
		if reqA.LatencyMs == nil || *reqA.LatencyMs != 1_000 {
			t.Errorf("req-a latency = %v, want 1000", reqA.LatencyMs)
		}
		// the local router passes claude-* straight on to Anthropic
		if reqA.Upstream != shared.DefaultUpstream || reqA.Via == nil || *reqA.Via != "127.0.0.1:8787" ||
			reqA.Provider != "Anthropic" {
			t.Errorf("req-a = %+v, want Anthropic via 127.0.0.1:8787 at the default upstream", reqA)
		}
		reqRef := findRequest(t, st, "req-ref")
		if reqRef.SessionID != "referenced" || reqRef.Upstream != shared.DefaultUpstream {
			// no SessionStart taught us this session's base URL: Claude Code called Anthropic itself
			t.Errorf("req-ref = %+v, want session referenced at the default upstream", reqRef)
		}
		detail := st.SessionDetail("sess-a")
		if detail == nil {
			t.Fatalf("session sess-a not found")
		}
		if detail.Cwd == nil || *detail.Cwd != "/w/app" || detail.Project == nil || *detail.Project != "app" ||
			detail.CcVersion == nil || *detail.CcVersion != "2.0.0" {
			t.Errorf("session detail = %+v, want cwd /w/app, project app, ccVersion 2.0.0", detail)
		}
	})

	t.Run("reads the walked transcripts newest-modified first, so a cold start is useful within seconds", func(t *testing.T) {
		e := makeEnv(t)
		projects := filepath.Join(e.config, "projects")
		live := filepath.Join(projects, "-w-live")
		archive := filepath.Join(projects, "-w-archive")
		liveNow := writeText(t, filepath.Join(live, "now.jsonl"), jsonl(t, assistantLine(watchT1, "req-now")))
		livePast := writeText(t, filepath.Join(live, "past.jsonl"), jsonl(t, assistantLine(watchT1, "req-past")))
		cold := writeText(t, filepath.Join(archive, "cold.jsonl"), jsonl(t, assistantLine(watchT1, "req-cold")))
		at := func(agoMs int64) time.Time { return time.UnixMilli(watchT0 - agoMs) }
		for _, pin := range []struct {
			path string
			ago  int64
		}{{liveNow, 60_000}, {livePast, 2 * 3_600_000}, {cold, 90_000}} {
			if err := os.Chtimes(pin.path, at(pin.ago), at(pin.ago)); err != nil {
				t.Fatalf("chtimes %s: %v", pin.path, err)
			}
		}
		// directories too: the fresh project is entered first
		for _, dir := range []struct {
			path string
			ago  int64
		}{{live, 60_000}, {archive, 2 * 3_600_000}} {
			if err := os.Chtimes(dir.path, at(dir.ago), at(dir.ago)); err != nil {
				t.Fatalf("chtimes %s: %v", dir.path, err)
			}
		}
		st := newStore()
		var order []string
		st.OnUpdate(func(change store.Change) {
			for _, request := range change.Requests {
				order = append(order, request.ID)
			}
		})
		manualWatcher(t, e.env, st)
		if !slices.Equal(order, []string{"req-now", "req-past", "req-cold"}) {
			t.Errorf("request order = %v, want [req-now req-past req-cold]", order)
		}
	})

	t.Run("gives each request the base URL in effect when it was made, not the session's latest", func(t *testing.T) {
		e := makeEnv(t)
		projects := filepath.Join(e.config, "projects", "-w-app")
		writeText(t, filepath.Join(projects, "sess-r.jsonl"), jsonl(t,
			assistantLine(watchT0-60_000, "req-before"), // older than every hook report
			assistantLine(watchT0+1_000, "req-direct"),
			assistantLine(watchT0+61_000, "req-routed"),
		))
		hook := func(ts int64, baseURL string) map[string]any {
			return map[string]any{
				"ts": iso(ts), "event": "UserPromptSubmit", "session_id": "sess-r", "base_url": baseURL,
			}
		}
		writeText(t, spoolFilePath(t, e.env), jsonl(t,
			hook(watchT0, "https://api.anthropic.com"),
			hook(watchT0+30_000, "https://api.anthropic.com"), // a repeat changes nothing
			hook(watchT0+60_000, "http://127.0.0.1:17787"),    // the router was set up mid-session
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		upstream := func(id string) string {
			request := findRequest(t, st, id)
			return request.Upstream
		}
		// no base URL in effect yet: Claude Code called Anthropic itself
		if got := upstream("req-before"); got != shared.DefaultUpstream {
			t.Errorf("req-before upstream = %q, want %q", got, shared.DefaultUpstream)
		}
		if got := upstream("req-direct"); got != "https://api.anthropic.com" {
			t.Errorf("req-direct upstream = %q, want https://api.anthropic.com", got)
		}
		// the local router passes claude-* straight on to Anthropic
		if got := upstream("req-routed"); got != shared.DefaultUpstream {
			t.Errorf("req-routed upstream = %q, want %q", got, shared.DefaultUpstream)
		}
		if via := findRequest(t, st, "req-routed").Via; via == nil || *via != "127.0.0.1:17787" {
			t.Errorf("req-routed via = %v, want 127.0.0.1:17787", via)
		}
	})

	t.Run("finds new transcripts only on the rescan cadence, never mid-window", func(t *testing.T) {
		e := makeEnv(t)
		projects := filepath.Join(e.config, "projects", "p")
		writeText(t, filepath.Join(projects, "a.jsonl"), jsonl(t, assistantLine(watchT1, "req-a")))
		st := newStore()
		w := manualWatcher(t, e.env, st)
		if got := requestIDs(t, st, store.RequestFilter{}); len(got) != 1 {
			t.Fatalf("requests = %v, want one", got)
		}
		writeText(t, filepath.Join(projects, "b.jsonl"), jsonl(t, assistantLine(watchT1, "req-b")))
		w.tick(1_000) // inside the 5s window: no rescan, no discovery
		if got := requestIDs(t, st, store.RequestFilter{}); !slices.Equal(got, []string{"req-a"}) {
			t.Errorf("requests after a mid-window tick = %v, want [req-a]", got)
		}
		w.tick(5_000) // past the window: rescan picks the new file up
		ids := requestIDs(t, st, store.RequestFilter{})
		if len(ids) != 2 || !slices.Contains(ids, "req-b") {
			t.Errorf("requests after the rescan = %v, want req-a and req-b", ids)
		}
	})

	// A folder whose modified time stands keeps the listing an earlier walk read: these pin
	// what a kept listing still finds.
	settled := func(t *testing.T, dir string) {
		t.Helper()
		hourAgo := time.Now().Add(-time.Hour)
		if err := os.Chtimes(dir, hourAgo, hourAgo); err != nil {
			t.Fatalf("chtimes %s: %v", dir, err)
		}
	}

	t.Run("finds a transcript added to a folder an earlier rescan listed", func(t *testing.T) {
		e := makeEnv(t)
		projects := filepath.Join(e.config, "projects", "p")
		writeText(t, filepath.Join(projects, "a.jsonl"), jsonl(t, assistantLine(watchT1, "req-a")))
		settled(t, projects)
		st := newStore()
		w := manualWatcher(t, e.env, st)
		w.tick(5_000) // this rescan walks the folder from its kept listing
		writeText(t, filepath.Join(projects, "b.jsonl"), jsonl(t, assistantLine(watchT1, "req-b")))
		w.tick(5_000)
		ids := requestIDs(t, st, store.RequestFilter{})
		if len(ids) != 2 || !slices.Contains(ids, "req-b") {
			t.Errorf("requests after the rescan = %v, want req-a and req-b", ids)
		}
	})

	t.Run("finds a stale transcript that grows again in a folder an earlier rescan listed", func(t *testing.T) {
		e := makeEnv(t)
		projects := filepath.Join(e.config, "projects", "p")
		stale := writeText(t, filepath.Join(projects, "old.jsonl"), jsonl(t, assistantLine(watchT1, "req-old")))
		setMtime(t, stale, watchT0-10*watchDay)
		settled(t, projects)
		st := newStore()
		w := manualWatcher(t, e.env, st)
		w.tick(5_000)
		if got := requestIDs(t, st, store.RequestFilter{}); len(got) != 0 {
			t.Fatalf("requests = %v, want none: the stale transcript is outside the window", got)
		}
		// an append moves the file's modified time, never its folder's
		file, err := os.OpenFile(stale, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(jsonl(t, assistantLine(watchT1+1_000, "req-new"))); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		settled(t, projects)
		w.tick(5_000)
		if ids := requestIDs(t, st, store.RequestFilter{}); !slices.Contains(ids, "req-new") {
			t.Errorf("requests after the rescan = %v, want req-new", ids)
		}
	})
}

func TestClaudeUpstreamEnrichment(t *testing.T) {
	// watched is one session `name` whose call went out at T1, the hook reporting baseURL at T0.
	watched := func(t *testing.T, name, model string, baseURL *string) *store.Store {
		e := makeEnv(t)
		writeText(t, filepath.Join(e.config, "projects", "-w-app", name+".jsonl"),
			jsonl(t, assistantLine(watchT1, "req-"+name, model)))
		if baseURL != nil {
			writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
				"ts": iso(watchT0), "event": "UserPromptSubmit", "session_id": name, "base_url": *baseURL,
			}))
		}
		st := newStore()
		manualWatcher(t, e.env, st)
		return st
	}

	t.Run("went to Anthropic through the local router, which the request names as via", func(t *testing.T) {
		st := watched(t, "s1", "claude-sonnet-5-5", ptr("http://127.0.0.1:17787"))
		all := st.Requests(store.RequestFilter{})
		if len(all) != 1 {
			t.Fatalf("requests = %+v, want one", all)
		}
		if all[0].Upstream != shared.DefaultUpstream || all[0].Provider != "Anthropic" {
			t.Errorf("request = %+v, want Anthropic at the default upstream", all[0])
		}
		if all[0].Via == nil || *all[0].Via != "127.0.0.1:17787" {
			t.Errorf("via = %v, want 127.0.0.1:17787", all[0].Via)
		}
	})

	t.Run("went to Anthropic from Claude Code itself when there is no base URL at all", func(t *testing.T) {
		st := watched(t, "s2", "claude-sonnet-5-5", nil)
		all := st.Requests(store.RequestFilter{})
		if len(all) != 1 {
			t.Fatalf("requests = %+v, want one", all)
		}
		if all[0].Upstream != shared.DefaultUpstream || all[0].Provider != "Anthropic" {
			t.Errorf("request = %+v, want Anthropic at the default upstream", all[0])
		}
		if all[0].Via != nil {
			t.Errorf("via = %v, want none", all[0].Via)
		}
	})

	t.Run("names a provider model after its own offering until a route line says where it went", func(t *testing.T) {
		st := watched(t, "s3", "glm-5.3", ptr("http://127.0.0.1:17787"))
		all := st.Requests(store.RequestFilter{})
		if len(all) != 1 {
			t.Fatalf("requests = %+v, want one", all)
		}
		if all[0].Upstream != "https://api.z.ai" || all[0].Provider != "Z.ai" {
			t.Errorf("request = %+v, want Z.ai at its own offering", all[0])
		}
		if all[0].Via == nil || *all[0].Via != "127.0.0.1:17787" {
			t.Errorf("via = %v, want 127.0.0.1:17787", all[0].Via)
		}
	})

	t.Run("leaves a model no offering serves unnamed", func(t *testing.T) {
		st := watched(t, "s5", "totally-unknown-model", ptr("http://127.0.0.1:17787"))
		all := st.Requests(store.RequestFilter{})
		if len(all) != 1 {
			t.Fatalf("requests = %+v, want one", all)
		}
		if all[0].Upstream != "" || all[0].Provider != "other" {
			t.Errorf("request = %+v, want an empty upstream and provider other", all[0])
		}
		if all[0].Via == nil || *all[0].Via != "127.0.0.1:17787" {
			t.Errorf("via = %v, want 127.0.0.1:17787", all[0].Via)
		}
	})

	t.Run("keeps an explicit https base URL as the upstream", func(t *testing.T) {
		st := watched(t, "s4", "claude-sonnet-5-5", ptr("https://proxy.example.com"))
		all := st.Requests(store.RequestFilter{})
		if len(all) != 1 {
			t.Fatalf("requests = %+v, want one", all)
		}
		if all[0].Upstream != "https://proxy.example.com" {
			t.Errorf("upstream = %q, want the proxy", all[0].Upstream)
		}
		if all[0].Via != nil {
			t.Errorf("via = %v, want none", all[0].Via)
		}
	})
}

func TestWatcherProviderJobs(t *testing.T) {
	t.Run("surfaces external agents, tails their attempts and retires finished jobs", func(t *testing.T) {
		e := makeEnv(t)
		j1 := filepath.Join(e.zai, "jobs", "j1")
		writeText(t, filepath.Join(j1, "job.json"),
			`{"brief":{"title":"Fix tests","model":"glm-5.3"},"state":"running","updatedAt":"2030-01-01T00:00:00Z"}`)
		writeText(t, filepath.Join(j1, "attempt-1.jsonl"), jsonl(t,
			map[string]any{"type": "user", "timestamp": iso(watchT0), "message": map[string]any{"content": "go"}},
			map[string]any{
				"type": "assistant", "timestamp": iso(watchT1), "requestId": "zreq-1",
				"message": map[string]any{
					"model": "glm-5.3", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
					"stop_reason": "end_turn", "content": []any{},
				},
			},
		))
		j2 := filepath.Join(e.zai, "jobs", "j2")
		writeText(t, filepath.Join(j2, "job.json"), `{"state":"failed","updatedAt":"2030-01-01T00:00:00Z"}`)
		writeText(t, filepath.Join(j2, "attempt-1.jsonl"), jsonl(t, assistantLine(watchT1, "zreq-2")))
		st := newStore()
		manualWatcher(t, e.env, st)
		ids := sessionIDs(t, st)
		if !slices.Contains(ids, "zai:j1") || !slices.Contains(ids, "zai:j2") {
			t.Errorf("session ids = %v, want zai:j1 and zai:j2", ids)
		}
		detail1 := st.SessionDetail("zai:j1")
		if detail1 == nil {
			t.Fatalf("session zai:j1 not found")
		}
		agent1 := findAgent(t, detail1, "j1")
		if agent1.ID != "j1" || agent1.Kind != shared.KindExternal || agent1.Name == nil || *agent1.Name != "Fix tests" {
			t.Errorf("agent j1 = %+v, want an external agent named Fix tests", agent1)
		}
		if !agent1.Live {
			t.Errorf("agent j1 live = false, want true")
		}
		// a landed job is no open session: neither its agent nor its session reads as live
		detail2 := st.SessionDetail("zai:j2")
		if detail2 == nil {
			t.Fatalf("session zai:j2 not found")
		}
		if agent2 := findAgent(t, detail2, "j2"); agent2.Live {
			t.Errorf("agent j2 live = true, want false")
		}
		endedAt := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		if detail2.Live || detail2.EndedAt == nil || *detail2.EndedAt != endedAt {
			t.Errorf("session zai:j2 = %+v, want ended at %d", detail2, endedAt)
		}
		requests := st.Requests(sessionFilter("zai:j1"))
		if len(requests) != 1 {
			t.Fatalf("zai:j1 requests = %+v, want one", requests)
		}
		if requests[0].ID != "zreq-1" || requests[0].AgentID != "j1" || requests[0].Model != "glm-5.3" ||
			requests[0].Provider != "Z.ai" {
			t.Errorf("zai:j1 request = %+v, want zreq-1 under j1 on glm-5.3 from Z.ai", requests[0])
		}
		if requests[0].LatencyMs == nil || *requests[0].LatencyMs != 1_000 {
			t.Errorf("zai:j1 latency = %v, want 1000", requests[0].LatencyMs)
		}
	})

	t.Run("tails an attempt's real transcript as one job session and skips the attempt file", func(t *testing.T) {
		e := makeEnv(t)
		writeJobDir(t, e.env, "", "zai", "j1", map[string]any{
			"brief":     map[string]any{"title": "Fix tests", "model": "glm-5.3"},
			"state":     "running",
			"workspace": map[string]any{"repoRoot": "/w/main", "branch": "zai/j1"},
			"origin":    map[string]any{"sessionId": "sess-0"},
			"attempts": []any{
				map[string]any{"n": 1, "sessionId": "att-1"},
			},
			"updatedAt": "2030-01-01T00:00:00Z",
		})
		writeText(t, filepath.Join(e.zai, "jobs", "j1", "attempt-1.jsonl"),
			jsonl(t, assistantLine(watchT1, "zreq-attempt")))
		transcript := func(id string, ts int64) map[string]any {
			return map[string]any{
				"type": "assistant", "timestamp": iso(ts), "sessionId": "att-1", "requestId": id,
				"message": map[string]any{
					"model": "glm-5.3", "usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
					"stop_reason": "end_turn", "content": []any{},
				},
			}
		}
		writeText(t, filepath.Join(e.zai, "claude-home", "projects", "-w-main", "att-1.jsonl"),
			jsonl(t, transcript("treq-1", watchT0), transcript("treq-2", watchT1)))
		writeText(t, filepath.Join(e.zai, "claude-home", "projects", "-w-main", "att-1", "subagents", "agent-w1.jsonl"),
			jsonl(t, transcript("sreq-1", watchT1)))
		writeText(t, filepath.Join(e.zai, "claude-home", "projects", "-w-main", "att-1", "subagents", "agent-w1.meta.json"),
			`{"agentType":"Explore","description":"find the flaky test"}`)
		st := newStore()
		manualWatcher(t, e.env, st)
		ids := requestIDs(t, st, sessionFilter("zai:j1"))
		// the attempt file's zreq-attempt is not counted twice
		if !slices.Equal(ids, []string{"treq-2", "sreq-1", "treq-1"}) {
			t.Errorf("zai:j1 request ids = %v, want [treq-2 sreq-1 treq-1]", ids)
		}
		first := findRequest(t, st, "treq-1")
		if first.AgentID != "j1" || first.SessionID != "zai:j1" || first.Upstream != ingest.ZaiJobUpstream {
			t.Errorf("treq-1 = %+v, want agent j1 on session zai:j1 at zai's own upstream", first)
		}
		if sub := findRequest(t, st, "sreq-1"); sub.AgentID != "agent-w1" {
			t.Errorf("sreq-1 = %+v, want agent agent-w1", sub)
		}
		detail := st.SessionDetail("zai:j1")
		if detail == nil {
			t.Fatalf("session zai:j1 not found")
		}
		subAgent := findAgent(t, detail, "agent-w1")
		if subAgent.ParentID == nil || *subAgent.ParentID != "j1" || subAgent.Kind != shared.KindSubagent {
			t.Errorf("agent-w1 = %+v, want a subagent under j1", subAgent)
		}
		if subAgent.Name == nil || *subAgent.Name != "Explore — find the flaky test" ||
			subAgent.AgentType == nil || *subAgent.AgentType != "Explore" ||
			subAgent.Description == nil || *subAgent.Description != "find the flaky test" {
			t.Errorf("agent-w1 = %+v, want Explore — find the flaky test with its type and task", subAgent)
		}
		var listed *store.SessionListItem
		for i := range st.SessionList() {
			if st.SessionList()[i].ID == "zai:j1" {
				listed = &st.SessionList()[i]
			}
		}
		if listed == nil {
			t.Fatalf("session zai:j1 not listed")
		}
		if listed.Name == nil || *listed.Name != "Fix tests" || listed.Branch == nil || *listed.Branch != "zai/j1" ||
			listed.Repo == nil || *listed.Repo != "/w/main" ||
			listed.ParentSessionID == nil || *listed.ParentSessionID != "sess-0" {
			t.Errorf("listed session = %+v, want Fix tests on zai/j1 in /w/main from sess-0", listed)
		}
	})

	t.Run("falls back to an attempt file whose transcript never appeared", func(t *testing.T) {
		e := makeEnv(t)
		writeJobDir(t, e.env, "", "zai", "old", map[string]any{
			"attempts":  []any{map[string]any{"n": 1, "sessionId": "att-gone"}},
			"updatedAt": "2030-01-01T00:00:00Z",
		})
		writeText(t, filepath.Join(e.zai, "jobs", "old", "attempt-1.jsonl"),
			jsonl(t, assistantLine(watchT1, "zreq-old")))
		st := newStore()
		manualWatcher(t, e.env, st)
		if got := requestIDs(t, st, sessionFilter("zai:old")); !slices.Equal(got, []string{"zreq-old"}) {
			t.Errorf("zai:old request ids = %v, want [zreq-old]", got)
		}
	})

	t.Run("keeps a transcript-less attempt file even when job.json names no session", func(t *testing.T) {
		e := makeEnv(t)
		writeJobDir(t, e.env, "", "zai", "plain", map[string]any{"updatedAt": "2030-01-01T00:00:00Z"})
		writeText(t, filepath.Join(e.zai, "jobs", "plain", "attempt-1.jsonl"),
			jsonl(t, assistantLine(watchT1, "zreq-plain")))
		st := newStore()
		manualWatcher(t, e.env, st)
		if got := requestIDs(t, st, sessionFilter("zai:plain")); !slices.Equal(got, []string{"zreq-plain"}) {
			t.Errorf("zai:plain request ids = %v, want [zreq-plain]", got)
		}
	})

	t.Run("sends a plugin's jobs where its router sent that plugin's models, and nowhere else", func(t *testing.T) {
		e := makeEnv(t)
		e.env["KIMI_STATE_DIR"] = filepath.Join(e.home, "kimi")
		e.env["QWEN_STATE_DIR"] = filepath.Join(e.home, "qwen")
		writeText(t, spoolFilePath(t, e.env), jsonl(t,
			map[string]any{
				"ts": iso(watchT0), "event": "route", "plugin": "kimi", "model": "kimi-k2.5",
				"upstream": "https://api.moonshot.ai/anthropic", "status": 200, "session_id": "s1",
			},
			map[string]any{
				// a passthrough says nothing about where kimi's own models go
				"ts": iso(watchT0), "event": "route", "plugin": "kimi", "model": "claude-sonnet-5-5",
				"upstream": "https://api.anthropic.com", "status": 200, "session_id": "s1",
			},
		))
		writeJobDir(t, e.env, e.home, "kimi", "k1",
			map[string]any{"attempts": []any{map[string]any{"n": 1}}, "updatedAt": "2030-01-01T00:00:00Z"})
		writeJobDir(t, e.env, e.home, "qwen", "q1",
			map[string]any{"attempts": []any{map[string]any{"n": 1}}, "updatedAt": "2030-01-01T00:00:00Z"})
		writeText(t, filepath.Join(e.home, "kimi", "jobs", "k1", "attempt-1.jsonl"),
			jsonl(t, assistantLine(watchT1, "kreq-1", "kimi-k2.5")))
		writeText(t, filepath.Join(e.home, "qwen", "jobs", "q1", "attempt-1.jsonl"),
			jsonl(t, assistantLine(watchT1, "qreq-1", "qwen3.8-max")))
		st := newStore()
		manualWatcher(t, e.env, st)
		kimi := st.Requests(sessionFilter("kimi:k1"))
		if len(kimi) != 1 {
			t.Fatalf("kimi:k1 requests = %+v, want one", kimi)
		}
		if kimi[0].Upstream != "https://api.moonshot.ai/anthropic" || kimi[0].Provider != "Moonshot" {
			t.Errorf("kimi:k1 request = %+v, want Moonshot where its router sent it", kimi[0])
		}
		// a model no route line ever placed still names the host its own offering serves
		qwen := st.Requests(sessionFilter("qwen:q1"))
		if len(qwen) != 1 {
			t.Fatalf("qwen:q1 requests = %+v, want one", qwen)
		}
		if qwen[0].Upstream != "https://dashscope-intl.aliyuncs.com" {
			t.Errorf("qwen:q1 upstream = %q, want the dashscope host", qwen[0].Upstream)
		}
	})
}

func TestWatcherSubagentMetaFiles(t *testing.T) {
	t.Run("names a discovered session subagent after its type and carries its task", func(t *testing.T) {
		e := makeEnv(t)
		subagents := filepath.Join(e.config, "projects", "-w-app", "sess-a", "subagents")
		writeText(t, filepath.Join(subagents, "agent-w1.jsonl"), jsonl(t, assistantLine(watchT1, "sreq-1")))
		writeText(t, filepath.Join(subagents, "agent-w1.meta.json"),
			`{"agentType":"Explore","description":"find the flaky test"}`)
		st := newStore()
		manualWatcher(t, e.env, st)
		detail := st.SessionDetail("sess-a")
		if detail == nil {
			t.Fatalf("session sess-a not found")
		}
		agent := findAgent(t, detail, "agent-w1")
		if agent.ParentID == nil || *agent.ParentID != "main" || agent.Kind != shared.KindSubagent {
			t.Errorf("agent-w1 = %+v, want a subagent under main", agent)
		}
		if agent.Name == nil || *agent.Name != "Explore — find the flaky test" ||
			agent.AgentType == nil || *agent.AgentType != "Explore" ||
			agent.Description == nil || *agent.Description != "find the flaky test" {
			t.Errorf("agent-w1 = %+v, want Explore — find the flaky test with its type and task", agent)
		}
	})

	t.Run("does not rename a subagent whose meta file is missing or unusable", func(t *testing.T) {
		e := makeEnv(t)
		subagents := filepath.Join(e.config, "projects", "-w-app", "sess-b", "subagents")
		writeText(t, filepath.Join(subagents, "agent-w2.jsonl"), jsonl(t, assistantLine(watchT1, "sreq-2")))
		writeText(t, filepath.Join(subagents, "agent-w3.jsonl"), jsonl(t, assistantLine(watchT1, "sreq-3")))
		writeText(t, filepath.Join(subagents, "agent-w3.meta.json"), `{"agentType":42}`)
		st := newStore()
		manualWatcher(t, e.env, st)
		detail := st.SessionDetail("sess-b")
		if detail == nil {
			t.Fatalf("session sess-b not found")
		}
		var names []string
		for _, agent := range detail.Agents {
			if agent.Name != nil {
				names = append(names, *agent.Name)
			}
		}
		slices.Sort(names)
		if !slices.Equal(names, []string{"Subagent", "Subagent", "main"}) {
			t.Errorf("agent names = %v, want two Subagent and main", names)
		}
	})

	t.Run("names a subagent without a meta file after its first prompt, one line, 80 characters", func(t *testing.T) {
		e := makeEnv(t)
		subagents := filepath.Join(e.config, "projects", "-w-app", "sess-c", "subagents")
		writeText(t, filepath.Join(subagents, "agent-w4.jsonl"), jsonl(t,
			map[string]any{
				"type": "user", "timestamp": iso(watchT0),
				"message": map[string]any{"role": "user", "content": strings.Repeat("a", 100) + "\nsecond line"},
			},
			assistantLine(watchT1, "sreq-4"),
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		detail := st.SessionDetail("sess-c")
		if detail == nil {
			t.Fatalf("session sess-c not found")
		}
		agent := findAgent(t, detail, "agent-w4")
		if agent.Kind != shared.KindSubagent || agent.Name == nil || *agent.Name != strings.Repeat("a", 80) {
			t.Errorf("agent-w4 = %+v, want a subagent named by its first 80 characters", agent)
		}
	})
}

func TestWatcherSessionRegistry(t *testing.T) {
	t.Run("marks the sessions it lists live and names them as the user named them", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-a","status":"busy","name":"ProxBeam","nameSource":"user"}`, os.Getpid()))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"), jsonl(t, userLine(watchT0)))
		st := newStore()
		manualWatcher(t, e.env, st)
		detail := st.SessionDetail("sess-a")
		if detail == nil {
			t.Fatalf("session sess-a not found")
		}
		if !detail.Live {
			t.Errorf("session sess-a live = false, want true")
		}
		if detail.Status == nil || *detail.Status != shared.StatusWorking {
			t.Errorf("session sess-a status = %v, want working", detail.Status)
		}
		if detail.Name == nil || *detail.Name != "ProxBeam" {
			t.Errorf("session sess-a name = %v, want ProxBeam", detail.Name)
		}
	})

	t.Run("ends a session whose pid died, without any SessionEnd hook", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-a","status":"busy"}`, os.Getpid()))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"), jsonl(t, userLine(watchT0)))
		st := newStore()
		w := manualWatcher(t, e.env, st)
		if detail := st.SessionDetail("sess-a"); detail == nil || !detail.Live {
			t.Fatalf("session sess-a = %+v, want live after the first pass", detail)
		}
		writeText(t, filepath.Join(sessions, "81812.json"),
			`{"pid":999999999,"sessionId":"sess-a","status":"busy"}`)
		w.tick(5_000) // the next rescan reads the registry again
		if detail := st.SessionDetail("sess-a"); detail == nil || detail.Live {
			t.Errorf("session sess-a = %+v, want ended once its pid died", detail)
		}
	})

	t.Run("never asks about a pid from the .key files beside the session files", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.key"), `{"pid":777,"sessionId":"from-the-key"}`)
		var asked []int64
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env:        e.env,
			Store:      newStore(),
			SinceMs:    watchDay,
			IntervalMs: 3_600_000,
			RescanMs:   5_000,
			Now:        func() int64 { return watchT0 },
			Alive: func(pid int64) bool {
				asked = append(asked, pid)
				return true
			},
		})
		waitFor(t, "initial ingest", 5*time.Second, func() bool { return !w.CatchingUp() })
		w.Stop()
		if len(asked) != 0 {
			t.Errorf("asked about pids %v, want none: the .key files are never read", asked)
		}
	})

	t.Run("reads a live session's transcript however old it is, from the cwd's slug dir", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-old","status":"idle","name":"app-56","nameSource":"derived","cwd":"/w/app","startedAt":%d}`,
			os.Getpid(), watchT0-2*watchDay))
		// opened two days ago: outside the --since window, so only the registry's word brings it in
		old := writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-old.jsonl"),
			jsonl(t, userLine(watchT0-2*watchDay), assistantLine(watchT0-2*watchDay+1_000, "req-old")))
		setMtime(t, old, watchT0-2*watchDay)
		st := newStore()
		manualWatcher(t, e.env, st)
		detail := st.SessionDetail("sess-old")
		if detail == nil {
			t.Fatalf("session sess-old not found")
		}
		if !detail.Live || detail.Status == nil || *detail.Status != shared.StatusIdle {
			t.Errorf("session sess-old = %+v, want live and idle", detail)
		}
		if detail.Name == nil || *detail.Name != "app-56" {
			t.Errorf("session sess-old name = %v, want app-56", detail.Name)
		}
		if detail.Cwd == nil || *detail.Cwd != "/w/app" {
			t.Errorf("session sess-old cwd = %v, want /w/app", detail.Cwd)
		}
		if detail.StartedAt == nil || *detail.StartedAt != watchT0-2*watchDay {
			t.Errorf("session sess-old startedAt = %v, want two days back", detail.StartedAt)
		}
		if got := requestIDs(t, st, sessionFilter("sess-old")); !slices.Equal(got, []string{"req-old"}) {
			t.Errorf("sess-old request ids = %v, want [req-old]", got)
		}
	})

	t.Run("reads a live session's subagents from the start, then the lines they grow by", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-live","status":"busy","cwd":"/w/app","startedAt":%d}`,
			os.Getpid(), watchT0-2*watchDay))
		subagents := filepath.Join(e.config, "projects", "-w-app", "sess-live", "subagents")
		subagentFile := writeText(t, filepath.Join(subagents, "agent-w1.jsonl"), jsonl(t,
			assistantLine(watchT0+1_000, "sreq-1"),
			assistantLine(watchT0+2_000, "sreq-2"),
			assistantLine(watchT0+3_000, "sreq-3"),
		))
		st := newStore()
		w := manualWatcher(t, e.env, st)
		if got := requestIDs(t, st, sessionFilter("sess-live")); !slices.Equal(got, []string{"sreq-3", "sreq-2", "sreq-1"}) {
			t.Errorf("sess-live request ids = %v, want [sreq-3 sreq-2 sreq-1]", got)
		}
		appendText(t, subagentFile, jsonl(t,
			assistantLine(watchT0+4_000, "sreq-4"),
			assistantLine(watchT0+5_000, "sreq-5"),
		))
		w.tick(1_000) // no rescan needed: the tail reads on from its stored offset
		want := []string{"sreq-5", "sreq-4", "sreq-3", "sreq-2", "sreq-1"}
		if got := requestIDs(t, st, sessionFilter("sess-live")); !slices.Equal(got, want) {
			t.Errorf("sess-live request ids = %v, want %v", got, want)
		}
	})

	t.Run("discovers a subagent file a live session created after start, whatever its age", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-live","status":"busy","cwd":"/w/app","startedAt":%d}`,
			os.Getpid(), watchT0-2*watchDay))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-live.jsonl"),
			jsonl(t, userLine(watchT0-2*watchDay)))
		st := newStore()
		w := manualWatcher(t, e.env, st)
		if got := st.Requests(sessionFilter("sess-live")); len(got) != 0 {
			t.Fatalf("sess-live requests = %+v, want none", got)
		}
		// the session spawns a subagent after we first saw it; its file is already old by the clock
		late := writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-live", "subagents", "agent-late.jsonl"),
			jsonl(t, assistantLine(watchT0-2*watchDay, "sreq-late")))
		setMtime(t, late, watchT0-10*watchDay)
		w.tick(5_000) // the next rescan re-lists the live session's subagents
		if got := requestIDs(t, st, sessionFilter("sess-live")); !slices.Equal(got, []string{"sreq-late"}) {
			t.Errorf("sess-live request ids = %v, want [sreq-late]", got)
		}
	})

	t.Run("finds a registry session's transcript by search when its cwd maps to no project dir", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-odd","status":"busy","cwd":"/elsewhere/odd cwd+","startedAt":%d}`,
			os.Getpid(), watchT0))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-odd.jsonl"), jsonl(t,
			map[string]any{"type": "user", "timestamp": iso(watchT0 - 3*watchDay), "cwd": "/w/app", "slug": "odd-session"},
			assistantLine(watchT0-3*watchDay+1_000, "req-odd"),
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		detail := st.SessionDetail("sess-odd")
		if detail == nil {
			t.Fatalf("session sess-odd not found")
		}
		if !detail.Live || detail.Status == nil || *detail.Status != shared.StatusWorking {
			t.Errorf("session sess-odd = %+v, want live and working", detail)
		}
		// no registry name, so the transcript's own naming stands
		if detail.Name == nil || *detail.Name != "odd-session" {
			t.Errorf("session sess-odd name = %v, want odd-session", detail.Name)
		}
		if got := requestIDs(t, st, sessionFilter("sess-odd")); !slices.Equal(got, []string{"req-odd"}) {
			t.Errorf("sess-odd request ids = %v, want [req-odd]", got)
		}
	})
}

func TestWatcherSessionNamesAndRepos(t *testing.T) {
	t.Run("names a session after its title lines and keeps the latest of each", func(t *testing.T) {
		e := makeEnv(t)
		projects := filepath.Join(e.config, "projects", "-w-app")
		writeText(t, filepath.Join(projects, "sess-a.jsonl"), jsonl(t,
			map[string]any{"type": "user", "timestamp": iso(watchT0), "cwd": "/w/app", "slug": "porting", "gitBranch": "feat/a"},
			map[string]any{"type": "custom-title", "customTitle": "Port the router"},
			map[string]any{"type": "ai-title", "aiTitle": "Porting the router", "sessionId": "sess-a"},
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		var listed *store.SessionListItem
		items := st.SessionList()
		for i := range items {
			if items[i].ID == "sess-a" {
				listed = &items[i]
			}
		}
		if listed == nil {
			t.Fatalf("session sess-a not listed")
		}
		if listed.Name == nil || *listed.Name != "Port the router" {
			t.Errorf("session name = %v, want Port the router", listed.Name)
		}
		if listed.Branch == nil || *listed.Branch != "feat/a" {
			t.Errorf("session branch = %v, want feat/a", listed.Branch)
		}
	})

	t.Run("derives a session's repo from its cwd, worktrees folded into their main checkout", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatalf("mkdir .git: %v", err)
		}
		e := makeEnv(t)
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"),
			jsonl(t, map[string]any{"type": "user", "timestamp": iso(watchT0), "cwd": filepath.Join(repo, "src")}))
		st := newStore()
		manualWatcher(t, e.env, st)
		items := st.SessionList()
		for i := range items {
			if items[i].ID == "sess-a" {
				if items[i].Repo == nil || *items[i].Repo != repo {
					t.Errorf("session repo = %v, want %s", items[i].Repo, repo)
				}
				return
			}
		}
		t.Fatalf("session sess-a not listed")
	})

	t.Run("ends a session huddle's registry has ended though Claude's file still lists it", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-a","status":"idle"}`, os.Getpid()))
		writeText(t, filepath.Join(sessions, "81813.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-b","status":"idle"}`, os.Getpid()))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"), jsonl(t, userLine(watchT0)))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-b.jsonl"), jsonl(t, userLine(watchT0)))
		st := newStore()
		hubEnded := &fakeHubEnded{ended: map[string]bool{"sess-b": true}}
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env:        e.env,
			Store:      st,
			SinceMs:    watchDay,
			IntervalMs: 3_600_000,
			RescanMs:   5_000,
			Now:        func() int64 { return watchT0 },
			Alive:      ownPidAlive,
			Ended:      hubEnded,
		})
		waitFor(t, "initial ingest", 5*time.Second, func() bool { return !w.CatchingUp() })
		w.Stop()
		if detail := st.SessionDetail("sess-a"); detail == nil || !detail.Live {
			t.Errorf("session sess-a = %+v, want live", detail)
		}
		if detail := st.SessionDetail("sess-b"); detail == nil || detail.Live {
			t.Errorf("session sess-b = %+v, want ended by huddle's registry", detail)
		}
		if got := hubEnded.asked(); !slices.Equal(got, []string{"sess-a", "sess-b"}) {
			t.Errorf("the fake was asked %v, want [sess-a sess-b]", got)
		}
	})

	t.Run("leaves every listed session live when huddle's registry cannot answer", func(t *testing.T) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-a","status":"idle"}`, os.Getpid()))
		writeText(t, filepath.Join(sessions, "81813.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-b","status":"idle"}`, os.Getpid()))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"), jsonl(t, userLine(watchT0)))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-b.jsonl"), jsonl(t, userLine(watchT0)))
		st := newStore()
		hubEnded := &fakeHubEnded{err: errors.New("registry down")}
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env:        e.env,
			Store:      st,
			SinceMs:    watchDay,
			IntervalMs: 3_600_000,
			RescanMs:   5_000,
			Now:        func() int64 { return watchT0 },
			Alive:      ownPidAlive,
			Ended:      hubEnded,
		})
		waitFor(t, "initial ingest", 5*time.Second, func() bool { return !w.CatchingUp() })
		w.Stop()
		for _, id := range []string{"sess-a", "sess-b"} {
			if detail := st.SessionDetail(id); detail == nil || !detail.Live {
				t.Errorf("session %s = %+v, want live", id, detail)
			}
		}
	})
}

// fakeHubEnded is a HubEnded the test programs: it remembers the ids it was asked about and
// answers the sessions it was told ended, or its error.
type fakeHubEnded struct {
	ended map[string]bool
	err   error

	mu   sync.Mutex
	seen []string
}

func (f *fakeHubEnded) EndedAmong(_ context.Context, ids []string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, ids...)
	return f.ended, f.err
}

func (f *fakeHubEnded) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func TestWatcherInterval(t *testing.T) {
	t.Run("survives a throwing tick and keeps ingesting afterwards", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, todaySpoolPath(t, e.env), jsonl(t, map[string]any{
			"ts": jsfmt.ISOTime(time.Now()), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "a",
		}))
		realStore := newStore()
		var trip atomic.Bool
		detach := realStore.OnUpdate(func(change store.Change) {
			if trip.Load() {
				panic("boom")
			}
		})
		defer detach()
		tick := newFakeTicker()
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env: e.env, Store: realStore, SinceMs: watchDay, IntervalMs: 10, RescanMs: 5_000,
			NewTicker: tick.new, Alive: ownPidAlive,
		})
		t.Cleanup(w.Stop)
		limit := int64(10)
		waitFor(t, "initial ingest", 5*time.Second, func() bool {
			return len(realStore.Events(store.EventsFilter{Limit: &limit})) == 1
		})
		trip.Store(true)
		appendText(t, path, oneLine(t, map[string]any{
			"ts": jsfmt.ISOTime(time.Now()), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "b",
		}))
		tick.fire() // the tick throws; the watcher must not die
		waitFor(t, "the throwing tick's line", 5*time.Second, func() bool {
			return len(realStore.Events(store.EventsFilter{Limit: &limit})) == 2
		})
		trip.Store(false)
		appendText(t, path, oneLine(t, map[string]any{
			"ts": jsfmt.ISOTime(time.Now()), "event": "Stop", "session_id": "s1",
		}))
		tick.fire()
		waitFor(t, "the next healthy tick", 5*time.Second, func() bool {
			return len(realStore.Events(store.EventsFilter{Limit: &limit})) == 3
		})
	})

	t.Run("stops ingesting once stopped", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, todaySpoolPath(t, e.env), jsonl(t, map[string]any{
			"ts": jsfmt.ISOTime(time.Now()), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "a",
		}))
		st := newStore()
		tick := newFakeTicker()
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env: e.env, Store: st, SinceMs: watchDay, IntervalMs: 10, RescanMs: 5_000,
			NewTicker: tick.new, Alive: ownPidAlive,
		})
		limit := int64(10)
		waitFor(t, "initial ingest", 5*time.Second, func() bool {
			return len(st.Events(store.EventsFilter{Limit: &limit})) == 1
		})
		w.Stop()
		select {
		case <-w.Done():
		default:
			t.Fatal("watcher goroutine still running after Stop")
		}
		appendText(t, path, oneLine(t, map[string]any{
			"ts": jsfmt.ISOTime(time.Now()), "event": "Stop", "session_id": "s1",
		}))
		tick.fire()  // the loop is out: the tick lands nowhere
		w.PollOnce() // a poll after stop is a no-op
		if got := len(st.Events(store.EventsFilter{Limit: &limit})); got != 1 {
			t.Errorf("events after stop = %d, want 1", got)
		}
	})

	t.Run("yields to the event loop while a large first catch-up is still running", func(t *testing.T) {
		e := makeEnv(t)
		var lines []any
		for i := 0; i < 2_000; i += 1 {
			lines = append(lines, map[string]any{
				"ts": iso(watchT0 + int64(i)), "event": "UserPromptSubmit",
				"session_id": fmt.Sprintf("s%d", i%40), "prompt": fmt.Sprintf("prompt %d", i),
			})
		}
		writeText(t, todaySpoolPath(t, e.env), jsonl(t, lines...))
		realStore := newStore()
		var seen atomic.Int64
		detach := realStore.OnUpdate(func(change store.Change) {
			// ~0.2 ms of work per line: a ~20 ms slice holds ~100 lines, so the 2,000-line
			// backlog spans slices
			until := time.Now().Add(200 * time.Microsecond)
			for time.Now().Before(until) {
				// stay busy, like a real ingest line does
			}
			seen.Add(int64(len(change.Events)))
		})
		defer detach()
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env: e.env, Store: realStore, SinceMs: watchDay, IntervalMs: 3_600_000, Alive: ownPidAlive,
		})
		t.Cleanup(w.Stop)
		// the observer ran while the scan was still short of the end: progress is visible mid-catch-up
		waitFor(t, "the backlog under way", 10*time.Second, func() bool {
			count := seen.Load()
			return count > 0 && count < 2_000
		})
		waitFor(t, "the whole backlog", 10*time.Second, func() bool { return seen.Load() == 2_000 })
		if got := len(realStore.SessionList()); got != 40 {
			t.Errorf("session count = %d, want 40", got)
		}
	})

	t.Run("leaves no goroutine behind once stopped", func(t *testing.T) {
		before := runtime.NumGoroutine()
		e := makeEnv(t)
		writeText(t, todaySpoolPath(t, e.env), jsonl(t, map[string]any{
			"ts": jsfmt.ISOTime(time.Now()), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "a",
		}))
		st := newStore()
		tick := newFakeTicker()
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env: e.env, Store: st, SinceMs: watchDay, IntervalMs: 10, RescanMs: 5_000,
			NewTicker: tick.new, Alive: ownPidAlive,
		})
		limit := int64(10)
		waitFor(t, "initial ingest", 5*time.Second, func() bool {
			return len(st.Events(store.EventsFilter{Limit: &limit})) == 1
		})
		tick.fire()
		w.Stop()
		select {
		case <-w.Done():
		default:
			t.Fatal("watcher goroutine still running after Stop")
		}
		waitFor(t, "no goroutine left", 5*time.Second, func() bool {
			return runtime.NumGoroutine() <= before
		})
	})
}

func TestLoopbackHost(t *testing.T) {
	t.Run("names a router on this machine and nothing else", func(t *testing.T) {
		if host, ok := ingest.LoopbackHost("http://127.0.0.1:17787"); !ok || host != "127.0.0.1:17787" {
			t.Errorf("loopbackHost(127.0.0.1:17787) = %q, %v", host, ok)
		}
		if host, ok := ingest.LoopbackHost("http://localhost:8080/v1"); !ok || host != "localhost:8080" {
			t.Errorf("loopbackHost(localhost:8080/v1) = %q, %v", host, ok)
		}
		if host, ok := ingest.LoopbackHost("http://[::1]:9000"); !ok || host != "[::1]:9000" {
			t.Errorf("loopbackHost([::1]:9000) = %q, %v", host, ok)
		}
		if _, ok := ingest.LoopbackHost("https://api.anthropic.com"); ok {
			t.Error("loopbackHost(api.anthropic.com) = a router, want none")
		}
		if _, ok := ingest.LoopbackHost("not a url"); ok {
			t.Error("loopbackHost(not a url) = a router, want none")
		}
	})
}

func TestTailEviction(t *testing.T) {
	// seeded is a projects tree with a registry-named session and a discovery-found one beside it.
	seeded := func(t *testing.T) (testEnv, string) {
		e, sessions := registryEnv(t)
		writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
			`{"pid":%d,"sessionId":"sess-live","status":"busy","cwd":"/w/app"}`, os.Getpid()))
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-live.jsonl"),
			jsonl(t, userLine(watchT0), assistantLine(watchT0+1_000, "req-live")))
		quiet := writeText(t, filepath.Join(e.config, "projects", "-w-app", "quiet.jsonl"),
			jsonl(t, userLine(watchT0), assistantLine(watchT0+1_000, "req-quiet")))
		return e, quiet
	}

	t.Run("drops a quiet unregistered transcript on a rescan and keeps a live session's", func(t *testing.T) {
		e, quiet := seeded(t)
		st := newStore()
		w := manualWatcher(t, e.env, st)
		if got := w.tracked(); got != 2 { // both files tailed from the first pass
			t.Fatalf("tracked = %d, want 2", got)
		}
		setMtime(t, quiet, watchT0-2*watchDay)
		w.tick(5_000) // the rescan: the quiet file is past the since window and nobody names it
		if got := w.tracked(); got != 1 {
			t.Errorf("tracked after the rescan = %d, want 1", got)
		}
		w.tick(5_000) // and it stays dropped, however many rescans follow
		if got := w.tracked(); got != 1 {
			t.Errorf("tracked after a second rescan = %d, want 1", got)
		}
	})

	t.Run("brings a dropped file back when it changes again, without doubling its rows", func(t *testing.T) {
		e, quiet := seeded(t)
		st := newStore()
		w := manualWatcher(t, e.env, st)
		setMtime(t, quiet, watchT0-2*watchDay)
		w.tick(5_000) // dropped
		if got := w.tracked(); got != 1 {
			t.Fatalf("tracked after the drop = %d, want 1", got)
		}
		appendText(t, quiet, oneLine(t, assistantLine(watchT0+2_000, "req-newer")))
		w.tick(5_000) // the fresh mtime re-discovers it like any new file
		if got := w.tracked(); got != 2 {
			t.Errorf("tracked after the change = %d, want 2", got)
		}
		ids := requestIDs(t, st, store.RequestFilter{})
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"req-live", "req-newer", "req-quiet"}) {
			t.Errorf("request ids = %v, want req-live, req-newer and req-quiet once each", ids)
		}
		w.tick(5_000) // quiet again (no change since), still held: within the window
		if got := w.tracked(); got != 2 {
			t.Errorf("tracked after a quiet rescan = %d, want 2", got)
		}
	})
}

// The side lines the watcher writes itself: a router health line never reaches the timeline but
// feeds the router view and the retry-storm alert, and an interrupted turn does reach it, as the
// Stop hook Claude Code never sends.
func TestWatcherSideLines(t *testing.T) {
	t.Run("keeps a router health line out of the timeline and counts its rate limit as an error", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
			"kind": "router.event", "plugin": "zai", "event": "rate_limited",
			"reason": "429 from the provider", "ts": watchT0,
		}))
		st := newStore()
		manualWatcher(t, e.env, st)
		events := st.RouterEvents()
		if len(events) != 1 || events[0].Plugin != "zai" || events[0].Event != shared.EventRateLimited {
			t.Fatalf("router events = %+v, want one zai rate limit", events)
		}
		errors := st.APIErrors()
		if len(errors) != 1 || errors[0].Status != 429 || errors[0].Source != shared.SourceRouter ||
			errors[0].Plugin == nil || *errors[0].Plugin != "zai" {
			t.Errorf("api errors = %+v, want the rate limit under zai from the router", errors)
		}
		if got := eventKinds(t, st, 100); len(got) != 0 {
			t.Errorf("events = %v, want none: a health line is not a timeline event", got)
		}
	})

	t.Run("keeps a cancelled call as a 499 request and out of the API errors", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
			"ts": iso(watchT0), "event": "route", "session_id": "s9", "model": "claude-fable-5-1",
			"plugin": "huddle-router", "route": "anthropic", "status": 499, "error": "client closed",
			"latency_ms": 12,
		}))
		st := newStore()
		manualWatcher(t, e.env, st)
		routed := st.Requests(sessionFilter("s9"))
		if len(routed) != 1 || routed[0].Status == nil || *routed[0].Status != "499" ||
			routed[0].Error == nil || *routed[0].Error != "client closed" {
			t.Fatalf("requests = %+v, want one 499 client closed", routed)
		}
		if errors := st.APIErrors(); len(errors) != 0 {
			t.Errorf("api errors = %+v, want none: a client that left is no failure", errors)
		}
	})

	t.Run("shows a turn the user interrupted on the timeline as Interrupted", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"), jsonl(t,
			map[string]any{
				"type": "user", "timestamp": iso(watchT0), "cwd": "/w/app",
				"message": map[string]any{"content": "[Request interrupted by user]"},
			},
		))
		st := newStore()
		manualWatcher(t, e.env, st)
		limit := int64(10)
		events := st.Events(store.EventsFilter{Limit: &limit})
		if len(events) != 1 || events[0].Kind != "Interrupted" || events[0].SessionID == nil ||
			*events[0].SessionID != "sess-a" || events[0].AgentID != nil {
			t.Errorf("events = %+v, want one Interrupted on sess-a under no agent", events)
		}
	})

	t.Run("carries a Claude Code notice's message on the timeline line", func(t *testing.T) {
		line := ingest.NoticeLine(ingest.Notice{SessionID: "sess-a", AgentID: "main", Ts: watchT1, What: "usage limit"})
		encoded, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf(`{"ts":%q,"event":"Notice","session_id":"sess-a",`+
			`"message":"Claude Code notice: usage limit"}`, iso(watchT1))
		if string(encoded) != want {
			t.Errorf("notice line = %s, want %s", encoded, want)
		}
	})
}

func TestIngestBacklogWord(t *testing.T) {
	t.Run("stands while a first pass spans slices and falls once the backlog drains", func(t *testing.T) {
		e := makeEnv(t)
		var lines []any
		for i := 0; i < 2_000; i += 1 {
			lines = append(lines, map[string]any{
				"ts": iso(watchT0 + int64(i)), "event": "UserPromptSubmit",
				"session_id": fmt.Sprintf("s%d", i%40), "prompt": fmt.Sprintf("prompt %d", i),
			})
		}
		writeText(t, todaySpoolPath(t, e.env), jsonl(t, lines...))
		realStore := newStore()
		var seen atomic.Int64
		detach := realStore.OnUpdate(func(change store.Change) {
			until := time.Now().Add(200 * time.Microsecond)
			for time.Now().Before(until) {
				// stay busy, like a real ingest line does
			}
			seen.Add(int64(len(change.Events)))
		})
		defer detach()
		w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env: e.env, Store: realStore, SinceMs: watchDay, IntervalMs: 3_600_000, Alive: ownPidAlive,
		})
		t.Cleanup(w.Stop)
		waitFor(t, "the catch-up to be under way", 10*time.Second, func() bool {
			count := seen.Load()
			return count > 0 && count < 2_000
		})
		if !w.CatchingUp() {
			t.Fatal("catching up = false while the backlog drains")
		}
		waitFor(t, "the whole backlog", 10*time.Second, func() bool { return seen.Load() == 2_000 })
		waitFor(t, "the pass's last slice to end", 5*time.Second, func() bool { return !w.CatchingUp() })
		w.PollOnce() // a quiet tick's pass runs to completion inside the call
		if w.CatchingUp() {
			t.Error("catching up = true after a quiet poll")
		}
	})
}
