package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// rejoinEvent is one channel event the fake hub lists.
type rejoinEvent struct {
	Seq  int64  `json:"seq"`
	From string `json:"from"`
	To   string `json:"to"`
	Msg  string `json:"msg"`
}

// rejoinHub is a fake Huddle whose channel homelab can drop its member, refuse the live stream
// with a status, and answer joins slowly or not at all. It records the order of what it saw.
type rejoinHub struct {
	*httptest.Server
	firstList chan struct{} // closed once the first sessions list has been answered
	firstOnce sync.Once

	mu             sync.Mutex
	listed         bool          // the member is on the channel's list
	leftRow        bool          // the member's row is on the list, in state left
	toolJoins      int           // join tool calls the hub was sent
	leaves         int           // leave ops the hub was sent
	liveStatus     int           // a status the live stream answers instead of streaming; 0 streams
	endFirstLive   bool          // the first stream ends after its hello, with the member dropped
	joinStatus     int           // a status the join answers; 0 answers 200
	joinDelay      time.Duration // how long a join takes to answer
	last           int64         // the channel's last seq, as the hello names it
	events         []rejoinEvent
	lives          int
	order          []string // "live" and "join", as they arrived
	joins          []joinBodyOf
	joinsInFlight  int
	joinOverlap    bool // two joins ran at once
	listDuringJoin bool // a sessions list arrived while a join was in flight
}

func newRejoinHub(t *testing.T) *rejoinHub {
	t.Helper()
	hub := &rejoinHub{firstList: make(chan struct{}), listed: true, last: 5}
	hub.Server = httptest.NewServer(http.HandlerFunc(hub.serve))
	t.Cleanup(hub.Close)
	return hub
}

