// listen in its own shapes: a live stream that prints events and writes --state, the replay of
// what was missed, each channel's newest event when several are followed, own events filtered,
// and a Huddle that is not there. The servers here are httptest ones that speak the stream's
// protocol: the e2e leg proves the same paths against the real server.
package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// streamServer answers the two endpoints listen reads: the live stream and the timeline. Both
// are recorded, so a test can prove what listen asked.
type streamServer struct {
	server   *httptest.Server
	live     func(w http.ResponseWriter, r *http.Request)
	timeline func(w http.ResponseWriter, channel string, after, limit string)
	mu       sync.Mutex
	queries  []string
}

// startStreamServer serves until the test ends.
func startStreamServer(t *testing.T, live func(w http.ResponseWriter, r *http.Request), timeline func(w http.ResponseWriter, channel, after, limit string)) *streamServer {
	t.Helper()
	s := &streamServer{live: live, timeline: timeline}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		channel, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/c/"), "/")
		s.mu.Lock()
		s.queries = append(s.queries, rest+"?"+r.URL.RawQuery)
		s.mu.Unlock()
		if rest == "live" {
			s.live(w, r)
			return
		}
		if rest == "timeline" {
			s.timeline(w, channel, r.URL.Query().Get("after"), r.URL.Query().Get("limit"))
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

// asked lists the recorded endpoint calls.
func (s *streamServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.queries...)
}

// quietTimeline answers an empty timeline, so nothing is replayed.
func quietTimeline(w http.ResponseWriter, channel, after, limit string) {
	w.Header().Set("content-type", "application/json")
	_, _ = w.Write([]byte(`[]`))
}

// sseWrites writes event blocks and flushes each, so the client sees them at once.
func sseWrites(w http.ResponseWriter, blocks ...string) {
	w.Header().Set("content-type", "text/event-stream")
	for _, block := range blocks {
		_, _ = fmt.Fprint(w, block)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// runListen runs listen in w with a deadline, the way a caller cancels it once it has served its
// purpose: a quiet stream reconnects forever.
func runListen(t *testing.T, w *World, within time.Duration, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	var out, errOut strings.Builder
	code := Huddle(ctx, w, append([]string{"listen"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// listen prints what a hello replayed of the events after --after, skips what the live stream
// then repeats, and writes the last shown seq to --state.
func TestListenReplaysWhatWasMissedAndWritesState(t *testing.T) {
	s := startStreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrites(w,
			"data: {\"type\":\"hello\"}\n\n",
			"data: {\"type\":\"event\",\"data\":{\"seq\":3,\"from\":\"b\",\"kind\":\"pub\",\"msg\":\"hi\"}}\n\n")
		<-r.Context().Done() // quiet: no more blocks, so the client's quiet timer ends the read
	}, func(w http.ResponseWriter, channel, after, limit string) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`[{"seq":3,"from":"b","kind":"pub","msg":"hi"}]`))
	})
	w := huddleWorld(t, s.server.URL, map[string]string{"HUDDLE_LISTEN_QUIET_MS": "400"})
	state := filepath.Join(t.TempDir(), "state.json")
	code, out, errOut := runListen(t, w, 3*time.Second, "--state", state, "--after", "2")
	if code != 0 || errOut != "" {
		t.Fatalf("listen = %d %q %q", code, out, errOut)
	}
	if want := "{\"channel\":\"c\",\"seq\":3,\"from\":\"b\",\"kind\":\"pub\",\"msg\":\"hi\"}\n"; out != want {
		t.Errorf("listen printed %q, want the replayed event once: %q", out, want)
	}
	raw, err := os.ReadFile(state)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if string(raw) != `{"c":3}` {
		t.Errorf("state file %q, want %q", raw, `{"c":3}`)
	}
	saw := false
	for _, q := range s.asked() {
		if q == "timeline?after=2&limit=2000" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("the hello never replayed: %v", s.asked())
	}
}

// own events are filtered out (the caller said who it is with ?as=); --all keeps them and drops
// the ?as= parameter.
func TestListenFiltersOwnEventsUntilAll(t *testing.T) {
	s := startStreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("as") != "" {
			sseWrites(w, "data: {\"type\":\"event\",\"data\":{\"seq\":1,\"from\":\"a\",\"kind\":\"pub\",\"msg\":\"mine\"}}\n\n")
		}
		<-r.Context().Done()
	}, quietTimeline)
	w := huddleWorld(t, s.server.URL, map[string]string{"HUDDLE_LISTEN_QUIET_MS": "300"})
	state := filepath.Join(t.TempDir(), "state.json")
	code, out, errOut := runListen(t, w, 1500*time.Millisecond, "--state", state)
	if code != 0 || errOut != "" {
		t.Fatalf("listen = %d %q %q", code, out, errOut)
	}
	if out != "" {
		t.Errorf("listen printed its own event: %q", out)
	}
	raw, _ := os.ReadFile(state)
	if string(raw) != `{"c":1}` {
		t.Errorf("state file %q, want the last seq even when filtered", raw)
	}
	sawAs := false
	for _, q := range s.asked() {
		if q == "live?as=a" {
			sawAs = true
		}
	}
	if !sawAs {
		t.Errorf("listen never said who it is: %v", s.asked())
	}

	s2 := startStreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		sseWrites(w, "data: {\"type\":\"event\",\"data\":{\"seq\":1,\"from\":\"a\",\"kind\":\"pub\",\"msg\":\"mine\"}}\n\n")
		<-r.Context().Done()
	}, quietTimeline)
	w2 := huddleWorld(t, s2.server.URL, map[string]string{"HUDDLE_LISTEN_QUIET_MS": "300"})
	code2, out2, errOut2 := runListen(t, w2, 1500*time.Millisecond, "--all")
	if code2 != 0 || errOut2 != "" {
		t.Fatalf("listen --all = %d %q %q", code2, out2, errOut2)
	}
	if !strings.Contains(out2, "\"msg\":\"mine\"") {
		t.Errorf("listen --all printed %q, want its own event kept", out2)
	}
	for _, q := range s2.asked() {
		if strings.HasPrefix(q, "live?as=") {
			t.Errorf("listen --all asked %q, want no ?as=", q)
		}
	}
}

