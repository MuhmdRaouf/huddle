package satellite

// The link against a fake hub on a free loopback port: what the machine says while the hub is
// down lands in the outbox and replays in order; a decision from the hub lands on the machine's
// own item under this machine's switches; a message lands on its session. Never a real hub,
// never a real port.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// fakeHub is the hub's satellite routes, as the link sees them: every batch and every result is
// recorded, and the handouts a poll takes come from a queue the test fills.
type fakeHub struct {
	mu       sync.Mutex
	reports  []reportBatch
	results  []string
	handouts []hub.SatHandout
	srv      *httptest.Server
}

func startFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	f := &fakeHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sat/report", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var batch reportBatch
		if err := json.Unmarshal(raw, &batch); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.reports = append(f.reports, batch)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/sat/next", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		out := f.handouts
		f.handouts = nil
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Handouts []hub.SatHandout `json:"handouts"`
		}{out})
	})
	mux.HandleFunc("/api/sat/result/", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.results = append(f.results, r.URL.Path+" "+string(raw))
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// linkOn builds one link over a fresh home and registry, paced for a test.
func linkOn(t *testing.T, hubURL string) (*Link, *hub.DB) {
	t.Helper()
	return linkOnWith(t, hubURL, nil)
}

// linkOnWith builds one link whose Deliver is the given call, nil for a link without one.
func linkOnWith(t *testing.T, hubURL string, deliver func(context.Context, int64, hub.MessageIn) string) (*Link, *hub.DB) {
	t.Helper()
	h := home.Home{Root: t.TempDir()}
	db, err := hub.Open(context.Background(), filepath.Join(h.Root, "hub.db"))
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := home.SaveConfig(h, home.Config{
		Role: "satellite", Machine: "mbp",
		Remote: home.RemoteConfig{Permissions: true, Messages: true, Allow: []string{"*"}},
	}); err != nil {
		t.Fatalf("config: %v", err)
	}
	woken := []int64{}
	var mu sync.Mutex
	// the link's clock sits half a minute after the items the tests create (12:00:00), so an
	// item it reads is open whatever the wall clock says
	clock := time.Date(2026, 10, 16, 12, 0, 30, 0, time.UTC)
	link := New(Options{
		Reg: db, Home: h, HubURL: hubURL, Credential: "hsat_test-credential-0001",
		FlushEvery: 20 * time.Millisecond, PollWait: time.Second,
		Now: func() time.Time { return clock },
		Wake: func(itemID int64) {
			mu.Lock()
			woken = append(woken, itemID)
			mu.Unlock()
		},
		Deliver: deliver,
	})
	return link, db
}

// reportsOf answers every report batch the fake hub has taken so far.
func reportsOf(f *fakeHub) []reportBatch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]reportBatch{}, f.reports...)
}

func resultsOf(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.results...)
}

// TestFlushReplaysTheOutboxInOrder proves the offline half of SPEC 6.7: with the hub down, a
// flush lands the batch in outbox.jsonl; when the hub answers again, the same batch is posted
// first and the file is spent — and the cursor moved with the hand-off, so the hub never hears
// the same event twice.
func TestFlushReplaysTheOutboxInOrder(t *testing.T) {
	dead := startFakeHub(t)
	deadURL := dead.srv.URL
	dead.srv.Close() // the hub is down from the first flush on

	link, db := linkOn(t, deadURL)
	ctx := context.Background()
	link.StartCursor(ctx)
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-1", Machine: "mbp", LastEvent: "UserPromptSubmit",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if err := link.flush(ctx); err == nil {
		t.Fatal("a flush at a dead hub succeeded")
	}
	lines, err := link.outboxLines()
	if err != nil || len(lines) != 1 {
		t.Fatalf("the outbox holds %d lines, %v, want 1", len(lines), err)
	}

	f := startFakeHub(t)
	link.hubURL = f.srv.URL
	if err := link.flush(ctx); err != nil {
		t.Fatalf("the replay flush: %v", err)
	}
	batches := reportsOf(f)
	if len(batches) != 1 || len(batches[0].Reports) != 1 || batches[0].Reports[0].SessionID != "s-1" {
		t.Fatalf("the replayed batches = %+v, want the one queued report", batches)
	}
	if lines, err = link.outboxLines(); err != nil || len(lines) != 0 {
		t.Fatalf("the outbox after the replay holds %d lines, %v, want 0", len(lines), err)
	}
	// the cursor left with the batch: a second flush posts nothing new
	if err := link.flush(ctx); err != nil {
		t.Fatalf("the second flush: %v", err)
	}
	if got := reportsOf(f); len(got) != 1 {
		t.Fatalf("the second flush posted %d more batches, want none", len(got)-1)
	}
	if _, err := link.outboxLines(); err != nil {
		t.Fatalf("the outbox: %v", err)
	}
}

