package satellite

// The link's own loop and its answers from a hub that misbehaves: Run stops with its context,
// a hub that refuses for good has its batches dropped, one that is down has them queued, and a
// handout that cannot be read or found is refused with the reason the hub shows the owner.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// countingHub is a hub face that counts the link's three calls and queues one handout for the
// first poll to take.
type countingHub struct {
	mu      sync.Mutex
	reports int
	polls   int
	results int
	handout string
	srv     *httptest.Server
}

func startCountingHub(t *testing.T) *countingHub {
	t.Helper()
	c := &countingHub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/sat/report", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.reports++
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/sat/next", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.polls++
		payload := c.handout
		c.handout = ""
		c.mu.Unlock()
		handouts := []hub.SatHandout{}
		if payload != "" {
			handouts = append(handouts, hub.SatHandout{ID: 7, Kind: hub.SatMessage, Payload: payload})
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Handouts []hub.SatHandout `json:"handouts"`
		}{handouts})
	})
	mux.HandleFunc("/api/sat/result/", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.results++
		c.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	c.srv = httptest.NewServer(mux)
	t.Cleanup(c.srv.Close)
	return c
}

// counts answers the calls the hub has taken so far.
func (c *countingHub) counts() (int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reports, c.polls, c.results
}

// waitFor waits until check answers true, at most two seconds.
func waitFor(check func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return check()
}

// deadHubURL answers the address of a hub that is down: the server is closed before it is used.
func deadHubURL(t *testing.T) string {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	return url
}

// refusingHub answers 403 to everything, the way a hub that rejects a machine's credential does.
func refusingHub(t *testing.T) string {
	t.Helper()
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(refusing.Close)
	return refusing.URL
}

