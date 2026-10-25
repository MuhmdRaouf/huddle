// Process-level wiring for the foreground server: create the store, start the watcher and the
// history writer (the writer before the watcher's first pass, so everything ingest sees,
// history sees too), bind loopback with the saved port first, publish server.json (0600 — this
// process writes it itself, so it never races a bind it does not own), own shutdown, and watch
// for the plugin's own removal: this process outlives its plugin.
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/alerts"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/lifecycle"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// DrainMs is how long shutdown waits for requests it already took before it cuts the
// connections.
const DrainMs int64 = 30_000

// ServerInfo is the content of server.json: the address and pid of the one running dashboard.
type ServerInfo struct {
	// Pid is this server's own process.
	Pid int64
	// Port is the loopback port the dashboard answers on.
	Port int
	// URL is the address a browser opens.
	URL string
	// StartedAt is the epoch milliseconds this start began.
	StartedAt float64
	// Token is the credential a proxying huddle presents on the owner's guarded radar routes;
	// empty when the writer reads no credential (a standalone radar has none to give).
	Token string
}

// serverInfoBody fixes server.json's key order: pid, port, url, startedAt, token.
type serverInfoBody struct {
	Pid       int64   `json:"pid"`
	Port      int     `json:"port"`
	URL       string  `json:"url"`
	StartedAt jsFloat `json:"startedAt"`
	Token     string  `json:"token,omitempty"`
}

// MarshalJSON writes server.json's body: the keys in their order, the timestamps the way
// JavaScript prints them.
func (i ServerInfo) MarshalJSON() ([]byte, error) {
	return domain.MarshalCompactJSON(serverInfoBody{
		Pid:       i.Pid,
		Port:      i.Port,
		URL:       i.URL,
		StartedAt: jsFloat(i.StartedAt),
		Token:     i.Token,
	})
}

// jsFloat is a JSON number rendered the way JavaScript prints it.
type jsFloat float64

func (f jsFloat) MarshalJSON() ([]byte, error) { return []byte(jsfmt.JSNumber(float64(f))), nil }

// WriteServerInfo publishes server.json (0600, one line); false when the file could not be
// written. A first-ever start has no state dir yet, so it is created on the way.
func WriteServerInfo(ctx context.Context, env domain.EnvLookup, info ServerInfo) bool {
	if !paths.EnsureStateDirs(ctx, env) {
		return false
	}
	body, err := domain.MarshalCompactJSON(info)
	if err != nil {
		return false
	}
	file, err := os.OpenFile(paths.ServerInfoPath(ctx, env), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		return false
	}
	return file.Close() == nil
}

// ReadServerInfo answers server.json, or nil when it is missing, malformed, or not the shape
// this server writes: pid and port must be numbers, the url falls back to the port's address,
// startedAt to 0 and the token to none.
func ReadServerInfo(ctx context.Context, env domain.EnvLookup) *ServerInfo {
	data, err := os.ReadFile(paths.ServerInfoPath(ctx, env))
	if err != nil {
		return nil
	}
	value, err := jsfmt.DecodeValue(data)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return nil
	}
	pid, pidOk := numberFieldOf(fieldOf(value, "pid"))
	port, portOk := numberFieldOf(fieldOf(value, "port"))
	if !pidOk || !portOk {
		return nil
	}
	url := "http://127.0.0.1:" + jsfmt.JSNumber(port)
	if field := fieldOf(value, "url"); field != nil && field.Kind() == jsfmt.KindString {
		url = field.Text()
	}
	var startedAt float64
	if read, ok := numberFieldOf(fieldOf(value, "startedAt")); ok {
		startedAt = read
	}
	token := ""
	if field := fieldOf(value, "token"); field != nil && field.Kind() == jsfmt.KindString {
		token = field.Text()
	}
	return &ServerInfo{Pid: int64(pid), Port: int(port), URL: url, StartedAt: startedAt, Token: token}
}

// numberFieldOf reads one JSON number, the only shape typeof n === "number" admits: the
// literal is re-rendered the way JavaScript prints it first, so an out-of-range literal
// (1e999) reads as ±Inf, the way JSON.parse yields Infinity.
func numberFieldOf(value *jsfmt.Value) (float64, bool) {
	if value == nil || value.Kind() != jsfmt.KindNumber {
		return 0, false
	}
	parsed, _ := strconv.ParseFloat(value.EncodeJSON(), 64)
	return parsed, true
}

// RemoveServerInfo unlinks server.json; nothing to remove is the common case after a crash.
func RemoveServerInfo(ctx context.Context, env domain.EnvLookup) {
	_ = os.Remove(paths.ServerInfoPath(ctx, env))
}

