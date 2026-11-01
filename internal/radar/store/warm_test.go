package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// fakeWarmSource is a WarmSource that holds what a test hands it, in the orders the
// interface documents, and remembers the limits it was asked for.
type fakeWarmSource struct {
	nodes    []WarmNode
	requests []shared.RequestRecord // newest first
	// written is the position the source's file wrote a request at, by id; a request
	// without one is positioned by its ts.
	written map[string]int64
	tools   []shared.ToolCallRecord // newest first
	events  []shared.EventRecord    // oldest first

	requestLimit int
	eventLimit   int
	// beforeRequest runs before the i-th request is handed over, so a test can hold the
	// load between two batches.
	beforeRequest func(i int)
}

func (f *fakeWarmSource) WarmRequests(_ int64, limit int, fn func(shared.RequestRecord, int64) error) error {
	f.requestLimit = limit
	for i, record := range f.requests {
		if i >= limit {
			return nil
		}
		if f.beforeRequest != nil {
			f.beforeRequest(i)
		}
		written, ok := f.written[record.ID]
		if !ok {
			written = record.Ts
		}
		if err := fn(record, written); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeWarmSource) WarmTools(_ int64, fn func(nodeID string, t shared.ToolCallRecord) error) error {
	for _, tool := range f.tools {
		if err := fn(tool.SessionID+"/main", tool); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeWarmSource) WarmEvents(since int64, limit int, fn func(shared.EventRecord) error) error {
	f.eventLimit = limit
	events := slices.DeleteFunc(slices.Clone(f.events), func(e shared.EventRecord) bool { return e.Ts < since })
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	for _, event := range events {
		if err := fn(event); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeWarmSource) WarmNodes(_ int64, fn func(WarmNode) error) error {
	for _, node := range f.nodes {
		if err := fn(node); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeWarmSource) WarmEnd() (WarmEnd, error) {
	var end WarmEnd
	for _, event := range f.events {
		if event.Seq > end.Seq {
			end = WarmEnd{Seq: event.Seq, Ts: event.Ts}
		}
	}
	return end, nil
}

// warmSession is a session's lead node, as a source hands it over.
func warmSession(id string, lastAt int64) WarmNode {
	return WarmNode{
		Session: &SessionUpsert{ID: id, Cwd: str("/work/" + id), StartedAt: i64(lastAt - 500)},
		Agent:   AgentUpsert{SessionID: id, ID: "main", Kind: kindPtr(shared.KindMain)},
		LastAt:  i64(lastAt),
	}
}

// warmRequests makes n requests of one session, newest first: ts counts down from newest.
func warmRequests(session string, n int, newest int64) []shared.RequestRecord {
	out := make([]shared.RequestRecord, 0, n)
	for i := range n {
		out = append(out, makeRequest(func(r *shared.RequestRecord) {
			r.ID = fmt.Sprintf("%s-r%05d", session, n-i)
			r.SessionID = session
			r.Ts = newest - int64(i)*100
		}))
	}
	return out
}

func TestWarmReloadsRequestsToolsEventsAndNodes(t *testing.T) {
	store := newTestStore()
	subagent := WarmNode{
		Agent: AgentUpsert{
			SessionID: "s1", ID: "agent-a", Kind: kindPtr(shared.KindSubagent),
			AgentType: str("Explore"), Description: str("find the flaky test"),
			ParentID: strPtrPtr(str("main")),
		},
		LastAt: i64(ts1),
	}
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2), subagent},
		requests: warmRequests("s1", 3, ts2),
		tools: []shared.ToolCallRecord{
			makeTool(func(c *shared.ToolCallRecord) { c.ID = "t2"; c.StartedAt = ts2 }),
			makeTool(func(c *shared.ToolCallRecord) { c.ID = "t1"; c.StartedAt = ts1 }),
		},
		events: []shared.EventRecord{
			{Seq: 4, Ts: ts1, Kind: "UserPromptSubmit", SessionID: str("s1"), Payload: line(nil)},
			{Seq: 9, Ts: ts2, Kind: "Notification", SessionID: str("s1"), Payload: line(nil)},
		},
	}

	stats, err := store.Warm(t.Context(), source)
	if err != nil {
		t.Fatalf("warm: %v", err)
	}
	if want := (WarmStats{Requests: 3, Tools: 2, Events: 2, Sessions: 1, Agents: 2}); stats.Requests != want.Requests ||
		stats.Tools != want.Tools || stats.Events != want.Events || stats.Sessions != want.Sessions ||
		stats.Agents != want.Agents {
		t.Errorf("the stats: got %+v, want counts %+v", stats, want)
	}

	list := store.SessionList()
	if len(list) != 1 || list[0].ID != "s1" || list[0].RequestCount != 3 || list[0].AgentCount != 2 {
		t.Fatalf("the session list: got %+v, want s1 with 3 requests and 2 agents", list)
	}
	if !eqStr(list[0].Cwd, "/work/s1") {
		t.Errorf("the session's cwd: got %v, want /work/s1", list[0].Cwd)
	}
	requests := store.RequestsFor(RequestFilter{})
	if len(requests) != 3 {
		t.Fatalf("the requests: got %d, want 3", len(requests))
	}
	byID := map[string]shared.RequestRecord{}
	for _, request := range requests {
		byID[request.ID] = request
	}
	for _, want := range source.requests {
		got, ok := byID[want.ID]
		if !ok || got.Ts != want.Ts || got.Tokens != want.Tokens || got.Model != want.Model {
			t.Errorf("request %s: got %+v (held %v), want %+v", want.ID, got, ok, want)
		}
	}
	tools := store.Tools(ToolsFilter{})
	if len(tools) != 2 || tools[0].ID != "t2" || tools[1].ID != "t1" {
		t.Errorf("the tools: got %+v, want t2 then t1", tools)
	}
	events := store.Events(EventsFilter{})
	if len(events) != 2 || events[0].Seq != 9 || events[1].Seq != 4 {
		t.Errorf("the events: got %+v, want seqs 9 and 4", events)
	}

	store.AddSpoolLine(line(func(l *shared.SpoolLine) { l.Event = "Stop" }))
	if next := store.Events(EventsFilter{Limit: i64(1)}); len(next) != 1 || next[0].Seq != 10 {
		t.Errorf("the next live event: got %+v, want seq 10 after the reloaded max 9", next)
	}
}

func TestWarmKeepsTheRecordCapNewestFirst(t *testing.T) {
	store := newTestStore()
	store.maxRecords, store.evictBatch = 100, 10
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2)},
		requests: warmRequests("s1", 300, ts2),
	}

	stats, err := store.Warm(t.Context(), source)
	if err != nil {
		t.Fatalf("warm: %v", err)
	}
	if source.requestLimit != 100 {
		t.Errorf("the limit asked of the source: got %d, want the cap 100", source.requestLimit)
	}
	if stats.Requests != 100 {
		t.Errorf("the requests reloaded: got %d, want 100", stats.Requests)
	}
	held := store.RequestsFor(RequestFilter{})
	if len(held) != 100 {
		t.Fatalf("the requests held: got %d, want 100", len(held))
	}
	oldest := held[0].Ts
	for _, request := range held {
		oldest = min(oldest, request.Ts)
	}
	if want := source.requests[99].Ts; oldest != want {
		t.Errorf("the oldest request held: got ts %d, want %d (the newest 100 stay)", oldest, want)
	}
}