// TestApplyDecisionFollowsThisMachinesSwitches proves SPEC 6.6 on the machine: an allow within
// the allow list lands on the item and is answered delivered; an allow outside it is recorded
// as this machine's deny — the asking hook's poll ends — and answered refused with the reason.
func TestApplyDecisionFollowsThisMachinesSwitches(t *testing.T) {
	f := startFakeHub(t)
	link, db := linkOn(t, f.srv.URL)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	id, err := db.CreatePermissionItem(ctx, "s-1", "Bash", []byte(`{"command":"npm test"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := db.KeyOfItem(ctx, id)
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key, SessionID: "s-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	result, reason := link.apply(ctx, hub.SatHandout{ID: 1, Kind: hub.SatDecision, Payload: payload})
	if result != hub.SatDelivered || reason != "" {
		t.Fatalf("the allowed decision = (%s, %q), want delivered", result, reason)
	}
	item, err := db.PermissionItemByID(ctx, id, now.Add(time.Second))
	if err != nil || item.State != hub.StateAnswered || item.Decision != "allow" {
		t.Fatalf("the item = %s/%s, %v, want answered allow", item.State, item.Decision, err)
	}

	// the same decision again (a re-offered handout) still reads delivered, never a second write
	result, _ = link.apply(ctx, hub.SatHandout{ID: 1, Kind: hub.SatDecision, Payload: payload})
	if result != hub.SatDelivered {
		t.Fatalf("the re-applied decision = %s, want delivered", result)
	}

	// an allow outside this machine's own list is refused, and the item reads the deny
	id2, err := db.CreatePermissionItem(ctx, "s-2", "Bash", []byte(`{"command":"curl example.com"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key2, _ := db.KeyOfItem(ctx, id2)
	h := home.Home{Root: link.home.Root}
	cfg, _ := home.LoadConfig(h)
	cfg.Remote.Allow = []string{"Read(*)"}
	if err := home.SaveConfig(h, cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	payload2, _ := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key2, SessionID: "s-2", Decision: "allow"})
	result, reason = link.apply(ctx, hub.SatHandout{ID: 2, Kind: hub.SatDecision, Payload: payload2})
	if result != hub.SatRefused || reason == "" {
		t.Fatalf("the refused decision = (%s, %q), want refused with a reason", result, reason)
	}
	item2, _ := db.PermissionItemByID(ctx, id2, now.Add(time.Second))
	if item2.State != hub.StateAnswered || item2.Decision != "deny" || item2.Message != reason {
		t.Fatalf("the refused item = %s/%s/%q, want the machine's deny with the reason",
			item2.State, item2.Decision, item2.Message)
	}
}

// TestApplyMessageQueuesItForTheSession proves the message half: a handout lands on the
// machine's own session, and the result says delivered.
func TestApplyMessageQueuesItForTheSession(t *testing.T) {
	f := startFakeHub(t)
	link, db := linkOn(t, f.srv.URL)
	ctx := context.Background()
	payload, err := hub.EncodeSatPayload(hub.SatMessageBody{SessionID: "s-1", Text: "ship it"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, reason := link.apply(ctx, hub.SatHandout{ID: 5, Kind: hub.SatMessage, Payload: payload})
	if result != hub.SatDelivered || reason != "" {
		t.Fatalf("the message = (%s, %q), want delivered", result, reason)
	}
	taken, err := db.TakeMessages(ctx, "s-1", "2026-10-16T12:00:01.000Z")
	if err != nil || len(taken) != 1 || !strings.Contains(taken[0], "ship it") {
		t.Fatalf("the session's messages = %v, %v, want the owner's words", taken, err)
	}
	// the machine's own switch rules here: remote messages off refuses the handout
	h := home.Home{Root: link.home.Root}
	cfg, _ := home.LoadConfig(h)
	cfg.Remote.Messages = false
	if err := home.SaveConfig(h, cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 6, Kind: hub.SatMessage, Payload: payload})
	if result != hub.SatRefused || reason == "" {
		t.Fatalf("the refused message = (%s, %q), want refused with a reason", result, reason)
	}
}

// messageDelivery is one call the link made to its server's Deliver.
type messageDelivery struct {
	id int64
	in hub.MessageIn
}

// messageRow answers the registry's one message row, read straight from the link's own hub.db.
func messageRow(t *testing.T, h home.Home) map[string]any {
	t.Helper()
	db, err := store.SQLite(context.Background(), filepath.Join(h.Root, "hub.db"))
	if err != nil {
		t.Fatalf("open the registry: %v", err)
	}
	defer db.Close()
	rows, err := db.All(context.Background(), "SELECT id, state FROM messages")
	if err != nil {
		t.Fatalf("read the message row: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("the registry holds %d message rows, %v, want 1", len(rows), err)
	}
	return rows[0]
}

// TestApplyMessageDeliversAQueuedMessageAtOnce proves the at-once half of the message road: the
// moment the owner's words are queued the link hands them to the machine's own server, with the
// id the registry answered, and the handout still reads delivered while the row waits as queued.
func TestApplyMessageDeliversAQueuedMessageAtOnce(t *testing.T) {
	f := startFakeHub(t)
	var calls []messageDelivery
	link, _ := linkOnWith(t, f.srv.URL, func(_ context.Context, id int64, in hub.MessageIn) string {
		calls = append(calls, messageDelivery{id: id, in: in})
		return hub.RouteInbox
	})
	ctx := context.Background()
	payload, err := hub.EncodeSatPayload(hub.SatMessageBody{SessionID: "s-1", Text: "ship it"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, reason := link.apply(ctx, hub.SatHandout{ID: 5, Kind: hub.SatMessage, Payload: payload})
	if result != hub.SatDelivered || reason != "" {
		t.Fatalf("the message = (%s, %q), want delivered", result, reason)
	}
	if len(calls) != 1 {
		t.Fatalf("Deliver ran %d times, want once: %+v", len(calls), calls)
	}
	got := calls[0]
	if got.in.SessionID != "s-1" || got.in.FromKind != hub.FromOwner || got.in.Text != "ship it" {
		t.Fatalf("Deliver heard %+v, want the owner's words for s-1", got.in)
	}
	row := messageRow(t, link.home)
	queued, ok := row["id"].(int64)
	if !ok || got.id != queued {
		t.Fatalf("Deliver heard id %d, want the queued row's %v", got.id, row["id"])
	}
	if row["state"] != "pending" {
		t.Fatalf("the queued row = %v, want it still pending for the session's next prompt", row)
	}
}

// TestAMessagesOffLinkNeverCallsDeliver proves the machine's switch rules the at-once road too:
// with remote messages off the handout is refused there and the server is never asked.
func TestAMessagesOffLinkNeverCallsDeliver(t *testing.T) {
	f := startFakeHub(t)
	called := false
	link, _ := linkOnWith(t, f.srv.URL, func(context.Context, int64, hub.MessageIn) string {
		called = true
		return hub.RouteInbox
	})
	h := home.Home{Root: link.home.Root}
	cfg, err := home.LoadConfig(h)
	if err != nil {
		t.Fatalf("load the config: %v", err)
	}
	cfg.Remote.Messages = false
	if err := home.SaveConfig(h, cfg); err != nil {
		t.Fatalf("save the config: %v", err)
	}
	payload, err := hub.EncodeSatPayload(hub.SatMessageBody{SessionID: "s-1", Text: "ship it"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, reason := link.apply(context.Background(), hub.SatHandout{ID: 7, Kind: hub.SatMessage, Payload: payload})
	if result != hub.SatRefused || reason == "" {
		t.Fatalf("the message = (%s, %q), want refused with a reason", result, reason)
	}
	if called {
		t.Fatal("Deliver ran with the machine's remote messages off")
	}
}

// TestPollSettlesEveryHandout proves one poll takes what the hub hands and answers each: the
// fake hub records the results under the handouts' own ids.
func TestPollSettlesEveryHandout(t *testing.T) {
	f := startFakeHub(t)
	link, db := linkOn(t, f.srv.URL)
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	id, err := db.CreatePermissionItem(ctx, "s-1", "Read", []byte(`"/tmp/notes"`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := db.KeyOfItem(ctx, id)
	payload, _ := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key, SessionID: "s-1", Decision: "deny"})
	msg, _ := hub.EncodeSatPayload(hub.SatMessageBody{SessionID: "s-1", Text: "hello"})
	f.mu.Lock()
	f.handouts = []hub.SatHandout{
		{ID: 11, Kind: hub.SatDecision, Payload: payload},
		{ID: 12, Kind: hub.SatMessage, Payload: msg},
	}
	f.mu.Unlock()
	if err := link.poll(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	results := resultsOf(f)
	if len(results) != 2 ||
		!strings.Contains(results[0], "/api/sat/result/11") || !strings.Contains(results[0], hub.SatDelivered) ||
		!strings.Contains(results[1], "/api/sat/result/12") || !strings.Contains(results[1], hub.SatDelivered) {
		t.Fatalf("the results = %v, want both handouts settled delivered", results)
	}
}