func (h *rejoinHub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/c/homelab/sessions":
		h.mu.Lock()
		if h.joinsInFlight > 0 {
			h.listDuringJoin = true
		}
		listed, state := h.listed, "working"
		if h.leftRow {
			listed, state = true, "left"
		}
		h.mu.Unlock()
		if listed {
			fmt.Fprintf(w, `{"sessions":[{"name":"proxbeam","state":%q,"last_seen":%q}],"turn":{"holder":null,"since":null},"config":{}}`,
				state, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
		} else {
			fmt.Fprint(w, `{"sessions":[],"turn":{"holder":null,"since":null},"config":{}}`)
		}
		h.firstOnce.Do(func() { close(h.firstList) })
	case r.Method == http.MethodPost && r.URL.Path == "/api/c/homelab/op/join":
		raw, _ := io.ReadAll(r.Body)
		var body joinBodyOf
		_ = json.Unmarshal(raw, &body)
		h.mu.Lock()
		h.joinsInFlight++
		h.joinOverlap = h.joinOverlap || h.joinsInFlight > 1
		h.order = append(h.order, "join")
		h.joins = append(h.joins, body)
		delay, status := h.joinDelay, h.joinStatus
		h.mu.Unlock()
		time.Sleep(delay)
		h.mu.Lock()
		h.joinsInFlight--
		if status == 0 {
			h.listed = true
		}
		h.mu.Unlock()
		if status != 0 {
			http.Error(w, `{"error":"busy"}`, status)
			return
		}
		fmt.Fprint(w, `{"result":{},"text":"joined"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/mcp/homelab":
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"name":"leave"`) {
			h.mu.Lock()
			h.leaves++
			h.listed = false // the hub drops the row as the leave lands
			h.mu.Unlock()
		}
		if strings.Contains(string(raw), `"name":"join"`) {
			h.mu.Lock()
			h.toolJoins++
			h.listed = true
			h.mu.Unlock()
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"left"}]}}`)
	case strings.HasSuffix(r.URL.Path, "/live"):
		h.serveLive(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/op/events"):
		var args struct {
			After int64 `json:"after"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &args)
		h.mu.Lock()
		out := []rejoinEvent{}
		for _, event := range h.events {
			if event.Seq > args.After {
				out = append(out, event)
			}
		}
		h.mu.Unlock()
		encoded, _ := json.Marshal(map[string]any{"result": out})
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write(encoded)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/op/inbox"):
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"result":[]}`)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/timeline"):
		fmt.Fprint(w, `[]`)
	default:
		http.NotFound(w, r)
	}
}

func (h *rejoinHub) serveLive(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.lives++
	n, status, last := h.lives, h.liveStatus, h.last
	h.order = append(h.order, "live")
	h.mu.Unlock()
	if status != 0 {
		http.Error(w, `{"error":"refused"}`, status)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "data: {\"type\":\"hello\",\"data\":{\"last\":%d}}\n\n", last)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if n == 1 && h.endFirstLive {
		select {
		case <-h.firstList:
		case <-time.After(5 * time.Second):
		}
		h.mu.Lock()
		h.listed = false
		h.events = append(h.events, rejoinEvent{Seq: 6, From: "homelab", To: "proxbeam", Msg: "sent while you were gone"})
		h.last = 6
		h.mu.Unlock()
		return
	}
	<-r.Context().Done()
}

// dropMember takes the member off the channel's list, once the first list has been answered.
func (h *rejoinHub) dropMember() {
	<-h.firstList
	h.mu.Lock()
	h.listed = false
	h.mu.Unlock()
}

func (h *rejoinHub) joined() []joinBodyOf {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]joinBodyOf{}, h.joins...)
}

func (h *rejoinHub) liveCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lives
}

// waitFor polls keep up to ms and says whether it held.
func waitFor(ms int, keep func() bool) bool {
	deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
	for time.Now().Before(deadline) {
		if keep() {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return keep()
}

// rejoinEnv is the proxbeam session with the membership check every 100 ms.
func rejoinEnv(url string) map[string]string {
	env := proxbeamEnv(url)
	env["HUDDLE_TEST_MEMBER_CHECK_MS"] = "100"
	return env
}

// linesWith counts the bridge's diagnostics holding text.
func linesWith(l *mcpLog, text string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, line := range l.lines {
		if strings.Contains(line, text) {
			count++
		}
	}
	return count
}

// TestASessionTheHubHoldsNoRowForJoinsAgainAndTheMissedAskArrivesOnce: the first stream ends,
// the hub holds no row for the member and holds an ask meant for it; the bridge joins before it
// connects again, with the role and context the hook's join carries, and the ask arrives once.
func TestASessionTheHubHoldsNoRowForJoinsAgainAndTheMissedAskArrivesOnce(t *testing.T) {
	hub := newRejoinHub(t)
	hub.endFirstLive = true
	b := runMcpBridge(t, "", rejoinEnv(hub.URL))
	b.send(firstToolCall)
	if b.until(10000, isPushWith("sent while you were gone")) == nil {
		t.Fatal("the ask sent while the member was gone never arrived")
	}
	got := hub.joined()
	if len(got) != 1 {
		t.Fatalf("the hub saw %d joins, want 1", len(got))
	}
	want := joinBodyOf{Role: "builder", Task: "session started", Context: "fresh", ClaudeSession: "a4c70c75-0000"}
	if got[0] != want {
		t.Errorf("join body = %+v, want %+v", got[0], want)
	}
	hub.mu.Lock()
	order := strings.Join(hub.order, ",")
	hub.mu.Unlock()
	if !strings.HasPrefix(order, "live,join,live") {
		t.Errorf("the hub saw %q, want the join between the two streams", order)
	}
	time.Sleep(400 * time.Millisecond)
	if n := len(b.out.findAll(isPushWith("sent while you were gone"))); n != 1 {
		t.Errorf("the ask arrived %d times, want 1", n)
	}
}

// TestAStreamThatStaysUpRejoinsWithinOneCheckAfterTheHubDropsTheMember: the stream never breaks
// and answers 200, yet the hub holds no row for the member; the next check joins again.
func TestAStreamThatStaysUpRejoinsWithinOneCheckAfterTheHubDropsTheMember(t *testing.T) {
	hub := newRejoinHub(t)
	b := runMcpBridge(t, "", rejoinEnv(hub.URL))
	b.send(firstToolCall)
	hub.dropMember()
	if !waitFor(5000, func() bool { return len(hub.joined()) >= 1 }) {
		t.Fatal("the bridge never joined again while its stream stayed up")
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(hub.joined()); n != 1 {
		t.Errorf("the hub saw %d joins, want 1: the member is listed again after the first", n)
	}
	if n := hub.liveCount(); n != 1 {
		t.Errorf("the stream was opened %d times, want the one that stayed up", n)
	}
}

// TestAMissingChannelStopsRejoiningAndSaysHowToCreateIt: a hub that answers 404 for the stream
// has no such channel; a join would make one, so the bridge does not join, says so once, and
// keeps its stream backoff.
func TestAMissingChannelStopsRejoiningAndSaysHowToCreateIt(t *testing.T) {
	hub := newRejoinHub(t)
	hub.liveStatus = http.StatusNotFound
	b := runMcpBridge(t, "", rejoinEnv(hub.URL))
	b.send(firstToolCall)
	hub.dropMember()
	if !waitFor(10000, func() bool { return hub.liveCount() >= 3 }) {
		t.Fatalf("the stream was opened %d times, want its backoff to keep trying", hub.liveCount())
	}
	if n := len(hub.joined()); n != 0 {
		t.Errorf("the hub saw %d joins for a channel that does not exist, want none", n)
	}
	if n := linesWith(b.errs, "does not exist"); n != 1 {
		t.Errorf("the bridge said the channel does not exist %d times, want once:\n%s", n, b.errs.text())
	}
	if linesWith(b.errs, "/huddle:channel create") != 1 {
		t.Errorf("the bridge never named /huddle:channel create:\n%s", b.errs.text())
	}
}

// TestA401SaysToRunHuddleJoinAndNeverJoins: the hub holds no credential for the session, which
// a join cannot mend; the bridge names /huddle:join once and sends no join.
func TestA401SaysToRunHuddleJoinAndNeverJoins(t *testing.T) {
	hub := newRejoinHub(t)
	hub.liveStatus = http.StatusUnauthorized
	b := runMcpBridge(t, "", rejoinEnv(hub.URL))
	b.send(firstToolCall)
	hub.dropMember()
	if !waitFor(10000, func() bool { return hub.liveCount() >= 3 }) {
		t.Fatalf("the stream was opened %d times, want its backoff to keep trying", hub.liveCount())
	}
	if n := len(hub.joined()); n != 0 {
		t.Errorf("the hub saw %d joins for a credential it does not hold, want none", n)
	}
	if n := linesWith(b.errs, "/huddle:join"); n != 1 {
		t.Errorf("the bridge named /huddle:join %d times across the run of 401s, want once:\n%s", n, b.errs.text())
	}
}

// TestACheckNeverRunsBesideTheBridgesOwnJoin: while the bridge's own join keeps trying against a
// hub that refuses it, the membership check stays out: no join overlaps another, no list is read
// during one, and the joins keep the bridge's own pace.
func TestACheckNeverRunsBesideTheBridgesOwnJoin(t *testing.T) {
	hub := newRejoinHub(t)
	hub.listed = false
	hub.joinStatus = http.StatusServiceUnavailable
	hub.joinDelay = 150 * time.Millisecond
	env := proxbeamEnv(hub.URL)
	env["HUDDLE_TEST_MEMBER_CHECK_MS"] = "20"
	b := runMcpBridge(t, "", env)
	b.send(firstToolCall)
	time.Sleep(2600 * time.Millisecond)
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.joinOverlap {
		t.Error("two joins ran at once")
	}
	if hub.listDuringJoin {
		t.Error("a sessions list was read while a join was in flight")
	}
	if n := len(hub.joins); n < 1 || n > 3 {
		t.Errorf("the hub saw %d joins in 2.6 s, want the bridge's own 1 s then 2 s pace (at most 3)", n)
	}
}

// TestARefusedStreamAnswersTheStatusAsAValue: a stream the hub refuses comes back as a
// liveStatusError carrying the status, matched with errors.As.
func TestARefusedStreamAnswersTheStatusAsAValue(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway} {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"refused"}`, status)
		}))
		vars := proxbeamEnv(hub.URL)
		b := &mcpBridge{
			w: &World{
				Getenv:  func(key string) string { return vars[key] },
				Environ: func() []string { return nil },
				Dir:     t.TempDir(),
				Now:     time.Now,
			},
			stdout: io.Discard, stderr: io.Discard,
			url: hub.URL, channelName: "homelab", me: "proxbeam",
			pushed: map[int64]bool{},
		}
		err, connected := b.streamOnce(t.Context())
		hub.Close()
		var refused *liveStatusError
		if !errors.As(err, &refused) || refused.Code != status {
			t.Errorf("status %d: streamOnce answered %v, want a liveStatusError with that code", status, err)
		}
		if connected {
			t.Errorf("status %d: streamOnce says it connected", status)
		}
	}
}

