// The bridge from the live store to the history file: it queues what every store update
// holds and writes the batch in one transaction per flush — at most one every 500 ms, plus
// a final flush on stop — mapping the store's own views (never a re-read of the
// transcripts) into node, request, tool and event rows, and deriving node rows only for
// the sessions the batch touched. A request-heavy update skips the views — request rows
// already carry everything they need — and only a naming or lifecycle change, or the first
// sighting of a session or agent, re-reads the session detail. Nothing is dropped: records
// queue until they are written. Every failure is contained: the writer logs one line and
// detaches, and radar runs on without history.
package history

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// flushEvery is how long updates may queue before the next flush writes them all in one
// transaction.
const flushEvery = 500 * time.Millisecond

// HistorySink is the history surface the writer writes through: the store's own type, or a
// wrapper that counts or fails.
type HistorySink interface {
	Transaction(fn func() error) error
	UpsertNode(n NodeUpsert) error
	PutRequest(nodeID string, r shared.RequestRecord) error
	PutContent(requestID string, c ContentSides) error
	PutTool(nodeID string, t shared.ToolCallRecord) error
	AddEvent(nodeID *string, e shared.EventRecord) error
	PutCapture(c shared.CaptureRecord) error
	Close() error
}

// HistoryWriterOptions wires the writer: the store it follows and the history it writes.
// Log receives the one line a failed write leaves (stderr when nil).
type HistoryWriterOptions struct {
	Store   *store.Store
	History HistorySink
	Log     func(line string)
}

// HistoryWriter is the running bridge from one store into one history file. The flush loop
// owns the beat; the store's own goroutines only queue, so a mutation never waits on a
// write, and a write never runs on the mutator's goroutine.
type HistoryWriter struct {
	store   *store.Store
	history HistorySink
	log     func(line string)

	mu      sync.Mutex
	broken  bool
	stopped bool
	// dirty lists the sessions the queued updates touched, in arrival order; the ones a
	// record-less update touched must re-read their views.
	dirty  stringSet
	forced stringSet
	// queued holds every record the queued updates carry, in arrival order; written once,
	// at the next flush.
	queuedRequests []shared.RequestRecord
	queuedContent  []store.ContentRecord
	queuedTools    []shared.ToolCallRecord
	queuedEvents   []shared.EventRecord
	queuedCaptures []shared.CaptureRecord
	// known is what the writer remembers about a session it has mapped: its node ids, its
	// job flag and its lead agent.
	known map[string]knownSession

	unsubscribe func()
	wake        chan struct{}
	stop        chan struct{}
	stopOnce    sync.Once
	shutOnce    sync.Once
	done        chan struct{}
}

// knownSession is what the writer remembers about a session it has mapped: its node ids,
// its job flag and its lead agent.
type knownSession struct {
	external bool
	lead     string
	nodes    stringSet
}

// changeBatch is everything one flush holds.
type changeBatch struct {
	touched  []string
	requests []shared.RequestRecord
	content  []store.ContentRecord
	tools    []shared.ToolCallRecord
	events   []shared.EventRecord
	captures []shared.CaptureRecord
}

// stringSet is a set of ids in first-addition order.
type stringSet struct {
	order []string
	has   map[string]struct{}
}

func (s *stringSet) add(value string) {
	if s.has == nil {
		s.has = make(map[string]struct{})
	}
	if _, ok := s.has[value]; ok {
		return
	}
	s.has[value] = struct{}{}
	s.order = append(s.order, value)
}

func (s *stringSet) holds(value string) bool {
	_, ok := s.has[value]
	return ok
}

func (s *stringSet) snapshot() []string { return slices.Clone(s.order) }

func (s *stringSet) size() int { return len(s.order) }

func (s *stringSet) clear() {
	s.order = nil
	s.has = nil
}