func TestWarmShedsPastTheCapOldestFirst(t *testing.T) {
	store := newTestStore()
	store.maxRecords, store.evictBatch = 100, 10
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2)},
		requests: warmRequests("s1", 100, ts2),
	}
	for i := range 60 {
		source.tools = append(source.tools, makeTool(func(c *shared.ToolCallRecord) {
			c.ID = fmt.Sprintf("t%03d", i)
			c.StartedAt = ts2 - 50 - int64(i)*100 // newest first, interleaved with the requests
		}))
	}

	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}

	type stamped struct {
		id string
		ts int64
	}
	var all []stamped
	for _, request := range source.requests {
		all = append(all, stamped{request.ID, request.Ts})
	}
	for _, tool := range source.tools {
		all = append(all, stamped{tool.ID, tool.StartedAt})
	}
	slices.SortFunc(all, func(a, b stamped) int { return int(b.ts - a.ts) })
	var want []string
	for _, kept := range all[:100] {
		want = append(want, kept.id)
	}
	raw := store.Raw()
	var got []string
	for _, request := range raw.Requests {
		got = append(got, request.ID)
	}
	for _, tool := range raw.Tools {
		got = append(got, tool.ID)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the records held: got %d, want the newest 100 of the requests and tools together", len(got))
	}
}

