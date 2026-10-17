package server

// The registry's report endpoint: every credentialed POST lands in the home's hub.db — one row
// per event, one per session — and a request without a credential is refused, so the hook
// outboxes it. The reads answer every session with its state computed when read, and a report
// that lands for a session in a channel fans out to that channel's live stream.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// isoAgo answers one registry timestamp age before now, so a fixture's state against the wall
// clock is the state the test names.
func isoAgo(age time.Duration) string {
	return jsfmt.ISOTime(time.Now().Add(-age))
}

func TestTheRegistryReportRouteStoresEventsAndUpsertsTheSession(t *testing.T) {
	srv := startServer(t, nil)
	reports := []string{
		`{"session_id":"s-1","machine":"box","project":"/repo","branch":"main","title":"Fix the checkout flow","cwd":"/repo","pid":4212,"channel":"shop","last_event":"SessionStart","last_event_at":"2026-10-16T09:00:00.000Z"}`,
		`{"session_id":"s-1","machine":"box","project":"/repo","task":"ship the fix","model":"glm-4.7","context_percent":80,"cwd":"/repo","pid":4212,"channel":"shop","last_event":"UserPromptSubmit","last_event_at":"2026-10-16T09:00:01.000Z"}`,
		`{"session_id":"s-1","last_event":"PostToolUse","last_event_at":"2026-10-16T09:00:02.000Z"}`,
	}
	for _, body := range reports {
		if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
			t.Fatalf("post a report: %d %s", code, out)
		}
	}
	// no credential: the report is refused, and the hook keeps it in its outbox
	if code, _ := srv.post(t, "/api/registry/report", reports[0], map[string]string{"x-huddle-token": ""}); code != 401 {
		t.Fatalf("a report without a credential = %d, want 401", code)
	}
	db, err := store.SQLite(context.Background(), filepath.Join(srv.home, "hub.db"))
	if err != nil {
		t.Fatalf("open the hub db: %v", err)
	}
	defer db.Close()
	events, err := db.All(context.Background(), "SELECT session_id, at, kind FROM events ORDER BY seq")
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d rows, want 3", len(events))
	}
	for i, want := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse"} {
		if events[i]["kind"] != want || events[i]["session_id"] != "s-1" {
			t.Errorf("event %d = %v, want %s of s-1", i, events[i], want)
		}
	}
	if at := events[0]["at"]; at != "2026-10-16T09:00:00.000Z" {
		t.Errorf("event 0 at = %v, want the report's own time", at)
	}
	row, err := db.Get(context.Background(), "SELECT title, task, model, context_percent, last_event FROM sessions WHERE session_id = 's-1'")
	if err != nil || row == nil {
		t.Fatalf("the session row: %v %v", row, err)
	}
	if row["title"] != "Fix the checkout flow" {
		t.Errorf("title = %v, want the first one said", row["title"])
	}
	if row["task"] != "ship the fix" || row["model"] != "glm-4.7" {
		t.Errorf("task/model = %v/%v, want the newest said", row["task"], row["model"])
	}
	if row["last_event"] != "PostToolUse" {
		t.Errorf("last_event = %v, want PostToolUse", row["last_event"])
	}
}

func TestTheRegistryReportRouteRefusesAReportThatNamesNoSession(t *testing.T) {
	srv := startServer(t, nil)
	code, out := srv.post(t, "/api/registry/report", `{"last_event":"Stop"}`, nil)
	if code != 400 {
		t.Fatalf("a report without a session = %d %s, want 400", code, out)
	}
	db, err := store.SQLite(context.Background(), filepath.Join(srv.home, "hub.db"))
	if err != nil {
		t.Fatalf("open the hub db: %v", err)
	}
	defer db.Close()
	events, err := db.All(context.Background(), "SELECT 1 FROM events")
	if err != nil {
		t.Fatalf("read the events: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %d rows, want none", len(events))
	}
}

// reportStates seeds one server with a session in every state and answers them by id.
func reportStates(t *testing.T, srv *childServer) map[string]map[string]any {
	t.Helper()
	for _, body := range []string{
		`{"session_id":"s-work","machine":"box","project":"/shop","last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(2*time.Second) + `"}`,
		`{"session_id":"s-idle","machine":"box","project":"/site","last_event":"Stop","last_event_at":"` + isoAgo(5*time.Second) + `"}`,
		`{"session_id":"s-old","machine":"laptop","project":"/shop","last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(15*time.Minute) + `"}`,
		`{"session_id":"s-done","machine":"laptop","project":"/site","last_event":"SessionEnd","last_event_at":"` + isoAgo(8*time.Second) + `"}`,
	} {
		if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
			t.Fatalf("post a report: %d %s", code, out)
		}
	}
	code, _, out := srv.get(t, "/api/registry/sessions", nil)
	if code != 200 {
		t.Fatalf("list sessions = %d %s", code, out)
	}
	parsed := bodyMap(t, out)
	rows, _ := parsed["sessions"].([]any)
	byID := map[string]map[string]any{}
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			if id, _ := m["session_id"].(string); id != "" {
				byID[id] = m
			}
		}
	}
	return byID
}

