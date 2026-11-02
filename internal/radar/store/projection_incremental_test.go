package store

import (
	"fmt"
	"math/rand"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

const (
	equivalenceSteps    = 2_000
	equivalenceSessions = 5
)

var equivalenceAgents = []string{"main", "sub-1", "sub-2"}

// equivalenceWorld drives a store with a seeded random run of writes, so the projection and
// the event index can be compared with a fresh build after every one.
type equivalenceWorld struct {
	t     *testing.T
	rng   *rand.Rand
	store *Store
	now   int64

	requestIDs []string
	toolIDs    []string
	requestSeq int
	toolSeq    int
	callSeq    int
}

func newEquivalenceWorld(t *testing.T, seed int64) *equivalenceWorld {
	w := &equivalenceWorld{t: t, rng: rand.New(rand.NewSource(seed)), now: ts9}
	w.store = CreateStore(func() time.Time { return time.UnixMilli(w.now) })
	w.store.KeepWindow(time.Hour)
	w.store.maxRecords, w.store.evictBatch = 150, 20
	return w
}

func (w *equivalenceWorld) session() string {
	return fmt.Sprintf("s%d", w.rng.Intn(equivalenceSessions))
}

func (w *equivalenceWorld) agent() string {
	return equivalenceAgents[w.rng.Intn(len(equivalenceAgents))]
}

// ts is a moment up to 90 minutes back, so some records land already past the window.
func (w *equivalenceWorld) ts() int64 { return w.now - w.rng.Int63n(90*60_000) }

func (w *equivalenceWorld) tokens() shared.Tokens {
	return shared.Tokens{Input: w.rng.Int63n(500), Output: w.rng.Int63n(200)}
}

// newRequest adds a request with a fresh id and no request id to pair on.
func (w *equivalenceWorld) newRequest() {
	w.requestSeq++
	id := fmt.Sprintf("req-%d", w.requestSeq)
	w.requestIDs = append(w.requestIDs, id)
	w.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.SessionID, r.AgentID, r.Ts, r.Tokens = id, w.session(), w.agent(), w.ts(), w.tokens()
		r.Model = fmt.Sprintf("model-%d", w.rng.Intn(3))
	}), nil)
}

// updateRequest adds a held request's id again with other tokens, and sometimes another
// session or agent.
func (w *equivalenceWorld) updateRequest() {
	if len(w.requestIDs) == 0 {
		return
	}
	id := w.requestIDs[w.rng.Intn(len(w.requestIDs))]
	w.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.SessionID, r.AgentID, r.Ts, r.Tokens = id, w.session(), w.agent(), w.ts(), w.tokens()
	}), nil)
}

// pairedCallAcross adds a route line and then a transcript copy of the same call that names
// another session, or another agent in the same session. The copy joins the route line's
// record, which takes the copy's session and agent.
func (w *equivalenceWorld) pairedCallAcross(otherSession bool) {
	w.callSeq++
	session, agent, at := w.session(), w.agent(), w.ts()
	copySession, copyAgent := session, agent
	for otherSession && copySession == session {
		copySession = w.session()
	}
	for !otherSession && copyAgent == agent {
		copyAgent = w.agent()
	}
	callID := fmt.Sprintf("call-%d", w.callSeq)
	route := "anthropic"
	w.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.SessionID, r.AgentID, r.Ts = "route-"+callID, callID, session, agent, at
		r.Route = &route
	}), nil)
	w.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.SessionID, r.AgentID, r.Ts, r.Tokens = "copy-"+callID, callID, copySession, copyAgent, at, w.tokens()
	}), nil)
}

func (w *equivalenceWorld) pairedCallMovingSession() { w.pairedCallAcross(true) }

func (w *equivalenceWorld) pairedCallChangingAgent() { w.pairedCallAcross(false) }