func TestWarmEmitsOnlyWarmChanges(t *testing.T) {
	store := newTestStore()
	var changes []Change
	store.OnUpdate(func(change Change) { changes = append(changes, change) })
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2)},
		requests: warmRequests("s1", 2_500, ts2),
		tools:    []shared.ToolCallRecord{makeTool(nil)},
		events:   []shared.EventRecord{{Seq: 1, Ts: ts1, Kind: "Stop", SessionID: str("s1"), Payload: line(nil)}},
	}

	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("warm delivered no change")
	}
	var requests int
	for i, change := range changes {
		if !change.Warm {
			t.Errorf("change %d: got Warm false, want true", i)
		}
		if len(change.Requests) > 1_000 {
			t.Errorf("change %d: got %d requests, want at most one batch of 1000", i, len(change.Requests))
		}
		requests += len(change.Requests)
	}
	if requests != 2_500 {
		t.Errorf("the requests the changes carried: got %d, want 2500", requests)
	}
}

func TestWarmSeedsTheWaitingQueuesNearTheEnd(t *testing.T) {
	newest := ts2
	routeOnly := func(id string, ts int64) shared.RequestRecord {
		return makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Route = id, ts, str("anthropic")
		})
	}
	source := &fakeWarmSource{
		nodes: []WarmNode{warmSession("s1", newest)},
		// newest first: the near one waits, the one a minute older does not
		requests: []shared.RequestRecord{routeOnly("route-near", newest), routeOnly("route-far", newest-60_000)},
	}
	store := newTestStore()
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}

	copyOf := func(id string, ts int64) shared.RequestRecord {
		return makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.What = id, ts, str("a prompt") })
	}
	store.AddRequest(copyOf("copy-near", newest+100), nil)
	if got := len(store.RequestsFor(RequestFilter{})); got != 2 {
		t.Errorf("after a copy within the window: got %d requests, want 2 (it paired with route-near)", got)
	}
	store.AddRequest(copyOf("copy-far", newest-60_000+100), nil)
	if got := len(store.RequestsFor(RequestFilter{})); got != 3 {
		t.Errorf("after a copy beside the old route: got %d requests, want 3 (the old route did not wait)", got)
	}
}

func TestWarmSeedsAWaitingCopyNearTheEnd(t *testing.T) {
	source := &fakeWarmSource{
		nodes: []WarmNode{warmSession("s1", ts2)},
		requests: []shared.RequestRecord{
			makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.What = "copy-near", ts2, str("a prompt") }),
		},
	}
	store := newTestStore()
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.Ts, r.Route = "route-late", ts2+100, str("anthropic")
	}), nil)
	if got := len(store.RequestsFor(RequestFilter{})); got != 1 {
		t.Errorf("a route line after warm: got %d requests, want 1 (it paired with the reloaded copy)", got)
	}
}

func TestWarmReleasesTheLockBetweenBatches(t *testing.T) {
	store := newTestStore()
	reached := make(chan struct{})
	release := make(chan struct{})
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2)},
		requests: warmRequests("s1", 5_000, ts2),
	}
	source.beforeRequest = func(i int) {
		if i == 1_000 {
			close(reached)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := store.Warm(t.Context(), source)
		done <- err
	}()

	<-reached
	read := make(chan []SessionListItem, 1)
	go func() { read <- store.SessionList() }()
	select {
	case list := <-read:
		if len(list) != 1 || list[0].RequestCount != 1_000 {
			t.Errorf("a read between batches: got %+v, want s1 with the first 1000 requests", list)
		}
	case <-time.After(10 * time.Second):
		t.Error("a read blocked while the warm load waited between batches")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("warm: %v", err)
	}
	if got := store.SessionList()[0].RequestCount; got != 5_000 {
		t.Errorf("the requests after the load: got %d, want 5000", got)
	}
}

func TestWarmStopsWhenItsContextEnds(t *testing.T) {
	store := newTestStore()
	ctx, cancel := context.WithCancel(t.Context())
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2)},
		requests: warmRequests("s1", 5_000, ts2),
	}
	source.beforeRequest = func(i int) {
		if i == 1_000 {
			cancel()
		}
	}
	_, err := store.Warm(ctx, source)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("warm after cancel: got %v, want context.Canceled", err)
	}
	if got := len(store.RequestsFor(RequestFilter{})); got > 1_000 {
		t.Errorf("the requests held after a cancel: got %d, want at most the first batch", got)
	}
}

