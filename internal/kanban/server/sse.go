package server

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// The event stream: every open page holds a queue of frames fed by the
// watcher; a page too slow to keep up is closed rather than blocked or left
// behind — it reconnects and reads a fresh snapshot.

// sseFrame is one stream event: its name and its data payload.
type sseFrame struct {
	name string
	data string
}

// frame renders one event frame.
func frame(name, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, data)
}

// sseClient is one open page.
type sseClient struct {
	frames chan sseFrame
	dead   chan struct{}
	once   sync.Once
}

// newClient answers one open page with a 64-frame queue.
func newClient() *sseClient {
	return &sseClient{frames: make(chan sseFrame, 64), dead: make(chan struct{})}
}

// drop ends the page's stream.
func (c *sseClient) drop() { c.once.Do(func() { close(c.dead) }) }

// enqueue queues one frame; a full queue drops the page instead of blocking
// the publisher.
func (c *sseClient) enqueue(f sseFrame) {
	select {
	case <-c.dead:
		return
	default:
	}
	select {
	case c.frames <- f:
	default:
		c.drop()
	}
}

// hub is every open page.
type hub struct {
	mu    sync.Mutex
	conns map[*sseClient]struct{}
}

// subscribe adds one page.
func (h *hub) subscribe(c *sseClient) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()
}

// unsubscribe removes one page and ends its stream.
func (h *hub) unsubscribe(c *sseClient) {
	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	c.drop()
}

// snapshot answers the open pages under one lock hold.
func (h *hub) clients() []*sseClient {
	h.mu.Lock()
	conns := make([]*sseClient, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	return conns
}

// publish queues one frame for every open page.
func (h *hub) publish(f sseFrame) {
	for _, c := range h.clients() {
		c.enqueue(f)
	}
}

// closeAll ends every open page: the shutdown path, after which no frame is
// queued.
func (h *hub) closeAll() {
	for _, c := range h.clients() {
		c.drop()
	}
}

// serveEvents answers one /api/events request: the snapshot first, then one
// delta per change, a reload after each refresh, and a ping event on the
// keepalive period. The ping is a named event with data rather than a comment,
// because EventSource never surfaces a comment: the page counts pings to tell a
// quiet board from a dead socket.
func (a *App) serveEvents(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	client := newClient()
	a.hub.subscribe(client)
	defer a.hub.unsubscribe(client)

	write := func(text string) bool {
		select {
		case <-r.Context().Done():
			return false
		default:
		}
		if _, err := io.WriteString(w, text); err != nil {
			return false
		}
		_ = controller.Flush()
		return true
	}

	// the subscription goes in before the snapshot is read, so a change that
	// lands while it is read waits in the queue and goes out after it, in
	// order; the deltas are idempotent upserts, so a repeat costs nothing
	snapshot := a.watcher.Snapshot()
	if !write(frame("snapshot", string(encodeCompact(snapshot)))) {
		return
	}

	ping := time.NewTicker(a.ping)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-client.dead:
			return
		case f := <-client.frames:
			if !write(frame(f.name, f.data)) {
				return
			}
		case <-ping.C:
			if !write(frame("ping", "{}")) {
				return
			}
		}
	}
}
