package router

// The ledger tap: bodies reach the sink through one writer while the request path never waits —
// an offer takes a slice and a budget slot or answers false at once, the queue frees its budget
// as bodies are written, and Close writes what is still queued.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var errFake = errors.New("sink failed")

// tapCall is one body a fake sink recorded.
type tapCall struct {
	id, side string
	body     []byte
	at       time.Time
}

// fakeSink records every PutBody in order on an unbuffered channel, so a test that does not
// receive holds the writer inside PutBody. entered carries one token when PutBody is entered,
// so a test can hold the writer after it took an item from the queue. The first failFirst
// writes fail.
type fakeSink struct {
	calls     chan tapCall
	entered   chan struct{}
	failFirst int
}

func newFakeSink() *fakeSink {
	return &fakeSink{calls: make(chan tapCall), entered: make(chan struct{}, 1)}
}

func (s *fakeSink) PutBody(ctx context.Context, requestID, side string, body []byte, at time.Time) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	s.calls <- tapCall{id: requestID, side: side, body: append([]byte(nil), body...), at: at}
	if s.failFirst > 0 {
		s.failFirst--
		return errFake
	}
	return nil
}

// nextCall receives the next recorded call, failing the test when none arrives in time.
func nextCall(t *testing.T, s *fakeSink) tapCall {
	t.Helper()
	select {
	case call := <-s.calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("no body reached the sink in time")
		return tapCall{}
	}
}

// closeTap drains nothing: the sink must be released already. It fails when the writer does not
// end in time.
func closeTap(t *testing.T, tap *LedgerTap) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tap.Close(ctx); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
}

// bodyOf is one body of n bytes all carrying seed, so bodies differ at a glance.
func bodyOf(n int, seed byte) []byte {
	return bytes.Repeat([]byte{seed}, n)
}

func TestTheTapNeverDelaysARequest(t *testing.T) {
	t.Parallel()
	at := time.Now()

	// a full budget refuses the next body at once, and the writer is held
	sink := newFakeSink()
	tap := NewLedgerTap(sink, 1<<10, func(string) {})
	for i := 0; i < 3; i++ {
		if !tap.Offer("req", "input", bodyOf(300, byte('a'+i)), at) {
			t.Fatalf("offer %d refused while the budget had room", i)
		}
	}
	start := time.Now()
	if tap.Offer("req", "input", bodyOf(300, 'd'), at) {
		t.Error("the offer past the budget was accepted")
	}
	if elapsed := time.Since(start); elapsed > time.Millisecond {
		t.Errorf("a refused offer took %s, want under 1ms", elapsed)
	}
	for i := 0; i < 3; i++ {
		call := nextCall(t, sink)
		if call.id != "req" || call.side != "input" || !bytes.Equal(call.body, bodyOf(300, byte('a'+i))) || !call.at.Equal(at) {
			t.Errorf("call %d = %s/%s %q at %v, want req/input %q at %v", i, call.id, call.side, call.body, call.at, bodyOf(300, byte('a'+i)), at)
		}
	}
	closeTap(t, tap)

	// a full queue refuses the next body at once: the writer holds one body, the channel
	// holds tapSlots more
	sink2 := newFakeSink()
	tap2 := NewLedgerTap(sink2, 1<<30, func(string) {})
	first := bodyOf(10, 'x')
	if !tap2.Offer("r0", "output", first, at) {
		t.Fatal("the first offer into an empty tap was refused")
	}
	<-sink2.entered
	for i := 1; i <= tapSlots; i++ {
		if !tap2.Offer(fmt.Sprintf("r%d", i), "output", bodyOf(10, 'y'), at) {
			t.Fatalf("offer r%d refused while the queue had room", i)
		}
	}
	start = time.Now()
	if tap2.Offer("r-full", "output", bodyOf(10, 'z'), at) {
		t.Error("an offer onto a full queue was accepted")
	}
	if elapsed := time.Since(start); elapsed > time.Millisecond {
		t.Errorf("a refused offer took %s, want under 1ms", elapsed)
	}
	call := nextCall(t, sink2)
	if call.id != "r0" || !bytes.Equal(call.body, first) || call.side != "output" || !call.at.Equal(at) {
		t.Errorf("first call = %s/%s %q at %v, want r0/output the first body at %v", call.id, call.side, call.body, call.at, at)
	}
	for i := 1; i <= tapSlots; i++ {
		if call := nextCall(t, sink2); call.id != fmt.Sprintf("r%d", i) {
			t.Fatalf("call has id %s, want r%d: offer order not kept", call.id, i)
		}
	}
	closeTap(t, tap2)
}

func TestTheTapFreesItsBudgetAsItWrites(t *testing.T) {
	t.Parallel()
	sink := newFakeSink()
	tap := NewLedgerTap(sink, 1<<10, func(string) {})
	at := time.Now()
	for i := 0; i < 3; i++ {
		if !tap.Offer("req", "input", bodyOf(300, byte('a'+i)), at) {
			t.Fatalf("offer %d refused while the budget had room", i)
		}
	}
	if tap.Offer("req", "input", bodyOf(300, 'd'), at) {
		t.Error("the offer past the budget was accepted")
	}
	if call := nextCall(t, sink); !bytes.Equal(call.body, bodyOf(300, 'a')) {
		t.Errorf("first written body = %q, want the first offered", call.body)
	}
	// one body is written, its bytes rejoin the budget, and the refused offer now fits
	deadline := time.Now().Add(2 * time.Second)
	for offered := false; !offered; {
		if offered = tap.Offer("req", "input", bodyOf(300, 'd'), at); offered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the budget never freed after a body was written")
		}
		time.Sleep(100 * time.Microsecond)
	}
	for i, seed := range []byte{'b', 'c', 'd'} {
		if call := nextCall(t, sink); !bytes.Equal(call.body, bodyOf(300, seed)) {
			t.Errorf("written body %d = %q, want seed %q", i, call.body, seed)
		}
	}
	closeTap(t, tap)
}

