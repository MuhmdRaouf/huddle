// Package radartab runs radar inside the huddle server: one binary, one server. It builds
// radar's app — the store, the history writer, the ingest watcher, the insights — and serves
// radar's routes under /api/radar on huddle's own mux, so radar's ingest, its spool and
// budget-status.json keep their contracts and the routers need no change. The one-ingester rule
// decides who reads the spool: when radar's own lock (server.json in RADAR_HOME) names a live
// owner that answers health, huddle proxies /api/radar to it and starts no watcher of its own;
// otherwise huddle takes the lock the same way radar's app does — same file, same fields — so a
// later standalone radar sees huddle as the owner and reuses it instead of ingesting twice.
package radartab

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/radar/alerts"
	"github.com/MuhmdRaouf/huddle/internal/radar/cli"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/hook"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/server"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// Prefix is the huddle path radar's routes live under: /api/radar + radar's own path.
const Prefix = "/api/radar"

// healthPath is radar's health path. Huddle answers it on its own port while it owns the lock,
// because that is the path radar's command line probes to tell an owner from a stale record.
const healthPath = "/api/health"

// probeTimeout bounds one ownership probe; radar's own CLI gives health two seconds.
const probeTimeout = 2 * time.Second

// MountOptions shapes the mount. Env is radar's whole environment (RADAR_HOME, HOME,
// CLAUDE_CONFIG_DIR, the providers' state dirs); Port is the port huddle serves on, the port
// radar's host guard checks and the lock records.
type MountOptions struct {
	Env     domain.EnvLookup
	Version string
	Port    int
	// HostAllowed names the hosts the embedding server answers — the hub listener's
	// spellings beside loopback — so the tab serves the names that server serves; nil keeps
	// radar's loopback-only rule.
	HostAllowed func(host string) bool
	// OriginAllowed names the origins the embedding server's own write guard accepts — the
	// hub listener's spellings beside loopback — so the tab takes the writes that server
	// takes; nil keeps radar's loopback-only rule.
	OriginAllowed func(origin string) bool
	// SinceMs is how far back the first ingest pass reads; 0 takes radar's own default.
	SinceMs int64
	// IntervalMs is the ingest poll cadence; 0 takes the watcher's default.
	IntervalMs int64
	// InsightsTickMs is the insights flush cadence; 0 takes the 10 s default.
	InsightsTickMs int64
	// NotifyRunner shows a desktop notification; nil spawns one detached.
	NotifyRunner alerts.Runner
	// Now is the clock everything here reads; nil is the real one.
	Now func() time.Time
	// Log receives the one line a proxying mount leaves; nil writes stderr.
	Log func(line string)
	// Client fetches a recorded owner; nil takes one with the probe timeout.
	Client *http.Client
}

// Tab is radar mounted inside huddle. In the owning mode it holds the running app (the store,
// the watcher feeding it, the insights derived from it, the history writer behind it) and
// answers radar's routes; in the proxying mode a recorded owner holds the spool and the tab
// forwards /api/radar to it.
type Tab struct {
	Store    *store.Store
	Watcher  *ingest.Watcher
	Insights *server.Insights
	Writer   *history.HistoryWriter
	// Owns says this mount took radar's lock and runs the one ingester.
	Owns bool

	port       int
	env        domain.EnvLookup
	log        func(line string)
	cancel     func()
	prunes     *history.PruneSchedule
	httpServer *server.HttpServer

	// proxying mode: the recorded owner's address and whether its radar routes sit under the
	// prefix too (another huddle) or at the root (a standalone radar)
	target     string
	keepPrefix bool
	client     *http.Client
	proxy      *httputil.ReverseProxy

	// token is the credential this mount and its radar routes answer: owning, it is the word in
	// the mount's own lock record that a proxying huddle presents; proxying, it is the word the
	// owner's record published, sent on every fetch the mount makes of the owner.
	token string

	stopOnce sync.Once
	stopped  chan struct{}
}

