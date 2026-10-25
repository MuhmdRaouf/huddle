// The HTTP server. Loopback only, and only for us: a Host header naming anything but this
// exact port on 127.0.0.1/localhost is a DNS-rebinding probe and gets 403 — no CORS headers
// exist to relax. GET/HEAD for everything; the dashboard's own four writes (budgets,
// settings, dismissing an alert, clearing history) also take PUT/POST, but only as
// application/json (so another site cannot send one without a preflight we never answer),
// only from our own origin when the browser names one, and only up to 64 KiB.
// The pages live in the huddle hub's Radar tab; any other path answers a 404 naming that,
// and /api/stream is SSE: one snapshot, then incremental updates and a heartbeat until the
// client goes away.
package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// writePaths maps each write route to the one method it takes.
var writePaths = map[string]string{
	"/api/budgets":        "PUT",
	"/api/settings":       "PUT",
	"/api/alerts/dismiss": "POST",
	"/api/history/clear":  "POST",
	"/api/quota":          "POST",
}

// maxBody bounds one write's payload.
const maxBody = 64 * 1024

// pagesAnswer is the 404 every non-API path gets: radar serves its API only, the pages are
// the huddle hub's Radar tab.
const pagesAnswer = "radar's pages are the huddle hub's Radar tab"

// How long a stream may hold a full-state message back to fold in further changes: the
// record deltas (a request, a tool call, an event) still go out at once, but the sessions
// message rebuilds every view, so a catch-up feeding hundreds of single-record changes must
// cost one rebuild here, not one per line. Short enough that the rail reads as live.
const sessionsEveryMs = 500

// How often the ingest backlog word is checked for a flip to tell the open streams about:
// the read is one boolean, and 500 ms keeps the footer honest without a timer per client.
const ingestEveryMs = 500

// defaultHeartbeatMs is the SSE heartbeat a caller that names none gets.
const defaultHeartbeatMs = 15_000

// ServerInsights is what the HTTP server serves from insights: every read the API and
// /metrics answer, plus the writes the dashboard's own forms send.
type ServerInsights interface {
	InsightsView
	WriteInsights
	Metrics(version string) string
}

// Watcher is the ingest watcher's backlog word, for the snapshot and ingest messages a
// stream carries.
type Watcher interface {
	CatchingUp() bool
}

// HttpOptions shapes one HTTP server. Tests pin Port before listen; production leaves it
// zero and calls SetPort once the caller bound its own draw.
type HttpOptions struct {
	Store     *store.Store
	Version   string
	StartedAt int64
	Port      int
	// HeartbeatMs is the SSE heartbeat; zero takes the default.
	HeartbeatMs int64
	// Insights is the derived views; nil serves an empty /metrics and 503 on writes.
	Insights ServerInsights
	// Quotas holds the statusline's plan-limits snapshots; nil answers 503 on /api/quota.
	Quotas *Quotas
	// History is the opened SQLite history; nil when it failed to open.
	History *history.History
	// SelfCheck answers /api/selfcheck; nil answers 503 there.
	SelfCheck *SelfCheck
	// Watcher names the ingest backlog word; nil leaves the ingest messages out.
	Watcher Watcher
	// Otel holds Claude Code's OpenTelemetry export; nil answers 503 on its intake and read.
	Otel *store.OtelRows
	// Day answers the day card's facts from the embedding hub; nil answers 503 on /api/day.
	Day DaySource
	// SnapshotSent runs once the snapshot frame is written and before the deltas start; nil
	// in production. The store is subscribed by then, so a change landed here must still
	// reach this client.
	SnapshotSent func()
	// HostAllowed, when set, is the whole host check: the embedding server's own, naming the
	// listeners it serves this server's routes on beyond loopback. Nil keeps radar's
	// loopback-only rule.
	HostAllowed func(host string) bool
	// OriginAllowed, when set, is the whole Origin check on writes: the embedding server's
	// own, naming the origins its write guard accepts beyond loopback. Nil keeps radar's
	// loopback-only rule.
	OriginAllowed func(origin string) bool
}

// FlightTracker counts the requests a shutdown must let finish. An SSE response is not one
// of them (it has answered; its events run forever), so those are closed by the shutdown
// instead of awaited.
type FlightTracker struct {
	mu       sync.Mutex
	inFlight int
	waiting  []chan struct{}
}