// pairedCall adds a route line and then its transcript copy, which the store folds into one
// call.
func (w *equivalenceWorld) pairedCall() {
	w.callSeq++
	session, agent, at := w.session(), w.agent(), w.ts()
	callID := fmt.Sprintf("call-%d", w.callSeq)
	route := "anthropic"
	w.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.SessionID, r.AgentID, r.Ts = "route-"+callID, callID, session, agent, at
		r.Route = &route
	}), nil)
	w.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.SessionID, r.AgentID, r.Ts, r.Tokens = "copy-"+callID, callID, session, agent, at, w.tokens()
	}), nil)
}

func (w *equivalenceWorld) newTool() {
	w.toolSeq++
	id := fmt.Sprintf("tool-%d", w.toolSeq)
	w.toolIDs = append(w.toolIDs, id)
	var agent *string
	if w.rng.Intn(3) > 0 {
		agent = str(w.agent())
	}
	ok := w.rng.Intn(2) == 0
	w.store.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
		c.ID, c.SessionID, c.AgentID, c.StartedAt, c.Ok = id, w.session(), agent, w.ts(), ok
	}))
}

// updateTool adds a held tool call's id again, which can settle it as succeeded.
func (w *equivalenceWorld) updateTool() {
	if len(w.toolIDs) == 0 {
		return
	}
	id := w.toolIDs[w.rng.Intn(len(w.toolIDs))]
	w.store.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
		c.ID, c.SessionID, c.StartedAt, c.Ok = id, w.session(), w.ts(), true
	}))
}

// settleFailedTool adds a held tool call's id again in its own session as succeeded, which
// takes it off its session's and its agent's failed count.
func (w *equivalenceWorld) settleFailedTool() {
	if len(w.toolIDs) == 0 {
		return
	}
	id := w.toolIDs[w.rng.Intn(len(w.toolIDs))]
	w.store.mu.RLock()
	held, ok := w.store.tools[id]
	var record shared.ToolCallRecord
	if ok {
		record = held.record
	}
	w.store.mu.RUnlock()
	if !ok {
		return
	}
	w.store.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
		c.ID, c.SessionID, c.AgentID, c.StartedAt, c.Ok = id, record.SessionID, record.AgentID, record.StartedAt, true
	}))
}

// moveTool adds a held tool call's id again under another session.
func (w *equivalenceWorld) moveTool() {
	if len(w.toolIDs) == 0 {
		return
	}
	id := w.toolIDs[w.rng.Intn(len(w.toolIDs))]
	w.store.mu.RLock()
	held, ok := w.store.tools[id]
	var record shared.ToolCallRecord
	if ok {
		record = held.record
	}
	w.store.mu.RUnlock()
	if !ok {
		return
	}
	to := record.SessionID
	for to == record.SessionID {
		to = w.session()
	}
	w.store.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
		c.ID, c.SessionID, c.AgentID, c.StartedAt, c.Ok = id, to, record.AgentID, record.StartedAt, record.Ok
	}))
}

func (w *equivalenceWorld) event() {
	session := w.session()
	w.store.AddSpoolLine(shared.SpoolLine{Ts: isoTime(w.now - w.rng.Int63n(60_000)), Event: "UserPromptSubmit", SessionID: &session})
}

func (w *equivalenceWorld) hookTool() {
	session, toolUse := w.session(), fmt.Sprintf("use-%d", w.rng.Intn(40))
	event := "PostToolUse"
	if w.rng.Intn(3) == 0 {
		event = "PostToolUseFailure"
	}
	w.store.AddSpoolLine(shared.SpoolLine{
		Ts: isoTime(w.now - w.rng.Int63n(60_000)), Event: event, SessionID: &session, ToolUseID: &toolUse,
	})
}

func (w *equivalenceWorld) sessionFacts() {
	if w.rng.Intn(2) == 0 {
		w.store.UpsertSession(SessionUpsert{ID: w.session()})
		return
	}
	w.store.EndSession(w.session(), w.now)
}

func (w *equivalenceWorld) dropSession() {
	w.store.mu.Lock()
	w.store.dropSessionLocked(w.session())
	w.store.notifyLocked(Change{})
	w.store.mu.Unlock()
}

