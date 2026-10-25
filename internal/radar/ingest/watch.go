// The ingest loop: every tick it tails the spool (hook lines), the transcripts referenced there
// plus anything recent under the projects dir, the provider plugins' job attempts and their real
// transcripts, feeding the store. New files come in on a slower rescan cadence. A pass runs in
// ~20 ms slices with the lock released between them, so a large first catch-up blocks nothing
// for long and a stop lands within one slice. Failures are contained per file — one unreadable
// transcript never stops the rest.
package ingest

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/routerhealth"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// Slice is how long one slice of a pass may run before the watcher yields: a stop or another
// caller lands between slices, however large the first catch-up.
const Slice = 20 * time.Millisecond

// DefaultIntervalMs is the poll cadence a watcher without an explicit interval runs on.
const DefaultIntervalMs int64 = 1_000

// SpoolWindowDays is how many days of spool files one pass lists.
const SpoolWindowDays = 7

// maxWalkDepth is how deep under the projects dir the transcript walk goes.
const maxWalkDepth = 6

// listingSettle is how long after a directory's last change its listing must have been read
// before the next walk trusts it: a filesystem with coarse timestamps can stamp a change made
// just after the read with the same modified time.
const listingSettle = 2 * time.Second

// idleCompact is how long a transcript tail may sit without new bytes before its parser's
// streamed windows are dropped: the file sits at its end, so no window can grow back, and
// a new byte re-arms it on the read that brings it.
const idleCompact = 10 * time.Minute

// WatcherOptions configures the ingest loop. Now is the clock the cadences and windows read
// (epoch ms); Alive decides who counts as a living pid in the session registry; NewTicker builds
// the poll clock, so tests drive the interval by hand. Log receives the one line a failing
// harness collector leaves (stderr when nil).
type WatcherOptions struct {
	Env        domain.EnvLookup
	Store      *store.Store
	SinceMs    int64
	IntervalMs int64
	RescanMs   int64
	Now        func() int64
	Alive      PidAlive
	NewTicker  func(interval time.Duration) (<-chan time.Time, func())
	// HarnessCollectors are the other agents' transcript collectors (Codex, opencode) one
	// rescanning pass runs after the jobs; nil keeps radar on Claude transcripts alone.
	HarnessCollectors []HarnessCollector
	// Ledger is the request ledger the route lines and the transcript requests file
	// themselves into; nil keeps the watcher without a ledger.
	Ledger *ledger.Store
	Log    func(line string)
}

// fileTail is one transcript file's read position and its parser.
type fileTail struct {
	path     string
	tail     TailState
	parser   *TranscriptState
	identity FileIdentity
	// lastGrowthMs is when the tail last read new bytes: idle compaction counts from it.
	lastGrowthMs int64
	// compacted is that the parser's windows were emptied since the last growth.
	compacted bool
}

// upstreamAt is one base URL a session's hooks reported, at the moment they reported it.
type upstreamAt struct {
	ts  int64
	url string
}

// pathSet is a set of paths in first-addition order.
type pathSet struct {
	order []string
	has   map[string]struct{}
}

func (s *pathSet) add(path string) {
	if s.has == nil {
		s.has = make(map[string]struct{})
	}
	if _, ok := s.has[path]; ok {
		return
	}
	s.has[path] = struct{}{}
	s.order = append(s.order, path)
}

func (s *pathSet) remove(path string) {
	if _, ok := s.has[path]; !ok {
		return
	}
	delete(s.has, path)
	for at, held := range s.order {
		if held == path {
			s.order = append(s.order[:at], s.order[at+1:]...)
			return
		}
	}
}

func (s *pathSet) snapshot() []string { return slices.Clone(s.order) }

// tailSet is the watched files in first-discovery order — the order the pass reads them in. A Go
// map iterates in no order; the discovery order is the read order.
type tailSet struct {
	order []string
	files map[string]*fileTail
}

func (t *tailSet) has(path string) bool {
	_, ok := t.files[path]
	return ok
}

func (t *tailSet) set(path string, tail *fileTail) {
	if t.files == nil {
		t.files = make(map[string]*fileTail)
	}
	if _, ok := t.files[path]; ok {
		return
	}
	t.files[path] = tail
	t.order = append(t.order, path)
}

func (t *tailSet) delete(path string) {
	if _, ok := t.files[path]; !ok {
		return
	}
	delete(t.files, path)
	for at, held := range t.order {
		if held == path {
			t.order = append(t.order[:at], t.order[at+1:]...)
			return
		}
	}
}

func (t *tailSet) size() int { return len(t.files) }

func (t *tailSet) snapshot() []*fileTail {
	out := make([]*fileTail, 0, len(t.order))
	for _, path := range t.order {
		if tail, ok := t.files[path]; ok {
			out = append(out, tail)
		}
	}
	return out
}

// Watcher is the running ingest loop: one goroutine on a poll ticker, one pass at a time, every
// pass a walk over the spool, the registry, the transcripts and the provider jobs.
type Watcher struct {
	ctx       context.Context
	env       domain.EnvLookup
	store     *store.Store
	ledger    *ledger.Store
	nowFn     func() int64
	alive     PidAlive
	sinceMs   int64
	rescanMs  int64
	newTicker func(interval time.Duration) (<-chan time.Time, func())
	log       func(line string)

	// harnessCollectors are the other agents' transcript stores, each read once per
	// rescanning pass; harnessMetas remembers the cwd each harness session last filed, so
	// a quiet collector never re-upserts its sessions.
	harnessCollectors []HarnessCollector
	harnessMetas      map[string]string

	mu      sync.Mutex
	stopped bool
	queued  bool
	pass    *passWork

	spoolTails      map[string]*TailState
	transcriptTails tailSet
	jobTails        tailSet
	// sessionUpstreams is per session every base URL its hooks reported, oldest first: a session
	// can start direct and be routed later (or the reverse), so a request takes the URL in
	// effect when it was made.
	sessionUpstreams map[string][]upstreamAt
	// routerUpstreams is per provider plugin the base URL its router sent that plugin's models
	// to, learned from route lines: a job's own calls name no upstream, so its plugin's is all
	// radar can tell about where they went.
	routerUpstreams       map[string]string
	agentMetas            map[string]*AgentMeta
	referencedTranscripts pathSet
	livePaths             map[string]struct{}
	registryTranscripts   map[string]string
	lastRescan            int64
	// listings are the projects walk's directory listings from its last full walk: a
	// directory whose modified time stands is not read again, so a quiet history of
	// thousands of session folders costs one stat each per rescan.
	listings map[string]dirListing

	cancel func()
	done   chan struct{}
}

