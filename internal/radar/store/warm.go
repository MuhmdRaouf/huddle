package store

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"slices"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// warmBatch is the most records one step of a warm load places while holding the store's
// lock: readers and the HTTP handlers get the lock back between steps.
const warmBatch = 1_000

// WarmSource is what an earlier run kept, which the store reloads at start: today
// history.db; the store needs nothing else from it.
type WarmSource interface {
	// WarmRequests calls fn with the newest requests first, those at or after since (unix
	// ms), at most limit of them, until fn returns an error. written is the position the source's file first wrote the request
	// at, which the store keeps as the request's arrival order: a view that names the last
	// call of an agent reads the same after a restart.
	WarmRequests(since int64, limit int, fn func(record shared.RequestRecord, written int64) error) error
	// WarmTools calls fn with the tool calls that started at or after since and fall within
	// the span of the requests WarmRequests names, until fn returns an error.
	WarmTools(since int64, fn func(nodeID string, t shared.ToolCallRecord) error) error
	// WarmEvents calls fn with the timeline events at or after since, oldest first, at most
	// limit of them: the newest limit the source holds.
	WarmEvents(since int64, limit int, fn func(shared.EventRecord) error) error
	// WarmNodes calls fn with the sessions and agents that were active at or after since, the
	// sessions before their subagents, as the store's upsert inputs.
	WarmNodes(since int64, fn func(WarmNode) error) error
	// WarmEnd answers the seq and time of the newest event the source holds, whatever its
	// age, and zeros when it holds none: the live events number on from the seq, and a spool
	// line older than the window and no newer than the time is one the source already saw.
	WarmEnd() (WarmEnd, error)
}

// WarmEnd is the newest event a source holds.
type WarmEnd struct {
	Seq, Ts int64
}

// WarmNode is one node a source kept: an agent, and for a session's lead agent the session
// it leads. LastAt is the node's newest activity; EndedAt is set once the node ended.
type WarmNode struct {
	Session *SessionUpsert
	Agent   AgentUpsert
	LastAt  *int64
	EndedAt *int64
}

// WarmStats is what one warm load reloaded and how long it took.
type WarmStats struct {
	Requests, Tools, Events, Sessions, Agents int
	Took                                      time.Duration
}

// Warm reloads what the source holds from the last shared.StoreWindow into the store: the
// sessions and agents first, then the newest requests under the record cap, the tool calls
// and the room the cap leaves for the timeline. The window is cut once, from the store's
// clock, and the live events number on from the source's last seq. It runs before the live feed starts. Every step places at most warmBatch
// records under the lock and emits one Change marked Warm, which the history writer skips.
func (s *Store) Warm(ctx context.Context, src WarmSource) (WarmStats, error) {
	started := time.Now()
	load := &warmLoad{ctx: ctx, store: s, since: s.nowMillis() - shared.StoreWindow.Milliseconds()}
	err := load.run(src)
	load.stats.Took = time.Since(started)
	return load.stats, err
}

// warmRequest is a reloaded request with the position its file wrote it at.
type warmRequest struct {
	record  shared.RequestRecord
	written int64
}

// warmLoad is one Warm call's state: the batch each kind of record collects before it is
// placed, and what the later steps need from the earlier ones.
type warmLoad struct {
	ctx   context.Context
	store *Store
	stats WarmStats
	// since is the start of the window, in unix ms.
	since int64

	nodes    []WarmNode
	requests []warmRequest
	tools    []shared.ToolCallRecord
	events   []shared.EventRecord

	// newest is the timestamp of the first request the source handed over, the newest one.
	newest int64
	seen   bool
}

func (l *warmLoad) run(src WarmSource) error {
	if err := src.WarmNodes(l.since, l.addNode); err != nil {
		return err
	}
	if err := l.flushNodes(); err != nil {
		return err
	}
	limit := int(l.store.maxRecords)
	if err := src.WarmRequests(l.since, limit, l.addRequest); err != nil {
		return err
	}
	if err := l.flushRequests(); err != nil {
		return err
	}
	if err := src.WarmTools(l.since, l.addTool); err != nil {
		return err
	}
	if err := l.flushTools(); err != nil {
		return err
	}
	if room := l.store.room(); room > 0 {
		if err := src.WarmEvents(l.since, room, l.addEvent); err != nil {
			return err
		}
	}
	if err := l.flushEvents(); err != nil {
		return err
	}
	end, err := src.WarmEnd()
	if err != nil {
		return err
	}
	l.store.ContinueSeq(end.Seq)
	l.store.keepWarmWindow(l.since, end.Ts)
	return nil
}

