package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// reply is one loopback answer as the client read it.
type reply struct {
	status      int
	body        string
	contentType string
}

// call sends one request over real loopback http and reads the whole answer.
func call(t *testing.T, port int, method, path, body string, headers map[string]string) reply {
	t.Helper()
	request, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the %s %s request: %v", method, path, err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("the %s %s request failed: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s %s answer: %v", method, path, err)
	}
	return reply{status: response.StatusCode, body: string(text), contentType: response.Header.Get("Content-Type")}
}

// serve binds one server on a free loopback port and names the port it took, the way the
// running app fills the port in after listen.
func serve(t *testing.T, options HttpOptions) (*HttpServer, int) {
	t.Helper()
	created := CreateHttpServer(options)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind the loopback listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	created.SetPort(port)
	go func() { _ = created.Server.Serve(listener) }()
	t.Cleanup(func() {
		created.CloseStreams()
		_ = created.Server.Close()
	})
	return created, port
}

// failingSettings is insights whose settings save always fails: the write route's 500 path.
type failingSettings struct {
	*Insights
}

func (failingSettings) SetSettings(context.Context, *jsfmt.Value) (settings.Settings, string, error) {
	return settings.Settings{}, "", errors.New("disk full")
}

func TestHttpWritesAndMetrics(t *testing.T) {
	t.Run("only lets JSON from our own origin through", func(t *testing.T) {
		jsonType := http.Header{"Content-Type": []string{"application/json"}}
		if !WriteAllowed(jsonType, 5) {
			t.Error("our own JSON is refused")
		}
		if WriteAllowed(http.Header{"Content-Type": []string{"text/plain"}}, 5) {
			t.Error("plain text is allowed through")
		}
		if WriteAllowed(http.Header{}, 5) {
			t.Error("a nameless content type is allowed through")
		}
		if WriteAllowed(http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"http://evil.test"}}, 5) {
			t.Error("another origin's JSON is allowed through")
		}
		if !WriteAllowed(http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"http://localhost:5"}}, 5) {
			t.Error("our own origin's JSON is refused")
		}
		if WriteAllowed(http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{"cross-site"}}, 5) {
			t.Error("a cross-site fetch is allowed through")
		}
		if !WriteAllowed(http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{"same-origin"}}, 5) {
			t.Error("a same-site fetch is refused")
		}
	})

	t.Run("serves writes and metrics over real loopback http", func(t *testing.T) {
		fresh := seededInsights(t)
		_, port := serve(t, HttpOptions{
			Store:     fresh.store,
			Version:   "0.0.1",
			StartedAt: 1,
			Insights:  fresh.insights,
		})
		jsonHeaders := map[string]string{"Content-Type": "application/json"}
		budgets := `{"budgets":[{"id":"a","scope":"total","period":"week","limitUsd":3,"action":"warn"}]}`
		if reply := call(t, port, "GET", "/api/budgets", "", nil); reply.status != 200 {
			t.Errorf("GET /api/budgets = %d %s, want 200", reply.status, reply.body)
		} else if reply.body == "" || strings.Contains(reply.body, `"budgets":null`) {
			// the dashboard reads null as "not loaded yet"; an empty store is a list, not nothing
			t.Errorf("GET /api/budgets = %s, want budgets as a list", reply.body)
		}
		if reply := call(t, port, "PUT", "/api/budgets", budgets, jsonHeaders); reply.status != 200 {
			t.Errorf("PUT /api/budgets = %d %s, want 200", reply.status, reply.body)
		}
		if held := fresh.insights.Budgets(); len(held) != 1 {
			t.Errorf("the saved budgets hold %d entries, want 1", len(held))
		}
		if reply := call(t, port, "PUT", "/api/budgets", budgets, map[string]string{"Content-Type": "text/plain"}); reply.status != 403 {
			t.Errorf("a plain-text write = %d, want 403", reply.status)
		}
		if reply := call(t, port, "PUT", "/api/budgets", budgets, map[string]string{
			"Content-Type": "application/json",
			"Origin":       "http://evil.test",
		}); reply.status != 403 {
			t.Errorf("a cross-origin write = %d, want 403", reply.status)
		}
		if reply := call(t, port, "POST", "/api/budgets", budgets, jsonHeaders); reply.status != 405 {
			t.Errorf("POST the budgets = %d, want 405", reply.status)
		}
		if reply := call(t, port, "PUT", "/api/summary", "{}", jsonHeaders); reply.status != 405 {
			t.Errorf("PUT the summary = %d, want 405", reply.status)
		}
		if reply := call(t, port, "PUT", "/api/settings", "{nope", jsonHeaders); reply.status != 400 {
			t.Errorf("a malformed settings body = %d, want 400", reply.status)
		}
		if reply := call(t, port, "PUT", "/api/settings", strings.Repeat("x", 70_000), jsonHeaders); reply.status != 413 {
			t.Errorf("an oversized settings body = %d, want 413", reply.status)
		}
		dismissed := call(t, port, "POST", "/api/alerts/dismiss", `{"id":"k"}`, jsonHeaders)
		if dismissed.status != 200 || strings.TrimSpace(dismissed.body) != `{"dismissed":"k"}` {
			t.Errorf("the dismissal = %d %s, want the id back", dismissed.status, dismissed.body)
		}
		metrics := call(t, port, "GET", "/metrics", "", nil)
		if metrics.status != 200 {
			t.Errorf("GET /metrics = %d, want 200", metrics.status)
		}
		if metrics.contentType != "text/plain; version=0.0.4; charset=utf-8" {
			t.Errorf("/metrics content type = %q", metrics.contentType)
		}
		if !strings.Contains(metrics.body, "radar_requests_total") {
			t.Errorf("/metrics body has no requests family:\n%s", metrics.body)
		}
		if head := call(t, port, "HEAD", "/metrics", "", nil); head.body != "" {
			t.Errorf("HEAD /metrics body = %q, want empty", head.body)
		}
	})

	t.Run("answers 500 when a save fails and an empty /metrics without insights", func(t *testing.T) {
		fresh := seededInsights(t)
		_, brokenPort := serve(t, HttpOptions{
			Store:     fresh.store,
			Version:   "0.0.1",
			StartedAt: 1,
			Insights:  failingSettings{fresh.insights},
		})
		answer := call(t, brokenPort, "PUT", "/api/settings", "{}", map[string]string{"Content-Type": "application/json"})
		if answer.status != 500 {
			t.Errorf("a failed save = %d, want 500", answer.status)
		}
		if !strings.Contains(answer.body, "disk full") {
			t.Errorf("the failed save's body = %s, want it to name the error", answer.body)
		}
		_, barePort := serve(t, HttpOptions{Store: fresh.store, Version: "0.0.1", StartedAt: 1})
		if metrics := call(t, barePort, "GET", "/metrics", "", nil); metrics.body != "" {
			t.Errorf("/metrics without insights = %q, want empty", metrics.body)
		}
	})
}