// Mount wires radar into mux. Radar's lock is read first: an owner that answers health keeps
// the spool and this mount proxies to it; anything else — no record, a dead pid, a port nothing
// answers — leaves huddle taking the lock and starting the ingest itself. Serving starts when
// the caller serves the mux; Stop ends the ingest and releases the lock.
func Mount(ctx context.Context, mux *http.ServeMux, radarHome string, options MountOptions) *Tab {
	env := options.Env
	if radarHome != "" {
		env = domain.EnvLookup{}
		for key, value := range options.Env {
			env[key] = value
		}
		env["RADAR_HOME"] = radarHome
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	log := options.Log
	if log == nil {
		log = func(line string) { fmt.Fprintln(os.Stderr, line) }
	}
	t := &Tab{
		port:    options.Port,
		env:     env,
		log:     log,
		stopped: make(chan struct{}),
	}
	if owner, ok := findRecordedOwner(ctx, env, options.Client); ok {
		t.target = fmt.Sprintf("127.0.0.1:%d", owner.Port)
		t.keepPrefix = owner.ServesPrefix
		t.token = owner.Token
		t.client = options.Client
		if t.client == nil {
			t.client = &http.Client{Timeout: probeTimeout}
		}
		t.proxy = proxyTo(t.target, t.keepPrefix, t.token)
		mux.HandleFunc(Prefix, t.serveProxy)
		mux.HandleFunc(Prefix+"/", t.serveProxy)
		ownerKind := "a standalone radar"
		if owner.ServesPrefix {
			ownerKind = "another huddle"
		}
		log(fmt.Sprintf("huddle: %s owns radar's dashboard at http://%s; serving /api/radar through it",
			ownerKind, t.target))
		return t
	}

	appCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	since := options.SinceMs
	if since == 0 {
		since = cli.DefaultSinceMs
	}
	st := store.CreateStore(now)
	var hist *history.History
	var writer *history.HistoryWriter
	if opened, err := history.OpenHistory(paths.HistoryPath(appCtx, env), nil); err != nil {
		log(fmt.Sprintf("radar: history unavailable, running without it (%v)", err))
	} else {
		hist = opened
		writer = history.AttachHistoryWriter(appCtx, history.HistoryWriterOptions{Store: st, History: hist})
	}
	if hist != nil {
		// the retention the user chose, read fresh on every prune so a change takes the next beat
		retention := func(ctx context.Context) int {
			return settings.ReadSettings(ctx, env).HistoryRetentionDays
		}
		t.prunes = history.SchedulePrunes(appCtx, hist, retention, history.PruneOptions{})
	}
	watcher := ingest.StartWatcher(appCtx, ingest.WatcherOptions{
		Env:        env,
		Store:      st,
		SinceMs:    since,
		IntervalMs: options.IntervalMs,
		Now:        func() int64 { return now().UnixMilli() },
		Log:        log,
		HarnessCollectors: []ingest.HarnessCollector{
			ingest.NewCodexCollector(env),
			ingest.NewOpencodeCollector(env),
		},
	})
	tickMs := options.InsightsTickMs
	if tickMs == 0 {
		tickMs = server.DefaultTickMs
	}
	insights := server.CreateInsights(appCtx, server.InsightsOptions{
		Env:     env,
		Store:   st,
		Now:     now,
		TickMs:  tickMs,
		Runner:  options.NotifyRunner,
		Watcher: watcher,
	})
	startedAt := now().UnixMilli()
	t.Store = st
	t.Watcher = watcher
	t.Insights = insights
	t.Writer = writer
	t.Owns = true
	// the token a proxying huddle presents on this mount's guarded routes: huddle's own members
	// live only in their owner's memory, so the lock record is the one ground two huddles share
	t.token = auth.Rand36(32)
	t.httpServer = server.CreateHttpServer(server.HttpOptions{
		Store:         st,
		Version:       options.Version,
		StartedAt:     startedAt,
		Port:          options.Port,
		Insights:      insights,
		Quotas:        server.CreateQuotas(env),
		History:       hist,
		SelfCheck:     server.NewSelfCheck(hist, ingest.TranscriptRoots(appCtx, env), now),
		Watcher:       watcher,
		HostAllowed:   options.HostAllowed,
		OriginAllowed: options.OriginAllowed,
	})
	mux.HandleFunc(Prefix, t.serveRadar)
	mux.HandleFunc(Prefix+"/", t.serveRadar)
	mux.HandleFunc(healthPath, t.serveHealth)
	// the lock, taken the way radar's own app takes it: this process's pid, the port the
	// answers live on, and the tab's token. The port preference file stays radar's own — huddle
	// binds no radar port, and overwriting it would move the standalone plugin's bookmark.
	// radar's command line probes this record's port at /api/health, so that path must answer.
	server.WriteServerInfo(ctx, env, server.ServerInfo{
		Pid:       int64(os.Getpid()),
		Port:      options.Port,
		URL:       fmt.Sprintf("http://127.0.0.1:%d", options.Port),
		StartedAt: float64(startedAt),
		Token:     t.token,
	})
	return t
}

// recordedOwner is the live owner radar's lock names, when there is one: a recorded pid that is
// still alive and a port that answers radar's health path. A standalone radar serves its routes
// at the root; a huddle serves them under the prefix like this mount does, behind the token its
// record publishes.
type recordedOwner struct {
	Port         int
	ServesPrefix bool
	Token        string
}

// findRecordedOwner reads the lock and probes it; false when no owner answers.
func findRecordedOwner(ctx context.Context, env domain.EnvLookup, client *http.Client) (*recordedOwner, bool) {
	info := server.ReadServerInfo(ctx, env)
	if info == nil || !process.IsAlive(info.Pid) {
		return nil, false
	}
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}
	if !healthAnswers(ctx, client, info.Port, healthPath, "") {
		return nil, false
	}
	// the prefix probe carries the record's token: another huddle's /api/radar answers only a
	// credentialed request, and a 401 there must not read as a standalone radar at the root
	return &recordedOwner{
		Port:         info.Port,
		ServesPrefix: healthAnswers(ctx, client, info.Port, Prefix+healthPath, info.Token),
		Token:        info.Token,
	}, true
}