// TestASessionThatLeftOnPurposeStaysLeft: the channel lists the member in state left, as after
// a leave the person asked for; across many checks the bridge sends no join and says nothing.
func TestASessionThatLeftOnPurposeStaysLeft(t *testing.T) {
	hub := newRejoinHub(t)
	b := runMcpBridge(t, "", rejoinEnv(hub.URL))
	b.send(firstToolCall)
	<-hub.firstList
	hub.mu.Lock()
	hub.leftRow = true
	hub.mu.Unlock()
	time.Sleep(time.Second) // ten checks
	if n := len(hub.joined()); n != 0 {
		t.Errorf("the hub saw %d joins for a session that left on purpose, want none", n)
	}
	if n := linesWith(b.errs, "joining again"); n != 0 {
		t.Errorf("the bridge said it joined again %d times:\n%s", n, b.errs.text())
	}
}

// TestASessionThatLeavesThroughTheBridgeStaysLeft: the leave tool goes through the bridge and the
// hub then holds no row; the bridge sends no join for the rest of the run.
func TestASessionThatLeavesThroughTheBridgeStaysLeft(t *testing.T) {
	hub := newRejoinHub(t)
	b := runMcpBridge(t, "", rejoinEnv(hub.URL))
	b.send(firstToolCall)
	<-hub.firstList
	b.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"leave","arguments":{"summary":"done"}}}`)
	if !waitFor(5000, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return hub.leaves == 1
	}) {
		t.Fatal("the hub never saw the leave")
	}
	time.Sleep(time.Second) // ten checks
	if n := len(hub.joined()); n != 0 {
		t.Errorf("the hub saw %d joins after the session left through the bridge, want none", n)
	}
}

// TestASessionThatJoinsAgainThroughTheBridgeIsKeptListedAgain: a session leaves through the
// bridge and joins again through it; when the hub then holds no row for it, the bridge joins it
// again within one check.
func TestASessionThatJoinsAgainThroughTheBridgeIsKeptListedAgain(t *testing.T) {
	hub := newRejoinHub(t)
	b := runMcpBridge(t, "", rejoinEnv(hub.URL))
	b.send(firstToolCall)
	<-hub.firstList
	b.send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"leave","arguments":{"summary":"done"}}}`)
	if !waitFor(5000, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return hub.leaves == 1
	}) {
		t.Fatal("the hub never saw the leave")
	}
	b.send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"join","arguments":{}}}`)
	if !waitFor(5000, func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return hub.toolJoins == 1
	}) {
		t.Fatal("the hub never saw the join")
	}
	hub.mu.Lock()
	hub.listed = false // the hub drops the row after the join
	hub.mu.Unlock()
	if !waitFor(5000, func() bool { return len(hub.joined()) >= 1 }) {
		t.Fatal("the bridge never joined again after the hub dropped the row")
	}
}