// A flipping watcher tells the streams when the ingest backlog word moves.
type flipWatcher struct {
	mu   sync.Mutex
	word bool
}

func (f *flipWatcher) CatchingUp() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.word
}

func (f *flipWatcher) flip() {
	f.mu.Lock()
	f.word = !f.word
	f.mu.Unlock()
}

func TestHttpReads(t *testing.T) {
	t.Run("serves the api over loopback, and every page answers the hub's tab", func(t *testing.T) {
		fresh := seededInsights(t)
		_, port := serve(t, HttpOptions{Store: fresh.store, Version: "0.0.1", StartedAt: 1})
		answer := call(t, port, "GET", "/api/summary", "", nil)
		if answer.status != 200 {
			t.Errorf("GET /api/summary = %d, want 200", answer.status)
		}
		if answer.contentType != "application/json; charset=utf-8" {
			t.Errorf("the api content type = %q", answer.contentType)
		}
		if !strings.HasSuffix(answer.body, "\n") {
			t.Errorf("the api body does not end in a newline: %q", answer.body)
		}
		for _, table := range []struct {
			path string
			want string
		}{
			{"/nope", "radar's pages are the huddle hub's Radar tab"},
			{"/fonts/../app.js", "radar's pages are the huddle hub's Radar tab"},
			{"/fonts/nosuch.txt", "radar's pages are the huddle hub's Radar tab"},
		} {
			var body struct {
				Error string `json:"error"`
			}
			answer := call(t, port, "GET", table.path, "", nil)
			if err := json.Unmarshal([]byte(answer.body), &body); err != nil {
				t.Fatalf("decode %s's body %q: %v", table.path, answer.body, err)
			}
			if body.Error != table.want {
				t.Errorf("GET %s = %q, want %q", table.path, body.Error, table.want)
			}
		}
		if head := call(t, port, "HEAD", "/api/summary", "", nil); head.status != 200 || head.body != "" {
			t.Errorf("HEAD the api = %d %q, want an empty 200", head.status, head.body)
		}
	})

	t.Run("refuses a host that is not ours", func(t *testing.T) {
		fresh := seededInsights(t)
		_, port := serve(t, HttpOptions{Store: fresh.store, Version: "0.0.1", StartedAt: 1})
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("dial the server: %v", err)
		}
		defer func() { _ = conn.Close() }()
		request := "GET /api/summary HTTP/1.1\r\nHost: evil.test\r\nConnection: close\r\n\r\n"
		if _, err := conn.Write([]byte(request)); err != nil {
			t.Fatalf("write the probe: %v", err)
		}
		raw, err := io.ReadAll(conn)
		if err != nil {
			t.Fatalf("read the probe's answer: %v", err)
		}
		if !strings.Contains(string(raw), " 403 ") || !strings.Contains(string(raw), "forbidden host") {
			t.Errorf("the rebinding probe = %q, want a 403", raw)
		}
	})

	t.Run("streams a snapshot, the deltas and the ingest word until the client goes", func(t *testing.T) {
		fresh := seededInsights(t)
		watcher := &flipWatcher{}
		_, port := serve(t, HttpOptions{
			Store:     fresh.store,
			Version:   "0.0.1",
			StartedAt: 1,
			Watcher:   watcher,
			// a fast beat keeps the test short
			HeartbeatMs: 100,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("http://127.0.0.1:%d/api/stream", port), nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("open the stream: %v", err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
			t.Fatalf("the stream content type = %q", response.Header.Get("Content-Type"))
		}
		frames := make(chan string, 64)
		go func() {
			scanner := bufio.NewScanner(response.Body)
			scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.HasPrefix(line, "data: ") {
					frames <- strings.TrimPrefix(line, "data: ")
				}
			}
		}()
		nextFrame := func(want string) string {
			t.Helper()
			select {
			case frame := <-frames:
				if !strings.HasPrefix(frame, want) {
					t.Fatalf("frame = %s, want one starting %q", frame, want)
				}
				return frame
			case <-time.After(4 * time.Second):
				t.Fatalf("no frame arrived, want one starting %q", want)
			}
			return ""
		}
		// waitFrame reads until the wanted frame arrives, letting the ingest words through:
		// they ride the ingest timer's own half-second beat, which no store update orders
		// against, so the word may land before the delta the update caused.
		waitFrame := func(want string) string {
			t.Helper()
			deadline := time.After(4 * time.Second)
			for {
				select {
				case frame := <-frames:
					if strings.HasPrefix(frame, want) {
						return frame
					}
					if !strings.HasPrefix(frame, `{"type":"ingest"`) {
						t.Fatalf("frame = %s, want one starting %q", frame, want)
					}
				case <-deadline:
					t.Fatalf("no frame arrived, want one starting %q", want)
				}
			}
		}
		snapshot := nextFrame(`{"type":"snapshot"`)
		if !strings.Contains(snapshot, `"catchingUp":false`) {
			t.Errorf("the snapshot = %s, want the watcher's word", snapshot)
		}
		fresh.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts = "r9", nowAt.UnixMilli()
		}), nil)
		waitFrame(`{"type":"request"`)
		// the sessions rebuild and the ingest flip each ride their own half-second timer, so
		// the two frames may arrive in either order
		watcher.flip()
		sawSessions, sawIngest := false, false
		for deadline := time.After(4 * time.Second); !sawSessions || !sawIngest; {
			var frame string
			select {
			case frame = <-frames:
			case <-deadline:
				t.Fatalf("the stream stalled: sessions %v ingest %v", sawSessions, sawIngest)
			}
			switch {
			case strings.HasPrefix(frame, `{"type":"sessions"`):
				sawSessions = true
			case strings.HasPrefix(frame, `{"type":"ingest","catchingUp":true`):
				sawIngest = true
			}
		}
		// the heartbeat is a comment, not a data frame; reaching here means the deltas flowed
	})

	t.Run("an idle tracker returns at once, a busy one only once its requests close", func(t *testing.T) {
		tracker := CreateFlightTracker()
		tracker.WhenIdle(context.Background(), 50) // idle: no wait
		release := tracker.Track()
		done := make(chan struct{})
		go func() {
			tracker.WhenIdle(context.Background(), 5_000)
			close(done)
		}()
		select {
		case <-done:
			t.Fatal("a busy tracker answered idle")
		case <-time.After(50 * time.Millisecond):
		}
		release()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the tracker never went idle after its request closed")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		busy := CreateFlightTracker()
		held := busy.Track()
		defer held()
		started := time.Now()
		busy.WhenIdle(ctx, 60_000) // a cancelled caller stops waiting
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("a cancelled wait took %v, want it to stop at once", elapsed)
		}
	})
}

