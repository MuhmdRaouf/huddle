package server

// The link routes and the overhear fan-out: the app ties 2 or more sessions — here or
// on a satellite — to a channel at a level, each hears of the tie at once, a broadcast post
// reaches the overhear links alone, and an unlink stops the delivery with it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"

	"github.com/MuhmdRaouf/huddle/internal/huddle/webauthntest"
)

// patchHeaders answers the headers a level move carries: a JSON body and the root credential,
// which request() alone does not add.
func patchHeaders(srv *childServer) map[string]string {
	return map[string]string{"content-type": "application/json", "x-huddle-token": srv.token}
}

func linkedRows(t *testing.T, srv *childServer) []map[string]any {
	t.Helper()
	rows := messageRows(t, srv)
	out := []map[string]any{}
	for _, row := range rows {
		if row["from_kind"] == hub.FromChannel {
			out = append(out, row)
		}
	}
	return out
}

func TestLinkJoinsThreeSessionsAcrossHubAndSatellite(t *testing.T) {
	hubHome, satHome := t.TempDir(), t.TempDir()
	hubLog, satLog := &runLog{}, &runLog{}
	hubBase, stopHub, hubDone := startHuddleRun(t, hubHome, "hub-token", hubLog)
	t.Cleanup(func() {
		stopHub()
		select {
		case <-hubDone:
		case <-time.After(10 * time.Second):
		}
	})

	// two of this machine's sessions, in no channel: a link needs no membership
	report := func(base, token, id string) {
		t.Helper()
		status, body := call(t, base, http.MethodPost, "/api/registry/report", token, "",
			fmt.Sprintf(`{"session_id":%q,"last_event":"UserPromptSubmit","last_event_at":"2026-10-16T12:00:00.000Z"}`, id))
		if status != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("report %s = %d %s", id, status, body)
		}
	}
	report(hubBase, "hub-token", "s-local-a")
	report(hubBase, "hub-token", "s-local-b")

	// the satellite joins the hub the way join-hub does, then starts into its role
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
		t.Fatalf("the join = %s: %v", body, err)
	}
	if err := minter.Close(); err != nil {
		t.Fatalf("close the hub's database: %v", err)
	}
	h := home.Home{Root: satHome}
	if err := home.SaveConfig(h, home.Config{
		Role: "satellite", Machine: "mbp", Hub: home.HubConfig{URL: hubBase},
		Remote: home.RemoteConfig{Permissions: true, Messages: true, Allow: []string{"*"}},
	}); err != nil {
		t.Fatalf("the satellite's config: %v", err)
	}
	if err := home.SaveSatelliteSecret(context.Background(), home.KeyFiles{Dir: h.Keys()}, hubBase, joined.Credential); err != nil {
		t.Fatalf("the satellite's credential: %v", err)
	}
	satBase, stopSat, satDone := startHuddleRun(t, satHome, "sat-token", satLog)
	t.Cleanup(func() {
		stopSat()
		select {
		case <-satDone:
		case <-time.After(10 * time.Second):
		}
	})
	if !until(5*time.Second, func() bool {
		return strings.Contains(satLog.joined(), "satellite link to "+hubBase)
	}) {
		t.Fatalf("the link never came up: %s / %s", satLog.joined(), hubLog.joined())
	}
	report(satBase, "sat-token", "s-1")
	if !until(8*time.Second, func() bool {
		_, body := call(t, hubBase, http.MethodGet, "/api/registry/sessions?machine=mbp", "hub-token", "", "")
		return strings.Contains(body, "s-1")
	}) {
		t.Fatalf("the satellite's session never reached the hub: %s / %s", hubLog.joined(), satLog.joined())
	}

	// the owner links the three into a channel that does not exist yet, at the overhear level
	status, body = call(t, hubBase, http.MethodPost, "/api/links", "hub-token", "",
		`{"sessions":["s-local-a","s-local-b","s-1"],"channel":"kanban-release","create":true,"level":"overhear"}`)
	if status != 200 {
		t.Fatalf("the link answered %d: %s", status, body)
	}
	var linked struct {
		Channel  string   `json:"channel"`
		Level    string   `json:"level"`
		Sessions []string `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(body), &linked); err != nil ||
		linked.Channel != "kanban-release" || linked.Level != hub.LevelOverhear || len(linked.Sessions) != 3 {
		t.Fatalf("the link's answer = %s (%v)", body, err)
	}
	// each local session hears of the tie on its next prompt
	for _, id := range []string{"s-local-a", "s-local-b"} {
		_, body := call(t, hubBase, http.MethodPost, "/api/sessions/"+id+"/messages", "hub-token", "", "{}")
		if !strings.Contains(body, "You are linked into #kanban-release with") ||
			!strings.Contains(body, "at level overhear") {
			t.Fatalf("the notice for %s = %s", id, body)
		}
	}
	// the satellite's session hears of it through the handout its machine's poll takes
	if !until(8*time.Second, func() bool {
		_, body := call(t, satBase, http.MethodPost, "/api/sessions/s-1/messages", "sat-token", "", "{}")
		return strings.Contains(body, "You are linked into #kanban-release with")
	}) {
		t.Fatalf("the satellite's session never heard of the link: %s / %s", hubLog.joined(), satLog.joined())
	}
	// the channel's list names the three links
	_, body = call(t, hubBase, http.MethodGet, "/api/links/kanban-release", "hub-token", "", "")
	var listed struct {
		Links []hub.Link `json:"links"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil || len(listed.Links) != 3 {
		t.Fatalf("the links of kanban-release = %s (%v)", body, err)
	}
	for i, want := range []string{"s-1", "s-local-a", "s-local-b"} { // ordered by session id
		if listed.Links[i].SessionID != want || listed.Links[i].Level != hub.LevelOverhear {
			t.Errorf("link %d = %+v, want session %s at overhear", i, listed.Links[i], want)
		}
	}
}

