package gateway

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/spool"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The proxy: a request that arrives on the loopback goes to its route's upstream byte for byte
// and header for header (the hop-by-hop ones excepted), the answer comes back byte for byte —
// streamed or not — and one route line is recorded per request. A stranger's Host is refused, a
// client's cancel reaches the upstream, and a failing upstream is passed through and recorded.

// upstreamSpy is what one httptest upstream saw.
type upstreamSpy struct {
	body   []byte
	header http.Header
	path   string
	query  string
	method string
	host   string
}

// recorder collects the route lines one test's gateway wrote, and can pass each one on to
// another writer (the spool, for the radar-ingest test).
type recorder struct {
	mu     sync.Mutex
	events []domain.RouteEvent
	out    func(domain.RouteEvent)
}

func (r *recorder) record(event domain.RouteEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	if r.out != nil {
		r.out(event)
	}
}

func (r *recorder) all() []domain.RouteEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.RouteEvent(nil), r.events...)
}

// waitFor holds the test until the gateway has recorded n route lines — the client can be done
// reading an answer while the handler is still writing its line — and answers what it saw.
func (r *recorder) waitFor(t *testing.T, n int) []domain.RouteEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if events := r.all(); len(events) >= n {
			return events
		}
		time.Sleep(2 * time.Millisecond)
	}
	return r.all()
}

// captureUpstream builds an upstream that answers answer with status and content type, keeping
// what it saw in spy.
func captureUpstream(t *testing.T, spy *upstreamSpy, status int, contentType, answer string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("upstream read: %v", err)
		}
		*spy = upstreamSpy{
			body: body, header: r.Header.Clone(), path: r.URL.Path,
			query: r.URL.RawQuery, method: r.Method, host: r.Host,
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(server.Close)
	return server
}

// gatewayOver builds a gateway serving config, recording into rec.
func gatewayOver(t *testing.T, config Config, rec *recorder) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(NewServer(config, rec.record, nil))
	t.Cleanup(server.Close)
	return server
}

// post sends one JSON request to the gateway as a loopback client would.
func post(t *testing.T, url, path, body string, header map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range header {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

const messageBody = `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`

func TestProxyForwardsRequestAndAnswerUnchanged(t *testing.T) {
	var spy upstreamSpy
	upstream := captureUpstream(t, &spy, http.StatusOK, "application/json", `{"id":"msg_1","usage":{"input_tokens":4,"output_tokens":2}}`)
	rec := &recorder{}
	gateway := gatewayOver(t, Config{Anthropic: upstream.URL}, rec)

	response := post(t, gateway.URL, "/v1/messages?beta=true", messageBody, map[string]string{
		"Authorization":     "Bearer sk-huddle-test",
		"Anthropic-Version": "2023-06-01",
		// the hop-by-hop pair: the header and the token it names never reach the upstream
		"Connection": "X-Tracing",
		"X-Tracing":  "drop-me",
	})
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", response.StatusCode)
	}
	if got, want := string(body), `{"id":"msg_1","usage":{"input_tokens":4,"output_tokens":2}}`; got != want {
		t.Errorf("answer = %q, want %q", got, want)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("answer content type = %q, want application/json", got)
	}

	if spy.body == nil {
		t.Fatal("the upstream saw no request")
	}
	if got, want := string(spy.body), messageBody; got != want {
		t.Errorf("upstream body = %q, want %q", got, want)
	}
	if spy.method != http.MethodPost || spy.path != "/v1/messages" || spy.query != "beta=true" {
		t.Errorf("upstream saw %s %s?%s, want POST /v1/messages?beta=true", spy.method, spy.path, spy.query)
	}
	if spy.host != strings.TrimPrefix(upstream.URL, "http://") {
		t.Errorf("upstream Host = %q, want its own %q", spy.host, upstream.URL)
	}
	for key, want := range map[string]string{
		"Authorization":     "Bearer sk-huddle-test",
		"Anthropic-Version": "2023-06-01",
		"Content-Type":      "application/json",
	} {
		if got := spy.header.Get(key); got != want {
			t.Errorf("upstream %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"Connection", "X-Tracing"} {
		if _, ok := spy.header[key]; ok {
			t.Errorf("upstream saw hop-by-hop %s", key)
		}
	}

	events := rec.waitFor(t, 1)
	if len(events) != 1 {
		t.Fatalf("recorded %d route lines, want 1", len(events))
	}
	event := events[0]
	if event.Plugin != "gateway" || event.Route != "gateway" || event.Via != "gateway" {
		t.Errorf("route line = plugin %q route %q via %q, want gateway throughout", event.Plugin, event.Route, event.Via)
	}
	if event.Model != "glm-5.3" || event.Status != http.StatusOK || event.Upstream != upstream.URL {
		t.Errorf("route line = model %q status %d upstream %q", event.Model, event.Status, event.Upstream)
	}
	if event.LatencyMs < 0 {
		t.Errorf("route line latency = %d, want a measured one", event.LatencyMs)
	}
	if _, ok := jsscalar.ParseISO(event.Ts); !ok {
		t.Errorf("route line ts = %q, want ISO", event.Ts)
	}
}

// A streamed answer reaches the client event by event: the upstream waits until the client has
// the first event before writing the second, so a buffering gateway deadlocks the test instead
// of passing it.
func TestProxyStreamsAnswerWithFlushes(t *testing.T) {
	first := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"glm-5.3\",\"usage\":{\"input_tokens\":25,\"output_tokens\":1}}}\n\n"
	second := "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":15}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	gotFirst := make(chan struct{})
	upstreamDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, first)
		flusher.Flush()
		select {
		case <-gotFirst:
		case <-time.After(5 * time.Second):
			t.Error("the client never took the first event; the gateway buffered the stream")
			return
		}
		_, _ = io.WriteString(w, second)
	}))
	t.Cleanup(upstream.Close)
	rec := &recorder{}
	gateway := gatewayOver(t, Config{Anthropic: upstream.URL}, rec)

	response := post(t, gateway.URL, "/v1/messages", messageBody, nil)
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading the first event line: %v", err)
	}
	if line != "event: message_start\n" {
		t.Errorf("first line = %q, want the message start event", line)
	}
	close(gotFirst)
	rest, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the rest: %v", err)
	}
	if got, want := line+string(rest), first+second; got != want {
		t.Errorf("stream = %q, want %q", got, want)
	}
	select {
	case <-upstreamDone:
	case <-time.After(5 * time.Second):
		t.Error("the upstream never finished")
	}

	events := rec.waitFor(t, 1)
	if len(events) != 1 {
		t.Fatalf("recorded %d route lines, want 1", len(events))
	}
	usage := events[0].Usage
	if usage == nil || (*usage)["input_tokens"] != int64(25) || (*usage)["output_tokens"] != int64(15) {
		t.Errorf("stream route line usage = %v, want 25 in and 15 out", usage)
	}
}

