package server

// The session digest's arrival (SPEC 8): a Stop report that carries a digest reaches every
// digest-level peer once — the newest digest of a sender replaces the one still waiting — and
// is published in the session's own channel under digest.<session>. A satellite's report rides
// the same path through /api/sat/report, with no satellite code of its own.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// stopDigest lands one Stop report carrying a digest, as the Stop hook would.
func stopDigest(t *testing.T, srv *childServer, id, channel, member, digest string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"channel":%q,"member":%q,"last_event":"Stop","last_event_at":%q,"digest":%q}`,
		id, channel, member, isoAgo(2*time.Second), digest)
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("the Stop report for %s = %d %s", id, code, out)
	}
}

// digestRows reads the digest messages straight from the home's hub.db.
func digestRows(t *testing.T, srv *childServer) []map[string]any {
	t.Helper()
	rows := messageRows(t, srv)
	out := []map[string]any{}
	for _, row := range rows {
		if row["from_kind"] == hub.FromDigest {
			out = append(out, row)
		}
	}
	return out
}

func TestADigestReachesDigestLevelPeersOnce(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/links",
		`{"sessions":["s-a","s-b"],"channel":"kanban-release","level":"digest"}`, nil); code != 200 {
		t.Fatalf("the link = %d %s", code, out)
	}
	// two Stops before s-b's next prompt leave it one message: the newest
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the first digest")
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the second digest")
	rows := digestRows(t, srv)
	if len(rows) != 1 {
		t.Fatalf("s-b holds %d digest messages (%v), want the newest alone", len(rows), rows)
	}
	if rows[0]["session_id"] != "s-b" || rows[0]["text"] != "the second digest" || rows[0]["state"] != "pending" {
		t.Fatalf("the digest row = %v, want s-b pending the second digest", rows[0])
	}
	// the sender hears nothing of its own digest
	_, body := srv.post(t, "/api/sessions/s-a/messages", "{}", nil)
	if strings.Contains(body, "Digest from session") {
		t.Errorf("the sender took its own digest: %s", body)
	}
	// the peer's take says who it came from
	_, body = srv.post(t, "/api/sessions/s-b/messages", "{}", nil)
	if !strings.Contains(body, "Digest from session s-a via huddle: the second digest") {
		t.Errorf("the take = %s", body)
	}
	// the next Stop replaces what is still waiting: one pending row again
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the third digest")
	rows = digestRows(t, srv)
	if len(rows) != 2 {
		t.Fatalf("after the third Stop s-b holds %d digest rows (%v), want the delivered and the newest", len(rows), rows)
	}
	if rows[0]["state"] != "delivered" || rows[1]["text"] != "the third digest" || rows[1]["state"] != "pending" {
		t.Fatalf("the digest rows = %v", rows)
	}
}

func TestOverhearAndOnRequestPeersGetNoDigest(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-h", "kanban-release", "hear", "UserPromptSubmit")
	reportSession(t, srv, "s-o", "kanban-release", "ask", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/links",
		`{"sessions":["s-o","s-h"],"channel":"kanban-release","level":"on-request"}`, nil); code != 200 {
		t.Fatalf("the on-request link = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/links",
		`{"sessions":["s-h","s-a"],"channel":"kanban-release","level":"overhear"}`, nil); code != 200 {
		t.Fatalf("the overhear link = %d %s", code, out)
	}
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
	if rows := digestRows(t, srv); len(rows) != 0 {
		t.Fatalf("the digest reached %v, want overhear and on-request peers skipped", rows)
	}
}

func TestTheDigestIsPublishedOnItsTopic(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest text")
	code, out := srv.op(t, "kanban-release", "events", "api", `{"topic":"digest.s-a"}`)
	if code != 200 {
		t.Fatalf("events = %d %s", code, out)
	}
	events, ok := out["result"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("the topic carries %v, want the one digest event", out["result"])
	}
	ev, _ := events[0].(map[string]any)
	if ev["topic"] != "digest.s-a" || ev["from"] != "api" || ev["msg"] != "the digest text" {
		t.Errorf("the event = %v, want api's digest on digest.s-a", ev)
	}
}

// TestASatelliteSessionsDigestReachesHubPeers pins the satellite path: the machine's report
// batch posts through /api/sat/report, and the hub fans the digest out to the hub-side links.
func TestASatelliteSessionsDigestReachesHubPeers(t *testing.T) {
	hubHome := t.TempDir()
	hubLog := &runLog{}
	hubBase, stopHub, hubDone := startHuddleRun(t, hubHome, "hub-token", hubLog)
	t.Cleanup(func() {
		stopHub()
		select {
		case <-hubDone:
		case <-time.After(10 * time.Second):
		}
	})
	report := func(id string) {
		t.Helper()
		status, body := call(t, hubBase, http.MethodPost, "/api/registry/report", "hub-token", "",
			fmt.Sprintf(`{"session_id":%q,"last_event":"UserPromptSubmit","last_event_at":"2026-10-16T12:00:00.000Z"}`, id))
		if status != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("report %s = %d %s", id, status, body)
		}
	}
	report("s-a")
	report("s-b")

	// the satellite joins the hub the way join-hub does, then its reports carry its credential
	minter, err := hub.Open(context.Background(), home.Home{Root: hubHome}.HubDB())
	if err != nil {
		t.Fatalf("open the hub's database: %v", err)
	}
	invite, err := minter.MintSatelliteInvite(context.Background(), time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body := call(t, hubBase, http.MethodPost, "/api/sat/join", "", "",
		`{"invite":"`+invite+`","name":"mbp"}`)
	if status != 200 {
		t.Fatalf("the join answered %d: %s", status, body)
	}
	var joined struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(body), &joined); err != nil || joined.Credential == "" {
		t.Fatalf("the join = %s (%v)", body, err)
	}
	if err := minter.Close(); err != nil {
		t.Fatalf("close the hub's database: %v", err)
	}

	// the satellite's session registers through its machine's batch, then all three link
	satPost := func(payload string) {
		t.Helper()
		status, body := call(t, hubBase, http.MethodPost, "/api/sat/report", "", joined.Credential, payload)
		if status != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("the sat report = %d %s", status, body)
		}
	}
	satPost(`{"reports":[{"session_id":"s-sat","last_event":"UserPromptSubmit","last_event_at":"2026-10-16T12:01:00.000Z"}]}`)
	status, body = call(t, hubBase, http.MethodPost, "/api/links", "hub-token", "",
		`{"sessions":["s-a","s-b","s-sat"],"channel":"linked","create":true,"level":"digest"}`)
	if status != 200 {
		t.Fatalf("the link answered %d: %s", status, body)
	}

	// the satellite's Stop carries the digest: the hub's peers each hold it once
	satPost(`{"reports":[{"session_id":"s-sat","last_event":"Stop","last_event_at":"2026-10-16T12:02:00.000Z","digest":"the satellite digest"},{"session_id":"s-sat","last_event":"Stop","last_event_at":"2026-10-16T12:03:00.000Z","digest":"the newer satellite digest"}]}`)
	var held []string
	for _, id := range []string{"s-a", "s-b"} {
		_, body := call(t, hubBase, http.MethodPost, "/api/sessions/"+id+"/messages", "hub-token", "", "{}")
		if !strings.Contains(body, "Digest from session s-sat via huddle: the newer satellite digest") {
			t.Fatalf("%s's take = %s", id, body)
		}
		held = append(held, id)
	}
	if len(held) != 2 {
		t.Fatalf("the digest reached %v, want both hub peers", held)
	}
}

func TestBriefSendsTheNewestDigest(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the newest digest")
	stopDigest(t, srv, "s-a", "kanban-release", "api", "an even newer digest")
	// a brief is explicit: two asks arrive as two messages, nothing waiting is dropped, and
	// both carry the digest the sender stores now — the newest
	for range 2 {
		code, out := srv.post(t, "/api/brief", `{"from":"s-a","to":"s-b"}`, nil)
		if code != 200 || !strings.Contains(out, `"route":"next_prompt"`) {
			t.Fatalf("the brief = %d %s", code, out)
		}
	}
	rows := digestRows(t, srv)
	if len(rows) != 2 || rows[0]["text"] != "an even newer digest" || rows[1]["text"] != "an even newer digest" {
		t.Fatalf("s-b holds %v, want both briefs carrying the newest digest", rows)
	}
	_, body := srv.post(t, "/api/sessions/s-b/messages", "{}", nil)
	if !strings.Contains(body, "Digest from session s-a via huddle: an even newer digest") {
		t.Errorf("the take = %s", body)
	}
	_, _, audit := srv.get(t, "/api/audit", nil)
	if !strings.Contains(audit, `"action":"brief"`) {
		t.Errorf("the audit never heard of the brief: %s", audit)
	}
}

func TestBriefWithoutADigestAnswers404(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
	for _, want := range []struct{ body, why string }{
		{`{"from":"s-b","to":"s-a"}`, "session s-b has no digest yet"},
		{`{"from":"nope","to":"s-a"}`, "no session nope"},
		{`{"from":"s-a","to":"nope"}`, "no session nope"},
		{`{"from":"s-a"}`, "name from and to"},
	} {
		code, out := srv.post(t, "/api/brief", want.body, nil)
		if code != 404 && code != 400 {
			t.Errorf("%s = %d %s, want a refusal", want.body, code, out)
		}
		if !strings.Contains(out, want.why) {
			t.Errorf("%s = %d %s, want %q", want.body, code, out, want.why)
		}
	}
}

func TestHandoverMovesOpenTasksAndLeavesTheChannel(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join api = %d %s", code, out)
	}
	if code, out := srv.op(t, "kanban-release", "join", "web", "{}"); code != 200 {
		t.Fatalf("join web = %d %s", code, out)
	}
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the handover digest")
	if code, out := srv.op(t, "kanban-release", "task_create", "api",
		`{"title":"the open work","owner":"api","id":"open-one"}`); code != 200 {
		t.Fatalf("task_create open = %d %s", code, out)
	}
	if code, out := srv.op(t, "kanban-release", "task_create", "api",
		`{"title":"the closed work","owner":"api","id":"closed-one"}`); code != 200 {
		t.Fatalf("task_create closed = %d %s", code, out)
	}
	if code, out := srv.op(t, "kanban-release", "finish", "api",
		`{"id":"closed-one","note":"evidence: the tests pass"}`); code != 200 {
		t.Fatalf("finish = %d %s", code, out)
	}
	code, out := srv.post(t, "/api/handover", `{"from":"s-a","to":"s-b"}`, nil)
	if code != 200 {
		t.Fatalf("the handover = %d %s", code, out)
	}
	var moved struct {
		Moved []string `json:"moved"`
	}
	if err := json.Unmarshal([]byte(out), &moved); err != nil || len(moved.Moved) != 1 || moved.Moved[0] != "open-one" {
		t.Fatalf("the handover moved %s (%v), want open-one alone", out, err)
	}
	// the receiver owns the open task now, the done one stays where it was
	_, tasks := srv.op(t, "kanban-release", "tasks", "web", `{"owner":"web"}`)
	list, _ := tasks["result"].([]any)
	if len(list) != 1 {
		t.Fatalf("web holds %v, want the open task", tasks["result"])
	}
	if task, _ := list[0].(map[string]any); task["id"] != "open-one" || task["owner"] != "web" {
		t.Errorf("the moved task = %v, want open-one owned by web", task)
	}
	// the brief arrived with the handover, and the audit names the act
	rows := digestRows(t, srv)
	if len(rows) != 1 || rows[0]["text"] != "the handover digest" {
		t.Fatalf("s-b holds %v, want the handover's brief", rows)
	}
	_, _, audit := srv.get(t, "/api/audit", nil)
	if !strings.Contains(audit, `"action":"handover"`) {
		t.Errorf("the audit never heard of the handover: %s", audit)
	}
	// the sender left the channel, saying where the work went
	_, events := srv.op(t, "kanban-release", "events", "web", `{"topic":"session.left"}`)
	evs, _ := events["result"].([]any)
	if len(evs) != 1 {
		t.Fatalf("the channel holds %v, want the one leave", events["result"])
	}
	if ev, _ := evs[0].(map[string]any); ev["from"] != "api" || ev["msg"] != "api left: handed over to web" {
		t.Errorf("the leave = %v, want api handing over to web", ev)
	}
}

func TestHandoverAcrossChannelsAnswers409(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "first-channel", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "second-channel", "web", "UserPromptSubmit")
	stopDigest(t, srv, "s-a", "first-channel", "api", "the digest")
	stopDigest(t, srv, "s-b", "second-channel", "web", "the other digest")
	// the brief lands in both directions, then the channel check refuses: two channels, and
	// neither row shares the other's
	for _, want := range []string{`{"from":"s-a","to":"s-b"}`, `{"from":"s-b","to":"s-a"}`} {
		code, out := srv.post(t, "/api/handover", want, nil)
		if code != 409 || !strings.Contains(out, "hand over needs both sessions in one channel") {
			t.Errorf("%s = %d %s, want 409", want, code, out)
		}
	}
}

func TestBriefAndHandoverPassTheStepUp(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
	key := webauthntest.NewSoftKey(t)
	setCookie, _ := pairDevice(t, srv, key, "iPhone")
	cookie := cookieValueOf(t, setCookie)
	stepUpStamp(t, srv, cookie, time.Now().UnixMilli()-6*60*1000)
	device := deviceHeaders(cookie)
	for _, route := range []string{"/api/brief", "/api/handover"} {
		code, out := srv.post(t, route, `{"from":"s-a","to":"s-b"}`, device)
		if code != 401 || out != stepUpRequiredBody {
			t.Errorf("%s from a device with a stale assertion = %d %s, want 401 %s",
				route, code, out, stepUpRequiredBody)
		}
	}
}

func TestTheDigestRoutesRefuseWhenTheChannelOrTheBodyIsWrong(t *testing.T) {
	srv := startServer(t, nil)
	// both rows name a channel that was never created; the link that shares their digests
	// lives in a real one
	reportSession(t, srv, "s-a", "ghost-channel", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "ghost-channel", "web", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/links",
		`{"sessions":["s-a","s-b"],"channel":"kanban-release","create":true,"level":"digest"}`, nil); code != 200 {
		t.Fatalf("the link = %d %s", code, out)
	}
	stopDigest(t, srv, "s-a", "ghost-channel", "api", "the digest")
	if rows := digestRows(t, srv); len(rows) != 1 || rows[0]["text"] != "the digest" {
		t.Fatalf("s-b holds %v, want the digest despite the ghost channel", rows)
	}
	// a handover into a channel that does not exist is refused, not guessed
	code, out := srv.post(t, "/api/handover", `{"from":"s-a","to":"s-b"}`, nil)
	if code != 404 || !strings.Contains(out, "no channel ghost-channel") {
		t.Errorf("the handover into a ghost channel = %d %s, want 404", code, out)
	}
	// a body that is not JSON is refused before the registry is asked
	code, out = srv.post(t, "/api/brief", `not json`, nil)
	if code != 400 {
		t.Errorf("a torn brief body = %d %s, want 400", code, out)
	}
}

func TestTheHandoverNeedsTheReceiverInTheChannel(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
	if code, out := srv.op(t, "kanban-release", "task_create", "api",
		`{"title":"the open work","owner":"api","id":"open-one"}`); code != 200 {
		t.Fatalf("task_create = %d %s", code, out)
	}
	// both rows name the channel, but web never joined it: the open task cannot move to it
	code, out := srv.post(t, "/api/handover", `{"from":"s-a","to":"s-b"}`, nil)
	if code != 404 || !strings.Contains(out, "has not joined channel kanban-release") {
		t.Errorf("the handover to an unjoined member = %d %s, want 404", code, out)
	}
}

func TestBriefAndHandoverRefuseWhenTheRegistryIsGone(t *testing.T) {
	srv := startServerWithoutRegistry(t)
	for _, route := range []string{"/api/brief", "/api/handover"} {
		code, _, out, err := srv.request(context.Background(), http.MethodPost, route,
			`{"from":"s-a","to":"s-b"}`, map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"})
		if err != nil || code != 500 || !strings.Contains(out, "the registry's database is unavailable") {
			t.Errorf("%s with no registry = %d %s (%v), want 500", route, code, out, err)
		}
	}
}

// TestTheDigestRoutesRefuseATornRegistry pins the torn-database lines: a Stop report never
// fails for its digest — the digest's sharing is the best effort, the report itself always
// lands — and the acts that need the missing piece refuse naming it.
func TestTheDigestRoutesRefuseATornRegistry(t *testing.T) {
	drop := func(t *testing.T, srv *childServer, dbPath, table string) {
		t.Helper()
		db, err := store.SQLite(context.Background(), dbPath)
		if err != nil {
			t.Fatalf("open %s: %v", dbPath, err)
		}
		defer db.Close()
		if err := db.Run(context.Background(), "DROP TABLE "+table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	pair := func(t *testing.T) *childServer {
		t.Helper()
		srv := startServer(t, nil)
		reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
		reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
		return srv
	}
	hubDB := func(srv *childServer) string { return filepath.Join(srv.home, "hub.db") }

	t.Run("a torn links table never fails the report and shares nothing", func(t *testing.T) {
		srv := pair(t)
		if code, out := srv.post(t, "/api/links",
			`{"sessions":["s-a","s-b"],"channel":"kanban-release","create":true,"level":"digest"}`, nil); code != 200 {
			t.Fatalf("the link = %d %s", code, out)
		}
		stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
		drop(t, srv, hubDB(srv), "links")
		stopDigest(t, srv, "s-a", "kanban-release", "api", "the next digest")
		if rows := digestRows(t, srv); len(rows) != 1 {
			t.Fatalf("s-b holds %v, want only the digest shared before the drop", rows)
		}
	})
	t.Run("a torn messages table never fails the report and refuses the acts", func(t *testing.T) {
		srv := pair(t)
		drop(t, srv, hubDB(srv), "messages")
		stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
		for _, route := range []string{"/api/brief", "/api/handover"} {
			code, out := srv.post(t, route, `{"from":"s-a","to":"s-b"}`, nil)
			if code != 500 || !strings.Contains(out, "no such table: messages") {
				t.Errorf("%s with torn messages = %d %s, want 500", route, code, out)
			}
		}
	})
	t.Run("a torn digests table refuses the brief", func(t *testing.T) {
		srv := pair(t)
		drop(t, srv, hubDB(srv), "digests")
		code, out := srv.post(t, "/api/brief", `{"from":"s-a","to":"s-b"}`, nil)
		if code != 500 || !strings.Contains(out, "no such table: digests") {
			t.Errorf("the brief with torn digests = %d %s, want 500", code, out)
		}
	})
	t.Run("a torn sessions table refuses the brief", func(t *testing.T) {
		srv := pair(t)
		drop(t, srv, hubDB(srv), "sessions")
		code, out := srv.post(t, "/api/brief", `{"from":"s-a","to":"s-b"}`, nil)
		if code != 500 || !strings.Contains(out, "no such table: sessions") {
			t.Errorf("the brief with torn sessions = %d %s, want 500", code, out)
		}
	})
	t.Run("a torn audit table refuses the brief after the message is queued", func(t *testing.T) {
		srv := pair(t)
		stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
		drop(t, srv, hubDB(srv), "audit")
		code, out := srv.post(t, "/api/brief", `{"from":"s-a","to":"s-b"}`, nil)
		if code != 500 || !strings.Contains(out, "no such table: audit") {
			t.Errorf("the brief with torn audit = %d %s, want 500", code, out)
		}
	})
	t.Run("a torn tasks table refuses the handover", func(t *testing.T) {
		srv := pair(t)
		if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
			t.Fatalf("join api = %d %s", code, out)
		}
		if code, out := srv.op(t, "kanban-release", "join", "web", "{}"); code != 200 {
			t.Fatalf("join web = %d %s", code, out)
		}
		stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
		drop(t, srv, filepath.Join(srv.dataDir, "channels", "kanban-release.db"), "tasks")
		code, out := srv.post(t, "/api/handover", `{"from":"s-a","to":"s-b"}`, nil)
		if code != 500 || !strings.Contains(out, "no such table: tasks") {
			t.Errorf("the handover with torn tasks = %d %s, want 500", code, out)
		}
	})
	t.Run("the handover needs the sender in the channel to leave it", func(t *testing.T) {
		srv := pair(t)
		if code, out := srv.post(t, "/api/links",
			`{"sessions":["s-a","s-b"],"channel":"kanban-release","create":true,"level":"digest"}`, nil); code != 200 {
			t.Fatalf("the link = %d %s", code, out)
		}
		stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
		code, out := srv.post(t, "/api/handover", `{"from":"s-a","to":"s-b"}`, nil)
		if code != 404 || !strings.Contains(out, `has not joined channel kanban-release`) {
			t.Errorf("the handover with nobody joined = %d %s, want 404", code, out)
		}
	})
	t.Run("the handover refuses when the sender has no digest", func(t *testing.T) {
		srv := pair(t)
		code, out := srv.post(t, "/api/handover", `{"from":"s-b","to":"s-a"}`, nil)
		if code != 404 || !strings.Contains(out, "session s-b has no digest yet") {
			t.Errorf("the handover with no digest = %d %s, want 404", code, out)
		}
	})
}
