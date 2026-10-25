package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/watch"
	"github.com/MuhmdRaouf/huddle/internal/kanban/web"
)

// The exits one serve run takes, and the budgets it keeps.
const (
	ReasonSignal    = "signal"
	ReasonStop      = "stop"
	ReasonBoardGone = "board gone"
	ReasonDuplicate = "duplicate"
)

const (
	// FirstPort and LastPort bound the default port walk.
	FirstPort = 18800
	LastPort  = 18849
	// defaultEveryMs is the watcher cadence the serve run takes.
	defaultEveryMs = 300
	// defaultPingMs is the event stream's ping cadence.
	defaultPingMs = 15_000
	// defaultGoneMs is how long the board folder may be gone.
	defaultGoneMs = 10_000
	// defaultDupMs is how often a running server checks for a successor.
	defaultDupMs = 5_000
	// shutdownMs bounds the drain once an exit arrived: the process is gone
	// within two seconds of its stop.
	shutdownMs = 2_000
)

// Options shapes one serve run.
type Options struct {
	Board   board.Board
	Home    string
	Version string
	// Port pins one port (>0); 0 walks FirstPort..LastPort; below 0 lets the
	// OS pick at once.
	Port        int
	EveryMs     int
	PingMs      int
	BoardGoneMs int
	DupMs       int
	Now         func() time.Time
	Stdout      io.Writer
}

// App is one board server: the watcher feeding an event hub, and the HTTP
// surface in front of both.
type App struct {
	Port  int
	URL   string
	State State

	board   board.Board
	home    string
	key     string
	version string
	abs     string
	every   time.Duration
	ping    time.Duration
	gone    time.Duration
	dup     time.Duration
	now     func() time.Time
	stdout  io.Writer

	watcher    *watch.Watcher
	hub        *hub
	httpServer *http.Server
	files      fs.FS
	token      string
	stopReason chan string
	port       atomic.Int64
}

// NewApp builds the server around one watcher, unbound and unpublished.
func NewApp(opts Options) *App {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	a := &App{
		board:      opts.Board,
		home:       opts.Home,
		version:    opts.Version,
		abs:        absPath(opts.Board.Root),
		every:      milliseconds(opts.EveryMs, defaultEveryMs),
		ping:       milliseconds(opts.PingMs, defaultPingMs),
		gone:       milliseconds(opts.BoardGoneMs, defaultGoneMs),
		dup:        milliseconds(opts.DupMs, defaultDupMs),
		now:        now,
		stdout:     opts.Stdout,
		watcher:    watch.New(opts.Board, watch.Options{EveryMs: opts.EveryMs, Now: opts.Now}),
		hub:        &hub{conns: map[*sseClient]struct{}{}},
		files:      web.FS(),
		token:      newToken(),
		stopReason: make(chan string, 4),
	}
	a.key = KeyOf(a.abs)
	a.httpServer = &http.Server{Handler: a}
	return a
}

// milliseconds answers one option's duration, or the default when it is unset.
func milliseconds(value, fallback int) time.Duration {
	if value > 0 {
		return time.Duration(value) * time.Millisecond
	}
	return time.Duration(fallback) * time.Millisecond
}

// Handler answers the app's HTTP surface; the app itself is the handler.
func (a *App) Handler() http.Handler { return a }

// SetPort names the port the Host guard checks against and the URLs carry.
func (a *App) SetPort(port int) {
	a.port.Store(int64(port))
	a.Port = port
	a.URL = fmt.Sprintf("http://127.0.0.1:%d", port)
}

// publish hands one watcher delta to every open page.
func (a *App) publish(delta watch.Delta) {
	a.hub.publish(sseFrame{name: "delta", data: string(encodeCompact(delta))})
}

// exitWith asks the serve run to end; a reason already queued wins.
func (a *App) exitWith(reason string) {
	select {
	case a.stopReason <- reason:
	default:
	}
}