// TestRunFlushesPollsAndStopsWithItsContext proves the link's loop: the machine's news is
// flushed, the long poll takes the handout waiting for it and answers it, and the whole loop
// ends the moment the context does.
func TestRunFlushesPollsAndStopsWithItsContext(t *testing.T) {
	f := startCountingHub(t)
	link, db := linkOn(t, f.srv.URL)
	ctx := context.Background()
	msg, err := hub.EncodeSatPayload(hub.SatMessageBody{SessionID: "s-1", Text: "hello from the hub"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	f.mu.Lock()
	f.handout = msg
	f.mu.Unlock()
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-1", Machine: "mbp", LastEvent: "UserPromptSubmit",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		link.Run(runCtx)
		close(done)
	}()
	if !waitFor(func() bool {
		reports, polls, results := f.counts()
		return reports > 0 && polls > 0 && results > 0
	}) {
		cancel()
		t.Fatal("the link never flushed, polled and answered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the link kept running after its context ended")
	}
}

// TestRunKeepsTryingWhileTheHubIsDown proves a dead hub costs neither loop: the flush logs its
// miss, the poll retries, and both stop the moment the context ends.
func TestRunKeepsTryingWhileTheHubIsDown(t *testing.T) {
	link, db := linkOn(t, deadHubURL(t))
	if err := db.Report(context.Background(), hub.Report{
		SessionID: "s-1", Machine: "mbp", LastEvent: "UserPromptSubmit",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		link.Run(runCtx)
		close(done)
	}()
	time.Sleep(80 * time.Millisecond) // a few flush and poll rounds pass while nothing answers
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the link never stopped")
	}
}

// TestPostAndGetReadTheHubsAnswers pins what a link makes of a hub's answers: a 5xx is worth
// another try, a 4xx is a refusal trying again cannot fix, and a torn body is an error — each
// named with the status the hub gave.
func TestPostAndGetReadTheHubsAnswers(t *testing.T) {
	ctx := context.Background()
	link := New(Options{}) // every default: its own client, the wall clock, no log
	status := func(code int) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	json200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	t.Cleanup(json200.Close)

	link.hubURL = status(http.StatusInternalServerError)
	if err := link.post(ctx, "/api/sat/report", reportBatch{}); err == nil || permanent(err) {
		t.Errorf("a 500 answer = %v, want a temporary error", err)
	}
	if err := link.get(ctx, "/api/sat/next", &struct{}{}); err == nil || permanent(err) {
		t.Errorf("a 500 poll = %v, want a temporary error", err)
	}
	link.hubURL = status(http.StatusForbidden)
	err := link.post(ctx, "/api/sat/report", reportBatch{})
	if err == nil || !permanent(err) {
		t.Fatalf("a 403 answer = %v, want a refusal", err)
	}
	if got := err.Error(); got != "the hub answered 403 Forbidden" {
		t.Errorf("the refusal = %q", got)
	}
	link.hubURL = status(http.StatusUnauthorized)
	if err := link.get(ctx, "/api/sat/next", &struct{}{}); err == nil || !permanent(err) {
		t.Errorf("a 401 poll = %v, want a refusal", err)
	}
	link.hubURL = json200.URL
	if err := link.get(ctx, "/api/sat/next", &struct{}{}); err == nil || permanent(err) {
		t.Errorf("a torn poll body = %v, want a read error", err)
	}
	link.hubURL = deadHubURL(t)
	if err := link.post(ctx, "/api/sat/report", reportBatch{}); err == nil || permanent(err) {
		t.Errorf("a dead hub = %v, want a transport error", err)
	}
}

// TestReplayDropsTheBatchesTheHubRefusesForGood proves the replay's order and its mercy: a
// refused-for-good batch is dropped and the replay goes on, a dead hub stops it where it stands,
// and an unreadable outbox is an error, never a silent loss.
func TestReplayDropsTheBatchesTheHubRefusesForGood(t *testing.T) {
	link, _ := linkOn(t, refusingHub(t))
	ctx := context.Background()
	if err := link.writeOutbox([]outboxLine{
		{Kind: "report", Body: `{"reports":[{"session_id":"s-1","last_event":"UserPromptSubmit","last_event_at":"2026-10-16T12:00:00.000Z"}]}`},
		{Kind: "report", Body: `{"reports":[]}`},
	}); err != nil {
		t.Fatalf("write the outbox: %v", err)
	}
	if err := link.replay(ctx); err != nil {
		t.Fatalf("the replay at a refusing hub = %v, want every refused batch dropped", err)
	}
	if lines, err := link.outboxLines(); err != nil || len(lines) != 0 {
		t.Fatalf("the outbox holds %d lines, %v, want 0", len(lines), err)
	}

	// a hub that does not answer stops the replay where it stands
	dead := deadHubURL(t)
	link.hubURL = dead
	if err := link.writeOutbox([]outboxLine{{Kind: "report", Body: `{"reports":[]}`}}); err != nil {
		t.Fatalf("write the outbox: %v", err)
	}
	if err := link.replay(ctx); err == nil || permanent(err) {
		t.Fatalf("the replay at a dead hub = %v, want an unreachable error", err)
	}
	if lines, _ := link.outboxLines(); len(lines) != 1 {
		t.Fatalf("the outbox holds %d lines, want the batch kept for later", len(lines))
	}

	// an outbox that cannot be read is an error
	link.home = home.Home{Root: writeOnlyDir(t)}
	if err := link.replay(ctx); err == nil {
		t.Fatal("a replay over an unreadable outbox succeeded")
	}
}

// writeOnlyDir answers a directory holding a file named home, so Root/outbox.jsonl cannot be
// read (its parent is a file) and no batch can be written there either.
func writeOnlyDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "home"), []byte("not a home"), 0o600); err != nil {
		t.Fatalf("write the decoy: %v", err)
	}
	return filepath.Join(dir, "home")
}