// held is how many requests, tool calls and events the store holds.
func (w *equivalenceWorld) held() int {
	w.store.mu.RLock()
	defer w.store.mu.RUnlock()
	return len(w.store.requests) + len(w.store.tools) + len(w.store.events)
}

// cachesLive says whether the projection stands built, without building it.
func (w *equivalenceWorld) projectionLive() bool {
	w.store.mu.RLock()
	defer w.store.mu.RUnlock()
	return w.store.proj != nil
}

// matchesRebuild fails the test when the cached projection or event index differs from one
// built from the held records.
func (w *equivalenceWorld) matchesRebuild(step int, op string) {
	w.t.Helper()
	s := w.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if got, want := s.projectionLocked(), s.buildProjectionLocked(); !reflect.DeepEqual(got, want) {
		w.t.Fatalf("step %d (%s): the projection differs from a rebuild", step, op)
	}
	if got, want := s.eventIndexLocked(), s.buildEventIndexLocked(); !reflect.DeepEqual(got, want) {
		w.t.Fatalf("step %d (%s): the event index differs from a rebuild", step, op)
	}
}

// The projection and the event index, kept current as writes land, equal a rebuild from the
// held records after every step of a random run of appends, updates, pairings, sheds and
// drops.
func TestProjectionEqualsARebuildAfterEveryWrite(t *testing.T) {
	w := newEquivalenceWorld(t, 20261102)
	steps := []struct {
		name string
		do   func()
	}{
		{"new request", w.newRequest},
		{"new request", w.newRequest},
		{"new request", w.newRequest},
		{"update request", w.updateRequest},
		{"paired call moving a session", w.pairedCallMovingSession},
		{"paired call changing an agent", w.pairedCallChangingAgent},
		{"paired call", w.pairedCall},
		{"new tool", w.newTool},
		{"new tool", w.newTool},
		{"update tool", w.updateTool},
		{"settle failed tool", w.settleFailedTool},
		{"move tool", w.moveTool},
		{"event", w.event},
		{"event", w.event},
		{"hook tool", w.hookTool},
		{"session facts", w.sessionFacts},
	}
	for step := 0; step < equivalenceSteps; step++ {
		if step%100 == 99 {
			w.now += 2 * time.Minute.Milliseconds()
		}
		if step%400 == 399 {
			w.dropSession()
			w.matchesRebuild(step, "drop session")
		}
		pick := steps[w.rng.Intn(len(steps))]
		pick.do()
		w.matchesRebuild(step, pick.name)
	}
}

// A write that only appends leaves the built projection standing: the next read finds it
// and does not build again.
func TestAppendingKeepsTheProjection(t *testing.T) {
	w := newEquivalenceWorld(t, 1)
	w.store.maxRecords = 1_000_000
	w.store.KeepWindow(0)
	w.newRequest()
	w.matchesRebuild(0, "seed")
	appends := map[string]func(){"request": w.newRequest, "tool": w.newTool, "event": w.event}
	for name, add := range appends {
		before := w.held()
		add()
		if w.held() != before+1 {
			t.Fatalf("%s: the store holds %d records, want %d", name, w.held(), before+1)
		}
		if !w.projectionLive() {
			t.Errorf("a new %s discarded the projection", name)
		}
		w.matchesRebuild(0, name)
	}
}

// appendAndReadCost is the heap one append followed by a read of every session's records
// allocates, with the given number of requests held (and a tool call per request, an event
// per ten) across 200 sessions.
func appendAndReadCost(requests int) (bytes, mallocs uint64) {
	const sessions, rounds, perRound = 200, 5, 20
	now := ts9
	s := CreateStore(func() time.Time { return time.UnixMilli(now) })
	for n := 0; n < requests; n++ {
		session := fmt.Sprintf("s%d", n%sessions)
		s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = fmt.Sprintf("r%d", n), session, now-int64(requests-n)
		}), nil)
		s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
			c.ID, c.SessionID, c.StartedAt = fmt.Sprintf("t%d", n), session, now-int64(requests-n)
		}))
		if n%10 == 0 {
			s.AddSpoolLine(shared.SpoolLine{Ts: isoTime(now - int64(requests-n)), Event: "UserPromptSubmit", SessionID: &session})
		}
	}
	read := func() { s.EachSession(func(string, SessionRecords) {}) }
	read()

	next := requests
	bytes = ^uint64(0)
	for round := 0; round < rounds; round++ {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < perRound; i++ {
			next++
			n := next
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.SessionID, r.Ts = fmt.Sprintf("r%d", n), fmt.Sprintf("s%d", n%sessions), now
			}), nil)
			read()
		}
		runtime.ReadMemStats(&after)
		if spent := (after.TotalAlloc - before.TotalAlloc) / perRound; spent < bytes {
			bytes, mallocs = spent, (after.Mallocs-before.Mallocs)/perRound
		}
	}
	return bytes, mallocs
}

