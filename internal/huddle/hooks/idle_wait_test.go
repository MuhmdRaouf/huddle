package hooks

// The idle-wait hook wakes an idle session on what is meant for it. The decision is pure and is
// held case by case; the waiter's loop runs in process against a fake hub that serves the live
// stream, the events operation and the registry the real one serves.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

func homelab() cli.Identity { return cli.Identity{Channel: "homelab", As: "homelab"} }

func noPush() bool { return false }

func TestWakeLinesWakeOnWhatIsMeantForTheSession(t *testing.T) {
	cases := []struct {
		name  string
		event wakeEvent
		want  string
	}{
		{"an ask to me", wakeEvent{Seq: 42, From: "proxbeam", To: "homelab", Topic: "ask", Msg: "PING 1", NeedsReply: true},
			`proxbeam asks: PING 1 (reply with: huddle reply 42 "<text>")`},
		{"an ask to everyone", wakeEvent{Seq: 7, From: "proxbeam", Topic: "ask", Msg: "who owns the NAS?", NeedsReply: true},
			`proxbeam asks: who owns the NAS? (reply with: huddle reply 7 "<text>")`},
		{"a direct message to me", wakeEvent{Seq: 43, From: "proxbeam", To: "homelab", Topic: "msg", Msg: "the pool is mounted"},
			`proxbeam says: the pool is mounted`},
		{"a reply to me", wakeEvent{Seq: 44, From: "proxbeam", To: "homelab", Topic: "reply", Msg: "PONG 1"},
			`proxbeam replied: PONG 1`},
		{"a ready task of mine", wakeEvent{Seq: 45, From: "proxbeam", To: "homelab", Topic: "task.ready", Msg: "T-2 deploy: everything it waits on is done"},
			`task ready: T-2 deploy: everything it waits on is done`},
		{"a turn passed to me", wakeEvent{Seq: 46, From: "proxbeam", To: "homelab", Topic: "turn.pass", Msg: "your move"},
			`proxbeam passed you the turn: your move`},
		{"the owner's word to me", wakeEvent{Seq: 47, From: "owner", To: "homelab", Topic: "msg", Msg: "stop and report"},
			`owner says: stop and report`},
		{"a message to the whole channel", wakeEvent{Seq: 49, From: "proxbeam", Topic: "msg", Msg: "build is green"},
			`proxbeam says to the channel: build is green`},
		{"a handover topic that needs my reply", wakeEvent{Seq: 50, From: "proxbeam", To: "homelab", Topic: "handoff.review", Msg: "check the PR", NeedsReply: true},
			`proxbeam asks: check the PR (reply with: huddle reply 50 "<text>")`},
		{"a message that runs over several lines", wakeEvent{Seq: 48, From: "proxbeam", To: "homelab", Topic: "msg", Msg: "line one\nline two"},
			`proxbeam says: line one line two`},
	}
	for _, c := range cases {
		got := wakeLines(homelab(), []wakeEvent{c.event}, noPush)
		if !reflect.DeepEqual(got, []string{c.want}) {
			t.Errorf("%s: wake lines = %q, want %q", c.name, got, []string{c.want})
		}
	}
}