func TestTheRegistryListRouteComputesEveryStateWhenRead(t *testing.T) {
	srv := startServer(t, nil)
	byID := reportStates(t, srv)
	for id, want := range map[string]string{
		"s-work": "working", "s-idle": "idle", "s-old": "stale", "s-done": "ended",
	} {
		row := byID[id]
		if row == nil {
			t.Fatalf("no row for %s in %v", id, byID)
		}
		if row["state"] != want {
			t.Errorf("%s state = %v, want %q", id, row["state"], want)
		}
	}
	if machine, _ := byID["s-work"]["machine"].(string); machine != "box" {
		t.Errorf("s-work machine = %v, want box", byID["s-work"]["machine"])
	}
}

func TestTheRegistryListRouteFiltersByMachineProjectAndState(t *testing.T) {
	srv := startServer(t, nil)
	reportStates(t, srv)
	cases := []struct {
		query string
		want  []string
	}{
		{"machine=box", []string{"s-work", "s-idle"}},
		{"project=/shop", []string{"s-work", "s-old"}},
		{"state=ended", []string{"s-done"}},
		{"machine=box&project=/site", []string{"s-idle"}},
		{"machine=nowhere", nil},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			code, _, out := srv.get(t, "/api/registry/sessions?"+tc.query, nil)
			if code != 200 {
				t.Fatalf("list = %d %s", code, out)
			}
			parsed := bodyMap(t, out)
			rows, _ := parsed["sessions"].([]any)
			got := []string{}
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					id, _ := m["session_id"].(string)
					got = append(got, id)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("sessions = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("sessions = %v, want %v", got, tc.want)
				}
			}
		})
	}
	if code, _, _ := srv.get(t, "/api/registry/sessions?state=confused", nil); code != 400 {
		t.Errorf("an unknown state = %d, want 400", code)
	}
}

func TestTheRegistrySessionRouteAnswersTheRowAndItsEvents(t *testing.T) {
	srv := startServer(t, nil)
	for _, body := range []string{
		`{"session_id":"s-1","machine":"box","project":"/repo","title":"Fix the checkout","last_event":"SessionStart","last_event_at":"` + isoAgo(6*time.Second) + `"}`,
		`{"session_id":"s-1","last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(5*time.Second) + `"}`,
	} {
		if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
			t.Fatalf("post a report: %d %s", code, out)
		}
	}
	code, _, out := srv.get(t, "/api/registry/sessions/s-1", nil)
	if code != 200 {
		t.Fatalf("the session = %d %s", code, out)
	}
	parsed := bodyMap(t, out)
	if parsed["session_id"] != "s-1" || parsed["state"] != "working" || parsed["title"] != "Fix the checkout" {
		t.Errorf("the row = %v", parsed)
	}
	events, _ := parsed["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %d, want both reports", len(events))
	}
	first, _ := events[0].(map[string]any)
	if first["seq"] != float64(1) {
		t.Errorf("the first event's seq = %v, want 1 (oldest first)", first["seq"])
	}
	if report, _ := first["report"].(map[string]any); report["last_event"] != "SessionStart" {
		t.Errorf("the first event's report = %v, want the SessionStart report", first["report"])
	}
	if code, _, _ := srv.get(t, "/api/registry/sessions/no-such", nil); code != 404 {
		t.Errorf("an unknown session = %d, want 404", code)
	}
}

