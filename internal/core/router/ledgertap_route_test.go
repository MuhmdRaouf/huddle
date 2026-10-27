package router

// The ledger tap on the live route: one request keeps the bytes the upstream saw as its input
// and the bytes the client read as its output under one body id, an encoded answer is stored
// decoded, a full tap never slows the request it refused, and without a tap the route event is
// what it always was. Every case runs the multi router with one zai lane.

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// tapAnswer is the plain JSON answer the tests' upstreams serve; the gzip one serves it gzipped.
const tapAnswer = `{"type":"message","usage":{"input_tokens":3,"output_tokens":4}}`

// tapSink records every body the tap's writer keeps; a held sink blocks the writer inside
// PutBody until its hold closes.
type tapSink struct {
	mu    sync.Mutex
	calls []tapCall
	hold  chan struct{}
}

func newTapSink() *tapSink { return &tapSink{} }

func newHeldTapSink() *tapSink { return &tapSink{hold: make(chan struct{})} }

func (s *tapSink) PutBody(ctx context.Context, requestID, side string, body []byte, at time.Time) error {
	if s.hold != nil {
		<-s.hold
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, tapCall{id: requestID, side: side, body: append([]byte(nil), body...), at: at})
	return nil
}

// recorded is every body kept, in write order, and fails the test when the count differs.
func (s *tapSink) recorded(t *testing.T, count int) []tapCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) != count {
		t.Fatalf("the sink kept %d bodies, want %d: %+v", len(s.calls), count, s.calls)
	}
	return s.calls
}

// tapJSONUpstream answers every request with one plain JSON answer and records what it saw.
func tapJSONUpstream(t *testing.T, seen *seenList) *url.URL {
	t.Helper()
	return tapUpstream(t, seen, "")
}

// tapGzipUpstream answers every request with the JSON answer gzipped and records what it saw.
func tapGzipUpstream(t *testing.T, seen *seenList) *url.URL {
	t.Helper()
	return tapUpstream(t, seen, "gzip")
}

// tapUpstream serves one upstream that records every body it got and answers the JSON answer,
// gzipped when encoding says so.
func tapUpstream(t *testing.T, seen *seenList, encoding string) *url.URL {
	t.Helper()
	out := []byte(tapAnswer)
	if encoding == "gzip" {
		var buf bytes.Buffer
		writer := gzip.NewWriter(&buf)
		if _, err := writer.Write(out); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		out = buf.Bytes()
	}
	port := serve(t, func(w http.ResponseWriter, req *http.Request) {
		body, err := ReadBody(req, BodyLimitBytes)
		if err != nil {
			return
		}
		seen.record(pathOf(req), req.Header, string(body))
		res := NewResponse(w)
		res.Header().Set("Content-Type", "application/json")
		if encoding != "" {
			res.Header().Set("Content-Encoding", encoding)
		}
		res.WriteHeader(200)
		res.End(out)
	}, nil)
	return mustURL(t, fmt.Sprintf("http://127.0.0.1:%d", port))
}

// tapRouter is one served multi router with one zai lane: its port, the events it wrote, and
// what its provider upstream saw.
type tapRouter struct {
	port     int
	events   *eventLog
	laneSeen *seenList
}

// newTapRouter serves the multi router over one zai lane with the tap given: anthropic is where
// claude-* requests go; a nil provider takes a plain JSON one on the lane.
func newTapRouter(t *testing.T, tap *LedgerTap, anthropic, provider *url.URL) tapRouter {
	t.Helper()
	laneSeen := &seenList{}
	if provider == nil {
		provider = tapJSONUpstream(t, laneSeen)
	}
	events := &eventLog{}
	server, _ := CreateMulti(MultiOptions{
		Anthropic: anthropic,
		Lanes: []RouterOptions{{
			Provider:    routerProvider,
			ProviderURL: provider,
			Key:         func(context.Context) (string, error) { return "zai-secret", nil },
		}},
		Events: events.record,
		Tap:    tap,
	})
	port := reservePort(t)
	serveRouter(t, server, port)
	return tapRouter{port: port, events: events, laneSeen: laneSeen}
}

// flushTap closes the tap so every body it took is in the sink before the test reads them.
func flushTap(t *testing.T, tap *LedgerTap) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tap.Close(ctx); err != nil {
		t.Fatalf("the tap never drained: %v", err)
	}
}

// waitRoute is the request's route event, polled for: the note lands when the answer's last
// byte is out, just ahead of the client reading it.
func waitRoute(t *testing.T, events *eventLog) domain.RouteEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if routes := events.routes(); len(routes) == 1 {
			return routes[0]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("one route event never landed (have %d)", len(events.routes()))
	return domain.RouteEvent{}
}