// CreateFlightTracker builds an empty tracker.
func CreateFlightTracker() *FlightTracker { return &FlightTracker{} }

// Track counts one request until the returned release runs.
func (f *FlightTracker) Track() func() {
	f.mu.Lock()
	f.inFlight++
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.inFlight--
		if f.inFlight > 0 {
			f.mu.Unlock()
			return
		}
		waiting := f.waiting
		f.waiting = nil
		f.mu.Unlock()
		for _, wake := range waiting {
			close(wake)
		}
	}
}

// WhenIdle returns once nothing is counted, or after timeoutMs — whatever comes first.
func (f *FlightTracker) WhenIdle(ctx context.Context, timeoutMs int64) {
	f.mu.Lock()
	if f.inFlight == 0 {
		f.mu.Unlock()
		return
	}
	idle := make(chan struct{})
	f.waiting = append(f.waiting, idle)
	f.mu.Unlock()
	timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-idle:
	case <-timer.C:
	case <-ctx.Done():
	}
}

// streamConn is one open SSE client: the frames queued for it and how to end it.
type streamConn struct {
	frames chan string
	cancel context.CancelFunc
}

// HttpServer is the dashboard's loopback server: unbound until the caller serves a
// listener on it, then served through Server.
type HttpServer struct {
	Server  *http.Server
	options HttpOptions

	port     func() int64
	hostOK   func(string) bool
	originOK func(string) bool
	flights  *FlightTracker

	streamsMu sync.Mutex
	streams   map[*streamConn]struct{}

	ingestMu   sync.Mutex
	ingestWord *bool
	closeOnce  sync.Once
	closed     chan struct{}
}

// CreateHttpServer builds the server. The port the handler host-checks against is read
// live, so requests that race in before SetPort are still checked against the eventual
// value.
func CreateHttpServer(options HttpOptions) *HttpServer {
	port := int64(options.Port)
	h := &HttpServer{
		Server:   &http.Server{},
		options:  options,
		port:     func() int64 { return port },
		hostOK:   options.HostAllowed,
		originOK: options.OriginAllowed,
		flights:  CreateFlightTracker(),
		streams:  map[*streamConn]struct{}{},
		closed:   make(chan struct{}),
	}
	if options.Watcher != nil {
		go h.watchIngest()
	}
	h.Server.Handler = http.HandlerFunc(h.handle)
	return h
}

// SetPort names the port the caller bound.
func (h *HttpServer) SetPort(port int) {
	h.port = func() int64 { return int64(port) }
}

// WhenIdle returns once no counted request is in flight, or after timeoutMs.
func (h *HttpServer) WhenIdle(ctx context.Context, timeoutMs int64) {
	h.flights.WhenIdle(ctx, timeoutMs)
}

// CloseStreams ends every live SSE stream and cuts the ingest watch.
func (h *HttpServer) CloseStreams() {
	h.closeOnce.Do(func() {
		close(h.closed)
		h.streamsMu.Lock()
		conns := make([]*streamConn, 0, len(h.streams))
		for conn := range h.streams {
			conns = append(conns, conn)
		}
		h.streamsMu.Unlock()
		for _, conn := range conns {
			conn.cancel()
		}
	})
}

// hostAllowed answers whether one Host header names a listener this server answers. The
// embedding server's predicate, when one was given, is the whole check; nil keeps radar's
// loopback-only rule, read against the port live so a request racing SetPort is still checked
// against the eventual value.
func (h *HttpServer) hostAllowed(host string) bool {
	if h.hostOK != nil {
		return h.hostOK(host)
	}
	return HostAllowed(host, int(h.port()))
}

// originAllowed answers whether one Origin header names a writer this server takes. The
// embedding server's predicate, when one was given, is the whole check; nil keeps radar's
// loopback-only rule, read against the port live so a request racing SetPort is still checked
// against the eventual value.
func (h *HttpServer) originAllowed(origin string) bool {
	if h.originOK != nil {
		return h.originOK(origin)
	}
	ours := fmt.Sprintf("http://127.0.0.1:%d", h.port())
	local := fmt.Sprintf("http://localhost:%d", h.port())
	return origin == ours || origin == local
}

// handle routes one request.
func (h *HttpServer) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	if path != "/api/stream" {
		// streams are closed on shutdown, not awaited
		release := h.flights.Track()
		defer release()
	}
	if !h.hostAllowed(r.Host) {
		sendJSON(w, 403, errorBody{Error: "forbidden host"})
		return
	}
	if path == OTLPMetricsPath {
		h.otlpRoute(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.writeRoute(w, r, path)
		return
	}
	h.route(w, r, path)
}