// healthAnswers says whether one port answers a health path with the JSON object radar's probe
// reads: any 2xx JSON object, the same reading radar's own fetch makes. A token goes out as the
// credential header the owner's guard reads.
func healthAnswers(ctx context.Context, client *http.Client, port int, path, token string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return false
	}
	if token != "" {
		request.Header.Set(auth.Header, token)
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, response.Body)
		return false
	}
	var body map[string]any
	return json.NewDecoder(response.Body).Decode(&body) == nil
}

// proxyTo builds the reverse proxy onto one recorded owner. A standalone radar takes radar's
// own path (the prefix stripped); an owner serving the prefix (another huddle) takes the path as
// it came. The outbound Host names the target, the only host radar's guard answers. The
// credential header names this mount, not the owner, so it is swapped for the token the owner's
// record published — or dropped, never forwarded.
func proxyTo(target string, keepPrefix bool, token string) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: "http", Host: target})
			if token != "" {
				pr.Out.Header.Set(auth.Header, token)
			} else {
				pr.Out.Header.Del(auth.Header)
			}
			if keepPrefix {
				return
			}
			escaped := pr.Out.URL.EscapedPath()
			stripped := strings.TrimPrefix(escaped, Prefix)
			if stripped == "" {
				stripped = "/"
			}
			// a re-parse keeps the escapes exactly as they came: a %2F in a route id must
			// survive to the target's own EscapedPath read
			parsed, err := url.ParseRequestURI(stripped)
			if err != nil {
				pr.Out.URL.Path = stripped
				pr.Out.URL.RawPath = ""
				return
			}
			pr.Out.URL.Path = parsed.Path
			pr.Out.URL.RawPath = parsed.RawPath
		},
	}
}

// serveProxy forwards one /api/radar request to the recorded owner.
func (t *Tab) serveProxy(w http.ResponseWriter, r *http.Request) {
	t.proxy.ServeHTTP(w, r)
}

// serveRadar answers one /api/radar request through radar's own router, the prefix stripped.
func (t *Tab) serveRadar(w http.ResponseWriter, r *http.Request) {
	stripped, ok := stripPrefix(r.URL)
	if !ok {
		http.NotFound(w, r)
		return
	}
	forwarded := r.Clone(r.Context())
	forwarded.URL = stripped
	t.httpServer.Server.Handler.ServeHTTP(w, forwarded)
}

// serveHealth answers radar's health path on huddle's port, the answer radar's own probe reads.
func (t *Tab) serveHealth(w http.ResponseWriter, r *http.Request) {
	t.httpServer.Server.Handler.ServeHTTP(w, r)
}

// stripPrefix lifts /api/radar off one request URL, keeping the escapes byte for byte: radar's
// router reads the escaped path, and a %2F inside a history node id must stay a %2F.
func stripPrefix(original *url.URL) (*url.URL, bool) {
	escaped := original.EscapedPath()
	target := ""
	switch {
	case escaped == Prefix:
		target = "/"
	case strings.HasPrefix(escaped, Prefix+"/"):
		target = escaped[len(Prefix):]
	default:
		return nil, false
	}
	parsed, err := url.ParseRequestURI(target)
	if err != nil {
		return nil, false
	}
	copied := *original
	copied.Path = parsed.Path
	copied.RawPath = parsed.RawPath
	return &copied, true
}

// RecordHook writes one Claude Code hook payload as a radar spool line, in-process — the same
// record the radar plugin's own hook writes, so both may run: huddle's hook calls this, and the
// plugin's hooks, when still installed, append the same line. Radar's ingest lands the
// id-addressed records once (a session by its id, a tool call by its tool use id); the timeline
// keeps every line it read.
func RecordHook(ctx context.Context, env domain.EnvLookup, environ []string, raw string) (string, bool) {
	io := hook.MakeIo(append(environOf(env), environ...), time.Now, int64(os.Getpid()), int64(os.Getppid()))
	return hook.RunHook(ctx, raw, &io)
}

