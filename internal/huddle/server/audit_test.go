package server

// The audit log over the real server: every browser sign-in appends exactly one row, the read
// answers newest first with a cursor, and a sign-in whose row cannot be written is refused. The
// alerts fan out in-process with their stored read state.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// signInBrowser redeems one login code the way a browser does, answering the sign-in's status
// and the cookie it set (empty when it set none).
func signInBrowser(t *testing.T, srv *childServer, code string) (int, string) {
	t.Helper()
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	request, err := http.NewRequest(http.MethodGet, srv.base+"/?code="+code, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	cookie := response.Header.Get("Set-Cookie")
	if cookie != "" {
		cookie = strings.SplitN(cookie, ";", 2)[0]
	}
	return response.StatusCode, cookie
}

// loginCode mints one sign-in code as the credential headers name.
func loginCode(t *testing.T, srv *childServer, headers map[string]string) string {
	t.Helper()
	_, out := srv.post(t, "/api/login", "{}", headers)
	code, _ := bodyMap(t, out)["code"].(string)
	if code == "" {
		t.Fatalf("no login code: %s", out)
	}
	return code
}

func TestEverySignInWritesExactlyOneAuditRow(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}

	// the root's browser signs in
	status, cookie := signInBrowser(t, srv, loginCode(t, srv, root))
	if status != 303 {
		t.Fatalf("the root's sign-in = %d, want 303", status)
	}
	// a member's browser signs in: the join, the member's own code, then the redemption
	_, out := srv.post(t, "/api/tokens", "{}", root)
	invite, _ := bodyMap(t, out)["token"].(string)
	_, out = srv.post(t, "/api/join", `{"token": "`+invite+`", "name": "dev"}`, nil)
	cred, _ := bodyMap(t, out)["credential"].(string)
	dev := map[string]string{"x-huddle-token": cred}
	devStatus, devCookie := signInBrowser(t, srv, loginCode(t, srv, dev))
	if devStatus != 303 {
		t.Fatalf("the member's sign-in = %d, want 303", devStatus)
	}

	_, _, body := srv.get(t, "/api/audit", root)
	parsed := bodyMap(t, body)
	entries, _ := parsed["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("the audit holds %d rows after two sign-ins, want exactly one per sign-in: %s", len(entries), body)
	}
	first, _ := entries[0].(map[string]any) // newest first
	second, _ := entries[1].(map[string]any)
	if first["action"] != "signin" || first["actor"] != "dev" || first["target"] != "dashboard" {
		t.Errorf("the member's sign-in row = %v, want dev signing in to the dashboard", first)
	}
	if second["action"] != "signin" || second["actor"] != "owner" {
		t.Errorf("the root's sign-in row = %v, want the owner's", second)
	}
	if at, _ := first["at"].(string); at == "" || !strings.HasSuffix(at, "Z") {
		t.Errorf("the row's time = %v, want an ISO timestamp", first["at"])
	}
	if first["detail"] != nil {
		t.Errorf("a sign-in's detail = %v, want none: nothing but the fact belongs in the log", first["detail"])
	}
	// the root's browser reads the log; a member and a member's browser may not
	if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/audit", "", map[string]string{"Cookie": cookie}); code != 200 {
		t.Fatalf("the owner's browser reads the audit = %d, want 200", code)
	}
	if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/audit", "", dev); code != 403 {
		t.Fatalf("a member reads the audit = %d, want 403", code)
	}
	if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/audit", "",
		map[string]string{"Cookie": devCookie}); code != 403 {
		t.Fatalf("a member's browser reads the audit = %d, want 403", code)
	}
	if code, _, _ := srv.get(t, "/api/audit", nil); code != 200 {
		t.Fatalf("the root session reads the audit = %d, want 200", code)
	}
}

func TestTheAuditReadPaginatesNewestFirstAndBoundsItsLimit(t *testing.T) {
	srv := startServer(t, nil)
	root := map[string]string{"x-huddle-token": srv.token}
	ids := []float64{}
	for range 3 {
		signInBrowser(t, srv, loginCode(t, srv, root))
		_, _, body := srv.get(t, "/api/audit", root)
		entries, _ := bodyMap(t, body)["entries"].([]any)
		newest, _ := entries[0].(map[string]any)
		ids = append(ids, newest["id"].(float64))
	}
	_, _, body := srv.get(t, "/api/audit", root)
	entries, _ := bodyMap(t, body)["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("the whole log = %d rows, want 3", len(entries))
	}
	if entries[0].(map[string]any)["id"] != ids[2] || entries[2].(map[string]any)["id"] != ids[0] {
		t.Fatalf("the log answers oldest first: %s, want the newest row first", body)
	}
	_, _, body = srv.get(t, "/api/audit?limit=2", root)
	if entries, _ = bodyMap(t, body)["entries"].([]any); len(entries) != 2 ||
		entries[0].(map[string]any)["id"] != ids[2] || entries[1].(map[string]any)["id"] != ids[1] {
		t.Fatalf("limit=2 = %s, want the two newest rows", body)
	}
	before := int(ids[1])
	_, _, body = srv.get(t, "/api/audit?before="+strconv.Itoa(before), root)
	if entries, _ = bodyMap(t, body)["entries"].([]any); len(entries) != 1 ||
		entries[0].(map[string]any)["id"] != ids[0] {
		t.Fatalf("before=%d = %s, want only the oldest row", before, body)
	}
	for _, query := range []string{"limit=501", "limit=0", "limit=-1", "limit=two", "before=0", "before=x"} {
		if code, _, out := srv.get(t, "/api/audit?"+query, root); code != 400 {
			t.Fatalf("%s = %d %s, want 400: the read takes a limit of at most 500 and a positive cursor", query, code, out)
		}
	}
	if code, _, _ := srv.get(t, "/api/audit?limit=500", root); code != 200 {
		t.Fatalf("limit=500 = %d, want 200: the bound itself is allowed", code)
	}
	// no credential: the browser's 401, like every other /api read
	if code, _, _, _ := srv.request(context.Background(), http.MethodGet, "/api/audit", "", nil); code != 401 {
		t.Fatalf("the audit without a credential = %d, want 401", code)
	}
}