func TestOverhearCarriesEveryBroadcastToLinkedSessions(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	reportSession(t, srv, "s-c", "kanban-release", "ops", "UserPromptSubmit")
	// the channel exists: its members joined, as sessions do
	for _, member := range []string{"api", "web", "ops"} {
		if code, out := srv.op(t, "kanban-release", "join", member, "{}"); code != 200 {
			t.Fatalf("join %s = %d %s", member, code, out)
		}
	}
	if code, out := srv.post(t, "/api/links", `{"sessions":["s-a","s-b","s-c"],"channel":"kanban-release","level":"overhear"}`, nil); code != 200 {
		t.Fatalf("the link = %d %s", code, out)
	}
	// a broadcast reaches the linked sessions that are not the speaker
	if code, out := srv.op(t, "kanban-release", "publish", "api", `{"topic":"build.ready","msg":"the build is green"}`); code != 200 {
		t.Fatalf("publish = %d %s", code, out)
	}
	rows := linkedRows(t, srv)
	if len(rows) != 2 {
		t.Fatalf("the post reached %d linked sessions (%v), want 2", len(rows), rows)
	}
	for _, row := range rows {
		if id := row["session_id"]; id != "s-b" && id != "s-c" {
			t.Errorf("the post reached %v, want s-b and s-c only", id)
		}
	}
	// what the listen hook hands the session carries the channel's and the speaker's name
	for _, id := range []string{"s-b", "s-c"} {
		_, body := srv.post(t, "/api/sessions/"+id+"/messages", "{}", nil)
		if !strings.Contains(body, "In #kanban-release, api said: the build is green") {
			t.Errorf("the take for %s = %s", id, body)
		}
	}
	// the speaker's own session hears nothing of its own post but its link notice
	_, body := srv.post(t, "/api/sessions/s-a/messages", "{}", nil)
	if strings.Contains(body, "In #kanban-release") {
		t.Errorf("the speaker's take carried its own post: %s", body)
	}
	// a send addressed to one session is no broadcast: no linked session overhears it
	if code, out := srv.op(t, "kanban-release", "send", "api", `{"to":"web","msg":"just for web"}`); code != 200 {
		t.Fatalf("send = %d %s", code, out)
	}
	if rows = linkedRows(t, srv); len(rows) != 2 {
		t.Fatalf("an addressed send reached a linked session: %v", rows)
	}
}