// The HTTP boundary cases over a loopback wire, plus the two createFlightTracker cases.

// full is one loopback answer with every header the boundary checks read.
type full struct {
	status int
	header http.Header
	body   string
}

// fetch sends one request over real loopback http and reads the whole answer.
func fetch(t *testing.T, port int, method, path, body string, headers map[string]string) full {
	t.Helper()
	request, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the %s %s request: %v", method, path, err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("the %s %s request failed: %v", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s %s answer: %v", method, path, err)
	}
	return full{status: response.StatusCode, header: response.Header, body: string(text)}
}

// rawHTTP writes one hand-built request and reads the wire answer to its end, for headers
// and spellings the client would never send itself.
func rawHTTP(t *testing.T, port int, request string) string {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("dial the server: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write the request: %v", err)
	}
	wire, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return string(wire)
}

// backlogWatcher is a settable ingest backlog word.
type backlogWatcher struct {
	mu   sync.Mutex
	word bool
}

func (w *backlogWatcher) CatchingUp() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.word
}

func (w *backlogWatcher) set(word bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.word = word
}

// streamTap is one /api/stream client that accumulates the wire text for inspection.
type streamTap struct {
	mu     sync.Mutex
	wire   string
	ended  bool
	cancel context.CancelFunc
}

// openStream connects one SSE client the way the dashboard does.
func openStream(t *testing.T, port int) *streamTap {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/api/stream", port), nil)
	if err != nil {
		cancel()
		t.Fatalf("build the stream request: %v", err)
	}
	tap := &streamTap{cancel: cancel}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatalf("open the stream: %v", err)
	}
	go func() {
		defer func() { _ = response.Body.Close() }()
		buffer := make([]byte, 8*1024)
		for {
			n, err := response.Body.Read(buffer)
			if n > 0 {
				tap.mu.Lock()
				tap.wire += string(buffer[:n])
				tap.mu.Unlock()
			}
			if err != nil {
				tap.mu.Lock()
				tap.ended = true
				tap.mu.Unlock()
				return
			}
		}
	}()
	return tap
}

func (s *streamTap) buffer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wire
}

func (s *streamTap) endedFlag() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

func (s *streamTap) close() { s.cancel() }

// sseFrameList holds the complete SSE frames of one wire: the data payloads and the
// comment lines.
type sseFrameList struct {
	data     []map[string]any
	comments []string
}

// sseFrames parses the complete frames only: anything past the last blank line is a
// half-arrived frame.
func sseFrames(wire string) sseFrameList {
	complete := wire
	if !strings.HasSuffix(complete, "\n\n") {
		at := strings.LastIndex(complete, "\n\n")
		if at < 0 {
			complete = ""
		} else {
			complete = complete[:at+2]
		}
	}
	frames := sseFrameList{}
	for _, line := range strings.Split(complete, "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			var value map[string]any
			if err := json.Unmarshal([]byte(payload), &value); err != nil {
				continue
			}
			frames.data = append(frames.data, value)
		} else if strings.HasPrefix(line, ":") {
			frames.comments = append(frames.comments, line)
		}
	}
	return frames
}

// countOf counts the data frames of one type.
func (f sseFrameList) countOf(name string) int {
	total := 0
	for _, frame := range f.data {
		if frame["type"] == name {
			total++
		}
	}
	return total
}

// firstOf answers the first data frame of one type, nil when none arrived.
func (f sseFrameList) firstOf(name string) map[string]any {
	for _, frame := range f.data {
		if frame["type"] == name {
			return frame
		}
	}
	return nil
}