// AttachHistoryWriter follows the store into the history file. The writer ends with ctx
// exactly as it does on Stop: it detaches, flushes what is queued and closes the file. It
// answers nil when either side is missing.
func AttachHistoryWriter(ctx context.Context, options HistoryWriterOptions) *HistoryWriter {
	if options.Store == nil || options.History == nil {
		return nil
	}
	log := options.Log
	if log == nil {
		log = func(line string) { fmt.Fprintln(os.Stderr, line) }
	}
	w := &HistoryWriter{
		store:   options.Store,
		history: options.History,
		log:     log,
		known:   make(map[string]knownSession),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	w.unsubscribe = options.Store.OnUpdate(w.enqueue)
	go w.run(ctx)
	return w
}

// run owns the beat: the first update after a flush arms it, the ones that follow never
// push it later, and it fires into a plain flush. The loop and its timer end when ctx does
// or Stop runs.
func (w *HistoryWriter) run(ctx context.Context) {
	defer close(w.done)
	timer := time.NewTimer(flushEvery)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	armed := false
	for {
		select {
		case <-ctx.Done():
			w.shutdown()
			return
		case <-w.stop:
			return
		case <-w.wake:
			if !armed {
				armed = true
				timer.Reset(flushEvery)
			}
		case <-timer.C:
			armed = false
			w.Flush()
		}
	}
}

// Flush writes everything queued so far, now: the shutdown path and tests use it. An empty
// queue writes nothing.
func (w *HistoryWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	batch, reRead, ok := w.takeBatchLocked()
	if !ok {
		return
	}
	if err := w.transact(batch, reRead); err != nil {
		// the store must never learn that history failed; run without it from here on
		w.broken = true
		unsubscribe := w.unsubscribe
		w.unsubscribe = nil
		if unsubscribe != nil {
			unsubscribe()
		}
		w.log(fmt.Sprintf("radar: history write failed, history disabled (%v)", err))
	}
}

// transact runs one batch's write inside the history's transaction; a panic inside the
// sink reads as a failed write, so a broken sink costs the writer its history, never its
// lock.
func (w *HistoryWriter) transact(batch changeBatch, reRead []string) (err error) {
	defer func() {
		if caught := recover(); caught != nil {
			err = fmt.Errorf("%v", caught)
		}
	}()
	return w.history.Transaction(func() error { return w.applyBatchLocked(batch, reRead) })
}

// Stop detaches from the store, flushes what is queued, and closes the history file.
// Idempotent: shutdown paths may both try (a self-removal and the caller's stop).
func (w *HistoryWriter) Stop() {
	w.shutdown()
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

// Done closes once the writer's flush loop is out.
func (w *HistoryWriter) Done() <-chan struct{} { return w.done }

// shutdown is the one detach-flush-close path Stop and the context's end share.
func (w *HistoryWriter) shutdown() {
	w.shutOnce.Do(func() {
		w.mu.Lock()
		w.stopped = true
		unsubscribe := w.unsubscribe
		w.unsubscribe = nil
		w.mu.Unlock()
		if unsubscribe != nil {
			unsubscribe()
		}
		w.Flush()
		_ = w.history.Close()
	})
}

// enqueue is the store update path: it only queues, never writes.
func (w *HistoryWriter) enqueue(change store.Change) {
	w.mu.Lock()
	if w.broken || w.stopped {
		w.mu.Unlock()
		return
	}
	w.markTouchedLocked(change)
	w.queuedRequests = append(w.queuedRequests, change.Requests...)
	w.queuedContent = append(w.queuedContent, change.Content...)
	w.queuedTools = append(w.queuedTools, change.Tools...)
	w.queuedEvents = append(w.queuedEvents, change.Events...)
	w.queuedCaptures = append(w.queuedCaptures, change.Captures...)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// markTouchedLocked marks the sessions the change touched; a record-less change (naming,
// lifecycle) must re-read views.
func (w *HistoryWriter) markTouchedLocked(change store.Change) {
	lifecycle := len(change.Requests) == 0 && len(change.Tools) == 0 && len(change.Events) == 0
	for _, sessionID := range touchedOf(change) {
		w.dirty.add(sessionID)
		if lifecycle {
			w.forced.add(sessionID)
		}
	}
}

// takeBatchLocked answers everything one batch holds plus the sessions whose views it must
// re-read; ok is false when nothing queued.
func (w *HistoryWriter) takeBatchLocked() (changeBatch, []string, bool) {
	// a batch of captures alone (no session touched) is still worth writing now
	if w.broken || (w.dirty.size() == 0 && len(w.queuedCaptures) == 0) {
		return changeBatch{}, nil, false
	}
	batch := changeBatch{
		touched:  w.dirty.snapshot(),
		requests: w.queuedRequests,
		content:  w.queuedContent,
		tools:    w.queuedTools,
		events:   w.queuedEvents,
		captures: w.queuedCaptures,
	}
	reRead := w.forced.snapshot()
	w.dirty.clear()
	w.forced.clear()
	w.queuedRequests = nil
	w.queuedContent = nil
	w.queuedTools = nil
	w.queuedEvents = nil
	w.queuedCaptures = nil
	return batch, reRead, true
}

// touchedOf lists the sessions a change touched, from its payload and its records, in
// first-addition order.
func touchedOf(change store.Change) []string {
	var touched stringSet
	for _, sessionID := range change.Touched {
		touched.add(sessionID)
	}
	for _, request := range change.Requests {
		touched.add(request.SessionID)
	}
	for _, tool := range change.Tools {
		touched.add(tool.SessionID)
	}
	for _, event := range change.Events {
		if event.SessionID != nil {
			touched.add(*event.SessionID)
		}
	}
	return touched.snapshot()
}

// nodeIdOf is a record's node id: the session's lead agent (main, or a job's own agent)
// sits on the session node, every other agent on its own /agent node. A job's phantom main
// agent collapses onto the job node.
func nodeIdOf(sessionID, agentID string, external bool, lead string) string {
	if agentID == "main" || agentID == "" || (external && agentID == lead) {
		if external {
			return "job:" + sessionID
		}
		return sessionID + "/main"
	}
	return sessionID + "/" + agentID
}

// rootUpsertOf is the session node's id, kind and parent: a job hangs under the session
// that submitted it.
func rootUpsertOf(view *shared.SessionView, lead string, lastAt *int64) NodeUpsert {
	kind := shared.NodeMain
	if view.External {
		kind = shared.NodeJob
	}
	var parentID *string
	var parentSessionID *string
	if view.External && view.ParentSessionID != nil {
		parent := *view.ParentSessionID + "/main"
		parentID = &parent
		parentSessionID = view.ParentSessionID
	}
	ended := ClearEnd()
	if view.EndedAt != nil {
		ended = EndAt(*view.EndedAt)
	}
	return NodeUpsert{
		ID:              nodeIdOf(view.ID, "main", view.External, lead),
		Kind:            kind,
		SessionID:       view.ID,
		AgentID:         lead,
		ParentID:        parentID,
		Name:            view.Name,
		Repo:            view.Repo,
		Branch:          view.Branch,
		Project:         view.Project,
		Cwd:             view.Cwd,
		Model:           view.Model,
		ParentSessionID: parentSessionID,
		StartedAt:       view.StartedAt,
		LastAt:          lastAt,
		EndedAt:         ended,
	}
}

// agentUpsertOf maps one agent view onto its node: subagent nodes are named after their
// agent view, and the session node carries the session's own facts.
func agentUpsertOf(view *shared.SessionView, agent shared.AgentView, rootID, lead string) NodeUpsert {
	id := nodeIdOf(view.ID, agent.ID, view.External, lead)
	parent := &rootID
	if agent.ParentID != nil && *agent.ParentID != agent.ID {
		named := nodeIdOf(view.ID, *agent.ParentID, view.External, lead)
		parent = &named
	}
	if *parent == id {
		parent = nil
	}
	return NodeUpsert{
		ID:          id,
		Kind:        shared.NodeSubagent,
		SessionID:   view.ID,
		AgentID:     agent.ID,
		ParentID:    parent,
		Name:        agent.Name,
		AgentType:   agent.AgentType,
		Description: agent.Description,
		Model:       agent.Model,
		LastAt:      agent.LastAt,
	}
}

// leadOf is the lead agent: the one whose requests name the session's model — a job's own
// agent, else main.
func leadOf(view *shared.SessionView) string {
	if view.External {
		for _, agent := range view.Agents {
			if agent.Kind == shared.KindExternal {
				return agent.ID
			}
		}
		return view.ID
	}
	for _, agent := range view.Agents {
		if agent.ID == "main" {
			return agent.ID
		}
	}
	return "main"
}

// lastAtOf is the newest activity the views and this change can vouch for, for the node's
// last_at clock.
func lastAtOf(view *shared.SessionView, change changeBatch) *int64 {
	var last *int64
	if view.StartedAt != nil {
		value := *view.StartedAt
		last = &value
	}
	newer := func(ts int64) {
		if last == nil || ts > *last {
			value := ts
			last = &value
		}
	}
	for _, agent := range view.Agents {
		if agent.LastAt != nil {
			newer(*agent.LastAt)
		}
	}
	for _, request := range change.requests {
		if request.SessionID == view.ID {
			newer(request.Ts)
		}
	}
	for _, tool := range change.tools {
		if tool.SessionID == view.ID {
			newer(tool.StartedAt)
		}
	}
	for _, event := range change.events {
		if event.SessionID != nil && *event.SessionID == view.ID {
			newer(event.Ts)
		}
	}
	return last
}

// refreshSessionLocked maps one session and its agents from the store's view; it ends the
// node when the view says ended.
func (w *HistoryWriter) refreshSessionLocked(sessionID string, change changeBatch) error {
	view := w.store.SessionDetail(sessionID)
	if view == nil {
		return nil
	}
	lead := leadOf(view)
	root := rootUpsertOf(view, lead, lastAtOf(view, change))
	if err := w.history.UpsertNode(root); err != nil {
		return err
	}
	var nodes stringSet
	nodes.add(root.ID)
	for _, agent := range view.Agents {
		node := agentUpsertOf(view, agent, root.ID, lead)
		if node.ID == root.ID {
			continue
		}
		if err := w.history.UpsertNode(node); err != nil {
			return err
		}
		nodes.add(node.ID)
	}
	w.known[view.ID] = knownSession{external: view.External, lead: lead, nodes: nodes}
	return nil
}

// sessionAgent is the session and agent one record of a change names.
type sessionAgent struct {
	session string
	agent   string
}

// recordAgentsOf lists the session and agent every record of the change names, in the
// order they arrived.
func recordAgentsOf(change changeBatch) []sessionAgent {
	pairs := make([]sessionAgent, 0, len(change.requests)+len(change.tools)+len(change.events))
	for _, request := range change.requests {
		agent := request.AgentID
		if agent == "" {
			agent = "main"
		}
		pairs = append(pairs, sessionAgent{session: request.SessionID, agent: agent})
	}
	for _, tool := range change.tools {
		pairs = append(pairs, sessionAgent{session: tool.SessionID, agent: agentOf(tool.AgentID)})
	}
	for _, event := range change.events {
		if event.SessionID != nil {
			pairs = append(pairs, sessionAgent{session: *event.SessionID, agent: agentOf(event.AgentID)})
		}
	}
	return pairs
}

// agentOf is the agent a record names, main when it names none.
func agentOf(agentID *string) string {
	if agentID == nil {
		return "main"
	}
	return *agentID
}

// needsRefresh says whether a session's nodes must be re-derived: a record-bearing update
// for a session whose nodes are all known already needs no view.
func needsRefresh(known map[string]knownSession, pairs []sessionAgent, sessionID string) bool {
	state, ok := known[sessionID]
	if !ok {
		return true
	}
	for _, pair := range pairs {
		if pair.session == sessionID && !state.nodes.holds(nodeIdOf(sessionID, pair.agent, state.external, state.lead)) {
			return true
		}
	}
	return false
}

// applyBatchLocked writes one batch in one transaction: fresh nodes first, then the
// records.
func (w *HistoryWriter) applyBatchLocked(batch changeBatch, forced []string) error {
	pairs := recordAgentsOf(batch)
	var reRead stringSet
	for _, sessionID := range forced {
		reRead.add(sessionID)
	}
	for _, sessionID := range batch.touched {
		// a naming or lifecycle change carries no records: the views are the only source,
		// so re-read them
		if reRead.holds(sessionID) || needsRefresh(w.known, pairs, sessionID) {
			if err := w.refreshSessionLocked(sessionID, batch); err != nil {
				return err
			}
		}
	}
	return w.writeRecordsLocked(batch)
}

// recordNodeOf is the node id a record of a session lands on, with the defaults a session
// never mapped gets.
func recordNodeOf(known map[string]knownSession, sessionID string, agentID *string) string {
	external, lead := false, "main"
	if state, ok := known[sessionID]; ok {
		external, lead = state.external, state.lead
	}
	return nodeIdOf(sessionID, agentOf(agentID), external, lead)
}

// writeRecordsLocked lands the records beside their nodes.
func (w *HistoryWriter) writeRecordsLocked(change changeBatch) error {
	for _, request := range change.requests {
		var agent *string
		if request.AgentID != "" {
			agent = &request.AgentID
		}
		if err := w.history.PutRequest(recordNodeOf(w.known, request.SessionID, agent), request); err != nil {
			return err
		}
	}
	for _, content := range change.content {
		if err := w.history.PutContent(content.RequestID, ContentSides{
			Input: content.Input, Output: content.Output,
		}); err != nil {
			return err
		}
	}
	for _, tool := range change.tools {
		if err := w.history.PutTool(recordNodeOf(w.known, tool.SessionID, tool.AgentID), tool); err != nil {
			return err
		}
	}
	for _, event := range change.events {
		var node *string
		if event.SessionID != nil {
			id := recordNodeOf(w.known, *event.SessionID, event.AgentID)
			node = &id
		}
		if err := w.history.AddEvent(node, event); err != nil {
			return err
		}
	}
	for _, capture := range change.captures {
		if err := w.history.PutCapture(capture); err != nil {
			return err
		}
	}
	return nil
}