// RecordHook writes one hook payload through this mount: only a mount that owns the ingest
// records, because a proxying mount's owner has its own hook writing the same line.
func (t *Tab) RecordHook(ctx context.Context, raw string) (string, bool) {
	if !t.Owns {
		return "", false
	}
	return RecordHook(ctx, t.env, nil, raw)
}

// environOf renders one lookup as ordered entries, so a caller that holds only the lookup still
// sees its keys in the hook's line.
func environOf(env domain.EnvLookup) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

// RadarAnswer reads one radar API path in-process — the source the registry's cost and provider
// fields and the channel views read, with no socket between huddle and radar. A streaming path
// never answers here: its response does not end.
func (t *Tab) RadarAnswer(ctx context.Context, path string) ([]byte, bool) {
	if strings.HasPrefix(path, "/api/stream") {
		return nil, false
	}
	if t.Owns {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, false
		}
		request.Host = fmt.Sprintf("127.0.0.1:%d", t.port)
		recorded := httptest.NewRecorder()
		t.httpServer.Server.Handler.ServeHTTP(recorded, request)
		if recorded.Code != 200 || recorded.Body == nil {
			return nil, false
		}
		body := recorded.Body.Bytes()
		if !json.Valid(body) {
			return nil, false
		}
		return body, true
	}
	// proxying mode: the recorded owner answers. Another huddle serves radar under /api/radar
	// behind its credential, so the unprefixed path the callers use takes the prefix and the
	// request carries the token the owner's record published; a standalone radar serves at the
	// root and reads no credential.
	target := path
	if t.keepPrefix {
		target = Prefix + path
	} else {
		target = strings.TrimPrefix(path, Prefix)
		if target == "" {
			target = "/"
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+t.target+target, nil)
	if err != nil {
		return nil, false
	}
	if t.token != "" {
		request.Header.Set(auth.Header, t.token)
	}
	client := t.client
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil || !json.Valid(body) {
		return nil, false
	}
	return body, true
}

// AnswersToken says whether the request carries the tab token this mount published in its lock
// record — the one credential a proxying huddle can hold, since huddle's own members live only
// in their owner's memory. A mount with no token answers no one.
func (t *Tab) AnswersToken(given string) bool {
	if t.token == "" || given == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(t.token)) == 1
}

// SessionFacts is one session's cost and provider as the mounted tab knows them: the two
// fields the registry's rows carry, nil when radar has no answer for them.
type SessionFacts struct {
	CostUSD  *float64
	Provider *string
}

// FactsOf reads one session's cost and provider through the tab's own API, in-process — the
// same answer the dashboard reads. The cost is the sum over the session's agents, so a session
// whose requests are all unpriced answers nil; the provider comes from the session's model, so
// a session with no model answers nil.
func (t *Tab) FactsOf(ctx context.Context, id string) SessionFacts {
	body, ok := t.RadarAnswer(ctx, "/api/sessions/"+url.PathEscape(id))
	if !ok {
		return SessionFacts{}
	}
	var answer struct {
		Session *struct {
			Model  *string `json:"model"`
			Agents []struct {
				CostUsd *float64 `json:"costUsd"`
			} `json:"agents"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &answer); err != nil || answer.Session == nil {
		return SessionFacts{}
	}
	var facts SessionFacts
	for _, agent := range answer.Session.Agents {
		if agent.CostUsd == nil {
			continue
		}
		if facts.CostUSD == nil {
			cost := *agent.CostUsd
			facts.CostUSD = &cost
			continue
		}
		*facts.CostUSD += *agent.CostUsd
	}
	if model := answer.Session.Model; model != nil && *model != "" {
		provider := shared.ProviderOf(*model)
		facts.Provider = &provider
	}
	return facts
}

// Stop ends the mount. The watcher, the insights, the retention and the history writer stop, the
// streams radar holds close, and the lock goes — but only while the record on disk is still this
// mount's own: a successor that already took the lock keeps it. Idempotent.
func (t *Tab) Stop() {
	t.stopOnce.Do(func() {
		defer close(t.stopped)
		if !t.Owns {
			return
		}
		t.Watcher.Stop()
		t.Insights.Stop()
		t.prunes.Stop()
		if t.Writer != nil {
			t.Writer.Stop()
		}
		if t.cancel != nil {
			t.cancel()
		}
		t.httpServer.WhenIdle(context.Background(), 2_000)
		t.httpServer.CloseStreams()
		releaseLock(t.env, t.port)
	})
}

// releaseLock removes the lock when it still names this process and port; a record a successor
// already rewrote is not ours to remove.
func releaseLock(env domain.EnvLookup, port int) {
	ctx := context.Background()
	info := server.ReadServerInfo(ctx, env)
	if info == nil || info.Pid != int64(os.Getpid()) || info.Port != port {
		return
	}
	server.RemoveServerInfo(ctx, env)
}