func TestTheTapLogsAFailedWriteWithoutTheBody(t *testing.T) {
	t.Parallel()
	sink := newFakeSink()
	sink.failFirst = 1
	lines := make(chan string, 8)
	tap := NewLedgerTap(sink, 1<<10, func(line string) { lines <- line })
	secret := []byte("SECRET-PROMPT-never-in-a-log")
	if !tap.Offer("req-1", "input", secret, time.Now()) {
		t.Fatal("the first offer was refused")
	}
	if !tap.Offer("req-2", "output", bodyOf(11, 'y'), time.Now()) {
		t.Fatal("the second offer was refused")
	}
	if call := nextCall(t, sink); call.id != "req-1" || call.side != "input" {
		t.Fatalf("first call = %s/%s, want req-1/input", call.id, call.side)
	}
	select {
	case line := <-lines:
		if !strings.Contains(line, "req-1") || !strings.Contains(line, "input") {
			t.Errorf("the failed write's log line = %q, want the request id and the side", line)
		}
		if strings.Contains(line, string(secret)) {
			t.Errorf("the log line carries the body: %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no log line for the failed write")
	}
	if call := nextCall(t, sink); call.id != "req-2" || !bytes.Equal(call.body, bodyOf(11, 'y')) {
		t.Errorf("the body after the failed write = %s/%q, want req-2 through", call.id, call.body)
	}
	closeTap(t, tap)
}

func TestClosingTheTapWritesWhatIsQueued(t *testing.T) {
	t.Parallel()

	// the writer holds one body, another sits in the queue, and Close writes both
	sink := newFakeSink()
	tap := NewLedgerTap(sink, 1<<10, func(string) {})
	at := time.Now()
	if !tap.Offer("q-1", "input", bodyOf(300, 'a'), at) || !tap.Offer("q-2", "input", bodyOf(300, 'b'), at) {
		t.Fatal("offers into an empty tap were refused")
	}
	<-sink.entered
	closed := make(chan error, 1)
	go func() { closed <- tap.Close(context.Background()) }()
	if call := nextCall(t, sink); call.id != "q-1" || !bytes.Equal(call.body, bodyOf(300, 'a')) {
		t.Fatalf("first call = %s/%q, want q-1", call.id, call.body)
	}
	if call := nextCall(t, sink); call.id != "q-2" || !bytes.Equal(call.body, bodyOf(300, 'b')) {
		t.Fatalf("second call = %s/%q, want q-2 written by Close", call.id, call.body)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the queue drained")
	}
	if tap.Offer("q-3", "input", bodyOf(300, 'c'), at) {
		t.Error("an offer after Close was accepted")
	}
	closeTap(t, tap) // a second Close is safe

	// a Close whose context has already ended returns at once while the sink is held
	sink2 := newFakeSink()
	tap2 := NewLedgerTap(sink2, 1<<10, func(string) {})
	if !tap2.Offer("h-1", "input", bodyOf(300, 'a'), at) {
		t.Fatal("the offer was refused")
	}
	<-sink2.entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := tap2.Close(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Close on an ended context took %s, want it back at once", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Close on an ended context = %v, want context.Canceled", err)
	}
	if call := nextCall(t, sink2); call.id != "h-1" {
		t.Errorf("held call = %s, want h-1", call.id)
	}
}

func TestTheTapBufferKeepsAWholeAnswerOrNothing(t *testing.T) {
	t.Parallel()
	buf := newTapBuffer(100)
	buf.push([]byte("0123456789"))
	buf.push(bytes.Repeat([]byte("x"), 40))
	if body, whole := buf.bytes(); !whole || !bytes.Equal(body, append([]byte("0123456789"), bytes.Repeat([]byte("x"), 40)...)) {
		t.Errorf("chunks under the limit = %q whole=%v, want them joined and whole", body, whole)
	}
	// the limit itself still fits
	buf.push(bytes.Repeat([]byte("x"), 47))
	if body, whole := buf.bytes(); !whole || len(body) != 97 {
		t.Errorf("a body at the limit = %d bytes whole=%v, want 97 and true", len(body), whole)
	}
	// a chunk the caller changes afterwards leaves the buffer as it was
	chunk := []byte("abc")
	buf.push(chunk)
	chunk[0] = 'X'
	if body, whole := buf.bytes(); !whole || !bytes.HasSuffix(body, []byte("abc")) {
		t.Errorf("the buffer holds %q whole=%v, want the pushed chunk as it was pushed", body, whole)
	}
	// past the limit the buffer frees what it held and keeps nothing more
	buf.push(bytes.Repeat([]byte("y"), 4))
	if body, whole := buf.bytes(); whole || body != nil {
		t.Errorf("a body past the limit = %q whole=%v, want nil and false", body, whole)
	}
	buf.push([]byte("tiny"))
	if body, whole := buf.bytes(); whole || body != nil {
		t.Errorf("a push after the limit = %q whole=%v, want still nothing", body, whole)
	}
}
