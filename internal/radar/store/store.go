package store

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// SessionNow is what the session's main agent is doing right now: its newest call's
// one-line what and when that request went out.
type SessionNow struct {
	What *string `json:"what"`
	Ts   int64   `json:"ts"`
}

// SessionContext is how full that same call's context window is, and how large the
// window is.
type SessionContext struct {
	Used   int64 `json:"used"`
	Window int64 `json:"window"`
}

// SessionListItem is one session's card in the live rail.
type SessionListItem struct {
	ID              string                `json:"id"`
	Project         *string               `json:"project"`
	Cwd             *string               `json:"cwd"`
	Name            *string               `json:"name"`
	Branch          *string               `json:"branch"`
	Repo            *string               `json:"repo"`
	ParentSessionID *string               `json:"parentSessionId"`
	StartedAt       *int64                `json:"startedAt"`
	EndedAt         *int64                `json:"endedAt"`
	Live            bool                  `json:"live"`
	Status          *shared.SessionStatus `json:"status"`
	Activity        shared.Activity       `json:"activity"`
	Model           *string               `json:"model"`
	AgentCount      int64                 `json:"agentCount"`
	LiveAgentCount  int64                 `json:"liveAgentCount"`
	RequestCount    int64                 `json:"requestCount"`
	Tokens          int64                 `json:"tokens"`
	LastAt          int64                 `json:"lastAt"`
	External        bool                  `json:"external"`
	Title           *string               `json:"title"`
	CostUsd         *shared.USD           `json:"costUsd"`
	CostSplitUsd    *cost.CostSplit       `json:"costSplitUsd"`
	Now             *SessionNow           `json:"now"`
	Context         *SessionContext       `json:"context"`
	// CPUPct and RSSMB are the session's process tree's latest sample: the share of one core it
	// used over the last 10 s and its resident memory. Only the GET /api/sessions rows carry
	// them, and only for a session whose pid the hub's registry names.
	CPUPct *float64 `json:"cpuPct,omitempty"`
	RSSMB  *float64 `json:"rssMB,omitempty"`
}

// SessionTreeItem is a session list item with its jobs folded under it: the shape
// /api/sessions serves — main sessions and the Unattached group at the top level, every
// job a child of the session that submitted it.
type SessionTreeItem struct {
	SessionListItem
	Jobs []SessionTreeItem `json:"jobs,omitempty"`
}

// ContentPair is the captured input and output text riding with a request.
type ContentPair struct {
	Input  *string
	Output *string
}

// ContentRecord is a request's captured input and output text, keyed by the id the
// request is stored under.
type ContentRecord struct {
	RequestID string  `json:"requestId"`
	Input     *string `json:"input"`
	Output    *string `json:"output"`
}

// Change is one mutation's delta, handed to every listener.
type Change struct {
	Sessions bool `json:"sessions"`
	// Touched lists every session the change touched (its records' sessions, or the one an
	// upsert or end named).
	Touched  []string                `json:"touched"`
	Requests []shared.RequestRecord  `json:"requests"`
	Events   []shared.EventRecord    `json:"events"`
	Tools    []shared.ToolCallRecord `json:"tools"`
	Content  []ContentRecord         `json:"content"`
	// Captures lists the distinct prompt captures the change brought (a spool capture
	// line); only the history writer reads them, so the store holds none.
	Captures []shared.CaptureRecord `json:"captures,omitempty"`
}

// RequestFilter narrows the request reads: a session, an agent, a model, a floor
// timestamp, a page size. Absent means all.
type RequestFilter struct {
	Session *string
	Agent   *string
	Model   *string
	Since   *int64
	Limit   *int64
}

// matches says whether a request answers the filter.
func (f RequestFilter) matches(r *shared.RequestRecord) bool {
	if f.Session != nil && r.SessionID != *f.Session {
		return false
	}
	if f.Agent != nil && r.AgentID != *f.Agent {
		return false
	}
	if f.Model != nil && r.Model != *f.Model {
		return false
	}
	if f.Since != nil && r.Ts < *f.Since {
		return false
	}
	return true
}

// EventsFilter narrows the event reads: a session, a floor timestamp, a page size.
type EventsFilter struct {
	Session *string
	Since   *int64
	Limit   *int64
}

// keeps says whether an event answers the filter.
func (f EventsFilter) keeps(event *shared.EventRecord) bool {
	if f.Session != nil && (event.SessionID == nil || *event.SessionID != *f.Session) {
		return false
	}
	return f.Since == nil || event.Ts >= *f.Since
}

// ToolsFilter narrows the tool reads: a session, only the failed ones, a page size.
type ToolsFilter struct {
	Session *string
	Failed  bool
	Limit   *int64
}

// keeps says whether a tool call answers the filter.
func (f ToolsFilter) keeps(tool *shared.ToolCallRecord) bool {
	if f.Session != nil && tool.SessionID != *f.Session {
		return false
	}
	return !f.Failed || !tool.Ok
}

// ModelsScope is what the models tables can be narrowed to: a time window and the picked
// sessions. Absent means all.
type ModelsScope struct {
	From     *int64
	To       *int64
	Sessions []string
}

// ModelsAnswer is the models tab's tables.
type ModelsAnswer struct {
	Models    []shared.ModelRow    `json:"models"`
	Upstreams []shared.UpstreamRow `json:"upstreams"`
	Tools     []ToolCountRow       `json:"tools"`
}

// SessionUpsert is a session's facts as one source saw them; the first writer wins each
// fact, and the naming side updates as the session runs.
type SessionUpsert struct {
	ID          string
	Cwd         *string
	Upstream    *string
	CcVersion   *string
	StartedAt   *int64
	External    *bool
	CustomTitle *string
	AgentName   *string
	AiTitle     *string
	Slug        *string
	Branch      *string
	// Repo is what a job ingest knows outright; when absent a plain session derives its
	// repo from cwd.
	Repo            *string
	ParentSessionID *string
}

// AgentUpsert is one agent's facts as one source saw them. ParentID and Title carry an
// explicit null (a pointer to nil) apart from absence, because writing them null is a
// change the store honours.
type AgentUpsert struct {
	SessionID   string
	ID          string
	ParentID    **string
	Kind        *shared.AgentKind
	Name        *string
	AgentType   *string
	Description *string
	Model       *string
	Title       **string
}

// RawRecords is every request, tool call and event held, unsorted — the alert engine's
// input.
type RawRecords struct {
	Requests []shared.RequestRecord
	Tools    []shared.ToolCallRecord
	Events   []shared.EventRecord
}

const (
	// maxSideRecords bounds the side records (API errors, router events): small ones that
	// only matter while recent.
	maxSideRecords = 5000

	// subagentLiveMs is how recent a subagent's last request (or start) must be for it to
	// read as live.
	subagentLiveMs = 5 * 60_000

	// minP95Samples is the least timed requests a percentile needs before it says
	// anything; the view shows a dash instead.
	minP95Samples = 5

	// derivedTTLMs is how long a cached whole-store answer stays fresh: a version bump
	// alone is not enough, because now enters the views (a live session's activity
	// window) — one build answers a burst of reads, a stale one is never served.
	derivedTTLMs = 1_000

	// aliasesCap bounds the transcript ids remembered under their router twin's id. A
	// copy lands within moments of its route line, so a window this wide never misses
	// one; dropping the oldest keeps the map flat.
	aliasesCap = 512

	// defaultPage is the page size every paged read serves when the caller named none.
	defaultPage = 200
)

// pruneLookahead is how many fresh entries a prune reads past before it stops: the walk
// costs what it drops plus this many checks, so a burst of calls inside the match window
// (which nothing can prune) pays a constant per insert, and a catch-up's stale tail still
// goes in one pass — the advancing frontier turns it stale, and the next call's walk
// deletes it where it stands.
const pruneLookahead = 8

// sessionTitles is the names a session's transcript carries; the latest value of each
// stands.
type sessionTitles struct {
	customTitle *string
	agentName   *string
	aiTitle     *string
	slug        *string
}

// sessionState is one session the store tracks.
type sessionState struct {
	id        string
	cwd       *string
	upstream  *string
	ccVersion *string
	titles    sessionTitles
	branch    *string
	// repo is a job's workspace repo root; a plain session derives its repo from cwd,
	// cached per cwd.
	repo            *string
	parentSessionID *string
	external        bool
	startedAt       *int64
	// registryStartedAt is the start the registry named for the session, while it lists
	// it: what a live session displays.
	registryStartedAt *int64
	endedAt           *int64
	// live: only Claude Code's registry (or a SessionStart hook, until the next rescan)
	// says a session is live.
	live bool
	// registryName is the registry's name for the session, when the user named it; else
	// the transcript titles stand.
	registryName *string
	// registryDerivedName is the registry's own derived name; outranked by every human
	// and transcript choice.
	registryDerivedName *string
	// status is the registry's busy/idle state, as long as the session is live.
	status     *shared.SessionStatus
	model      *string
	lastAt     int64
	agents     map[string]*agentState
	agentOrder orderedIDs
}

// agentState is one agent of one session.
type agentState struct {
	id          string
	parentID    *string
	kind        shared.AgentKind
	name        *string
	agentType   *string
	description *string
	model       *string
	live        bool
	startedAt   *int64
	endedAt     *int64
	title       *string
}

// agentSlice is one agent's records within a session, pre-computed so a view never
// filters the session's records. The slices hold pointers into the store's own records,
// so one record lives once however many views read it.
type agentSlice struct {
	requests    []*shared.RequestRecord
	tools       []*shared.ToolCallRecord
	failed      int64
	lastRequest *shared.RequestRecord
	lastTs      int64
	tokens      shared.Tokens
}

// sessionSlice is one session's records, grouped per agent inside, rebuilt when the
// version moves.
type sessionSlice struct {
	requests []*shared.RequestRecord
	tools    []*shared.ToolCallRecord
	failed   int64
	tokens   shared.Tokens
	byAgent  map[string]*agentSlice
}

// storedRequest is a request and its arrival number: the arrival order is the order a
// JavaScript Map iterates, which the stable sorts below break ties with.
type storedRequest struct {
	record shared.RequestRecord
	seq    int64
}

// storedTool is a tool call and its arrival number.
type storedTool struct {
	record shared.ToolCallRecord
	seq    int64
}

// projectionCache is the records grouped by session, one build per version.
type projectionCache struct {
	version    int64
	projection map[string]*sessionSlice
}

// viewsCache is the built session views, one per (session, version): list, detail and
// summary reads within one version are free.
type viewsCache struct {
	version   int64
	bySession map[string]*shared.SessionView
}

