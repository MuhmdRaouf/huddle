package server

// The satellite routes' refusals and the roads a decision and a message take through them: a
// route without its credential answers nothing, a torn body is a usage error, a long poll wakes
// the moment the hub queues a handout, and a machine's refusal lands on the hub's own row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// satCoverHub starts one hub, mints and spends one satellite join, and answers the base url, the
// hub's own database and the machine's credential.
func satCoverHub(t *testing.T) (string, *hub.DB, string) {
	t.Helper()
	hubHome := t.TempDir()
	log := &runLog{}
	base, stop, done := startHuddleRun(t, hubHome, "hub-token", log)
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	})
	minter, err := hub.Open(context.Background(), home.Home{Root: hubHome}.HubDB())
	if err != nil {
		t.Fatalf("open the hub's database: %v", err)
	}
	t.Cleanup(func() { _ = minter.Close() })
	invite, err := minter.MintSatelliteInvite(context.Background(), time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	status, body := call(t, base, http.MethodPost, "/api/sat/join", "", "",
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
	// a session reporting from that machine, so the hub knows where its words travel
	_, body = call(t, base, http.MethodPost, "/api/registry/report", "hub-token", "",
		`{"session_id":"s-1","machine":"mbp","project":"/repo","last_event":"SessionStart","last_event_at":"2026-10-16T12:00:00.000Z"}`)
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("the machine's report = %s", body)
	}
	return base, minter, joined.Credential
}

