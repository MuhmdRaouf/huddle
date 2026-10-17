package kanbantab

// The flow route's relay: a command for a board another huddle hosts is handed
// to it whole before the local board is read, and its verdict passes through
// unchanged — status, body and all. Not relayed (the callback answers false, or
// no Relay is set) leaves the command on the local board.

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// relayCall is one command the tab handed its Relay callback.
type relayCall struct {
	key  string
	body []byte
}

// relayLog records every Relay call and answers what the test pinned; nil
// answer takes nothing.
type relayLog struct {
	mu     sync.Mutex
	calls  []relayCall
	answer func(key string, body []byte) (int, []byte, bool)
}

func (l *relayLog) record(_ context.Context, key string, body []byte) (int, []byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, relayCall{key: key, body: body})
	if l.answer == nil {
		return 0, nil, false
	}
	return l.answer(key, body)
}

func (l *relayLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.calls)
}

func TestARelayedCommandAnswersTheHubsVerdictThrough(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "REL-1")
	key := server.KeyOf(boardRoot(repo))
	relays := &relayLog{}
	relays.answer = func(string, []byte) (int, []byte, bool) {
		return http.StatusConflict, []byte("{\"error\":\"REL-1 is held by w9\"}\n"), true
	}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Relay: relays.record,
	})
	status, out := flowBody(t, tab, key, `{"command":"pull","actor":"w1","id":"REL-1"}`)
	if status != http.StatusConflict || !strings.Contains(out, "held by w9") {
		t.Fatalf("the relayed pull = %d %s, want the hub's 409 through unchanged", status, out)
	}
	// the callback saw the key and the raw body, and the local board never moved
	relays.mu.Lock()
	calls := append([]relayCall{}, relays.calls...)
	relays.mu.Unlock()
	if len(calls) != 1 || calls[0].key != key ||
		string(calls[0].body) != `{"command":"pull","actor":"w1","id":"REL-1"}` {
		t.Fatalf("the relay calls = %+v, want one with the key and the raw body", calls)
	}
	if col := columnOf(t, tab, key, "REL-1"); col != "ready" {
		t.Fatalf("REL-1 sits in %s, want ready: a relayed write never touches the local board", col)
	}
}

func TestACommandTheRelayDoesNotTakeRunsLocally(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "LOC-1")
	key := server.KeyOf(boardRoot(repo))
	relays := &relayLog{} // no answer pinned: nothing is relayed
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Relay: relays.record,
	})
	flow(t, tab, key, `{"command":"pull","actor":"w1","id":"LOC-1"}`,
		filepath.Join("in-progress", "LOC-1.md"))
	if col := columnOf(t, tab, key, "LOC-1"); col != "in-progress" {
		t.Fatalf("LOC-1 sits in %s after the pull, want in-progress", col)
	}
	if got := relays.count(); got != 1 {
		t.Fatalf("the relay heard %d calls, want the one command", got)
	}
}

func TestARelayedAnswerCarriesItsOwnStatusWhateverItIs(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "REL-2")
	key := server.KeyOf(boardRoot(repo))
	relays := &relayLog{}
	relays.answer = func(string, []byte) (int, []byte, bool) {
		return http.StatusServiceUnavailable, []byte("{\"error\":\"the hub is unreachable\"}\n"), true
	}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Relay: relays.record,
	})
	status, out := flowBody(t, tab, key, `{"command":"pull","actor":"w1","id":"REL-2"}`)
	if status != http.StatusServiceUnavailable || !strings.Contains(out, "the hub is unreachable") {
		t.Fatalf("the relayed answer = %d %s, want the 503 through unchanged", status, out)
	}
}