func TestProxyChoosesTheUpstreamByRoute(t *testing.T) {
	var routed, fallback upstreamSpy
	routedUpstream := captureUpstream(t, &routed, http.StatusOK, "application/json", "{}")
	fallbackUpstream := captureUpstream(t, &fallback, http.StatusOK, "application/json", "{}")
	rec := &recorder{}
	gateway := gatewayOver(t, Config{
		Routes:    []Route{{Match: "glm-*", Upstream: routedUpstream.URL}},
		Anthropic: fallbackUpstream.URL,
		OpenAI:    fallbackUpstream.URL,
	}, rec)

	post(t, gateway.URL, "/v1/messages", messageBody, nil)
	if routed.body == nil || fallback.body != nil {
		t.Errorf("glm-5.3 reached its route (%v), the fallback saw it too (%v)", routed.body != nil, fallback.body != nil)
	}

	post(t, gateway.URL, "/v1/messages", `{"model":"claude-sonnet-4","messages":[]}`, nil)
	if fallback.body == nil {
		t.Error("a model no route names never reached the family default")
	}

	chatBody := `{"model":"gpt-5","messages":[]}`
	post(t, gateway.URL, "/v1/chat/completions", chatBody, nil)
	if fallback.body == nil || string(fallback.body) != chatBody {
		t.Errorf("chat completions went to the wrong upstream or lost bytes: %q", fallback.body)
	}

	responsesBody := `{"model":"gpt-5","input":"hi"}`
	post(t, gateway.URL, "/v1/responses", responsesBody, nil)
	if fallback.body == nil || string(fallback.body) != responsesBody {
		t.Errorf("responses went to the wrong upstream or lost bytes: %q", fallback.body)
	}
}