// waitFor polls probe until it holds or the budget passes.
func waitFor(t *testing.T, label string, budget time.Duration, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if probe() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// wireInsights builds one insights over st the way the running app wires it.
func wireInsights(t *testing.T, st *store.Store) *Insights {
	t.Helper()
	world := makeEnv(t)
	created := CreateInsights(context.Background(), InsightsOptions{
		Env:    world.env,
		Store:  st,
		Now:    clock,
		TickMs: 0,
	})
	t.Cleanup(created.Stop)
	return created
}

// nowZero flattens the one volatile field of a snapshot frame: the store clock riding
// summary.now.
var nowZero = regexp.MustCompile(`"now":\d+`)

func TestHostAllowed(t *testing.T) {
	t.Run("accepts only the two loopback spellings of this exact port", func(t *testing.T) {
		for _, table := range []struct {
			name string
			host string
			port int
			want bool
		}{
			{"the numeric loopback", "127.0.0.1:8787", 8787, true},
			{"the named loopback", "localhost:8787", 8787, true},
			{"right name, wrong port", "127.0.0.1:8787", 9999, false},
			{"another port on loopback", "127.0.0.1:8788", 8787, false},
			{"no port at all", "127.0.0.1", 8787, false},
			{"localhost with no port", "localhost", 8787, false},
			{"a rebound name", "evil.example:8787", 8787, false},
			{"a rebound name with no port", "evil.example", 8787, false},
			{"the ipv6 loopback", "[::1]:8787", 8787, false},
			{"an uppercase localhost", "LOCALHOST:8787", 8787, false},
			{"a router port", "localhost:18787", 8787, false},
			{"no host header", "", 8787, false},
		} {
			if got := HostAllowed(table.host, table.port); got != table.want {
				t.Errorf("%s: HostAllowed(%q, %d) = %v, want %v", table.name, table.host, table.port, got, table.want)
			}
		}
	})
}

func TestOriginAllowed(t *testing.T) {
	budgets := `{"budgets":[{"id":"a","scope":"total","period":"week","limitUsd":3,"action":"warn"}]}`
	writeFrom := func(t *testing.T, port int, origin string) int {
		t.Helper()
		return call(t, port, "PUT", "/api/budgets", budgets, map[string]string{
			"Content-Type": "application/json",
			"Origin":       origin,
		}).status
	}

	t.Run("the embedding server's predicate is the whole Origin check on writes", func(t *testing.T) {
		fresh := seededInsights(t)
		const hub = "https://hub.example.test:8443"
		_, port := serve(t, HttpOptions{
			Store:         fresh.store,
			Version:       "0.0.1",
			StartedAt:     1,
			Insights:      fresh.insights,
			OriginAllowed: func(origin string) bool { return origin == hub },
		})
		if code := writeFrom(t, port, hub); code != 200 {
			t.Errorf("the hub origin's write = %d, want 200", code)
		}
		if held := fresh.insights.Budgets(); len(held) != 1 {
			t.Errorf("the saved budgets hold %d entries, want 1", len(held))
		}
		// the predicate replaces the rule, so radar's own loopback spelling passes only when
		// the embedding server names it too
		if code := writeFrom(t, port, fmt.Sprintf("http://127.0.0.1:%d", port)); code != 403 {
			t.Errorf("a loopback origin the predicate does not name = %d, want 403", code)
		}
		if code := writeFrom(t, port, "https://evil.example"); code != 403 {
			t.Errorf("a foreign origin = %d, want 403", code)
		}
	})

	t.Run("standalone radar keeps its loopback-only Origin rule", func(t *testing.T) {
		fresh := seededInsights(t)
		_, port := serve(t, HttpOptions{
			Store:     fresh.store,
			Version:   "0.0.1",
			StartedAt: 1,
			Insights:  fresh.insights,
		})
		if code := writeFrom(t, port, "https://hub.example.test:8443"); code != 403 {
			t.Errorf("a non-loopback origin on standalone radar = %d, want 403", code)
		}
	})
}

func TestWriteAllowedBoundary(t *testing.T) {
	t.Run("takes JSON from our own origin only", func(t *testing.T) {
		for _, table := range []struct {
			name    string
			header  http.Header
			port    int
			allowed bool
		}{
			{"our own JSON", http.Header{"Content-Type": []string{"application/json"}}, 8787, true},
			{"JSON with a charset", http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}, 8787, true},
			{"an uppercase content type", http.Header{"Content-Type": []string{"APPLICATION/JSON"}}, 8787, true},
			{"plain text", http.Header{"Content-Type": []string{"text/plain"}}, 8787, false},
			{"a form body", http.Header{"Content-Type": []string{"multipart/form-data; boundary=x"}}, 8787, false},
			{"a nameless content type", http.Header{}, 8787, false},
			{"a foreign origin", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"http://evil.test"}}, 8787, false},
			{"our numeric origin", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"http://127.0.0.1:8787"}}, 8787, true},
			{"our localhost origin", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"http://localhost:8787"}}, 8787, true},
			{"our origin on another port", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"http://127.0.0.1:9999"}}, 8787, false},
			{"an https origin", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"https://127.0.0.1:8787"}}, 8787, false},
			{"a sandboxed null origin", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"null"}}, 8787, false},
			{"an empty origin", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{""}}, 8787, false},
			// a second, hostile value joins the first the way the runtime hands the header
			// over, so it must not hide behind our own spelling
			{"our origin first, a hostile second", http.Header{"Content-Type": []string{"application/json"}, "Origin": []string{"http://127.0.0.1:8787", "http://evil.test"}}, 8787, false},
			{"a cross-site fetch", http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{"cross-site"}}, 8787, false},
			{"a same-site fetch", http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{"same-site"}}, 8787, false},
			{"a same-origin fetch", http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{"same-origin"}}, 8787, true},
			{"a none fetch, the bookmark case", http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{"none"}}, 8787, true},
			{"an empty site word", http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{""}}, 8787, false},
			{"a hostile second site word", http.Header{"Content-Type": []string{"application/json"}, "Sec-Fetch-Site": []string{"same-origin", "cross-site"}}, 8787, false},
		} {
			if got := WriteAllowed(table.header, table.port); got != table.allowed {
				t.Errorf("%s: WriteAllowed = %v, want %v", table.name, got, table.allowed)
			}
		}
	})
}

func TestDnsRebindingGuard(t *testing.T) {
	t.Run("rejects foreign, missing and wrong-port Host headers with 403", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		foreign := rawHTTP(t, port, fmt.Sprintf(
			"GET / HTTP/1.1\r\nHost: evil.example:%d\r\nConnection: close\r\n\r\n", port))
		if !strings.HasPrefix(foreign, "HTTP/1.1 403") {
			t.Errorf("the foreign host's answer starts %q, want a 403 status line", firstLine(foreign))
		}
		if !strings.Contains(foreign, `"error":"forbidden host"`) {
			t.Errorf("the foreign host's answer = %q, want the forbidden host error", foreign)
		}
		if strings.HasPrefix(foreign, "HTTP/1.1 200") {
			t.Error("the foreign host's answer is a 200: a byte of the dashboard leaked")
		}
		noHost := rawHTTP(t, port, "GET / HTTP/1.0\r\nConnection: close\r\n\r\n")
		if !strings.Contains(firstLine(noHost), " 403 ") {
			t.Errorf("the hostless answer starts %q, want a 403 status line", firstLine(noHost))
		}
		wrongPort := rawHTTP(t, port, "GET / HTTP/1.1\r\nHost: 127.0.0.1:1\r\nConnection: close\r\n\r\n")
		if !strings.HasPrefix(wrongPort, "HTTP/1.1 403") {
			t.Errorf("the wrong-port answer starts %q, want a 403 status line", firstLine(wrongPort))
		}
	})

	t.Run("serves both loopback spellings of this port", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		numeric := fetch(t, port, "GET", "/api/health", "", nil)
		if numeric.status != 200 {
			t.Errorf("GET /api/health = %d, want 200", numeric.status)
		}
		local := rawHTTP(t, port, fmt.Sprintf(
			"GET /api/health HTTP/1.1\r\nHost: localhost:%d\r\nConnection: close\r\n\r\n", port))
		if !strings.HasPrefix(local, "HTTP/1.1 200") {
			t.Errorf("the localhost answer starts %q, want a 200 status line", firstLine(local))
		}
	})
}