// Serve runs the board's server until one of its exits arrives: a signal, the
// stop request, the board folder disappearing, or another server answering for
// the board. Every exit closes the streams, waits at most shutdownMs for the
// requests it took, removes the state file when it is still this process's,
// and leaves one line in the log.
func Serve(ctx context.Context, opts Options) (string, error) {
	if opts.Home == "" {
		return "", errors.New("no state dir: set KANBAN_HOME or a home directory")
	}
	a := NewApp(opts)
	warn := func(line string) { AppendLog(a.home, a.key, line) }

	// a server that already answers for this board is the server; this one
	// bows out without touching anything of its predecessor's
	if recorded := ReadState(opts.Home, a.key); recorded != nil && healthyAt(ctx, recorded.Port, a.abs) {
		AppendLog(opts.Home, a.key, "kanban: stopped (reason: "+ReasonDuplicate+")")
		return ReasonDuplicate, nil
	}

	listener, port, err := bindPort(opts.Port)
	if err != nil {
		return "", err
	}
	a.SetPort(port)

	// the first pass fills the snapshot before the first page opens
	_, _ = a.watcher.Scan(ctx)

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	safego.Go(warn, func() { a.watcher.Run(runCtx, a.publish) })

	serveDone := make(chan error, 1)
	safego.Go(warn, func() { serveDone <- a.httpServer.Serve(listener) })

	// the state names a listening server only: the file appears once the port
	// answers, so a start that reads it never races a bind that failed
	a.State = State{
		Pid: os.Getpid(), Port: port, Board: a.abs, Version: a.version,
		Token: a.token, StartedAt: a.now().Format(time.RFC3339),
	}
	if err := WriteState(ctx, opts.Home, a.key, a.State); err != nil {
		cancelRun()
		a.hub.closeAll()
		_ = a.httpServer.Close()
		<-serveDone
		return "", fmt.Errorf("cannot write the state file: %w", err)
	}
	listening := "kanban: listening on " + a.URL + " (" + a.abs + ")"
	AppendLog(opts.Home, a.key, listening)
	if a.stdout != nil {
		fmt.Fprintln(a.stdout, listening)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	safego.Go(warn, func() { a.watchBoardGone(runCtx) })
	safego.Go(warn, func() { a.watchDuplicate(runCtx) })

	var reason string
	select {
	case reason = <-a.stopReason:
	case <-signals:
		reason = ReasonSignal
	case <-ctx.Done():
		reason = ReasonSignal
	}

	cancelRun()
	a.hub.closeAll()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), shutdownMs*time.Millisecond)
	defer cancelShutdown()
	_ = a.httpServer.Shutdown(shutdownCtx)
	<-serveDone
	if recorded := ReadState(opts.Home, a.key); recorded != nil && recorded.Pid == os.Getpid() {
		RemoveState(opts.Home, a.key)
	}
	AppendLog(opts.Home, a.key, "kanban: stopped (reason: "+reason+")")
	return reason, nil
}

// watchBoardGone ends the run when the board folder has been gone for the
// whole budget: a rename or a short blip never ends it.
func (a *App) watchBoardGone(ctx context.Context) {
	tick := a.gone / 4
	if tick > time.Second {
		tick = time.Second
	}
	if tick < 20*time.Millisecond {
		tick = 20 * time.Millisecond
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	var missingSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if _, err := os.Stat(a.board.Root); err == nil {
			missingSince = time.Time{}
			continue
		}
		now := time.Now()
		if missingSince.IsZero() {
			missingSince = now
			continue
		}
		if now.Sub(missingSince) >= a.gone {
			a.exitWith(ReasonBoardGone)
			return
		}
	}
}

// watchDuplicate ends the run when another server answers for this board: two
// starts that raced both bound, and the state file names the winner.
func (a *App) watchDuplicate(ctx context.Context) {
	ticker := time.NewTicker(a.dup)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		recorded := ReadState(a.home, a.key)
		if recorded == nil || recorded.Pid == os.Getpid() {
			continue
		}
		if healthyAt(ctx, recorded.Port, a.abs) {
			a.exitWith(ReasonDuplicate)
			return
		}
	}
}