func TestWakeLinesStayQuietOnWhatIsNotMeantForTheSession(t *testing.T) {
	cases := []struct {
		name  string
		event wakeEvent
	}{
		{"a message between two others", wakeEvent{Seq: 1, From: "proxbeam", To: "third", Topic: "msg", Msg: "hi"}},
		{"an ask between two others", wakeEvent{Seq: 2, From: "proxbeam", To: "third", Topic: "ask", Msg: "hi", NeedsReply: true}},
		{"a broadcast task update", wakeEvent{Seq: 3, From: "proxbeam", Topic: "task.status", Msg: "T-1 done"}},
		{"a broadcast custom topic", wakeEvent{Seq: 11, From: "proxbeam", Topic: "build.ready", Msg: "green"}},
		{"a direct message between two others", wakeEvent{Seq: 12, From: "proxbeam", To: "nas", Topic: "msg", Msg: "psst"}},
		{"a ready task nobody owns", wakeEvent{Seq: 13, From: "proxbeam", Topic: "task.ready", Msg: "T-3 lint"}},
		{"a knowledge entry", wakeEvent{Seq: 4, From: "proxbeam", Topic: "kb.add", Msg: "the NAS is on VLAN 20"}},
		{"my own message", wakeEvent{Seq: 5, From: "homelab", To: "proxbeam", Topic: "msg", Msg: "hi"}},
		{"my own ask to everyone", wakeEvent{Seq: 6, From: "homelab", Topic: "ask", Msg: "hi", NeedsReply: true}},
		{"my subagent's message", wakeEvent{Seq: 7, From: "homelab.scout", To: "homelab", Topic: "msg", Msg: "found it"}},
		{"a join", wakeEvent{Seq: 8, From: "proxbeam", To: "homelab", Topic: "session.join", Msg: "joined"}},
		{"the owner's word to another session", wakeEvent{Seq: 9, From: "owner", To: "third", Topic: "msg", Msg: "stop"}},
		{"a task update to me", wakeEvent{Seq: 10, From: "proxbeam", To: "homelab", Topic: "task.status", Msg: "T-1 done"}},
	}
	for _, c := range cases {
		if got := wakeLines(homelab(), []wakeEvent{c.event}, noPush); len(got) != 0 {
			t.Errorf("%s: wake lines = %q, want none", c.name, got)
		}
	}
}

func TestWakeLinesNeedAJoinedSession(t *testing.T) {
	ask := wakeEvent{Seq: 1, From: "proxbeam", To: "homelab", Topic: "ask", Msg: "PING", NeedsReply: true}
	for _, id := range []cli.Identity{{}, {Channel: "homelab"}, {As: "homelab"}} {
		if got := wakeLines(id, []wakeEvent{ask}, noPush); len(got) != 0 {
			t.Errorf("identity %+v: wake lines = %q, want none", id, got)
		}
	}
}

func TestWakeLinesSkipWhatThePushAlreadyDelivered(t *testing.T) {
	ask := wakeEvent{Seq: 1, From: "proxbeam", To: "homelab", Topic: "ask", Msg: "PING", NeedsReply: true}
	if got := wakeLines(homelab(), []wakeEvent{ask}, func() bool { return true }); len(got) != 0 {
		t.Errorf("with the push live, wake lines = %q, want none", got)
	}
	asked := false
	chatter := wakeEvent{Seq: 2, From: "proxbeam", Topic: "kb.add", Msg: "the NAS is on VLAN 20"}
	wakeLines(homelab(), []wakeEvent{chatter}, func() bool { asked = true; return true })
	if asked {
		t.Error("the push was probed for an event that never wakes the session")
	}
}

func TestWakeLinesKeepTheOrderOfSeveralEvents(t *testing.T) {
	got := wakeLines(homelab(), []wakeEvent{
		{Seq: 1, From: "proxbeam", To: "homelab", Topic: "msg", Msg: "first"},
		{Seq: 2, From: "proxbeam", To: "third", Topic: "msg", Msg: "overheard"},
		{Seq: 4, From: "proxbeam", Topic: "kb.add", Msg: "a fact"},
		{Seq: 3, From: "proxbeam", To: "homelab", Topic: "msg", Msg: "second"},
	}, noPush)
	want := []string{"proxbeam says: first", "proxbeam says: second"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("wake lines = %q, want %q", got, want)
	}
}

func TestSeenCursorReadsTheFeedsCursorWithoutMovingIt(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "seen", "sess-1.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"homelab":17,"other":3}`
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if seq, ok := seenCursor(home, "sess-1", "homelab"); !ok || seq != 17 {
		t.Errorf("seenCursor = (%d, %v), want (17, true)", seq, ok)
	}
	if _, ok := seenCursor(home, "sess-1", "missing"); ok {
		t.Error("seenCursor answered a channel the feed never saw")
	}
	if _, ok := seenCursor(home, "no-such-session", "homelab"); ok {
		t.Error("seenCursor answered a session the feed never saw")
	}
	if raw, _ := os.ReadFile(file); string(raw) != body {
		t.Errorf("the feed's cursor file changed to %q", raw)
	}
}

// waiterCommand is the command line a verified waiter has.
const waiterCommand = "/opt/huddle/current hook idle-wait"