func TestAReportFansOutToTheChannelsLiveStream(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.base+"/api/c/reg/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-huddle-token", srv.token)
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	frames := make(chan string, 64)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				frames <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	// the hello frame says the subscription is live: only then may the report fire
	waitFor := func(want string) string {
		deadline := time.After(5 * time.Second)
		for {
			var frame string
			select {
			case line, open := <-frames:
				if !open {
					t.Fatal("the stream closed early")
				}
				frame = line
			case <-deadline:
				t.Fatalf("no %s event within 5 s", want)
			}
			var m struct {
				Type string `json:"type"`
				Data struct {
					SessionID string `json:"session_id"`
					State     string `json:"state"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(frame), &m); err != nil || m.Type != want {
				continue
			}
			return frame
		}
	}
	waitFor("hello")
	body := `{"session_id":"s-live","machine":"box","channel":"reg","last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(time.Second) + `"}`
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("post a report: %d %s", code, out)
	}
	frame := waitFor("registry.session")
	var got struct {
		Data struct {
			SessionID string `json:"session_id"`
			State     string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(frame), &got); err != nil {
		t.Fatalf("the registry event = %s: %v", frame, err)
	}
	if got.Data.SessionID != "s-live" || got.Data.State != "working" {
		t.Fatalf("the registry event = %s, want s-live working", frame)
	}
}

// openRegistryLive opens the all-channels registry stream and answers its frames, oldest first,
// as JSON strings. The Now list reads sessions of every channel, so one stream carries them all.
func openRegistryLive(t *testing.T, srv *childServer) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.base+"/api/registry/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-huddle-token", srv.token)
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	frames := make(chan string, 64)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				frames <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return frames
}

// nextRegistryEvent answers the next frame whose type is want, failing the test when the stream
// closes or nothing of that type arrives within 5 s.
func nextRegistryEvent(t *testing.T, frames <-chan string, want string) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		var frame string
		select {
		case line, open := <-frames:
			if !open {
				t.Fatal("the registry stream closed early")
			}
			frame = line
		case <-deadline:
			t.Fatalf("no %s event within 5 s", want)
		}
		var m struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(frame), &m); err != nil || m.Type != want {
			continue
		}
		return m.Data
	}
}

func TestTheRegistryLiveStreamCarriesEverySession(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_PID_CHECK_MS": "100"})
	if code, out := srv.post(t, "/api/channels", `{"name":"reg"}`, nil); code != 200 {
		t.Fatalf("create the channel: %d %s", code, out)
	}
	frames := openRegistryLive(t, srv)
	nextRegistryEvent(t, frames, "hello")
	// a session in a channel and one outside any channel: both reach the one stream
	inChannel := `{"session_id":"s-live","machine":"box","channel":"reg","last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(time.Second) + `"}`
	if code, out := srv.post(t, "/api/registry/report", inChannel, nil); code != 200 {
		t.Fatalf("post the channel report: %d %s", code, out)
	}
	data := nextRegistryEvent(t, frames, "registry.session")
	if data["session_id"] != "s-live" || data["state"] != "working" {
		t.Fatalf("the channel session's event = %v", data)
	}
	solo := `{"session_id":"s-solo","machine":"box","last_event":"Stop","last_event_at":"` + isoAgo(2*time.Second) + `"}`
	if code, out := srv.post(t, "/api/registry/report", solo, nil); code != 200 {
		t.Fatalf("post the channel-less report: %d %s", code, out)
	}
	data = nextRegistryEvent(t, frames, "registry.session")
	if data["session_id"] != "s-solo" || data["state"] != "idle" {
		t.Fatalf("the channel-less session's event = %v", data)
	}
	// a process the pid checker cannot name ends its session, and the stream says so
	gone := `{"session_id":"s-gone","machine":"box","pid":2000000000,"last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(time.Second) + `"}`
	if code, out := srv.post(t, "/api/registry/report", gone, nil); code != 200 {
		t.Fatalf("post the doomed report: %d %s", code, out)
	}
	for {
		data = nextRegistryEvent(t, frames, "registry.session")
		if data["session_id"] != "s-gone" || data["state"] != "ended" {
			continue // the report's own working row arrives first; the checker's follows
		}
		break
	}
	// a request without a credential never opens the stream
	request, err := http.NewRequest(http.MethodGet, srv.base+"/api/registry/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("a stream without a credential = %d, want 401", response.StatusCode)
	}
}

func TestTheServerEndsASessionWhoseProcessIsGone(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_PID_CHECK_MS": "100"})
	// a pid no process holds: the checker's ps cannot name it, so the session reads ended
	body := `{"session_id":"s-gone","pid":2000000000,"last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(time.Second) + `"}`
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("post a report: %d %s", code, out)
	}
	ended := until(5*time.Second, func() bool {
		code, _, out := srv.get(t, "/api/registry/sessions/s-gone", nil)
		if code != 200 {
			return false
		}
		return bodyMap(t, out)["state"] == "ended"
	})
	if !ended {
		code, _, out := srv.get(t, "/api/registry/sessions/s-gone", nil)
		t.Fatalf("the session never read ended: %d %s", code, out)
	}
}

// spoolRouteLine appends one router route line to the child's radar spool, the spool's own
// shape, so the tab the server runs ingests one priced request for the session.
func spoolRouteLine(t *testing.T, srv *childServer, session string) {
	t.Helper()
	path := filepath.Join(srv.radarHome, "spool", time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("make the spool dir: %v", err)
	}
	line := `{"ts":` + strconv.Quote(time.Now().UTC().Format("2006-01-02T15:04:05.000Z")) +
		`,"event":"route","session_id":"` + session +
		`","agent_id":"main","model":"glm-5.3","upstream":"https://api.z.ai/api/anthropic",` +
		`"plugin":"zai","route":"direct","status":200,"latency_ms":900,` +
		`"usage":{"input_tokens":50,"output_tokens":20,"cache_read_input_tokens":500,` +
		`"cache_creation_input_tokens":100}}`
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open the spool: %v", err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		t.Fatalf("append the route line: %v", err)
	}
}

func TestAReportReachesTheRadarTabOnce(t *testing.T) {
	srv := startServer(t, nil)
	if code, out := srv.post(t, "/api/registry/report",
		`{"session_id":"s-radar","cwd":"/w/one","last_event":"UserPromptSubmit","last_event_at":"`+
			isoAgo(time.Second)+`"}`, nil); code != 200 {
		t.Fatalf("post a report: %d %s", code, out)
	}
	// the report's event landed in radar's spool exactly once, as the hook line radar reads
	spool := filepath.Join(srv.radarHome, "spool", time.Now().UTC().Format("2006-01-02")+".jsonl")
	raw, err := os.ReadFile(spool)
	if err != nil {
		t.Fatalf("read the spool: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"session_id":"s-radar"`) ||
		!strings.Contains(lines[0], `"event":"UserPromptSubmit"`) {
		t.Fatalf("the spool = %s, want the one recorded report", raw)
	}
	// a report the registry refuses never records: it names no session
	if code, _ := srv.post(t, "/api/registry/report", `{"last_event":"Stop","last_event_at":"`+
		isoAgo(time.Second)+`"}`, nil); code != 400 {
		t.Fatalf("a report without a session = %d, want 400", code)
	}
	if raw, err = os.ReadFile(spool); err != nil {
		t.Fatalf("re-read the spool: %v", err)
	}
	if count := strings.Count(string(raw), "\n"); count != 1 {
		t.Fatalf("the spool holds %d lines after the refused report, want the one", count)
	}
}

func TestTheRegistryRowsCarryTheCostAndProviderRadarKnows(t *testing.T) {
	srv := startServer(t, nil)
	spoolRouteLine(t, srv, "s-cost")
	// the tab the server runs ingests on its own cadence: wait until it answers the session
	if !until(10*time.Second, func() bool {
		code, _, _ := srv.get(t, "/api/radar/api/sessions/s-cost", nil)
		return code == 200
	}) {
		t.Fatal("the radar tab never answered the session the spool line describes")
	}
	for _, body := range []string{
		`{"session_id":"s-cost","machine":"box","project":"/repo","last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(2*time.Second) + `"}`,
		`{"session_id":"s-plain","machine":"box","project":"/repo","last_event":"UserPromptSubmit","last_event_at":"` + isoAgo(2*time.Second) + `"}`,
	} {
		if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
			t.Fatalf("post a report: %d %s", code, out)
		}
	}
	code, _, out := srv.get(t, "/api/registry/sessions/s-cost", nil)
	if code != 200 {
		t.Fatalf("the session = %d %s", code, out)
	}
	row := bodyMap(t, out)
	cost, priced := row["cost_usd"].(float64)
	if !priced || cost <= 0 {
		t.Errorf("cost_usd = %v, want radar's estimate over 0", row["cost_usd"])
	}
	if provider, _ := row["provider"].(string); provider != "Z.ai" {
		t.Errorf("provider = %v, want Z.ai, the glm-5.3 requests' provider", row["provider"])
	}
	// the list carries the same facts, and a session radar never saw keeps both null
	code, _, out = srv.get(t, "/api/registry/sessions", nil)
	if code != 200 {
		t.Fatalf("list sessions = %d %s", code, out)
	}
	rows, _ := bodyMap(t, out)["sessions"].([]any)
	byID := map[string]map[string]any{}
	for _, one := range rows {
		if m, ok := one.(map[string]any); ok {
			if id, _ := m["session_id"].(string); id != "" {
				byID[id] = m
			}
		}
	}
	if plain := byID["s-plain"]; plain == nil {
		t.Fatalf("no row for s-plain in %v", byID)
	} else if plain["cost_usd"] != nil || plain["provider"] != nil {
		t.Errorf("the unknown session's row = %v, want both facts null", plain)
	}
	if listed := byID["s-cost"]; listed == nil || listed["cost_usd"] == nil || listed["provider"] != "Z.ai" {
		t.Errorf("the listed row = %v, want the cost and provider carried", listed)
	}
}