// Appending a request and reading the projection costs the same with 20,000 requests held as
// with 2,000: the work follows the new record, not the store.
func TestAppendAndReadCostIsFlatInTheRecordCount(t *testing.T) {
	smallBytes, smallMallocs := appendAndReadCost(2_000)
	largeBytes, largeMallocs := appendAndReadCost(20_000)
	t.Logf("2,000 requests: %d B, %d allocs per append and read; 20,000 requests: %d B, %d allocs",
		smallBytes, smallMallocs, largeBytes, largeMallocs)
	if largeBytes > 2*smallBytes {
		t.Errorf("an append and read over 20,000 requests allocated %d B, over 2,000 %d B: want under twice, flat in the record count",
			largeBytes, smallBytes)
	}
}

// readProjection builds or finds the projection, as a read of every session's records does.
func readProjection(s *Store) {
	s.mu.Lock()
	s.projectionLocked()
	s.mu.Unlock()
}

// updateAndReadCost is the heap one update to a held request followed by a read of the
// projection allocates, with the given number of requests held (and a tool call per
// request) in sessions of ten requests each.
func updateAndReadCost(requests int) (bytes, mallocs uint64) {
	const perSession, rounds, perRound = 10, 5, 20
	now := ts9
	s := CreateStore(func() time.Time { return time.UnixMilli(now) })
	sessionOf := func(n int) string { return fmt.Sprintf("s%d", n/perSession) }
	for n := 0; n < requests; n++ {
		s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts = fmt.Sprintf("r%d", n), sessionOf(n), now-int64(requests-n)
		}), nil)
		s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) {
			c.ID, c.SessionID, c.StartedAt = fmt.Sprintf("t%d", n), sessionOf(n), now-int64(requests-n)
		}))
	}
	readProjection(s)

	bytes = ^uint64(0)
	next := 0
	for round := 0; round < rounds; round++ {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < perRound; i++ {
			next = (next + 7919) % requests
			n := next
			s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.SessionID, r.Ts, r.Tokens = fmt.Sprintf("r%d", n), sessionOf(n), now, shared.Tokens{Input: int64(i + 1)}
			}), nil)
			readProjection(s)
		}
		runtime.ReadMemStats(&after)
		if spent := (after.TotalAlloc - before.TotalAlloc) / perRound; spent < bytes {
			bytes, mallocs = spent, (after.Mallocs-before.Mallocs)/perRound
		}
	}
	return bytes, mallocs
}

// Updating a held request and reading the projection costs the same with 20,000 requests
// held as with 2,000, in sessions of the same size: the work follows the updated record's
// session, not the store.
func TestUpdateAndReadCostIsFlatInTheRecordCount(t *testing.T) {
	smallBytes, smallMallocs := updateAndReadCost(2_000)
	largeBytes, largeMallocs := updateAndReadCost(20_000)
	t.Logf("2,000 requests: %d B, %d allocs per update and read; 20,000 requests: %d B, %d allocs",
		smallBytes, smallMallocs, largeBytes, largeMallocs)
	if largeBytes > 2*smallBytes {
		t.Errorf("an update and read over 20,000 requests allocated %d B, over 2,000 %d B: want under twice, flat in the record count",
			largeBytes, smallBytes)
	}
}