// orderCache is the newest-first record order, one build per version: the list reads
// slice it, never re-sorting the store per call. The slices hold pointers into the
// store's own records; a read copies only the page it returns.
type orderCache struct {
	version  int64
	requests []*shared.RequestRecord
	tools    []*shared.ToolCallRecord
}

// derived is one cached whole-store answer with the moment it was built.
type derived[T any] struct {
	version int64
	at      int64
	value   T
}

// derivedOf serves a cached answer while its version stands and it is young enough.
func derivedOf[T any](slot **derived[T], version, nowMs int64, build func() T) T {
	held := *slot
	if held != nil && held.version == version && nowMs-held.at < derivedTTLMs {
		return held.value
	}
	value := build()
	*slot = &derived[T]{version: version, at: nowMs, value: value}
	return value
}

// listenerEntry is one registered listener, removed by pointer identity (function values
// do not compare).
type listenerEntry struct {
	fn func(Change)
}

// Store is the in-memory model, rebuilt from disk on start and fed incrementally after
// that. Bounded: at most shared.MaxSessions sessions and shared.MaxRecords request,
// tool and event records, shed oldest-timestamp first in batches, a live session's live
// agents kept while anything less protected remains. Reads group records by session once
// per version (cached), so a full projection costs one pass, not sessions × records.
type Store struct {
	mu  sync.RWMutex
	now func() time.Time

	// maxRecords caps the request, tool and event records kept; evictBatch is how many
	// records one eviction pass sheds: a tenth of the cap in one go, so a pass costs a
	// tenth of an insert amortised — a per-insert eviction would mean a full scan of the
	// store on every catch-up line. A pair of fields so one store can shed under a smaller
	// cap by the same arithmetic.
	maxRecords int64
	evictBatch int64

	seq      int64 // the timeline events' own numbering
	arrival  int64 // the arrival number of a stored record
	ver      int64
	repoRoot map[string]*string

	sessions      map[string]*sessionState
	sessionOrder  orderedIDs
	requests      map[string]*storedRequest
	tools         map[string]*storedTool
	events        []shared.EventRecord
	listeners     []*listenerEntry
	apiErrors     []shared.APIErrorRecord
	routerEvents  []shared.RouterEventRecord
	notices       map[string]map[string]int64
	pendingStarts map[string]int64

	proj         *projectionCache
	views        *viewsCache
	newest       *orderCache
	listCache    *derived[[]SessionListItem]
	summaryCache *derived[shared.Summary]
	modelsCache  *derived[ModelsAnswer]
	lonelyRoutes map[string]*waitingMap
	lonelyCopies map[string]*waitingSet
	aliases      *aliasMap
	routedTotals map[string]struct{}
	// calls files each held call under its request id: the key the pairing tries first.
	calls map[string]*callSlot
	// utilization is the newest unified limits a route line carried, and the line's ts.
	utilization   *domain.UnifiedUtilization
	utilizationAt int64
}

// CreateStore builds an empty store; now is the clock its derived answers age by.
func CreateStore(now func() time.Time) *Store {
	return &Store{
		now:           now,
		maxRecords:    shared.MaxRecords,
		evictBatch:    (shared.MaxRecords + 9) / 10,
		repoRoot:      make(map[string]*string),
		sessions:      make(map[string]*sessionState),
		requests:      make(map[string]*storedRequest),
		tools:         make(map[string]*storedTool),
		notices:       make(map[string]map[string]int64),
		pendingStarts: make(map[string]int64),
		lonelyRoutes:  make(map[string]*waitingMap),
		lonelyCopies:  make(map[string]*waitingSet),
		aliases:       newAliasMap(),
		routedTotals:  make(map[string]struct{}),
		calls:         make(map[string]*callSlot),
	}
}

// osRepoFS is the only filesystem the store touches: stat and read a .git, to name the
// repo a session works in.
type osRepoFS struct{}