func TestDigestAndOnRequestLinksHearNoPosts(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-d1", "kanban-release", "d-one", "UserPromptSubmit")
	reportSession(t, srv, "s-d2", "kanban-release", "d-two", "UserPromptSubmit")
	reportSession(t, srv, "s-o1", "kanban-release", "o-one", "UserPromptSubmit")
	reportSession(t, srv, "s-o2", "kanban-release", "o-two", "UserPromptSubmit")
	reportSession(t, srv, "s-h", "kanban-release", "hear", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	for _, link := range []struct{ sessions, level string }{
		{`["s-d1","s-d2"]`, hub.LevelDigest},
		{`["s-o1","s-o2"]`, hub.LevelOnRequest},
		{`["s-h","s-a"]`, hub.LevelOverhear},
	} {
		body := fmt.Sprintf(`{"sessions":%s,"channel":"kanban-release","level":%q}`, link.sessions, link.level)
		if code, out := srv.post(t, "/api/links", body, nil); code != 200 {
			t.Fatalf("the %s link = %d %s", link.level, code, out)
		}
	}
	if code, out := srv.op(t, "kanban-release", "publish", "api", `{"topic":"build.ready","msg":"green"}`); code != 200 {
		t.Fatalf("publish = %d %s", code, out)
	}
	// the overhear link heard it; the digest and on-request links heard nothing
	rows := linkedRows(t, srv)
	if len(rows) != 1 || rows[0]["session_id"] != "s-h" {
		t.Fatalf("the post reached %v, want s-h alone", rows)
	}
}

func TestUnlinkStopsDeliveryAtOnce(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/links", `{"sessions":["s-a","s-b"],"channel":"kanban-release","level":"overhear"}`, nil); code != 200 {
		t.Fatalf("the link = %d %s", code, out)
	}
	if code, out := srv.op(t, "kanban-release", "publish", "api", `{"topic":"build.ready","msg":"first"}`); code != 200 {
		t.Fatalf("publish = %d %s", code, out)
	}
	if rows := linkedRows(t, srv); len(rows) != 1 || rows[0]["session_id"] != "s-b" {
		t.Fatalf("the first post reached %v, want s-b", rows)
	}
	// the owner undoes the tie: the next post never reaches the session again
	code, _, out, err := srv.request(context.Background(), http.MethodDelete, "/api/links/kanban-release/s-b", "",
		map[string]string{"x-huddle-token": srv.token})
	if err != nil || code != 200 || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("the unlink = %d %s (%v)", code, out, err)
	}
	if code, out := srv.op(t, "kanban-release", "publish", "api", `{"topic":"build.ready","msg":"second"}`); code != 200 {
		t.Fatalf("publish = %d %s", code, out)
	}
	if rows := linkedRows(t, srv); len(rows) != 1 {
		t.Fatalf("after the unlink the post reached %v, want nothing new", rows)
	}
	_, _, body := srv.get(t, "/api/links/kanban-release", nil)
	if !strings.Contains(body, "s-a") || strings.Contains(body, "s-b") {
		t.Fatalf("the links after the unlink = %s", body)
	}
}