// firstLine answers a wire answer's status line.
func firstLine(wire string) string {
	line, _, _ := strings.Cut(wire, "\r\n")
	return line
}

func TestHttpMethods(t *testing.T) {
	t.Run("answers GET and HEAD only", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		post := rawHTTP(t, port, fmt.Sprintf(
			"POST /api/health HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", port))
		if !strings.HasPrefix(post, "HTTP/1.1 405") {
			t.Errorf("the POST answer starts %q, want a 405 status line", firstLine(post))
		}
		if !strings.Contains(post, `"error":"method not allowed"`) {
			t.Errorf("the POST answer = %q, want the method error", post)
		}
		head := fetch(t, port, "HEAD", "/api/health", "", nil)
		if head.status != 200 {
			t.Errorf("HEAD /api/health = %d, want 200", head.status)
		}
		if head.body != "" { // the body is suppressed; the headers stay
			t.Errorf("HEAD /api/health body = %q, want empty", head.body)
		}
		if got := head.header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Errorf("HEAD /api/health content type = %q", got)
		}
	})
}

// TestPagesAnswerTheHubsTab: the server serves its API only, and any page request answers
// the one 404 that names where the pages live.
func TestPagesAnswerTheHubsTab(t *testing.T) {
	st := store.CreateStore(clock)
	_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
	for _, path := range []string{"/", "/index.html", "/app.js"} {
		answer := call(t, port, "GET", path, "", nil)
		if answer.status != 404 {
			t.Errorf("GET %s = %d, want 404", path, answer.status)
		}
		want := `{"error":"radar's pages are the huddle hub's Radar tab"}` + "\n"
		if answer.body != want {
			t.Errorf("GET %s body = %q, want %q", path, answer.body, want)
		}
	}
	if answer := call(t, port, "GET", "/api/summary", "", nil); answer.status != 200 {
		t.Errorf("GET /api/summary = %d, want 200: the api stays", answer.status)
	}
}

func TestJsonApi(t *testing.T) {
	t.Run("reports health from the live store, with no CORS headers", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		empty := fetch(t, port, "GET", "/api/health", "", nil)
		if empty.status != 200 {
			t.Fatalf("GET /api/health = %d, want 200", empty.status)
		}
		if got := empty.header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("the health cache control = %q", got)
		}
		if got := empty.header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("the health answer carries access-control-allow-origin %q, want none", got)
		}
		var health struct {
			OK        bool   `json:"ok"`
			Version   string `json:"version"`
			StartedAt int64  `json:"startedAt"`
			Port      int    `json:"port"`
			Sessions  int64  `json:"sessions"`
			UptimeMs  any    `json:"uptimeMs"`
		}
		decode(t, empty.body, &health)
		if !health.OK || health.Version != "test" || health.Port != port || health.Sessions != 0 || health.StartedAt != 1_000 {
			t.Errorf("health = %+v, want the build facts and no sessions", health)
		}
		if health.UptimeMs == nil {
			t.Error("health uptimeMs is missing, want a number")
		}
		cwd := "/w/app"
		st.AddSpoolLine(shared.SpoolLine{Ts: isoOf(nowAt.UnixMilli()), Event: "SessionStart", SessionID: strPtr("s1"), Cwd: &cwd})
		var fed struct {
			Sessions int64 `json:"sessions"`
		}
		decode(t, fetch(t, port, "GET", "/api/health", "", nil).body, &fed)
		if fed.Sessions != 1 {
			t.Errorf("the fed health sessions = %d, want 1", fed.Sessions)
		}
		var sessions struct {
			Sessions []struct {
				ID string `json:"id"`
			} `json:"sessions"`
		}
		decode(t, fetch(t, port, "GET", "/api/sessions", "", nil).body, &sessions)
		if len(sessions.Sessions) != 1 || sessions.Sessions[0].ID != "s1" {
			t.Errorf("the session ids = %+v, want [s1]", sessions.Sessions)
		}
	})

	t.Run("lists the route table for unknown api paths", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		res := fetch(t, port, "GET", "/api/nope", "", nil)
		if res.status != 404 {
			t.Fatalf("GET /api/nope = %d, want 404", res.status)
		}
		var body struct {
			Error  string   `json:"error"`
			Routes []string `json:"routes"`
		}
		decode(t, res.body, &body)
		if body.Error != "no such route: /api/nope" {
			t.Errorf("the error = %q", body.Error)
		}
		found := false
		for _, route := range body.Routes {
			if route == "/api/summary" {
				found = true
			}
		}
		if !found {
			t.Errorf("the routes = %v, want /api/summary among them", body.Routes)
		}
	})
}

// decode fills target from one answer's JSON body.
func decode(t *testing.T, body string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), target); err != nil {
		t.Fatalf("decode the body %q: %v", body, err)
	}
}