// AppOptions shapes one app run.
type AppOptions struct {
	Env     domain.EnvLookup
	Version string
	// SinceMs is how far back the first ingest pass reads.
	SinceMs int64
	// Now is the clock everything here reads; nil is the real one.
	Now func() time.Time
	// Port pins one port (0 takes the saved one, else a random draw).
	Port int
	// Random draws candidate ports; nil draws natively.
	Random RandomSource
	// IntervalMs is the ingest poll cadence; 0 takes the watcher's default.
	IntervalMs int64
	// RemovalIntervalMs overrides the removal-check interval; nil takes RADAR_REMOVAL_MS, then
	// 10 s.
	RemovalIntervalMs *int
	// Exit ends the process when the server removed itself; nil exits for real.
	Exit func(code int)
	// NotifyRunner shows a desktop notification; nil spawns one detached.
	NotifyRunner alerts.Runner
	// InsightsTickMs is the insights flush cadence; 0 takes the 10 s default.
	InsightsTickMs int64
	// Log receives the one line a missing history file leaves; nil writes stderr.
	Log func(line string)
}

// App is the running dashboard: the store, the watcher feeding it, the insights derived from
// it, the history writer behind it, and the loopback server in front — with the Stop that
// ends all of it.
type App struct {
	Server    *HttpServer
	Store     *store.Store
	Watcher   *ingest.Watcher
	Insights  *Insights
	Writer    *history.HistoryWriter
	Port      int
	URL       string
	MovedFrom int

	env     domain.EnvLookup
	ctx     context.Context
	cancel  func()
	prunes  *history.PruneSchedule
	removal *lifecycle.RemovalWatch

	signals   chan os.Signal
	sigDone   chan struct{}
	serveDone chan struct{}
	stopOnce  sync.Once
	stopped   chan struct{}
}

// StartApp wires the dashboard and serves. The store, the history file, the watcher and
// insights come first; loopback binds with the saved port when one is there; then server.json
// and the port file publish the address, and the removal watch and the SIGTERM handler own the
// exits this process does not take itself.
func StartApp(ctx context.Context, options AppOptions) (*App, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	log := options.Log
	if log == nil {
		log = func(line string) { fmt.Fprintln(os.Stderr, line) }
	}
	appCtx, cancel := context.WithCancel(ctx)
	st := store.CreateStore(now)

	var hist *history.History
	var writer *history.HistoryWriter
	if opened, err := history.OpenHistory(paths.HistoryPath(appCtx, options.Env), nil); err != nil {
		log(fmt.Sprintf("radar: history unavailable, running without it (%v)", err))
	} else {
		hist = opened
		writer = history.AttachHistoryWriter(appCtx, history.HistoryWriterOptions{Store: st, History: hist})
	}
	var prunes *history.PruneSchedule
	if hist != nil {
		// the retention the user chose, read fresh on every prune so a change takes the next beat
		retention := func(ctx context.Context) int {
			return settings.ReadSettings(ctx, options.Env).HistoryRetentionDays
		}
		prunes = history.SchedulePrunes(appCtx, hist, retention, history.PruneOptions{})
	}
	watcher := ingest.StartWatcher(appCtx, ingest.WatcherOptions{
		Env:        options.Env,
		Store:      st,
		SinceMs:    options.SinceMs,
		IntervalMs: options.IntervalMs,
		Now:        func() int64 { return now().UnixMilli() },
		Log:        log,
		HarnessCollectors: []ingest.HarnessCollector{
			ingest.NewCodexCollector(options.Env),
			ingest.NewOpencodeCollector(options.Env),
		},
	})
	tickMs := options.InsightsTickMs
	if tickMs == 0 {
		tickMs = DefaultTickMs
	}
	quotas := CreateQuotas(options.Env)
	insights := CreateInsights(appCtx, InsightsOptions{
		Env:     options.Env,
		Store:   st,
		Now:     now,
		TickMs:  tickMs,
		Runner:  options.NotifyRunner,
		Quotas:  quotas,
		Watcher: watcher,
	})
	startedAt := now().UnixMilli()
	httpServer := CreateHttpServer(HttpOptions{
		Store:     st,
		Version:   options.Version,
		StartedAt: startedAt,
		Insights:  insights,
		Quotas:    quotas,
		History:   hist,
		SelfCheck: NewSelfCheck(hist, ingest.TranscriptRoots(appCtx, options.Env), now),
		Watcher:   watcher,
	})
	listener, bound, movedFrom, err := bindSticky(ctx, options)
	if err != nil {
		watcher.Stop()
		insights.Stop()
		prunes.Stop()
		if writer != nil {
			writer.Stop()
		}
		httpServer.CloseStreams()
		cancel()
		return nil, err
	}
	httpServer.SetPort(bound)
	url := fmt.Sprintf("http://127.0.0.1:%d", bound)
	a := &App{
		Server:    httpServer,
		Store:     st,
		Watcher:   watcher,
		Insights:  insights,
		Writer:    writer,
		Port:      bound,
		URL:       url,
		MovedFrom: movedFrom,
		env:       options.Env,
		ctx:       ctx,
		cancel:    cancel,
		prunes:    prunes,
		signals:   make(chan os.Signal, 1),
		sigDone:   make(chan struct{}),
		serveDone: make(chan struct{}),
		stopped:   make(chan struct{}),
	}
	go func() {
		defer close(a.serveDone)
		_ = httpServer.Server.Serve(listener)
	}()
	WriteServerInfo(ctx, options.Env, ServerInfo{
		Pid:       int64(os.Getpid()),
		Port:      bound,
		URL:       url,
		StartedAt: float64(startedAt),
	})
	paths.SavePort(ctx, options.Env, bound)

	// Two agreeing checks that the plugin is gone stop the server: uninstalled takes the state
	// dir with it, disabled keeps it for the reinstall. Either way the requests already taken
	// finish.
	exit := options.Exit
	if exit == nil {
		exit = os.Exit
	}
	interval := lifecycle.RemovalIntervalMs(options.Env, options.RemovalIntervalMs)
	a.removal = lifecycle.WatchRemoval(appCtx, lifecycle.WatchOptions{
		Env:         options.Env,
		Plugin:      "radar",
		ProjectDirs: func() []string { return projectDirsOf(st) },
		IntervalMs:  &interval,
		OnRemove: func(ctx context.Context, kind lifecycle.RemovalKind) {
			a.Stop()
			if kind == lifecycle.RemovalUninstalled {
				lifecycle.RemoveStateTree(ctx, options.Env)
			}
			exit(0)
		},
	})
	signal.Notify(a.signals, syscall.SIGTERM)
	go func() {
		select {
		case <-a.signals:
			a.Stop()
			os.Exit(0)
		case <-a.sigDone:
		}
	}()
	return a, nil
}

