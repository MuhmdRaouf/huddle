package kanbantab

// Every board open the tab runs reports under kanban:<repo name>: failing after a board whose
// store cannot open, ok after a good one, whether the open came from a read mount or a flow
// command.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// healthLog records every open result a tab hands it, from any goroutine.
type healthLog struct {
	mu    sync.Mutex
	calls []healthCall
}

// healthCall is one store's report: the component name and whether the open was good.
type healthCall struct {
	name string
	ok   bool
}

func (l *healthLog) OK(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, healthCall{name: name, ok: true})
}

func (l *healthLog) Fail(name string, _ error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, healthCall{name: name, ok: false})
}

// reported answers every call recorded so far.
func (l *healthLog) reported() []healthCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]healthCall{}, l.calls...)
}

func TestABoardThatCannotOpenReportsFailing(t *testing.T) {
	home := t.TempDir()
	broken := newRepo(t, home, "alpha")
	// a directory where the board's store would open: the board cannot bind it
	if err := os.Mkdir(filepath.Join(boardRoot(broken), "kanban.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	good := newRepo(t, home, "beta")
	reports := &healthLog{}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(broken, good), Version: "test", Now: pinnedNow, Health: reports,
	})
	brokenKey := server.KeyOf(boardRoot(broken))
	if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+brokenKey+"/api/board"); status != http.StatusNotFound {
		t.Fatalf("the broken board = %d %s, want 404", status, body)
	}
	if got := reports.reported(); len(got) != 1 || got[0] != (healthCall{name: "kanban:alpha", ok: false}) {
		t.Fatalf("reports after the broken board = %v, want kanban:alpha failing", got)
	}

	// a board whose store opens reports ok, under the same name the boards list gives
	goodKey := server.KeyOf(boardRoot(good))
	if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+goodKey+"/api/board"); status != http.StatusOK {
		t.Fatalf("the good board = %d %s, want 200", status, body)
	}
	if got := reports.reported(); len(got) != 2 || got[1] != (healthCall{name: "kanban:beta", ok: true}) {
		t.Fatalf("reports after the good board = %v, want kanban:beta ok second", got)
	}
}

func TestAFlowOpenOnABrokenStoreReportsFailing(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "HLT-1")
	if err := os.Mkdir(filepath.Join(boardRoot(repo), "kanban.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	reports := &healthLog{}
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow, Health: reports,
	})
	key := server.KeyOf(boardRoot(repo))
	if status, body := flowBody(t, tab, key, `{"command":"pull","actor":"w1","id":"HLT-1"}`); status != http.StatusInternalServerError {
		t.Fatalf("the flow on the broken store = %d %s, want 500", status, body)
	}
	if got := reports.reported(); len(got) != 1 || got[0] != (healthCall{name: "kanban:alpha", ok: false}) {
		t.Fatalf("reports after the failed flow = %v, want kanban:alpha failing", got)
	}
}