// A path none of the three spoken APIs use is refused, and nothing is forwarded or recorded.
func TestProxyRefusesUnspokenPaths(t *testing.T) {
	var spy upstreamSpy
	upstream := captureUpstream(t, &spy, http.StatusOK, "application/json", "{}")
	rec := &recorder{}
	gateway := gatewayOver(t, Config{Anthropic: upstream.URL}, rec)

	response := post(t, gateway.URL, "/v1/embeddings", `{"model":"glm-5.3","input":"x"}`, nil)
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", response.StatusCode)
	}
	if spy.body != nil {
		t.Error("the upstream saw an unspoken path")
	}
	if len(rec.all()) != 0 {
		t.Error("an unspoken path left a route line")
	}
}

func TestProxyRecordsEachUsageShape(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		body      string
		answer    string
		wantModel string
		want      map[string]int64
	}{
		{
			"chat stream without a usage chunk records no tokens",
			"/v1/chat/completions", `{"model":"glm-5.3","stream":true,"messages":[]}`,
			"data: {\"choices\":[]}\n\ndata: [DONE]\n\n",
			"glm-5.3", nil,
		},
		{
			"responses stream reads response.completed",
			"/v1/responses", `{"model":"gpt-5","stream":true,"input":"hi"}`,
			"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":50,\"input_tokens_details\":{\"cached_tokens\":10},\"output_tokens\":8}}}\n\n",
			"gpt-5", map[string]int64{"input_tokens": 40, "output_tokens": 8, "cache_read_input_tokens": 10},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var spy upstreamSpy
			upstream := captureUpstream(t, &spy, http.StatusOK, "text/event-stream", tc.answer)
			rec := &recorder{}
			gateway := gatewayOver(t, Config{Anthropic: upstream.URL, OpenAI: upstream.URL}, rec)

			post(t, gateway.URL, tc.path, tc.body, nil)
			events := rec.waitFor(t, 1)
			if len(events) != 1 {
				t.Fatalf("recorded %d route lines, want 1", len(events))
			}
			event := events[0]
			if event.Model != tc.wantModel {
				t.Errorf("model = %q, want %q", event.Model, tc.wantModel)
			}
			if tc.want == nil {
				if event.Usage != nil {
					t.Errorf("usage = %v, want none recorded", event.Usage)
				}
				return
			}
			if event.Usage == nil {
				t.Fatalf("usage = nil, want %v", tc.want)
			}
			for key, want := range tc.want {
				if got := (*event.Usage)[key]; got != want {
					t.Errorf("usage[%s] = %v, want %d", key, got, want)
				}
			}
		})
	}
}