func TestWarmKeepsAnIDALiveRecordAlreadyHolds(t *testing.T) {
	store := newTestStore()
	store.AddRequest(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts, r.Tokens.Output = "r-live", ts2, 99 }), nil)
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2)},
		requests: []shared.RequestRecord{makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r-live", ts2 })},
	}
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	held := store.RequestsFor(RequestFilter{})
	if len(held) != 1 || held[0].Tokens.Output != 99 {
		t.Errorf("the record both hold: got %+v, want the live one kept", held)
	}
}

func TestWarmHandsAChangeToEveryListenerOutsideTheLock(t *testing.T) {
	store := newTestStore()
	var seen [][]string
	store.OnUpdate(func(change Change) {
		var ids []string
		for _, item := range store.SessionList() { // a listener reads the store back
			ids = append(ids, item.ID)
		}
		seen = append(seen, ids)
	})
	source := &fakeWarmSource{
		nodes:    []WarmNode{warmSession("s1", ts2)},
		requests: warmRequests("s1", 3, ts2),
	}
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if len(seen) == 0 || !slices.Equal(seen[len(seen)-1], []string{"s1"}) {
		t.Errorf("the session list a listener read: got %v, want [s1] at the end", seen)
	}
}

// spoolLineFrom reads a line off its text, the way the spool and the history file hand one over.
func spoolLineFrom(t *testing.T, text string) shared.SpoolLine {
	t.Helper()
	var l shared.SpoolLine
	if err := json.Unmarshal([]byte(text), &l); err != nil {
		t.Fatalf("read the line %s: %v", text, err)
	}
	return l
}

func TestAReReadLineOlderThanTheWindowIsNotAddedAgain(t *testing.T) {
	old := spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"UserPromptSubmit","session_id":"s1","prompt":"a"}`, isoTime(ts0-2*dayMs)))
	recent := spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"Stop","session_id":"s1"}`, isoTime(ts1)))
	source := &fakeWarmSource{
		nodes: []WarmNode{warmSession("s1", ts1)},
		events: []shared.EventRecord{
			{Seq: 4, Ts: ts0 - 2*dayMs, Kind: old.Event, SessionID: old.SessionID, Payload: old},
			{Seq: 9, Ts: ts1, Kind: recent.Event, SessionID: recent.SessionID, Payload: recent},
		},
	}
	store := newTestStore()
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if held := store.Events(EventsFilter{}); len(held) != 1 || held[0].Seq != 9 {
		t.Fatalf("the events held: got %+v, want only seq 9, the window leaves seq 4 out", held)
	}

	store.AddSpoolLine(old)
	store.AddSpoolLine(recent)
	if held := store.Events(EventsFilter{}); len(held) != 1 {
		t.Errorf("the events after the lines are read again: got %+v, want only seq 9", held)
	}
	store.AddSpoolLine(spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"Notification","session_id":"s1"}`, isoTime(ts2))))
	if next := store.Events(EventsFilter{Limit: i64(1)}); len(next) != 1 || next[0].Seq != 10 {
		t.Errorf("the next live event: got %+v, want seq 10", next)
	}
}

func TestAReReadEventTheWarmLoadHoldsIsNotAddedAgain(t *testing.T) {
	first := spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"UserPromptSubmit","session_id":"s1","prompt":"a"}`, isoTime(ts1)))
	second := spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"Stop","session_id":"s1"}`, isoTime(ts2)))
	source := &fakeWarmSource{
		nodes: []WarmNode{warmSession("s1", ts2)},
		events: []shared.EventRecord{
			{Seq: 4, Ts: ts1, Kind: first.Event, SessionID: first.SessionID, Payload: first},
			{Seq: 9, Ts: ts2, Kind: second.Event, SessionID: second.SessionID, Payload: second},
		},
	}
	store := newTestStore()
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}

	// the files are read again: the same two lines, then one that is new
	store.AddSpoolLine(spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"UserPromptSubmit","session_id":"s1","prompt":"a"}`, isoTime(ts1))))
	store.AddSpoolLine(spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"Stop","session_id":"s1"}`, isoTime(ts2))))
	if got := store.Events(EventsFilter{}); len(got) != 2 {
		t.Fatalf("the events after a re-read: got %d, want the 2 the warm load held", len(got))
	}
	store.AddSpoolLine(spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"Stop","session_id":"s1"}`, isoTime(ts3))))
	store.AddSpoolLine(spoolLineFrom(t, fmt.Sprintf(`{"ts":%q,"event":"UserPromptSubmit","session_id":"s1","prompt":"b"}`, isoTime(ts1))))
	got := store.Events(EventsFilter{})
	if len(got) != 4 || got[0].Seq != 11 || got[1].Seq != 10 {
		t.Errorf("the events after two new lines: got %+v, want 4 with seqs 10 and 11 last", got)
	}
}