// writeRoute takes the dashboard's own writes: our JSON only, our origin only, 64 KiB at
// most.
func (h *HttpServer) writeRoute(w http.ResponseWriter, r *http.Request, path string) {
	if writePaths[path] != r.Method {
		sendJSON(w, 405, errorBody{Error: "method not allowed"})
		return
	}
	if !writeAllowed(r.Header, h.originAllowed) {
		sendJSON(w, 403, errorBody{Error: "writes take application/json from the dashboard only"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	_ = r.Body.Close()
	if err != nil {
		sendJSON(w, 500, errorBody{Error: "could not read the body: " + err.Error()})
		return
	}
	if len(body) > maxBody {
		// past the limit: the body is drained, never kept
		sendJSON(w, 413, errorBody{Error: "body too large"})
		return
	}
	parsed, err := jsfmt.DecodeValue(body)
	if err != nil {
		sendJSON(w, 400, errorBody{Error: "body is not JSON"})
		return
	}
	answer, err := HandleWrite(r.Context(), h.options.Insights, h.options.Quotas, r.Method, path, parsed, h.options.History)
	if err != nil {
		sendJSON(w, 500, errorBody{Error: "could not save: " + err.Error()})
		return
	}
	sendJSON(w, answer.Status, answer.Body)
}

// route answers one read.
func (h *HttpServer) route(w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case path == "/api/stream":
		h.streamRoute(w, r)
	case path == OtelPath:
		h.otelRead(w)
	case strings.HasPrefix(path, "/api/"):
		h.apiRoute(w, r, path)
	case path == "/metrics":
		h.metricsRoute(w, r)
	default:
		sendJSON(w, 404, errorBody{Error: pagesAnswer})
	}
}

// apiRoute answers one /api read through the pure router.
func (h *HttpServer) apiRoute(w http.ResponseWriter, r *http.Request, path string) {
	answer, err := h.answerApi(r, path)
	if err != nil {
		sendJSON(w, 500, errorBody{Error: "could not answer: " + err.Error()})
		return
	}
	if r.Method == http.MethodHead && answer.Status == 200 {
		send(w, 200, "", map[string]string{
			"Content-Type":  "application/json; charset=utf-8",
			"Cache-Control": "no-store",
		})
		return
	}
	sendJSON(w, answer.Status, answer.Body)
}

// answerApi is one /api read's answer: the self-check from its own cache, the day card from
// the embedding hub, the rest from the pure router.
func (h *HttpServer) answerApi(r *http.Request, path string) (ApiResponse, error) {
	if path == SelfCheckPath {
		if h.options.SelfCheck == nil {
			return errorOf(503, "self-check unavailable"), nil
		}
		return h.options.SelfCheck.Answer(r.Context())
	}
	if path == DayPath {
		return dayRoute(r.Context(), h.options.Day, time.Now())
	}
	return HandleApi(
		h.options.Store,
		AppInfo{Version: h.options.Version, StartedAt: h.options.StartedAt, Port: int(h.port())},
		ApiRequest{Path: path, Query: r.URL.Query()},
		h.options.Insights,
		h.options.Quotas,
		h.options.History,
		time.Now,
	)
}

// metricsRoute answers the Prometheus exposition.
func (h *HttpServer) metricsRoute(w http.ResponseWriter, r *http.Request) {
	body := ""
	if h.options.Insights != nil {
		body = h.options.Insights.Metrics(h.options.Version)
	}
	headers := map[string]string{
		"Content-Type":  "text/plain; version=0.0.4; charset=utf-8",
		"Cache-Control": "no-store",
	}
	if r.Method == http.MethodHead {
		send(w, 200, "", headers)
		return
	}
	send(w, 200, body, headers)
}

// streamRoute answers one SSE request.
func (h *HttpServer) streamRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodHead {
		send(w, 200, "", map[string]string{"Content-Type": "text/event-stream; charset=utf-8"})
		return
	}
	h.streamClient(w, r)
}

// The SSE payloads, their keys in the order the dashboard reads them.

type snapshotMessage struct {
	Type       string                  `json:"type"`
	Summary    shared.Summary          `json:"summary"`
	Sessions   []store.SessionListItem `json:"sessions"`
	Models     store.ModelsAnswer      `json:"models"`
	CatchingUp bool                    `json:"catchingUp"`
}

type sessionsMessage struct {
	Type     string                  `json:"type"`
	Sessions []store.SessionListItem `json:"sessions"`
	Summary  shared.Summary          `json:"summary"`
	Models   store.ModelsAnswer      `json:"models"`
}

type requestMessage struct {
	Type    string               `json:"type"`
	Request shared.RequestRecord `json:"request"`
}

type toolMessage struct {
	Type string                `json:"type"`
	Tool shared.ToolCallRecord `json:"tool"`
}

type eventMessage struct {
	Type  string             `json:"type"`
	Event shared.EventRecord `json:"event"`
}

type ingestMessage struct {
	Type       string `json:"type"`
	CatchingUp bool   `json:"catchingUp"`
}

// sseFrame renders one data frame.
func sseFrame(payload any) string {
	encoded, err := domain.MarshalCompactJSON(payload)
	if err != nil {
		return ""
	}
	return "data: " + string(encoded) + "\n\n"
}

// streamClient serves one SSE client: snapshot on connect, store deltas after that,
// heartbeat to keep intermediaries honest.
func (h *HttpServer) streamClient(w http.ResponseWriter, r *http.Request) {
	controller := http.NewResponseController(w)
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ctx, cancel := context.WithCancel(r.Context())
	conn := &streamConn{frames: make(chan string, 256), cancel: cancel}
	h.streamsMu.Lock()
	h.streams[conn] = struct{}{}
	h.streamsMu.Unlock()
	defer func() {
		h.streamsMu.Lock()
		delete(h.streams, conn)
		h.streamsMu.Unlock()
		cancel()
	}()

	write := func(frame string) bool {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		if _, err := io.WriteString(w, frame); err != nil {
			return false
		}
		_ = controller.Flush()
		return true
	}

	if !write("retry: 3000\n\n") {
		return
	}

	// the subscription goes in before the snapshot is built: a change that lands while the
	// snapshot is being read waits in the connection's queue and goes out after it, in
	// order, so nothing is lost between the two. A repeat is harmless: the snapshot carries
	// no records, and the dashboard replaces a request or tool frame's older copy by id.
	// the sessions message rebuilds every view, so a burst of changes costs it one build:
	// the first change arms a window, changes inside it fold in, the window's end sends one
	// message
	sessions := make(chan struct{}, 1)
	pushSessions := func() {
		select {
		case sessions <- struct{}{}:
		default:
		}
	}
	unsubscribe := h.options.Store.OnUpdate(func(change store.Change) {
		for _, request := range change.Requests {
			conn.enqueue(sseFrame(requestMessage{Type: "request", Request: request}))
		}
		for _, tool := range change.Tools {
			conn.enqueue(sseFrame(toolMessage{Type: "tool", Tool: tool}))
		}
		for _, event := range change.Events {
			conn.enqueue(sseFrame(eventMessage{Type: "event", Event: event}))
		}
		if change.Sessions {
			pushSessions()
		}
	})
	defer unsubscribe()

	catchingUp := h.options.Watcher != nil && h.options.Watcher.CatchingUp()
	if !write(sseFrame(snapshotMessage{
		Type:       "snapshot",
		Summary:    h.options.Store.Summary(),
		Sessions:   h.options.Store.SessionList(),
		Models:     h.options.Store.Models(nil),
		CatchingUp: catchingUp,
	})) {
		return
	}
	if h.options.SnapshotSent != nil {
		h.options.SnapshotSent()
	}

	heartbeat := h.options.HeartbeatMs
	if heartbeat <= 0 {
		heartbeat = defaultHeartbeatMs
	}
	beat := time.NewTicker(time.Duration(heartbeat) * time.Millisecond)
	defer beat.Stop()
	sessionsWindow := time.NewTimer(sessionsEveryMs * time.Millisecond)
	defer sessionsWindow.Stop()
	if !sessionsWindow.Stop() {
		<-sessionsWindow.C
	}
	windowArmed := false
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-conn.frames:
			if !write(frame) {
				return
			}
		case <-sessions:
			if !windowArmed {
				windowArmed = true
				sessionsWindow.Reset(sessionsEveryMs * time.Millisecond)
			}
		case <-sessionsWindow.C:
			windowArmed = false
			// the changes that landed inside the window are spent here: their signals are
			// drained, so only a change after this point opens the next window
			drainDone := false
			for !drainDone {
				select {
				case <-sessions:
				default:
					drainDone = true
				}
			}
			if !write(sseFrame(sessionsMessage{
				Type:     "sessions",
				Sessions: h.options.Store.SessionList(),
				Summary:  h.options.Store.Summary(),
				Models:   h.options.Store.Models(nil),
			})) {
				return
			}
		case <-beat.C:
			if !write(":hb\n\n") {
				return
			}
		}
	}
}