func TestLinkRefusals(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	t.Run("no array of sessions", func(t *testing.T) {
		for _, body := range []string{`{"channel":"kanban-release"}`, `{"sessions":"s-a","channel":"kanban-release"}`, `{"sessions":[7],"channel":"kanban-release"}`} {
			code, out := srv.post(t, "/api/links", body, nil)
			if code != 400 || !strings.Contains(out, "sessions: an array of session ids") {
				t.Errorf("%s = %d %s, want 400", body, code, out)
			}
		}
	})
	t.Run("fewer than two distinct sessions", func(t *testing.T) {
		for _, sessions := range []string{`["s-a"]`, `["s-a","s-a"]`} {
			code, out := srv.post(t, "/api/links",
				`{"sessions":`+sessions+`,"channel":"kanban-release","level":"overhear"}`, nil)
			if code != 400 || !strings.Contains(out, "link takes 2 or more sessions") {
				t.Errorf("%s = %d %s, want 400", sessions, code, out)
			}
		}
	})
	t.Run("a level that is none of the three", func(t *testing.T) {
		code, out := srv.post(t, "/api/links",
			`{"sessions":["s-a","s-b"],"channel":"kanban-release","level":"shout"}`, nil)
		if code != 400 || !strings.Contains(out, "level is overhear, digest or on-request") {
			t.Fatalf("a bad level = %d %s, want 400", code, out)
		}
	})
	t.Run("an empty level means digest", func(t *testing.T) {
		code, out := srv.post(t, "/api/links", `{"sessions":["s-a","s-b"],"channel":"digest-here","create":true}`, nil)
		if code != 200 || !strings.Contains(out, `"level":"digest"`) {
			t.Fatalf("a levelless link = %d %s, want 200 digest", code, out)
		}
	})
	t.Run("a session the registry does not know", func(t *testing.T) {
		code, out := srv.post(t, "/api/links",
			`{"sessions":["s-a","nope"],"channel":"kanban-release","level":"overhear"}`, nil)
		if code != 404 || !strings.Contains(out, "no session nope") {
			t.Fatalf("an unknown session = %d %s, want 404", code, out)
		}
	})
	t.Run("a channel that does not exist and is not asked for", func(t *testing.T) {
		code, out := srv.post(t, "/api/links",
			`{"sessions":["s-a","s-b"],"channel":"no-such-channel","level":"overhear"}`, nil)
		if code != 404 || !strings.Contains(out, "no channel no-such-channel") {
			t.Fatalf("an unknown channel = %d %s, want 404", code, out)
		}
	})
	t.Run("linking and unlinking from a paired device owe the step-up", func(t *testing.T) {
		key := webauthntest.NewSoftKey(t)
		setCookie, _ := pairDevice(t, srv, key, "iPhone")
		cookie := cookieValueOf(t, setCookie)
		stepUpStamp(t, srv, cookie, time.Now().UnixMilli()-6*60*1000)
		device := deviceHeaders(cookie)
		code, out := srv.post(t, "/api/links",
			`{"sessions":["s-a","s-b"],"channel":"kanban-release","level":"overhear"}`, device)
		if code != 401 || out != stepUpRequiredBody {
			t.Fatalf("the device's link = %d %s, want 401 %s", code, out, stepUpRequiredBody)
		}
		code, _, out, err := srv.request(context.Background(), http.MethodDelete,
			"/api/links/kanban-release/s-b", "", device)
		if err != nil || code != 401 || out != stepUpRequiredBody {
			t.Fatalf("the device's unlink = %d %s (%v), want 401 %s", code, out, err, stepUpRequiredBody)
		}
		// a level move is no dangerous act: it passes the step-up and answers the registry
		move := map[string]string{"Cookie": device["Cookie"], "x-huddle-token": "", "content-type": "application/json"}
		code, _, out, err = srv.request(context.Background(), http.MethodPatch,
			"/api/links/kanban-release/s-b", `{"level":"digest"}`, move)
		if err != nil || code == 401 {
			t.Fatalf("the device's level move = %d %s (%v), want past the step-up", code, out, err)
		}
	})
}

// TestTheLinkRoutesRefuseWhenTheRegistryIsGone pins the refusals on a home whose hub.db cannot
// open (a file sits in its place): no route links, reads or moves a level without the registry,
// and a channel op runs on without fanning anything out.
func TestTheLinkRoutesRefuseWhenTheRegistryIsGone(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(blocked, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("the blocker: %v", err)
	}
	srv := startServer(t, map[string]string{"HUDDLE_HOME": blocked})
	for _, route := range []struct {
		name, method, path, body string
	}{{"link", http.MethodPost, "/api/links", `{"sessions":["s-a","s-b"],"channel":"c"}`},
		{"level move", http.MethodPatch, "/api/links/c/s-a", `{"level":"digest"}`},
		{"unlink", http.MethodDelete, "/api/links/c/s-a", ""},
		{"links of", http.MethodGet, "/api/links/c", ""}} {
		code, _, out, err := srv.request(context.Background(), route.method, route.path, route.body,
			map[string]string{"x-huddle-token": srv.token, "content-type": "application/json"})
		if err != nil || code != 500 || !strings.Contains(out, "the registry's database is unavailable") {
			t.Errorf("the %s with no registry = %d %s (%v), want 500", route.name, code, out, err)
		}
	}
}