func TestASignInThatCannotBeAuditedIsRefused(t *testing.T) {
	home := tempDir("huddle-no-hub-db-")
	// a directory where hub.db would open: the registry cannot start, and a sign-in that would
	// go unaudited is refused instead
	if err := os.MkdirAll(filepath.Join(home, "hub.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := startServer(t, map[string]string{"HUDDLE_HOME": home})
	root := map[string]string{"x-huddle-token": srv.token}
	code, cookie := signInBrowser(t, srv, loginCode(t, srv, root))
	if code != 503 {
		t.Fatalf("a sign-in with no audit log = %d, want 503: security events are never unaudited", code)
	}
	if cookie != "" {
		t.Fatalf("the refused sign-in set a cookie anyway: %q", cookie)
	}
}

// fixedClock answers one never-moving moment, so the stored rows carry times the test names.
func fixedClock() func() time.Time {
	return func() time.Time { return time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC) }
}

// openRegistry opens one hub database in a temp home, closed with the test.
func openRegistry(t *testing.T) (*hub.DB, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "hub.db")
	db, err := hub.Open(bg, file)
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, file
}

// auditServer is one server face over a temp registry and a fixed clock, for the in-process
// paths no route of the landed cards reaches yet. It answers the registry's file beside it.
func auditServer(t *testing.T) (*server, string) {
	t.Helper()
	registry, file := openRegistry(t)
	s := &server{registry: registry, alerts: newAlertFanout(), now: fixedClock(),
		auth: auth.New("root-credential", fixedClock(), nil, "")}
	return s, file
}

// dropAlertsTable breaks the alerts store under one audit server, for the failure paths.
func dropAlertsTable(t *testing.T, file string) {
	t.Helper()
	db, err := store.SQLite(bg, file)
	if err != nil {
		t.Fatalf("open the registry file: %v", err)
	}
	defer db.Close()
	if err := db.Run(bg, "DROP TABLE alerts"); err != nil {
		t.Fatalf("drop the alerts table: %v", err)
	}
}

func TestRaiseAlertStoresTheRowAndFansItOut(t *testing.T) {
	s, _ := auditServer(t)
	events := make(chan AlertEvent, 4)
	s.alerts.subscribe(func(e AlertEvent) { events <- e })
	id, err := s.raiseAlert(bg, "alert.device_paired", pairDetail{Device: "phone"})
	if err != nil {
		t.Fatalf("raise the alert: %v", err)
	}
	select {
	case e := <-events:
		if e.Type != "alert.device_paired" || e.ID != id || e.At != "2026-10-16T09:00:00.000Z" {
			t.Fatalf("the event = %+v, want alert.device_paired with the row's own id and time", e)
		}
		if e.Body == nil || e.Body.EncodeJSON() != `{"device":"phone"}` {
			t.Fatalf("the event's body = %v, want the stored body", e.Body)
		}
	default:
		t.Fatal("no alert event reached the subscriber")
	}
	rows, err := s.registry.Alerts(bg, hub.AlertsFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("the alerts table = %d rows (%v), want the raised alert stored", len(rows), err)
	}
	if rows[0].Kind != "alert.device_paired" || rows[0].ReadAt != nil {
		t.Fatalf("the stored alert = %+v, want alert.device_paired and unread until the owner reads it", rows[0])
	}
}