// mergedRow is a call a history file holds as one row: the route line's facts and the
// transcript copy's, under one id.
func mergedRow(id string, ts int64, shape func(*shared.RequestRecord)) shared.RequestRecord {
	return makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.Ts = id, ts
		r.Route, r.Via, r.What = str("provider"), str("zai"), str("→ text")
		r.Status = str("200")
		r.LatencyMs = i64(1_400)
		r.Tokens = shared.Tokens{Input: 900, Output: 210}
		if shape != nil {
			shape(r)
		}
	})
}

func warmedWith(t *testing.T, rows ...shared.RequestRecord) *Store {
	t.Helper()
	store := newTestStore()
	source := &fakeWarmSource{nodes: []WarmNode{warmSession("s1", ts2)}, requests: rows}
	if _, err := store.Warm(t.Context(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	return store
}

func TestAReReadCopyJoinsTheMergedRowTheWarmLoadHolds(t *testing.T) {
	store := warmedWith(t, mergedRow("route:s1:main:m:1", ts2, nil))
	copyAt := func(id string, ts int64) shared.RequestRecord {
		return makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.What = id, ts, str("→ text")
			r.Tokens = shared.Tokens{Input: 900, Output: 210}
		})
	}
	store.AddRequest(copyAt("req_2001", ts2), nil)
	if got := len(store.RequestsFor(RequestFilter{})); got != 1 {
		t.Fatalf("after the copy of a merged call is read again: got %d requests, want 1", got)
	}
	store.AddRequest(copyAt("req_2001", ts2), nil) // streamed again
	if got := len(store.RequestsFor(RequestFilter{})); got != 1 {
		t.Errorf("after a second sighting of that copy: got %d requests, want 1", got)
	}
	store.AddRequest(copyAt("req_2002", ts2+5_000), nil)
	if got := len(store.RequestsFor(RequestFilter{})); got != 2 {
		t.Errorf("after a different call 5 s later: got %d requests, want 2", got)
	}
}

func TestAReReadRouteJoinsTheMergedRowTheWarmLoadHolds(t *testing.T) {
	store := warmedWith(t, mergedRow("req_x", ts2, nil)) // the copy arrived first: its id stands
	routeAt := func(id string, ts int64, shape func(*shared.RequestRecord)) shared.RequestRecord {
		return mergedRow(id, ts, func(r *shared.RequestRecord) {
			r.What, r.AgentID = nil, ""
			if shape != nil {
				shape(r)
			}
		})
	}
	store.AddRequest(routeAt("route:s1::m:1", ts2+1_000, nil), nil)
	if got := len(store.RequestsFor(RequestFilter{})); got != 1 {
		t.Fatalf("after the route line of a merged call is read again: got %d requests, want 1", got)
	}
	store.AddRequest(routeAt("route:s1::m:2", ts2+3_000, func(r *shared.RequestRecord) {
		r.Tokens = shared.Tokens{Input: 5, Output: 5}
	}), nil)
	if got := len(store.RequestsFor(RequestFilter{})); got != 2 {
		t.Errorf("after a route line with other usage: got %d requests, want 2", got)
	}
}