func TestStream(t *testing.T) {
	t.Run("opens with a retry hint and a snapshot of the current store", func(t *testing.T) {
		st := store.CreateStore(clock)
		cwd := "/w/app"
		st.AddSpoolLine(shared.SpoolLine{Ts: isoOf(nowAt.UnixMilli()), Event: "SessionStart", SessionID: strPtr("s1"), Cwd: &cwd})
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		stream := openStream(t, port)
		defer stream.close()
		waitFor(t, "the snapshot frame", 2*time.Second, func() bool {
			return len(sseFrames(stream.buffer()).data) == 1
		})
		wire := stream.buffer()
		if !strings.HasPrefix(wire, "retry: 3000\n\n") {
			t.Errorf("the wire starts %q, want the retry hint", firstN(wire, 20))
		}
		snapshot := sseFrames(wire).firstOf("snapshot")
		if snapshot == nil {
			t.Fatalf("the wire holds no snapshot: %q", wire)
		}
		sessions, _ := snapshot["sessions"].([]any)
		if len(sessions) != 1 || sessions[0].(map[string]any)["id"] != "s1" {
			t.Errorf("the snapshot sessions = %v, want [s1]", snapshot["sessions"])
		}
		if snapshot["models"] == nil || snapshot["summary"] == nil {
			t.Errorf("the snapshot = %v, want models and summary objects", snapshot)
		}
		if snapshot["catchingUp"] != false { // no watcher wired: the server is never mid-backlog
			t.Errorf("the snapshot catchingUp = %v, want false", snapshot["catchingUp"])
		}
	})

	t.Run("tells the open streams when the ingest backlog word flips", func(t *testing.T) {
		watcher := &backlogWatcher{word: true}
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000, Watcher: watcher})
		stream := openStream(t, port)
		defer stream.close()
		waitFor(t, "the snapshot frame", 2*time.Second, func() bool {
			return sseFrames(stream.buffer()).firstOf("snapshot") != nil
		})
		if snapshot := sseFrames(stream.buffer()).firstOf("snapshot"); snapshot["catchingUp"] != true {
			t.Errorf("the snapshot catchingUp = %v, want the watcher's word up front", snapshot["catchingUp"])
		}
		watcher.set(false) // the backlog drained
		waitFor(t, "the ingest message", 2*time.Second, func() bool {
			for _, frame := range sseFrames(stream.buffer()).data {
				if frame["type"] == "ingest" && frame["catchingUp"] == false {
					return true
				}
			}
			return false
		})
	})

	t.Run("pushes records as they land, then unsubscribes when the client detaches", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		stream := openStream(t, port)
		waitFor(t, "the snapshot frame", 2*time.Second, func() bool {
			return len(sseFrames(stream.buffer()).data) == 1
		})
		prompt := "hi"
		st.AddSpoolLine(shared.SpoolLine{
			Ts: isoOf(nowAt.UnixMilli()), Event: "UserPromptSubmit", SessionID: strPtr("s1"), Prompt: &prompt,
		})
		waitFor(t, "the event and its session refresh", 2*time.Second, func() bool {
			frames := sseFrames(stream.buffer())
			return frames.firstOf("event") != nil && frames.firstOf("sessions") != nil
		})
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts = "r-live", nowAt.UnixMilli()
		}), nil)
		waitFor(t, "the streamed request", 2*time.Second, func() bool {
			return strings.Contains(stream.buffer(), "r-live")
		})
		streamed := sseFrames(stream.buffer()).firstOf("request")
		if streamed == nil {
			t.Fatalf("the wire holds no request frame: %q", stream.buffer())
		}
		if request, _ := streamed["request"].(map[string]any); request == nil || request["id"] != "r-live" {
			t.Errorf("the streamed request = %v, want r-live", streamed["request"])
		}
		stream.close()
		// let the server take the close in before the ghost lands: the unsubscribe rides the
		// handler's exit, which follows the connection's end at the OS's pace
		time.Sleep(120 * time.Millisecond)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts = "r-ghost", nowAt.UnixMilli()
		}), nil)
		time.Sleep(120 * time.Millisecond)
		if strings.Contains(stream.buffer(), "r-ghost") { // the close handler unsubscribed us
			t.Error("the detached stream received r-ghost")
		}
	})

	t.Run("a change that lands as the snapshot goes out still reaches the client", func(t *testing.T) {
		st := store.CreateStore(clock)
		// one spool line lands the moment the snapshot frame is written — the span between
		// the snapshot and the deltas, where a client not yet subscribed loses the change
		// for good
		prompt := "during the snapshot"
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000, SnapshotSent: func() {
			st.AddSpoolLine(shared.SpoolLine{
				Ts: isoOf(nowAt.UnixMilli()), Event: "UserPromptSubmit", SessionID: strPtr("s1"), Prompt: &prompt,
			})
		}})
		stream := openStream(t, port)
		defer stream.close()
		waitFor(t, "the snapshot frame", 2*time.Second, func() bool {
			return sseFrames(stream.buffer()).firstOf("snapshot") != nil
		})
		waitFor(t, "the event frame the change landed", 2*time.Second, func() bool {
			return sseFrames(stream.buffer()).firstOf("event") != nil
		})
		atSnapshot, atEvent := -1, -1
		for at, frame := range sseFrames(stream.buffer()).data {
			switch frame["type"] {
			case "snapshot":
				atSnapshot = at
			case "event":
				atEvent = at
			}
		}
		if atSnapshot < 0 || atEvent < 0 || atEvent < atSnapshot {
			t.Fatalf("the frames = %v, want the snapshot then the event", sseFrames(stream.buffer()).data)
		}
		event := sseFrames(stream.buffer()).firstOf("event")["event"].(map[string]any)
		if event["kind"] != "UserPromptSubmit" || event["sessionId"] != "s1" {
			t.Errorf("the streamed event = %v, want the landed prompt line", event)
		}
	})

	t.Run("folds a burst of changes into one sessions message, the record deltas still immediate", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		stream := openStream(t, port)
		defer stream.close()
		waitFor(t, "the snapshot frame", 2*time.Second, func() bool {
			return len(sseFrames(stream.buffer()).data) == 1
		})
		// a catch-up burst: many single-record changes inside one coalescing window
		for i := 0; i < 25; i++ {
			session := fmt.Sprintf("s%d", i)
			prompt := fmt.Sprintf("prompt %d", i)
			st.AddSpoolLine(shared.SpoolLine{
				Ts: isoOf(nowAt.UnixMilli() + int64(i)), Event: "UserPromptSubmit",
				SessionID: &session, Prompt: &prompt,
			})
		}
		waitFor(t, "the folded sessions message", 2*time.Second, func() bool {
			return sseFrames(stream.buffer()).countOf("sessions") == 1
		})
		waitFor(t, "the 25 event deltas", 2*time.Second, func() bool {
			return sseFrames(stream.buffer()).countOf("event") == 25
		})
		folded := sseFrames(stream.buffer()).firstOf("sessions")
		if folded == nil {
			t.Fatalf("the wire holds no sessions message: %q", stream.buffer())
		}
		sessions, _ := folded["sessions"].([]any)
		if len(sessions) != 25 {
			t.Errorf("the folded sessions hold %d entries, want 25", len(sessions))
		}
		// one full-state refresh for the whole burst, however many lines fed it
		if within := sseFrames(stream.buffer()).countOf("sessions"); within != 1 {
			t.Errorf("the burst produced %d sessions messages, want one", within)
		}
		// the next change after the window opens a new refresh
		prompt := "later"
		st.AddSpoolLine(shared.SpoolLine{
			Ts: isoOf(nowAt.UnixMilli() + 1_000), Event: "UserPromptSubmit",
			SessionID: strPtr("s26"), Prompt: &prompt,
		})
		waitFor(t, "a second sessions message", 2*time.Second, func() bool {
			return sseFrames(stream.buffer()).countOf("sessions") == 2
		})
	})

	t.Run("beats a heartbeat comment between updates", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000, HeartbeatMs: 50})
		stream := openStream(t, port)
		defer stream.close()
		waitFor(t, "a heartbeat", 2*time.Second, func() bool {
			for _, comment := range sseFrames(stream.buffer()).comments {
				if comment == ":hb" {
					return true
				}
			}
			return false
		})
	})

	t.Run("closeStreams ends every live stream", func(t *testing.T) {
		st := store.CreateStore(clock)
		srv, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		stream := openStream(t, port)
		defer stream.close()
		waitFor(t, "the snapshot frame", 2*time.Second, func() bool {
			return len(sseFrames(stream.buffer()).data) == 1
		})
		srv.CloseStreams()
		waitFor(t, "the stream to end", 2*time.Second, func() bool {
			return stream.endedFlag()
		})
	})

	t.Run("answers HEAD on the stream without a body", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		head := fetch(t, port, "HEAD", "/api/stream", "", nil)
		if head.status != 200 {
			t.Errorf("HEAD /api/stream = %d, want 200", head.status)
		}
		if head.body != "" {
			t.Errorf("HEAD /api/stream body = %q, want empty", head.body)
		}
		if got := head.header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
			t.Errorf("HEAD /api/stream content type = %q", got)
		}
	})
}