// bindPort binds loopback: the pinned port once, else the walk over
// FirstPort..LastPort, else whatever the OS picks.
func bindPort(port int) (net.Listener, int, error) {
	if port > 0 {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		return listener, port, err
	}
	if port == 0 {
		for candidate := FirstPort; candidate <= LastPort; candidate++ {
			if listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(candidate))); err == nil {
				return listener, candidate, nil
			}
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	return listener, listener.Addr().(*net.TCPAddr).Port, nil
}

// newToken answers the shutdown token: 32 random bytes as hex.
func newToken() string {
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

// healthyAt answers whether the port answers this board's health within a
// second. A pid alone never decides this: after a reboot it can name another
// process, so health is the only witness.
func healthyAt(ctx context.Context, port int, absBoard string) bool {
	requestCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/api/health", port), nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var health struct {
		OK    bool   `json:"ok"`
		Board string `json:"board"`
	}
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		return false
	}
	return health.OK && health.Board == absBoard
}

// API answers the read routes the page loads with — the board, one ticket, the
// event stream — for a server that mounts the board inside its own. The
// mounting server owns host, origin and credential, so the Host guard stays
// out, and the routes only the standalone server answers (health, refresh,
// shutdown) and the static page are not served here.
func (a *App) API() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.route(w, r, true)
	})
}

// ServeHTTP routes one request: the guards first, then the API, then the
// embedded page.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.allowed(r) {
		sendJSON(w, http.StatusForbidden, errorBody{Error: "forbidden host"})
		return
	}
	a.route(w, r, false)
}

// route answers one request's path: the three read routes both surfaces serve,
// then, on the standalone server only, health, refresh, shutdown and the page;
// every other path is a 404 either way.
func (a *App) route(w http.ResponseWriter, r *http.Request, mounted bool) {
	path := r.URL.Path
	switch {
	case path == "/api/board":
		a.oneMethod(w, r, http.MethodGet, func() { sendJSON(w, http.StatusOK, a.watcher.Snapshot()) })
	case strings.HasPrefix(path, "/api/tickets/"):
		id := strings.Trim(path[len("/api/tickets/"):], "/")
		a.oneMethod(w, r, http.MethodGet, func() { a.ticketRoute(w, id) })
	case path == "/api/events":
		a.oneMethod(w, r, http.MethodGet, func() { a.serveEvents(w, r) })
	case mounted:
		sendJSON(w, http.StatusNotFound, errorBody{Error: "not found: " + path})
	case path == "/api/health":
		a.oneMethod(w, r, http.MethodGet, func() {
			sendJSON(w, http.StatusOK, healthBody{OK: true, Board: a.abs, Version: a.version, Pid: os.Getpid()})
		})
	case path == "/api/refresh":
		a.oneMethod(w, r, http.MethodPost, func() { a.refreshRoute(w, r) })
	case path == "/api/shutdown":
		a.oneMethod(w, r, http.MethodPost, func() { a.shutdownRoute(w, r) })
	case strings.HasPrefix(path, "/api/"):
		sendJSON(w, http.StatusNotFound, errorBody{Error: "not found: " + path})
	default:
		a.staticRoute(w, path)
	}
}

// Run keeps the board's snapshot fresh with no listener, no port and no state
// record — the serve run's scan-and-watch half — so a server that mounts the
// board answers from a live view it never binds for. When the context ends the
// watcher stops and the open event streams close.
func (a *App) Run(ctx context.Context) {
	_, _ = a.watcher.Scan(ctx)
	watchCtx, cancelWatch := context.WithCancel(ctx)
	warn := func(line string) { AppendLog(a.home, a.key, line) }
	safego.Go(warn, func() { a.watcher.Run(watchCtx, a.publish) })
	safego.Go(warn, func() {
		<-ctx.Done()
		cancelWatch()
		a.hub.closeAll()
	})
}

// oneMethod answers one route for its one method, 405 for the others.
func (a *App) oneMethod(w http.ResponseWriter, r *http.Request, method string, serve func()) {
	if r.Method != method {
		sendJSON(w, http.StatusMethodNotAllowed, errorBody{Error: "method not allowed"})
		return
	}
	serve()
}