// TestTheLinksListGroupsByChannel pins GET /api/links: an empty registry answers no channels,
// and every link comes back under its own channel in the query's order, each carrying the time
// of the session's newest digest when it has one. The read owes no step-up.
func TestTheLinksListGroupsByChannel(t *testing.T) {
	srv := startServer(t, nil)
	_, _, body := srv.get(t, "/api/links", nil)
	if body != `{"channels":[]}` {
		t.Fatalf("the links list with nothing linked = %s, want an empty group list", body)
	}
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	reportSession(t, srv, "s-c", "other", "ops", "UserPromptSubmit")
	reportSession(t, srv, "s-d", "other", "docs", "UserPromptSubmit")
	if code, out := srv.post(t, "/api/links", `{"sessions":["s-a","s-b"],"channel":"kanban-release","create":true,"level":"digest"}`, nil); code != 200 {
		t.Fatalf("the first link = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/links", `{"sessions":["s-c","s-d"],"channel":"other","create":true,"level":"overhear"}`, nil); code != 200 {
		t.Fatalf("the second link = %d %s", code, out)
	}
	// s-a has digested; the others have not
	stopDigest(t, srv, "s-a", "kanban-release", "api", "the digest")
	_, _, body = srv.get(t, "/api/links", nil)
	var listed struct {
		Channels []struct {
			Channel string        `json:"channel"`
			Links   []hub.LinkRow `json:"links"`
		} `json:"channels"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("the links list = %s (%v)", body, err)
	}
	if len(listed.Channels) != 2 {
		t.Fatalf("the links list = %s, want 2 channel groups", body)
	}
	first, second := listed.Channels[0], listed.Channels[1]
	if first.Channel != "kanban-release" || len(first.Links) != 2 {
		t.Fatalf("the first group = %+v, want kanban-release with 2 links", first)
	}
	if first.Links[0].SessionID != "s-a" || first.Links[0].Level != hub.LevelDigest || first.Links[0].DigestAt == "" {
		t.Errorf("s-a's row = %+v, want digest with a digest time", first.Links[0])
	}
	if first.Links[1].SessionID != "s-b" || first.Links[1].DigestAt != "" {
		t.Errorf("s-b's row = %+v, want no digest time", first.Links[1])
	}
	if second.Channel != "other" || len(second.Links) != 2 ||
		second.Links[0].SessionID != "s-c" || second.Links[1].SessionID != "s-d" {
		t.Fatalf("the second group = %+v, want other with s-c then s-d", second)
	}
}

func TestPatchChangesTheLevel(t *testing.T) {
	srv := startServer(t, nil)
	reportSession(t, srv, "s-a", "kanban-release", "api", "UserPromptSubmit")
	reportSession(t, srv, "s-b", "kanban-release", "web", "UserPromptSubmit")
	if code, out := srv.op(t, "kanban-release", "join", "api", "{}"); code != 200 {
		t.Fatalf("join = %d %s", code, out)
	}
	if code, out := srv.post(t, "/api/links", `{"sessions":["s-a","s-b"],"channel":"kanban-release","level":"overhear"}`, nil); code != 200 {
		t.Fatalf("the link = %d %s", code, out)
	}
	code, _, out, err := srv.request(context.Background(), http.MethodPatch,
		"/api/links/kanban-release/s-b", `{"level":"digest"}`, patchHeaders(srv))
	if err != nil || code != 200 {
		t.Fatalf("the level move = %d %s (%v)", code, out, err)
	}
	var moved struct {
		Channel   string `json:"channel"`
		SessionID string `json:"session_id"`
		Level     string `json:"level"`
	}
	if err := json.Unmarshal([]byte(out), &moved); err != nil ||
		moved.Channel != "kanban-release" || moved.SessionID != "s-b" || moved.Level != hub.LevelDigest {
		t.Fatalf("the level move = %s (%v)", out, err)
	}
	_, _, body := srv.get(t, "/api/links/kanban-release", nil)
	if !strings.Contains(body, `"session_id":"s-b"`) || !strings.Contains(body, `"level":"digest"`) {
		t.Fatalf("the links after the move = %s", body)
	}
	// a link that is not there moves nothing, and a level that is none of the three is refused
	code, _, out, err = srv.request(context.Background(), http.MethodPatch,
		"/api/links/kanban-release/s-c", `{"level":"digest"}`, patchHeaders(srv))
	if err != nil || code != 404 || !strings.Contains(out, "no link") {
		t.Fatalf("a move with no link = %d %s (%v), want 404 no link", code, out, err)
	}
	code, _, out, err = srv.request(context.Background(), http.MethodPatch,
		"/api/links/kanban-release/s-b", `{"level":"shout"}`, patchHeaders(srv))
	if err != nil || code != 400 || !strings.Contains(out, "level is overhear, digest or on-request") {
		t.Fatalf("a move to a bad level = %d %s (%v), want 400", code, out, err)
	}
	// an unlink with no link answers 404 too
	code, _, out, err = srv.request(context.Background(), http.MethodDelete,
		"/api/links/kanban-release/s-c", "", map[string]string{"x-huddle-token": srv.token})
	if err != nil || code != 404 || !strings.Contains(out, "no link") {
		t.Fatalf("an unlink with no link = %d %s (%v), want 404", code, out, err)
	}
}