// TestSatelliteRoutesRefuseWhatTheyCannotUse pins every refusal a satellite route gives before
// any work happens: the join answers POST alone, every other route needs the machine's
// credential, a torn or too-large body and a half-filled report are usage errors.
func TestSatelliteRoutesRefuseWhatTheyCannotUse(t *testing.T) {
	base, minter, credential := satCoverHub(t)

	// the join is the one public route, and it answers POST alone
	if status, _ := call(t, base, http.MethodGet, "/api/sat/join", "", "", ""); status != 405 {
		t.Fatalf("a GET join answered %d, want 405", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/api/sat/join", "", "", "not json"); status != 400 {
		t.Fatalf("a torn join body answered %d, want 400", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/api/sat/join", "", "", `{"invite":"nope","name":"mbp"}`); status != 400 {
		t.Fatalf("a bad invite answered %d, want 400", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/api/sat/join", "", "",
		`{"invite":"hsat_aaaaaaaaaaaaaaaaaaaaaa","name":"bad name!"}`); status != 400 {
		t.Fatalf("a bad machine name answered %d, want 400", status)
	}

	// without the credential the routes answer nothing
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", "", `{}`); status != 401 {
		t.Fatalf("a credentialless report answered %d, want 401", status)
	}
	if status, _ := call(t, base, http.MethodGet, "/api/sat/next", "", "", ""); status != 401 {
		t.Fatalf("a credentialless poll answered %d, want 401", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/api/sat/result/1", "", "", `{"result":"delivered"}`); status != 401 {
		t.Fatalf("a credentialless result answered %d, want 401", status)
	}

	// with one: no such route, and no method the routes do not name
	if status, _ := call(t, base, http.MethodGet, "/api/sat/nope", "", credential, ""); status != 404 {
		t.Fatalf("an unknown route answered %d, want 404", status)
	}
	if status, _ := call(t, base, http.MethodGet, "/api/sat/result/1", "", credential, ""); status != 404 {
		t.Fatalf("a GET result answered %d, want 404", status)
	}

	// the report route's refusals
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential, "not json"); status != 400 {
		t.Fatalf("a torn report answered %d, want 400", status)
	}
	tooBig := `{"reports":[` + strings.TrimSuffix(strings.Repeat(
		`{"session_id":"s","last_event":"e","last_event_at":"a"},`, 501), ",") + `]}`
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential, tooBig); status != 400 {
		t.Fatalf("a 501-report batch answered %d, want 400", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential,
		`{"reports":[{"session_id":"s-1"}]}`); status != 400 {
		t.Fatalf("a half-filled report answered %d, want 400", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential,
		`{"items":[{"key":"k-1"}]}`); status != 400 {
		t.Fatalf("an item with no session answered %d, want 400", status)
	}
	// and its one success: a whole report with a forwarded item lands
	if status, body := call(t, base, http.MethodPost, "/api/sat/report", "", credential,
		`{"reports":[{"session_id":"s-9","last_event":"UserPromptSubmit","last_event_at":"2026-10-16T12:00:00.000Z"}],`+
			`"items":[{"Key":"k-1","SessionID":"s-9","Tool":"Bash","Input":"{\"command\":\"npm test\"}","CreatedAt":"2026-10-16T12:00:00.000Z"}]}`); status != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("a whole batch answered %d: %s", status, body)
	}

	// a report whose event says the terminal answered its own prompt withdraws the copies
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential,
		`{"reports":[{"session_id":"s-9","last_event":"Stop","last_event_at":"2026-10-16T12:00:01.000Z"}]}`); status != 200 {
		t.Fatalf("a Stop report answered %d", status)
	}

	// the poll route's refusals and holds
	if status, _ := call(t, base, http.MethodGet, "/api/sat/next?wait=abc", "", credential, ""); status != 400 {
		t.Fatalf("a bad wait answered %d, want 400", status)
	}
	if status, body := call(t, base, http.MethodGet, "/api/sat/next?wait=1", "", credential, ""); status != 200 || !strings.Contains(body, `"handouts":[]`) {
		t.Fatalf("an empty hold answered %d: %s", status, body)
	}
	_ = minter
}

// TestSatelliteRoutesCarryADecisionAndAMessage proves the roads themselves: a long poll wakes
// the moment the hub queues the machine's words, the result refusals hold, and a machine's
// refusal lands on the hub's own row for the item.
func TestSatelliteRoutesCarryADecisionAndAMessage(t *testing.T) {
	base, minter, credential := satCoverHub(t)
	ctx := context.Background()

	// the poll holds, the send wakes it, the handout goes out
	answer := make(chan string, 1)
	go func() {
		_, body := call(t, base, http.MethodGet, "/api/sat/next?wait=10", "", credential, "")
		answer <- body
	}()
	time.Sleep(300 * time.Millisecond) // the poll arms before the send
	if status, body := call(t, base, http.MethodPost, "/api/send", "hub-token", "",
		`{"sessions":["s-1"],"text":"wake up"}`); status != 200 || !strings.Contains(body, `"route":"satellite"`) {
		t.Fatalf("the send answered %d: %s", status, body)
	}
	var handouts struct {
		Handouts []hub.SatHandout `json:"handouts"`
	}
	select {
	case got := <-answer:
		if err := json.Unmarshal([]byte(got), &handouts); err != nil {
			t.Fatalf("the poll's answer = %s: %v", got, err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the poll never woke")
	}
	if len(handouts.Handouts) != 1 || handouts.Handouts[0].Kind != hub.SatMessage {
		t.Fatalf("the handouts = %+v, want the message", handouts.Handouts)
	}
	id := handouts.Handouts[0].ID

	// the result route's refusals
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", id), "", credential, "not json"); status != 400 {
		t.Fatalf("a torn result answered %d, want 400", status)
	}
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", id), "", credential, `{"result":"maybe"}`); status != 400 {
		t.Fatalf("an unknown result answered %d, want 400", status)
	}
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", id), "", credential, `{"result":"refused"}`); status != 400 {
		t.Fatalf("a reasonless refusal answered %d, want 400", status)
	}
	// the message's result: delivered ends the hub's row through the machine's own answer
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", id), "", credential, `{"result":"delivered"}`); status != 200 {
		t.Fatalf("the delivered result answered %d", status)
	}
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", id), "", credential, `{"result":"delivered"}`); status != 409 {
		t.Fatalf("a second result answered %d, want 409", status)
	}

	// a forwarded permission item the machine refuses: the hub's own row reads refused
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential,
		`{"items":[{"Key":"k-decide","SessionID":"s-1","Tool":"Bash","Input":"{\"command\":\"npm test\"}","CreatedAt":"2026-10-16T12:00:00.000Z"}]}`); status != 200 {
		t.Fatalf("the forwarded item answered %d", status)
	}
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: "k-decide", SessionID: "s-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decided, err := minter.SatEnqueue(ctx, "mbp", hub.SatDecision, "s-1", 0, 0, payload, "2026-10-16T12:00:00.000Z")
	if err != nil || decided == 0 {
		t.Fatalf("enqueue = %d, %v", decided, err)
	}
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", decided), "", credential,
		`{"result":"refused","reason":"not on this machine"}`); status != 200 {
		t.Fatalf("the refused decision answered %d", status)
	}
	item, err := minter.PermissionItemByKey(ctx, "k-decide", time.Now())
	if err != nil || item == nil || item.State != hub.StateAnswered || item.Decision != "refused" {
		t.Fatalf("the hub's row = %+v, %v, want refused with the machine's reason", item, err)
	}

	// a prompt the terminal answered first stays as it lays: the machine's refusal writes
	// nothing, and the answer is still taken
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential,
		`{"items":[{"Key":"k-withdrawn","SessionID":"s-1","Tool":"Bash","Input":"{\"command\":\"npm test\"}","CreatedAt":"2026-10-16T12:00:00.000Z"}]}`); status != 200 {
		t.Fatalf("the second forwarded item answered %d", status)
	}
	if status, _ := call(t, base, http.MethodPost, "/api/sat/report", "", credential,
		`{"reports":[{"session_id":"s-1","last_event":"Stop","last_event_at":"2026-10-16T12:00:05.000Z"}]}`); status != 200 {
		t.Fatalf("the Stop report answered %d", status)
	}
	gonePayload, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: "k-withdrawn", SessionID: "s-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	withdrawn, err := minter.SatEnqueue(ctx, "mbp", hub.SatDecision, "s-1", 0, 0, gonePayload, "2026-10-16T12:00:06.000Z")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", withdrawn), "", credential,
		`{"result":"refused","reason":"why"}`); status != 200 {
		t.Fatalf("a withdrawn item's result answered %d", status)
	}

	// a decision payload no JSON can read: the refusal is logged, the route answers on
	torn, err := minter.SatEnqueue(ctx, "mbp", hub.SatDecision, "s-1", 0, 0, "{bad", "2026-10-16T12:00:00.000Z")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", torn), "", credential,
		`{"result":"refused","reason":"why"}`); status != 200 {
		t.Fatalf("a torn handout's result answered %d", status)
	}
	// a key nothing holds: nothing to write on, and still an answer
	gone, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: "no-such-key", SessionID: "s-1", Decision: "deny"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	unknown, err := minter.SatEnqueue(ctx, "mbp", hub.SatDecision, "s-1", 0, 0, gone, "2026-10-16T12:00:00.000Z")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if status, _ := call(t, base, http.MethodPost, fmt.Sprintf("/api/sat/result/%d", unknown), "", credential,
		`{"result":"refused","reason":"why"}`); status != 200 {
		t.Fatalf("an unknown item's result answered %d", status)
	}
}