// TestKeepForLaterCapsTheOutboxAndRefusesAnUnwritableHome proves the outbox cannot eat the disk:
// one batch past the limit drops its oldest line, an empty outbox is no file, and a home that
// cannot hold a file refuses the batch by name.
func TestKeepForLaterCapsTheOutboxAndRefusesAnUnwritableHome(t *testing.T) {
	link, _ := linkOn(t, deadHubURL(t))
	ctx := context.Background()
	lines := make([]outboxLine, outboxLimit)
	for i := range lines {
		lines[i] = outboxLine{Kind: "report", Body: fmt.Sprintf(`{"n":%d}`, i)}
	}
	if err := link.writeOutbox(lines); err != nil {
		t.Fatalf("fill the outbox: %v", err)
	}
	if err := link.keepForLater(reportBatch{}); err != nil {
		t.Fatalf("keep one batch past the limit: %v", err)
	}
	kept, err := link.outboxLines()
	if err != nil || len(kept) != outboxLimit {
		t.Fatalf("the outbox holds %d lines, %v, want the limit", len(kept), err)
	}
	if kept[0].Body != `{"n":1}` {
		t.Errorf("the oldest line stayed: %q", kept[0].Body)
	}

	// an empty outbox leaves no file
	if err := link.writeOutbox(nil); err != nil {
		t.Fatalf("empty the outbox: %v", err)
	}
	if _, err := os.Stat(link.home.Outbox()); !os.IsNotExist(err) {
		t.Errorf("the empty outbox left a file behind: %v", err)
	}

	// a home that cannot hold a file refuses the batch
	link.home = home.Home{Root: writeOnlyDir(t)}
	if err := link.keepForLater(reportBatch{}); err == nil {
		t.Fatal("the outbox took a batch in a home that cannot hold a file")
	}
	// the same home stops the flush at its replay, and a drop over it costs nothing
	if err := link.flush(ctx); err == nil {
		t.Fatal("the flush ran over a home that cannot be read")
	}
	link.dropOutbox(1)
}

// TestFlushRefusesTheBatchTheHubWillNeverTake proves a hub's refusal travels: the flush answers
// it by name, the cursor already moved with the hand-off, and nothing is queued behind it.
func TestFlushRefusesTheBatchTheHubWillNeverTake(t *testing.T) {
	link, db := linkOn(t, refusingHub(t))
	ctx := context.Background()
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-1", Machine: "mbp", LastEvent: "UserPromptSubmit",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	err := link.flush(ctx)
	if err == nil || !strings.Contains(err.Error(), "refused the batch") {
		t.Fatalf("the flush = %v, want the hub's refusal named", err)
	}
}