func TestProxyPassesAnUpstreamErrorThrough(t *testing.T) {
	const answer = `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
	var spy upstreamSpy
	upstream := captureUpstream(t, &spy, http.StatusTooManyRequests, "application/json", answer)
	rec := &recorder{}
	gateway := gatewayOver(t, Config{Anthropic: upstream.URL}, rec)

	response := post(t, gateway.URL, "/v1/messages", messageBody, nil)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading the answer: %v", err)
	}
	if response.StatusCode != http.StatusTooManyRequests || string(body) != answer {
		t.Errorf("got %d %q, want 429 %q", response.StatusCode, body, answer)
	}
	events := rec.waitFor(t, 1)
	if len(events) != 1 {
		t.Fatalf("recorded %d route lines, want 1", len(events))
	}
	event := events[0]
	if event.Status != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", event.Status)
	}
	if event.Error == nil || *event.Error == "" {
		t.Errorf("error = %v, want the failed upstream named", event.Error)
	}
}

// A client that walks away takes its upstream request with it.
func TestProxyClientCancelCancelsUpstream(t *testing.T) {
	upstreamSawCancel := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {}\n\n")
		flusher.Flush()
		<-r.Context().Done()
		close(upstreamSawCancel)
	}))
	t.Cleanup(upstream.Close)
	rec := &recorder{}
	gateway := gatewayOver(t, Config{Anthropic: upstream.URL}, rec)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/messages", strings.NewReader(messageBody))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer response.Body.Close()
	if _, err := bufio.NewReader(response.Body).ReadString('\n'); err != nil {
		t.Fatalf("reading the first event: %v", err)
	}
	cancel()
	select {
	case <-upstreamSawCancel:
	case <-time.After(5 * time.Second):
		t.Error("the upstream never saw the cancel")
	}
}

// A Host that is not this machine is refused with 421 before anything is forwarded or recorded.
func TestProxyRefusesAStrangersHost(t *testing.T) {
	var spy upstreamSpy
	upstream := captureUpstream(t, &spy, http.StatusOK, "application/json", "{}")
	rec := &recorder{}
	gateway := gatewayOver(t, Config{Anthropic: upstream.URL}, rec)

	request, err := http.NewRequest(http.MethodPost, gateway.URL+"/v1/messages", strings.NewReader(messageBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "evil.example"
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("status = %d, want 421", response.StatusCode)
	}
	if spy.body != nil {
		t.Error("the upstream saw a stranger's request")
	}
	if len(rec.all()) != 0 {
		t.Error("a refused request left a route line")
	}
}

// The harness a client names rides on the route line; a client that names none is unknown.
func TestProxyRecordsTheHarness(t *testing.T) {
	var spy upstreamSpy
	upstream := captureUpstream(t, &spy, http.StatusOK, "application/json", "{}")
	rec := &recorder{}
	gateway := gatewayOver(t, Config{Anthropic: upstream.URL}, rec)

	post(t, gateway.URL, "/v1/messages", messageBody, map[string]string{"X-Huddle-Harness": "codex"})
	post(t, gateway.URL, "/v1/messages", messageBody, nil)
	// Both posts must have landed before the length is judged: waiting for one and
	// asserting two raced the handler that had not written its line yet.
	events := rec.waitFor(t, 2)
	if len(events) != 2 {
		t.Fatalf("recorded %d route lines, want 2", len(events))
	}
	if events[0].Harness != "codex" {
		t.Errorf("harness = %q, want codex", events[0].Harness)
	}
	if events[1].Harness != "unknown" {
		t.Errorf("harness = %q, want unknown when no header named one", events[1].Harness)
	}
}

// Acceptance: a gateway run in a test leaves route lines radar ingests — asserted through
// radar's own parser and record builder, the way the dashboard will read them.
func TestRouteLinesReachRadar(t *testing.T) {
	var spy upstreamSpy
	upstream := captureUpstream(t, &spy, http.StatusOK, "application/json",
		`{"id":"msg_1","usage":{"input_tokens":12,"output_tokens":34,"cache_creation_input_tokens":2,"cache_read_input_tokens":7}}`)

	radarHome := t.TempDir()
	env := domain.EnvLookup{"RADAR_HOME": radarHome}
	write := spool.CreateSpoolWriter(env, nil, nil)
	config := Config{
		Routes:    []Route{{Match: "glm-*", Upstream: upstream.URL}},
		Anthropic: "https://api.anthropic.com",
	}
	gateway := gatewayOver(t, config, &recorder{out: func(event domain.RouteEvent) { write(event) }})

	response := post(t, gateway.URL, "/v1/messages", messageBody, map[string]string{"X-Huddle-Harness": "codex"})
	if _, err := io.ReadAll(response.Body); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("gateway answered %d (%v)", response.StatusCode, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	var lines []shared.SpoolLine
	for time.Now().Before(deadline) {
		files := ingest.ListSpoolFiles(ctx, env, time.Now().UnixMilli(), 30)
		lines = ingest.ReadSpoolLines(files)
		if len(lines) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(lines) != 1 {
		names, _ := filepath.Glob(filepath.Join(radarHome, "spool", "*"))
		t.Fatalf("radar's spool holds %d lines (%v), want the one route line", len(lines), names)
	}
	line := lines[0]
	if line.Event != "route" || line.Model == nil || *line.Model != "glm-5.3" {
		t.Fatalf("line = event %q model %v, want a route line on glm-5.3", line.Event, line.Model)
	}
	ts, ok := jsscalar.ParseISO(line.Ts)
	if !ok {
		t.Fatalf("line ts = %q, want ISO", line.Ts)
	}
	record := ingest.RouteRequest(line, *line.Model, ts.UnixMilli())
	if record.Via == nil || *record.Via != "gateway" || record.Route == nil || *record.Route != "gateway" {
		t.Errorf("record = via %v route %v, want gateway throughout", record.Via, record.Route)
	}
	wantTokens := shared.Tokens{Input: 12, Output: 34, CacheRead: 7, CacheWrite: 2}
	if record.Tokens != wantTokens {
		t.Errorf("record tokens = %+v, want %+v", record.Tokens, wantTokens)
	}
	if record.StopReason != nil {
		t.Errorf("record stop reason = %q, want none on a 200", *record.StopReason)
	}
	value := line.Field("harness")
	if value == nil || value.Kind() != jsfmt.KindString || value.Text() != "codex" {
		t.Errorf("line harness = %v, want codex", value)
	}
}

// A body that cannot be read, and one past the cap, are refused before any upstream is
// named: 413, and no route line, for both.
func TestProxyRefusesABodyItCannotRead(t *testing.T) {
	var rec recorder
	gateway := NewServer(Config{Anthropic: "https://api.anthropic.example"}, rec.record, nil)
	t.Run("a body that fails to read", func(t *testing.T) {
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18870/v1/messages", errReader{}))
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", w.Code)
		}
	})
	t.Run("a body past the cap", func(t *testing.T) {
		w := httptest.NewRecorder()
		body := bytes.NewReader(make([]byte, maxRequestBytes+1))
		gateway.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "http://127.0.0.1:18870/v1/messages", body))
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, want 413", w.Code)
		}
	})
	if events := rec.all(); len(events) != 0 {
		t.Errorf("recorded %d route lines, want none: no request reached an upstream", len(events))
	}
}

// errReader is a request body whose read always fails.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the body is gone") }

// A config with nothing to say sends no request anywhere: the client learns there is no
// upstream, and the gateway keeps no line for a request it did not forward.
func TestProxyRefusesARequestWithNoUpstream(t *testing.T) {
	var rec recorder
	gateway := gatewayOver(t, Config{}, &rec)
	response := post(t, gateway.URL, "/v1/messages", messageBody, nil)
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", response.StatusCode)
	}
	if !strings.Contains(string(body), "no upstream") {
		t.Errorf("body = %q, want the missing upstream named", body)
	}
	if events := rec.all(); len(events) != 0 {
		t.Errorf("recorded %d route lines, want none: nothing left the gateway", len(events))
	}
}

// A config that skipped validation can name an upstream no URL can parse; the request
// build fails, and the client learns of it.
func TestProxyRefusesAnUpstreamItCannotName(t *testing.T) {
	var rec recorder
	gateway := gatewayOver(t, Config{Anthropic: "http://exa mple.invalid"}, &rec)
	response := post(t, gateway.URL, "/v1/messages", messageBody, nil)
	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", response.StatusCode)
	}
}

// An upstream that never answers is 502 to the client and one recorded line with the
// problem on it — the model is still the body's.
func TestProxyRecordsAnUnreachableUpstream(t *testing.T) {
	var rec recorder
	gateway := gatewayOver(t, Config{Anthropic: "http://127.0.0.1:1"}, &rec)
	response := post(t, gateway.URL, "/v1/messages", messageBody, nil)
	if response.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", response.StatusCode)
	}
	events := rec.waitFor(t, 1)
	if len(events) != 1 {
		t.Fatalf("recorded %d route lines, want 1", len(events))
	}
	if events[0].Status != http.StatusBadGateway {
		t.Errorf("line status = %d, want 502", events[0].Status)
	}
	if events[0].Error == nil || !strings.Contains(*events[0].Error, "upstream unreachable") {
		t.Errorf("line error = %v, want the unreachable upstream named", events[0].Error)
	}
	if events[0].Model != "glm-5.3" {
		t.Errorf("line model = %q, want the body's model", events[0].Model)
	}
}

// The header copy to the client takes the answer's headers and leaves the hop-by-hop ones
// behind.
func TestCopyHeadersLeavesTheHopByHopOnesBehind(t *testing.T) {
	dst := http.Header{}
	src := http.Header{
		"Connection":   {"close"},
		"Content-Type": {"application/json"},
	}
	copyHeaders(dst, src)
	if len(dst["Connection"]) != 0 {
		t.Errorf("Connection rode along: %v", dst)
	}
	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want it copied", got)
	}
}

// The model a request names: the body's, and none when the body names nothing readable.
func TestModelOfReadsWhatTheBodyNames(t *testing.T) {
	if got := modelOf([]byte(messageBody)); got != "glm-5.3" {
		t.Errorf("model = %q, want glm-5.3", got)
	}
	for name, body := range map[string]string{
		"not json":       "not json",
		"the wrong type": `{"model":42}`,
		"no model":       `{"messages":[]}`,
	} {
		if got := modelOf([]byte(body)); got != "" {
			t.Errorf("%s: model = %q, want none", name, got)
		}
	}
}

// The gateway answers only a loopback Host, whatever port or bracket form it carries.
func TestLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:18870", true},
		{"localhost:18870", true},
		{"LOCALHOST", true},
		{"127.0.0.1", true},
		{"[127.0.0.1]:80", true},
		{"[localhost]:8080", true},
		{"[::1]:80", false},
		{"example.com:443", false},
		{"127.0.0.2", false},
		{"localhost.example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := loopbackHost(tc.host); got != tc.want {
			t.Errorf("loopbackHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
