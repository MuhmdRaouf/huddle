// The hub's browser streams ping with a data frame: EventSource never surfaces an SSE comment,
// so a ping the page can count is the only proof a quiet stream is still alive. The interval is
// HUDDLE_SSE_PING_MS here, so the test sees a ping in milliseconds rather than 15 s.
package server

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// openStreamData opens one of the hub's SSE routes with the owner's token and answers every
// data line it carries, oldest first.
func openStreamData(t *testing.T, srv *childServer, path string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("x-huddle-token", srv.token)
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", path, response.StatusCode)
	}
	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
				lines <- strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return lines
}

// awaitPing fails the test unless a {"type":"ping"} data frame arrives within 5 s.
func awaitPing(t *testing.T, lines <-chan string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatal("the stream closed before a ping")
			}
			if line == `{"type":"ping"}` {
				return
			}
		case <-deadline:
			t.Fatal(`no {"type":"ping"} data frame within 5 s`)
		}
	}
}

func TestEveryBrowserStreamPingsWithADataFrame(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_SSE_PING_MS": "50"})

	t.Run("the registry stream, which carries the sessions and the needs-you items", func(t *testing.T) {
		awaitPing(t, openStreamData(t, srv, "/api/registry/live"))
	})

	t.Run("radar's stream under /api/radar", func(t *testing.T) {
		awaitPing(t, openStreamData(t, srv, "/api/radar/api/stream"))
	})
}