// room is how many more records the cap lets the timeline take.
func (s *Store) room() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int(s.maxRecords) - len(s.requests) - len(s.tools) - len(s.events)
}

func (l *warmLoad) addNode(node WarmNode) error {
	l.nodes = append(l.nodes, node)
	if len(l.nodes) < warmBatch {
		return nil
	}
	return l.flushNodes()
}

func (l *warmLoad) addRequest(record shared.RequestRecord, written int64) error {
	if !l.seen {
		l.newest, l.seen = record.Ts, true
	}
	l.requests = append(l.requests, warmRequest{record: record, written: written})
	if len(l.requests) < warmBatch {
		return nil
	}
	return l.flushRequests()
}

func (l *warmLoad) addTool(_ string, tool shared.ToolCallRecord) error {
	l.tools = append(l.tools, tool)
	if len(l.tools) < warmBatch {
		return nil
	}
	return l.flushTools()
}

func (l *warmLoad) addEvent(event shared.EventRecord) error {
	l.events = append(l.events, event)
	if len(l.events) < warmBatch {
		return nil
	}
	return l.flushEvents()
}

func (l *warmLoad) flushNodes() error {
	batch := takeBatch(&l.nodes)
	return l.commit(len(batch), func(change *Change) {
		change.Sessions = true
		for _, node := range batch {
			l.store.applyWarmNodeLocked(node)
			touch(change, node.Agent.SessionID)
			if node.Session != nil {
				l.stats.Sessions++
			}
			l.stats.Agents++
		}
		l.store.evictSessionsLocked()
	})
}

func (l *warmLoad) flushRequests() error {
	batch := takeBatch(&l.requests)
	return l.commit(len(batch), func(change *Change) {
		change.Sessions = true
		s := l.store
		// the batch is newest first; the arrival order each request keeps is the position
		// its file wrote it at, whatever order the batches come in
		for at := len(batch) - 1; at >= 0; at-- {
			record := batch[at].record
			if !s.placeWarmRequestLocked(record, batch[at].written) {
				continue
			}
			s.seedWaitingLocked(record, l.newest)
			change.Requests = append(change.Requests, record)
			touch(change, record.SessionID)
			l.stats.Requests++
		}
		slices.Reverse(change.Requests)
		s.requestOrder.settle()
		s.evictSessionsLocked()
		s.evictRecordsLocked(nil)
	})
}

func (l *warmLoad) flushTools() error {
	batch := takeBatch(&l.tools)
	return l.commit(len(batch), func(change *Change) {
		change.Sessions = true
		s := l.store
		for _, tool := range batch {
			if _, held := s.tools[tool.ID]; held {
				continue
			}
			s.storeToolLocked(tool)
			s.touchLocked(tool.SessionID, tool.StartedAt)
			change.Tools = append(change.Tools, tool)
			touch(change, tool.SessionID)
			l.stats.Tools++
		}
		s.evictSessionsLocked()
		s.evictRecordsLocked(nil)
	})
}

func (l *warmLoad) flushEvents() error {
	batch := takeBatch(&l.events)
	return l.commit(len(batch), func(change *Change) {
		s := l.store
		for _, event := range batch {
			if line, ok := event.Payload.(shared.SpoolLine); ok {
				event.Payload = line.Slim()
			}
			s.events = append(s.events, event)
			s.keepWarmedEventLocked(event)
			change.Events = append(change.Events, event)
			l.stats.Events++
		}
		s.evictRecordsLocked(nil)
	})
}