// several channels and no --after: each starts from its newest event, and --state keeps the
// channels in arrival order.
func TestListenStartsEachChannelAtItsNewestEvent(t *testing.T) {
	s := startStreamServer(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // no events: the channels only read their newest
	}, func(w http.ResponseWriter, channel, after, limit string) {
		w.Header().Set("content-type", "application/json")
		if channel == "c" {
			_, _ = w.Write([]byte(`[{"seq":7}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"seq":4}]`))
	})
	w := huddleWorld(t, s.server.URL, map[string]string{
		"HUDDLE_LISTEN":          "other",
		"HUDDLE_LISTEN_QUIET_MS": "250",
	})
	state := filepath.Join(t.TempDir(), "state.json")
	code, out, errOut := runListen(t, w, 1500*time.Millisecond, "--state", state)
	if code != 0 || errOut != "" {
		t.Fatalf("listen = %d %q %q", code, out, errOut)
	}
	if out != "" {
		t.Errorf("listen printed %q, want nothing: both channels only found their newest", out)
	}
	// the newest-event start only moves the in-memory cursor: nothing was shown, so nothing is
	// written to --state yet
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Errorf("state file %v appeared before anything was shown", err)
	}
	newest := 0
	for _, q := range s.asked() {
		if q == "timeline?after=0&limit=1" {
			newest++
		}
	}
	if newest != 2 {
		t.Errorf("the newest-event read ran %d times (%v), want once per channel", newest, s.asked())
	}
}

// a stream that never connected is the one listen dies on.
func TestListenDiesWhenTheHuddleIsUnreachable(t *testing.T) {
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := closed.URL
	closed.Close()
	w := huddleWorld(t, url, nil)
	code, out, errOut := runH(t, w, "listen")
	if code != 5 {
		t.Fatalf("listen = %d %q %q, want 5", code, out, errOut)
	}
	if !strings.Contains(errOut, "Huddle unreachable at "+url) {
		t.Errorf("listen said %q", errOut)
	}
}

