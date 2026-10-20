package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// routeInfo is the build facts every route test serves under.
var routeInfo = AppInfo{Version: "0.0.1", StartedAt: 1_000, Port: 12_345}

// routeQuery parses one query string the way a request's URL would.
func routeQuery(raw string) url.Values {
	values, _ := url.ParseQuery(raw)
	return values
}

// apiGet routes one GET without insights or history, the way a bare server answers.
func apiGet(t *testing.T, st *store.Store, path, query string) ApiResponse {
	t.Helper()
	answer, err := HandleApi(st, routeInfo, ApiRequest{Path: path, Query: routeQuery(query)}, nil, nil, nil, clock)
	if err != nil {
		t.Fatalf("HandleApi(%s) errored: %v", path, err)
	}
	return answer
}

// marshalBody renders one answer's body exactly as the server sends it.
func marshalBody(t *testing.T, answer ApiResponse) []byte {
	t.Helper()
	encoded, err := domain.MarshalCompactJSON(answer.Body)
	if err != nil {
		t.Fatalf("marshal the body of a %d answer: %v", answer.Status, err)
	}
	return encoded
}

// decodeBody fills target from one answer's body.
func decodeBody(t *testing.T, answer ApiResponse, target any) {
	t.Helper()
	if err := json.Unmarshal(marshalBody(t, answer), target); err != nil {
		t.Fatalf("decode the body %s: %v", marshalBody(t, answer), err)
	}
}

// valueOf decodes one answer's body into an order-keeping value.
func valueOf(t *testing.T, answer ApiResponse) *jsfmt.Value {
	t.Helper()
	value, err := jsfmt.DecodeValue(marshalBody(t, answer))
	if err != nil {
		t.Fatalf("decode the body %s: %v", marshalBody(t, answer), err)
	}
	return value
}

// readClock is a moment after the seeds: a read always runs a little after the fixtures it
// reads were written, and that is what pulls a request sitting exactly on a bucket boundary
// into the bar before it.
func readClock() time.Time { return nowAt.Add(time.Millisecond) }

// isoOf stamps one epoch the way the spool and the transcripts carry it.
func isoOf(ms int64) string { return jsfmt.ISOTime(time.UnixMilli(ms)) }

// addSpool drops one spool line in the store, the way the watcher would.
func addSpool(st *store.Store, entry shared.SpoolLine) { st.AddSpoolLine(entry) }

// seededStore fills the store the request-filter tests read: four requests across sessions,
// agents, models and times.
func seededStore(t *testing.T) *store.Store {
	t.Helper()
	st := store.CreateStore(clock)
	st.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.Model = "r1", 100, "a" }), nil)
	st.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.Model = "r2", 300, "b" }), nil)
	st.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.SessionID = "r3", 200, "s2" }), nil)
	st.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.AgentID = "r4", 50, "w1" }), nil)
	return st
}