// allowed answers whether the request is ours: the Host header names this
// exact server (a DNS-rebinding probe naming another domain is not), and the
// connection came from loopback.
func (a *App) allowed(r *http.Request) bool {
	port := int(a.port.Load())
	ours := fmt.Sprintf("127.0.0.1:%d", port)
	local := fmt.Sprintf("localhost:%d", port)
	if r.Host != ours && r.Host != local {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ticketRoute answers one ticket whole: its card, its fields, its body and its
// path; a ticket that is not on the board is a 404.
func (a *App) ticketRoute(w http.ResponseWriter, id string) {
	shown, err := a.board.Show(context.Background(), id)
	if err != nil {
		sendJSON(w, http.StatusNotFound, errorBody{Error: err.Error()})
		return
	}
	fields := shown.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	sendJSON(w, http.StatusOK, ticketBody{Card: shown.Card, Fields: fields, Body: shown.Body, Path: shown.Path})
}

// refreshRoute rescans the board now, sends the reload and answers the rev.
func (a *App) refreshRoute(w http.ResponseWriter, r *http.Request) {
	if delta, changed := a.watcher.Scan(r.Context()); changed {
		a.publish(delta)
	}
	rev := a.watcher.Snapshot().Rev
	a.hub.publish(sseFrame{name: "reload", data: string(encodeCompact(revBody{Rev: rev}))})
	sendJSON(w, http.StatusOK, revBody{Rev: rev})
}

// shutdownRoute takes the stop request: only the token the state file carries.
func (a *App) shutdownRoute(w http.ResponseWriter, r *http.Request) {
	if a.token == "" || r.Header.Get("X-Kanban-Token") != a.token {
		sendJSON(w, http.StatusForbidden, errorBody{Error: "forbidden token"})
		return
	}
	sendJSON(w, http.StatusOK, okBody{OK: true})
	a.exitWith(ReasonStop)
}

// staticRoute serves the embedded page: a known file by name, every other
// non-API path the index, which owns its own routes.
func (a *App) staticRoute(w http.ResponseWriter, path string) {
	name := strings.TrimPrefix(path, "/")
	if name != "" && name != "index.html" {
		if data, err := fs.ReadFile(a.files, name); err == nil {
			send(w, http.StatusOK, string(data), contentTypeOf(name))
			return
		}
	}
	data, err := fs.ReadFile(a.files, "index.html")
	if err != nil {
		sendJSON(w, http.StatusInternalServerError, errorBody{Error: "the board page is missing"})
		return
	}
	send(w, http.StatusOK, string(data), "text/html; charset=utf-8")
}

// contentTypeOf answers one file name's content type.
func contentTypeOf(name string) string {
	switch filepath.Ext(name) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	default:
		return "application/octet-stream"
	}
}

// The JSON bodies, their keys in the order the page reads them.

type errorBody struct {
	Error string `json:"error"`
}

type healthBody struct {
	OK      bool   `json:"ok"`
	Board   string `json:"board"`
	Version string `json:"version"`
	Pid     int    `json:"pid"`
}

type ticketBody struct {
	Card   board.Card        `json:"card"`
	Fields map[string]string `json:"fields"`
	Body   string            `json:"body"`
	Path   string            `json:"path"`
}

type revBody struct {
	Rev int64 `json:"rev"`
}

type okBody struct {
	OK bool `json:"ok"`
}

// encodeCompact marshals one body on one line with the angle brackets, the
// ampersands and the quotes raw, the way ticket text reads.
func encodeCompact(v any) []byte {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return []byte("null")
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// sendJSON writes one JSON answer: the body one line with a newline to end it.
func sendJSON(w http.ResponseWriter, status int, body any) {
	encoded := append(encodeCompact(body), '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// send writes one whole answer: the body's byte length, not its character
// count, sizes the frame.
func send(w http.ResponseWriter, status int, body, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
