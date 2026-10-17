package server

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The event stream: the snapshot first, one delta per change, a reload after a
// refresh and the ping comment.

// events is one open /api/events connection: its lines arrive on a channel.
type events struct {
	cancel context.CancelFunc
	lines  chan string
}

// openEvents connects to one server's event stream.
func openEvents(t *testing.T, url string) *events {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != 200 {
		cancel()
		t.Fatalf("/api/events status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		cancel()
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
	e := &events{cancel: cancel, lines: make(chan string, 256)}
	go func() {
		defer func() { _ = response.Body.Close(); close(e.lines) }()
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			select {
			case e.lines <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(cancel)
	return e
}

// next answers the next non-empty line within the budget.
func (e *events) next(t *testing.T, budget time.Duration) string {
	t.Helper()
	select {
	case line, ok := <-e.lines:
		if !ok {
			t.Fatal("the event stream ended")
		}
		return line
	case <-time.After(budget):
		t.Fatalf("no event line within %s", budget)
		return ""
	}
}

// event reads one whole event: its name and its data payload.
func (e *events) event(t *testing.T, budget time.Duration) (string, string) {
	t.Helper()
	name, data := "", ""
	deadline := time.After(budget)
	for {
		var line string
		select {
		case got, ok := <-e.lines:
			if !ok {
				t.Fatal("the event stream ended mid-event")
			}
			line = got
		case <-deadline:
			t.Fatalf("no whole event within %s (so far %q %q)", budget, name, data)
		}
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if name != "" || data != "" {
				return name, data
			}
		}
	}
}

func TestEvents(t *testing.T) {
	t.Run("the snapshot comes first, then a delta per change", func(t *testing.T) {
		app, ts, b := newApp(t)
		writeTicket(t, b, "backlog", "SSE-1")
		if _, changed := app.watcher.Scan(context.Background()); !changed {
			t.Fatal("the first scan changed nothing")
		}
		go app.watcher.Run(context.Background(), app.publish)
		e := openEvents(t, ts.URL)

		name, data := e.event(t, 3*time.Second)
		if name != "snapshot" || !strings.Contains(data, `"rev":1`) || !strings.Contains(data, "SSE-1") {
			t.Fatalf("first event = %s %s, want the snapshot", name, data)
		}

		writeTicket(t, b, "ready", "SSE-2")
		name, data = e.event(t, 3*time.Second)
		if name != "delta" || !strings.Contains(data, "SSE-2") || !strings.Contains(data, `"upsert"`) {
			t.Fatalf("second event = %s %s, want the delta for SSE-2", name, data)
		}
	})

	t.Run("a moved card arrives as one upsert, never remove plus upsert", func(t *testing.T) {
		app, ts, b := newApp(t)
		writeTicket(t, b, "backlog", "MOV-1")
		if _, changed := app.watcher.Scan(context.Background()); !changed {
			t.Fatal("the first scan changed nothing")
		}
		go app.watcher.Run(context.Background(), app.publish)
		e := openEvents(t, ts.URL)
		if name, _ := e.event(t, 3*time.Second); name != "snapshot" {
			t.Fatal("the snapshot did not come first")
		}

		from := b.Root + "/backlog/MOV-1.md"
		to := b.Root + "/ready/MOV-1.md"
		if err := renameFile(from, to); err != nil {
			t.Fatal(err)
		}
		name, data := e.event(t, 3*time.Second)
		if name != "delta" {
			t.Fatalf("event = %s, want delta", name)
		}
		if strings.Contains(data, `"remove"`) && strings.Contains(data, "MOV-1") &&
			strings.Contains(data, `"remove":["MOV-1"]`) {
			t.Errorf("a move arrived as a remove: %s", data)
		}
		if !strings.Contains(data, "MOV-1") {
			t.Errorf("the delta names nothing of the move: %s", data)
		}
	})

	t.Run("a refresh sends a reload with the rev", func(t *testing.T) {
		app, ts, b := newApp(t)
		writeTicket(t, b, "backlog", "REL-1")
		if _, changed := app.watcher.Scan(context.Background()); !changed {
			t.Fatal("the first scan changed nothing")
		}
		go app.watcher.Run(context.Background(), app.publish)
		e := openEvents(t, ts.URL)
		if name, _ := e.event(t, 3*time.Second); name != "snapshot" {
			t.Fatal("the snapshot did not come first")
		}

		response, err := http.Post(ts.URL+"/api/refresh", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("refresh status = %d", response.StatusCode)
		}
		name, data := e.event(t, 3*time.Second)
		if name != "reload" || !strings.Contains(data, `"rev":`) {
			t.Fatalf("event = %s %s, want a reload with the rev", name, data)
		}
	})

	t.Run("a ping comment keeps the line warm", func(t *testing.T) {
		b := newBoard(t)
		app := NewApp(Options{Board: b, Home: t.TempDir(), Version: "test", EveryMs: 25, PingMs: 100})
		ts := listenApp(t, app)
		e := openEvents(t, ts.URL)
		if name, _ := e.event(t, 3*time.Second); name != "snapshot" {
			t.Fatal("the snapshot did not come first")
		}
		line := e.next(t, 3*time.Second)
		if !strings.HasPrefix(line, ":") {
			t.Fatalf("line = %q, want a comment ping", line)
		}
	})
}