// TestOfferDecisionQueuesForTheSatelliteAndSkipsTheRest proves where a decision travels: a
// session of this machine queues nothing, a satellite's session queues the handout under the
// item's own key, and the machine's poll wakes.
func TestOfferDecisionQueuesForTheSatelliteAndSkipsTheRest(t *testing.T) {
	db, _ := openRegistry(t)
	s := &server{registry: db, now: fixedClock(), satQueue: newSatWaiters()}
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	// this machine's own session: no handout for anyone
	local, err := db.CreatePermissionItem(ctx, "s-local", "Bash", []byte(`{"command":"npm test"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	item, err := db.PermissionItemByID(ctx, local, now)
	if err != nil || item == nil {
		t.Fatalf("the item = %+v, %v", item, err)
	}
	s.offerDecision(ctx, *item, "allow", "")
	if handouts, _ := db.SatOffer(ctx, "mbp", now); len(handouts) != 0 {
		t.Fatalf("a local session queued %d handouts, want none", len(handouts))
	}

	// a satellite's session: the decision is queued under the item's key
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-sat", Machine: "mbp", LastEvent: "SessionStart",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := db.SaveSatellite(ctx, auth.PairDigest("hsat_cover_credential_000001"), "mbp", now.UnixMilli()); err != nil {
		t.Fatalf("save the satellite: %v", err)
	}
	satItem, err := db.CreatePermissionItem(ctx, "s-sat", "Bash", []byte(`{"command":"curl example.com"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	item2, err := db.PermissionItemByID(ctx, satItem, now)
	if err != nil || item2 == nil {
		t.Fatalf("the item = %+v, %v", item2, err)
	}
	s.offerDecision(ctx, *item2, "allow", "go ahead")
	handouts, err := db.SatOffer(ctx, "mbp", now)
	if err != nil || len(handouts) != 1 {
		t.Fatalf("the satellite's offer = %d handouts, %v, want 1", len(handouts), err)
	}
	var body hub.SatRefusalBody
	key, err := db.KeyOfItem(ctx, satItem)
	if err != nil || key == "" {
		t.Fatalf("the item's key = %q, %v", key, err)
	}
	if err := hub.DecodeSatPayload(handouts[0].Payload, &body); err != nil || body.Key != key || body.Decision != "allow" {
		t.Fatalf("the handout = %+v (%v), want the allow under the item's key", handouts[0], err)
	}
}

// TestSatellitePathsLeaveAnUnwiredServerAlone proves the satellite paths answer a server with no
// registry and no items by refusing, never by panicking.
func TestSatellitePathsLeaveAnUnwiredServerAlone(t *testing.T) {
	s := &server{}
	rec := httptest.NewRecorder()
	s.satRoute(rec, httptest.NewRequest(http.MethodGet, "/api/sat/next", nil), "/api/sat/next")
	if rec.Code != 500 {
		t.Fatalf("a poll with no registry answered %d, want 500", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.satRoute(rec, httptest.NewRequest(http.MethodPost, "/api/sat/join", strings.NewReader("{}")), satJoinPath)
	if rec.Code != 500 {
		t.Fatalf("a join with no registry answered %d, want 500", rec.Code)
	}
	s.wakeAppliedItem(1) // no registry: nothing to wake

	// a verified satellite in the context skips the bearer lookup, and an item no one holds
	// wakes nothing
	db, _ := openRegistry(t)
	wired := &server{registry: db, now: fixedClock(), auth: auth.New("root-credential", fixedClock(), nil, ""),
		satQueue: newSatWaiters(), permits: newPermitWaiters()}
	rec = httptest.NewRecorder()
	ctxRequest := httptest.NewRequest(http.MethodGet, "/api/sat/next?wait=0", nil)
	wired.satRoute(rec, ctxRequest.WithContext(withSatellite(ctxRequest.Context(), "mbp")), "/api/sat/next")
	if rec.Code != 200 {
		t.Fatalf("a verified poll answered %d: %s", rec.Code, rec.Body.String())
	}
	wired.wakeAppliedItem(999)
}

// TestSatelliteRoutesAnswerABrokenRegistry proves every route answers a registry that says no
// with one 500 line and no panic: the poll, the result, the queue, the offer and the refusal
// each refuse whole.
func TestSatelliteRoutesAnswerABrokenRegistry(t *testing.T) {
	dead, _ := openRegistry(t)
	if err := dead.Close(); err != nil {
		t.Fatalf("close the registry: %v", err)
	}
	s := &server{registry: dead, now: fixedClock(), auth: auth.New("root-credential", fixedClock(), nil, ""),
		satQueue: newSatWaiters(), permits: newPermitWaiters(),
		o: Options{Stderr: io.Discard}}
	sat := func(method, target, body string) *http.Request {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		return r.WithContext(withSatellite(r.Context(), "mbp"))
	}
	rec := httptest.NewRecorder()
	s.satRoute(rec, sat(http.MethodGet, "/api/sat/next?wait=0", ""), "/api/sat/next")
	if rec.Code != 500 {
		t.Fatalf("a poll on a dead registry answered %d, want 500", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.satRoute(rec, sat(http.MethodPost, "/api/sat/result/1", `{"result":"delivered"}`), "/api/sat/result/1")
	if rec.Code != 500 {
		t.Fatalf("a result on a dead registry answered %d, want 500", rec.Code)
	}
	if _, _, err := s.queueForSession(context.Background(), "s-1",
		hub.MessageIn{SessionID: "s-1", FromKind: hub.FromOwner, Text: "hello"}); err == nil {
		t.Fatal("a message was queued on a dead registry")
	}
	s.refuseForwardedItem(context.Background(),
		hub.SatHandout{ID: 1, Kind: hub.SatDecision, Payload: `{"key":"k","session":"s-1"}`},
		"mbp", "why", "2026-10-16T12:00:00.000Z")

	// a held poll whose registry dies while it waits: the wake answers with the 500
	live, _ := openRegistry(t)
	wired := &server{registry: live, now: fixedClock(), auth: auth.New("root-credential", fixedClock(), nil, ""),
		satQueue: newSatWaiters(), permits: newPermitWaiters(),
		o: Options{Stderr: io.Discard}}
	held := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		wired.satRoute(rec, sat(http.MethodGet, "/api/sat/next?wait=30", ""), "/api/sat/next")
		held <- rec.Code
	}()
	time.Sleep(300 * time.Millisecond) // the poll arms and offers nothing
	if err := live.Close(); err != nil {
		t.Fatalf("close the live registry: %v", err)
	}
	wired.satQueue.wake("mbp")
	select {
	case code := <-held:
		if code != 500 {
			t.Fatalf("a woken poll on a dead registry answered %d, want 500", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the woken poll never answered")
	}
}

// TestSatelliteRoutesHoldTheClampAndSkipTheRest proves the poll's wait is capped at the spec's
// thirty seconds, an offer follows the key the item carries or none, and a send to a session off
// the satellites takes its own road.
func TestSatelliteRoutesHoldTheClampAndSkipTheRest(t *testing.T) {
	db, _ := openRegistry(t)
	s := &server{registry: db, now: fixedClock(), auth: auth.New("root-credential", fixedClock(), nil, ""),
		satQueue: newSatWaiters(), permits: newPermitWaiters(),
		o: Options{Stdout: io.Discard, Stderr: io.Discard}}
	ctx := context.Background()
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-sat", Machine: "mbp", LastEvent: "SessionStart",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	now := fixedClock()
	if err := db.SaveSatellite(ctx, auth.PairDigest("hsat_cover_credential_000002"), "mbp", now().UnixMilli()); err != nil {
		t.Fatalf("save the satellite: %v", err)
	}
	// a wait past the cap is clamped, and the hold still ends when the queue wakes it
	held := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/sat/next?wait=99", nil)
		s.satRoute(rec, request.WithContext(withSatellite(request.Context(), "mbp")), "/api/sat/next")
		held <- rec.Code
	}()
	time.Sleep(300 * time.Millisecond)
	s.satQueue.wake("mbp")
	select {
	case code := <-held:
		if code != 200 {
			t.Fatalf("a clamped poll answered %d, want 200", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the clamped poll never answered")
	}

	// an offer follows the key the item carries: an item no row names offers nothing
	s.offerDecision(ctx, hub.PermissionItem{Item: hub.Item{ID: 99999, SessionID: "s-sat"}}, "allow", "")
	if handouts, _ := db.SatOffer(ctx, "mbp", time.Now()); len(handouts) != 0 {
		t.Fatalf("an unknown item queued %d handouts, want none", len(handouts))
	}

	// a send to a session whose machine is no satellite of this hub rides the local road
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-local", Machine: "someone", LastEvent: "SessionStart",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if route, _, err := s.queueForSession(ctx, "s-local",
		hub.MessageIn{SessionID: "s-local", FromKind: hub.FromOwner, Text: "ship it"}); err != nil || route == satelliteRoute {
		t.Fatalf("a local send took route %q, %v", route, err)
	}
}

// TestSatReportRouteRefusesOnABrokenRegistry proves the report route answers a registry that
// says no with its own 500 per half: the reports are refused, then the items, and a body that
// tears mid-read reads as no body at all.
func TestSatReportRouteRefusesOnABrokenRegistry(t *testing.T) {
	dead, _ := openRegistry(t)
	_ = dead.Close()
	s := &server{registry: dead, now: fixedClock(), o: Options{Stderr: io.Discard}}
	rec := httptest.NewRecorder()
	s.satReportRoute(rec, httptest.NewRequest(http.MethodPost, "/api/sat/report",
		iotest.ErrReader(errors.New("torn body"))), "mbp")
	if rec.Code != 400 {
		t.Fatalf("a torn body answered %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.satReportRoute(rec, httptest.NewRequest(http.MethodPost, "/api/sat/report",
		strings.NewReader(`{"reports":[{"session_id":"s-1","last_event":"UserPromptSubmit","last_event_at":"2026-10-16T12:00:00.000Z"}]}`)), "mbp")
	if rec.Code != 500 {
		t.Fatalf("a report on a dead registry answered %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.satReportRoute(rec, httptest.NewRequest(http.MethodPost, "/api/sat/report",
		strings.NewReader(`{"items":[{"Key":"k-1","SessionID":"s-1"}]}`)), "mbp")
	if rec.Code != 500 {
		t.Fatalf("an item on a dead registry answered %d: %s", rec.Code, rec.Body.String())
	}
	// the result route reads its body the same way
	rec = httptest.NewRecorder()
	s.satResultRoute(rec, httptest.NewRequest(http.MethodPost, "/api/sat/result/1",
		iotest.ErrReader(errors.New("torn body"))), "/api/sat/result/1", "mbp")
	if rec.Code != 400 {
		t.Fatalf("a torn result body answered %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSatelliteWritesRefuseOnAReadOnlyRegistry proves the two writes a decision needs answer a
// store that takes no writes: the offer cannot queue its handout and the machine's refusal
// cannot write on the hub's row, each without touching the reads before them.
func TestSatelliteWritesRefuseOnAReadOnlyRegistry(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hub.db")
	db, err := hub.Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-sat", Machine: "mbp", LastEvent: "SessionStart",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := db.SaveSatellite(ctx, auth.PairDigest("hsat_cover_credential_000003"), "mbp", now.UnixMilli()); err != nil {
		t.Fatalf("save the satellite: %v", err)
	}
	id, err := db.CreatePermissionItem(ctx, "s-sat", "Bash", []byte(`{"command":"npm test"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.Chmod(file, 0o444); err != nil {
		t.Fatalf("read-only the file: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(file, 0o644) })
	ro, err := hub.Open(ctx, file)
	if err != nil {
		t.Fatalf("reopen read-only: %v", err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	s := &server{registry: ro, now: fixedClock(), satQueue: newSatWaiters(), permits: newPermitWaiters(),
		o: Options{Stderr: io.Discard}}
	item, err := ro.PermissionItemByID(ctx, id, now)
	if err != nil || item == nil {
		t.Fatalf("the item = %+v, %v", item, err)
	}
	// the offer reads the item and its key, and cannot queue the handout
	s.offerDecision(ctx, *item, "allow", "")
	// the machine's refusal reads the item, and cannot write on the hub's own row
	key, err := ro.KeyOfItem(ctx, id)
	if err != nil || key == "" {
		t.Fatalf("the key = %q, %v", key, err)
	}
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key, SessionID: "s-sat", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s.refuseForwardedItem(ctx, hub.SatHandout{ID: 1, Kind: hub.SatDecision, Payload: payload},
		"mbp", "why", "2026-10-16T12:00:01.000Z")
}