func newWaiterFiles(t *testing.T, commands map[int]string, stopped *[]int) waiterFiles {
	t.Helper()
	return waiterFiles{
		dir: filepath.Join(t.TempDir(), "run", "idle-wait"),
		commandOf: func(pid int) (string, bool) {
			command, ok := commands[pid]
			return command, ok
		},
		stop: func(pid int) { *stopped = append(*stopped, pid) },
	}
}

func TestClaimWritesTheWaitersPidFileAndReleaseRemovesIt(t *testing.T) {
	var stopped []int
	files := newWaiterFiles(t, nil, &stopped)
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	if err := files.claim("sess-1", 4242, now); err != nil {
		t.Fatalf("claim: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(files.dir, "sess-1.json"))
	if err != nil {
		t.Fatalf("the pid file: %v", err)
	}
	var got struct {
		PID     int    `json:"pid"`
		Started string `json:"started"`
		Session string `json:"session"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	if got.PID != 4242 || got.Session != "sess-1" || got.Started != "2026-11-01T10:00:00.000Z" {
		t.Errorf("pid file = %+v, want pid 4242, session sess-1, started 2026-11-01T10:00:00.000Z", got)
	}
	files.release("sess-1", 4242)
	if _, err := os.Stat(filepath.Join(files.dir, "sess-1.json")); !os.IsNotExist(err) {
		t.Errorf("the pid file is still there after release: %v", err)
	}
	if len(stopped) != 0 {
		t.Errorf("claim signalled %v with no earlier waiter", stopped)
	}
}

func TestClaimStopsThePreviousWaiterOfTheSameSessionOnlyWhenItsCommandLineSaysSo(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		commands map[int]string
		want     []int
	}{
		{"a live waiter", map[int]string{111: waiterCommand}, []int{111}},
		{"a waiter run through the plugin's launcher", map[int]string{111: "sh /plugin/dist/run hook idle-wait"}, []int{111}},
		{"a pid another program took", map[int]string{111: "/usr/bin/vim notes.md"}, nil},
		{"another huddle hook", map[int]string{111: "/opt/huddle/current hook stop"}, nil},
		{"a pid that is gone", map[int]string{}, nil},
	}
	for _, c := range cases {
		var stopped []int
		files := newWaiterFiles(t, c.commands, &stopped)
		if err := files.claim("sess-1", 111, now); err != nil {
			t.Fatalf("%s: first claim: %v", c.name, err)
		}
		if err := files.claim("sess-1", 222, now); err != nil {
			t.Fatalf("%s: second claim: %v", c.name, err)
		}
		if !reflect.DeepEqual(stopped, c.want) {
			t.Errorf("%s: stopped %v, want %v", c.name, stopped, c.want)
		}
		raw, _ := os.ReadFile(filepath.Join(files.dir, "sess-1.json"))
		if !strings.Contains(string(raw), `"pid":222`) {
			t.Errorf("%s: pid file = %s, want the second waiter's pid 222", c.name, raw)
		}
	}
}

func TestClaimNeverSignalsItself(t *testing.T) {
	var stopped []int
	files := newWaiterFiles(t, map[int]string{111: waiterCommand}, &stopped)
	_ = files.claim("sess-1", 111, time.Now())
	_ = files.claim("sess-1", 111, time.Now())
	if len(stopped) != 0 {
		t.Errorf("a waiter signalled itself: %v", stopped)
	}
}

func TestReleaseLeavesTheNewerWaitersFile(t *testing.T) {
	var stopped []int
	files := newWaiterFiles(t, nil, &stopped)
	_ = files.claim("sess-1", 111, time.Now())
	_ = files.claim("sess-1", 222, time.Now())
	files.release("sess-1", 111)
	if _, err := os.Stat(filepath.Join(files.dir, "sess-1.json")); err != nil {
		t.Errorf("the older waiter's release removed the newer one's pid file: %v", err)
	}
}

// fakeHub serves what the waiter reads from a hub: the channel's live stream, its events
// operation and the registry's session list.
type fakeHub struct {
	t      *testing.T
	server *httptest.Server

	mu        sync.Mutex
	last      int64            // the hello's last seq
	backlog   []map[string]any // what the events operation answers
	after     []int64          // the cursors the events operation was asked for
	live      chan string      // frames the stream writes after its hello
	route     string           // the registry's delivery route for the session
	streamEnd chan struct{}    // closes the stream when closed
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	h := &fakeHub{t: t, live: make(chan string, 16), route: "next_prompt"}
	h.server = httptest.NewServer(http.HandlerFunc(h.serve))
	t.Cleanup(h.server.Close)
	return h
}

func (h *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/c/homelab/live":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		h.mu.Lock()
		last := h.last
		h.mu.Unlock()
		fmt.Fprintf(w, "retry: 3000\ndata: {\"ch\":\"homelab\",\"type\":\"hello\",\"data\":{\"last\":%d}}\n\n", last)
		w.(http.Flusher).Flush()
		for {
			select {
			case frame := <-h.live:
				io.WriteString(w, frame)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	case r.URL.Path == "/api/c/homelab/op/events":
		var body struct {
			After int64 `json:"after"`
			Limit int   `json:"limit"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.after = append(h.after, body.After)
		var out []map[string]any
		for _, event := range h.backlog {
			if int64(event["seq"].(int)) > body.After && (body.Limit <= 0 || len(out) < body.Limit) {
				out = append(out, event)
			}
		}
		h.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"result": out})
	case r.URL.Path == "/api/registry/sessions":
		h.mu.Lock()
		route := h.route
		h.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{
			{"session_id": "sess-1", "delivery_route": route},
		}})
	default:
		http.NotFound(w, r)
	}
}