// StartWatcher starts the ingest loop. The first pass is queued before the goroutine starts, so
// CatchingUp is true the moment StartWatcher returns.
func StartWatcher(ctx context.Context, options WatcherOptions) *Watcher {
	interval := options.IntervalMs
	if interval <= 0 {
		interval = DefaultIntervalMs
	}
	rescan := options.RescanMs
	if rescan <= 0 {
		rescan = RegistryRescanMs
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	newTicker := options.NewTicker
	if newTicker == nil {
		newTicker = func(d time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(d)
			return ticker.C, ticker.Stop
		}
	}
	log := options.Log
	if log == nil {
		log = func(line string) { fmt.Fprintln(os.Stderr, line) }
	}
	w := &Watcher{
		ctx:                 ctx,
		env:                 options.Env,
		store:               options.Store,
		ledger:              options.Ledger,
		nowFn:               now,
		alive:               options.Alive,
		sinceMs:             options.SinceMs,
		rescanMs:            rescan,
		newTicker:           newTicker,
		log:                 log,
		harnessCollectors:   options.HarnessCollectors,
		harnessMetas:        make(map[string]string),
		spoolTails:          make(map[string]*TailState),
		sessionUpstreams:    make(map[string][]upstreamAt),
		routerUpstreams:     make(map[string]string),
		agentMetas:          make(map[string]*AgentMeta),
		livePaths:           make(map[string]struct{}),
		registryTranscripts: make(map[string]string),
		done:                make(chan struct{}),
	}
	w.mu.Lock()
	w.queued = true
	w.mu.Unlock()
	loopCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	ticks, stopTicker := newTicker(time.Duration(interval) * time.Millisecond)
	go w.run(loopCtx, ticks, stopTicker)
	return w
}

// run is the loop's goroutine: the first pass starts now, not on the first tick.
func (w *Watcher) run(ctx context.Context, ticks <-chan time.Time, stopTicker func()) {
	defer close(w.done)
	defer stopTicker()
	w.PollOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			w.PollOnce()
		}
	}
}

// PollOnce asks for a poll and drives it one slice at a time, the lock free between slices. A
// poll asked for while a pass is running starts when that one ends, never during it.
func (w *Watcher) PollOnce() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.queued = true
	w.mu.Unlock()
	for {
		w.mu.Lock()
		if w.stopped {
			w.pass = nil
			w.mu.Unlock()
			return
		}
		if w.pass == nil {
			if !w.queued {
				w.mu.Unlock()
				return
			}
			w.queued = false
			w.pass = newPassWork(w)
		}
		finished := w.sliceStep()
		w.mu.Unlock()
		if finished {
			// the pass drained; a still-queued poll starts the next one
			w.mu.Lock()
			w.pass = nil
			w.mu.Unlock()
		}
	}
}

// sliceStep runs one slice of the running pass; a pass that dies mid-slice dies with its pass,
// never the watcher — the next poll retries every file.
func (w *Watcher) sliceStep() (finished bool) {
	defer func() {
		if recover() != nil {
			w.pass = nil
			finished = true
		}
	}()
	return !w.sliceLocked()
}

// sliceLocked runs the pass until the slice budget is out; true while work remains.
func (w *Watcher) sliceLocked() bool {
	start := time.Now()
	for time.Since(start) < Slice {
		if w.pass == nil || !w.pass.step() {
			return false
		}
	}
	return true
}

// Stop ends the loop: a pass in flight stops within a slice, the goroutine and its ticker go
// out, and Done is closed once nothing of the watcher runs any more.
func (w *Watcher) Stop() {
	w.mu.Lock()
	w.stopped = true
	w.pass = nil
	w.mu.Unlock()
	w.cancel()
	<-w.done
}

// Done closes once the watcher's goroutine is out.
func (w *Watcher) Done() <-chan struct{} { return w.done }

// CatchingUp is true while a pass is running or queued: the first ingest is still draining its
// backlog, so a dashboard that reconnects mid-way knows its lists are still filling in.
func (w *Watcher) CatchingUp() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pass != nil || w.queued
}

// Tracked is how many transcript files the watcher still tails: eviction keeps this at the live
// few, not every transcript ever seen.
func (w *Watcher) Tracked() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.transcriptTails.size()
}

func (w *Watcher) now() int64         { return w.nowFn() }
func (w *Watcher) sinceCutoff() int64 { return w.now() - w.sinceMs }

// jobUpstream is the upstream a provider's jobs went to: zai's is known outright, the rest only
// what a router taught.
func (w *Watcher) jobUpstream(plugin JobPlugin) string { return w.routerUpstreams[string(plugin)] }

// feedLine parses one transcript line of one tail into the store.
func (w *Watcher) feedLine(tail *fileTail, line string) {
	w.applyEmit(FeedTranscriptLine(tail.parser, tail.identity, line))
}

// applyEmit lands one transcript line's records: session meta, agents, tool calls, the failure
// side, then the requests with the input and output the line captured.
func (w *Watcher) applyEmit(emit TranscriptEmit) {
	if emit.Session != nil {
		w.applySessionMeta(*emit.Session)
	}
	for _, agent := range emit.Agents {
		w.upsertDescriptor(agent)
	}
	for _, call := range emit.ToolCalls {
		w.store.AddToolCall(call)
	}
	for _, failure := range emit.APIErrors {
		w.store.AddAPIError(failure)
	}
	for _, stop := range emit.Interrupts {
		w.store.AddSpoolLine(InterruptLine(stop))
	}
	for _, notice := range emit.Notices {
		w.store.AddSpoolLine(NoticeLine(notice))
	}
	captured := make(map[string]ContentCapture, len(emit.Content))
	for _, content := range emit.Content {
		captured[content.RequestID] = content
	}
	for _, request := range emit.Requests {
		w.store.AddRequest(w.defaultUpstream(w.enrichUpstream(request)), contentOf(captured, request.ID))
		w.fileTranscriptFacts(request)
	}
}

// applySessionMeta copies the naming data a transcript line carried onto the session.
func (w *Watcher) applySessionMeta(meta SessionMeta) {
	w.store.UpsertSession(store.SessionUpsert{
		ID:          meta.ID,
		Cwd:         meta.Cwd,
		CcVersion:   meta.CcVersion,
		CustomTitle: meta.CustomTitle,
		AgentName:   meta.AgentName,
		AiTitle:     meta.AITitle,
		Slug:        meta.Slug,
		Branch:      meta.Branch,
	})
}

// upsertDescriptor lands one transcript-sighted agent in the store; its prompt title rides only
// when the parser saw one.
func (w *Watcher) upsertDescriptor(agent AgentDescriptor) {
	kind := agent.Kind
	upsert := store.AgentUpsert{
		SessionID:   agent.SessionID,
		ID:          agent.ID,
		ParentID:    &agent.ParentID,
		Kind:        &kind,
		Name:        agent.Name,
		AgentType:   agent.AgentType,
		Description: agent.Description,
	}
	if agent.Title != nil {
		upsert.Title = &agent.Title
	}
	w.store.UpsertAgent(upsert)
}