// enqueue queues one frame for the client. A client too slow to keep up has its stream closed
// rather than blocking the store's mutation or silently missing a delta: the browser reconnects
// after the stream's retry interval and starts again from a fresh snapshot.
func (c *streamConn) enqueue(frame string) {
	if frame == "" {
		return
	}
	select {
	case c.frames <- frame:
	default:
		c.cancel()
	}
}

// watchIngest tells every open stream when the ingest backlog word flips: a client that
// connected mid-catch-up read `true` on its snapshot, and one that missed the last word
// must not wait for a change that never comes.
func (h *HttpServer) watchIngest() {
	ticker := time.NewTicker(ingestEveryMs * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.closed:
			return
		case <-ticker.C:
			catchingUp := h.options.Watcher.CatchingUp()
			h.ingestMu.Lock()
			flipped := h.ingestWord == nil || *h.ingestWord != catchingUp
			word := catchingUp
			h.ingestWord = &word
			h.ingestMu.Unlock()
			if !flipped {
				continue
			}
			h.streamsMu.Lock()
			conns := make([]*streamConn, 0, len(h.streams))
			for conn := range h.streams {
				conns = append(conns, conn)
			}
			h.streamsMu.Unlock()
			for _, conn := range conns {
				conn.enqueue(sseFrame(ingestMessage{Type: "ingest", CatchingUp: catchingUp}))
			}
		}
	}
}