func TestHandleApi(t *testing.T) {
	t.Run("lists every route, the original eight first", func(t *testing.T) {
		want := []string{
			"/api/summary",
			"/api/sessions",
			"/api/sessions/:id",
			"/api/requests",
			"/api/events",
			"/api/tools",
			"/api/models",
			"/api/health",
			"/api/alerts",
			"/api/attribution",
			"/api/budgets",
			"/api/budget-status",
			"/api/spend",
			"/api/settings",
			"/api/router",
			"/api/advisor",
			"/api/usage",
			"/api/blocks",
			"/api/history/roots",
			"/api/history/tree/:rootId",
			"/api/history/requests",
			"/api/history/content/:requestId",
			"/api/history/context/:requestId",
			"/api/history/request/:requestId/capture",
			"/api/history/agent/:nodeId/transcript",
			"/api/history/repos",
			"/api/history/flow",
			"/api/history/stats",
			"/api/history/clear",
			"/api/quota",
		}
		if !slices.Equal(Routes, want) {
			t.Errorf("Routes = %v, want the original eight first, then the derived and history ones", Routes)
		}
	})

	t.Run("serves the summary and the session list", func(t *testing.T) {
		st := store.CreateStore(clock)
		prompt := "a"
		addSpool(st, shared.SpoolLine{Ts: isoOf(1_000), Event: "UserPromptSubmit", SessionID: strPtr("s1"), Prompt: &prompt})
		promptB := "b"
		addSpool(st, shared.SpoolLine{Ts: isoOf(2_000), Event: "UserPromptSubmit", SessionID: strPtr("s2"), Prompt: &promptB})
		var summary struct {
			Summary struct {
				Sessions int64 `json:"sessions"`
			} `json:"summary"`
		}
		decodeBody(t, apiGet(t, st, "/api/summary", ""), &summary)
		if summary.Summary.Sessions != 2 {
			t.Errorf("summary sessions = %d, want 2", summary.Summary.Sessions)
		}
		var sessions struct {
			Sessions []struct {
				ID string `json:"id"`
			} `json:"sessions"`
		}
		decodeBody(t, apiGet(t, st, "/api/sessions", ""), &sessions)
		got := make([]string, 0, len(sessions.Sessions))
		for _, session := range sessions.Sessions {
			got = append(got, session.ID)
		}
		if !slices.Equal(got, []string{"s2", "s1"}) {
			t.Errorf("session ids = %v, want [s2 s1]", got)
		}
	})

	t.Run("serves one session, decoding its id, and 404s the rest", func(t *testing.T) {
		st := store.CreateStore(clock)
		cwd := "/w/app"
		addSpool(st, shared.SpoolLine{Ts: isoOf(1_000), Event: "SessionStart", SessionID: strPtr("s 1"), Cwd: &cwd})
		var found struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		}
		decodeBody(t, apiGet(t, st, "/api/sessions/"+url.PathEscape("s 1"), ""), &found)
		if found.Session.ID != "s 1" {
			t.Errorf("session id = %q, want \"s 1\"", found.Session.ID)
		}
		var unknown struct {
			Error string `json:"error"`
		}
		decodeBody(t, apiGet(t, st, "/api/sessions/nope", ""), &unknown)
		if unknown.Error != "unknown session: nope" {
			t.Errorf("unknown session error = %q", unknown.Error)
		}
		decodeBody(t, apiGet(t, st, "/api/sessions/", ""), &unknown)
		if unknown.Error != "missing session id" {
			t.Errorf("missing session error = %q", unknown.Error)
		}
	})

	t.Run("serves 48-bucket activity for every session, a live window or the whole span", func(t *testing.T) {
		st := store.CreateStore(clock)
		now := nowAt.UnixMilli()
		addSpool(st, shared.SpoolLine{Ts: isoOf(now - 60_000), Event: "SessionStart", SessionID: strPtr("live-1")})
		busy := "busy"
		st.ApplyRegistry([]shared.RegistrySession{{
			PID:       100,
			SessionID: "live-1",
			Status:    &busy,
		}}, now)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "r1", "live-1", now-30_000
		}), nil)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts, r.Model = "r2", "old-1", 1_000, "glm-5.3"
		}), nil)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts, r.Model = "r3", "old-1", 2_000, "glm-5.3"
		}), nil)
		type shaped struct {
			ID       string                `json:"id"`
			Status   *shared.SessionStatus `json:"status"`
			Activity shared.Activity       `json:"activity"`
		}
		var listed struct {
			Sessions []shaped `json:"sessions"`
		}
		decodeBody(t, apiGet(t, st, "/api/sessions", ""), &listed)
		var live, ended *shaped
		for i := range listed.Sessions {
			switch listed.Sessions[i].ID {
			case "live-1":
				live = &listed.Sessions[i]
			case "old-1":
				ended = &listed.Sessions[i]
			}
		}
		if live == nil || ended == nil {
			t.Fatalf("sessions = %+v, want live-1 and old-1", listed.Sessions)
		}
		if live.Status == nil || *live.Status != shared.StatusWorking {
			t.Errorf("live-1 status = %v, want working", live.Status)
		}
		if live.Activity.BucketMs != 18_750 { // 15 minutes ending now, in 48
			t.Errorf("live bucketMs = %d, want 18750", live.Activity.BucketMs)
		}
		if sum := sumOf(live.Activity.Counts); sum != 1 {
			t.Errorf("live counts sum = %d, want 1", sum)
		}
		if ended.Status != nil {
			t.Errorf("old-1 status = %v, want null", *ended.Status)
		}
		if ended.Activity.BucketMs != 21 { // a one-second run stretched over the same 48
			t.Errorf("ended bucketMs = %d, want 21", ended.Activity.BucketMs)
		}
		if ended.Activity.Counts[0] != 1 || ended.Activity.Counts[47] != 1 {
			t.Errorf("ended counts = %v, want one in the first and last bars", ended.Activity.Counts)
		}
		if len(ended.Activity.Models) == 0 || ended.Activity.Models[0] != "glm-5.3" {
			t.Errorf("ended models = %v, want glm-5.3 first", ended.Activity.Models)
		}
		var detail struct {
			Session shaped `json:"session"`
		}
		decodeBody(t, apiGet(t, st, "/api/sessions/old-1", ""), &detail)
		if detail.Session.Activity.BucketMs != ended.Activity.BucketMs ||
			!slices.Equal(detail.Session.Activity.Counts, ended.Activity.Counts) ||
			!slices.Equal(detail.Session.Activity.Models, ended.Activity.Models) {
			t.Errorf("detail activity = %+v, want the list's %+v", detail.Session.Activity, ended.Activity)
		}
	})

	t.Run("carries a session's name, branch, repo and parent session in both session routes", func(t *testing.T) {
		st := store.CreateStore(clock)
		branch := "zai/j1"
		repo := "/w/main"
		parent := "s9"
		title := "Fix tests"
		st.UpsertSession(store.SessionUpsert{
			ID:              "zai:j1",
			Branch:          &branch,
			Repo:            &repo,
			ParentSessionID: &parent,
			CustomTitle:     &title,
		})
		// the job's parent is not listed: it hangs off the Unattached group, never off the top level
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "r0", shared.OutsideSession, 1_000
		}), nil)
		var listed struct {
			Sessions []struct {
				ID   string  `json:"id"`
				Name *string `json:"name"`
				Jobs []struct {
					ID              string  `json:"id"`
					Name            *string `json:"name"`
					Branch          *string `json:"branch"`
					Repo            *string `json:"repo"`
					ParentSessionID *string `json:"parentSessionId"`
				} `json:"jobs"`
			} `json:"sessions"`
		}
		decodeBody(t, apiGet(t, st, "/api/sessions", ""), &listed)
		if len(listed.Sessions) == 0 {
			t.Fatal("the session list is empty, want the Unattached group")
		}
		top := listed.Sessions[0]
		if top.ID != "outside-a-session" || top.Name == nil || *top.Name != "Unattached" {
			t.Errorf("top of the list = %s (%v), want the Unattached group", top.ID, top.Name)
		}
		if len(top.Jobs) != 1 {
			t.Fatalf("the group holds %d jobs, want 1", len(top.Jobs))
		}
		job := top.Jobs[0]
		if job.ID != "zai:j1" || job.Name == nil || *job.Name != "Fix tests" ||
			job.Branch == nil || *job.Branch != "zai/j1" ||
			job.Repo == nil || *job.Repo != "/w/main" ||
			job.ParentSessionID == nil || *job.ParentSessionID != "s9" {
			t.Errorf("the job = %+v, want its name, branch, repo and parent session", job)
		}
		var detail struct {
			Session struct {
				Name            *string `json:"name"`
				Branch          *string `json:"branch"`
				Repo            *string `json:"repo"`
				ParentSessionID *string `json:"parentSessionId"`
			} `json:"session"`
		}
		decodeBody(t, apiGet(t, st, "/api/sessions/zai%3Aj1", ""), &detail)
		session := detail.Session
		if session.Name == nil || *session.Name != "Fix tests" ||
			session.Branch == nil || *session.Branch != "zai/j1" ||
			session.Repo == nil || *session.Repo != "/w/main" ||
			session.ParentSessionID == nil || *session.ParentSessionID != "s9" {
			t.Errorf("the session detail = %+v, want its name, branch, repo and parent session", session)
		}
	})

	t.Run("filters requests by session, agent, model and since", func(t *testing.T) {
		st := seededStore(t)
		idsOf := func(query string) []string {
			t.Helper()
			var body struct {
				Requests []struct {
					ID string `json:"id"`
				} `json:"requests"`
			}
			decodeBody(t, apiGet(t, st, "/api/requests", query), &body)
			got := make([]string, 0, len(body.Requests))
			for _, request := range body.Requests {
				got = append(got, request.ID)
			}
			return got
		}
		for _, table := range []struct {
			query string
			want  []string
		}{
			{"", []string{"r2", "r3", "r1", "r4"}},
			{"session=s2", []string{"r3"}},
			{"model=a", []string{"r1"}},
			{"agent=w1", []string{"r4"}},
			{"since=150", []string{"r2", "r3"}},
			{"limit=2", []string{"r2", "r3"}},
		} {
			if got := idsOf(table.query); !slices.Equal(got, table.want) {
				t.Errorf("requests(%q) = %v, want %v", table.query, got, table.want)
			}
		}
	})

	t.Run("clamps request limits to the default and the cap", func(t *testing.T) {
		st := store.CreateStore(clock)
		for i := 0; i < 1_205; i++ {
			id := fmt.Sprintf("q%d", i)
			ts := int64(i)
			st.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = id, ts }), nil)
		}
		for _, table := range []struct {
			query   string
			columns int
		}{
			{"", 200},
			{"limit=5", 5},
			{"limit=5000", 1_000},
			{"limit=abc", 200},
			{"limit=0", 200},
			{"limit=-3", 200},
		} {
			var body struct {
				Requests []json.RawMessage `json:"requests"`
			}
			decodeBody(t, apiGet(t, st, "/api/requests", table.query), &body)
			if len(body.Requests) != table.columns {
				t.Errorf("requests(%q) hold %d rows, want %d", table.query, len(body.Requests), table.columns)
			}
		}
		var newest struct {
			Requests []struct {
				ID string `json:"id"`
			} `json:"requests"`
		}
		decodeBody(t, apiGet(t, st, "/api/requests", ""), &newest)
		if len(newest.Requests) == 0 {
			t.Fatal("the request page is empty, want q1204 at its head")
		}
		if newest.Requests[0].ID != "q1204" {
			t.Errorf("newest request = %v, want q1204", newest.Requests)
		}
	})

	t.Run("filters events by session and since, with the same limit clamps", func(t *testing.T) {
		st := store.CreateStore(clock)
		for i := 0; i < 250; i++ {
			session := "s2"
			if i%2 == 0 {
				session = "s1"
			}
			prompt := fmt.Sprintf("e%d", i)
			addSpool(st, shared.SpoolLine{
				Ts:        isoOf(int64(i)),
				Event:     "UserPromptSubmit",
				SessionID: &session,
				Prompt:    &prompt,
			})
		}
		var all struct {
			Events []struct {
				Label *string `json:"label"`
			} `json:"events"`
		}
		decodeBody(t, apiGet(t, st, "/api/events", ""), &all)
		if len(all.Events) != 200 { // default limit
			t.Fatalf("events hold %d rows, want the default 200", len(all.Events))
		}
		if all.Events[0].Label == nil || *all.Events[0].Label != "e249" {
			t.Errorf("newest event = %v, want e249", all.Events[0].Label)
		}
		var limited struct {
			Events []json.RawMessage `json:"events"`
		}
		decodeBody(t, apiGet(t, st, "/api/events", "limit=7"), &limited)
		if len(limited.Events) != 7 {
			t.Errorf("limit=7 holds %d events, want 7", len(limited.Events))
		}
		var mine struct {
			Events []struct {
				SessionID *string `json:"sessionId"`
			} `json:"events"`
		}
		decodeBody(t, apiGet(t, st, "/api/events", "session=s1"), &mine)
		if len(mine.Events) != 125 {
			t.Errorf("session=s1 holds %d events, want 125", len(mine.Events))
		}
		for _, event := range mine.Events {
			if event.SessionID == nil || *event.SessionID != "s1" {
				t.Fatalf("session=s1 holds event %v of another session", event.SessionID)
			}
		}
		for _, table := range []struct {
			query  string
			events int
		}{
			{"since=200", 50},
			{"since=abc", 200},
		} {
			var body struct {
				Events []json.RawMessage `json:"events"`
			}
			decodeBody(t, apiGet(t, st, "/api/events", table.query), &body)
			if len(body.Events) != table.events {
				t.Errorf("events(%q) hold %d rows, want %d", table.query, len(body.Events), table.events)
			}
		}
	})

	t.Run("serves recent tool calls newest first, filtered by session and failure", func(t *testing.T) {
		st := store.CreateStore(clock)
		st.AddToolCall(makeTool(func(t2 *shared.ToolCallRecord) {
			t2.ID, t2.SessionID, t2.StartedAt, t2.Ok = "t1", "s1", 1_000, true
		}))
		st.AddToolCall(makeTool(func(t2 *shared.ToolCallRecord) {
			t2.ID, t2.SessionID, t2.StartedAt, t2.Ok = "t2", "s2", 3_000, false
		}))
		st.AddToolCall(makeTool(func(t2 *shared.ToolCallRecord) {
			t2.ID, t2.SessionID, t2.StartedAt, t2.Ok = "t3", "s1", 2_000, false
		}))
		idsOf := func(query string) []string {
			t.Helper()
			var body struct {
				Tools []struct {
					ID string `json:"id"`
				} `json:"tools"`
			}
			decodeBody(t, apiGet(t, st, "/api/tools", query), &body)
			got := make([]string, 0, len(body.Tools))
			for _, tool := range body.Tools {
				got = append(got, tool.ID)
			}
			return got
		}
		for _, table := range []struct {
			query string
			want  []string
		}{
			{"", []string{"t2", "t3", "t1"}},
			{"session=s1", []string{"t3", "t1"}},
			{"failed=1", []string{"t2", "t3"}},
			{"failed=0", []string{"t2", "t3", "t1"}},
			{"limit=1", []string{"t2"}},
		} {
			if got := idsOf(table.query); !slices.Equal(got, table.want) {
				t.Errorf("tools(%q) = %v, want %v", table.query, got, table.want)
			}
		}
	})

	t.Run("serves the models view verbatim", func(t *testing.T) {
		st := store.CreateStore(clock)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.Model = "glm-5.3" }), nil)
		st.AddToolCall(makeTool(func(t2 *shared.ToolCallRecord) { t2.Ok = false }))
		answer := apiGet(t, st, "/api/models", "")
		if answer.Status != 200 {
			t.Fatalf("/api/models status = %d, want 200", answer.Status)
		}
		want, err := domain.MarshalCompactJSON(st.Models(nil))
		if err != nil {
			t.Fatal(err)
		}
		if have := marshalBody(t, answer); !bytes.Equal(have, want) {
			t.Errorf("/api/models body = %s, want the store's own answer %s", have, want)
		}
	})

	t.Run("narrows the models view to the window and sessions it is asked for", func(t *testing.T) {
		st := store.CreateStore(clock)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.Model = "r1", 100, "glm-5.3" }), nil)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model, r.SessionID = "r2", 300, "glm-5.3", "s2"
		}), nil)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model, r.SessionID = "r3", 500, "kimi-k3", "s2"
		}), nil)
		modelsOf := func(query string) [][2]any {
			t.Helper()
			var body struct {
				Models []struct {
					Model    string `json:"model"`
					Requests int64  `json:"requests"`
				} `json:"models"`
			}
			decodeBody(t, apiGet(t, st, "/api/models", query), &body)
			got := make([][2]any, 0, len(body.Models))
			for _, row := range body.Models {
				got = append(got, [2]any{row.Model, row.Requests})
			}
			return got
		}
		// no scope: every request, the cached answer the snapshot pushes
		if got := modelsOf(""); !equalRows(got, [][2]any{{"glm-5.3", int64(2)}, {"kimi-k3", int64(1)}}) {
			t.Errorf("unscoped models = %v", got)
		}
		// a window drops what ran before it
		if got := modelsOf("from=200"); !equalRows(got, [][2]any{{"glm-5.3", int64(1)}, {"kimi-k3", int64(1)}}) {
			t.Errorf("from=200 models = %v", got)
		}
		// the picked session (its subagents' records carry its id) narrows to it
		if got := modelsOf("session=s2"); !equalRows(got, [][2]any{{"glm-5.3", int64(1)}, {"kimi-k3", int64(1)}}) {
			t.Errorf("session=s2 models = %v", got)
		}
		// window and sessions together, the way the models tab asks
		if got := modelsOf("from=400&session=s2"); !equalRows(got, [][2]any{{"kimi-k3", int64(1)}}) {
			t.Errorf("from=400&session=s2 models = %v", got)
		}
	})

	t.Run("answers health with build facts and a live count", func(t *testing.T) {
		st := store.CreateStore(clock)
		prompt := "a"
		addSpool(st, shared.SpoolLine{Ts: isoOf(1_000), Event: "UserPromptSubmit", SessionID: strPtr("s1"), Prompt: &prompt})
		var body struct {
			OK        bool   `json:"ok"`
			Version   string `json:"version"`
			StartedAt int64  `json:"startedAt"`
			Port      int    `json:"port"`
			Sessions  int64  `json:"sessions"`
			UptimeMs  int64  `json:"uptimeMs"`
		}
		decodeBody(t, apiGet(t, st, "/api/health", ""), &body)
		if !body.OK || body.Version != "0.0.1" || body.StartedAt != 1_000 || body.Port != 12_345 || body.Sessions != 1 {
			t.Errorf("health = %+v, want the build facts and one session", body)
		}
		if body.UptimeMs < 0 || body.UptimeMs > nowAt.UnixMilli()-routeInfo.StartedAt {
			t.Errorf("uptimeMs = %d, want it inside the session", body.UptimeMs)
		}
	})

	t.Run("404s unknown api paths with the route list attached", func(t *testing.T) {
		answer := apiGet(t, store.CreateStore(clock), "/api/nope", "")
		if answer.Status != 404 {
			t.Fatalf("/api/nope status = %d, want 404", answer.Status)
		}
		var body struct {
			Error  string   `json:"error"`
			Routes []string `json:"routes"`
		}
		decodeBody(t, answer, &body)
		if !strings.Contains(body.Error, "/api/nope") {
			t.Errorf("error = %q, want it to name /api/nope", body.Error)
		}
		if !slices.Equal(body.Routes, Routes) {
			t.Errorf("routes = %v, want Routes", body.Routes)
		}
	})
}

// sumOf adds a sparkline's counts.
func sumOf(counts []int64) int64 {
	var total int64
	for _, count := range counts {
		total += count
	}
	return total
}

// equalRows compares decoded model rows against their wants.
func equalRows(got, want [][2]any) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i][0] != want[i][0] || got[i][1] != want[i][1] {
			return false
		}
	}
	return true
}

// The history routes: one opened SQLite history the reads page through.

const routeMinute = 60_000

const r1Input = `[{"type":"text","text":"fix the bug"}]`

const r1Output = `[{"type":"text","text":"on it"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]`

// historyNode is one node write, edited in place by edit when there is one.
func historyNode(edit func(*history.NodeUpsert)) history.NodeUpsert {
	node := history.NodeUpsert{ID: "s1/main", Kind: shared.NodeMain, SessionID: "s1", AgentID: "main"}
	if edit != nil {
		edit(&node)
	}
	return node
}