// bindSticky binds loopback. No pinned port: the port the dashboard had last time, so a
// bookmark survives a stop or a reboot; a new random one only when another program holds the
// saved one now, and movedFrom says which it was.
func bindSticky(ctx context.Context, options AppOptions) (net.Listener, int, int, error) {
	listenOptions := Options{Random: options.Random}
	if options.Port != 0 {
		listenOptions.Port = options.Port
	}
	saved, hasSaved := 0, false
	if options.Port == 0 {
		saved, hasSaved = paths.SavedPort(ctx, options.Env)
	}
	if !hasSaved {
		sock := NewServer()
		port, err := ListenLocal(ctx, sock, listenOptions)
		if err != nil {
			return nil, 0, 0, err
		}
		return sock.Listener(), port, 0, nil
	}
	first := NewServer()
	port, err := ListenLocal(ctx, first, Options{Port: saved})
	if err == nil {
		return first.Listener(), port, 0, nil
	}
	var listenErr *ListenError
	if errors.As(err, &listenErr) && listenErr.Retryable() {
		second := NewServer()
		port, retryErr := ListenLocal(ctx, second, listenOptions)
		if retryErr != nil {
			return nil, 0, 0, retryErr
		}
		return second.Listener(), port, saved, nil
	}
	return nil, 0, 0, err
}

// projectDirsOf lists the projects this server has tracked sessions in — their settings can
// re-enable the plugin.
func projectDirsOf(st *store.Store) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, session := range st.SessionList() {
		if session.Cwd == nil {
			continue
		}
		if _, duplicate := seen[*session.Cwd]; duplicate {
			continue
		}
		seen[*session.Cwd] = struct{}{}
		out = append(out, *session.Cwd)
	}
	return out
}

// Stop ends the app: the watcher, the removal watch, insights, retention and the history
// writer stop first, then the server stops taking connections and the requests it already
// took finish (at most DrainMs), the streams and the idle keep-alives are cut after that, and
// server.json goes. Idempotent: a self-removal and the caller's stop may both arrive.
func (a *App) Stop() {
	a.stopOnce.Do(a.shutdown)
	<-a.stopped
}

// shutdown is the one path every stop takes.
func (a *App) shutdown() {
	defer close(a.stopped)
	signal.Stop(a.signals)
	close(a.sigDone) // ends the SIGTERM watch
	a.Watcher.Stop()
	a.removal.Stop()
	a.Insights.Stop()
	a.prunes.Stop()
	if a.Writer != nil {
		a.Writer.Stop()
	}
	a.cancel()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		_ = a.Server.Server.Close() // no new connections from here on
	}()
	a.Server.WhenIdle(context.Background(), DrainMs)
	a.Server.CloseStreams() // streams first; the idle keep-alives Close already took
	<-closed
	<-a.serveDone
	RemoveServerInfo(a.ctx, a.env)
}