// sendLive queues one event frame for the stream.
func (h *fakeHub) sendLive(event map[string]any) {
	raw, _ := json.Marshal(map[string]any{"ch": "homelab", "type": "event", "data": event})
	h.live <- "data: " + string(raw) + "\n\n"
}

// addBacklog makes one more event readable through the events operation, without a live frame.
func (h *fakeHub) addBacklog(event map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backlog = append(h.backlog, event)
}

func (h *fakeHub) afters() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int64{}, h.after...)
}

// waitWorld is a world joined to the fake hub as homelab, with its own home.
func waitWorld(t *testing.T, hub *fakeHub) (*cli.World, string) {
	t.Helper()
	w, home, _ := waitWorldVars(t, hub)
	return w, home
}

// waitWorldVars is waitWorld with a way to change the world's variables while a waiter runs.
func waitWorldVars(t *testing.T, hub *fakeHub) (*cli.World, string, func(key, value string)) {
	t.Helper()
	home := t.TempDir()
	vars := map[string]string{
		"HOME": t.TempDir(), "HUDDLE_HOME": home, "HUDDLE_URL": hub.server.URL,
		"HUDDLE_CHANNEL": "homelab", "HUDDLE_AS": "homelab", "HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_TOKEN": "idle-wait-test", "HUDDLE_AUTOSTART": "0",
		"XDG_STATE_HOME": t.TempDir(), "CLAUDE_PROJECT_DIR": t.TempDir(),
	}
	var mu sync.Mutex
	w := &cli.World{Getenv: func(key string) string {
		mu.Lock()
		defer mu.Unlock()
		return vars[key]
	}, Dir: t.TempDir()}
	set := func(key, value string) {
		mu.Lock()
		defer mu.Unlock()
		if value == "" {
			delete(vars, key)
			return
		}
		vars[key] = value
	}
	return w, home, set
}

const stopInput = `{"hook_event_name":"Stop","session_id":"sess-1","stop_hook_active":false}`

func runWaiter(t *testing.T, w *cli.World, run time.Duration) (int, string, time.Duration) {
	t.Helper()
	var errOut bytes.Buffer
	started := time.Now()
	code := idleWait(context.Background(), w, strings.NewReader(stopInput), &errOut, run)
	return code, errOut.String(), time.Since(started)
}

func TestIdleWaitWakesOnALiveAsk(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 5
	w, home := waitWorld(t, hub)
	go func() {
		time.Sleep(300 * time.Millisecond)
		hub.sendLive(map[string]any{"seq": 6, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING 1", "needs_reply": true})
	}()
	code, stderr, took := runWaiter(t, w, 10*time.Second)
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr %q", code, stderr)
	}
	if want := `proxbeam asks: PING 1 (reply with: huddle reply 6 "<text>")` + "\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if took > 3*time.Second {
		t.Errorf("woke after %s, want under 3 s", took)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "run", "idle-wait")); len(entries) != 0 {
		t.Errorf("the waiter left %d pid file(s) behind", len(entries))
	}
}

