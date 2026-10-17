package satellite

// The link's read face (KBN-SAT-READS): one read of a board the hub hosts travels to it under
// this machine's credential, and its answer streams back whole. The read client carries no
// overall timeout — the hub's event stream lasts as long as the hub holds it — and the request's
// context ends the body with it.

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
)

// TestReadCarriesOneBoardReadToTheHubUnderTheCredential pins the wire shape: a GET on the hub's
// own board path, this machine's credential and the action header on it, and the hub's status,
// headers and body answered whole for the caller to stream.
func TestReadCarriesOneBoardReadToTheHubUnderTheCredential(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen["method"], seen["path"] = r.Method, r.URL.Path
		seen["bearer"] = r.Header.Get("Authorization")
		seen["action"] = r.Header.Get("x-huddle-action")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"cards\":[]}\n"))
	}))
	t.Cleanup(srv.Close)
	link, _ := linkOn(t, srv.URL)

	response, err := link.Read(context.Background(), "board123456789", "board")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the read = %s, want the hub's 200", response.Status)
	}
	if ct := response.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("the read's content type = %q, want the hub's own", ct)
	}
	raw, _ := io.ReadAll(response.Body)
	if string(raw) != "{\"cards\":[]}\n" {
		t.Fatalf("the read's body = %q, want the hub's whole answer", raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["method"] != http.MethodGet || seen["path"] != "/api/kanban/b/board123456789/api/board" {
		t.Fatalf("the hub saw %s %s, want the GET on the board's own api path",
			seen["method"], seen["path"])
	}
	if seen["bearer"] != "Bearer hsat_test-credential-0001" || seen["action"] != seen["path"] {
		t.Fatalf("the read's credential = %q, action = %q, want the machine's on both",
			seen["bearer"], seen["action"])
	}
}

// TestReadStreamsAnAnswerTheHubHoldsOpen proves a read outlives any fixed patience: the frame
// the hub flushes arrives while the hub still holds the stream, and the body ends the moment
// the request's context does.
func TestReadStreamsAnAnswerTheHubHoldsOpen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: snapshot\ndata: {}\n\n")
		flusher.Flush()
		<-r.Context().Done() // the hub holds the stream open
	}))
	t.Cleanup(srv.Close)
	link, _ := linkOn(t, srv.URL)
	if link.readClient.Timeout != 0 {
		t.Fatalf("the read client waits %s overall, want no bound a stream cannot outlive",
			link.readClient.Timeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	response, err := link.Read(ctx, "board123456789", "events")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "snapshot") {
		t.Fatalf("the stream's first line = %q, %v, want the snapshot while the hub holds it",
			line, err)
	}
	// the frame's remaining lines arrived with the snapshot: drain to its blank
	// end, so the next read waits on the hub's body alone
	for line != "\n" {
		if line, err = reader.ReadString('\n'); err != nil {
			t.Fatalf("the stream's frame never ended: %v", err)
		}
	}
	ended := make(chan error, 1)
	go func() {
		_, err := reader.ReadString('\n')
		ended <- err
	}()
	cancel()
	select {
	case err := <-ended:
		if err == nil {
			t.Fatal("the stream answered more after its context ended")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the stream never ended with its context")
	}
}
