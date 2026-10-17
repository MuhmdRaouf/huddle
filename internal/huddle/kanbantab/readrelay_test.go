package kanbantab

// The tab's read relay (KBN-SAT-READS): a GET under one board's api/ answers from the huddle
// that hosts the board — status, content type and body whole, flushed as the hub sends them so
// the event stream reaches the page live — while anything the relay does not take (a non-GET, a
// control path, a board it answers nothing for) serves from the local app as before.

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// readCall is one read the tab handed its ReadRelay callback: the board's key
// and the path under its api/.
type readCall struct {
	key, path string
}

// readLog records every ReadRelay call and answers what the test pinned; a nil
// answer takes nothing.
type readLog struct {
	mu     sync.Mutex
	calls  []readCall
	answer func(key, path string) (*http.Response, bool)
}

func (l *readLog) relay(_ context.Context, key, path string) (*http.Response, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, readCall{key: key, path: path})
	if l.answer == nil {
		return nil, false
	}
	return l.answer(key, path)
}

func (l *readLog) heard() []readCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]readCall{}, l.calls...)
}

// relayedAnswer builds one hub answer for the relay to carry: its status, its
// headers and a body that ends on its own.
func relayedAnswer(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type":  {contentType},
			"Cache-Control": {"no-store"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// TestARelayedReadAnswersTheHubsAnswerThrough proves the pass-through: the hub's status, answer
// headers and body reach the page, and the callback saw the key and the path under its api/.
func TestARelayedReadAnswersTheHubsAnswerThrough(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "RD-1")
	key := server.KeyOf(boardRoot(repo))
	reads := &readLog{}
	reads.answer = func(_, path string) (*http.Response, bool) {
		if path == "board" {
			return relayedAnswer(http.StatusOK, "application/json; charset=utf-8",
				"{\"cards\":[{\"id\":\"RD-1\",\"column\":\"in-progress\"}]}\n"), true
		}
		return nil, false
	}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, ReadRelay: reads.relay,
	})
	request := httptest.NewRequest(http.MethodGet, Prefix+"/b/"+key+"/api/board", nil)
	recorder := httptest.NewRecorder()
	tab.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"column":"in-progress"`) {
		t.Fatalf("the relayed board = %d %s, want the hub's answer through",
			recorder.Code, recorder.Body.String())
	}
	if ct, cc := recorder.Header().Get("Content-Type"), recorder.Header().Get("Cache-Control"); ct != "application/json; charset=utf-8" || cc != "no-store" {
		t.Fatalf("the relayed headers = %q and %q, want the hub's content type and no-store", ct, cc)
	}
	calls := reads.heard()
	if len(calls) != 1 || calls[0].key != key || calls[0].path != "board" {
		t.Fatalf("the read relay heard %+v, want the one board read", calls)
	}
}

// TestAReadTheRelayDoesNotTakeServesTheLocalBoard proves the fallback: a callback that answers
// nothing leaves the read on the local app, its board and its tickets whole.
func TestAReadTheRelayDoesNotTakeServesTheLocalBoard(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "RD-2")
	key := server.KeyOf(boardRoot(repo))
	reads := &readLog{} // no answer pinned: nothing is relayed
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, ReadRelay: reads.relay,
	})
	status, out := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board")
	if status != http.StatusOK || !strings.Contains(out, `"column":"ready"`) {
		t.Fatalf("the local board = %d %s, want the card in ready", status, out)
	}
	status, out = answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/tickets/RD-2")
	if status != http.StatusOK || !strings.Contains(out, "Body text.") {
		t.Fatalf("the local ticket = %d %s, want the ticket whole", status, out)
	}
	calls := reads.heard()
	if len(calls) != 2 || calls[0].path != "board" || calls[1].path != "tickets/RD-2" {
		t.Fatalf("the read relay heard %+v, want the board and the ticket", calls)
	}
}

// TestOnlyGetReadsUnderTheBoardsApiReachTheRelay pins the relay's gate: a non-GET, the board's
// control routes and a path outside its api/ never leave the machine — each answers the app's
// own verdict, and the callback hears nothing.
func TestOnlyGetReadsUnderTheBoardsApiReachTheRelay(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "RD-3")
	key := server.KeyOf(boardRoot(repo))
	reads := &readLog{}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, ReadRelay: reads.relay,
	})
	for _, one := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/api/board", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/events", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/refresh", http.StatusNotFound},
		{http.MethodGet, "/api/shutdown", http.StatusNotFound},
		{http.MethodGet, "/nowhere", http.StatusNotFound},
	} {
		status, out := answer(t, tab, one.method, Prefix+"/b/"+key+one.path)
		if status != one.want {
			t.Fatalf("%s %s = %d %s, want %d", one.method, one.path, status, out, one.want)
		}
	}
	if calls := reads.heard(); len(calls) != 0 {
		t.Fatalf("the read relay heard %+v, want nothing: only GET reads under api/ leave", calls)
	}
}

// TestARelayedStreamReachesThePageWhileTheHubHoldsIt proves the streaming half: a frame the
// hub sends crosses to the page while the hub still holds the stream — the flush after every
// write carries it, not the stream's end — and the hub's body ending ends the page's stream.
func TestARelayedStreamReachesThePageWhileTheHubHoldsIt(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "RD-4")
	key := server.KeyOf(boardRoot(repo))
	fromHub, hubWrites := io.Pipe()
	// every way out of the test ends the hub's body, so a failure never leaves
	// the relay blocked reading it
	t.Cleanup(func() { _ = hubWrites.Close() })
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow,
		ReadRelay: func(_ context.Context, _ string, _ string) (*http.Response, bool) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}},
				Body:       fromHub,
			}, true
		},
	})
	srv := httptest.NewServer(tab)
	t.Cleanup(srv.Close)

	frames := make(chan string, 4)
	read := make(chan error, 1)
	wrote := make(chan error, 1)
	// the hub's frame crosses while it holds the stream: the page's headers ride
	// the same flushed write, so the client and the frame travel together
	go func() {
		_, err := hubWrites.Write([]byte("event: snapshot\n\n"))
		wrote <- err
	}()
	response, err := (&http.Client{}).Get(srv.URL + Prefix + "/b/" + key + "/api/events")
	if err != nil {
		t.Fatalf("open the relayed stream: %v", err)
	}
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			frames <- scanner.Text()
		}
		read <- scanner.Err()
		_ = response.Body.Close()
	}()

	select {
	case line := <-frames:
		if !strings.Contains(line, "snapshot") {
			t.Fatalf("the frame = %q, want the snapshot", line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the frame never reached the page while the hub held the stream")
	}
	if err := <-wrote; err != nil {
		t.Fatalf("the hub's frame write: %v", err)
	}

	_ = hubWrites.Close()
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		t.Fatal("the relayed stream never ended with the hub's body")
	}
}

// ctxBody answers one frame at once, then nothing until its context ends, the
// way a hub's event stream holds open after its snapshot and ends when the
// request that carries it is canceled.
type ctxBody struct {
	ctx     context.Context
	started bool
}

func (b *ctxBody) Read(p []byte) (int, error) {
	if !b.started {
		b.started = true
		return copy(p, "event: snapshot\n\n"), nil
	}
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

// TestTheRelayedStreamEndsWhenItsClientGoes proves the other end: the client leaving ends the
// relay with its own request, so a page that closes its stream never leaves one running.
func TestTheRelayedStreamEndsWhenItsClientGoes(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "RD-5")
	key := server.KeyOf(boardRoot(repo))
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow,
		ReadRelay: func(ctx context.Context, _ string, _ string) (*http.Response, bool) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}},
				Body:       io.NopCloser(&ctxBody{ctx: ctx}),
			}, true
		},
	})
	served := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tab.ServeHTTP(w, r)
		close(served)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+Prefix+"/b/"+key+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		t.Fatalf("open the relayed stream: %v", err)
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("the relay kept running after its client went")
	}
}