// historyReq is one stored request, edited in place by edit when there is one.
func historyReq(now int64, edit func(*shared.RequestRecord)) shared.RequestRecord {
	latency := int64(10)
	record := shared.RequestRecord{
		ID:        "r1",
		SessionID: "s1",
		AgentID:   "main",
		Model:     "glm-5.3",
		Upstream:  "https://api.z.ai",
		Ts:        now - 4*routeMinute,
		LatencyMs: &latency,
		Tokens:    shared.Tokens{Input: 1000, Output: 100},
		Provider:  "zai",
	}
	if edit != nil {
		edit(&record)
	}
	return record
}

// mustUpsert writes one node, failing the test when the write fails.
func mustUpsert(t *testing.T, h *history.History, node history.NodeUpsert) {
	t.Helper()
	if err := h.UpsertNode(node); err != nil {
		t.Fatalf("upsert the node %s: %v", node.ID, err)
	}
}

// mustPutRequest stores one request, failing the test when the write fails.
func mustPutRequest(t *testing.T, h *history.History, nodeID string, record shared.RequestRecord) {
	t.Helper()
	if err := h.PutRequest(nodeID, record); err != nil {
		t.Fatalf("put the request %s: %v", record.ID, err)
	}
}

// mustPutContent stores one request's sides, failing the test when the write fails.
func mustPutContent(t *testing.T, h *history.History, requestID string, sides history.ContentSides) {
	t.Helper()
	if err := h.PutContent(requestID, sides); err != nil {
		t.Fatalf("put the content of %s: %v", requestID, err)
	}
}

// openTestHistory opens one throwaway history file the test closes by itself.
func openTestHistory(t *testing.T) *history.History {
	t.Helper()
	h, err := history.OpenHistory(filepath.Join(t.TempDir(), "history.db"), nil)
	if err != nil {
		t.Fatalf("open the history: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// seedHistory fills one live tree (main, two subagents, a linked job) plus an old main and
// an unattached job in history.
func seedHistory(t *testing.T, h *history.History, now int64) {
	t.Helper()
	repo := "/w/main"
	branch := "main"
	name := "Big fix"
	project := "app"
	cwd := "/w/main"
	model := "glm-5.3"
	started := now - 5*routeMinute
	last := now
	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.Repo, n.Branch, n.Name, n.Project, n.Cwd = &repo, &branch, &name, &project, &cwd
		n.Model, n.StartedAt, n.LastAt = &model, &started, &last
	}))
	parent := "s1/main"
	agentType := "fork"
	description := "dig"
	flash := "glm-5.3-flash"
	a1Last := now - routeMinute
	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.ID, n.Kind, n.ParentID, n.AgentID = "s1/a1", shared.NodeSubagent, &parent, "a1"
		n.AgentType, n.Description = &agentType, &description
		n.Model, n.LastAt = &flash, &a1Last
	}))
	general := "general-purpose"
	a2Last := now - 2*routeMinute
	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.ID, n.Kind, n.ParentID, n.AgentID = "s1/a2", shared.NodeSubagent, &parent, "a2"
		n.AgentType = &general
		n.Model, n.LastAt = &flash, &a2Last
	}))
	running := "running"
	jobRepo := "/w/main"
	jobLast := now - 3*routeMinute
	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.ID, n.Kind, n.ParentID, n.AgentID = "job:s1", shared.NodeJob, &parent, "job"
		n.JobState, n.Repo, n.LastAt = &running, &jobRepo, &jobLast
	}))
	otherRepo := "/w/other"
	feat := "feat"
	oldName := "Old work"
	oldStart := now - 60*routeMinute
	oldLast := now - 20*routeMinute
	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.ID, n.SessionID = "s2/main", "s2"
		n.Repo, n.Branch, n.Name = &otherRepo, &feat, &oldName
		n.Model, n.StartedAt, n.LastAt = &flash, &oldStart, &oldLast
	}))
	done := "done"
	s3Last := now - 30*routeMinute
	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.ID, n.Kind, n.SessionID, n.AgentID = "job:s3", shared.NodeJob, "s3", "job"
		n.JobState, n.Repo, n.LastAt = &done, &otherRepo, &s3Last
	}))
	mustPutRequest(t, h, "s1/main", historyReq(now, nil))
	input500 := int64(500)
	output50 := int64(50)
	r2At := now - 150_000
	mustPutRequest(t, h, "s1/a1", historyReq(now, func(r *shared.RequestRecord) {
		r.ID, r.AgentID, r.Model, r.Ts = "r2", "a1", flash, r2At
		r.Tokens = shared.Tokens{Input: input500, Output: output50}
	}))
	anthropic := "https://api.anthropic.com"
	sonnet := "claude-sonnet-5-5"
	in200 := int64(200)
	out20 := int64(20)
	r3At := now - 90_000
	mustPutRequest(t, h, "s1/a2", historyReq(now, func(r *shared.RequestRecord) {
		r.ID, r.AgentID, r.Model, r.Upstream = "r3", "a2", sonnet, anthropic
		r.Provider, r.Ts = "anthropic", r3At
		r.Tokens = shared.Tokens{Input: in200, Output: out20}
	}))
	r4At := now - 25*routeMinute
	mustPutRequest(t, h, "s2/main", historyReq(now, func(r *shared.RequestRecord) {
		r.ID, r.SessionID, r.Model, r.Ts = "r4", "s2", flash, r4At
	}))
	r1In := r1Input
	r1Out := r1Output
	mustPutContent(t, h, "r1", history.ContentSides{Input: &r1In, Output: &r1Out})
}

// historyGet routes one GET against the opened history, the way the running server does:
// the store stands in for the live registry.
func historyGet(t *testing.T, h *history.History, path string, insights InsightsView, st *store.Store) ApiResponse {
	t.Helper()
	if st == nil {
		st = store.CreateStore(clock)
	}
	at := strings.Index(path, "?")
	query := ""
	if at >= 0 {
		query = path[at+1:]
		path = path[:at]
	}
	answer, err := HandleApi(st, routeInfo, ApiRequest{Path: path, Query: routeQuery(query)}, insights, nil, h, readClock)
	if err != nil {
		t.Fatalf("HandleApi(%s) errored: %v", path, err)
	}
	return answer
}

// routeInsights is one insights over an empty store, the way the stats route reads settings.
func routeInsights(t *testing.T) *Insights {
	t.Helper()
	world := makeEnv(t)
	created := CreateInsights(context.Background(), InsightsOptions{
		Env:    world.env,
		Store:  store.CreateStore(clock),
		Now:    clock,
		TickMs: 0,
	})
	t.Cleanup(created.Stop)
	return created
}

// one shaped row the roots assertions read.
type rootRow struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Name      *string         `json:"name"`
	Repo      *string         `json:"repo"`
	Branch    *string         `json:"branch"`
	Model     *string         `json:"model"`
	SessionID string          `json:"sessionId"`
	Cwd       *string         `json:"cwd"`
	Requests  int64           `json:"requests"`
	Nodes     int64           `json:"nodes"`
	LiveNodes int64           `json:"liveNodes"`
	CostUsd   *float64        `json:"costUsd"`
	Activity  shared.Activity `json:"activity"`
	Tokens    shared.Tokens   `json:"tokens"`
}

type rootsPage struct {
	Roots []rootRow `json:"roots"`
	Next  *int64    `json:"next"`
}