// postTap sends one request as Claude Code would and reads the whole answer.
func postTap(t *testing.T, port int, body string) answer {
	t.Helper()
	got, err := postRawBody(port, "/v1/messages", body, map[string]string{
		"Content-Type": "application/json",
		// Go's client decompresses what it asked for itself; these answers are read as bytes.
		"Accept-Encoding": "identity",
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestTheTapKeepsWhatWasSentAndWhatCameBack(t *testing.T) {
	t.Parallel()
	sink := newTapSink()
	tap := NewLedgerTap(sink, TapBudget, func(string) {})
	anthropicSeen := &seenList{}
	rig := newTapRouter(t, tap, tapJSONUpstream(t, anthropicSeen), nil)

	got := postTap(t, rig.port, `{"model":"claude-opus-5-5","messages":[]}`)
	if got.status != 200 {
		t.Fatalf("the answer's status = %d, want 200: %s", got.status, got.body)
	}
	if got.body != tapAnswer {
		t.Fatalf("the client read %q, want the answer the upstream served", got.body)
	}
	sent := anthropicSeen.list()
	if len(sent) != 1 {
		t.Fatalf("the Anthropic upstream saw %d requests, want 1", len(sent))
	}
	flushTap(t, tap)

	calls := sink.recorded(t, 2)
	in, out := calls[0], calls[1]
	if in.side != "input" {
		t.Errorf("the first body's side = %q, want input", in.side)
	}
	if in.id == "" {
		t.Error("the bodies carry no id")
	}
	if string(in.body) != sent[0].body {
		t.Errorf("the kept input = %q, want the bytes the upstream saw", in.body)
	}
	if out.side != "output" {
		t.Errorf("the second body's side = %q, want output", out.side)
	}
	if out.id != in.id {
		t.Errorf("the output's id = %q, want the input's %q", out.id, in.id)
	}
	if string(out.body) != tapAnswer {
		t.Errorf("the kept output = %q, want the bytes the client read", out.body)
	}

	event := waitRoute(t, rig.events)
	if event.BodyID == nil || *event.BodyID != in.id {
		t.Errorf("the event's BodyID = %v, want the bodies' id %q", event.BodyID, in.id)
	}
	if event.Body != "kept" {
		t.Errorf("the event's Body = %q, want kept", event.Body)
	}
}

func TestTheTapKeepsAStreamedAnswerWhole(t *testing.T) {
	t.Parallel()
	sink := newTapSink()
	tap := NewLedgerTap(sink, TapBudget, func(string) {})
	anthropicSeen := &seenList{}
	rig := newTapRouter(t, tap, recordingUpstream(t, "anthropic", "", anthropicSeen), nil)

	got := postTap(t, rig.port, `{"model":"claude-opus-5-5","messages":[]}`)
	if got.status != 200 {
		t.Fatalf("the answer's status = %d, want 200: %s", got.status, got.body)
	}
	want := "data: anthropic-1\n\ndata: anthropic-2\n\n"
	if got.body != want {
		t.Fatalf("the client read %q, want %q", got.body, want)
	}
	flushTap(t, tap)

	calls := sink.recorded(t, 2)
	if calls[0].side != "input" || calls[1].side != "output" {
		t.Fatalf("the sink kept sides %q then %q, want input then output", calls[0].side, calls[1].side)
	}
	if calls[1].id != calls[0].id {
		t.Errorf("the output's id = %q, want the input's %q", calls[1].id, calls[0].id)
	}
	if string(calls[1].body) != want {
		t.Errorf("the kept output = %q, want the whole stream", calls[1].body)
	}
	if event := waitRoute(t, rig.events); event.BodyID == nil || event.Body != "kept" {
		t.Errorf("the event's bodies = %v/%q, want an id and kept", event.BodyID, event.Body)
	}
}

func TestTheTapStoresAGzipAnswerDecoded(t *testing.T) {
	t.Parallel()
	sink := newTapSink()
	tap := NewLedgerTap(sink, TapBudget, func(string) {})
	rig := newTapRouter(t, tap, tapGzipUpstream(t, &seenList{}), nil)

	got := postTap(t, rig.port, `{"model":"claude-opus-5-5","messages":[]}`)
	if got.status != 200 {
		t.Fatalf("the answer's status = %d, want 200: %s", got.status, got.body)
	}
	if got.body == tapAnswer {
		t.Fatal("the client read the plain answer, want the gzip bytes the upstream served")
	}
	if header := got.header.Get("Content-Encoding"); header != "gzip" {
		t.Errorf("the answer's Content-Encoding = %q, want gzip", header)
	}
	flushTap(t, tap)

	calls := sink.recorded(t, 2)
	if calls[1].side != "output" {
		t.Fatalf("the second body's side = %q, want output", calls[1].side)
	}
	if string(calls[1].body) != tapAnswer {
		t.Errorf("the sink kept %q, want the answer decoded to plain bytes", calls[1].body)
	}
	if event := waitRoute(t, rig.events); event.Body != "kept" {
		t.Errorf("the event's Body = %q, want kept", event.Body)
	}
}

func TestTheTapNeverDelaysARequestWhenFull(t *testing.T) {
	t.Parallel()

	// The same request twice: once behind a tap whose budget is spent and whose sink is held,
	// once with no tap at all.
	run := func(tap *LedgerTap, sink *tapSink) (domain.RouteEvent, time.Duration) {
		rig := newTapRouter(t, tap, tapJSONUpstream(t, &seenList{}), nil)
		if sink != nil {
			t.Cleanup(func() { close(sink.hold) })
		}
		start := time.Now()
		got := postTap(t, rig.port, `{"model":"claude-opus-5-5","messages":[]}`)
		took := time.Since(start)
		if got.status != 200 || got.body != tapAnswer {
			t.Fatalf("the client read %d %q, want 200 and the whole answer", got.status, got.body)
		}
		return waitRoute(t, rig.events), took
	}

	sink := newHeldTapSink()
	tap := NewLedgerTap(sink, 1, func(string) {})
	fullEvent, full := run(tap, sink)
	_, plain := run(nil, nil)

	if fullEvent.BodyID == nil {
		t.Error("the full tap left the event without a body id")
	}
	if fullEvent.Body != "dropped" {
		t.Errorf("the event's Body = %q, want dropped", fullEvent.Body)
	}
	if full > plain+50*time.Millisecond {
		t.Errorf("the request behind the full tap took %s, over 50 ms more than the %s without a tap", full, plain)
	}
	flushTap(t, tap) // a full tap held nothing, so the writer is not stuck in the sink
	sink.recorded(t, 0)
}

func TestTheTapTakesTheInputAndRefusesTheOutput(t *testing.T) {
	t.Parallel()
	asked := `{"model":"claude-opus-5-5","messages":[]}`
	sink := newTapSink()
	// A budget one over the request body takes the input; the answer is longer than the
	// request, so it cannot fit even on its own.
	tap := NewLedgerTap(sink, int64(len(asked))+1, func(string) {})
	rig := newTapRouter(t, tap, tapJSONUpstream(t, &seenList{}), nil)

	got := postTap(t, rig.port, asked)
	if got.status != 200 {
		t.Fatalf("the answer's status = %d, want 200: %s", got.status, got.body)
	}
	flushTap(t, tap)

	calls := sink.recorded(t, 1)
	if calls[0].side != "input" {
		t.Errorf("the kept body's side = %q, want input", calls[0].side)
	}
	if string(calls[0].body) != asked {
		t.Errorf("the kept input = %q, want the request as sent", calls[0].body)
	}
	event := waitRoute(t, rig.events)
	if event.BodyID == nil {
		t.Error("the event carries no body id although the tap took the input")
	}
	if event.Body != "dropped" {
		t.Errorf("the event's Body = %q, want dropped", event.Body)
	}
}

func TestTheProviderLaneKeepsTheAdaptedBody(t *testing.T) {
	t.Parallel()
	sink := newTapSink()
	tap := NewLedgerTap(sink, TapBudget, func(string) {})
	rig := newTapRouter(t, tap, tapJSONUpstream(t, &seenList{}), nil)

	asked := `{"model":"glm-5.3","messages":[],"max_tokens":999999}`
	got := postTap(t, rig.port, asked)
	if got.status != 200 {
		t.Fatalf("the answer's status = %d, want 200: %s", got.status, got.body)
	}
	sent := rig.laneSeen.list()
	if len(sent) != 1 {
		t.Fatalf("the zai upstream saw %d requests, want 1", len(sent))
	}
	if sent[0].body == asked {
		t.Fatal("the zai upstream saw the request as sent, want the adapted body (max_tokens clamped)")
	}
	flushTap(t, tap)

	calls := sink.recorded(t, 2)
	if in := calls[0]; in.side != "input" {
		t.Errorf("the first body's side = %q, want input", in.side)
	} else if string(in.body) != sent[0].body {
		t.Errorf("the kept input = %q, want the body the zai upstream saw", in.body)
	}
	if event := waitRoute(t, rig.events); event.Body != "kept" {
		t.Errorf("the event's Body = %q, want kept", event.Body)
	}
}

func TestNoTapLeavesTheEventAsItWas(t *testing.T) {
	t.Parallel()
	rig := newTapRouter(t, nil, tapJSONUpstream(t, &seenList{}), nil)
	got := postTap(t, rig.port, `{"model":"claude-opus-5-5","messages":[]}`)
	if got.status != 200 {
		t.Fatalf("the answer's status = %d, want 200: %s", got.status, got.body)
	}
	event := waitRoute(t, rig.events)
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "body_id") || strings.Contains(string(data), `"body"`) {
		t.Errorf("the event without a tap = %s, want it without body_id and body", data)
	}
}