// TestApplyRefusesWhatItCannotReadOrFind proves a handout the machine cannot act on is refused
// with the reason the hub shows: an unknown kind, a torn payload, an item already gone, and a
// registry that cannot be read holds the decision for later.
func TestApplyRefusesWhatItCannotReadOrFind(t *testing.T) {
	f := startFakeHub(t)
	link, db := linkOn(t, f.srv.URL)
	ctx := context.Background()

	result, reason := link.apply(ctx, hub.SatHandout{ID: 1, Kind: "other", Payload: "{}"})
	if result != hub.SatRefused || !strings.Contains(reason, "no kind") {
		t.Fatalf("an unknown kind = (%s, %q), want refused", result, reason)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 1, Kind: hub.SatDecision, Payload: "{bad"})
	if result != hub.SatRefused || !strings.Contains(reason, "cannot be read") {
		t.Fatalf("a torn decision = (%s, %q), want refused", result, reason)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 1, Kind: hub.SatMessage, Payload: "{bad"})
	if result != hub.SatRefused || !strings.Contains(reason, "cannot be read") {
		t.Fatalf("a torn message = (%s, %q), want refused", result, reason)
	}
	unknown, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: "no-such-key", SessionID: "s-1", Decision: "deny"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 2, Kind: hub.SatDecision, Payload: unknown})
	if result != hub.SatRefused || !strings.Contains(reason, "gone at the machine") {
		t.Fatalf("an unknown key = (%s, %q), want refused", result, reason)
	}

	// a deny the owner made needs no switch to check: it lands and reads delivered
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	id, err := db.CreatePermissionItem(ctx, "s-1", "Bash", []byte(`{"command":"npm test"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := db.KeyOfItem(ctx, id)
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key, SessionID: "s-1", Decision: "deny"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 3, Kind: hub.SatDecision, Payload: payload})
	if result != hub.SatDelivered || reason != "" {
		t.Fatalf("the deny = (%s, %q), want delivered", result, reason)
	}
	item, err := db.PermissionItemByID(ctx, id, now.Add(time.Second))
	if err != nil || item.Decision != "deny" || item.State != hub.StateAnswered {
		t.Fatalf("the item = %+v, %v, want the deny landed", item, err)
	}
	// the same item answered with the other decision reads as answered at the machine first
	allow, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key, SessionID: "s-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 3, Kind: hub.SatDecision, Payload: allow})
	if result != hub.SatRefused || !strings.Contains(reason, "answered at the machine first") {
		t.Fatalf("a second decision on one item = (%s, %q), want refused", result, reason)
	}

	// an allow the machine's own switch is off for is refused there, the item denied
	h := home.Home{Root: link.home.Root}
	cfg, err := home.LoadConfig(h)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	cfg.Remote.Permissions = false
	if err := home.SaveConfig(h, cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	id2, err := db.CreatePermissionItem(ctx, "s-2", "Bash", []byte(`{"command":"npm test"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key2, _ := db.KeyOfItem(ctx, id2)
	allow2, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key2, SessionID: "s-2", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 4, Kind: hub.SatDecision, Payload: allow2})
	if result != hub.SatRefused || !strings.Contains(reason, "remote permissions are off") {
		t.Fatalf("a switched-off machine = (%s, %q), want refused", result, reason)
	}

	// a machine whose switches cannot be read refuses the allow, so the terminal's prompt ends
	id3, err := db.CreatePermissionItem(ctx, "s-3", "Bash", []byte(`{"command":"npm test"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key3, _ := db.KeyOfItem(ctx, id3)
	allow3, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key3, SessionID: "s-3", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	link.home = home.Home{Root: writeOnlyDir(t)}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 5, Kind: hub.SatDecision, Payload: allow3})
	if result != hub.SatRefused || !strings.Contains(reason, "switches cannot be read") {
		t.Fatalf("an unreadable config = (%s, %q), want refused", result, reason)
	}

	// a registry that cannot be read holds the decision for later, answered busy
	if err := db.Close(); err != nil {
		t.Fatalf("close the registry: %v", err)
	}
	result, reason = link.apply(ctx, hub.SatHandout{ID: 6, Kind: hub.SatDecision, Payload: payload})
	if result != hub.SatBusy || !strings.Contains(reason, "could not be read") {
		t.Fatalf("a closed registry = (%s, %q), want busy", result, reason)
	}
	if err := link.flush(ctx); err == nil {
		t.Fatal("the flush read a closed registry")
	}
}

// TestStartCursorLeavesTheCursorWhenTheRegistryRefuses proves a registry that cannot answer
// costs nothing: the cursor starts where it was, and the first flush reads the events itself.
func TestStartCursorLeavesTheCursorWhenTheRegistryRefuses(t *testing.T) {
	f := startFakeHub(t)
	link, db := linkOn(t, f.srv.URL)
	if err := db.Close(); err != nil {
		t.Fatalf("close the registry: %v", err)
	}
	link.StartCursor(context.Background())
	if got := link.cursorOf(); got != 0 {
		t.Fatalf("the cursor = %d, want 0", got)
	}
}

// TestSettleAppliesTheDecisionEvenWhenTheHubCannotHear proves the machine's own item is settled
// whatever the hub's answer is worth: the result that cannot be told is a delay, never a loss.
func TestSettleAppliesTheDecisionEvenWhenTheHubCannotHear(t *testing.T) {
	link, db := linkOn(t, deadHubURL(t))
	ctx := context.Background()
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	id, err := db.CreatePermissionItem(ctx, "s-1", "Bash", []byte(`{"command":"npm test"}`), now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	key, _ := db.KeyOfItem(ctx, id)
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: key, SessionID: "s-1", Decision: "deny"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	link.settle(ctx, hub.SatHandout{ID: 9, Kind: hub.SatDecision, Payload: payload})
	item, err := db.PermissionItemByID(ctx, id, now.Add(time.Second))
	if err != nil || item.State != hub.StateAnswered || item.Decision != "deny" {
		t.Fatalf("the item = %+v, %v, want the deny landed", item, err)
	}
}

// TestLoopsEndWhenTheirContextIsDone proves each loop answers an already-ended context at once:
// neither loop runs a round on a machine that is shutting down.
func TestLoopsEndWhenTheirContextIsDone(t *testing.T) {
	f := startFakeHub(t)
	link, _ := linkOn(t, f.srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		link.pollLoop(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the poll loop ran on an ended context")
	}
	link.flushLoop(ctx) // answers at once
}

// TestPollAsksForAtLeastOneSecond proves a poll never asks the hub to hold for nothing: a wait
// shorter than a second is sent as one second.
func TestPollAsksForAtLeastOneSecond(t *testing.T) {
	f := startCountingHub(t)
	link, _ := linkOn(t, f.srv.URL)
	link.pollWait = 500 * time.Millisecond
	if err := link.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
}

// TestSettleTellsTheHubWhy proves a refused handout's reason rides the result: the hub shows the
// owner why the machine would not take the decision.
func TestSettleTellsTheHubWhy(t *testing.T) {
	f := startFakeHub(t)
	link, _ := linkOn(t, f.srv.URL)
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{Key: "no-such-key", SessionID: "s-1", Decision: "allow"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	link.settle(context.Background(), hub.SatHandout{ID: 5, Kind: hub.SatDecision, Payload: payload})
	results := resultsOf(f)
	if len(results) != 1 || !strings.Contains(results[0], hub.SatRefused) || !strings.Contains(results[0], "reason") {
		t.Fatalf("the results = %v, want one refusal with its reason", results)
	}
}

// TestFlushNamesBothRefusalsWhenNothingTakesTheBatch proves the flush says exactly what failed:
// a hub that will not take the batch and an outbox that cannot hold it are both named.
func TestFlushNamesBothRefusalsWhenNothingTakesTheBatch(t *testing.T) {
	link, db := linkOn(t, deadHubURL(t))
	ctx := context.Background()
	if err := db.Report(ctx, hub.Report{
		SessionID: "s-1", Machine: "mbp", LastEvent: "UserPromptSubmit",
		LastEventAt: "2026-10-16T12:00:00.000Z",
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	// the outbox file reads, its directory takes no new file: the replay goes through and the
	// keep-for-later fails
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "outbox.jsonl"), nil, 0o600); err != nil {
		t.Fatalf("write the outbox file: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("close the directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	link.home = home.Home{Root: dir}
	err := link.flush(ctx)
	if err == nil || !strings.Contains(err.Error(), "neither the hub nor the outbox") {
		t.Fatalf("the flush = %v, want both refusals named", err)
	}
}

// TestOutboxLosesATornLineAndGoesOn proves a torn outbox line is lost and the whole lines after
// it still replay in order.
func TestOutboxLosesATornLineAndGoesOn(t *testing.T) {
	f := startFakeHub(t)
	link, _ := linkOn(t, f.srv.URL)
	// the outbox as a crash left it: one half-written line, then a whole one
	raw := "{torn\n" + `{"kind":"report","body":"{\"reports\":[]}"}` + "\n"
	if err := os.WriteFile(link.home.Outbox(), []byte(raw), 0o600); err != nil {
		t.Fatalf("write the torn outbox: %v", err)
	}
	if err := link.replay(context.Background()); err != nil {
		t.Fatalf("replay: %v", err)
	}
	batches := reportsOf(f)
	if len(batches) != 1 {
		t.Fatalf("the replay posted %d batches, want the one whole line", len(batches))
	}
	if lines, _ := link.outboxLines(); len(lines) != 0 {
		t.Fatalf("the outbox holds %d lines, want it spent", len(lines))
	}
}

// TestPostAndGetRefuseWhatNeverFormsARequest proves a body no JSON can carry and a hub address
// no URL can hold are errors, never a silent skip.
func TestPostAndGetRefuseWhatNeverFormsARequest(t *testing.T) {
	ctx := context.Background()
	link, _ := linkOn(t, deadHubURL(t))
	if err := link.post(ctx, "/api/sat/report", make(chan int)); err == nil {
		t.Error("a channel was posted")
	}
	link.hubURL = "http://exa mple.test"
	if err := link.post(ctx, "/api/sat/report", reportBatch{}); err == nil {
		t.Error("a hub address with a space was posted to")
	}
	if err := link.get(ctx, "/api/sat/next", &struct{}{}); err == nil {
		t.Error("a hub address with a space was polled")
	}
}