// firstN answers the first n characters of a wire, for failure messages.
func firstN(wire string, n int) string {
	if len(wire) <= n {
		return wire
	}
	return wire[:n]
}

func TestHistoryOverTheWire(t *testing.T) {
	// seedWireTree fills one live tree the wire tests read.
	seedWireTree := func(t *testing.T, h *history.History, now int64) {
		t.Helper()
		repo := "/w/app"
		branch := "main"
		name := "Wire"
		last := now
		mustUpsert(t, h, historyNode(func(n *history.NodeUpsert) {
			n.Repo, n.Branch, n.Name, n.LastAt = &repo, &branch, &name, &last
		}))
		latency := int64(5)
		mustPutRequest(t, h, "s1/main", shared.RequestRecord{
			ID:        "rw",
			SessionID: "s1",
			AgentID:   "main",
			Model:     "glm-5.3",
			Upstream:  "https://api.z.ai",
			Ts:        now,
			LatencyMs: &latency,
			Tokens:    shared.Tokens{Input: 10, Output: 4},
			Provider:  "zai",
		})
	}

	t.Run("503s every history read when no history is wired", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000})
		for _, path := range []string{
			"/api/history/roots",
			"/api/history/tree/x",
			"/api/history/requests",
			"/api/history/content/x",
			"/api/history/context/x",
			"/api/history/repos",
			"/api/history/stats",
		} {
			res := fetch(t, port, "GET", path, "", nil)
			if res.status != 503 {
				t.Errorf("GET %s = %d, want 503", path, res.status)
			}
			var body struct {
				Error string `json:"error"`
			}
			decode(t, res.body, &body)
			if body.Error != "history unavailable" {
				t.Errorf("GET %s error = %q, want history unavailable", path, body.Error)
			}
		}
	})

	t.Run("serves the seeded tree through every read route", func(t *testing.T) {
		h := openTestHistory(t)
		seedWireTree(t, h, time.Now().UnixMilli())
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{
			Store: st, Version: "test", StartedAt: 1_000, Insights: wireInsights(t, st), History: h,
		})
		var roots struct {
			Roots []struct {
				ID      string   `json:"id"`
				Reqs    int64    `json:"requests"`
				CostUsd *float64 `json:"costUsd"`
			} `json:"roots"`
		}
		decode(t, fetch(t, port, "GET", "/api/history/roots", "", nil).body, &roots)
		if len(roots.Roots) != 1 || roots.Roots[0].ID != "s1/main" {
			t.Fatalf("the roots = %+v, want s1/main", roots.Roots)
		}
		if roots.Roots[0].Reqs != 1 || roots.Roots[0].CostUsd == nil {
			t.Errorf("the root = %+v, want one request and a price", roots.Roots[0])
		}
		var tree struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		}
		decode(t, fetch(t, port, "GET", "/api/history/tree/s1%2Fmain", "", nil).body, &tree)
		if len(tree.Nodes) != 1 || tree.Nodes[0].ID != "s1/main" {
			t.Errorf("the tree nodes = %+v, want s1/main", tree.Nodes)
		}
		var requests struct {
			Requests []struct {
				ID   string `json:"id"`
				Cost *struct {
					Detail []string `json:"detail"`
				} `json:"cost"`
			} `json:"requests"`
		}
		decode(t, fetch(t, port, "GET", "/api/history/requests?root=s1%2Fmain", "", nil).body, &requests)
		if len(requests.Requests) != 1 || requests.Requests[0].ID != "rw" {
			t.Fatalf("the requests = %+v, want rw", requests.Requests)
		}
		if requests.Requests[0].Cost == nil || len(requests.Requests[0].Cost.Detail) != 1 ||
			requests.Requests[0].Cost.Detail[0] != "Z.ai list" {
			t.Errorf("the request cost = %+v, want the Z.ai list detail", requests.Requests[0].Cost)
		}
		var repos struct {
			Repos []struct {
				Repo  string `json:"repo"`
				Roots int64  `json:"roots"`
			} `json:"repos"`
		}
		decode(t, fetch(t, port, "GET", "/api/history/repos", "", nil).body, &repos)
		if len(repos.Repos) != 1 || repos.Repos[0].Repo != "/w/app" || repos.Repos[0].Roots != 1 {
			t.Errorf("the repos = %+v, want one /w/app root", repos.Repos)
		}
		var stats struct {
			Nodes         int64 `json:"nodes"`
			Requests      int64 `json:"requests"`
			RetentionDays int64 `json:"retentionDays"`
		}
		decode(t, fetch(t, port, "GET", "/api/history/stats", "", nil).body, &stats)
		if stats.Nodes != 1 || stats.Requests != 1 || stats.RetentionDays != 30 {
			t.Errorf("the stats = %+v, want one node, one request, the default retention", stats)
		}
	})

	t.Run("clears history only as JSON from the dashboard, like every write", func(t *testing.T) {
		h := openTestHistory(t)
		seedWireTree(t, h, time.Now().UnixMilli())
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{
			Store: st, Version: "test", StartedAt: 1_000, Insights: wireInsights(t, st), History: h,
		})
		plain := fetch(t, port, "POST", "/api/history/clear", "{}", map[string]string{"Content-Type": "text/plain"})
		if plain.status != 403 {
			t.Errorf("the plain-text clear = %d, want 403", plain.status)
		}
		foreign := fetch(t, port, "POST", "/api/history/clear", "{}", map[string]string{
			"Content-Type": "application/json",
			"Origin":       "http://evil.test",
		})
		if foreign.status != 403 {
			t.Errorf("the cross-origin clear = %d, want 403", foreign.status)
		}
		cleared := fetch(t, port, "POST", "/api/history/clear", "{}", map[string]string{
			"Content-Type": "application/json",
		})
		if cleared.status != 200 {
			t.Fatalf("the JSON clear = %d, want 200", cleared.status)
		}
		var clearedBody struct {
			Cleared bool `json:"cleared"`
		}
		decode(t, cleared.body, &clearedBody)
		if !clearedBody.Cleared {
			t.Errorf("the clear's body = %q, want cleared true", cleared.body)
		}
		var stats struct {
			Nodes int64 `json:"nodes"`
		}
		decode(t, fetch(t, port, "GET", "/api/history/stats", "", nil).body, &stats)
		if stats.Nodes != 0 {
			t.Errorf("the stats nodes after the clear = %d, want 0", stats.Nodes)
		}
		if got := fetch(t, port, "GET", "/api/history/clear", "", nil).status; got != 404 {
			t.Errorf("GET the clear = %d, want 404: GET was never a route", got)
		}
		absent := fetch(t, port, "POST", "/api/history/clear", "{}", map[string]string{
			"Content-Type": "application/json",
		})
		if absent.status != 200 { // clearing an empty history is fine
			t.Errorf("the second clear = %d, want 200", absent.status)
		}
	})
}

