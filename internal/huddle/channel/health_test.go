package channel

// The hub hands each store's open result to its Health: a channel's open under channel:<name>,
// the server-wide knowledge's under knowledge.db. A missing channel opened with create off
// opens no store, so it reports nothing.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// healthLog records every open result a hub hands it, from any goroutine.
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

func TestTheHubReportsEachStoreOpen(t *testing.T) {
	t.Run("a channel's first open reports the channel and the knowledge store ok", func(t *testing.T) {
		reports := &healthLog{}
		h := NewHub(t.TempDir(), true)
		h.Health = reports
		t.Cleanup(func() { _ = h.Close(bg) })
		if _, err := h.Get(bg, "a"); err != nil {
			t.Fatal(err)
		}
		want := []healthCall{{name: "channel:a", ok: true}, {name: "knowledge.db", ok: true}}
		if got := reports.reported(); len(got) != len(want) {
			t.Fatalf("reports = %v, want %v", got, want)
		} else {
			for i, call := range got {
				if call != want[i] {
					t.Errorf("report %d = %+v, want %+v", i, call, want[i])
				}
			}
		}
	})

	t.Run("a channel file that cannot open reports the channel failing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "channels", "b.db"), 0o700); err != nil {
			t.Fatal(err)
		}
		reports := &healthLog{}
		h := NewHub(dir, true)
		h.Health = reports
		t.Cleanup(func() { _ = h.Close(bg) })
		if _, err := h.Get(bg, "b"); err == nil {
			t.Fatal("get over a directory answered no error")
		}
		want := []healthCall{{name: "channel:b", ok: false}}
		if got := reports.reported(); len(got) != len(want) {
			t.Fatalf("reports = %v, want %v", got, want)
		} else {
			for i, call := range got {
				if call != want[i] {
					t.Errorf("report %d = %+v, want %+v", i, call, want[i])
				}
			}
		}
	})

	t.Run("a missing channel with create off opens nothing and reports nothing", func(t *testing.T) {
		reports := &healthLog{}
		h := NewHub(t.TempDir(), false)
		h.Health = reports
		t.Cleanup(func() { _ = h.Close(bg) })
		if _, err := h.Get(bg, "ghost"); err == nil || errStatus(err) != 404 {
			t.Fatalf("missing channel error = %v, want 404", err)
		}
		if got := reports.reported(); len(got) != 0 {
			t.Errorf("reports = %v, want none: a missing channel opens no store", got)
		}
	})
}