// contentOf is a request's captured input and output, nil when none was kept.
func contentOf(captured map[string]ContentCapture, requestID string) *store.ContentPair {
	content, ok := captured[requestID]
	if !ok {
		return nil
	}
	return &store.ContentPair{Input: content.Input, Output: content.Output}
}

// enrichUpstream gives a transcript request the base URL in effect when it was made: the last
// one its session's hooks reported at or before it, one older than every report keeps the empty
// upstream. A local router is not where the call went: name it as the way, and let the router's
// own line say where.
func (w *Watcher) enrichUpstream(request shared.RequestRecord) shared.RequestRecord {
	known := ""
	found := false
	for _, entry := range w.sessionUpstreams[request.SessionID] {
		if entry.ts > request.Ts {
			break
		}
		known, found = entry.url, true
	}
	if !found {
		return request
	}
	if host, loopback := LoopbackHost(known); loopback {
		request.Via = &host
		if !slices.Contains(request.TailKeys, "via") {
			request.TailKeys = append(request.TailKeys, "via")
		}
		return request
	}
	request.Upstream = known
	return request
}

// defaultUpstream fills a claude-* call whose enrichment named no upstream: Claude Code called
// the API itself, or a local router passed it straight through (Via says which). A provider
// model keeps the empty upstream until the router's route line names where it actually went.
func (w *Watcher) defaultUpstream(request shared.RequestRecord) shared.RequestRecord {
	if request.Upstream != "" || !strings.HasPrefix(request.Model, "claude-") {
		return request
	}
	request.Upstream = shared.DefaultUpstream
	return request
}

// rememberUpstream records a base URL a session's hooks reported; a repeat of the newest one at
// or after its own moment changes nothing.
func (w *Watcher) rememberUpstream(sessionID string, ts int64, url string) {
	entries := w.sessionUpstreams[sessionID]
	if n := len(entries); n > 0 && entries[n-1].url == url && entries[n-1].ts <= ts {
		return
	}
	entries = append(entries, upstreamAt{ts: ts, url: url})
	slices.SortStableFunc(entries, func(a, b upstreamAt) int { return cmp.Compare(a.ts, b.ts) })
	w.sessionUpstreams[sessionID] = entries
}

// ingestSpoolText parses one spool line and lands it; an unparseable one is skipped.
func (w *Watcher) ingestSpoolText(text string) {
	if line, ok := ParseSpoolLine(text); ok {
		w.ingestSpoolLine(text, line)
	}
}

// ingestSpoolLine lands one spool line: the base URL and transcript a hook named, then the line
// itself by its kind — a route, a capture or a router health event never reaches the timeline.
// The line's own text rides along, so the route branch can hand the ledger the event as it
// was written.
func (w *Watcher) ingestSpoolLine(text string, line shared.SpoolLine) {
	if line.SessionID != nil && line.BaseURL != nil {
		if ts, ok := jsfmt.ParseISO(line.Ts); ok {
			w.rememberUpstream(*line.SessionID, ts.UnixMilli(), *line.BaseURL)
		}
	}
	if line.TranscriptPath != nil && *line.TranscriptPath != "" {
		w.referencedTranscripts.add(*line.TranscriptPath)
	}
	switch {
	case line.Event == "route":
		w.ingestRouteEvent(text, line)
	case line.Event == "capture":
		w.ingestCaptureEvent(line)
	case routerhealth.IsRouterEventLine(line):
		w.ingestRouterEvent(line)
	default:
		w.store.AddSpoolLine(line)
	}
}

// ingestCaptureEvent lands a capture line: the gzipped prompt a route line points at, handed to
// the history writer once per sighting.
func (w *Watcher) ingestCaptureEvent(line shared.SpoolLine) {
	ts, ok := jsfmt.ParseISO(line.Ts)
	if !ok || line.PromptHash == nil || *line.PromptHash == "" || line.Gz == nil {
		return
	}
	w.store.AddCapture(shared.CaptureRecord{Hash: *line.PromptHash, Gz: *line.Gz, FirstTs: ts.UnixMilli()})
}

// ingestRouterEvent lands a router health line: kept for the router view; a rate limit also
// feeds the retry-storm alert.
func (w *Watcher) ingestRouterEvent(line shared.SpoolLine) {
	event := routerhealth.ParseRouterEvent(line)
	if event == nil {
		return
	}
	w.store.AddRouterEvent(*event)
	if event.Event == shared.EventRateLimited {
		w.store.AddAPIError(shared.APIErrorRecord{
			Ts: event.Ts, Status: 429, Source: shared.SourceRouter, Plugin: &event.Plugin,
		})
	}
}

// ingestRouteEvent lands a route line: its retryable failure first, the unified limits it read
// from Anthropic's headers, then the request it stands for and whatever it taught about where a
// plugin's models go.
func (w *Watcher) ingestRouteEvent(text string, line shared.SpoolLine) {
	ts, ok := jsfmt.ParseISO(line.Ts)
	if !ok {
		return
	}
	if failure, failed := RouteFailure(line, ts.UnixMilli()); failed {
		w.store.AddAPIError(failure)
	}
	if line.Utilization != nil {
		w.store.NoteUtilization(ts.UnixMilli(), *line.Utilization)
	}
	if line.Model == nil {
		return
	}
	w.rememberRouterUpstream(line, *line.Model)
	w.store.AddRequest(RouteRequest(line, *line.Model, ts.UnixMilli()), nil)
	w.fileRouteFacts(text, line, *line.Model, ts.UnixMilli())
}

// fileRouteFacts hands one route line's facts to the ledger, so its row joins what the
// transcript says about the same request; a ledger miss is one log line, never a stop —
// ingest goes on.
func (w *Watcher) fileRouteFacts(text string, line shared.SpoolLine, model string, ts int64) {
	if w.ledger == nil {
		return
	}
	facts, ok := routeFactsOf(text, line, model, ts)
	if !ok {
		return
	}
	if err := w.ledger.AddRoute(w.ctx, facts); err != nil {
		w.log(fmt.Sprintf("radar: the ledger missed a route line (%v)", err))
	}
}

// fileTranscriptFacts hands one transcript request's facts to the ledger, so its row joins
// what the route line says about the same request; a ledger miss is one log line, never a
// stop — ingest goes on.
func (w *Watcher) fileTranscriptFacts(request shared.RequestRecord) {
	if w.ledger == nil {
		return
	}
	facts, ok := transcriptFactsOf(request)
	if !ok {
		return
	}
	if err := w.ledger.AddTranscript(w.ctx, facts); err != nil {
		w.log(fmt.Sprintf("radar: the ledger missed a transcript request (%v)", err))
	}
}