// commit runs one step under the lock and hands its Change to the listeners once the lock
// is open; an empty step does nothing, and a cancelled context ends the load before the
// step.
func (l *warmLoad) commit(size int, place func(change *Change)) error {
	if size == 0 {
		return nil
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	s := l.store
	change := Change{Warm: true}
	s.mu.Lock()
	place(&change)
	listeners := s.notifyLocked(change)
	s.mu.Unlock()
	s.deliver(listeners, change)
	return nil
}

// touch lists a session on the change once.
func touch(change *Change, sessionID string) {
	if !slices.Contains(change.Touched, sessionID) {
		change.Touched = append(change.Touched, sessionID)
	}
}

// takeBatch empties a batch and answers what it held.
func takeBatch[T any](batch *[]T) []T {
	taken := *batch
	*batch = nil
	return taken
}

// applyWarmNodeLocked folds one reloaded node in through the same writes the live upserts
// make, then restores its clock and its end.
func (s *Store) applyWarmNodeLocked(node WarmNode) {
	sessionID := node.Agent.SessionID
	if node.Session != nil {
		applySessionUpsertLocked(s.sessionLocked(sessionID), *node.Session)
	}
	s.agentLocked(sessionID, "main")
	applyAgentUpsertLocked(s.agentLocked(sessionID, node.Agent.ID), node.Agent)
	if node.LastAt != nil {
		s.touchLocked(sessionID, *node.LastAt)
	}
	if node.EndedAt == nil {
		return
	}
	if node.Session != nil {
		s.markSessionEndedLocked(sessionID, *node.EndedAt)
		return
	}
	s.markSubagentEndedLocked(sessionID, node.Agent.ID, *node.EndedAt)
}

// placeWarmRequestLocked files one reloaded request as the call it already is: a history
// row is one paired call, so it takes no part in pairing. A request the store already
// holds stays as the live feed made it. The request arrives at the position its file wrote
// it at, and the live feed's arrivals count on from the highest position held. It reports
// whether the request was placed.
func (s *Store) placeWarmRequestLocked(record shared.RequestRecord, written int64) bool {
	if _, held := s.requests[record.ID]; held {
		return false
	}
	fileUnder(s.requestsBySession, record.SessionID, record.ID)
	held := &storedRequest{record: record, seq: written}
	s.requests[record.ID] = held
	s.requestOrder.add(held)
	s.arrival = max(s.arrival, written)
	s.fileWarmedCallLocked(record)
	s.touchLocked(record.SessionID, record.Ts)
	if agent := s.agentLocked(record.SessionID, record.AgentID); agent.model == nil {
		agent.model = strPtr(record.Model)
	}
	return true
}

// seedWaitingLocked queues a reloaded request that holds one side of its call only, a route
// line without its transcript copy or the reverse, when it sits within the match window of
// the newest reloaded request: the live feed may still bring the other side. Older ones
// are past waiting, and the pairing's own prune drops the rest as new calls arrive.
func (s *Store) seedWaitingLocked(record shared.RequestRecord, newest int64) {
	if newest-record.Ts > MatchMs {
		return
	}
	key := pairKey(record)
	if !FromRouter(record) {
		copies := s.lonelyCopies[key]
		if copies == nil {
			copies = newWaitingSet()
			s.lonelyCopies[key] = copies
		}
		copies.Add(record.ID)
		copies.newest = max(copies.newest, record.Ts)
		return
	}
	if record.What != nil {
		return // the route line has its transcript copy
	}
	routes := s.lonelyRoutes[key]
	if routes == nil {
		routes = newWaitingMap()
		s.lonelyRoutes[key] = routes
	}
	agent := record.AgentID
	if agent == "main" {
		agent = "" // an untagged route line stores under main and takes any agent's copy
	}
	routes.Set(record.ID, agent)
}

// eventKey is a spool line's content identity: a hash of the line as the spool carries it, its
// time, event, session and every other key included. The line is read through the same
// decode and encode whichever side built it, so a line read off the spool and the one
// history gave back agree.
func eventKey(line shared.SpoolLine) (uint64, bool) {
	encoded, err := line.MarshalJSON()
	if err != nil {
		return 0, false
	}
	value, err := jsfmt.DecodeValue(encoded)
	if err != nil {
		return 0, false
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value.EncodeJSON()))
	return hash.Sum64(), true
}

// keepWarmedEventLocked remembers a reloaded event's content.
func (s *Store) keepWarmedEventLocked(event shared.EventRecord) {
	line, ok := event.Payload.(shared.SpoolLine)
	if !ok {
		return
	}
	key, ok := eventKey(line)
	if !ok {
		return
	}
	if s.warmedEvents == nil {
		s.warmedEvents = make(map[uint64]struct{})
	}
	s.warmedEvents[key] = struct{}{}
	s.warmedUntil = max(s.warmedUntil, event.Ts)
}

// keepWarmWindow remembers where the warm window starts and the time of the newest event
// the source holds, which bound the spool lines read again that the store already counts as
// seen.
func (s *Store) keepWarmWindow(since, until int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warmedSince = since
	s.warmedUntil = max(s.warmedUntil, until)
}