func (osRepoFS) Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (osRepoFS) ReadFile(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func strPtr(text string) *string                             { return &text }
func int64Ptr(value int64) *int64                            { return &value }
func statusPtr(s shared.SessionStatus) *shared.SessionStatus { return &s }

// firstStrPtr is the first non-nil pointer.
func firstStrPtr(values ...*string) *string {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// firstIntPtr is the first non-nil pointer.
func firstIntPtr(values ...*int64) *int64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func sameOptStr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameStatus(a, b *shared.SessionStatus) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *Store) nowMillis() int64 { return s.now().UnixMilli() }

// orderedIDs keeps a set of ids in arrival order — the order a JavaScript Map iterates —
// with deletes that keep the rest in place.
type orderedIDs struct {
	ids []string
	has map[string]struct{}
}

func (o *orderedIDs) add(id string) {
	if o.has == nil {
		o.has = make(map[string]struct{})
	}
	if _, ok := o.has[id]; ok {
		return
	}
	o.has[id] = struct{}{}
	o.ids = append(o.ids, id)
}

func (o *orderedIDs) delete(id string) {
	if _, ok := o.has[id]; !ok {
		return
	}
	delete(o.has, id)
	for at, existing := range o.ids {
		if existing == id {
			o.ids = append(o.ids[:at], o.ids[at+1:]...)
			return
		}
	}
}

func (o *orderedIDs) first() string {
	if len(o.ids) == 0 {
		return ""
	}
	return o.ids[0]
}

// snapshot lists the ids in arrival order; a walk that deletes while it goes reads the
// snapshot, not the live slice.
func (o *orderedIDs) snapshot() []string { return slices.Clone(o.ids) }

// WaitingIDs is one key's waiting entries as the pairing walks them: ids in arrival
// order, each deletable where it stands.
type WaitingIDs interface {
	IDs() []string
	Delete(id string)
}

// waitingSet is a set of ids in arrival order. newest is the newest call ts the queue
// has waited on: the mark its prune runs against, so an older copy waits its full window
// for a route even when the session's next call is already past it.
type waitingSet struct {
	order  orderedIDs
	newest int64
}

func newWaitingSet() *waitingSet { return &waitingSet{} }

func (w *waitingSet) Add(id string)    { w.order.add(id) }
func (w *waitingSet) IDs() []string    { return w.order.snapshot() }
func (w *waitingSet) Delete(id string) { w.order.delete(id) }

// waitingMap pairs each waiting route id with the agent its line named, in arrival order.
type waitingMap struct {
	order  orderedIDs
	agents map[string]string
}

func newWaitingMap() *waitingMap {
	return &waitingMap{agents: make(map[string]string)}
}

func (w *waitingMap) Set(id, agent string) {
	if _, ok := w.agents[id]; !ok {
		w.order.add(id)
	}
	w.agents[id] = agent
}

func (w *waitingMap) Get(id string) (string, bool) {
	agent, ok := w.agents[id]
	return agent, ok
}

func (w *waitingMap) IDs() []string { return w.order.snapshot() }

func (w *waitingMap) Delete(id string) {
	if _, ok := w.agents[id]; !ok {
		return
	}
	delete(w.agents, id)
	w.order.delete(id)
}

// aliasMap remembers, per transcript copy id, the held id its call folds into; bounded,
// dropping the oldest pair once past the cap.
type aliasMap struct {
	order orderedIDs
	held  map[string]string
}

func newAliasMap() *aliasMap {
	return &aliasMap{held: make(map[string]string)}
}

func (a *aliasMap) get(id string) (string, bool) {
	held, ok := a.held[id]
	return held, ok
}

func (a *aliasMap) set(copyID, heldID string) {
	if _, ok := a.held[copyID]; !ok {
		a.order.add(copyID)
	}
	a.held[copyID] = heldID
	if len(a.held) <= aliasesCap {
		return
	}
	if oldest := a.order.first(); oldest != "" {
		a.order.delete(oldest)
		delete(a.held, oldest)
	}
}

// PruneWaiting deletes a key's waiting entries the incoming call can no longer pair
// with: a twin must sit within MatchMs of it, and a record the store's caps evicted can
// never pair at all. Dropped where the call arrives, walking from the front, so the
// closest-fit scan reads genuinely recent candidates and the waiting maps stay flat — a
// catch-up or an uptime can otherwise queue thousands of calls that will never meet
// their pair.
func PruneWaiting(waiting WaitingIDs, atTs int64, recordOf func(id string) (ts int64, ok bool)) {
	fresh := 0
	for _, id := range waiting.IDs() {
		ts, ok := recordOf(id)
		if !ok || atTs-ts > MatchMs {
			waiting.Delete(id)
			continue
		}
		fresh++
		if fresh >= pruneLookahead {
			break
		}
	}
}

// isJobItem is a session radar only saw through a provider plugin: the same rule the
// dashboard's rail applies.
func isJobItem(item SessionListItem) bool {
	return item.External || strings.Contains(item.ID, ":")
}

// SessionTree is the session list as the tree the API serves: a job is never a top-level
// session item. A job whose submitting session is listed hangs under it (a job of a job
// under their first main ancestor); one whose chain leads nowhere — no origin, or a
// parent the list does not know — joins the Unattached group, where history keeps the
// traffic no session owns.
func SessionTree(items []SessionListItem) []SessionTreeItem {
	byID := make(map[string]SessionListItem, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	// ancestorOf is the nearest non-job ancestor of a job, when its chain ends in one.
	ancestorOf := func(job SessionListItem) (SessionListItem, bool) {
		seen := map[string]struct{}{job.ID: {}}
		current := job
		for {
			if current.ParentSessionID == nil {
				return SessionListItem{}, false
			}
			next, ok := byID[*current.ParentSessionID]
			if !ok {
				return SessionListItem{}, false
			}
			if _, loop := seen[next.ID]; loop {
				return SessionListItem{}, false
			}
			if !isJobItem(next) {
				return next, true
			}
			seen[next.ID] = struct{}{}
			current = next
		}
	}
	var order []string
	tree := make(map[string]SessionTreeItem, len(items))
	for _, item := range items {
		if isJobItem(item) {
			continue
		}
		if _, ok := tree[item.ID]; !ok {
			order = append(order, item.ID)
		}
		tree[item.ID] = SessionTreeItem{SessionListItem: item}
	}
	for _, item := range items {
		if !isJobItem(item) {
			continue
		}
		parent, ok := ancestorOf(item)
		if !ok {
			if _, listed := tree[shared.OutsideSession]; !listed {
				continue // no Unattached item in this list: the job has nowhere to hang
			}
			parent.ID = shared.OutsideSession
		}
		holder, ok := tree[parent.ID]
		if !ok {
			continue
		}
		holder.Jobs = append(holder.Jobs, SessionTreeItem{SessionListItem: item})
		tree[parent.ID] = holder
	}
	out := make([]SessionTreeItem, 0, len(order))
	for _, id := range order {
		out = append(out, tree[id])
	}
	return out
}

// sessionLocked is the session's state, created on first sight: nothing is live because
// the store saw it — only the registry (or a SessionStart hook, or a job's own files)
// makes a session live, so a read transcript can never pose as an open one.
func (s *Store) sessionLocked(id string) *sessionState {
	state, ok := s.sessions[id]
	if ok {
		return state
	}
	state = &sessionState{id: id, agents: make(map[string]*agentState)}
	s.sessions[id] = state
	s.sessionOrder.add(id)
	return state
}

// agentLocked is the agent's state, created on first sight: a subagent starts unnamed —
// its id is never a display name — and only its meta file, its first prompt or an
// explicit upsert may name it.
func (s *Store) agentLocked(sessionID, agentID string) *agentState {
	state := s.sessionLocked(sessionID)
	existing, ok := state.agents[agentID]
	if ok {
		return existing
	}
	kind := shared.KindSubagent
	parent := strPtr("main")
	var name *string
	if agentID == "main" {
		kind = shared.KindMain
		parent = nil
		name = strPtr("main")
	}
	existing = &agentState{id: agentID, parentID: parent, kind: kind, name: name}
	state.agents[agentID] = existing
	state.agentOrder.add(agentID)
	return existing
}

// touchLocked moves a session's clock to a record's timestamp.
func (s *Store) touchLocked(sessionID string, ts int64) {
	state := s.sessionLocked(sessionID)
	if ts > state.lastAt {
		state.lastAt = ts
	}
	if state.startedAt == nil || ts < *state.startedAt {
		state.startedAt = int64Ptr(ts)
	}
}

// sessionsInOrderLocked lists the sessions in arrival order.
func (s *Store) sessionsInOrderLocked() []*sessionState {
	states := make([]*sessionState, 0, len(s.sessions))
	for _, id := range s.sessionOrder.ids {
		if state, ok := s.sessions[id]; ok {
			states = append(states, state)
		}
	}
	return states
}

// requestsInOrderLocked lists the requests in arrival order.
func (s *Store) requestsInOrderLocked() []*storedRequest {
	list := make([]*storedRequest, 0, len(s.requests))
	for _, held := range s.requests {
		list = append(list, held)
	}
	slices.SortStableFunc(list, func(a, b *storedRequest) int { return cmp.Compare(a.seq, b.seq) })
	return list
}

// toolsInOrderLocked lists the tool calls in arrival order.
func (s *Store) toolsInOrderLocked() []*storedTool {
	list := make([]*storedTool, 0, len(s.tools))
	for _, held := range s.tools {
		list = append(list, held)
	}
	slices.SortStableFunc(list, func(a, b *storedTool) int { return cmp.Compare(a.seq, b.seq) })
	return list
}

func (s *Store) allRequestsLocked() []*shared.RequestRecord {
	ordered := s.requestsInOrderLocked()
	out := make([]*shared.RequestRecord, 0, len(ordered))
	for _, held := range ordered {
		out = append(out, &held.record)
	}
	return out
}

func (s *Store) allToolsLocked() []*shared.ToolCallRecord {
	ordered := s.toolsInOrderLocked()
	out := make([]*shared.ToolCallRecord, 0, len(ordered))
	for _, held := range ordered {
		out = append(out, &held.record)
	}
	return out
}

func (s *Store) nextArrivalLocked() int64 {
	s.arrival++
	return s.arrival
}

// projectionLocked groups the store's records by session (and per agent inside), rebuilt
// when the version moves.
func (s *Store) projectionLocked() map[string]*sessionSlice {
	if s.proj != nil && s.proj.version == s.ver {
		return s.proj.projection
	}
	proj := make(map[string]*sessionSlice)
	for _, held := range s.requestsInOrderLocked() {
		slice := sliceOf(proj, held.record.SessionID)
		slice.requests = append(slice.requests, &held.record)
	}
	for _, held := range s.toolsInOrderLocked() {
		slice := sliceOf(proj, held.record.SessionID)
		countTool(&slice.tools, &slice.failed, &held.record)
		agentID := "main"
		if held.record.AgentID != nil {
			agentID = *held.record.AgentID
		}
		mine := agentSliceOf(slice, agentID)
		countTool(&mine.tools, &mine.failed, &held.record)
	}
	for _, slice := range proj {
		rollUpSlice(slice)
	}
	s.proj = &projectionCache{version: s.ver, projection: proj}
	return proj
}

func sliceOf(proj map[string]*sessionSlice, sessionID string) *sessionSlice {
	slice, ok := proj[sessionID]
	if !ok {
		slice = &sessionSlice{byAgent: make(map[string]*agentSlice)}
		proj[sessionID] = slice
	}
	return slice
}

func agentSliceOf(slice *sessionSlice, agentID string) *agentSlice {
	mine, ok := slice.byAgent[agentID]
	if !ok {
		mine = &agentSlice{}
		slice.byAgent[agentID] = mine
	}
	return mine
}

// countTool folds a tool call into a slice's totals.
func countTool(tools *[]*shared.ToolCallRecord, failed *int64, tool *shared.ToolCallRecord) {
	*tools = append(*tools, tool)
	if !tool.Ok {
		*failed++
	}
}

// rollUpSlice finishes a session slice: the token sum, and each agent's own clock,
// tokens and last request.
func rollUpSlice(slice *sessionSlice) {
	slice.tokens = shared.ZeroTokens
	for _, held := range slice.requests {
		slice.tokens = shared.AddTokens(slice.tokens, held.Tokens)
		mine := agentSliceOf(slice, held.AgentID)
		mine.requests = append(mine.requests, held)
	}
	for _, mine := range slice.byAgent {
		if len(mine.requests) > 0 {
			mine.lastRequest = mine.requests[len(mine.requests)-1]
		}
		mine.tokens = shared.ZeroTokens
		for _, held := range mine.requests {
			if held.Ts > mine.lastTs {
				mine.lastTs = held.Ts
			}
			mine.tokens = shared.AddTokens(mine.tokens, held.Tokens)
		}
	}
}

// newestFirstLocked is every request and every tool call, newest first, one sort per
// version.
func (s *Store) newestFirstLocked() *orderCache {
	if s.newest != nil && s.newest.version == s.ver {
		return s.newest
	}
	requests := s.allRequestsLocked()
	slices.SortStableFunc(requests, func(a, b *shared.RequestRecord) int { return cmp.Compare(b.Ts, a.Ts) })
	tools := s.allToolsLocked()
	slices.SortStableFunc(tools, func(a, b *shared.ToolCallRecord) int { return cmp.Compare(b.StartedAt, a.StartedAt) })
	s.newest = &orderCache{version: s.ver, requests: requests, tools: tools}
	return s.newest
}

// newestPage is the first limit of a newest-first source that keep admits, dereferenced
// into the caller's own values — the whole page without a copy of the source.
func newestPage[T any](source []*T, limit int64, keep func(item *T) bool) []T {
	out := make([]T, 0, min(max(int(limit), 0), len(source)))
	for _, held := range source {
		if !keep(held) {
			continue
		}
		out = append(out, *held)
		if int64(len(out)) >= limit {
			break
		}
	}
	return out
}

// lastPage is the last limit of an append-ordered source that keep admits, last-written
// first.
func lastPage[T any](source []T, limit int64, keep func(item *T) bool) []T {
	out := make([]T, 0, min(max(int(limit), 0), len(source)))
	for at := int64(len(source)) - 1; at >= 0 && int64(len(out)) < limit; at-- {
		if keep(&source[at]) {
			out = append(out, source[at])
		}
	}
	return out
}

// evictionCandidate is one record the eviction pass considers: which collection holds
// it, its own timestamp, and whether a live session's live agent owns it. id breaks
// exact ties (and is unique within a source).
type evictionCandidate struct {
	rank   int
	ts     int64
	source int // 0 requests, 1 tools, 2 events
	id     string
}

// evictionPin names a record the mutation just placed, pinned against the one eviction
// its own inserts triggered.
type evictionPin struct {
	source int
	id     string
}

// rankOfAgentLocked is 1 while the record belongs to a live session's live agent — the
// dashboard's very subject: such a record is offered to eviction only once nothing less
// protected remains to shed instead.
func (s *Store) rankOfAgentLocked(sessionID, agentID string) int {
	state, ok := s.sessions[sessionID]
	if !ok || !state.live {
		return 0
	}
	agent := state.agents[agentID]
	if agent != nil && agent.live {
		return 1
	}
	return 0
}

func (s *Store) rankOfLocked(sessionID, agentID *string) int {
	if sessionID == nil {
		return 0
	}
	id := "main"
	if agentID != nil {
		id = *agentID
	}
	return s.rankOfAgentLocked(*sessionID, id)
}

// evictRecordsLocked sheds records past the cap, oldest by their own timestamps first —
// request ts, tool startedAt, event ts — never by arrival order: catch-up feeds files in
// no particular time order, so an arrival-order drain dropped a live session's newest
// requests while day-old records survived. One pass per batch, and a live agent's
// records rank behind everything less protected, however old they are.
func (s *Store) evictRecordsLocked(pins []evictionPin) {
	excess := int64(len(s.requests)+len(s.tools)+len(s.events)) - s.maxRecords
	if excess <= 0 {
		return
	}
	s.applyEvictionsLocked(s.oldestRecordsLocked(max(excess, s.evictBatch), pins))
}

// oldestRecordsLocked picks the batch records with the least claim on being kept. The
// order is total (the id breaks the last tie), so the picks never depend on the order
// the pass walked the store in.
func (s *Store) oldestRecordsLocked(batch int64, pins []evictionPin) []evictionCandidate {
	pinned := make(map[evictionPin]struct{}, len(pins))
	for _, pin := range pins {
		pinned[pin] = struct{}{}
	}
	candidates := make([]evictionCandidate, 0, len(s.requests)+len(s.tools)+len(s.events))
	for id, held := range s.requests {
		if _, skip := pinned[evictionPin{source: 0, id: id}]; skip {
			continue
		}
		candidates = append(candidates, evictionCandidate{
			rank:   s.rankOfAgentLocked(held.record.SessionID, held.record.AgentID),
			ts:     held.record.Ts,
			source: 0,
			id:     id,
		})
	}
	for id, held := range s.tools {
		if _, skip := pinned[evictionPin{source: 1, id: id}]; skip {
			continue
		}
		candidates = append(candidates, evictionCandidate{
			rank:   s.rankOfLocked(&held.record.SessionID, held.record.AgentID),
			ts:     held.record.StartedAt,
			source: 1,
			id:     id,
		})
	}
	for at := range s.events {
		id := strconv.FormatInt(s.events[at].Seq, 10)
		if _, skip := pinned[evictionPin{source: 2, id: id}]; skip {
			continue
		}
		candidates = append(candidates, evictionCandidate{
			rank:   s.rankOfLocked(s.events[at].SessionID, s.events[at].AgentID),
			ts:     s.events[at].Ts,
			source: 2,
			id:     id,
		})
	}
	slices.SortFunc(candidates, shedFirst)
	if int64(len(candidates)) > batch {
		candidates = candidates[:batch]
	}
	return candidates
}

// shedFirst orders the candidates so the first is the next to shed: less protected
// first, then older, the source and the id breaking exact ties.
func shedFirst(a, b evictionCandidate) int {
	if a.rank != b.rank {
		return cmp.Compare(a.rank, b.rank)
	}
	if a.ts != b.ts {
		return cmp.Compare(a.ts, b.ts)
	}
	if a.source != b.source {
		return cmp.Compare(a.source, b.source)
	}
	return strings.Compare(a.id, b.id)
}

// applyEvictionsLocked deletes the picked records from where they live; the events array
// compacts in place, once.
func (s *Store) applyEvictionsLocked(picks []evictionCandidate) {
	gone := make(map[string]struct{})
	eventsGone := false
	for _, pick := range picks {
		switch pick.source {
		case 0:
			if held, ok := s.requests[pick.id]; ok {
				s.forgetCallLocked(held.record)
			}
			delete(s.requests, pick.id)
		case 1:
			delete(s.tools, pick.id)
		default:
			eventsGone = true
			gone[pick.id] = struct{}{}
		}
	}
	if !eventsGone {
		return
	}
	kept := s.events[:0]
	for _, event := range s.events {
		if _, drop := gone[strconv.FormatInt(event.Seq, 10)]; drop {
			continue
		}
		kept = append(kept, event)
	}
	s.events = kept
}

// evictSessionsLocked keeps at most shared.MaxSessions sessions, dropping the stalest.
func (s *Store) evictSessionsLocked() {
	if int64(len(s.sessions)) <= shared.MaxSessions {
		return
	}
	ordered := s.sessionsInOrderLocked()
	slices.SortStableFunc(ordered, func(a, b *sessionState) int { return cmp.Compare(a.lastAt, b.lastAt) })
	for _, victim := range ordered[:len(ordered)-shared.MaxSessions] {
		s.dropSessionLocked(victim.id)
	}
}

// dropSessionLocked forgets a session and every record it owns.
func (s *Store) dropSessionLocked(sessionID string) {
	delete(s.sessions, sessionID)
	s.sessionOrder.delete(sessionID)
	delete(s.notices, sessionID)
	for id, held := range s.requests {
		if held.record.SessionID == sessionID {
			s.forgetCallLocked(held.record)
			delete(s.requests, id)
		}
	}
	for id, held := range s.tools {
		if held.record.SessionID == sessionID {
			delete(s.tools, id)
		}
	}
}

// notifyLocked bumps the version, drops every cached answer and lists the listeners to
// call once the lock is open.
func (s *Store) notifyLocked(change Change) []*listenerEntry {
	s.ver++
	s.proj = nil
	s.views = nil
	s.newest = nil
	s.listCache = nil
	s.summaryCache = nil
	s.modelsCache = nil
	if len(s.listeners) == 0 {
		return nil
	}
	return slices.Clone(s.listeners)
}

// deliver runs the listeners once the lock is open: a listener may read the store back,
// so it never runs holding the lock.
func (s *Store) deliver(listeners []*listenerEntry, change Change) {
	for _, entry := range listeners {
		entry.fn(change)
	}
}

// pairKey is the session and model a route line and its transcript copy must share.
func pairKey(r shared.RequestRecord) string { return r.SessionID + "\x00" + r.Model }

// callSlot is where the call one request id names is stored, and which sources have
// landed in it.
type callSlot struct {
	id     string
	routed bool
	copied bool
}

// holds reports whether a record from the given source already landed in the slot.
func (c *callSlot) holds(fromRouter bool) bool {
	if fromRouter {
		return c.routed
	}
	return c.copied
}

// placeRequestLocked stores one request, folding it into the record of the same call
// when one is held, and returns what is stored.
func (s *Store) placeRequestLocked(record shared.RequestRecord) shared.RequestRecord {
	if id, ok := s.ownCopyLocked(record); ok {
		existing := s.requests[id]
		stored := Refresh(existing.record, record)
		if _, finalUsage := s.routedTotals[stored.ID]; finalUsage && !FromRouter(record) &&
			shared.TotalTokens(existing.record.Tokens) > 0 {
			// the route line's own final usage stands: a later transcript copy of the same
			// call only repeats what the (possibly rewritten) stream opened with
			stored.Tokens = existing.record.Tokens
		}
		existing.record = stored
		s.noteCallLocked(stored, FromRouter(record), !FromRouter(record))
		return stored
	}
	stored, paired := s.pairLocked(record)
	if existing, ok := s.requests[stored.ID]; ok {
		existing.record = stored // the paired twin's slot, its arrival number kept
	} else {
		s.requests[stored.ID] = &storedRequest{record: stored, seq: s.nextArrivalLocked()}
	}
	s.noteCallLocked(stored, paired || FromRouter(record), paired || !FromRouter(record))
	return stored
}

// ownCopyLocked is the held record a later copy from the same source refreshes: the
// record's own id, the twin a transcript id was folded into, or the call its request id
// names once that source has landed in it.
func (s *Store) ownCopyLocked(record shared.RequestRecord) (string, bool) {
	id := record.ID
	if held, ok := s.aliases.get(record.ID); ok {
		id = held
	}
	if _, ok := s.requests[id]; ok {
		return id, true
	}
	if slot, ok := s.slotLocked(record.RequestID); ok && slot.holds(FromRouter(record)) {
		return slot.id, true
	}
	return "", false
}

// pairLocked lands a record with no held copy of its own source: it joins the other
// source's copy of the same call when one is held, else waits for one. The twin is found
// in this order: the request id both sources carry, then the closest fit in the match
// window (same session, model and agent). It reports whether the record joined a twin.
func (s *Store) pairLocked(record shared.RequestRecord) (shared.RequestRecord, bool) {
	fromRouter := FromRouter(record)
	twin := ""
	if slot, ok := s.slotLocked(record.RequestID); ok && !slot.holds(fromRouter) {
		twin = slot.id
	} else if fromRouter {
		twin = s.closestCopyLocked(record)
	} else {
		twin = s.closestRouteLocked(record)
	}
	if twin == "" {
		if fromRouter {
			return s.waitRouteLocked(record), false
		}
		return FillUpstream(s.waitCopyLocked(pairKey(record), record)), false
	}
	held := s.requests[twin].record
	if copies := s.lonelyCopies[pairKey(held)]; copies != nil {
		copies.Delete(twin)
	}
	if routes := s.lonelyRoutes[pairKey(held)]; routes != nil {
		routes.Delete(twin)
	}
	if fromRouter {
		if shared.TotalTokens(record.Tokens) > 0 {
			s.routedTotals[twin] = struct{}{}
		}
		return Combine(held, record, twin), true
	}
	s.aliases.set(record.ID, twin)
	if shared.TotalTokens(held.Tokens) > 0 {
		s.routedTotals[twin] = struct{}{}
	}
	return Combine(record, held, twin), true
}

// slotLocked is the held call a request id names; false when the id is empty or its
// record is no longer held.
func (s *Store) slotLocked(requestID string) (*callSlot, bool) {
	if requestID == "" {
		return nil, false
	}
	slot, ok := s.calls[requestID]
	if !ok {
		return nil, false
	}
	if _, held := s.requests[slot.id]; !held {
		delete(s.calls, requestID)
		return nil, false
	}
	return slot, true
}

// noteCallLocked files a stored record under its request id, with the sources now in it.
func (s *Store) noteCallLocked(stored shared.RequestRecord, routed, copied bool) {
	if stored.RequestID == "" {
		return
	}
	slot, ok := s.calls[stored.RequestID]
	if !ok {
		slot = &callSlot{}
		s.calls[stored.RequestID] = slot
	}
	slot.id = stored.ID
	slot.routed = slot.routed || routed
	slot.copied = slot.copied || copied
}

// forgetCallLocked drops the request id entry of a record leaving the store.
func (s *Store) forgetCallLocked(record shared.RequestRecord) {
	if slot, ok := s.calls[record.RequestID]; ok && slot.id == record.ID {
		delete(s.calls, record.RequestID)
	}
}

// waitRouteLocked leaves a spool line with no twin in reach waiting under its key, and
// stores it alone (under the main agent when untagged).
func (s *Store) waitRouteLocked(route shared.RequestRecord) shared.RequestRecord {
	key := pairKey(route)
	routes := s.lonelyRoutes[key]
	if routes == nil {
		routes = newWaitingMap()
		s.lonelyRoutes[key] = routes
	}
	s.pruneWaitingLocked(routes, route.Ts)
	routes.Set(route.ID, route.AgentID)
	if route.AgentID == "" {
		stored := route
		stored.AgentID = "main"
		return stored
	}
	return route
}

// closestCopyLocked is the waiting transcript copy closest to the route under
// MatchDistance, or "" when none can be the same call.
func (s *Store) closestCopyLocked(route shared.RequestRecord) string {
	waiting := s.lonelyCopies[pairKey(route)]
	if waiting == nil {
		return ""
	}
	s.pruneWaitingLocked(waiting, route.Ts)
	best := ""
	var bestDistance int64
	found := false
	for _, id := range waiting.IDs() {
		held, ok := s.requests[id]
		if !ok {
			continue
		}
		apart := MatchDistance(held.record, route, route.AgentID)
		if apart == nil {
			continue
		}
		if !found || *apart < bestDistance {
			best = id
			bestDistance = *apart
			found = true
		}
	}
	return best
}

// closestRouteLocked is the waiting route closest to the transcript call under
// MatchDistance, or "" when none can be the same call.
func (s *Store) closestRouteLocked(transcript shared.RequestRecord) string {
	routes := s.lonelyRoutes[pairKey(transcript)]
	if routes == nil {
		return ""
	}
	s.pruneWaitingLocked(routes, transcript.Ts)
	best := ""
	var bestDistance int64
	found := false
	for _, id := range routes.IDs() {
		held, ok := s.requests[id]
		if !ok {
			continue
		}
		agent, _ := routes.Get(id)
		apart := MatchDistance(transcript, held.record, agent)
		if apart == nil {
			continue
		}
		if !found || *apart < bestDistance {
			best = id
			bestDistance = *apart
			found = true
		}
	}
	return best
}

// waitCopyLocked leaves a call with no twin in reach waiting under its key, pruned
// against the newest call the queue has already waited on — not the call arriving now.
// A burst of one session's calls can sit minutes apart in stream time while arriving
// together, and each copy's route line can still cross a pass boundary after the burst;
// pruning against the arriving copy's own ts would evict the copy its line has not
// passed yet, and the line would stand alone and store the call twice.
func (s *Store) waitCopyLocked(key string, transcript shared.RequestRecord) shared.RequestRecord {
	copies := s.lonelyCopies[key]
	if copies == nil {
		copies = newWaitingSet()
		s.lonelyCopies[key] = copies
	}
	s.pruneWaitingLocked(copies, copies.newest)
	copies.Add(transcript.ID)
	if transcript.Ts > copies.newest {
		copies.newest = transcript.Ts
	}
	return transcript
}

// pruneWaitingLocked prunes one waiting collection against the store's own requests.
func (s *Store) pruneWaitingLocked(waiting WaitingIDs, atTs int64) {
	PruneWaiting(waiting, atTs, func(id string) (int64, bool) {
		held, ok := s.requests[id]
		if !ok {
			return 0, false
		}
		return held.record.Ts, true
	})
}

// AddSpoolLine folds one spool line in: session and agent lifecycle, tool pairing, and
// the timeline event itself.
func (s *Store) AddSpoolLine(line shared.SpoolLine) {
	s.mu.Lock()
	ts, ok := tsToMs(line.Ts)
	if !ok {
		ts = s.nowMillis()
	}
	change := Change{}
	if line.SessionID != nil {
		s.touchLocked(*line.SessionID, ts)
		applyLineMetaLocked(s.sessionLocked(*line.SessionID), &line)
		if line.Event == shared.NoticeEvent {
			s.countNoticeLocked(*line.SessionID, line.AgentID)
		}
		change.Sessions = true
		change.Touched = append(change.Touched, *line.SessionID)
	}
	s.applyLifecycleLocked(&line, ts, line.SessionID, line.AgentID)
	s.applyToolEventLocked(&line, ts, line.SessionID, line.AgentID, &change)
	s.seq++
	record := shared.EventRecord{
		Seq:       s.seq,
		Ts:        ts,
		Kind:      line.Event,
		SessionID: line.SessionID,
		AgentID:   line.AgentID,
		Label:     labelFor(&line),
		Payload:   line,
	}
	s.events = append(s.events, record)
	change.Events = append(change.Events, record)
	s.evictSessionsLocked()
	pins := []evictionPin{{source: 2, id: strconv.FormatInt(record.Seq, 10)}}
	for _, tool := range change.Tools {
		pins = append(pins, evictionPin{source: 1, id: tool.ID})
	}
	s.evictRecordsLocked(pins)
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// AddRequest stores one request, folding it into the record of the same call when one is
// held. The transcript's input/output capture rides with its request; the stored id (a
// route pairing may have renamed it) keys the content, so it always lands beside the
// request it belongs to.
func (s *Store) AddRequest(record shared.RequestRecord, content *ContentPair) {
	s.mu.Lock()
	merged := s.placeRequestLocked(record)
	s.touchLocked(merged.SessionID, merged.Ts)
	agent := s.agentLocked(merged.SessionID, merged.AgentID)
	if agent.model == nil {
		agent.model = strPtr(merged.Model)
	}
	s.evictRecordsLocked([]evictionPin{{source: 0, id: merged.ID}})
	change := Change{
		Sessions: true,
		Touched:  []string{merged.SessionID},
		Requests: []shared.RequestRecord{merged},
	}
	if content != nil {
		change.Content = []ContentRecord{{RequestID: merged.ID, Input: content.Input, Output: content.Output}}
	}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// AddCapture passes one distinct prompt capture straight through to the history writer:
// the store itself keeps none (nothing in the dashboard reads it live, and the bytes
// belong on disk once).
func (s *Store) AddCapture(record shared.CaptureRecord) {
	s.mu.Lock()
	change := Change{Captures: []shared.CaptureRecord{record}}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// AddToolCall stores one tool call, merging duration and success as they firm up.
func (s *Store) AddToolCall(record shared.ToolCallRecord) {
	s.mu.Lock()
	merged := record
	if existing, ok := s.tools[record.ID]; ok {
		merged = existing.record
		if record.DurationMs != nil {
			merged.DurationMs = record.DurationMs
		}
		if record.Ok {
			merged.Ok = true
		}
	}
	s.storeToolLocked(merged)
	s.touchLocked(merged.SessionID, merged.StartedAt)
	s.evictRecordsLocked([]evictionPin{{source: 1, id: merged.ID}})
	change := Change{
		Sessions: true,
		Touched:  []string{merged.SessionID},
		Tools:    []shared.ToolCallRecord{merged},
	}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// storeToolLocked places a tool call, keeping an existing slot's arrival number.
func (s *Store) storeToolLocked(record shared.ToolCallRecord) {
	if existing, ok := s.tools[record.ID]; ok {
		existing.record = record
		return
	}
	s.tools[record.ID] = &storedTool{record: record, seq: s.nextArrivalLocked()}
}

// UpsertSession writes a session's facts; the first writer wins each fact.
func (s *Store) UpsertSession(input SessionUpsert) {
	s.mu.Lock()
	applySessionUpsertLocked(s.sessionLocked(input.ID), input)
	s.agentLocked(input.ID, "main")
	s.evictSessionsLocked()
	change := Change{Sessions: true, Touched: []string{input.ID}}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// EndSession ends a session and every agent in it.
func (s *Store) EndSession(id string, endedAt int64) {
	s.mu.Lock()
	s.markSessionEndedLocked(id, endedAt)
	change := Change{Sessions: true, Touched: []string{id}}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// ApplyRegistry takes Claude Code's own live-session registry, rescanned every few
// seconds: what is live, and how it is.
func (s *Store) ApplyRegistry(entries []shared.RegistrySession, now int64) {
	s.mu.Lock()
	live := make(map[string]shared.RegistrySession, len(entries))
	for _, entry := range entries {
		live[entry.SessionID] = entry
	}
	var touched orderedIDs
	for _, state := range s.sessionsInOrderLocked() {
		if state.external || state.id == shared.OutsideSession {
			continue
		}
		if entry, ok := live[state.id]; ok {
			s.applyRegistryEntryLocked(state, &entry, &touched)
		} else {
			s.endUnlistedLocked(state, &touched)
		}
	}
	s.createUnlistedLocked(entries, &touched)
	s.recomputeSubagentsLocked(&touched, now)
	if len(touched.ids) == 0 {
		s.mu.Unlock()
		return
	}
	change := Change{Sessions: true, Touched: touched.snapshot()}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// applyRegistryEntryLocked sets one session against the registry: listed means live
// (with its state and name), absent means ended.
func (s *Store) applyRegistryEntryLocked(state *sessionState, entry *shared.RegistrySession, touched *orderedIDs) {
	if !state.live {
		state.live = true
		state.endedAt = nil
		s.agentLocked(state.id, "main").live = true
		touched.add(state.id)
	}
	s.applyRegistryOriginLocked(state, entry, touched)
	s.applyRegistryFactsLocked(state, entry, touched)
}

// createUnlistedLocked gives a registry entry with no session of its own one, live at
// once: the registry's word is enough to be listed.
func (s *Store) createUnlistedLocked(entries []shared.RegistrySession, touched *orderedIDs) {
	for i := range entries {
		entry := &entries[i]
		if entry.SessionID == shared.OutsideSession {
			continue
		}
		if _, ok := s.sessions[entry.SessionID]; ok {
			continue
		}
		s.applyRegistryEntryLocked(s.sessionLocked(entry.SessionID), entry, touched)
	}
}

// applyRegistryOriginLocked writes where and when the registry says the session runs; a
// transcript's own facts still win or keep. The start it names is the display truth
// while it lists the session: a transcript's earliest entry can reach back through
// resumes and read days older than the session actually open.
func (s *Store) applyRegistryOriginLocked(state *sessionState, entry *shared.RegistrySession, touched *orderedIDs) {
	if entry.Cwd != nil && state.cwd == nil {
		state.cwd = entry.Cwd
		touched.add(state.id)
	}
	if !sameInt64(entry.StartedAt, state.registryStartedAt) {
		state.registryStartedAt = entry.StartedAt
		touched.add(state.id)
	}
	earlierStarted(state, entry.StartedAt)
}

// endUnlistedLocked ends a session the registry no longer lists, at its last activity,
// hook or no hook.
func (s *Store) endUnlistedLocked(state *sessionState, touched *orderedIDs) {
	if !state.live {
		return
	}
	state.live = false
	state.status = nil
	if state.endedAt == nil {
		state.endedAt = int64Ptr(state.lastAt)
	}
	s.agentLocked(state.id, "main").live = false
	touched.add(state.id)
}

// applyRegistryFactsLocked writes the registry's busy/idle state and its names, applied
// when they differ from what is stored: a user-set name stands above everything radar
// could derive, a derived one only above the repo.
func (s *Store) applyRegistryFactsLocked(state *sessionState, entry *shared.RegistrySession, touched *orderedIDs) {
	status := registryStatusOf(entry.Status)
	if !sameStatus(status, state.status) {
		state.status = status
		touched.add(state.id)
	}
	var userName *string
	if entry.NameSource != nil && *entry.NameSource == "user" {
		userName = entry.Name
	}
	if !sameOptStr(userName, state.registryName) {
		state.registryName = userName
		touched.add(state.id)
	}
	var derived *string
	if entry.NameSource == nil || *entry.NameSource != "user" {
		derived = entry.Name
	}
	if !sameOptStr(derived, state.registryDerivedName) {
		state.registryDerivedName = derived
		touched.add(state.id)
	}
}

// recomputeSubagentsLocked applies the one liveness rule on the registry's cadence and
// stores the word, so the list and the session views — built from the same flags at the
// same version — can never tell the reader two different things.
func (s *Store) recomputeSubagentsLocked(touched *orderedIDs, now int64) {
	proj := s.projectionLocked()
	for _, state := range s.sessionsInOrderLocked() {
		if state.external || state.id == shared.OutsideSession {
			continue
		}
		s.recomputeSubagentsOfLocked(state, proj[state.id], touched, now)
	}
}

// subagentLiveLocked is one subagent against the clock: no recorded end, and work — or
// the start, before the first request arrived — under five minutes old. Most subagents
// never record an end, so recency is the whole test; an explicit end is never undone.
func subagentLiveLocked(agent *agentState, slice *sessionSlice, now int64) bool {
	if agent.endedAt != nil {
		return false
	}
	var lastRequest int64
	if slice != nil {
		if mine := slice.byAgent[agent.id]; mine != nil {
			lastRequest = mine.lastTs
		}
	}
	last := lastRequest
	if last == 0 && agent.startedAt != nil {
		last = *agent.startedAt
	}
	return now-last < subagentLiveMs
}

// agentLiveLocked is the one liveness rule every view reads through the stored flags: a
// subagent is live while its parent session is live and its last request — or its start
// — is under five minutes old; main and job agents keep the state their sources gave
// them.
func agentLiveLocked(agent *agentState, slice *sessionSlice, sessionLive bool, now int64) bool {
	if agent.kind != shared.KindSubagent {
		return agent.live
	}
	return sessionLive && subagentLiveLocked(agent, slice, now)
}

// recomputeSubagentsOfLocked moves only the subagents whose liveness changed.
func (s *Store) recomputeSubagentsOfLocked(state *sessionState, slice *sessionSlice, touched *orderedIDs, now int64) {
	for _, id := range state.agentOrder.ids {
		agent, ok := state.agents[id]
		if !ok || agent.kind != shared.KindSubagent {
			continue
		}
		live := agentLiveLocked(agent, slice, state.live, now)
		if live == agent.live {
			continue
		}
		agent.live = live
		touched.add(state.id)
	}
}

// UpsertAgent writes one agent's facts.
func (s *Store) UpsertAgent(input AgentUpsert) {
	s.mu.Lock()
	s.agentLocked(input.SessionID, "main")
	applyAgentUpsertLocked(s.agentLocked(input.SessionID, input.ID), input)
	change := Change{Sessions: true, Touched: []string{input.SessionID}}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// EndAgent retires one agent.
func (s *Store) EndAgent(sessionID, agentID string, endedAt int64) {
	s.mu.Lock()
	target := s.agentLocked(sessionID, agentID)
	target.live = false
	target.endedAt = int64Ptr(endedAt)
	change := Change{Sessions: true, Touched: []string{sessionID}}
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
}

// Summary is the dashboard's totals row.
func (s *Store) Summary() shared.Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	return derivedOf(&s.summaryCache, s.ver, s.nowMillis(), s.buildSummaryLocked)
}

func (s *Store) buildSummaryLocked() shared.Summary {
	states := s.sessionsInOrderLocked()
	views := make([]shared.SessionView, 0, len(states))
	for _, state := range states {
		views = append(views, *s.sessionViewLocked(state))
	}
	summary := Summarize(views, s.allRequestsLocked(), s.allToolsLocked(), s.nowMillis())
	for _, state := range states {
		summary.Errors += s.noticesOfLocked(state.id, nil)
	}
	summary.Utilization = s.utilization
	return summary
}

// NoteUtilization keeps the unified limits a route line carried at ts (epoch ms) when no newer
// line's are held.
func (s *Store) NoteUtilization(ts int64, utilization domain.UnifiedUtilization) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.utilization != nil && ts < s.utilizationAt {
		return
	}
	s.utilization, s.utilizationAt = &utilization, ts
	s.ver++
}

// SessionList is the session list, newest activity first.
func (s *Store) SessionList() []SessionListItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(derivedOf(&s.listCache, s.ver, s.nowMillis(), s.buildSessionListLocked))
}

func (s *Store) buildSessionListLocked() []SessionListItem {
	proj := s.projectionLocked()
	items := make([]SessionListItem, 0, len(s.sessions))
	for _, state := range s.sessionsInOrderLocked() {
		view := s.sessionViewLocked(state)
		var title *string
		for _, id := range state.agentOrder.ids {
			if agent, ok := state.agents[id]; ok && agent.title != nil {
				title = agent.title
				break
			}
		}
		var mine []*shared.RequestRecord
		if slice := proj[state.id]; slice != nil {
			mine = slice.requests
		}
		split := cost.CostByFamily(pricedRequests(mine))
		item := SessionListItem{
			ID:              view.ID,
			Project:         view.Project,
			Cwd:             view.Cwd,
			Name:            view.Name,
			Branch:          view.Branch,
			Repo:            view.Repo,
			ParentSessionID: view.ParentSessionID,
			StartedAt:       view.StartedAt,
			EndedAt:         view.EndedAt,
			Live:            view.Live,
			Status:          view.Status,
			Activity:        view.Activity,
			Model:           view.Model,
			AgentCount:      int64(len(view.Agents)),
			LiveAgentCount:  view.LiveAgentCount,
			RequestCount:    view.RequestCount,
			Tokens:          shared.TotalTokens(view.Tokens),
			LastAt:          state.lastAt,
			External:        view.External,
			Title:           title,
			CostUsd:         cost.CostOfAll(pricedRequests(mine)),
			CostSplitUsd:    &split,
		}
		item.Now, item.Context = cardPresence(mine, view.Live)
		items = append(items, item)
	}
	slices.SortStableFunc(items, func(a, b SessionListItem) int { return cmp.Compare(b.LastAt, a.LastAt) })
	return items
}

// SessionCount is how many sessions the store knows, without building a single view:
// what a health check reads.
func (s *Store) SessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// SessionDetail is one session's card, or nil when the store holds no such session.
func (s *Store) SessionDetail(id string) *shared.SessionView {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[id]
	if !ok {
		return nil
	}
	view := *s.sessionViewLocked(state)
	view.Agents = slices.Clone(view.Agents)
	return &view
}

// sessionViewLocked is the session's view, built once per version and shared by every
// read inside it.
func (s *Store) sessionViewLocked(state *sessionState) *shared.SessionView {
	if s.views == nil || s.views.version != s.ver {
		s.views = &viewsCache{version: s.ver, bySession: make(map[string]*shared.SessionView)}
	}
	if cached, ok := s.views.bySession[state.id]; ok {
		return cached
	}
	view := s.buildSessionView(state, s.projectionLocked())
	s.views.bySession[state.id] = &view
	return &view
}

// currentCall is the session's newest main-agent request that is one call and not a run
// total: what the rail card's now line reads and what its context gauge measures. Nil
// when the session has no such request — a subagent's calls never speak for the session,
// and a run total is no one call's context.
func currentCall(requests []*shared.RequestRecord) *shared.RequestRecord {
	var current *shared.RequestRecord
	for _, request := range requests {
		if request.AgentID != "main" || request.Totals {
			continue
		}
		if current == nil || request.Ts >= current.Ts {
			current = request
		}
	}
	return current
}

// cardPresence is the rail card's now line and context gauge, from the session's own
// requests: its newest main call, and how full that call's context window is. Both nil
// once the session is no longer live, or when it has no main-agent call to quote.
func cardPresence(requests []*shared.RequestRecord, live bool) (*SessionNow, *SessionContext) {
	if !live {
		return nil, nil
	}
	current := currentCall(requests)
	if current == nil {
		return nil, nil
	}
	return &SessionNow{What: current.What, Ts: current.Ts},
		&SessionContext{Used: shared.ContextOf(current), Window: shared.ContextWindow(requests)}
}

// buildAgentView is one agent's row in a session card.
func (s *Store) buildAgentView(sessionID string, agent *agentState, slice *sessionSlice) shared.AgentView {
	var mine *agentSlice
	if slice != nil {
		mine = slice.byAgent[agent.id]
	}
	view := shared.AgentView{
		ID:          agent.id,
		SessionID:   sessionID,
		ParentID:    agent.parentID,
		Kind:        agent.kind,
		Name:        agentNameOf(agent),
		AgentType:   agent.agentType,
		Description: agent.description,
		Model:       agent.model,
		Errors:      s.noticesOfLocked(sessionID, &agent.id),
		Tokens:      shared.ZeroTokens,
		Live:        agent.live,
	}
	if mine == nil {
		view.LastAt = firstIntPtr(agent.startedAt, agent.endedAt)
		return view
	}
	view.Requests = int64(len(mine.requests))
	view.Errors += mine.failed
	view.Tools = int64(len(mine.tools))
	view.Tokens = mine.tokens
	view.CostUsd = cost.CostOfAll(pricedRequests(mine.requests))
	if mine.lastRequest != nil {
		view.Model = strPtr(mine.lastRequest.Model)
	}
	if mine.lastTs > 0 {
		view.LastAt = int64Ptr(mine.lastTs)
	} else {
		view.LastAt = firstIntPtr(agent.startedAt, agent.endedAt)
	}
	latencies := latenciesOf(mine.requests)
	if len(latencies) >= minP95Samples {
		view.LatencyP95 = Percentile(latencies, 95)
	}
	return view
}

// agentViewLastAt is an agent row's clock, zero when it never ticked.
func agentViewLastAt(view *shared.AgentView) int64 {
	if view.LastAt != nil {
		return *view.LastAt
	}
	return 0
}

// buildSessionView is one session's card in the dashboard.
func (s *Store) buildSessionView(state *sessionState, proj map[string]*sessionSlice) shared.SessionView {
	slice := proj[state.id]
	agents := make([]shared.AgentView, 0, len(state.agentOrder.ids))
	for _, id := range state.agentOrder.ids {
		if agent, ok := state.agents[id]; ok {
			agents = append(agents, s.buildAgentView(state.id, agent, slice))
		}
	}
	slices.SortStableFunc(agents, func(a, b shared.AgentView) int {
		return cmp.Compare(agentViewLastAt(&b), agentViewLastAt(&a))
	})
	// The session runs what its main agent runs (a job's own agent when the session is a
	// job): naming it after the newest request from any agent would show an Opus session
	// as its GLM subagent.
	var lead *shared.AgentView
	for i := range agents {
		if state.external && agents[i].Kind == shared.KindExternal {
			lead = &agents[i]
			break
		}
		if !state.external && agents[i].ID == "main" {
			lead = &agents[i]
			break
		}
	}
	var lastRequest *shared.RequestRecord
	if slice != nil && len(slice.requests) > 0 {
		lastRequest = slice.requests[len(slice.requests)-1]
	}
	view := shared.SessionView{
		ID:              state.id,
		Cwd:             state.cwd,
		Project:         projectOf(state.cwd),
		Name:            s.viewNameLocked(state),
		Branch:          state.branch,
		Repo:            s.repoForLocked(state),
		ParentSessionID: state.parentSessionID,
		// a session the registry lists (or listed) starts when the registry says it does:
		// the records' own earliest timestamp can reach back through resumes, past the
		// session actually open
		StartedAt: firstIntPtr(state.registryStartedAt, state.startedAt),
		EndedAt:   viewEndedAt(state),
		Live:      state.live,
		Activity:  s.activityOfLocked(slice, state),
		CcVersion: state.ccVersion,
		Agents:    agents,
		External:  state.external,
	}
	if state.live {
		view.Status = state.status
	}
	var model *string
	if lead != nil && lead.Model != nil {
		model = lead.Model
	} else if state.model != nil {
		model = state.model
	} else if lastRequest != nil {
		model = strPtr(lastRequest.Model)
	}
	view.Model = model
	switch {
	case state.upstream != nil:
		view.Upstream = *state.upstream
	case lastRequest != nil:
		view.Upstream = lastRequest.Upstream
	}
	if slice != nil {
		view.RequestCount = int64(len(slice.requests))
		view.ErrorCount = slice.failed
		view.ToolCount = int64(len(slice.tools))
		view.Tokens = slice.tokens
	}
	view.ErrorCount += s.noticesOfLocked(state.id, nil)
	var liveAgents int64
	for i := range agents {
		if agents[i].Live {
			liveAgents++
		}
	}
	view.LiveAgentCount = liveAgents
	return view
}

// activityOfLocked is the session's sparkline over its own requests.
func (s *Store) activityOfLocked(slice *sessionSlice, state *sessionState) shared.Activity {
	var requests []*shared.RequestRecord
	if slice != nil {
		requests = slice.requests
	}
	rows := make([]ActivityRequest, 0, len(requests))
	for _, held := range requests {
		rows = append(rows, ActivityRequest{Ts: held.Ts, Model: held.Model})
	}
	return ActivityOf(rows, ActivityWindow{
		Live:  state.live,
		Start: state.startedAt,
		Last:  state.lastAt,
		Now:   s.nowMillis(),
	})
}

// viewNameLocked is the view name, in the order anyone may name a session: the user's
// registry name, the transcript's titles (custom title, agent name, AI title, slug), the
// registry's own derived name, the repo's name, and finally the id's first eight
// characters — never nil, never a path. The unattached pseudo session always reads as
// its own group, never as a project or an id.
func (s *Store) viewNameLocked(state *sessionState) *string {
	if state.id == shared.OutsideSession {
		return strPtr(shared.UnattachedName)
	}
	if name := firstStrPtr(state.registryName, sessionName(state.titles), state.registryDerivedName); name != nil {
		return name
	}
	if repo := s.repoForLocked(state); repo != nil {
		return strPtr(shared.RepoName(*repo))
	}
	return strPtr("Session " + jsscalar.UTF16Slice(state.id, 0, 8))
}

// repoForLocked is the repo a session works in: a job's own workspace repo, else the
// nearest .git above the cwd — walked once per cwd, never per view.
func (s *Store) repoForLocked(state *sessionState) *string {
	if state.repo != nil {
		return state.repo
	}
	if state.cwd == nil {
		return nil
	}
	cached, ok := s.repoRoot[*state.cwd]
	if !ok {
		if repo, found := shared.RepoOf(*state.cwd, osRepoFS{}); found {
			cached = &repo
		}
		s.repoRoot[*state.cwd] = cached
	}
	return cached
}

// viewEndedAt is the end the view states. The unattached traffic is never open: it reads
// as ended at its last activity, so history keeps it out of the live scope and groups it
// under its own name.
func viewEndedAt(state *sessionState) *int64 {
	if state.id == shared.OutsideSession && state.lastAt > 0 {
		ended := int64(0)
		if state.endedAt != nil {
			ended = *state.endedAt
		}
		return int64Ptr(max(ended, state.lastAt))
	}
	return state.endedAt
}

// projectOf is the last non-empty segment of the cwd: the name the rail card leads with.
func projectOf(cwd *string) *string {
	if cwd == nil || *cwd == "" {
		return nil
	}
	return strPtr(shared.RepoName(*cwd))
}

// earlierStarted keeps the earliest start a source named.
func earlierStarted(state *sessionState, startedAt *int64) {
	if startedAt == nil {
		return
	}
	if state.startedAt == nil || *startedAt < *state.startedAt {
		state.startedAt = startedAt
	}
}

// applySessionUpsertLocked writes a session's facts; the first writer wins each fact.
func applySessionUpsertLocked(state *sessionState, input SessionUpsert) {
	if input.Cwd != nil && state.cwd == nil {
		state.cwd = input.Cwd
	}
	if input.Upstream != nil && state.upstream == nil {
		state.upstream = input.Upstream
	}
	if input.CcVersion != nil && state.ccVersion == nil {
		state.ccVersion = input.CcVersion
	}
	earlierStarted(state, input.StartedAt)
	if input.External != nil {
		state.external = *input.External
		// a job's lifecycle is the job files' business, not the registry's: it runs while
		// its job says so
		if *input.External {
			state.live = true
		}
	}
	applySessionNamingLocked(state, input)
}

// applySessionNamingLocked is the naming side of an upsert: names and branch change as
// the session runs, repo and parent do not. A blank value changes nothing, so an empty
// line can never erase a name.
func applySessionNamingLocked(state *sessionState, input SessionUpsert) {
	if input.CustomTitle != nil && *input.CustomTitle != "" {
		state.titles.customTitle = input.CustomTitle
	}
	if input.AgentName != nil && *input.AgentName != "" {
		state.titles.agentName = input.AgentName
	}
	if input.AiTitle != nil && *input.AiTitle != "" {
		state.titles.aiTitle = input.AiTitle
	}
	if input.Slug != nil && *input.Slug != "" {
		state.titles.slug = input.Slug
	}
	if input.Branch != nil && *input.Branch != "" {
		state.branch = input.Branch
	}
	if input.Repo != nil {
		state.repo = input.Repo
	}
	if input.ParentSessionID != nil {
		state.parentSessionID = input.ParentSessionID
	}
}

// sessionName is the first non-empty of the four names a transcript can give a session,
// in that order.
func sessionName(titles sessionTitles) *string {
	return firstStrPtr(titles.customTitle, titles.agentName, titles.aiTitle, titles.slug)
}

// registryStatusOf is the registry's status word as the view states it: busy is working,
// idle is idle, the rest unknown.
func registryStatusOf(status *string) *shared.SessionStatus {
	if status == nil {
		return nil
	}
	switch *status {
	case "busy":
		return statusPtr(shared.StatusWorking)
	case "idle":
		return statusPtr(shared.StatusIdle)
	default:
		return nil
	}
}

// agentNameOf is an agent's display name. A subagent is its meta file's type and task
// ("Explore — find the flaky test"), else the head of its first prompt, else the name a
// source gave it, else just "Subagent" — never the raw agent-… id. Main and job agents
// keep the names their own sources gave them.
func agentNameOf(agent *agentState) *string {
	if agent.kind == shared.KindMain {
		return firstStrPtr(agent.name, strPtr("main"))
	}
	if agent.agentType != nil {
		if agent.description == nil || *agent.description == "" {
			return agent.agentType
		}
		return strPtr(*agent.agentType + " — " + *agent.description)
	}
	if agent.title != nil && *agent.title != "" {
		return agent.title
	}
	if agent.name != nil {
		return agent.name
	}
	if agent.kind == shared.KindSubagent {
		return strPtr("Subagent")
	}
	return strPtr(agent.id)
}

// applyAgentUpsertLocked writes one agent's facts; the naming side updates as the agent
// runs, the model seeds once.
func applyAgentUpsertLocked(target *agentState, input AgentUpsert) {
	if input.ParentID != nil {
		target.parentID = *input.ParentID
	}
	if input.Kind != nil {
		target.kind = *input.Kind
	}
	if input.Name != nil {
		target.name = input.Name
	}
	if input.AgentType != nil {
		target.agentType = input.AgentType
	}
	if input.Description != nil {
		target.description = input.Description
	}
	if input.Model != nil && target.model == nil {
		target.model = input.Model
	}
	if input.Title != nil {
		target.title = *input.Title
	}
	if input.Kind != nil && *input.Kind == shared.KindExternal {
		target.live = true
	}
}

// applyLineMetaLocked fills the session-scoped facts a spool line can fill once (first
// writer wins, later lines keep them).
func applyLineMetaLocked(state *sessionState, line *shared.SpoolLine) {
	if line.Cwd != nil && state.cwd == nil {
		state.cwd = line.Cwd
	}
	if line.BaseURL != nil && state.upstream == nil {
		state.upstream = line.BaseURL
	}
	if line.CcVersion != nil && state.ccVersion == nil {
		state.ccVersion = line.CcVersion
	}
	if line.ModelEnv != nil {
		if model, ok := line.ModelEnv["ANTHROPIC_MODEL"]; ok && state.model == nil {
			state.model = strPtr(model)
		}
	}
}

// countNoticeLocked counts one error notice under its agent, the main agent when the
// line named none.
func (s *Store) countNoticeLocked(sessionID string, agentID *string) {
	byAgent := s.notices[sessionID]
	if byAgent == nil {
		byAgent = make(map[string]int64)
		s.notices[sessionID] = byAgent
	}
	key := "main"
	if agentID != nil {
		key = *agentID
	}
	byAgent[key]++
}

// noticesOfLocked is the error notices Claude Code wrote itself for a session, or one
// agent of it: errors with no model request behind them.
func (s *Store) noticesOfLocked(sessionID string, agentID *string) int64 {
	byAgent := s.notices[sessionID]
	if byAgent == nil {
		return 0
	}
	if agentID != nil {
		return byAgent[*agentID]
	}
	var total int64
	for _, count := range byAgent {
		total += count
	}
	return total
}

// applyLifecycleLocked reads session and agent lifecycle off one spool line.
func (s *Store) applyLifecycleLocked(line *shared.SpoolLine, ts int64, sessionID, agentID *string) {
	if sessionID == nil {
		return
	}
	switch line.Event {
	case "SessionStart":
		s.markSessionLiveLocked(*sessionID, ts)
	case "SessionEnd":
		s.markSessionEndedLocked(*sessionID, ts)
	case "SubagentStart":
		if agentID != nil {
			s.markSubagentLiveLocked(line, ts, *sessionID, *agentID)
		}
	case "SubagentStop":
		if agentID != nil {
			s.markSubagentEndedLocked(*sessionID, *agentID, ts)
		}
	}
}

func (s *Store) markSessionLiveLocked(sessionID string, ts int64) {
	state := s.sessionLocked(sessionID)
	state.live = true
	state.endedAt = nil
	if state.startedAt == nil || ts < *state.startedAt {
		state.startedAt = int64Ptr(ts)
	}
	s.agentLocked(sessionID, "main").live = true
}

func (s *Store) markSessionEndedLocked(sessionID string, ts int64) {
	state := s.sessionLocked(sessionID)
	state.live = false
	state.endedAt = int64Ptr(ts)
	for _, id := range state.agentOrder.ids {
		if agent, ok := state.agents[id]; ok {
			agent.live = false
		}
	}
}

func (s *Store) markSubagentLiveLocked(line *shared.SpoolLine, ts int64, sessionID, agentID string) {
	target := s.agentLocked(sessionID, agentID)
	target.live = true
	target.startedAt = int64Ptr(ts)
	if line.AgentType != nil {
		target.name = line.AgentType
	}
	if line.Prompt != nil {
		target.title = strPtr(shared.PromptTitle(*line.Prompt))
	}
}

func (s *Store) markSubagentEndedLocked(sessionID, agentID string, ts int64) {
	target := s.agentLocked(sessionID, agentID)
	target.live = false
	target.endedAt = int64Ptr(ts)
}

// applyToolEventLocked pairs tool events across spool lines: PreToolUse seeds a start,
// the completion events close it.
func (s *Store) applyToolEventLocked(line *shared.SpoolLine, ts int64, sessionID, agentID *string, change *Change) {
	switch line.Event {
	case "PreToolUse", "PostToolUse", "PostToolUseFailure":
	default:
		return
	}
	if sessionID == nil {
		return
	}
	name := "unknown"
	if line.ToolName != nil {
		name = *line.ToolName
	}
	toolUseID := fmt.Sprintf("hook:%s:%s:%d", *sessionID, name, ts)
	if line.ToolUseID != nil {
		toolUseID = *line.ToolUseID
	}
	if line.Event == "PreToolUse" {
		s.pendingStarts[toolUseID] = ts
		return
	}
	startedAt := ts
	if at, ok := s.pendingStarts[toolUseID]; ok {
		startedAt = at
	}
	delete(s.pendingStarts, toolUseID)
	var duration *int64
	if existing, ok := s.tools[toolUseID]; ok && existing.record.DurationMs != nil {
		duration = existing.record.DurationMs
	} else if startedAt < ts {
		duration = int64Ptr(ts - startedAt)
	}
	record := shared.ToolCallRecord{
		ID:         toolUseID,
		SessionID:  *sessionID,
		AgentID:    agentID,
		Name:       name,
		StartedAt:  startedAt,
		DurationMs: duration,
		Ok:         line.Event == "PostToolUse",
	}
	s.storeToolLocked(record)
	change.Tools = append(change.Tools, record)
}

// labelFor is a one-line label for the timeline: what a hook event was about, without
// dumping its payload.
func labelFor(line *shared.SpoolLine) *string {
	if line.Prompt != nil {
		return strPtr(jsscalar.UTF16Slice(*line.Prompt, 0, 120))
	}
	if line.Message != nil {
		return strPtr(jsscalar.UTF16Slice(*line.Message, 0, 120))
	}
	if message, ok := extraString(line, "message"); ok {
		return strPtr(jsscalar.UTF16Slice(message, 0, 120))
	}
	if line.Model != nil && line.Upstream != nil {
		return strPtr(*line.Model + " → " + *line.Upstream)
	}
	if line.ToolName != nil {
		return strPtr(toolLabel(*line.ToolName, line.ToolInput))
	}
	for _, field := range []*string{line.AgentType, line.Reason, line.Trigger, line.Source} {
		if field != nil {
			return field
		}
	}
	return nil
}

// extraString reads an unknown field the line carried, when it holds a JSON string.
func extraString(line *shared.SpoolLine, key string) (string, bool) {
	raw, ok := line.Extra[key]
	if !ok {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	return text, true
}

// toolLabel is "Read /a/b.ts": the tool plus the most telling string of its input.
func toolLabel(name string, input any) string {
	object, ok := input.(map[string]any)
	if !ok {
		return name
	}
	for _, key := range []string{"file_path", "path", "command", "pattern", "url", "description"} {
		if value, ok := object[key].(string); ok && value != "" {
			return name + " " + jsscalar.UTF16Slice(value, 0, 90)
		}
	}
	return name
}

// tsToMs reads the line's timestamp the way Date.parse does over the ISO strings the
// writers stamp; anything it cannot parse reads as absent.
func tsToMs(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// Requests is the newest limit records matching the filter, newest first, walked off the
// cached newest-first order: no per-call sort, no full copy — the walk stops once the
// page is full.
func (s *Store) Requests(filter RequestFilter) []shared.RequestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return newestPage(s.newestFirstLocked().requests, filter.page(), filter.matches)
}

// RequestsFor is every record matching the filter, in arrival order.
func (s *Store) RequestsFor(filter RequestFilter) []shared.RequestRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]shared.RequestRecord, 0, len(s.requests))
	for _, held := range s.requestsInOrderLocked() {
		if filter.matches(&held.record) {
			out = append(out, held.record)
		}
	}
	return out
}

// page is the filter's page size, the default when it named none.
func (f RequestFilter) page() int64 {
	if f.Limit != nil {
		return *f.Limit
	}
	return defaultPage
}

// Events is the newest limit events matching the filter: the last page of the
// append-only log, newest (last-written) first, walked backwards so no filter copy of
// the whole log is made.
func (s *Store) Events(filter EventsFilter) []shared.EventRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := int64(defaultPage)
	if filter.Limit != nil {
		limit = *filter.Limit
	}
	return lastPage(s.events, limit, filter.keeps)
}

// Tools is the newest limit tool calls matching the filter, newest first; Failed keeps
// only the ones that errored.
func (s *Store) Tools(filter ToolsFilter) []shared.ToolCallRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := int64(defaultPage)
	if filter.Limit != nil {
		limit = *filter.Limit
	}
	return newestPage(s.newestFirstLocked().tools, limit, filter.keeps)
}

// Models is the models tab's tables. Without a scope this is the whole store's cached
// answer — the one the snapshot pushes; a scope (a window, the picked sessions)
// aggregates those requests fresh.
func (s *Store) Models(scope *ModelsScope) ModelsAnswer {
	s.mu.Lock()
	defer s.mu.Unlock()
	unscoped := scope == nil || (scope.From == nil && scope.To == nil && scope.Sessions == nil)
	if unscoped {
		answer := derivedOf(&s.modelsCache, s.ver, s.nowMillis(), func() ModelsAnswer {
			requests := s.allRequestsLocked()
			return ModelsAnswer{
				Models:    ModelRows(requests),
				Upstreams: UpstreamRows(requests),
				Tools:     ToolCounts(s.allToolsLocked()),
			}
		})
		return ModelsAnswer{
			Models:    slices.Clone(answer.Models),
			Upstreams: slices.Clone(answer.Upstreams),
			Tools:     slices.Clone(answer.Tools),
		}
	}
	// a scoped answer is bespoke to the ask, so it builds fresh: the requests inside the
	// window and the sessions named, the tool counts staying the store-wide ones the
	// tools tab shows
	requests := make([]*shared.RequestRecord, 0, len(s.requests))
	for _, held := range s.requestsInOrderLocked() {
		record := held.record
		if (scope.From == nil || record.Ts >= *scope.From) &&
			(scope.To == nil || record.Ts <= *scope.To) &&
			(scope.Sessions == nil || slices.Contains(scope.Sessions, record.SessionID)) {
			requests = append(requests, &held.record)
		}
	}
	return ModelsAnswer{
		Models:    ModelRows(requests),
		Upstreams: UpstreamRows(requests),
		Tools:     ToolCounts(s.allToolsLocked()),
	}
}

// AddAPIError keeps a 429/5xx seen anywhere (transcript retry, route line, router
// event) for the retry-storm alert.
func (s *Store) AddAPIError(record shared.APIErrorRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiErrors = append(s.apiErrors, record)
	s.apiErrors = trimSideRecords(s.apiErrors)
	s.ver++
}

// AddRouterEvent keeps a router health event (fallback, refusal, rate limit, budget
// stop, restart).
func (s *Store) AddRouterEvent(record shared.RouterEventRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routerEvents = append(s.routerEvents, record)
	s.routerEvents = trimSideRecords(s.routerEvents)
	s.ver++
}

// trimSideRecords keeps the newest maxSideRecords of a side collection.
func trimSideRecords[T any](records []T) []T {
	if len(records) <= maxSideRecords {
		return records
	}
	return records[len(records)-maxSideRecords:]
}

// APIErrors is the kept API errors.
func (s *Store) APIErrors() []shared.APIErrorRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.apiErrors)
}

// RouterEvents is the kept router health events.
func (s *Store) RouterEvents() []shared.RouterEventRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.routerEvents)
}

// Raw is every request, tool call and event held, unsorted. The records go out as the
// caller's own copies: the alert engine reads them without the lock.
func (s *Store) Raw() RawRecords {
	s.mu.RLock()
	defer s.mu.RUnlock()
	heldRequests := s.allRequestsLocked()
	heldTools := s.allToolsLocked()
	requests := make([]shared.RequestRecord, len(heldRequests))
	for i, held := range heldRequests {
		requests[i] = *held
	}
	tools := make([]shared.ToolCallRecord, len(heldTools))
	for i, held := range heldTools {
		tools[i] = *held
	}
	return RawRecords{
		Requests: requests,
		Tools:    tools,
		Events:   slices.Clone(s.events),
	}
}

// OnUpdate registers a listener for every change; the returned function unregisters it.
func (s *Store) OnUpdate(listener func(Change)) func() {
	s.mu.Lock()
	entry := &listenerEntry{fn: listener}
	s.listeners = append(s.listeners, entry)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.listeners = slices.DeleteFunc(s.listeners, func(other *listenerEntry) bool {
			return other == entry
		})
	}
}

// Version is the store's version, one per change.
func (s *Store) Version() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ver
}