// rememberRouterUpstream learns a plugin's real base URL from a route line that sent one of the
// plugin's own models somewhere; a passthrough says nothing about it.
func (w *Watcher) rememberRouterUpstream(line shared.SpoolLine, model string) {
	plugin, isString := stringOfExtra(line, "plugin")
	upstream := ""
	if line.Upstream != nil {
		upstream = *line.Upstream
	}
	if !isString || plugin == "" || upstream == "" {
		return
	}
	if served, known := cost.PluginOf(model); !known || served != plugin {
		return
	}
	w.routerUpstreams[plugin] = upstream
}

// spoolTailOf is one spool file's tail, opened on first sight.
func (w *Watcher) spoolTailOf(path string) *TailState {
	if tail, ok := w.spoolTails[path]; ok {
		return tail
	}
	tail := OpenTail(path)
	w.spoolTails[path] = &tail
	return &tail
}

// transcriptTail is a session transcript's tail, with its subagent's meta file beside it when
// there is one.
func (w *Watcher) transcriptTail(path string) *fileTail {
	identity := IdentifyFile(path)
	if identity.Kind == shared.KindSubagent {
		if meta := w.agentMetaFor(path); meta != nil {
			identity.AgentMeta = meta
		}
	}
	return &fileTail{
		path: path, tail: OpenTail(path), parser: NewTranscriptState(), identity: identity,
		lastGrowthMs: w.now(), // a fresh tail is not ancient history: the idle mark starts now
	}
}

// registryTranscriptOf is a live registry session's transcript under the projects dir, since
// window aside — the registry's word is that this session is open. Its cwd names the slug dir
// Claude Code keeps transcripts in; a cwd the slug scheme does not fit falls back to searching
// for the file name.
func (w *Watcher) registryTranscriptOf(entry shared.RegistrySession) (string, bool) {
	if known, ok := w.registryTranscripts[entry.SessionID]; ok {
		return known, true
	}
	projects := filepath.Join(paths.ClaudeConfigDir(w.env), "projects")
	if entry.Cwd != nil {
		direct := filepath.Join(projects, ProjectDirOf(*entry.Cwd), entry.SessionID+".jsonl")
		if isFile(direct) {
			return direct, true
		}
	}
	if found := FindAttemptTranscript(projects, entry.SessionID); found != nil {
		return found.File, true
	}
	return "", false
}

// agentMetaFor reads the subagents/agent-<id>.meta.json beside a subagent transcript once, null
// cached like a value.
func (w *Watcher) agentMetaFor(transcript string) *AgentMeta {
	key := strings.TrimSuffix(transcript, ".jsonl") + ".meta.json"
	if meta, cached := w.agentMetas[key]; cached {
		return meta
	}
	meta := readAgentMeta(key)
	w.agentMetas[key] = meta
	return meta
}