// TestACallReadAgainAfterAWarmLoadStaysOneCall is the legacy fixture's four calls: two copies
// that stand alone, one call merged by request id and one merged by the match window.
func TestACallReadAgainAfterAWarmLoadStaysOneCall(t *testing.T) {
	base := int64(1_792_054_800_000)
	session := func(r *shared.RequestRecord, id string) { r.SessionID = id }
	rows := []shared.RequestRecord{
		makeRequest(func(r *shared.RequestRecord) {
			session(r, "demo-sess-2")
			r.ID, r.Ts, r.Model, r.What = "req_3001", base-82_000_000, "kimi-k2", str("→ text")
		}),
		makeRequest(func(r *shared.RequestRecord) {
			session(r, "demo-sess-1")
			r.ID, r.Ts, r.What = "req_1001", base+4_000, str("↳ prompt  → Bash")
		}),
		mergedRow("route:demo-sess-1:main:glm-5.3:1", base+15_000, func(r *shared.RequestRecord) {
			session(r, "demo-sess-1")
			r.Model = "glm-5.3"
		}),
		mergedRow("route:demo-sess-1:scout-01:glm-5.3-flash:1", base+26_000, func(r *shared.RequestRecord) {
			session(r, "demo-sess-1")
			r.AgentID, r.Model, r.LatencyMs = "scout-01", "glm-5.3-flash", i64(6_000)
			r.Tokens = shared.Tokens{Input: 50, Output: 12}
		}),
	}
	store := warmedWith(t, rows...)

	// the spool is read first: its route lines
	store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		session(r, "demo-sess-1")
		r.ID, r.RequestID, r.Ts, r.Model, r.AgentID = "route:demo-sess-1:main:glm-5.3:1", "req_7f2k9", base+16_000, "glm-5.3", "main"
		r.Route, r.Via, r.Status, r.LatencyMs = str("provider"), str("zai"), str("200"), i64(1_400)
		r.Tokens = shared.Tokens{Input: 900, Output: 210}
	}), nil)
	store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		session(r, "demo-sess-1")
		r.ID, r.Ts, r.Model, r.AgentID = "route:demo-sess-1:scout-01:glm-5.3-flash:1", base+26_000, "glm-5.3-flash", "scout-01"
		r.Route, r.Via, r.Status, r.LatencyMs = str("provider"), str("zai"), str("200"), i64(6_000)
		r.Tokens = shared.Tokens{Input: 50, Output: 12}
	}), nil)
	// then the transcripts: their copies
	for _, copyOf := range []shared.RequestRecord{
		makeRequest(func(r *shared.RequestRecord) {
			session(r, "demo-sess-1")
			r.ID, r.Ts, r.What = "req_1001", base+4_000, str("↳ prompt  → Bash")
		}),
		makeRequest(func(r *shared.RequestRecord) {
			session(r, "demo-sess-1")
			r.ID, r.RequestID, r.Ts, r.Model, r.What = "req_7f2k9", "req_7f2k9", base+15_000, "glm-5.3", str("→ text")
			r.Tokens = shared.Tokens{Input: 900, Output: 210}
		}),
		makeRequest(func(r *shared.RequestRecord) {
			session(r, "demo-sess-1")
			r.ID, r.Ts, r.Model, r.AgentID, r.What = "req_2001", base+26_000, "glm-5.3-flash", "scout-01", str("→ text")
			r.Tokens = shared.Tokens{Input: 50, Output: 12}
		}),
		makeRequest(func(r *shared.RequestRecord) {
			session(r, "demo-sess-2")
			r.ID, r.Ts, r.Model, r.What = "req_3001", base-82_000_000, "kimi-k2", str("→ text")
		}),
	} {
		store.AddRequest(copyOf, nil)
	}
	ids := []string{}
	for _, request := range store.RequestsFor(RequestFilter{}) {
		ids = append(ids, request.ID)
	}
	slices.Sort(ids)
	want := []string{"req_1001", "req_3001", "route:demo-sess-1:main:glm-5.3:1", "route:demo-sess-1:scout-01:glm-5.3-flash:1"}
	if !slices.Equal(ids, want) {
		t.Errorf("the calls after the files are read again: got %v, want %v", ids, want)
	}
}

func TestWarmKeepsTheOrderTheSourceWroteTheRequestsIn(t *testing.T) {
	store := newTestStore()
	older := makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.SessionID, r.AgentID, r.Model, r.Ts = "older", "s1", "main", "claude-sonnet-5-5", 100
	})
	newer := makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.SessionID, r.AgentID, r.Model, r.Ts = "newer", "s1", "main", "glm-5.3", 300
	})
	source := &fakeWarmSource{
		requests: []shared.RequestRecord{newer, older},
		// the older call was written last, the way a transcript copy read after the spool is
		written: map[string]int64{"newer": 1, "older": 2},
	}
	if _, err := store.Warm(context.Background(), source); err != nil {
		t.Fatalf("warm: %v", err)
	}
	view := store.SessionDetail("s1")
	if view == nil || view.Model == nil || *view.Model != "claude-sonnet-5-5" {
		t.Errorf("the session's model: got %v, want the last written call's claude-sonnet-5-5", view)
	}
	var ids []string
	for _, record := range store.RequestsFor(RequestFilter{}) {
		ids = append(ids, record.ID)
	}
	if want := []string{"newer", "older"}; !slices.Equal(ids, want) {
		t.Errorf("the arrival order: got %v, want %v", ids, want)
	}
}