func TestEmitNewestAlertPassesAStoredAlertOn(t *testing.T) {
	s, _ := auditServer(t)
	// the pairing ceremony stores its alert inside its own transaction; this is the row the
	// route passes on afterwards
	if _, err := s.registry.RaiseAlert(bg, s.now().UnixMilli(), "alert.device_paired",
		pairDetail{Device: "tablet"}); err != nil {
		t.Fatalf("store the pairing alert: %v", err)
	}
	events := make(chan AlertEvent, 4)
	s.alerts.subscribe(func(e AlertEvent) { events <- e })
	s.emitNewestAlert(bg)
	select {
	case e := <-events:
		if e.Type != "alert.device_paired" || e.ID != 1 ||
			e.Body == nil || e.Body.EncodeJSON() != `{"device":"tablet"}` {
			t.Fatalf("the passed-on event = %+v, want the stored row as it lays", e)
		}
	default:
		t.Fatal("the stored alert never fanned out")
	}
	// a read alert is not passed on again: the push sender hears an alert once
	if _, err := s.registry.MarkAlertsRead(bg, 1, s.now().UnixMilli()); err != nil {
		t.Fatalf("mark the alert read: %v", err)
	}
	s.emitNewestAlert(bg)
	select {
	case e := <-events:
		t.Fatalf("a read alert fanned out again: %+v", e)
	default:
	}
}

func TestTheAuditDetailCarriesNoSecretOfThisRun(t *testing.T) {
	s, _ := auditServer(t)
	note := auditNote{Note: "allowed with root-credential and hcred_ab12cd34"}
	if _, err := s.audit(bg, "owner", "allow", "item-1", note); err != nil {
		t.Fatalf("record the action: %v", err)
	}
	rows, err := s.registry.Audit(bg, hub.AuditFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("the audit = %d rows (%v), want the recorded action", len(rows), err)
	}
	if rows[0].Detail == nil || rows[0].Detail.EncodeJSON() != `{"note":"allowed with <redacted> and <redacted>"}` {
		t.Fatalf("the detail = %v, want every credential of this run stripped", rows[0].Detail)
	}
}

func TestRecordingAndRaisingRefuseARegistryThatIsNotThere(t *testing.T) {
	s := &server{alerts: newAlertFanout(), now: fixedClock(),
		auth: auth.New("root-credential", fixedClock(), nil, "")}
	if _, err := s.audit(bg, "owner", "signin", "dashboard", nil); err == nil {
		t.Error("auditing without the registry succeeds, want the refusal")
	}
	if _, err := s.raiseAlert(bg, "alert.remote_allow", nil); err == nil {
		t.Error("raising an alert without the registry succeeds, want the refusal")
	}
	if err := s.auditSignin(bg, "any-credential"); err == nil {
		t.Error("a sign-in without the registry is audited, want the refusal")
	}
	// and nothing fans out of a registry that is not there
	events := make(chan AlertEvent, 1)
	s.alerts.subscribe(func(e AlertEvent) { events <- e })
	s.emitNewestAlert(bg)
	select {
	case e := <-events:
		t.Fatalf("an alert fanned out without the registry: %+v", e)
	default:
	}
}

func TestRecordingRefusesADetailThatCannotBeEncoded(t *testing.T) {
	s, file := auditServer(t)
	if _, err := s.audit(bg, "owner", "allow", "item-1", make(chan int)); err == nil {
		t.Error("an unencodable detail is stored, want the refusal")
	}
	if _, err := s.raiseAlert(bg, "alert.remote_allow", make(chan int)); err == nil {
		t.Error("an unencodable detail is alerted, want the refusal")
	}
	// a store that cannot take the row names its failure; the alert never fans half-out
	dropAlertsTable(t, file)
	if _, err := s.raiseAlert(bg, "alert.remote_allow", nil); err == nil {
		t.Error("raising against a broken store succeeds, want the error named")
	}
}

func TestEmitNewestAlertStaysQuietWithNothingUnread(t *testing.T) {
	s, _ := auditServer(t)
	events := make(chan AlertEvent, 4)
	s.alerts.subscribe(func(e AlertEvent) { events <- e })
	// nothing raised: nothing fans out, and the miss is not an error anyone hears
	s.emitNewestAlert(bg)
	select {
	case e := <-events:
		t.Fatalf("an empty store fanned out %+v", e)
	default:
	}
}

func TestTheAlertFanoutReachesEverySubscriberAndSurvivesAPanic(t *testing.T) {
	fanout := newAlertFanout()
	got := make(chan AlertEvent, 4)
	cancel := fanout.subscribe(func(e AlertEvent) { got <- e })
	// a listener that panics costs nothing: the rest still hear the alert
	fanout.subscribe(func(AlertEvent) { panic("one broken listener") })
	fanout.emit(AlertEvent{Type: "alert.remote_allow", ID: 3})
	select {
	case e := <-got:
		if e.Type != "alert.remote_allow" || e.ID != 3 {
			t.Fatalf("the event = %+v, want the one emitted", e)
		}
	default:
		t.Fatal("the subscriber heard nothing")
	}
	cancel()
	fanout.emit(AlertEvent{Type: "alert.remote_allow", ID: 4})
	select {
	case e := <-got:
		t.Fatalf("a cancelled subscriber still heard %+v", e)
	default:
	}
}

// pairDetail is one pairing alert's body.
type pairDetail struct {
	Device string `json:"device"`
}

// auditNote is one decision's detail as a caller hands it over.
type auditNote struct {
	Note string `json:"note"`
}