func TestFlightTrackerCases(t *testing.T) {
	t.Run("is idle at once with nothing counted, and wakes when the last counted response closes", func(t *testing.T) {
		tracker := CreateFlightTracker()
		tracker.WhenIdle(context.Background(), 1_000) // idle: no wait
		releaseFirst := tracker.Track()
		releaseSecond := tracker.Track()
		done := make(chan struct{})
		go func() {
			tracker.WhenIdle(context.Background(), 1_000)
			close(done)
		}()
		releaseFirst()
		select {
		case <-done:
			t.Fatal("the tracker answered idle with one response still out")
		case <-time.After(20 * time.Millisecond):
		}
		releaseSecond()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("the tracker never went idle after the last response closed")
		}
	})

	t.Run("gives up after the timeout rather than waiting forever", func(t *testing.T) {
		tracker := CreateFlightTracker()
		release := tracker.Track()
		defer release()
		started := time.Now()
		tracker.WhenIdle(context.Background(), 25)
		if elapsed := time.Since(started); elapsed < 20*time.Millisecond { // the timeout, not a close, ended it
			t.Errorf("the wait took %v, want the timeout to end it", elapsed)
		}
	})
}

func TestWireGoldens(t *testing.T) {
	t.Run("pins the SSE opening frames against the recorded wire", func(t *testing.T) {
		st := store.CreateStore(clock)
		_, port := serve(t, HttpOptions{Store: st, Version: "test", StartedAt: 1_000, HeartbeatMs: 40})
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("dial the server: %v", err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte(fmt.Sprintf(
			"GET /api/stream HTTP/1.1\r\nHost: 127.0.0.1:%d\r\n\r\n", port))); err != nil {
			t.Fatalf("write the stream request: %v", err)
		}
		var wire []byte
		buffer := make([]byte, 4*1024)
		for !bytes.Contains(wire, []byte(":hb\n\n\r\n")) {
			n, err := conn.Read(buffer)
			wire = append(wire, buffer[:n]...)
			if err != nil {
				t.Fatalf("read the stream: %v (wire so far %q)", err, wire)
			}
		}
		body := wire[bytes.Index(wire, []byte("\r\n\r\n"))+4:]
		cut := bytes.Index(body, []byte(":hb\n\n\r\n")) + len(":hb\n\n\r\n")
		body = body[:cut]
		golden, err := os.ReadFile("testdata/http_sse_open_golden.txt")
		if err != nil {
			t.Fatalf("read the golden: %v", err)
		}
		if normalized := nowZero.ReplaceAllString(string(body), `"now":0`); normalized != string(golden) {
			t.Errorf("the SSE opening = %q, want the golden %q", normalized, golden)
		}
	})
}

func TestStreamOverflowClosesTheStream(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	conn := &streamConn{frames: make(chan string, 1), cancel: cancel}
	conn.enqueue("data: one\n\n")
	if ctx.Err() != nil {
		t.Fatal("a stream with room was closed")
	}
	conn.enqueue("data: two\n\n")
	if ctx.Err() == nil {
		t.Fatal("a stream too slow to take a frame stayed open, missing the frame")
	}
}
