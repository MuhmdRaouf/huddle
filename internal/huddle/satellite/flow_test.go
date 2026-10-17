package satellite

// The link's kanban face: one flow command for a board the hub hosts rides the
// machine's own credential and answers the hub's verdict whole, and the board
// list a relay decides against is held for a short while — an unreachable hub
// leaves the last good set standing with its error. Against a fake hub on a
// loopback port, never a real one.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFlowCarriesOneCommandToTheHubsBoard(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotBody, gotAction string
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotPath, gotBody, gotAction = r.URL.Path, string(raw), r.Header.Get("x-huddle-action")
		mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"FLW-1 is held by w2"}`))
	}))
	t.Cleanup(hub.Close)
	link, _ := linkOn(t, hub.URL)

	status, answer, err := link.Flow(context.Background(), "abc123def456",
		[]byte(`{"command":"pull","actor":"w1","id":"FLW-1"}`))
	if err != nil {
		t.Fatalf("flow: %v", err)
	}
	if status != http.StatusConflict || !strings.Contains(string(answer), "held by w2") {
		t.Fatalf("the flow answer = %d %s, want the hub's verdict whole", status, answer)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/api/kanban/b/abc123def456/flow" || gotAction != gotPath {
		t.Fatalf("the flow request went to %q (action %q), want the board's flow route", gotPath, gotAction)
	}
	if gotBody != `{"command":"pull","actor":"w1","id":"FLW-1"}` {
		t.Fatalf("the flow body = %q, want the command's raw bytes", gotBody)
	}
}

func TestFlowAtAnUnreachableHubAnswersItsError(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	link, _ := linkOn(t, deadURL)
	if _, _, err := link.Flow(context.Background(), "abc123def456",
		[]byte(`{"command":"pull","actor":"w1"}`)); err == nil {
		t.Fatal("a flow at a dead hub carried no error")
	}
}

func TestHubBoardsHoldsTheHubsSetAndReReadsItAfterAWhile(t *testing.T) {
	var mu sync.Mutex
	listed := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		listed++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(struct {
			Boards []struct {
				Key string `json:"key"`
			} `json:"boards"`
		}{Boards: []struct {
			Key string `json:"key"`
		}{{Key: "held12345678"}, {Key: "gone87654321"}}})
	}))
	t.Cleanup(hub.Close)
	link, _ := linkOn(t, hub.URL)
	clock := time.Date(2026, 10, 16, 12, 0, 30, 0, time.UTC)
	link.now = func() time.Time { return clock }

	set, err := link.HubBoards(context.Background())
	if err != nil {
		t.Fatalf("hub boards: %v", err)
	}
	if !set["held12345678"] || !set["gone87654321"] || len(set) != 2 {
		t.Fatalf("the hub's set = %v, want both keys", set)
	}
	// the copy stands: a second ask inside the hold never leaves the machine
	if set, err = link.HubBoards(context.Background()); err != nil || len(set) != 2 {
		t.Fatalf("the held set = %v (%v), want the same two keys", set, err)
	}
	mu.Lock()
	hits := listed
	mu.Unlock()
	if hits != 1 {
		t.Fatalf("the hub listed its boards %d times, want the one read", hits)
	}
	// past the hold the set is read again
	clock = clock.Add(31 * time.Second)
	if _, err = link.HubBoards(context.Background()); err != nil {
		t.Fatalf("re-read the hub's boards: %v", err)
	}
	mu.Lock()
	hits = listed
	mu.Unlock()
	if hits != 2 {
		t.Fatalf("the hub listed its boards %d times, want a fresh read past the hold", hits)
	}
}

func TestHubBoardsAtAnUnreachableHubAnswersTheLastGoodSet(t *testing.T) {
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"boards":[{"key":"held12345678"}]}`))
	}))
	link, _ := linkOn(t, hub.URL)
	clock := time.Date(2026, 10, 16, 12, 0, 30, 0, time.UTC)
	link.now = func() time.Time { return clock }
	set, err := link.HubBoards(context.Background())
	if err != nil || !set["held12345678"] {
		t.Fatalf("the hub's set = %v (%v), want the one key", set, err)
	}
	hub.Close()
	// past the hold the set is read again, and only the read fails
	clock = clock.Add(31 * time.Second)
	set, err = link.HubBoards(context.Background())
	if err == nil {
		t.Fatal("a read at a dead hub carried no error")
	}
	if !set["held12345678"] {
		t.Fatalf("the set at a dead hub = %v, want the last good set standing", set)
	}
}