// An update to a held request or tool call rebuilds the slices of the sessions it touched
// and leaves every other session's slice standing.
func TestAnUpdateLeavesUntouchedSessionSlicesStanding(t *testing.T) {
	now := ts9
	s := CreateStore(func() time.Time { return time.UnixMilli(now) })
	for _, session := range []string{"a", "b", "c"} {
		s.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.SessionID, r.Ts = "r-"+session, session, now }), nil)
		s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) { c.ID, c.SessionID, c.StartedAt = "t-"+session, session, now }))
	}
	slices := func() map[string]*sessionSlice {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make(map[string]*sessionSlice)
		for id, slice := range s.projectionLocked() {
			out[id] = slice
		}
		return out
	}

	var before map[string]*sessionSlice

	// a route line held in session a joins its transcript copy named in session b, which moves
	// the call to b: c stands
	route := "anthropic"
	s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.SessionID, r.Model, r.Ts, r.Route = "route-1", "call-1", "a", "other-model", now, &route
	}), nil)
	before = slices()
	s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.RequestID, r.SessionID, r.Model, r.Ts = "copy-1", "call-1", "b", "other-model", now
	}), nil)
	after := slices()
	if after["c"] != before["c"] {
		t.Errorf("session c, untouched by moving a request from a to b, was rebuilt")
	}
	if after["a"] == before["a"] {
		t.Errorf("session a, which lost the call, kept its old slice")
	}
	if after["b"] == before["b"] {
		t.Errorf("session b, which gained the request, kept its old slice")
	}
	if got := len(after["b"].requests); got != 2 {
		t.Errorf("session b holds %d requests, want 2", got)
	}
	if got := len(after["a"].requests); got != 1 {
		t.Errorf("session a holds %d requests, want 1", got)
	}

	// a tool call settles in session c: a and b stand
	before = after
	s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) { c.ID, c.SessionID, c.StartedAt, c.Ok = "t-c", "c", now, true }))
	after = slices()
	if after["a"] != before["a"] || after["b"] != before["b"] {
		t.Errorf("sessions a and b, untouched by a tool update in c, were rebuilt")
	}
}

// A copy read again into the merged row a warm load holds changes no record, so the built
// projection stands as it is.
func TestAReReadCopyOfAWarmedRowKeepsTheProjection(t *testing.T) {
	s := warmedWith(t, mergedRow("route:s1:main:m:1", ts2, nil))
	readProjection(s)
	s.mu.RLock()
	before := s.proj["s1"]
	s.mu.RUnlock()

	s.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.Ts, r.What, r.Tokens = "req_2001", ts2, str("→ text"), shared.Tokens{Input: 900, Output: 210}
	}), nil)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proj == nil || len(s.staleSessions) != 0 {
		t.Fatalf("the re-read copy discarded the projection or marked sessions stale")
	}
	if got := s.projectionLocked()["s1"]; got != before {
		t.Errorf("the re-read copy rebuilt session s1's slice")
	}
}

// Records that share an arrival number, a warm load's file position meeting a live
// arrival's, come back from a session's rebuild in the order the arrival list keeps them.
func TestASessionRebuildKeepsTheArrivalOrderOfTiedNumbers(t *testing.T) {
	s := newTestStore()
	s.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.SessionID, r.Model = "live", "s1", "m-live" }), nil)
	source := &fakeWarmSource{
		requests: []shared.RequestRecord{makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Model = "a-warmed", "s1", "m-warmed"
		})},
		written: map[string]int64{"a-warmed": 1},
	}
	if _, err := s.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) { c.ID, c.SessionID, c.Ok = "t1", "s1", false }))
	readProjection(s)
	s.AddToolCall(makeTool(func(c *shared.ToolCallRecord) { c.ID, c.SessionID, c.Ok = "t1", "s1", true }))

	s.mu.Lock()
	defer s.mu.Unlock()
	if got, want := s.projectionLocked(), s.buildProjectionLocked(); !reflect.DeepEqual(got, want) {
		t.Errorf("the rebuilt session differs from a build over the arrival lists")
	}
}