// send writes one whole answer: the body's byte length, not its character count, sizes the
// frame — any non-ASCII glyph is more than one UTF-8 byte per code unit, and a short
// Content-Length makes the overflow parse as a phantom next response on the keep-alive
// connection.
func send(w http.ResponseWriter, status int, body string, extra map[string]string) {
	for key, value := range extra {
		w.Header().Set(key, value)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// sendJSON writes one JSON answer, the body one line with a newline to end it.
func sendJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := domain.MarshalCompactJSON(body)
	if err != nil {
		encoded = []byte(`{"error":"could not encode"}`)
	}
	send(w, status, string(encoded)+"\n", map[string]string{
		"Content-Type":  "application/json; charset=utf-8",
		"Cache-Control": "no-store",
	})
}

// WriteAllowed says whether one write may go through: it must come from the dashboard
// itself — JSON (a cross-site form or a "simple" fetch cannot send that without a preflight
// we never answer), and when the browser says where it came from, our own origin.
func WriteAllowed(header http.Header, port int) bool {
	return writeAllowed(header, func(origin string) bool {
		ours := fmt.Sprintf("http://127.0.0.1:%d", port)
		local := fmt.Sprintf("http://localhost:%d", port)
		return origin == ours || origin == local
	})
}

// writeAllowed is the write guard with the Origin check plugged in: standalone radar takes
// its loopback spellings, the embedding server names the origins its own guard takes.
func writeAllowed(header http.Header, originOK func(origin string) bool) bool {
	contentType := joinedHeader(header, "Content-Type")
	if !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return false
	}
	if len(header.Values("Origin")) > 0 && !originOK(joinedHeader(header, "Origin")) {
		return false
	}
	if len(header.Values("Sec-Fetch-Site")) > 0 {
		switch joinedHeader(header, "Sec-Fetch-Site") {
		case "same-origin", "none":
		default:
			return false
		}
	}
	return true
}

// joinedHeader reads one header the way the runtime hands it to the handler: every value of
// a repeated field joined with ", ", so a second, hostile value can never hide behind the
// first.
func joinedHeader(header http.Header, key string) string {
	return strings.Join(header.Values(key), ", ")
}

// HostAllowed says whether the Host header names this exact server — only
// 127.0.0.1:<port> / localhost:<port>; an attacker's domain resolving here fails this.
func HostAllowed(host string, port int) bool {
	return host == fmt.Sprintf("127.0.0.1:%d", port) || host == fmt.Sprintf("localhost:%d", port)
}