func TestIdleWaitCatchesUpFromTheFeedsCursor(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 9
	hub.backlog = []map[string]any{
		{"seq": 3, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "already shown", "needs_reply": true},
		{"seq": 8, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING 2", "needs_reply": true},
	}
	w, home := waitWorld(t, hub)
	if err := os.MkdirAll(filepath.Join(home, "seen"), 0o755); err != nil {
		t.Fatal(err)
	}
	seen := filepath.Join(home, "seen", "sess-1.json")
	if err := os.WriteFile(seen, []byte(`{"homelab":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr, _ := runWaiter(t, w, 10*time.Second)
	if code != 2 || !strings.Contains(stderr, "PING 2") || strings.Contains(stderr, "already shown") {
		t.Fatalf("exit %d, stderr %q, want 2 with PING 2 only", code, stderr)
	}
	if afters := hub.afters(); len(afters) == 0 || afters[0] != 5 {
		t.Errorf("the catch-up asked for events after %v, want the feed's cursor 5 first", afters)
	}
	if raw, _ := os.ReadFile(seen); string(raw) != `{"homelab":5}` {
		t.Errorf("the feed's cursor file changed to %q", raw)
	}
}

func TestIdleWaitStartsFromNowWhenTheFeedNeverSawTheChannel(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 9
	hub.backlog = []map[string]any{
		{"seq": 8, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "old", "needs_reply": true},
	}
	w, _ := waitWorld(t, hub)
	code, stderr, _ := runWaiter(t, w, 600*time.Millisecond)
	if code != 2 || stderr != keepaliveLine() {
		t.Errorf("exit %d, stderr %q, want only the keepalive line for an event before the waiter started", code, stderr)
	}
}

func keepaliveLine() string {
	return "huddle: still listening in homelab; no new messages, no reply needed.\n"
}

func TestIdleWaitStaysQuietOnChatterThenKeepsTheSessionListeningAtItsDeadline(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 5
	w, home := waitWorld(t, hub)
	hub.sendLive(map[string]any{"seq": 6, "from": "proxbeam", "to": "third", "topic": "msg", "msg": "psst"})
	hub.sendLive(map[string]any{"seq": 7, "from": "proxbeam", "topic": "kb.add", "msg": "the NAS is on VLAN 20"})
	code, stderr, took := runWaiter(t, w, 700*time.Millisecond)
	if code != 2 || stderr != keepaliveLine() {
		t.Errorf("exit %d, stderr %q, want 2 with only the keepalive line %q", code, stderr, keepaliveLine())
	}
	if took < 600*time.Millisecond || took > 3*time.Second {
		t.Errorf("returned after %s, want about its 700 ms deadline", took)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, "run", "idle-wait")); len(entries) != 0 {
		t.Errorf("the waiter left %d pid file(s) behind", len(entries))
	}
}

func TestIdleWaitDeliversAMessageThatArrivesAtTheDeadlineBeforeTheKeepalive(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 5
	w, _ := waitWorld(t, hub)
	go func() {
		time.Sleep(400 * time.Millisecond)
		// readable through the events operation, never sent on the stream: only the last look
		// at the deadline can find it
		hub.addBacklog(map[string]any{"seq": 6, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING at the edge", "needs_reply": true})
	}()
	code, stderr, _ := runWaiter(t, w, 700*time.Millisecond)
	want := `proxbeam asks: PING at the edge (reply with: huddle reply 6 "<text>")` + "\n"
	if code != 2 || stderr != want {
		t.Errorf("exit %d, stderr %q, want 2 with exactly %q", code, stderr, want)
	}
}

func TestIdleWaitDoesNotWakeTwiceForTheSameEvent(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 9
	hub.backlog = []map[string]any{
		{"seq": 8, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING 2", "needs_reply": true},
	}
	w, home := waitWorld(t, hub)
	if err := os.MkdirAll(filepath.Join(home, "seen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "seen", "sess-1.json"), []byte(`{"homelab":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stderr, _ := runWaiter(t, w, 10*time.Second); code != 2 || !strings.Contains(stderr, "PING 2") {
		t.Fatalf("first waiter: exit %d, stderr %q, want 2 with PING 2", code, stderr)
	}
	// the feed never moved (the session answered without a tool call), the next waiter starts
	// where the last one stopped
	code, stderr, _ := runWaiter(t, w, 500*time.Millisecond)
	if code != 2 || stderr != keepaliveLine() {
		t.Errorf("second waiter: exit %d, stderr %q, want only the keepalive line", code, stderr)
	}
}

func TestIdleWaitReadsEveryPageOfTheBacklog(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 260
	for seq := 1; seq <= 250; seq++ {
		hub.backlog = append(hub.backlog, map[string]any{"seq": seq, "from": "proxbeam", "topic": "kb.add", "msg": "fact"})
	}
	hub.backlog = append(hub.backlog, map[string]any{"seq": 251, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING deep", "needs_reply": true})
	w, home := waitWorld(t, hub)
	if err := os.MkdirAll(filepath.Join(home, "seen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "seen", "sess-1.json"), []byte(`{"homelab":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr, _ := runWaiter(t, w, 10*time.Second)
	if code != 2 || !strings.Contains(stderr, "PING deep") {
		t.Errorf("exit %d, stderr %q, want 2 with the ask behind 250 other events", code, stderr)
	}
}

func TestIdleWaitSkipsAnAskThePushDelivered(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 5
	hub.route = "now"
	w, _ := waitWorld(t, hub)
	hub.sendLive(map[string]any{"seq": 6, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING", "needs_reply": true})
	code, stderr, _ := runWaiter(t, w, 700*time.Millisecond)
	if code != 2 || stderr != keepaliveLine() {
		t.Errorf("exit %d, stderr %q, want only the keepalive line while the push is live", code, stderr)
	}
}

func TestIdleWaitPrintsNoErrorWhileTheHubIsDown(t *testing.T) {
	hub := newFakeHub(t)
	w, _ := waitWorld(t, hub)
	hub.server.Close()
	code, stderr, took := runWaiter(t, w, 1500*time.Millisecond)
	if code != 2 || stderr != keepaliveLine() {
		t.Errorf("exit %d, stderr %q, want only the keepalive line while the hub is down", code, stderr)
	}
	if took > 4*time.Second {
		t.Errorf("returned after %s, want about its 1.5 s deadline", took)
	}
}

func TestIdleWaitSkipsSubagentsAndInputWithoutASession(t *testing.T) {
	hub := newFakeHub(t)
	w, _ := waitWorld(t, hub)
	for _, input := range []string{`{"session_id":"sess-1","agent_id":"agent-9"}`, `{}`, ``} {
		var errOut bytes.Buffer
		started := time.Now()
		if code := idleWait(context.Background(), w, strings.NewReader(input), &errOut, 10*time.Second); code != 0 || errOut.Len() != 0 || time.Since(started) > time.Second {
			t.Errorf("input %q: exit %d, stderr %q after %s, want 0, silence, no wait", input, code, errOut.String(), time.Since(started))
		}
	}
}

func TestIdleWaitRunsForTheTimeoutHooksJsonGrantsLessItsMargin(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "plugin", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var file struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command     string `json:"command"`
				Timeout     int    `json:"timeout"`
				Async       bool   `json:"async"`
				AsyncRewake bool   `json:"asyncRewake"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	for _, event := range []string{"Stop", "SessionStart"} {
		found := 0
		for _, group := range file.Hooks[event] {
			for _, entry := range group.Hooks {
				if !strings.HasSuffix(entry.Command, "hook idle-wait") {
					continue
				}
				found++
				if !entry.Async || !entry.AsyncRewake {
					t.Errorf("the %s idle-wait line is async %v, asyncRewake %v, want both true", event, entry.Async, entry.AsyncRewake)
				}
				if entry.Timeout != idleWaitTimeoutSeconds {
					t.Errorf("the %s idle-wait line grants %d s, want idleWaitTimeoutSeconds = %d", event, entry.Timeout, idleWaitTimeoutSeconds)
				}
			}
		}
		if found != 1 {
			t.Fatalf("hooks.json has %d idle-wait %s lines, want 1", found, event)
		}
	}
	if idleWaitRun+idleWaitMargin != idleWaitTimeoutSeconds*time.Second {
		t.Errorf("the waiter runs %s with a %s margin, want them to sum to the %d s timeout", idleWaitRun, idleWaitMargin, idleWaitTimeoutSeconds)
	}
}

func TestIdleWaitWaitsForTheJoinThenListens(t *testing.T) {
	hub := newFakeHub(t)
	hub.last = 5
	w, _, set := waitWorldVars(t, hub)
	set("HUDDLE_CHANNEL", "")
	set("HUDDLE_AS", "")
	go func() {
		time.Sleep(1200 * time.Millisecond)
		set("HUDDLE_CHANNEL", "homelab")
		set("HUDDLE_AS", "homelab")
		time.Sleep(1500 * time.Millisecond)
		hub.sendLive(map[string]any{"seq": 6, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING after the join", "needs_reply": true})
	}()
	code, stderr, took := runWaiter(t, w, 30*time.Second)
	if code != 2 || !strings.Contains(stderr, "proxbeam asks: PING after the join") {
		t.Fatalf("exit %d, stderr %q, want 2 with the ask sent after the join", code, stderr)
	}
	if took < 2*time.Second {
		t.Errorf("woke after %s, before the join and the ask could have happened", took)
	}
}

func TestIdleWaitGivesUpOnAJoinThatNeverComes(t *testing.T) {
	hub := newFakeHub(t)
	w, home, set := waitWorldVars(t, hub)
	set("HUDDLE_CHANNEL", "")
	set("HUDDLE_AS", "")
	code, stderr, took := runWaiter(t, w, 1500*time.Millisecond)
	if code != 0 || stderr != "" {
		t.Errorf("exit %d, stderr %q, want 0 and silence for a session that never joined", code, stderr)
	}
	if took < time.Second || took > 4*time.Second {
		t.Errorf("returned after %s, want it to wait out the join for about its 1.5 s cycle", took)
	}
	if _, err := os.Stat(filepath.Join(home, "run", "idle-wait")); !os.IsNotExist(err) {
		t.Errorf("a session that never joined got a pid file: %v", err)
	}
}

func TestIdleWaitSkipsAnAskTheStopHookAlreadyRaised(t *testing.T) {
	cases := []struct {
		name   string
		raised string
		want   int
	}{
		{"raised for this member", `{"homelab/homelab":[6]}`, 2},
		{"raised for another member", `{"homelab/proxbeam":[6]}`, 2},
		{"another seq raised", `{"homelab/homelab":[4,5]}`, 2},
	}
	for _, c := range cases {
		hub := newFakeHub(t)
		hub.last = 5
		w, home := waitWorld(t, hub)
		if err := os.WriteFile(filepath.Join(home, "stop-raised.json"), []byte(c.raised), 0o644); err != nil {
			t.Fatal(err)
		}
		hub.sendLive(map[string]any{"seq": 6, "from": "proxbeam", "to": "homelab", "topic": "ask", "msg": "PING", "needs_reply": true})
		code, stderr, _ := runWaiter(t, w, 800*time.Millisecond)
		wokenOnAsk := strings.Contains(stderr, "proxbeam asks: PING")
		wantWoken := c.name != "raised for this member"
		if code != 2 || wokenOnAsk != wantWoken {
			t.Errorf("%s: exit %d, stderr %q, want the ask delivered = %v", c.name, code, stderr, wantWoken)
		}
	}
}

func TestSaveWokenCursorPrunesFilesOlderThanThirtyDays(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "woken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for name, age := range map[string]time.Duration{
		"old-session.json":   31 * 24 * time.Hour,
		"newer-session.json": 29 * 24 * time.Hour,
	} {
		file := filepath.Join(dir, name)
		if err := os.WriteFile(file, []byte(`{"homelab":3}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	saveWokenCursor(home, "sess-1", "homelab", 9, now)
	if _, err := os.Stat(filepath.Join(dir, "old-session.json")); !os.IsNotExist(err) {
		t.Errorf("the 31-day-old file was kept: %v", err)
	}
	for _, name := range []string{"newer-session.json", "sess-1.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
	if seq, ok := wokenCursor(home, "sess-1", "homelab"); !ok || seq != 9 {
		t.Errorf("wokenCursor = (%d, %v), want (9, true)", seq, ok)
	}
}