// warmedEventLocked says whether a line is one the source already saw: files read again
// after a restart, or after a crash that lost their saved position, bring their lines back,
// and the timeline holds each only once. A line older than the window and no newer than the
// source's newest event was seen and left behind by the window; a line inside the window is
// one when the warm load reloaded its content. A line newer than every event the source
// holds cannot be one of them, so a live line costs no hashing.
func (s *Store) warmedEventLocked(line *shared.SpoolLine, ts int64) bool {
	if ts > s.warmedUntil {
		return false
	}
	if ts < s.warmedSince {
		return true
	}
	if len(s.warmedEvents) == 0 {
		return false
	}
	key, ok := eventKey(*line)
	if !ok {
		return false
	}
	_, held := s.warmedEvents[key]
	return held
}

// warmedCallKey names the slot of the match window a call at ts falls in, for a session and
// model.
func warmedCallKey(sessionID, model string, ts int64) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(sessionID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(model))
	_, _ = hash.Write([]byte{0})
	var slot [8]byte
	binary.LittleEndian.PutUint64(slot[:], uint64(ts/MatchMs))
	_, _ = hash.Write(slot[:])
	return hash.Sum64()
}

// fileWarmedCallLocked files a reloaded request that holds both sides of its call.
func (s *Store) fileWarmedCallLocked(record shared.RequestRecord) {
	if !FromRouter(record) || record.What == nil {
		return
	}
	if s.warmedCalls == nil {
		s.warmedCalls = make(map[uint64][]string)
	}
	key := warmedCallKey(record.SessionID, record.Model, record.Ts)
	s.warmedCalls[key] = append(s.warmedCalls[key], record.ID)
}

// unfileWarmedCallLocked drops a request's entry as it leaves the store.
func (s *Store) unfileWarmedCallLocked(record shared.RequestRecord) {
	if len(s.warmedCalls) == 0 {
		return
	}
	key := warmedCallKey(record.SessionID, record.Model, record.Ts)
	ids := slices.DeleteFunc(s.warmedCalls[key], func(id string) bool { return id == record.ID })
	if len(ids) == 0 {
		delete(s.warmedCalls, key)
		return
	}
	s.warmedCalls[key] = ids
}

// warmedTwinLocked finds the reloaded merged call a request read again belongs to. A
// transcript copy matches by its exact time and what it says; a route line by its latency,
// usage and status within the match window, which a different call does not share.
func (s *Store) warmedTwinLocked(record shared.RequestRecord) (string, bool) {
	if len(s.warmedCalls) == 0 {
		return "", false
	}
	for _, at := range [3]int64{record.Ts - MatchMs, record.Ts, record.Ts + MatchMs} {
		for _, id := range s.warmedCalls[warmedCallKey(record.SessionID, record.Model, at)] {
			if held, ok := s.requests[id]; ok && readAgainOf(held.record, record) {
				return id, true
			}
		}
	}
	return "", false
}

// readAgainOf says whether a request is another sighting of the merged call held.
func readAgainOf(held, record shared.RequestRecord) bool {
	if held.SessionID != record.SessionID || held.Model != record.Model {
		return false
	}
	if record.AgentID != "" && shared.BareAgent(record.AgentID) != shared.BareAgent(held.AgentID) {
		return false
	}
	if !FromRouter(record) {
		return held.Ts == record.Ts && sameOptStr(held.What, record.What)
	}
	apart := held.Ts - record.Ts
	if apart < 0 {
		apart = -apart
	}
	if apart > MatchMs || !sameInt64(held.LatencyMs, record.LatencyMs) || !sameOptStr(held.Status, record.Status) {
		return false
	}
	return shared.TotalTokens(record.Tokens) == 0 || sameCounts(held.Tokens, record.Tokens)
}

// sameCounts says whether two token tallies agree on every counter.
func sameCounts(a, b shared.Tokens) bool {
	return a.Input == b.Input && a.Output == b.Output && a.CacheRead == b.CacheRead && a.CacheWrite == b.CacheWrite
}

// absorbIntoWarmedLocked takes a request read again into the merged call it repeats: the row
// stands as history kept it, and a copy's id is remembered as the row's. The route line's
// usage stands when a later sighting of the copy refreshes the row.
func (s *Store) absorbIntoWarmedLocked(id string, record shared.RequestRecord) shared.RequestRecord {
	held := s.requests[id].record
	if !FromRouter(record) {
		s.aliases.set(record.ID, id)
		if shared.TotalTokens(held.Tokens) > 0 {
			s.routedTotals[id] = struct{}{}
		}
	}
	return held
}
