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
		{"paired call", w.pairedCall},
		{"new tool", w.newTool},
		{"new tool", w.newTool},
		{"update tool", w.updateTool},
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