// readAgentMeta reads a subagents meta file (agentType, description); nil when it is absent or
// says nothing usable.
func readAgentMeta(path string) *AgentMeta {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value, err := domain.ParseJSON(string(data))
	if err != nil {
		return nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	agentType, ok := record["agentType"].(string)
	if !ok || agentType == "" {
		return nil
	}
	description, _ := record["description"].(string)
	return &AgentMeta{AgentType: agentType, Description: description}
}

// applyJob lands one provider job: its pseudo session, its external agent, and its attempts.
func (w *Watcher) applyJob(job PluginJob) {
	upsert := store.SessionUpsert{ID: job.SessionID, External: ptrOf(true)}
	if job.Title != nil {
		upsert.CustomTitle = job.Title
	}
	if job.Branch != nil {
		upsert.Branch = job.Branch
	}
	if job.RepoRoot != nil {
		upsert.Repo = job.RepoRoot
	}
	if job.ParentSessionID != nil {
		upsert.ParentSessionID = job.ParentSessionID
	}
	w.store.UpsertSession(upsert)
	kind := shared.KindExternal
	w.store.UpsertAgent(store.AgentUpsert{
		SessionID: job.SessionID,
		ID:        job.ID,
		Kind:      &kind,
		Name:      &job.ID,
		Model:     job.Model,
		Title:     &job.Title,
	})
	for _, attempt := range job.Attempts {
		w.applyAttempt(job, attempt)
	}
}

// applyAttempt tails one attempt: its real transcript when the engine recorded a session for it
// (the stream file would count the same calls twice), else the stream file itself.
func (w *Watcher) applyAttempt(job PluginJob, attempt JobAttempt) {
	var found *AttemptTranscript
	if attempt.SessionID != nil {
		found = FindAttemptTranscript(JobClaudeHome(w.ctx, w.env, job.Plugin), *attempt.SessionID)
	}
	if found == nil {
		// an older job with no transcript: its stream file is all there is
		w.tailJobFile(attempt.File, FileIdentity{
			SessionID: job.SessionID,
			AgentID:   &job.ID,
			Kind:      shared.KindExternal,
			Upstream:  &job.Upstream,
		})
		return
	}
	w.jobTails.delete(attempt.File)
	remap := Remap{SessionID: job.SessionID, AgentID: job.ID}
	w.tailJobFile(found.File, FileIdentity{
		SessionID: job.SessionID,
		AgentID:   &job.ID,
		Kind:      shared.KindExternal,
		Upstream:  &job.Upstream,
		Remap:     &remap,
	})
	for _, file := range found.Subagents {
		agentID := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		subRemap := Remap{SessionID: job.SessionID, AgentID: job.ID, ParentID: &job.ID}
		identity := FileIdentity{
			SessionID: job.SessionID,
			AgentID:   &agentID,
			Kind:      shared.KindSubagent,
			Upstream:  &job.Upstream,
			Remap:     &subRemap,
		}
		if meta := w.agentMetaFor(file); meta != nil {
			identity.AgentMeta = meta
		}
		w.tailJobFile(file, identity)
	}
}

// tailJobFile watches one job file, whatever it is, under the identity given.
func (w *Watcher) tailJobFile(file string, identity FileIdentity) {
	if w.jobTails.has(file) {
		return
	}
	w.jobTails.set(file, &fileTail{
		path: file, tail: OpenTail(file), parser: NewTranscriptState(), identity: identity,
	})
}

// InterruptLine is the timeline line for a turn the user interrupted (Claude Code sends no Stop
// hook for one).
func InterruptLine(stop Interrupt) shared.SpoolLine {
	line := shared.SpoolLine{
		Ts:        jsfmt.ISOTime(time.UnixMilli(stop.Ts)),
		Event:     "Interrupted",
		SessionID: &stop.SessionID,
	}
	if stop.AgentID != "main" {
		line.AgentID = &stop.AgentID
	}
	return line
}

// NoticeLine is the timeline line for an error notice Claude Code wrote itself; the store counts
// it as an agent error.
func NoticeLine(notice Notice) shared.SpoolLine {
	line := shared.SpoolLine{
		Ts:        jsfmt.ISOTime(time.UnixMilli(notice.Ts)),
		Event:     shared.NoticeEvent,
		SessionID: &notice.SessionID,
	}
	if notice.AgentID != "main" {
		line.AgentID = &notice.AgentID
	}
	message := "Claude Code notice: " + notice.What
	line.Message = &message
	return line
}

// RouteFailure reads a route line that came back 429/5xx as a retryable failure; ok is false for
// anything else.
func RouteFailure(line shared.SpoolLine, ts int64) (shared.APIErrorRecord, bool) {
	status, _ := statusOf(line)
	code, ok := jsNumberOf(status)
	if !ok || !(code == 429 || (code >= 500 && code <= 599)) {
		return shared.APIErrorRecord{}, false
	}
	var plugin *string
	if name, isString := stringOfExtra(line, "plugin"); isString {
		plugin = &name
	} else if line.Model != nil {
		if served, known := cost.PluginOf(*line.Model); known {
			plugin = &served
		}
	}
	return shared.APIErrorRecord{
		Ts:        ts,
		SessionID: line.SessionID,
		AgentID:   line.AgentID,
		Status:    int64(code),
		Source:    shared.SourceRoute,
		Plugin:    plugin,
	}, true
}

// statusOf is the status a route line named, as the string a template literal would print it:
// the routers write numbers, which the typed field reads as absent while the payload keeps
// them as read — and EncodeJSON prints a number exactly as String(n) does.
func statusOf(line shared.SpoolLine) (string, bool) {
	if line.Status != nil {
		return *line.Status, true
	}
	raw := line.Field("status")
	switch {
	case raw == nil, raw.Kind() == jsfmt.KindNull:
		return "", false
	case raw.Kind() == jsfmt.KindString:
		return raw.Text(), true
	case raw.Kind() == jsfmt.KindNumber, raw.Kind() == jsfmt.KindBool:
		return raw.EncodeJSON(), true
	}
	return "", false
}

// routeStatus reads a failed status as the request's stop reason; a success leaves the stop
// reason to the transcript.
func routeStatus(status string, present bool) *string {
	if !present {
		return nil
	}
	if code, ok := jsNumberOf(status); ok && code >= 200 && code < 300 {
		return nil
	}
	return &status
}

// jsNumberOf reads text as JavaScript's Number(): "" is 0, a decimal literal is its value,
// anything else is not a number.
func jsNumberOf(text string) (float64, bool) {
	trimmed := strings.TrimFunc(text, isJSSpace)
	if trimmed == "" {
		return 0, true
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

// routedProvider is the provider a routed model belongs to; a model id the table does not know
// takes its plugin's name.
func routedProvider(model string, line shared.SpoolLine) string {
	known := shared.ProviderOf(model)
	if known != "other" {
		return known
	}
	if plugin, isString := stringOfExtra(line, "plugin"); isString {
		return shared.PluginLabel(plugin)
	}
	return known
}

// routerOf is the router that wrote a route line: its plugin, or just "router" when the line
// does not say.
func routerOf(line shared.SpoolLine) string {
	if plugin, isString := stringOfExtra(line, "plugin"); isString && plugin != "" {
		return plugin
	}
	return "router"
}

// capturedHeader is the response headers a route line may carry, case-insensitively. The router
// already keeps only these; the spool is data, so the allow-list is enforced again here,
// whatever wrote the line.
var capturedHeader = regexp.MustCompile(`(?i)^(request-id|x-request-id|retry-after|content-type|anthropic-ratelimit-.+|x-ratelimit-.+)$`)

// headersOf is the allow-listed response headers a route line carries, as the pairs in
// the order the line carried them; nil when none survived.
func headersOf(line shared.SpoolLine) *shared.Headers {
	var out shared.Headers
	if line.Headers != nil {
		for _, pair := range *line.Headers {
			if capturedHeader.MatchString(pair.Name) {
				out = append(out, pair)
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return &out
}

// RouteRequest is a router's route line as a request record. The store pairs it with the
// transcript's copy of the same call when there is one; an untagged line (agentId "") takes the
// agent of that copy, else counts under the main agent, and a line with no session counts
// outside any session.
func RouteRequest(line shared.SpoolLine, model string, ts int64) shared.RequestRecord {
	sessionID := shared.OutsideSession
	if line.SessionID != nil {
		sessionID = *line.SessionID
	}
	agentID := ""
	if line.AgentID != nil {
		agentID = *line.AgentID
	}
	upstream := shared.DefaultUpstream
	if line.Upstream != nil {
		upstream = *line.Upstream
	}
	route, isRoute := stringOfExtra(line, "route")
	if !isRoute {
		route = "router"
	}
	via := routerOf(line)
	status, statusPresent := statusOf(line)
	// the agent and model sit in the id so parallel subagent calls that finish in the same
	// millisecond stay the separate records they were
	record := shared.RequestRecord{
		ID:         fmt.Sprintf("route:%s:%s:%s:%d:%s", sessionID, agentID, model, ts, status),
		SessionID:  sessionID,
		AgentID:    agentID,
		Model:      model,
		Upstream:   upstream,
		Ts:         ts,
		LatencyMs:  line.LatencyMs,
		TtfbMs:     line.TtfbMs,
		TtftMs:     line.TtftMs,
		Tokens:     usageOfSpool(line.Usage),
		StopReason: routeStatus(status, statusPresent),
		Provider:   routedProvider(model, line),
		Route:      &route,
		Via:        &via,
	}
	if statusPresent {
		record.Status = &status
	}
	// the optional fields a route line may carry, each on the record only when the line said (an
	// empty string counts as saying nothing)
	if line.Error != nil && *line.Error != "" {
		record.Error = line.Error
		record.EarlyKeys = append(record.EarlyKeys, "error")
	}
	if line.ParentAgentID != nil && *line.ParentAgentID != "" {
		record.ParentAgentID = line.ParentAgentID
		record.EarlyKeys = append(record.EarlyKeys, "parentAgentId")
	}
	if line.Fallback != nil && *line.Fallback != "" {
		record.Fallback = line.Fallback
		record.EarlyKeys = append(record.EarlyKeys, "fallback")
	}
	if line.PromptHash != nil && *line.PromptHash != "" {
		record.PromptHash = line.PromptHash
		record.EarlyKeys = append(record.EarlyKeys, "promptHash")
	}
	if headers := headersOf(line); headers != nil {
		record.Headers = headers
		record.EarlyKeys = append(record.EarlyKeys, "headers")
		record.RequestID, _ = headers.Get("request-id")
	}
	applySpoolBilling(&record, line.Usage)
	record.TailKeys = []string{"status", "route", "via"}
	return record
}

// usageOfSpool reads the usage block a route line carries with the same reading the transcript
// path gives its own: the counters and the thinking share of the output.
func usageOfSpool(usage *shared.SpoolUsage) shared.Tokens {
	if usage == nil {
		return shared.ZeroTokens
	}
	tokens := shared.Tokens{
		Input:      wholeOf(usage.InputTokens),
		Output:     wholeOf(usage.OutputTokens),
		CacheRead:  wholeOf(usage.CacheReadInputTokens),
		CacheWrite: wholeOf(usage.CacheCreationInputTokens),
	}
	if details, ok := objectOfRaw(usage.Extra["output_tokens_details"]); ok {
		if thinking, ok := positiveWhole(details, "thinking_tokens"); ok {
			tokens.Thinking = &thinking
		}
	}
	return tokens
}

// applySpoolBilling fills the billing conditions the usage block names: the 1-hour cache writes,
// fast mode, geo and service tier.
func applySpoolBilling(record *shared.RequestRecord, usage *shared.SpoolUsage) {
	if usage == nil {
		return
	}
	if creation, ok := objectOfRaw(usage.Extra["cache_creation"]); ok {
		if oneHour, ok := positiveWhole(creation, "ephemeral_1h_input_tokens"); ok {
			record.CacheWrite1h = &oneHour
		}
	}
	if word, ok := wordOfRaw(usage.Extra["speed"]); ok {
		record.Speed = &word
	}
	if word, ok := wordOfRaw(usage.Extra["inference_geo"]); ok {
		record.Geo = &word
	}
	if word, ok := wordOfRaw(usage.Extra["service_tier"]); ok {
		record.ServiceTier = &word
	}
}

// wholeOf reads an optional counter as a plain number.
func wholeOf(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// objectOfRaw decodes a raw JSON value as an object; a non-object is nothing.
func objectOfRaw(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, false
	}
	return object, true
}

// positiveWhole reads one of an object's fields as a whole number above zero, the only value a
// counter or a billing condition ever holds.
func positiveWhole(object map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := object[key]
	if !ok {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || value <= 0 || value != math.Trunc(value) {
		return 0, false
	}
	return int64(value), true
}

// wordOfRaw reads a raw JSON value as a non-empty string.
func wordOfRaw(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var word string
	if err := json.Unmarshal(raw, &word); err != nil || word == "" {
		return "", false
	}
	return word, true
}

// stringOfExtra reads one of a spool line's undeclared fields as a string; ok is false for an
// absent field or another shape.
func stringOfExtra(line shared.SpoolLine, key string) (string, bool) {
	raw, ok := line.Extra[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// LoopbackHost is the host:port of a base URL on this machine (a provider plugin's router);
// false for anything else. The host lowercases and loses its default port, as a URL parses it.
func LoopbackHost(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname != "127.0.0.1" && hostname != "localhost" && hostname != "::1" {
		return "", false
	}
	host := strings.ToLower(parsed.Host)
	if port := defaultPortOf(parsed.Scheme); port != "" {
		host = strings.TrimSuffix(host, ":"+port)
	}
	return host, true
}

// defaultPortOf is the port a URL's host does not name: 80 for http, 443 for https.
func defaultPortOf(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// ProjectDirOf is the project dir name Claude Code keeps a cwd's transcripts under: every
// separator and punctuation character becomes a dash (`/w/app` → `-w-app`, `.agents` →
// `-agents`).
func ProjectDirOf(cwd string) string {
	var out strings.Builder
	for _, r := range cwd {
		if isAlnumOrDash(r) {
			out.WriteRune(r)
			continue
		}
		out.WriteByte('-')
	}
	return out.String()
}

// isAlnumOrDash says r survives the slug: ASCII letters, digits and the dash.
func isAlnumOrDash(r rune) bool {
	switch {
	case r == '-', r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		return true
	}
	return false
}

// The pass phases, in run order: the registry before the transcripts, so a session it brings in
// has its transcript read this pass.
const (
	phaseSpool = iota
	phaseRegistry
	phaseDiscovery
	phaseTailing
	phaseEviction
	phaseJobs
	phaseHarness
	phaseDone
)

// passWork is one pass over every source as resumable work: one spool line, one tail opening or
// one registry entry a step, so a slice boundary can fall anywhere.
type passWork struct {
	w      *Watcher
	rescan bool
	phase  int

	spoolListed bool
	spoolFiles  []string
	spoolAt     int
	spoolLines  []string
	spoolLineAt int

	registryRead bool
	registry     []shared.RegistrySession
	registryAt   int
	entryListed  bool
	entryPaths   []string
	entryPathAt  int

	referencedDone bool
	discovered     []string
	discoverAt     int
	walkStarted    bool
	walk           []walkFrame
	walked         map[string]dirListing

	tailsSnapshotted bool
	tailSnapshot     []*fileTail
	tailAt           int
	tailLines        []string
	tailLineAt       int
	curTail          *fileTail

	evictSnapshotted bool
	evictPaths       []string
	evictAt          int

	jobsRead            bool
	jobs                []PluginJob
	jobAt               int
	jobTailsSnapshotted bool
	jobTailSnapshot     []*fileTail
	jobTailAt           int
	jobLines            []string
	jobLineAt           int
	curJobTail          *fileTail
	retired             bool

	harnessRead    bool
	harnessRecords []shared.RequestRecord
	harnessAt      int
}

// walkFrame is one directory of the projects walk: its fresh files, then its subdirectories,
// each newest-modified first.
type walkFrame struct {
	depth  int
	files  []string
	fileAt int
	dirs   []walkDir
	dirAt  int
}

// walkDir is a subdirectory the walk may enter, with its modified time for the order.
type walkDir struct {
	path    string
	mtimeMs int64
}

// dirListing is one directory's entries as a walk read them: its .jsonl files and its
// subdirectories, and the directory's modified time and the wall time of that read. Adding,
// removing or renaming an entry moves the modified time, so a listing stands while it holds.
type dirListing struct {
	mtimeMs int64
	readMs  int64
	files   []string
	dirs    []string
}

// newPassWork opens a pass, deciding whether this one rescans.
func newPassWork(w *Watcher) *passWork {
	rescan := w.lastRescan == 0 || w.now()-w.lastRescan >= w.rescanMs
	if rescan {
		w.lastRescan = w.now()
	}
	return &passWork{w: w, rescan: rescan}
}

// step runs one unit of work; false when the pass is complete.
func (p *passWork) step() bool {
	for p.phase != phaseDone {
		if p.stepPhase() {
			return true
		}
		p.phase++
	}
	return false
}

func (p *passWork) stepPhase() bool {
	switch p.phase {
	case phaseSpool:
		return p.stepSpool()
	case phaseRegistry:
		return p.stepRegistry()
	case phaseDiscovery:
		return p.stepDiscovery()
	case phaseTailing:
		return p.stepTailing()
	case phaseEviction:
		return p.stepEviction()
	case phaseJobs:
		return p.stepJobs()
	case phaseHarness:
		return p.stepHarness()
	}
	return false
}

// stepSpool tails the spool files, feeding one new line a step.
func (p *passWork) stepSpool() bool {
	if !p.spoolListed {
		p.spoolListed = true
		p.spoolFiles = ListSpoolFiles(p.w.ctx, p.w.env, p.w.now(), SpoolWindowDays)
	}
	if p.spoolLineAt < len(p.spoolLines) {
		line := p.spoolLines[p.spoolLineAt]
		p.spoolLineAt++
		p.w.ingestSpoolText(line)
		return true
	}
	for p.spoolAt < len(p.spoolFiles) {
		path := p.spoolFiles[p.spoolAt]
		p.spoolAt++
		result := TailFile(p.w.spoolTailOf(path), TailChunkBytes)
		if len(result.Lines) == 0 {
			continue
		}
		p.spoolLines = result.Lines
		p.spoolLineAt = 0
		return true
	}
	return false
}

// stepRegistry reads Claude Code's own registry on the rescan cadence — which sessions are live
// and how they are — and brings each one's transcripts into the tails however old they are. The
// subagents are re-listed on every rescan: a subagent the session spawns after we first saw it
// must be picked up whatever the since window says.
func (p *passWork) stepRegistry() bool {
	if !p.rescan {
		return false
	}
	if !p.registryRead {
		p.registryRead = true
		p.w.livePaths = make(map[string]struct{})
		p.registry = ReadRegistry(p.w.env, p.w.alive)
		p.w.store.ApplyRegistry(p.registry, p.w.now())
		return true
	}
	for p.registryAt < len(p.registry) {
		entry := p.registry[p.registryAt]
		if !p.entryListed {
			file, ok := p.w.registryTranscriptOf(entry)
			if !ok {
				p.registryAt++
				continue
			}
			p.entryListed = true
			p.w.registryTranscripts[entry.SessionID] = file
			subagents := ListSubagents(filepath.Join(filepath.Dir(file), entry.SessionID, "subagents"))
			p.entryPaths = append([]string{file}, subagents...)
			p.entryPathAt = 0
		}
		for p.entryPathAt < len(p.entryPaths) {
			path := p.entryPaths[p.entryPathAt]
			p.entryPathAt++
			p.w.livePaths[path] = struct{}{}
			if p.w.transcriptTails.has(path) {
				continue
			}
			p.w.transcriptTails.set(path, p.w.transcriptTail(path))
			return true
		}
		p.registryAt++
		p.entryListed = false
		p.entryPaths = nil
	}
	return false
}

// stepDiscovery opens the rescan's new transcript files: the spool-referenced paths first, then
// the projects walk, its files newest-modified first so a cold start reads the live session
// before days-old files.
func (p *passWork) stepDiscovery() bool {
	if !p.rescan {
		return false
	}
	if !p.referencedDone {
		p.referencedDone = true
		p.discovered = p.w.referencedTranscripts.snapshot()
		p.discoverAt = 0
	}
	if p.discoverAt < len(p.discovered) {
		path := p.discovered[p.discoverAt]
		p.discoverAt++
		if !p.w.transcriptTails.has(path) {
			p.w.transcriptTails.set(path, p.w.transcriptTail(path))
		}
		return true
	}
	for {
		if len(p.walk) == 0 {
			if p.walkStarted {
				// the walk is whole: its listings replace the last ones, so a directory that
				// went away takes its listing with it
				p.w.listings = p.walked
				return false
			}
			p.walkStarted = true
			p.walked = make(map[string]dirListing)
			root := filepath.Join(paths.ClaudeConfigDir(p.w.env), "projects")
			if mtime, ok := mtimeOf(root); ok {
				p.pushWalk(root, mtime, 0)
			}
			continue
		}
		frame := &p.walk[len(p.walk)-1]
		if frame.fileAt < len(frame.files) {
			path := frame.files[frame.fileAt]
			frame.fileAt++
			if !p.w.transcriptTails.has(path) {
				p.w.transcriptTails.set(path, p.w.transcriptTail(path))
			}
			return true
		}
		if frame.dirAt < len(frame.dirs) {
			sub := frame.dirs[frame.dirAt]
			frame.dirAt++
			if frame.depth+1 > maxWalkDepth {
				continue
			}
			p.pushWalk(sub.path, sub.mtimeMs, frame.depth+1)
			continue
		}
		p.walk = p.walk[:len(p.walk)-1]
	}
}

// pushWalk scans one directory, modified at mtimeMs, into a new frame.
func (p *passWork) pushWalk(dir string, mtimeMs int64, depth int) {
	listing, ok := p.w.listings[dir]
	if !ok || listing.mtimeMs != mtimeMs || listing.readMs < mtimeMs+listingSettle.Milliseconds() {
		listing, ok = readListing(dir, mtimeMs)
		if !ok {
			return
		}
	}
	p.walked[dir] = listing
	files, dirs := p.w.scanDir(listing)
	p.walk = append(p.walk, walkFrame{depth: depth, files: files, dirs: dirs})
}

// readListing reads dir's entries: its .jsonl files and its subdirectories; false when it
// cannot be read.
func readListing(dir string, mtimeMs int64) (dirListing, bool) {
	listing := dirListing{mtimeMs: mtimeMs, readMs: time.Now().UnixMilli()}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dirListing{}, false
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		switch {
		case entry.IsDir():
			listing.dirs = append(listing.dirs, path)
		case strings.HasSuffix(entry.Name(), ".jsonl"):
			listing.files = append(listing.files, path)
		}
	}
	return listing, true
}

// scanDir is one directory listing's fresh transcript paths and subdirectories, each
// newest-mtime first.
func (w *Watcher) scanDir(listing dirListing) ([]string, []walkDir) {
	type scanned struct {
		path    string
		mtimeMs int64
	}
	var files, dirs []scanned
	for _, path := range listing.dirs {
		mtime, ok := mtimeOf(path)
		if !ok {
			continue
		}
		dirs = append(dirs, scanned{path: path, mtimeMs: mtime})
	}
	for _, path := range listing.files {
		mtime, ok := mtimeOf(path)
		if !ok || mtime < w.sinceCutoff() {
			continue
		}
		files = append(files, scanned{path: path, mtimeMs: mtime})
	}
	newest := func(a, b scanned) int { return cmp.Compare(b.mtimeMs, a.mtimeMs) }
	slices.SortStableFunc(files, newest)
	slices.SortStableFunc(dirs, newest)
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	walkDirs := make([]walkDir, 0, len(dirs))
	for _, dir := range dirs {
		walkDirs = append(walkDirs, walkDir(dir))
	}
	return paths, walkDirs
}

// noteTailRead records a transcript tail read's outcome. New bytes re-arm the parser's
// windows and move the idle mark; a read that found nothing past idleCompact drops them,
// once — thousands of quiet files must not hold their last streams for the watcher's life.
func (w *Watcher) noteTailRead(tail *fileTail, grew bool) {
	if grew {
		tail.lastGrowthMs = w.now()
		tail.compacted = false
		return
	}
	if tail.compacted || w.now()-tail.lastGrowthMs < idleCompact.Milliseconds() {
		return
	}
	tail.parser.Compact()
	tail.compacted = true
}

// stepTailing feeds every tailed transcript's new lines, in discovery order, one line a step.
func (p *passWork) stepTailing() bool {
	if !p.tailsSnapshotted {
		p.tailsSnapshotted = true
		p.tailSnapshot = p.w.transcriptTails.snapshot()
		p.tailAt = 0
	}
	if p.tailLineAt < len(p.tailLines) {
		line := p.tailLines[p.tailLineAt]
		p.tailLineAt++
		p.w.feedLine(p.curTail, line)
		return true
	}
	for p.tailAt < len(p.tailSnapshot) {
		tail := p.tailSnapshot[p.tailAt]
		p.tailAt++
		result := TailFile(&tail.tail, TailChunkBytes)
		p.w.noteTailRead(tail, len(result.Lines) > 0)
		if len(result.Lines) == 0 {
			continue
		}
		p.curTail = tail
		p.tailLines = result.Lines
		p.tailLineAt = 0
		return true
	}
	return false
}

// stepEviction drops the tails nothing is waiting on: a file the registry no longer names and
// whose last change is past the since window can only sit quiet — yet every tick would stat it.
// Dropped with its referenced-transcript entry, so the rescan cannot re-open it from byte 0; a
// fresh change or a fresh spool line re-discovers it as any new file.
func (p *passWork) stepEviction() bool {
	if !p.rescan {
		return false
	}
	if !p.evictSnapshotted {
		p.evictSnapshotted = true
		p.evictPaths = make([]string, 0, len(p.w.transcriptTails.order))
		for _, path := range p.w.transcriptTails.order {
			if _, ok := p.w.transcriptTails.files[path]; ok {
				p.evictPaths = append(p.evictPaths, path)
			}
		}
		p.evictAt = 0
	}
	for p.evictAt < len(p.evictPaths) {
		path := p.evictPaths[p.evictAt]
		p.evictAt++
		if _, live := p.w.livePaths[path]; live {
			continue
		}
		mtime := int64(-1)
		if info, err := os.Stat(path); err == nil {
			mtime = info.ModTime().UnixMilli()
		}
		if mtime >= p.w.sinceCutoff() {
			continue
		}
		p.w.transcriptTails.delete(path)
		p.w.referencedTranscripts.remove(path)
		return true
	}
	return false
}

// stepJobs reads the provider plugins' jobs, feeds their tails, then retires the landed ones —
// after feeding: a transcript sighting re-lives both the job's session and its agent, so the
// pass must have the last word, and it ends the session, not just the agent.
func (p *passWork) stepJobs() bool {
	if !p.rescan {
		return false
	}
	if !p.jobsRead {
		p.jobsRead = true
		p.jobs = ReadJobs(p.w.ctx, p.w.env, p.w.jobUpstream)
		return true
	}
	if p.jobAt < len(p.jobs) {
		job := p.jobs[p.jobAt]
		p.jobAt++
		p.w.applyJob(job)
		return true
	}
	if !p.jobTailsSnapshotted {
		p.jobTailsSnapshotted = true
		p.jobTailSnapshot = p.w.jobTails.snapshot()
		p.jobTailAt = 0
	}
	if p.jobLineAt < len(p.jobLines) {
		line := p.jobLines[p.jobLineAt]
		p.jobLineAt++
		p.w.feedLine(p.curJobTail, line)
		return true
	}
	for p.jobTailAt < len(p.jobTailSnapshot) {
		tail := p.jobTailSnapshot[p.jobTailAt]
		p.jobTailAt++
		result := TailFile(&tail.tail, TailChunkBytes)
		if len(result.Lines) == 0 {
			continue
		}
		p.curJobTail = tail
		p.jobLines = result.Lines
		p.jobLineAt = 0
		return true
	}
	if !p.retired {
		p.retired = true
		for _, job := range p.jobs {
			if !job.Live && job.UpdatedAt != nil {
				p.w.store.EndSession(job.SessionID, *job.UpdatedAt)
			}
		}
		return len(p.jobs) > 0
	}
	return false
}

// mtimeOf is a path's modified time; false when it cannot be stat-ed (then it is not worth
// reading).
func mtimeOf(path string) (int64, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return info.ModTime().UnixMilli(), true
}

// stepHarness runs every harness collector once on a rescanning pass, then lands its
// records one a step.
func (p *passWork) stepHarness() bool {
	if !p.rescan {
		return false
	}
	if !p.harnessRead {
		p.harnessRead = true
		p.harnessRecords = p.w.collectHarness()
		return true
	}
	if p.harnessAt < len(p.harnessRecords) {
		record := p.harnessRecords[p.harnessAt]
		p.harnessAt++
		p.w.landHarness(record)
		return true
	}
	return false
}

// collectHarness runs each collector once; one failing logs its line and the rest go on.
func (w *Watcher) collectHarness() []shared.RequestRecord {
	var out []shared.RequestRecord
	for _, collector := range w.harnessCollectors {
		records, err := collector.Collect(w.ctx, w.sinceCutoff())
		if err != nil {
			w.log(fmt.Sprintf("radar: harness collector failed (%v)", err))
			continue
		}
		out = append(out, records...)
	}
	return out
}

// landHarness files one harness record: the session's cwd the record carried, then the
// request itself. The session is a plain one — the registry's next rescan ends it at its
// last activity, which is what an agent CLI's quieted session is.
func (w *Watcher) landHarness(record shared.RequestRecord) {
	if record.Cwd != nil && *record.Cwd != "" && w.harnessMetas[record.SessionID] != *record.Cwd {
		w.harnessMetas[record.SessionID] = *record.Cwd
		w.applySessionMeta(SessionMeta{ID: record.SessionID, Cwd: record.Cwd})
	}
	w.store.AddRequest(w.defaultUpstream(record), nil)
}