// sseBlock handles one block of the stream: hello replays, an event prints, anything else only
// with --all, and a block without parsable data says nothing.
func TestSSEBlock(t *testing.T) {
	tests := []struct {
		name   string
		block  string
		all    bool
		prints string
		replay bool
	}{
		{"hello replays", "data: {\"type\":\"hello\"}\n", false, "", true},
		{"an event prints", "data: {\"type\":\"event\",\"data\":{\"seq\":2}}\n", false, "{\"seq\":2}\n", false},
		{"a ping prints only with all", "data: {\"type\":\"ping\"}\n", true, "{\"type\":\"ping\"}\n", false},
		{"a ping stays silent without all", "data: {\"type\":\"ping\"}\n", false, "", false},
		{"no data lines say nothing", ": keep-alive\n", false, "", false},
		{"junk data says nothing", "data: not json\n", false, "", false},
		{"an event without data says nothing", "data: {\"type\":\"event\"}\n", false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			c := &cli{stdout: &out}
			replayed := false
			c.sseBlock(tt.block, tt.all, func(event *jsfmt.Value) {
				fmt.Fprintln(&out, event.EncodeJSON())
			}, func() { replayed = true })
			if out.String() != tt.prints {
				t.Errorf("printed %q, want %q", out.String(), tt.prints)
			}
			if replayed != tt.replay {
				t.Errorf("replayed = %v, want %v", replayed, tt.replay)
			}
		})
	}
}

// withChannel puts the channel in front of an event, keeping the event's own keys and order, and
// an event naming its own channel keeps that value.
func TestWithChannel(t *testing.T) {
	if got := withChannel("c", nil).EncodeJSON(); got != `{"channel":"c"}` {
		t.Errorf("nil event became %s", got)
	}
	event, _ := jsfmt.DecodeValue([]byte(`{"seq":3,"kind":"pub"}`))
	if got := withChannel("c", event).EncodeJSON(); got != `{"channel":"c","seq":3,"kind":"pub"}` {
		t.Errorf("event became %s", got)
	}
	own, _ := jsfmt.DecodeValue([]byte(`{"seq":1,"channel":"other"}`))
	if got := withChannel("c", own).EncodeJSON(); got != `{"channel":"other","seq":1}` {
		t.Errorf("event with its own channel became %s", got)
	}
}

// listenState keeps each channel's last shown seq in arrival order and resumes from the file.
func TestListenStateResumesWhereTheLastOneStopped(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(file, []byte(`{"c":5,"junk":"x","other":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	state := loadListenState(file)
	if seq, ok := state.get("c"); !ok || seq != 5 {
		t.Errorf("c = %d %v, want 5", seq, ok)
	}
	if _, ok := state.get("junk"); ok {
		t.Error("junk survived a load")
	}
	if seq, ok := state.get("other"); !ok || seq != 2 {
		t.Errorf("other = %d %v, want 2", seq, ok)
	}
	state.set("c", 9)
	state.set("fresh", 1)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"c":9,"other":2,"fresh":1}` {
		t.Errorf("state file %q, want the channels in arrival order", raw)
	}
	again := loadListenState(file)
	if seq, _ := again.get("c"); seq != 9 {
		t.Errorf("a reload lost c: %d", seq)
	}
	empty := loadListenState("")
	empty.set("c", 1) // no file: stays in memory only
	if _, err := os.Stat(filepath.Join(t.TempDir(), "nothing")); err == nil {
		t.Error("a stateless listen wrote a file")
	}
}

// listenMS reads one of listen's millisecond knobs: a positive number, else the fallback.
func TestListenMS(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want int
	}{
		{"unset takes the fallback", nil, 45_000},
		{"a number wins", map[string]string{"HUDDLE_LISTEN_QUIET_MS": " 250 "}, 250},
		{"junk takes the fallback", map[string]string{"HUDDLE_LISTEN_QUIET_MS": "soon"}, 45_000},
		{"zero takes the fallback", map[string]string{"HUDDLE_LISTEN_QUIET_MS": "0"}, 45_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (&cli{w: huddleWorld(t, "http://127.0.0.1:1", tt.env)}).listenMS("HUDDLE_LISTEN_QUIET_MS", 45_000); got != tt.want {
				t.Errorf("listenMS = %d, want %d", got, tt.want)
			}
		})
	}
}