func TestHistoryRoutes(t *testing.T) {
	now := nowAt.UnixMilli()

	t.Run("503s every history read when the history failed to open", func(t *testing.T) {
		for _, path := range []string{
			"/api/history/roots",
			"/api/history/tree/x",
			"/api/history/requests",
			"/api/history/content/x",
			"/api/history/context/x",
			"/api/history/agent/x/transcript",
			"/api/history/repos",
			"/api/history/flow",
			"/api/history/stats",
		} {
			answer := historyGet(t, nil, path, nil, nil)
			if answer.Status != 503 {
				t.Errorf("%s status = %d, want 503", path, answer.Status)
			}
			var body struct {
				Error string `json:"error"`
			}
			decodeBody(t, answer, &body)
			if body.Error != "history unavailable" {
				t.Errorf("%s error = %q, want history unavailable", path, body.Error)
			}
		}
	})

	t.Run("flows the window's requests and token kinds as zero-filled buckets", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		answer := historyGet(t, h, fmt.Sprintf("/api/history/flow?from=%d&to=%d&buckets=5", now-5*routeMinute, now), nil, nil)
		var series shared.FlowSeries
		decodeBody(t, answer, &series)
		if series.From != now-5*routeMinute || series.To != now {
			t.Errorf("flow window = [%d,%d], want the asked one", series.From, series.To)
		}
		if series.BucketMs != routeMinute {
			t.Errorf("bucketMs = %d, want one minute", series.BucketMs)
		}
		// r1 lands 1 min in, r2 at 2.5 min truncates to the 3rd bar, r3 at 3.5 min to the 4th; r4 is outside
		if !slices.Equal(series.Requests, []int64{0, 1, 1, 1, 0}) {
			t.Errorf("requests = %v, want [0 1 1 1 0]", series.Requests)
		}
		if !slices.Equal(series.Kinds.Input, []int64{0, 1000, 500, 200, 0}) {
			t.Errorf("input kinds = %v", series.Kinds.Input)
		}
		if !slices.Equal(series.Kinds.Output, []int64{0, 100, 50, 20, 0}) {
			t.Errorf("output kinds = %v", series.Kinds.Output)
		}
		if !slices.Equal(series.Kinds.CacheRead, []int64{0, 0, 0, 0, 0}) {
			t.Errorf("cacheRead kinds = %v", series.Kinds.CacheRead)
		}
		if !slices.Equal(series.Kinds.CacheWrite, []int64{0, 0, 0, 0, 0}) {
			t.Errorf("cacheWrite kinds = %v", series.Kinds.CacheWrite)
		}
	})

	t.Run("defaults to 60 buckets and drops nothing outside its bars", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		var series shared.FlowSeries
		decodeBody(t, historyGet(t, h, fmt.Sprintf("/api/history/flow?from=%d&to=%d", now-5*routeMinute, now), nil, nil), &series)
		if series.BucketMs != 5_000 {
			t.Errorf("bucketMs = %d, want 5000", series.BucketMs)
		}
		if len(series.Requests) != 60 {
			t.Errorf("bars = %d, want 60", len(series.Requests))
		}
		if sum := sumOf(series.Requests); sum != 3 {
			t.Errorf("requests sum = %d, want 3", sum)
		}
	})

	t.Run("400s a bad window and caps buckets, clamping the overflow into the last bar", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		for _, table := range []struct {
			path string
			want string
		}{
			{"/api/history/flow", "from: epoch ms the window starts at"},
			{fmt.Sprintf("/api/history/flow?from=%d", now), "to: epoch ms the window ends at"},
			{fmt.Sprintf("/api/history/flow?from=later&to=%d", now), "from: epoch ms the window starts at"},
			{fmt.Sprintf("/api/history/flow?from=%d&to=%d", now, now), "from must be before to"},
		} {
			var body struct {
				Error string `json:"error"`
			}
			decodeBody(t, historyGet(t, h, table.path, nil, nil), &body)
			if body.Error != table.want {
				t.Errorf("flow(%q) error = %q, want %q", table.path, body.Error, table.want)
			}
		}
		// 999 bars asked, 240 answered: every seeded request is hours inside the bucket-count cap's
		// span, so the clamped last bar must still hold all four
		var series shared.FlowSeries
		decodeBody(t, historyGet(t, h, fmt.Sprintf("/api/history/flow?from=0&to=%d&buckets=999", now), nil, nil), &series)
		if series.BucketMs != 86_400_000 {
			t.Errorf("bucketMs = %d, want one day", series.BucketMs)
		}
		if len(series.Requests) != 240 {
			t.Errorf("bars = %d, want 240", len(series.Requests))
		}
		if sum := sumOf(series.Requests); sum != 4 {
			t.Errorf("requests sum = %d, want 4", sum)
		}
	})

	t.Run("splits live roots from history, unattached jobs history-only, with roll-ups", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		var live rootsPage
		decodeBody(t, historyGet(t, h, "/api/history/roots", nil, nil), &live)
		if len(live.Roots) != 1 || live.Roots[0].ID != "s1/main" {
			t.Fatalf("live roots = %+v, want s1/main alone", live.Roots)
		}
		root := live.Roots[0]
		if root.Name == nil || *root.Name != "Big fix" || root.Repo == nil || *root.Repo != "/w/main" ||
			root.Branch == nil || *root.Branch != "main" || root.Model == nil || *root.Model != "glm-5.3" ||
			root.Requests != 3 || root.Nodes != 4 || root.LiveNodes != 4 {
			t.Errorf("live root = %+v, want the seeded roll-ups", root)
		}
		if root.Activity.BucketMs != 18_750 { // the 15-minute live window in 48 buckets
			t.Errorf("live activity bucketMs = %d, want 18750", root.Activity.BucketMs)
		}
		if sum := sumOf(root.Activity.Counts); sum != 3 {
			t.Errorf("live activity sum = %d, want 3", sum)
		}
		if root.Activity.Models[35] != "glm-5.3" { // 4 minutes ago
			t.Errorf("activity[35] = %q, want glm-5.3", root.Activity.Models[35])
		}
		if root.Activity.Models[39] != "glm-5.3-flash" { // 2.5 minutes ago
			t.Errorf("activity[39] = %q, want glm-5.3-flash", root.Activity.Models[39])
		}
		if root.Activity.Models[43] != "claude-sonnet-5-5" { // 90 seconds ago
			t.Errorf("activity[43] = %q, want claude-sonnet-5-5", root.Activity.Models[43])
		}
		if root.Tokens.Input != 1700 {
			t.Errorf("tokens input = %d, want 1700", root.Tokens.Input)
		}
		if root.CostUsd == nil || *root.CostUsd <= 0 {
			t.Errorf("costUsd = %v, want a positive figure", root.CostUsd)
		}
		if live.Next != nil {
			t.Errorf("live next = %v, want null", *live.Next)
		}

		var past rootsPage
		decodeBody(t, historyGet(t, h, "/api/history/roots?scope=history", nil, nil), &past)
		ids := make([]string, 0, len(past.Roots))
		for _, entry := range past.Roots {
			ids = append(ids, entry.ID)
		}
		if !slices.Equal(ids, []string{"s2/main", "outside-a-session"}) {
			t.Fatalf("history roots = %v, want s2/main then the Unattached group", ids)
		}
		if past.Roots[0].Requests != 1 || past.Roots[0].CostUsd == nil {
			t.Errorf("history root = %+v, want one request and a price", past.Roots[0])
		}
		// the Unattached group: the ended job s3, rolled up alone, priced at nothing it ran
		group := past.Roots[1]
		unattached := shared.UnattachedName
		if group.ID != shared.OutsideSession || group.Kind != string(shared.NodeJob) ||
			group.Name == nil || *group.Name != unattached || group.SessionID != shared.OutsideSession ||
			group.Nodes != 1 || group.Requests != 0 || group.CostUsd != nil {
			t.Errorf("the unattached group = %+v, want the rolled-up job at no price", group)
		}
		pastActivity := past.Roots[0].Activity
		if pastActivity.BucketMs != 50_000 { // a 40-minute run from start to last activity, in 48
			t.Errorf("history activity bucketMs = %d, want 50000", pastActivity.BucketMs)
		}
		if pastActivity.Counts[42] != 1 || len(pastActivity.Models) == 0 || pastActivity.Models[42] != "glm-5.3-flash" {
			t.Errorf("history activity = %+v, want r4 in bar 42", pastActivity)
		}
		if past.Next != nil {
			t.Errorf("history next = %v, want null", *past.Next)
		}
	})

	t.Run("keeps the registry's live sessions out of history, in the list and in the count", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		// s2 went quiet 20 minutes ago: time alone would call it ended, but the registry still
		// has it open, so it stays a Live card and History must not list it a second time
		st := store.CreateStore(clock)
		busy := "busy"
		st.ApplyRegistry([]shared.RegistrySession{{PID: 100, SessionID: "s2", Status: &busy}}, now)
		idsOf := func(path string) []string {
			t.Helper()
			var page rootsPage
			decodeBody(t, historyGet(t, h, path, nil, st), &page)
			ids := make([]string, 0, len(page.Roots))
			for _, root := range page.Roots {
				ids = append(ids, root.ID)
			}
			return ids
		}
		if got := idsOf("/api/history/roots?scope=history"); !slices.Equal(got, []string{shared.OutsideSession}) {
			t.Errorf("history roots with the registry = %v, want the Unattached group alone", got)
		}
		if got := idsOf("/api/history/roots"); !slices.Equal(got, []string{"s1/main"}) {
			t.Errorf("live roots with the registry = %v, want s1/main (the live scope is untouched)", got)
		}
		var counted struct {
			Roots int64 `json:"roots"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/stats", nil, st), &counted)
		if counted.Roots != 0 { // the badge counts what the list would show
			t.Errorf("stats roots = %d, want 0", counted.Roots)
		}
		// without the registry listing it, the same rows read as history again
		var unfiltered rootsPage
		decodeBody(t, historyGet(t, h, "/api/history/roots?scope=history", nil, nil), &unfiltered)
		ids := make([]string, 0, len(unfiltered.Roots))
		for _, root := range unfiltered.Roots {
			ids = append(ids, root.ID)
		}
		if !slices.Equal(ids, []string{"s2/main", shared.OutsideSession}) {
			t.Errorf("history roots without the registry = %v, want s2/main and the group", ids)
		}
	})

	t.Run("never names a root by a path: the old repo fallback reads as the repo's own name", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		// rows written before the name fallback learned better carry the repo's whole path as the name
		path := "/Users/raouf/.work/onthegosystems/wpml-org/app"
		last := now - 30*routeMinute
		mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
			n.ID, n.SessionID = "s5/main", "s5"
			n.Repo, n.Cwd, n.Name = &path, &path, &path
			n.LastAt = &last
		}))
		var past rootsPage
		decodeBody(t, historyGet(t, h, "/api/history/roots?scope=history", nil, nil), &past)
		var root *rootRow
		for i := range past.Roots {
			if past.Roots[i].ID == "s5/main" {
				root = &past.Roots[i]
			}
		}
		if root == nil {
			t.Fatal("s5/main is missing from the history roots")
		}
		if root.Name == nil || *root.Name != "app" {
			t.Errorf("name = %v, want app", root.Name)
		}
		if root.Cwd == nil || *root.Cwd != path { // the path stays on the node
			t.Errorf("cwd = %v, want the whole path", root.Cwd)
		}
	})

	t.Run("rejects an unknown scope and pages roots by last_at", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		bad := historyGet(t, h, "/api/history/roots?scope=nope", nil, nil)
		if bad.Status != 400 {
			t.Fatalf("scope=nope status = %d, want 400", bad.Status)
		}
		var body struct {
			Error string `json:"error"`
		}
		decodeBody(t, bad, &body)
		if body.Error != "scope: one of live, history" {
			t.Errorf("scope error = %q", body.Error)
		}
		repo := "/w/other"
		name := "Older still"
		started := now - 80*routeMinute
		last := now - 40*routeMinute
		mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
			n.ID, n.SessionID, n.Repo = "s4/main", "s4", &repo
			n.Name, n.StartedAt, n.LastAt = &name, &started, &last
		}))
		var first rootsPage
		decodeBody(t, historyGet(t, h, "/api/history/roots?scope=history&limit=1", nil, nil), &first)
		ids := make([]string, 0, len(first.Roots))
		for _, root := range first.Roots {
			ids = append(ids, root.ID)
		}
		// the unfiltered first page ends with the one Unattached group, but pagination counts mains only
		if !slices.Equal(ids, []string{"s2/main", shared.OutsideSession}) {
			t.Errorf("first page = %v, want s2/main and the Unattached group", ids)
		}
		if first.Next == nil || *first.Next != now-20*routeMinute {
			t.Errorf("first next = %v, want s2/main's last_at", first.Next)
		}
		var second rootsPage
		decodeBody(t, historyGet(t, h, fmt.Sprintf("/api/history/roots?scope=history&limit=1&before=%d", *first.Next), nil, nil), &second)
		ids = ids[:0]
		for _, root := range second.Roots {
			ids = append(ids, root.ID)
		}
		if !slices.Equal(ids, []string{"s4/main"}) {
			t.Errorf("second page = %v, want s4/main", ids)
		}
		if second.Next != nil {
			t.Errorf("second next = %v, want null", *second.Next)
		}
	})

	t.Run("filters roots by repo and by search text", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		idsOf := func(query string) []string {
			t.Helper()
			var page rootsPage
			decodeBody(t, historyGet(t, h, "/api/history/roots?"+query, nil, nil), &page)
			ids := make([]string, 0, len(page.Roots))
			for _, root := range page.Roots {
				ids = append(ids, root.ID)
			}
			return ids
		}
		for _, table := range []struct {
			query string
			want  []string
		}{
			{"scope=history&repo=/w/other", []string{"s2/main"}},
			{"repo=/w/main", []string{"s1/main"}},
			{"scope=history&q=old", []string{"s2/main"}},
			{"q=zzz", nil},
		} {
			if got := idsOf(table.query); !slices.Equal(got, table.want) {
				t.Errorf("roots(%q) = %v, want %v", table.query, got, table.want)
			}
		}
	})

	t.Run("shows one tree's nodes with per-node cost, live flags and job state", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		var body struct {
			Nodes []struct {
				ID          string   `json:"id"`
				Live        bool     `json:"live"`
				AgentType   *string  `json:"agentType"`
				Description *string  `json:"description"`
				ParentID    *string  `json:"parentId"`
				Kind        string   `json:"kind"`
				JobState    *string  `json:"jobState"`
				Requests    int64    `json:"requests"`
				CostUsd     *float64 `json:"costUsd"`
			} `json:"nodes"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/tree/s1%2Fmain", nil, nil), &body)
		ids := make([]string, 0, len(body.Nodes))
		live := make([]bool, 0, len(body.Nodes))
		for _, node := range body.Nodes {
			ids = append(ids, node.ID)
			live = append(live, node.Live)
		}
		if !slices.Equal(ids, []string{"s1/main", "s1/a1", "s1/a2", "job:s1"}) {
			t.Fatalf("tree ids = %v, want the seeded four", ids)
		}
		if !slices.Equal(live, []bool{true, true, true, true}) {
			t.Errorf("tree live flags = %v", live)
		}
		sub := body.Nodes[1]
		if sub.AgentType == nil || *sub.AgentType != "fork" || sub.Description == nil || *sub.Description != "dig" ||
			sub.ParentID == nil || *sub.ParentID != "s1/main" || sub.Requests != 1 || sub.CostUsd == nil {
			t.Errorf("the subagent = %+v, want its type, task, parent, one request and a price", sub)
		}
		job := body.Nodes[3]
		if job.Kind != string(shared.NodeJob) || job.JobState == nil || *job.JobState != "running" ||
			job.Requests != 0 || job.CostUsd != nil {
			t.Errorf("the job = %+v, want it running at no price", job)
		}
		unknown := historyGet(t, h, "/api/history/tree/nope", nil, nil)
		if unknown.Status != 404 {
			t.Errorf("/api/history/tree/nope status = %d, want 404", unknown.Status)
		}
		var notFound struct {
			Error string `json:"error"`
		}
		decodeBody(t, unknown, &notFound)
		if notFound.Error != "unknown root: nope" {
			t.Errorf("unknown root error = %q", notFound.Error)
		}
		missing := historyGet(t, h, "/api/history/tree/", nil, nil)
		if missing.Status != 404 {
			t.Errorf("/api/history/tree/ status = %d, want 404", missing.Status)
		}
		decodeBody(t, missing, &notFound)
		if notFound.Error != "missing root id" {
			t.Errorf("missing root error = %q", notFound.Error)
		}
	})

	t.Run("lists a tree's requests newest first with prices, keyset paged", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		var first struct {
			Requests []struct {
				ID   string `json:"id"`
				Cost *struct {
					Usd    float64  `json:"usd"`
					Detail []string `json:"detail"`
				} `json:"cost"`
			} `json:"requests"`
			Next *string `json:"next"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/requests?root=s1%2Fmain&limit=2", nil, nil), &first)
		ids := make([]string, 0, len(first.Requests))
		for _, request := range first.Requests {
			ids = append(ids, request.ID)
		}
		if !slices.Equal(ids, []string{"r3", "r2"}) {
			t.Fatalf("first page = %v, want [r3 r2]", ids)
		}
		if first.Requests[0].Cost == nil || len(first.Requests[0].Cost.Detail) == 0 ||
			first.Requests[0].Cost.Detail[0] != "Anthropic list" {
			t.Errorf("r3's cost = %+v, want the Anthropic list detail", first.Requests[0].Cost)
		}
		wantNext := fmt.Sprintf("%d:r2", now-150_000)
		if first.Next == nil || *first.Next != wantNext {
			t.Errorf("first next = %v, want %q", first.Next, wantNext)
		}
		var second struct {
			Requests []struct {
				ID string `json:"id"`
			} `json:"requests"`
			Next *string `json:"next"`
		}
		decodeBody(t, historyGet(t, h,
			fmt.Sprintf("/api/history/requests?root=s1%%2Fmain&limit=2&before=%s", url.QueryEscape(wantNext)), nil, nil), &second)
		if len(second.Requests) != 1 || second.Requests[0].ID != "r1" {
			t.Errorf("second page = %+v, want r1 alone", second.Requests)
		}
		if second.Next != nil {
			t.Errorf("second next = %v, want null", *second.Next)
		}
		var nodeAnswer struct {
			Requests []struct {
				ID string `json:"id"`
			} `json:"requests"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/requests?node=s1%2Fa1", nil, nil), &nodeAnswer)
		if len(nodeAnswer.Requests) != 1 || nodeAnswer.Requests[0].ID != "r2" {
			t.Errorf("node s1/a1 = %+v, want r2 alone", nodeAnswer.Requests)
		}
	})

	t.Run("rejects request listings without a node or root, and bad cursors", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		for _, path := range []string{
			"/api/history/requests",
			"/api/history/requests?node=x&before=zzz",
			"/api/history/requests?node=x&before=123:",
		} {
			if answer := historyGet(t, h, path, nil, nil); answer.Status != 400 {
				t.Errorf("%s status = %d, want 400", path, answer.Status)
			}
		}
	})

	t.Run("parses stored content back to block arrays and 404s the rest", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		answer := historyGet(t, h, "/api/history/content/r1", nil, nil)
		body := valueOf(t, answer)
		if have := textOfEncoded(t, fieldOf(body, "input")); have != r1Input {
			t.Errorf("input = %s, want the stored blocks", have)
		}
		if have := textOfEncoded(t, fieldOf(body, "output")); have != r1Output {
			t.Errorf("output = %s, want the stored blocks", have)
		}
		if bytes := numberOf(t, fieldOf(body, "bytes")); bytes != int64(len(r1Input)+len(r1Output)) {
			t.Errorf("bytes = %d, want the plain sizes summed", bytes)
		}
		unknown := historyGet(t, h, "/api/history/content/nope", nil, nil)
		if unknown.Status != 404 {
			t.Errorf("/api/history/content/nope status = %d, want 404", unknown.Status)
		}
		var notFound struct {
			Error string `json:"error"`
		}
		decodeBody(t, unknown, &notFound)
		if notFound.Error != "unknown request: nope" {
			t.Errorf("unknown request error = %q", notFound.Error)
		}
	})

	t.Run("caps a content response at a megabyte, cutting the longest block with a marker", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		big := `[{"type":"text","text":"` + strings.Repeat("x", 1_500_000) + `"}]`
		out := `[{"type":"text","text":"` + strings.Repeat("y", 200_000) + `"}]`
		last := now - routeMinute
		mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
			n.ID, n.SessionID, n.LastAt = "big/main", "big", &last
		}))
		bigAt := now - routeMinute
		mustPutRequest(t, h, "big/main", historyReq(now, func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "rbig", "big", bigAt
		}))
		mustPutContent(t, h, "rbig", history.ContentSides{Input: &big, Output: &out})
		answer := historyGet(t, h, "/api/history/content/rbig", nil, nil)
		if answer.Status != 200 {
			t.Fatalf("/api/history/content/rbig status = %d, want 200", answer.Status)
		}
		encoded := marshalBody(t, answer)
		if len(encoded) > 1024*1024+512 {
			t.Errorf("the capped body holds %d bytes, want at most a megabyte and a little", len(encoded))
		}
		var body struct {
			Input []struct {
				Text string `json:"text"`
			} `json:"input"`
			Output json.RawMessage `json:"output"`
			Bytes  int64           `json:"bytes"`
		}
		decodeBody(t, answer, &body)
		if body.Bytes != int64(len(big)+len(out)) {
			t.Errorf("bytes = %d, want the uncapped sizes summed", body.Bytes)
		}
		if len(body.Input) == 0 {
			t.Fatal("the capped input holds no blocks")
		}
		if !strings.HasPrefix(body.Input[0].Text, "xxxxxxxxxx") {
			t.Errorf("the cut text starts with %q", body.Input[0].Text[:min(20, len(body.Input[0].Text))])
		}
		if !strings.HasSuffix(body.Input[0].Text, "bytes]") {
			t.Errorf("the cut text does not end with the marker")
		}
		if !strings.Contains(string(encoded), "…[truncated") {
			t.Error("the body does not carry the truncation marker")
		}
		if string(body.Output) != out { // the shorter side survives whole
			t.Errorf("the output side was cut, want it whole")
		}
	})

	// transcribed seeds two requests on a quiet node whose sides come from a real transcript
	// feed: the request, its answer, the tool result the answer caused, then the request
	// that saw it.
	transcribed := func(t *testing.T, h *history.History) {
		t.Helper()
		last := now - routeMinute
		mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
			n.ID, n.SessionID, n.LastAt = "s9/main", "s9", &last
		}))
		tr0At := now - 3*routeMinute
		mustPutRequest(t, h, "s9/main", historyReq(now, func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "tr0", "s9", tr0At
		}))
		in400 := int64(400)
		cache120 := int64(120)
		tr1At := now - 2*routeMinute
		mustPutRequest(t, h, "s9/main", historyReq(now, func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "tr1", "s9", tr1At
			r.Tokens = shared.Tokens{Input: in400, CacheRead: cache120}
		}))
		state := ingest.NewTranscriptState()
		identity := ingest.FileIdentity{SessionID: "s9", Kind: shared.KindMain}
		lines := []string{
			fmt.Sprintf(`{"type":"user","sessionId":"s9","timestamp":%q,"message":{"content":"fix the bug"}}`, isoOf(now-181_000)),
			fmt.Sprintf(`{"type":"assistant","sessionId":"s9","requestId":"tr0","timestamp":%q,"message":{"model":"glm-5.3","content":[{"type":"text","text":"on it"},{"type":"tool_use","id":"call1","name":"Bash","input":{"command":"git status"}}],"usage":{"input_tokens":300}}}`, isoOf(now-180_000)),
			fmt.Sprintf(`{"type":"user","sessionId":"s9","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":"call1","content":"clean tree"}]}}`, isoOf(now-125_000)),
			fmt.Sprintf(`{"type":"assistant","sessionId":"s9","requestId":"tr1","timestamp":%q,"message":{"model":"glm-5.3","content":[{"type":"text","text":"nothing to fix"}],"usage":{"input_tokens":400,"cache_read_input_tokens":120,"output_tokens":9}}}`, isoOf(now-120_000)),
		}
		for _, line := range lines {
			emit := ingest.FeedTranscriptLine(state, identity, line)
			for _, capture := range emit.Content {
				mustPutContent(t, h, capture.RequestID, history.ContentSides{Input: capture.Input, Output: capture.Output})
			}
		}
	}

	t.Run("rebuilds the conversation a request sat in, ending at its own input", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		transcribed(t, h)
		var body struct {
			RequestID string `json:"requestId"`
			NodeID    string `json:"nodeId"`
			Model     string `json:"model"`
			Totals    struct {
				Messages     int64 `json:"messages"`
				ApproxTokens int64 `json:"approxTokens"`
				Usage        *struct {
					Input      int64 `json:"input"`
					Output     int64 `json:"output"`
					CacheRead  int64 `json:"cacheRead"`
					CacheWrite int64 `json:"cacheWrite"`
				} `json:"usage"`
				CacheTokens int64 `json:"cacheTokens"`
			} `json:"totals"`
			Messages []struct {
				Role      string `json:"role"`
				Kind      string `json:"kind"`
				RequestID string `json:"requestId"`
				Preview   string `json:"preview"`
			} `json:"messages"`
			Next *string `json:"next"`
			Note string  `json:"note"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/context/tr1", nil, nil), &body)
		if body.RequestID != "tr1" || body.NodeID != "s9/main" || body.Model != "glm-5.3" {
			t.Errorf("the context names %s of %s on %s", body.RequestID, body.NodeID, body.Model)
		}
		got := make([][4]string, 0, len(body.Messages))
		for _, message := range body.Messages {
			got = append(got, [4]string{message.Role, message.Kind, message.RequestID, message.Preview})
		}
		want := [][4]string{
			{"user", "text", "tr0", "fix the bug"},
			{"assistant", "text", "tr0", "on it"},
			{"user", "tool_result", "tr1", "clean tree"},
		}
		if !slices.EqualFunc(got, want, func(a, b [4]string) bool { return a == b }) {
			t.Errorf("messages = %v, want %v", got, want)
		}
		// the totals cover the whole conversation, and the cache split is the request's own usage
		if body.Totals.Messages != 3 {
			t.Errorf("totals messages = %d, want 3", body.Totals.Messages)
		}
		if body.Totals.Usage == nil || *body.Totals.Usage != (struct {
			Input      int64 `json:"input"`
			Output     int64 `json:"output"`
			CacheRead  int64 `json:"cacheRead"`
			CacheWrite int64 `json:"cacheWrite"`
		}{Input: 400, CacheRead: 120}) {
			t.Errorf("totals usage = %+v, want the request's own report", body.Totals.Usage)
		}
		if body.Totals.CacheTokens != 120 {
			t.Errorf("totals cacheTokens = %d, want 120", body.Totals.CacheTokens)
		}
		if body.Totals.ApproxTokens <= 0 {
			t.Errorf("totals approxTokens = %d, want a positive estimate", body.Totals.ApproxTokens)
		}
		if body.Note != ContextNote {
			t.Errorf("note = %q, want the transcript's own limits", body.Note)
		}
	})

	t.Run("pages the conversation backwards from the request, cursor by cursor", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		transcribed(t, h)
		type page struct {
			Messages []struct {
				RequestID string `json:"requestId"`
				Role      string `json:"role"`
			} `json:"messages"`
			Next *string `json:"next"`
		}
		pairsOf := func(answer ApiResponse) [][2]string {
			t.Helper()
			var body page
			decodeBody(t, answer, &body)
			got := make([][2]string, 0, len(body.Messages))
			for _, message := range body.Messages {
				got = append(got, [2]string{message.RequestID, message.Role})
			}
			return got
		}
		firstAnswer := historyGet(t, h, "/api/history/context/tr1?limit=1", nil, nil)
		var first struct {
			page
			Totals struct {
				Messages int64 `json:"messages"`
			} `json:"totals"`
		}
		decodeBody(t, firstAnswer, &first)
		if got := pairsOf(firstAnswer); !slices.EqualFunc(got, [][2]string{{"tr1", "user"}}, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("first page = %v, want tr1's user side", got)
		}
		if first.Totals.Messages != 3 {
			t.Errorf("totals messages = %d, want the whole conversation's 3", first.Totals.Messages)
		}
		// the cursor names the page's own first message, so the next page is everything before it
		wantNext := fmt.Sprintf("%d:tr1:0", now-120_000)
		if first.Next == nil || *first.Next != wantNext {
			t.Fatalf("first next = %v, want %q", first.Next, wantNext)
		}
		secondAnswer := historyGet(t, h, "/api/history/context/tr1?limit=1&cursor="+url.QueryEscape(*first.Next), nil, nil)
		if got := pairsOf(secondAnswer); !slices.EqualFunc(got, [][2]string{{"tr0", "assistant"}}, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("second page = %v, want tr0's answer", got)
		}
		var second page
		decodeBody(t, secondAnswer, &second)
		wantSecond := fmt.Sprintf("%d:tr0:1", now-180_000)
		if second.Next == nil || *second.Next != wantSecond {
			t.Fatalf("second next = %v, want %q", second.Next, wantSecond)
		}
		thirdAnswer := historyGet(t, h, "/api/history/context/tr1?limit=1&cursor="+url.QueryEscape(*second.Next), nil, nil)
		if got := pairsOf(thirdAnswer); !slices.EqualFunc(got, [][2]string{{"tr0", "user"}}, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("third page = %v, want tr0's user side", got)
		}
		var third page
		decodeBody(t, thirdAnswer, &third)
		if third.Next != nil {
			t.Errorf("third next = %v, want null", *third.Next)
		}
		// an unknown-but-well-formed cursor pages past the start to an empty page
		past := historyGet(t, h, "/api/history/context/tr1?cursor="+url.QueryEscape("1:zzz:0"), nil, nil)
		var pastPage struct {
			Messages []json.RawMessage `json:"messages"`
			Next     *string           `json:"next"`
		}
		decodeBody(t, past, &pastPage)
		if len(pastPage.Messages) != 0 || pastPage.Next != nil {
			t.Errorf("the page past the start = %+v, want an empty one with no cursor", pastPage)
		}
	})

	t.Run("serves a request's captured prompt decompressed, and 404s the rest", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		payload := `{"system":"be brief","tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write([]byte(payload))
		_ = writer.Close()
		hash := strings.Repeat("h", 64)
		capAt := now - routeMinute
		mustPutRequest(t, h, "s1/main", historyReq(now, func(r *shared.RequestRecord) {
			r.ID, r.Ts = "cap1", capAt
			r.PromptHash = &hash
			headers := shared.Headers{
				{Name: "request-id", Value: "req_9"},
				{Name: "retry-after", Value: "7"},
			}
			r.Headers = &headers
		}))
		if err := h.PutCapture(shared.CaptureRecord{
			Hash:    hash,
			Gz:      base64.StdEncoding.EncodeToString(compressed.Bytes()),
			FirstTs: capAt,
		}); err != nil {
			t.Fatalf("put the capture: %v", err)
		}
		var body struct {
			Hash    string            `json:"hash"`
			System  json.RawMessage   `json:"system"`
			Tools   json.RawMessage   `json:"tools"`
			Bytes   int64             `json:"bytes"`
			Headers map[string]string `json:"headers"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/request/cap1/capture", nil, nil), &body)
		if body.Hash != hash {
			t.Errorf("hash = %q, want the stored one", body.Hash)
		}
		if string(body.System) != `"be brief"` {
			t.Errorf("system = %s, want the stored prompt", body.System)
		}
		if want := `[{"name":"Bash","input_schema":{"type":"object"}}]`; string(body.Tools) != want {
			t.Errorf("tools = %s, want %s", body.Tools, want)
		}
		if body.Bytes != int64(len(payload)) {
			t.Errorf("bytes = %d, want the plain payload's %d", body.Bytes, len(payload))
		}
		if len(body.Headers) != 2 || body.Headers["request-id"] != "req_9" || body.Headers["retry-after"] != "7" {
			t.Errorf("headers = %v, want the recorded pair", body.Headers)
		}
		// a request whose route named no hash, and one that does not exist, have nothing to serve
		none := historyGet(t, h, "/api/history/request/r1/capture", nil, nil)
		if none.Status != 404 {
			t.Errorf("r1's capture status = %d, want 404", none.Status)
		}
		var notFound struct {
			Error string `json:"error"`
		}
		decodeBody(t, none, &notFound)
		if notFound.Error != "no capture for request: r1" {
			t.Errorf("no-capture error = %q", notFound.Error)
		}
		if answer := historyGet(t, h, "/api/history/request/nope/capture", nil, nil); answer.Status != 404 {
			t.Errorf("nope's capture status = %d, want 404", answer.Status)
		}
		missing := historyGet(t, h, "/api/history/request//capture", nil, nil)
		if missing.Status != 404 {
			t.Errorf("the empty capture status = %d, want 404", missing.Status)
		}
		decodeBody(t, missing, &notFound)
		if notFound.Error != "missing request id" {
			t.Errorf("missing capture error = %q", notFound.Error)
		}
		bad := historyGet(t, h, "/api/history/request/%zz/capture", nil, nil)
		if bad.Status != 400 {
			t.Errorf("the malformed capture status = %d, want 400", bad.Status)
		}
		decodeBody(t, bad, &notFound)
		if notFound.Error != "id is not valid URI encoding" {
			t.Errorf("malformed capture error = %q", notFound.Error)
		}
	})

	t.Run("rejects a bad context cursor and 404s an unknown request, like the other reads", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		unknown := historyGet(t, h, "/api/history/context/nope", nil, nil)
		if unknown.Status != 404 {
			t.Errorf("/api/history/context/nope status = %d, want 404", unknown.Status)
		}
		var notFound struct {
			Error string `json:"error"`
		}
		decodeBody(t, unknown, &notFound)
		if notFound.Error != "unknown request: nope" {
			t.Errorf("unknown request error = %q", notFound.Error)
		}
		if answer := historyGet(t, h, "/api/history/context/tr1?cursor=not-a-cursor", nil, nil); answer.Status != 400 {
			t.Errorf("a bad cursor status = %d, want 400", answer.Status)
		}
		missing := historyGet(t, h, "/api/history/context/", nil, nil)
		if missing.Status != 404 {
			t.Errorf("/api/history/context/ status = %d, want 404", missing.Status)
		}
		decodeBody(t, missing, &notFound)
		if notFound.Error != "missing request id" {
			t.Errorf("missing context error = %q", notFound.Error)
		}
	})

	t.Run("holds one context page to a megabyte by cutting its biggest text", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		big := strings.Repeat("x", 1_500_000)
		last := now - routeMinute
		mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
			n.ID, n.SessionID, n.LastAt = "big/main", "big", &last
		}))
		bigAt := now - routeMinute
		mustPutRequest(t, h, "big/main", historyReq(now, func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "rbig", "big", bigAt
		}))
		latestAt := now - 30_000
		mustPutRequest(t, h, "big/main", historyReq(now, func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = "rbig2", "big", latestAt
		}))
		bigIn := `[{"type":"text","text":"` + big + `"}]`
		shortOut := `[{"type":"text","text":"short answer"}]`
		mustPutContent(t, h, "rbig", history.ContentSides{Input: &bigIn, Output: &shortOut})
		latestIn := `[{"type":"text","text":"latest"}]`
		mustPutContent(t, h, "rbig2", history.ContentSides{Input: &latestIn})
		answer := historyGet(t, h, "/api/history/context/rbig2", nil, nil)
		if answer.Status != 200 {
			t.Fatalf("/api/history/context/rbig2 status = %d, want 200", answer.Status)
		}
		encoded := marshalBody(t, answer)
		if len(encoded) > 1024*1024+512 {
			t.Errorf("the capped page holds %d bytes, want at most a megabyte and a little", len(encoded))
		}
		if !strings.Contains(string(encoded), "…[truncated") {
			t.Error("the page does not carry the truncation marker")
		}
	})

	t.Run("serves an agent's whole transcript by node id, newest answer included", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		transcribed(t, h)
		var body struct {
			RequestID string `json:"requestId"`
			NodeID    string `json:"nodeId"`
			Model     string `json:"model"`
			Totals    struct {
				Messages int64 `json:"messages"`
			} `json:"totals"`
			Messages []struct {
				Role      string `json:"role"`
				RequestID string `json:"requestId"`
				Preview   string `json:"preview"`
			} `json:"messages"`
			Next *string `json:"next"`
			Note string  `json:"note"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/agent/s9/main/transcript", nil, nil), &body)
		if body.RequestID != "tr1" { // the agent's newest request, the way the context route names it
			t.Errorf("requestId = %q, want tr1", body.RequestID)
		}
		if body.NodeID != "s9/main" || body.Model != "glm-5.3" {
			t.Errorf("the transcript names %s on %s", body.NodeID, body.Model)
		}
		got := make([][3]string, 0, len(body.Messages))
		for _, message := range body.Messages {
			got = append(got, [3]string{message.Role, message.RequestID, message.Preview})
		}
		want := [][3]string{
			{"user", "tr0", "fix the bug"},
			{"assistant", "tr0", "on it"},
			{"user", "tr1", "clean tree"},
			{"assistant", "tr1", "nothing to fix"}, // the newest answer is in, where the context view stops
		}
		if !slices.EqualFunc(got, want, func(a, b [3]string) bool { return a == b }) {
			t.Errorf("messages = %v, want %v", got, want)
		}
		if body.Totals.Messages != 4 {
			t.Errorf("totals messages = %d, want 4", body.Totals.Messages)
		}
		if body.Next != nil {
			t.Errorf("next = %v, want null", *body.Next)
		}
		if body.Note != ContextNote {
			t.Errorf("note = %q, want the transcript's own limits", body.Note)
		}
	})

	t.Run("keeps a subagent's transcript to its own requests", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		r2In := `[{"type":"text","text":"dig here"}]`
		r2Out := `[{"type":"text","text":"found it"}]`
		mustPutContent(t, h, "r2", history.ContentSides{Input: &r2In, Output: &r2Out})
		var body struct {
			NodeID   string `json:"nodeId"`
			Model    string `json:"model"`
			Messages []struct {
				Role      string `json:"role"`
				RequestID string `json:"requestId"`
			} `json:"messages"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/agent/s1/a1/transcript", nil, nil), &body)
		if body.NodeID != "s1/a1" || body.Model != "glm-5.3-flash" {
			t.Errorf("the transcript names %s on %s", body.NodeID, body.Model)
		}
		got := make([][2]string, 0, len(body.Messages))
		for _, message := range body.Messages {
			got = append(got, [2]string{message.Role, message.RequestID})
		}
		want := [][2]string{{"user", "r2"}, {"assistant", "r2"}}
		if !slices.EqualFunc(got, want, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("messages = %v, want %v", got, want)
		}
		// the main agent's request never bleeds into the subagent's transcript
		var bare struct {
			Messages []json.RawMessage `json:"messages"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/agent/s1/a2/transcript", nil, nil), &bare)
		if len(bare.Messages) != 0 {
			t.Errorf("a2's transcript holds %d messages, want none", len(bare.Messages))
		}
	})

	t.Run("answers an empty page for a known agent with nothing stored, 404s the unknown ones", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		// the seeded job node exists but carries no request rows
		empty := historyGet(t, h, "/api/history/agent/job:s3/transcript", nil, nil)
		if empty.Status != 200 {
			t.Fatalf("job:s3's transcript status = %d, want 200", empty.Status)
		}
		var body struct {
			RequestID string            `json:"requestId"`
			NodeID    string            `json:"nodeId"`
			Messages  []json.RawMessage `json:"messages"`
			Next      *string           `json:"next"`
			Totals    struct {
				Messages int64            `json:"messages"`
				Usage    *json.RawMessage `json:"usage"`
			} `json:"totals"`
		}
		decodeBody(t, empty, &body)
		if body.RequestID != "" || body.NodeID != "job:s3" || len(body.Messages) != 0 ||
			body.Next != nil || body.Totals.Messages != 0 || body.Totals.Usage != nil {
			t.Errorf("the empty transcript = %+v, want an empty page of the known node", body)
		}
		unknown := historyGet(t, h, "/api/history/agent/nope/transcript", nil, nil)
		if unknown.Status != 404 {
			t.Errorf("nope's transcript status = %d, want 404", unknown.Status)
		}
		var notFound struct {
			Error string `json:"error"`
		}
		decodeBody(t, unknown, &notFound)
		if notFound.Error != "unknown agent: nope" {
			t.Errorf("unknown agent error = %q", notFound.Error)
		}
		missing := historyGet(t, h, "/api/history/agent//transcript", nil, nil)
		if missing.Status != 404 {
			t.Errorf("the empty agent status = %d, want 404", missing.Status)
		}
		decodeBody(t, missing, &notFound)
		if notFound.Error != "missing agent id" {
			t.Errorf("missing agent error = %q", notFound.Error)
		}
	})

	t.Run("pages the transcript backwards with the context route's cursor", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		transcribed(t, h)
		type page struct {
			Messages []struct {
				RequestID string `json:"requestId"`
				Role      string `json:"role"`
			} `json:"messages"`
			Next *string `json:"next"`
		}
		fetch := func(path string) page {
			t.Helper()
			var body page
			decodeBody(t, historyGet(t, h, path, nil, nil), &body)
			return body
		}
		pairsOf := func(body page) [][2]string {
			got := make([][2]string, 0, len(body.Messages))
			for _, message := range body.Messages {
				got = append(got, [2]string{message.RequestID, message.Role})
			}
			return got
		}
		first := fetch("/api/history/agent/s9/main/transcript?limit=1")
		if got := pairsOf(first); !slices.EqualFunc(got, [][2]string{{"tr1", "assistant"}}, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("first page = %v, want tr1's answer", got)
		}
		wantNext := fmt.Sprintf("%d:tr1:1", now-120_000)
		if first.Next == nil || *first.Next != wantNext {
			t.Fatalf("first next = %v, want %q", first.Next, wantNext)
		}
		second := fetch("/api/history/agent/s9/main/transcript?limit=1&cursor=" + url.QueryEscape(*first.Next))
		if got := pairsOf(second); !slices.EqualFunc(got, [][2]string{{"tr1", "user"}}, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("second page = %v, want tr1's user side", got)
		}
		if second.Next == nil {
			t.Fatal("second next = nil, want a cursor")
		}
		third := fetch("/api/history/agent/s9/main/transcript?limit=1&cursor=" + url.QueryEscape(*second.Next))
		if got := pairsOf(third); !slices.EqualFunc(got, [][2]string{{"tr0", "assistant"}}, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("third page = %v, want tr0's answer", got)
		}
		if third.Next == nil {
			t.Fatal("third next = nil, want a cursor")
		}
		fourth := fetch("/api/history/agent/s9/main/transcript?limit=1&cursor=" + url.QueryEscape(*third.Next))
		if got := pairsOf(fourth); !slices.EqualFunc(got, [][2]string{{"tr0", "user"}}, func(a, b [2]string) bool { return a == b }) {
			t.Errorf("fourth page = %v, want tr0's user side", got)
		}
		if fourth.Next != nil {
			t.Errorf("fourth next = %v, want null", *fourth.Next)
		}
	})

	t.Run("pages a long transcript past requests with nothing stored, route ids with colons included", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		last := now - routeMinute
		mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
			n.ID, n.SessionID, n.LastAt = "long/main", "long", &last
		}))
		// 40 requests, only every eighth with content: a page reads past the empty ones to fill itself
		for i := 0; i < 40; i++ {
			id := fmt.Sprintf("route:long:main:glm-5.3:%d", i)
			ts := now - int64(40-i)*1000
			mustPutRequest(t, h, "long/main", historyReq(now, func(r *shared.RequestRecord) {
				r.ID, r.SessionID, r.Ts = id, "long", ts
			}))
			if i%8 == 0 {
				in := fmt.Sprintf(`[{"type":"text","text":"ask %d"}]`, i)
				out := fmt.Sprintf(`[{"type":"text","text":"answer %d"}]`, i)
				mustPutContent(t, h, id, history.ContentSides{Input: &in, Output: &out})
			}
		}
		seen := []string{}
		var cursor *string
		for pages := 0; cursor != nil || pages == 0; pages++ {
			if pages >= 20 {
				t.Fatal("the transcript paged past 20 pages")
			}
			path := "/api/history/agent/long%2Fmain/transcript?limit=3"
			if cursor != nil {
				path += "&cursor=" + url.QueryEscape(*cursor)
			}
			var body struct {
				Messages []struct {
					Preview string `json:"preview"`
				} `json:"messages"`
				Next   *string `json:"next"`
				Totals struct {
					Messages int64 `json:"messages"`
				} `json:"totals"`
			}
			decodeBody(t, historyGet(t, h, path, nil, nil), &body)
			if body.Totals.Messages != 10 {
				t.Fatalf("totals messages = %d, want 10", body.Totals.Messages)
			}
			previews := make([]string, 0, len(body.Messages))
			for _, message := range body.Messages {
				previews = append(previews, message.Preview)
			}
			seen = append(previews, seen...)
			cursor = body.Next
			if cursor == nil {
				break
			}
		}
		want := []string{}
		for _, i := range []int{0, 8, 16, 24, 32} {
			want = append(want, fmt.Sprintf("ask %d", i), fmt.Sprintf("answer %d", i))
		}
		if !slices.Equal(seen, want) {
			t.Errorf("the paged transcript = %v, want %v", seen, want)
		}
	})

	t.Run("rejects a bad transcript cursor like the context route", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		if answer := historyGet(t, h, "/api/history/agent/s9/main/transcript?cursor=not-a-cursor", nil, nil); answer.Status != 400 {
			t.Errorf("a bad transcript cursor status = %d, want 400", answer.Status)
		}
	})

	t.Run("lists the repos trees ran in with root counts", func(t *testing.T) {
		h := openTestHistory(t)
		seedHistory(t, h, now)
		// roots are main sessions: the unattached job in /w/other is not a root of its own any more
		want := `{"repos":[{"repo":"/w/main","roots":1},{"repo":"/w/other","roots":1}]}`
		if have := marshalBody(t, historyGet(t, h, "/api/history/repos", nil, nil)); string(have) != want {
			t.Errorf("repos = %s, want %s", have, want)
		}
	})

	t.Run("reports stats with the retention setting, and honors a new one", func(t *testing.T) {
		ctx := context.Background()
		h := openTestHistory(t)
		seedHistory(t, h, now)
		insights := routeInsights(t)
		var stats struct {
			Bytes         int64 `json:"bytes"`
			Nodes         int64 `json:"nodes"`
			Requests      int64 `json:"requests"`
			Roots         int64 `json:"roots"`
			RetentionDays int   `json:"retentionDays"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/stats", insights, nil), &stats)
		if stats.Nodes != 6 || stats.Requests != 4 || stats.Roots != 1 {
			// s2/main: the one ended main tree; the unattached job is no root of its own
			t.Errorf("stats = %+v, want 6 nodes, 4 requests and 1 root", stats)
		}
		if stats.RetentionDays != settings.DefaultSettings.HistoryRetentionDays {
			t.Errorf("retentionDays = %d, want the default", stats.RetentionDays)
		}
		if stats.Bytes <= 0 {
			t.Errorf("bytes = %d, want a positive size", stats.Bytes)
		}
		if answer, err := HandleWrite(ctx, insights, nil, "PUT", "/api/settings", mustValue(t, `{"historyRetentionDays":7}`), nil); err != nil || answer.Status != 200 {
			t.Errorf("setting 7 days = %d (%v), want 200", answer.Status, err)
		}
		decodeBody(t, historyGet(t, h, "/api/history/stats", insights, nil), &stats)
		if stats.RetentionDays != 7 {
			t.Errorf("retentionDays after the save = %d, want 7", stats.RetentionDays)
		}
		if answer, err := HandleWrite(ctx, insights, nil, "PUT", "/api/settings", mustValue(t, `{"historyRetentionDays":14}`), nil); err != nil || answer.Status != 400 {
			t.Errorf("setting 14 days = %d (%v), want 400", answer.Status, err)
		}
	})

	t.Run("clears history through the write route, 503s without one, 405s other methods", func(t *testing.T) {
		ctx := context.Background()
		h := openTestHistory(t)
		seedHistory(t, h, now)
		insights := routeInsights(t)
		answer, err := HandleWrite(ctx, insights, nil, "POST", "/api/history/clear", mustValue(t, `{}`), h)
		if err != nil {
			t.Fatalf("clear errored: %v", err)
		}
		if answer.Status != 200 || string(marshalBody(t, answer)) != `{"cleared":true}` {
			t.Errorf("clear = %d %s, want 200 {\"cleared\":true}", answer.Status, marshalBody(t, answer))
		}
		var stats struct {
			Nodes    int64 `json:"nodes"`
			Requests int64 `json:"requests"`
		}
		decodeBody(t, historyGet(t, h, "/api/history/stats", nil, nil), &stats)
		if stats.Nodes != 0 || stats.Requests != 0 {
			t.Errorf("stats after the clear = %+v, want nothing left", stats)
		}
		denied, err := HandleWrite(ctx, insights, nil, "POST", "/api/history/clear", mustValue(t, `{}`), nil)
		if err != nil {
			t.Fatalf("clear without a history errored: %v", err)
		}
		if denied.Status != 503 || string(marshalBody(t, denied)) != `{"error":"history unavailable"}` {
			t.Errorf("clear without a history = %d %s, want 503", denied.Status, marshalBody(t, denied))
		}
		if wrong, err := HandleWrite(ctx, insights, nil, "DELETE", "/api/history/clear", mustValue(t, `{}`), h); err != nil || wrong.Status != 405 {
			t.Errorf("DELETE the clear = %d (%v), want 405", wrong.Status, err)
		}
	})
}

// textOfEncoded renders one value back as the JSON text it came from.
func textOfEncoded(t *testing.T, value *jsfmt.Value) string {
	t.Helper()
	if value == nil {
		return ""
	}
	return value.EncodeJSON()
}

// numberOf reads one value as the integer it holds.
func numberOf(t *testing.T, value *jsfmt.Value) int64 {
	t.Helper()
	if value == nil || value.Kind() != jsfmt.KindNumber {
		t.Fatalf("expected a number, held %v", value)
	}
	text := value.EncodeJSON()
	var number int64
	if err := json.Unmarshal([]byte(text), &number); err != nil {
		t.Fatalf("the number %s does not read as an integer: %v", text, err)
	}
	return number
}

// The carried-over insights case: every derived route answers, and the writes save through
// the same entry points the HTTP server uses.
func TestRoutesAnswersAndWrites(t *testing.T) {
	ctx := context.Background()
	fresh := seededInsights(t)
	get := func(path, query string) ApiResponse {
		t.Helper()
		answer, err := HandleApi(fresh.store, routeInfo, ApiRequest{Path: path, Query: routeQuery(query)}, fresh.insights, nil, nil, clock)
		if err != nil {
			t.Fatalf("HandleApi(%s) errored: %v", path, err)
		}
		return answer
	}
	if have := marshalBody(t, get("/api/alerts", "")); string(have) != `{"alerts":[]}` {
		t.Errorf("/api/alerts = %s, want an empty list", have)
	}
	attribution := get("/api/attribution", "by=model&range=month")
	if attribution.Status != 200 {
		t.Fatalf("/api/attribution status = %d, want 200", attribution.Status)
	}
	var rows struct {
		By    string `json:"by"`
		Range string `json:"range"`
		Rows  []struct {
			Key string `json:"key"`
		} `json:"rows"`
	}
	decodeBody(t, attribution, &rows)
	if rows.By != "model" || rows.Range != "month" || len(rows.Rows) != 1 || rows.Rows[0].Key != "glm-5.3" {
		t.Errorf("attribution by model = %+v, want the glm-5.3 row", rows)
	}
	decodeBody(t, get("/api/attribution", ""), &rows)
	if rows.By != "repo" || rows.Range != "day" {
		t.Errorf("attribution defaults = %s by %s, want repo by day", rows.Range, rows.By)
	}
	var tree struct {
		By    string `json:"by"`
		Range string `json:"range"`
		Tree  []struct {
			Kind     string `json:"kind"`
			Key      string `json:"key"`
			Requests int64  `json:"requests"`
		} `json:"tree"`
	}
	decodeBody(t, get("/api/attribution", "by=tree&range=day"), &tree)
	if tree.By != "tree" || tree.Range != "day" || len(tree.Tree) != 1 ||
		tree.Tree[0].Kind != "repo" || tree.Tree[0].Key != "app" || tree.Tree[0].Requests != 2 {
		t.Errorf("attribution by tree = %+v, want the app repo with both requests", tree)
	}
	if get("/api/attribution", "by=nope").Status != 400 {
		t.Error("attribution by=nope does not refuse")
	}
	if get("/api/attribution", "range=year").Status != 400 {
		t.Error("attribution range=year does not refuse")
	}
	var budgets struct {
		Budgets []json.RawMessage `json:"budgets"`
	}
	decodeBody(t, get("/api/budgets", ""), &budgets)
	if len(budgets.Budgets) != 0 {
		t.Errorf("/api/budgets = %s, want an empty list", marshalBody(t, get("/api/budgets", "")))
	}
	var status struct {
		Version int      `json:"version"`
		Stopped []string `json:"stopped"`
	}
	decodeBody(t, get("/api/budget-status", ""), &status)
	if status.Version != 1 || len(status.Stopped) != 0 {
		t.Errorf("/api/budget-status = %+v, want version 1 and nothing stopped", status)
	}
	var spend struct {
		PricesRetrieved string `json:"pricesRetrieved"`
	}
	decodeBody(t, get("/api/spend", ""), &spend)
	if spend.PricesRetrieved == "" {
		t.Error("/api/spend names no retrieval day")
	}
	if have := marshalBody(t, get("/api/settings", "")); string(have) != `{"settings":{"notifications":true,"historyRetentionDays":30}}` {
		t.Errorf("/api/settings = %s, want the defaults", have)
	}
	var router struct {
		Providers []json.RawMessage `json:"providers"`
	}
	decodeBody(t, get("/api/router", ""), &router)
	if len(router.Providers) != 0 {
		t.Errorf("/api/router = %s, want no providers", marshalBody(t, get("/api/router", "")))
	}
	var advisor struct {
		RunsChecked int64 `json:"runsChecked"`
	}
	decodeBody(t, get("/api/advisor", ""), &advisor)
	if advisor.RunsChecked != 1 {
		t.Errorf("/api/advisor checked %d runs, want 1", advisor.RunsChecked)
	}
	bare, err := HandleApi(fresh.store, routeInfo, ApiRequest{Path: "/api/alerts"}, nil, nil, nil, clock)
	if err != nil {
		t.Fatalf("HandleApi without insights errored: %v", err)
	}
	if bare.Status != 404 {
		t.Errorf("/api/alerts without insights = %d, want 404", bare.Status)
	}
	if answer, err := HandleWrite(ctx, nil, nil, "PUT", "/api/budgets", mustValue(t, `{}`), nil); err != nil || answer.Status != 503 {
		t.Errorf("a write without insights = %d (%v), want 503", answer.Status, err)
	}
	answer, err := HandleWrite(ctx, fresh.insights, nil, "PUT", "/api/budgets", mustValue(t, `{"budgets":[]}`), nil)
	if err != nil {
		t.Fatalf("saving budgets errored: %v", err)
	}
	if answer.Status != 200 || string(marshalBody(t, answer)) != `{"budgets":[]}` {
		t.Errorf("saving budgets = %d %s, want the empty list back", answer.Status, marshalBody(t, answer))
	}
	if answer, err := HandleWrite(ctx, fresh.insights, nil, "PUT", "/api/budgets", mustValue(t, `{"budgets":"x"}`), nil); err != nil || answer.Status != 400 {
		t.Errorf("saving bad budgets = %d (%v), want 400", answer.Status, err)
	}
	if answer, err := HandleWrite(ctx, fresh.insights, nil, "PUT", "/api/settings", mustValue(t, `{"notifications":false}`), nil); err != nil || answer.Status != 200 {
		t.Errorf("saving settings = %d (%v), want 200", answer.Status, err)
	}
	answer, err = HandleWrite(ctx, fresh.insights, nil, "POST", "/api/alerts/dismiss", mustValue(t, `{"id":"a"}`), nil)
	if err != nil {
		t.Fatalf("dismissing errored: %v", err)
	}
	if string(marshalBody(t, answer)) != `{"dismissed":"a"}` {
		t.Errorf("dismissing = %s, want the id back", marshalBody(t, answer))
	}
	if answer, err := HandleWrite(ctx, fresh.insights, nil, "POST", "/api/alerts/dismiss", mustValue(t, `{"id":3}`), nil); err != nil || answer.Status != 400 {
		t.Errorf("dismissing a number = %d (%v), want 400", answer.Status, err)
	}
	if answer, err := HandleWrite(ctx, fresh.insights, nil, "POST", "/api/alerts/dismiss", nil, nil); err != nil || answer.Status != 400 {
		t.Errorf("dismissing null = %d (%v), want 400", answer.Status, err)
	}
	if answer, err := HandleWrite(ctx, fresh.insights, nil, "DELETE", "/api/budgets", mustValue(t, `{}`), nil); err != nil || answer.Status != 405 {
		t.Errorf("DELETE the budgets = %d (%v), want 405", answer.Status, err)
	}
}

// The golden pins the byte caps' own cuts: the exact JSON the content and context routes
// answer once their oversized blocks are halved down. Change it together with the caps and
// the halving, never alone.
func TestRoutesContentGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/routes_content_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	golden, err := jsfmt.DecodeValue(raw)
	if err != nil {
		t.Fatalf("decode the golden: %v", err)
	}
	now := int64(1_800_000_000_000)
	st := store.CreateStore(clock)
	h := openTestHistory(t)
	last := now - 60_000
	// the golden's requests carry counters of their own, not historyReq's defaults
	smallTokens := func(r *shared.RequestRecord) { r.Tokens = shared.Tokens{Input: 1, Output: 1} }
	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.ID, n.SessionID, n.LastAt = "big/main", "big", &last
	}))
	mustPutRequest(t, h, "big/main", historyReq(now, func(r *shared.RequestRecord) {
		smallTokens(r)
		r.ID, r.SessionID, r.Ts = "rbig", "big", last
	}))
	input := `[` + strings.Join([]string{
		`{"type":"text","text":"` + strings.Repeat("x", 1_500_000) + `"}`,
		`{"type":"text","text":"` + strings.Repeat("é—€ 😀 ", 40_000) + `"}`,
		`{"type":"tool_use","id":"call1","name":"Bash","input":{"command":"` + strings.Repeat("c", 1_400_000) + `"}}`,
		`{"type":"text","context":"keep","text":"` + strings.Repeat("z", 900_000) + `","after":1}`,
	}, ",") + `]`
	output := "not json at all, just a very long plain side " + strings.Repeat("y", 1_100_000)
	mustPutContent(t, h, "rbig", history.ContentSides{Input: &input, Output: &output})
	content, err := HandleApi(st, AppInfo{}, ApiRequest{Path: "/api/history/content/rbig"}, nil, nil, h, clock)
	if err != nil {
		t.Fatalf("the content route errored: %v", err)
	}
	if want := fieldOf(golden, "content"); want == nil {
		t.Fatal("the golden holds no content answer")
	} else if have := string(marshalBody(t, content)); have != want.EncodeJSON() {
		reportGoldenDiff(t, "content", have, want.EncodeJSON())
	}

	mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
		n.ID, n.SessionID, n.LastAt = "ctx/main", "ctx", &last
	}))
	c1At := now - 120_000
	mustPutRequest(t, h, "ctx/main", historyReq(now, func(r *shared.RequestRecord) {
		smallTokens(r)
		r.ID, r.SessionID, r.Ts = "c1", "ctx", c1At
	}))
	mustPutRequest(t, h, "ctx/main", historyReq(now, func(r *shared.RequestRecord) {
		smallTokens(r)
		r.ID, r.SessionID, r.Ts = "c2", "ctx", last
	}))
	c1In := `[{"type":"text","text":"` + strings.Repeat("x", 1_500_000) + `"}]`
	c1Out := `[{"type":"thinking","thinking":"` + strings.Repeat("t", 1_200_000) + `","signature":"sig"},{"type":"text","text":"a short answer that must survive"}]`
	mustPutContent(t, h, "c1", history.ContentSides{Input: &c1In, Output: &c1Out})
	c2In := `[{"type":"text","text":"latest"}]`
	mustPutContent(t, h, "c2", history.ContentSides{Input: &c2In})
	answer, err := HandleApi(st, AppInfo{}, ApiRequest{Path: "/api/history/context/c2"}, nil, nil, h, clock)
	if err != nil {
		t.Fatalf("the context route errored: %v", err)
	}
	if want := fieldOf(golden, "context"); want == nil {
		t.Fatal("the golden holds no context answer")
	} else if have := string(marshalBody(t, answer)); have != want.EncodeJSON() {
		reportGoldenDiff(t, "context", have, want.EncodeJSON())
	}
}

// reportGoldenDiff names where two golden-sized answers first disagree.
func reportGoldenDiff(t *testing.T, name string, have, want string) {
	t.Helper()
	at := 0
	for at < len(have) && at < len(want) && have[at] == want[at] {
		at++
	}
	from := max(0, at-80)
	haveAround, wantAround := have[from:min(len(have), at+80)], want[from:min(len(want), at+80)]
	t.Errorf("the capped %s answer diverges from the golden at byte %d of %d:\nhave …%s…\nwant …%s…",
		name, at, len(want), haveAround, wantAround)
}

func TestCostedRequestJSON(t *testing.T) {
	priced := cost.PriceResult{USD: shared.USD(0.25), Detail: []string{"Z.ai list"}}
	one := int64(2)
	encoded, err := domain.MarshalCompactJSON(costedRequest{
		RequestRecord: shared.RequestRecord{
			ID: "r1", SessionID: "s1", AgentID: "main", Model: "glm-5.3", Upstream: "u", Ts: 1,
			Tokens: shared.Tokens{Input: 1}, StopReason: strPtr("end_turn"), Provider: "Z.ai",
			CacheWrite1h: &one, Route: strPtr("direct"), Via: strPtr("zai"),
			TailKeys: []string{"route", "via"},
		},
		Cost: &priced,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"r1","sessionId":"s1","agentId":"main","model":"glm-5.3","upstream":"u","ts":1,` +
		`"latencyMs":null,"tokens":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0},` +
		`"cacheWrite1h":2,"stopReason":"end_turn","provider":"Z.ai","route":"direct","via":"zai",` +
		`"cost":{"usd":0.25,"detail":["Z.ai list"]}}`
	if string(encoded) != want {
		t.Errorf("costed request = %s, want %s", encoded, want)
	}
}
