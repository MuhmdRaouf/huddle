// The writer cases, plus the route-line capture reads the watcher runs with the writer
// attached.
package history

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
)

const writerT0 = int64(1_700_000_000_000)

// writerISO renders epoch ms the way the transcripts and spool lines stamp their times.
func writerISO(ms int64) string { return jsfmt.ISOTime(time.UnixMilli(ms)) }

func kindPtrOf(kind shared.AgentKind) *shared.AgentKind { return &kind }
func boolPtrOf(value bool) *bool                        { return &value }
func strPtrPtr(value *string) **string                  { return &value }

// writerWorld is one writer case's temp dir, store and history; the writer is stopped when
// the case ends.
type writerWorld struct {
	dir     string
	store   *store.Store
	history *History
	writer  *HistoryWriter

	logMu sync.Mutex
	logs  []string
}

func newWriterWorld(t *testing.T) *writerWorld {
	t.Helper()
	world := &writerWorld{dir: t.TempDir(), store: store.CreateStore(time.Now)}
	t.Cleanup(func() {
		if world.writer != nil {
			world.writer.Stop()
		}
	})
	return world
}

// attach opens a fresh history file in the temp dir and follows the store into it.
func (w *writerWorld) attach(t *testing.T) *History {
	t.Helper()
	h, err := OpenHistory(filepath.Join(w.dir, "history.db"), nil)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	w.history = h
	w.writer = AttachHistoryWriter(t.Context(), HistoryWriterOptions{
		Store:   w.store,
		History: h,
		Log:     w.logLine,
	})
	return h
}

func (w *writerWorld) logLine(line string) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	w.logs = append(w.logs, line)
}

func (w *writerWorld) errorLogs() []string {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	return slices.Clone(w.logs)
}

// writerRequest is the base request every case builds on, the shape makeRequest gives.
func writerRequest(shape func(*shared.RequestRecord)) shared.RequestRecord {
	record := shared.RequestRecord{
		ID:        "req-1",
		SessionID: "s1",
		AgentID:   "main",
		Model:     "claude-sonnet-5-5",
		Upstream:  "https://api.anthropic.com",
		Ts:        1_000,
		LatencyMs: i64p(100),
		Tokens:    shared.Tokens{Input: 10, Output: 5},
		Provider:  "Anthropic",
	}
	if shape != nil {
		shape(&record)
	}
	return record
}

// writerTool is the base tool call every case builds on, the shape makeTool gives.
func writerTool(shape func(*shared.ToolCallRecord)) shared.ToolCallRecord {
	tool := shared.ToolCallRecord{
		ID:         "tool-1",
		SessionID:  "s1",
		AgentID:    sp("main"),
		Name:       "Bash",
		StartedAt:  1_000,
		DurationMs: i64p(50),
		Ok:         true,
	}
	if shape != nil {
		shape(&tool)
	}
	return tool
}

// writerSpoolLine is one timeline line for a session, stamped T0+5.
func writerSpoolLine(event, sessionID string) shared.SpoolLine {
	return shared.SpoolLine{Ts: writerISO(writerT0 + 5), Event: event, SessionID: &sessionID}
}

// eventRow is one events row the writer logged, straight from sqlite (the History type has
// no events reader).
type eventRow struct {
	kind string
	node *string
}

func eventRows(t *testing.T, file string) []eventRow {
	t.Helper()
	db, err := OpenDB(file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer db.Close()
	rows, err := db.All("SELECT kind, node_id FROM events ORDER BY seq")
	if err != nil {
		t.Fatalf("read the events of %s: %v", file, err)
	}
	out := make([]eventRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, eventRow{kind: rowString(row, "kind"), node: rowText(row, "node_id")})
	}
	return out
}

// tableCount counts one table's rows straight from sqlite.
func tableCount(t *testing.T, file, table string) int64 {
	t.Helper()
	db, err := OpenDB(file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	defer db.Close()
	row, err := db.Get("SELECT COUNT(*) AS c FROM " + table)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return derep(rowInt64(row, "c"), 0)
}

// writerWaitFor polls probe every few milliseconds until it holds or the deadline passes.
func writerWaitFor(t *testing.T, label string, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !probe() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// writerEnv is one throwaway world for the ingest cases, built from scratch so no stray
// variable of the surrounding shell can leak a real path in.
func writerEnv(t *testing.T) domain.EnvLookup {
	t.Helper()
	home := t.TempDir()
	return domain.EnvLookup{
		"HOME":              home,
		"RADAR_HOME":        filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
		"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
	}
}

// writerWriteText mkdir -p's the parent, then writes with 0600.
func writerWriteText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writerAppendText appends, the way the spool and transcripts grow.
func writerAppendText(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := file.WriteString(text); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// writerJSONL marshals entries to one jsonl document, trailing newline included.
func writerJSONL(t *testing.T, entries ...any) string {
	t.Helper()
	var document strings.Builder
	for _, entry := range entries {
		data, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		document.Write(data)
		document.WriteByte('\n')
	}
	return document.String()
}

// nodeTriple is one tree row as the mapping cases list it: id, kind, parent.
type nodeTriple struct {
	id     string
	kind   string
	parent *string
}

func treeTriples(t *testing.T, h *History, rootID string) []nodeTriple {
	t.Helper()
	rows, err := h.Tree(rootID, TreeQuery{})
	if err != nil {
		t.Fatalf("read the tree of %s: %v", rootID, err)
	}
	triples := make([]nodeTriple, 0, len(rows))
	for _, row := range rows {
		triples = append(triples, nodeTriple{id: row.ID, kind: string(row.Kind), parent: row.ParentID})
	}
	return triples
}

// writerEqJSON compares two JSON texts as decoded values, the order of keys kept.
func writerEqJSON(t *testing.T, label, got, want string) {
	t.Helper()
	gotValue, err := jsfmt.DecodeValue([]byte(got))
	if err != nil {
		t.Fatalf("decode the %s got: %v", label, err)
	}
	wantValue, err := jsfmt.DecodeValue([]byte(want))
	if err != nil {
		t.Fatalf("decode the %s want: %v", label, err)
	}
	if gotValue.EncodeJSON() != wantValue.EncodeJSON() {
		t.Errorf("%s: got %s, want %s", label, gotValue.EncodeJSON(), wantValue.EncodeJSON())
	}
}

// jsonText is one captured JSON value as the text it encodes to.
func jsonText(value shared.JSON) string {
	if value.Value == nil {
		return "null"
	}
	return value.Value.EncodeJSON()
}

// manualWatch is a watcher the test drives by hand: the interval never fires, the clock
// moves only on a tick.
type manualWatch struct {
	watcher *ingest.Watcher
	tick    func(advanceMs int64)
}

func manualWatcher(t *testing.T, env domain.EnvLookup, st *store.Store, t0 int64) manualWatch {
	t.Helper()
	var clock atomic.Int64
	clock.Store(t0)
	w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env:        env,
		Store:      st,
		SinceMs:    86_400_000,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        clock.Load,
		Alive:      func(pid int64) bool { return pid == int64(os.Getpid()) },
	})
	t.Cleanup(w.Stop)
	writerWaitFor(t, "initial ingest", func() bool { return !w.CatchingUp() })
	return manualWatch{
		watcher: w,
		tick: func(advanceMs int64) {
			clock.Add(advanceMs)
			w.PollOnce()
			writerWaitFor(t, "ingest completion", func() bool { return !w.CatchingUp() })
		},
	}
}

// throwingHistory fails every write, the containment case's broken disk.
type throwingHistory struct{}

// panickingHistory blows up inside the transaction: a sink that panics is contained the
// same as one that fails.
type panickingHistory struct{}

func (panickingHistory) Transaction(fn func() error) error {
	fn()
	return nil
}
func (panickingHistory) UpsertNode(NodeUpsert) error                   { panic("sink blew up") }
func (panickingHistory) PutRequest(string, shared.RequestRecord) error { return nil }
func (panickingHistory) PutContent(string, ContentSides) error         { return nil }
func (panickingHistory) PutTool(string, shared.ToolCallRecord) error   { return nil }
func (panickingHistory) AddEvent(*string, shared.EventRecord) error    { return nil }
func (panickingHistory) PutCapture(shared.CaptureRecord) error         { return nil }
func (panickingHistory) Close() error                                  { return nil }

func (throwingHistory) Transaction(func() error) error                { return errors.New("disk gone") }
func (throwingHistory) UpsertNode(NodeUpsert) error                   { return nil }
func (throwingHistory) PutRequest(string, shared.RequestRecord) error { return nil }
func (throwingHistory) PutContent(string, ContentSides) error         { return nil }
func (throwingHistory) PutTool(string, shared.ToolCallRecord) error   { return nil }
func (throwingHistory) AddEvent(*string, shared.EventRecord) error    { return nil }
func (throwingHistory) PutCapture(shared.CaptureRecord) error         { return nil }
func (throwingHistory) Close() error                                  { return nil }

// countingWriterHistory counts the transactions and node writes that reach the history.
type countingWriterHistory struct {
	*History
	transactions *atomic.Int64
	seenMu       sync.Mutex
	seen         []string
}

func (c *countingWriterHistory) Transaction(fn func() error) error {
	if c.transactions != nil {
		c.transactions.Add(1)
	}
	return c.History.Transaction(fn)
}

func (c *countingWriterHistory) UpsertNode(n NodeUpsert) error {
	c.seenMu.Lock()
	c.seen = append(c.seen, n.ID)
	c.seenMu.Unlock()
	return c.History.UpsertNode(n)
}

func (c *countingWriterHistory) seenSnapshot() []string {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	return slices.Clone(c.seen)
}

func TestWriterNodeMapping(t *testing.T) {
	t.Run("maps the main session and its subagents, nested ones included", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{
			ID: "s1", Cwd: sp("/work/app"), Branch: sp("main"), CustomTitle: sp("Fix the login"),
		})
		world.store.UpsertAgent(store.AgentUpsert{
			SessionID: "s1", ID: "w1", Kind: kindPtrOf(shared.KindSubagent),
			ParentID: strPtrPtr(sp("main")), Name: sp("scout"),
		})
		world.store.UpsertAgent(store.AgentUpsert{
			SessionID: "s1", ID: "w2", Kind: kindPtrOf(shared.KindSubagent),
			ParentID: strPtrPtr(sp("w1")), Name: sp("deep"),
		})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) {
			r.ID, r.AgentID, r.Ts = "r1", "w2", writerT0
		}), nil)
		world.writer.Flush()
		rows, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		slices.SortStableFunc(rows, func(a, b shared.TreeNodeRow) int { return strings.Compare(a.ID, b.ID) }) // localeCompare
		got := make([]nodeTriple, 0, len(rows))
		for _, row := range rows {
			got = append(got, nodeTriple{id: row.ID, kind: string(row.Kind), parent: row.ParentID})
		}
		want := []nodeTriple{
			{"s1/main", "main", nil},
			{"s1/w1", "subagent", sp("s1/main")},
			{"s1/w2", "subagent", sp("s1/w1")},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("the tree: got %v, want %v", got, want)
		}
		root := rows[0]
		if !reflect.DeepEqual(root.Name, sp("Fix the login")) || !reflect.DeepEqual(root.Branch, sp("main")) ||
			!reflect.DeepEqual(root.Project, sp("app")) || !reflect.DeepEqual(root.Cwd, sp("/work/app")) {
			t.Errorf("the session node's facts: got name %v branch %v project %v cwd %v",
				root.Name, root.Branch, root.Project, root.Cwd)
		}
		var worker *shared.TreeNodeRow
		for at := range rows {
			if rows[at].ID == "s1/w1" {
				worker = &rows[at]
			}
		}
		if worker == nil || !reflect.DeepEqual(worker.Name, sp("scout")) || worker.AgentType != nil {
			t.Errorf("the w1 node: got %+v, want name scout and no agent type", worker)
		}
		requests, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/w2"), Limit: 10})
		if err != nil {
			t.Fatalf("read the requests: %v", err)
		}
		ids := make([]string, 0, len(requests))
		for _, request := range requests {
			ids = append(ids, request.ID)
		}
		if !reflect.DeepEqual(ids, []string{"r1"}) {
			t.Errorf("the w2 requests: got %v, want [r1]", ids)
		}
	})

	t.Run("maps a job session with and without a parent, its own agent on the job node", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/work/app")})
		world.store.UpsertSession(store.SessionUpsert{
			ID: "zai:j1", External: boolPtrOf(true), ParentSessionID: sp("s1"),
			Repo: sp("/work/app"), Branch: sp("feat"),
		})
		world.store.UpsertAgent(store.AgentUpsert{
			SessionID: "zai:j1", ID: "j1", Kind: kindPtrOf(shared.KindExternal), Name: sp("j1"),
		})
		world.store.UpsertAgent(store.AgentUpsert{
			SessionID: "zai:j1", ID: "w1", Kind: kindPtrOf(shared.KindSubagent),
			ParentID: strPtrPtr(sp("j1")), Name: sp("hand"),
		})
		world.store.UpsertSession(store.SessionUpsert{ID: "zai:j2", External: boolPtrOf(true)})
		world.writer.Flush()
		rows, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		var job, hand *shared.TreeNodeRow
		for at := range rows {
			switch rows[at].ID {
			case "job:zai:j1":
				job = &rows[at]
			case "zai:j1/w1":
				hand = &rows[at]
			}
		}
		if job == nil || job.Kind != shared.NodeJob || !reflect.DeepEqual(job.ParentID, sp("s1/main")) ||
			job.SessionID != "zai:j1" || job.AgentID != "j1" || !reflect.DeepEqual(job.Repo, sp("/work/app")) ||
			!reflect.DeepEqual(job.Branch, sp("feat")) || !reflect.DeepEqual(job.ParentSessionID, sp("s1")) {
			t.Errorf("the job node: got %+v", job)
		}
		if hand == nil || hand.Kind != shared.NodeSubagent || !reflect.DeepEqual(hand.ParentID, sp("job:zai:j1")) {
			t.Errorf("the job's worker node: got %+v", hand)
		}
		if got := treeTriples(t, h, "job:zai:j2"); !reflect.DeepEqual(got, []nodeTriple{{"job:zai:j2", "job", nil}}) {
			t.Errorf("the parentless job's tree: got %v", got)
		}
	})

	t.Run("ends a session's node when the store ends it, and a later request revives it", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/work/app")})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r1", writerT0 }), nil)
		world.store.EndSession("s1", writerT0+100)
		world.writer.Flush()
		ended, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if at := derep(firstTreeRow(t, ended).EndedAt, 0); at != writerT0+100 {
			t.Errorf("the ended node: got endedAt %d, want %d", at, writerT0+100)
		}
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r2", writerT0+200 }), nil)
		world.writer.Flush()
		revived, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if endedAt := firstTreeRow(t, revived).EndedAt; endedAt != nil {
			t.Errorf("the revived node: got endedAt %d, want none", *endedAt)
		}
	})
}

func TestWriterUnattachedGroup(t *testing.T) {
	t.Run("keeps sessionless requests out of the live scope, grouped as Unattached in history", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) {
			r.ID, r.SessionID, r.Ts, r.Model = "r9", shared.OutsideSession, writerT0, "glm-5.3"
		}), nil)
		world.writer.Flush()
		live, err := h.Roots(RootsQuery{Scope: ScopeLive, Now: writerT0 + 1, LiveMs: 60_000, Limit: 10})
		if err != nil {
			t.Fatalf("read the live roots: %v", err)
		}
		if len(live) != 0 {
			t.Errorf("traffic with no session is never a live session: got %v", rootIDs(live))
		}
		past, err := h.Roots(RootsQuery{Scope: ScopeHistory, Now: writerT0 + 60_000, LiveMs: 60_000, Limit: 10})
		if err != nil {
			t.Fatalf("read the history roots: %v", err)
		}
		if len(past) != 1 || past[0].ID != "outside-a-session/main" || !reflect.DeepEqual(past[0].Name, sp(shared.UnattachedName)) {
			t.Errorf("the unattached group: got %v", past)
		}
	})
}

func TestWriterRecords(t *testing.T) {
	t.Run("writes the requests, tools and events of an update beside their nodes", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		file := filepath.Join(world.dir, "history.db")
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/work/app")})
		want := writerRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts = "r1", writerT0
			r.CacheWrite1h, r.Speed, r.Geo, r.ServiceTier = i64p(33), sp("fast"), sp("us"), sp("priority")
		})
		world.store.AddRequest(want, nil)
		world.store.AddToolCall(writerTool(func(tool *shared.ToolCallRecord) { tool.ID = "t1" }))
		world.store.AddSpoolLine(writerSpoolLine("UserPromptSubmit", "s1"))
		world.writer.Flush()
		got, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read the requests: %v", err)
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("the stored request: got %+v, want %+v", got, want)
		}
		if count := tableCount(t, file, "tools"); count != 1 {
			t.Errorf("the tools table: got %d rows, want 1", count)
		}
		if rows := eventRows(t, file); !reflect.DeepEqual(rows, []eventRow{{kind: "UserPromptSubmit", node: sp("s1/main")}}) {
			t.Errorf("the events table: got %v", rows)
		}
	})

	t.Run("keeps a request's failure reason beside it and reads it back", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{ID: "s1"})
		want := writerRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts = "r1", writerT0
			r.StopReason, r.Error = sp("502"), sp("connection failed (ECONNREFUSED)")
		})
		world.store.AddRequest(want, nil)
		world.writer.Flush()
		got, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read the requests: %v", err)
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("the stored request: got %+v, want %+v", got, want)
		}
	})

	t.Run("keeps a route line's parent agent on the record and reads it back", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{ID: "s1"})
		want := writerRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.ParentAgentID = "r1", writerT0, sp("a31cd")
		})
		world.store.AddRequest(want, nil)
		world.writer.Flush()
		got, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read the requests: %v", err)
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("the stored request: got %+v, want %+v", got, want)
		}
	})

	t.Run("carries the request's captured input and output, merging a longer later chunk", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/work/app")})
		fullInput := `[{"type":"text","text":"a full prompt text"}]`
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r1", writerT0 }), &store.ContentPair{
			Input:  sp(fullInput),
			Output: sp(`[{"type":"text","text":"He"}]`),
		})
		world.writer.Flush()
		first, err := h.Content("r1")
		if err != nil {
			t.Fatalf("read the content: %v", err)
		}
		if first == nil || first.Input == nil || *first.Input != fullInput {
			t.Errorf("the first content: got %+v", first)
		}
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r1", writerT0+1 }), &store.ContentPair{
			Output: sp(`[{"type":"text","text":"Hello"}]`),
		})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r1", writerT0+2 }), &store.ContentPair{
			Input:  sp(`[{"type":"text","text":"tiny"}]`),
			Output: sp(`[{"type":"text","text":"ok"}]`),
		})
		world.writer.Flush()
		merged, err := h.Content("r1")
		if err != nil {
			t.Fatalf("read the content: %v", err)
		}
		wantOutput := `[{"type":"text","text":"Hello"}]`
		if merged == nil || merged.Input == nil || *merged.Input != fullInput || merged.Output == nil || *merged.Output != wantOutput {
			// a shorter later chunk never erases what it held
			t.Errorf("the merged content: got %+v", merged)
		}
		if merged != nil && merged.Bytes != int64(len(fullInput)+len(wantOutput)) {
			t.Errorf("the merged bytes: got %d, want %d", merged.Bytes, len(fullInput)+len(wantOutput))
		}
	})
}

func TestWriterWholeIngestPath(t *testing.T) {
	t.Run("writes what the watcher ingests: nodes, the request and its captured text", func(t *testing.T) {
		env := writerEnv(t)
		world := newWriterWorld(t)
		h := world.attach(t)
		writerWriteText(t, filepath.Join(paths.ClaudeConfigDir(env), "projects", "proj", "s1.jsonl"), writerJSONL(t,
			map[string]any{
				"type": "user", "timestamp": writerISO(writerT0), "cwd": "/w/app",
				"message": map[string]any{"content": "Fix the login bug"},
			},
			map[string]any{
				"type": "assistant", "timestamp": writerISO(writerT0 + 1000), "requestId": "req_1",
				"message": map[string]any{
					"model":       "claude-sonnet-5-5",
					"usage":       map[string]any{"input_tokens": 10, "output_tokens": 5},
					"stop_reason": "tool_use",
					"content": []any{
						map[string]any{"type": "text", "text": "On it"},
						map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash", "input": map[string]any{"command": "ls"}},
					},
				},
			},
			map[string]any{
				"type": "user", "timestamp": writerISO(writerT0 + 2000),
				"message": map[string]any{
					"content": []any{
						map[string]any{"type": "tool_result", "tool_use_id": "tu1", "content": "file one\nfile two"},
					},
				},
			},
			map[string]any{
				"type": "assistant", "timestamp": writerISO(writerT0 + 3000), "requestId": "req_2",
				"message": map[string]any{
					"model":       "claude-sonnet-5-5",
					"usage":       map[string]any{"input_tokens": 20, "output_tokens": 6},
					"stop_reason": "end_turn",
					"content":     []any{map[string]any{"type": "text", "text": "Done"}},
				},
			},
		))
		writerWriteText(t, filepath.Join(paths.SpoolDir(t.Context(), env), "spool.jsonl"),
			writerJSONL(t, map[string]any{"ts": writerISO(writerT0 + 5), "event": "SessionStart", "session_id": "s1"}))
		watcher := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
			Env: env, Store: world.store, SinceMs: 86_400_000, IntervalMs: 3_600_000,
			Now:   func() int64 { return time.Date(2026, 1, 15, 6, 30, 0, 0, time.UTC).UnixMilli() },
			Alive: func(pid int64) bool { return pid == int64(os.Getpid()) },
		})
		t.Cleanup(watcher.Stop)
		writerWaitFor(t, "initial ingest", func() bool { return !watcher.CatchingUp() })
		world.writer.Flush()
		var sessions []string
		for _, session := range world.store.SessionList() {
			sessions = append(sessions, session.ID)
		}
		if !reflect.DeepEqual(sessions, []string{"s1"}) {
			t.Errorf("the sessions: got %v, want [s1]", sessions)
		}
		if got := treeNodeIDs(mustTree(t, h, "s1/main")); !reflect.DeepEqual(got, []string{"s1/main"}) {
			t.Errorf("the tree: got %v", got)
		}
		requests, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read the requests: %v", err)
		}
		var ids []string
		for _, request := range requests {
			ids = append(ids, request.ID)
		}
		if !reflect.DeepEqual(ids, []string{"req_2", "req_1"}) {
			t.Errorf("the requests: got %v, want [req_2 req_1]", ids)
		}
		first, err := h.Content("req_1")
		if err != nil || first == nil {
			t.Fatalf("read the content of req_1: %v, %v", first, err)
		}
		writerEqJSON(t, "the req_1 input", derep(first.Input, ""), `[{"type":"text","text":"Fix the login bug"}]`)
		writerEqJSON(t, "the req_1 output", derep(first.Output, ""),
			`[{"type":"text","text":"On it"},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]`)
		second, err := h.Content("req_2")
		if err != nil || second == nil {
			t.Fatalf("read the content of req_2: %v, %v", second, err)
		}
		writerEqJSON(t, "the req_2 input", derep(second.Input, ""),
			`[{"type":"tool_result","tool_use_id":"tu1","text":"file one\nfile two"}]`)
		writerEqJSON(t, "the req_2 output", derep(second.Output, ""), `[{"type":"text","text":"Done"}]`)
	})
}

// mustTree reads one tree, failing the case when it cannot.
func mustTree(t *testing.T, h *History, rootID string) []shared.TreeNodeRow {
	t.Helper()
	rows, err := h.Tree(rootID, TreeQuery{})
	if err != nil {
		t.Fatalf("read the tree of %s: %v", rootID, err)
	}
	return rows
}

func TestWriterContainment(t *testing.T) {
	t.Run("detaches after one failed write and the store keeps working", func(t *testing.T) {
		world := newWriterWorld(t)
		world.writer = AttachHistoryWriter(t.Context(), HistoryWriterOptions{
			Store:   world.store,
			History: throwingHistory{},
			Log:     world.logLine,
		})
		world.store.UpsertSession(store.SessionUpsert{ID: "s1"})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID = "r1" }), nil)
		world.writer.Flush()
		var sessions []string
		for _, session := range world.store.SessionList() {
			sessions = append(sessions, session.ID)
		}
		if !reflect.DeepEqual(sessions, []string{"s1"}) {
			t.Errorf("the store kept working: got %v, want [s1]", sessions)
		}
		logs := world.errorLogs()
		if len(logs) != 1 || !strings.Contains(logs[0], "history write failed") {
			t.Errorf("the failure line: got %v", logs)
		}
	})

	t.Run("contains a panic inside the sink the same as a failed write", func(t *testing.T) {
		world := newWriterWorld(t)
		world.writer = AttachHistoryWriter(t.Context(), HistoryWriterOptions{
			Store:   world.store,
			History: panickingHistory{},
			Log:     world.logLine,
		})
		world.store.UpsertSession(store.SessionUpsert{ID: "s1"})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID = "r1" }), nil)
		world.writer.Flush()
		logs := world.errorLogs()
		if len(logs) != 1 || !strings.Contains(logs[0], "history write failed") {
			t.Errorf("the failure line: got %v", logs)
		}
		world.writer.Stop()
	})

	t.Run("keeps the write load flat: 5,000 requests through the writer in under 2 s", func(t *testing.T) {
		// the race detector slows the same work 5-20x, so under it the case runs a fifth of
		// the requests with the clock limit off; the count below still asserts every one of
		// them landed
		requests := 5_000
		if testkit.RaceEnabled {
			requests = 1_000
		}
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/w/app")})
		started := time.Now()
		for i := range requests {
			agent := "main"
			if i%10 != 0 {
				agent = fmt.Sprintf("w%d", i%3)
			}
			world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) {
				r.ID, r.AgentID, r.Ts = fmt.Sprintf("r%d", i), agent, writerT0+int64(i)
			}), nil)
		}
		world.writer.Flush()
		elapsed := time.Since(started)
		stats, err := h.Stats(StatsQuery{Now: time.Now().UnixMilli(), LiveMs: 15 * 60_000})
		if err != nil {
			t.Fatalf("read the stats: %v", err)
		}
		if stats.Requests != int64(requests) {
			t.Errorf("the stored requests: got %d, want %d", stats.Requests, requests)
		}
		// atomic coverage counters slow every statement; the limit stays a load check, not a
		// benchmark of the instrumented build
		limit := 2 * time.Second
		if testing.CoverMode() == "atomic" {
			limit = 6 * time.Second
		}
		if !testkit.RaceEnabled && elapsed >= limit {
			t.Errorf("the write load: %s for 5,000 requests, want under %s", elapsed, limit)
		}
	})
}

func TestWriterCoalescing(t *testing.T) {
	t.Run("queues many updates and writes them all in one transaction at the flush", func(t *testing.T) {
		world := newWriterWorld(t)
		file := filepath.Join(world.dir, "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		var transactions atomic.Int64
		counting := &countingWriterHistory{History: h, transactions: &transactions}
		w := AttachHistoryWriter(t.Context(), HistoryWriterOptions{Store: world.store, History: counting})
		world.writer = w
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/w/app")})
		for i := range 500 {
			world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) {
				r.ID, r.Ts = fmt.Sprintf("r%d", i), writerT0+int64(i)
			}), nil)
		}
		world.store.AddToolCall(writerTool(func(tool *shared.ToolCallRecord) { tool.ID = "t1" }))
		world.store.AddSpoolLine(writerSpoolLine("UserPromptSubmit", "s1"))
		if transactions.Load() != 0 {
			t.Errorf("nothing written while the batch waits: got %d transactions", transactions.Load())
		}
		w.Flush()
		if transactions.Load() != 1 {
			t.Errorf("the whole batch in one transaction: got %d transactions", transactions.Load())
		}
		stats, err := h.Stats(StatsQuery{Now: time.Now().UnixMilli(), LiveMs: 15 * 60_000})
		if err != nil {
			t.Fatalf("read the stats: %v", err)
		}
		if stats.Requests != 500 {
			t.Errorf("the stored requests: got %d, want 500", stats.Requests)
		}
		if got := treeNodeIDs(mustTree(t, h, "s1/main")); !reflect.DeepEqual(got, []string{"s1/main"}) {
			t.Errorf("the tree: got %v", got)
		}
		if count := tableCount(t, file, "tools"); count != 1 {
			t.Errorf("the tools table: got %d rows, want 1", count)
		}
		if rows := eventRows(t, file); !reflect.DeepEqual(rows, []eventRow{{kind: "UserPromptSubmit", node: sp("s1/main")}}) {
			t.Errorf("the events table: got %v", rows)
		}
	})

	t.Run("flushes what is queued when the writer stops, losing nothing", func(t *testing.T) {
		world := newWriterWorld(t)
		file := filepath.Join(world.dir, "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		world.writer = AttachHistoryWriter(t.Context(), HistoryWriterOptions{Store: world.store, History: h})
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/w/app")})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r1", writerT0 }), nil)
		world.writer.Stop() // no explicit flush: stop must write the batch before closing
		if count := tableCount(t, file, "requests"); count != 1 {
			t.Errorf("the requests table: got %d rows, want 1", count)
		}
		if count := tableCount(t, file, "nodes"); count != 1 {
			t.Errorf("the nodes table: got %d rows, want 1", count)
		}
	})

	t.Run("flushes on the 500 ms timer without anyone asking", func(t *testing.T) {
		world := newWriterWorld(t)
		h := world.attach(t)
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/w/app")})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r1", writerT0 }), nil)
		stats, err := h.Stats(StatsQuery{Now: time.Now().UnixMilli(), LiveMs: 15 * 60_000})
		if err != nil {
			t.Fatalf("read the stats: %v", err)
		}
		if stats.Requests != 0 {
			t.Errorf("nothing written before the timer: got %d requests", stats.Requests)
		}
		writerWaitFor(t, "the timer flush", func() bool {
			stats, err := h.Stats(StatsQuery{Now: time.Now().UnixMilli(), LiveMs: 15 * 60_000})
			return err == nil && stats.Requests == 1
		})
	})

	t.Run("re-derives node rows only for the sessions a batch touched", func(t *testing.T) {
		world := newWriterWorld(t)
		h, err := OpenHistory(filepath.Join(world.dir, "history.db"), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		counting := &countingWriterHistory{History: h}
		w := AttachHistoryWriter(t.Context(), HistoryWriterOptions{Store: world.store, History: counting})
		world.writer = w
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/w/app")})
		world.store.UpsertSession(store.SessionUpsert{ID: "s2", Cwd: sp("/w/lib")})
		world.store.UpsertAgent(store.AgentUpsert{
			SessionID: "s2", ID: "w9", Kind: kindPtrOf(shared.KindSubagent), Name: sp("s2 worker"),
		})
		w.Flush()
		counting.seenMu.Lock()
		counting.seen = nil
		counting.seenMu.Unlock()
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) {
			r.ID, r.AgentID, r.Ts = "r1", "w1", writerT0
		}), nil)
		w.Flush()
		// s1's new agent only; s2's nodes were left alone
		if got := counting.seenSnapshot(); !reflect.DeepEqual(got, []string{"s1/main", "s1/w1"}) {
			t.Errorf("the re-derived nodes: got %v, want [s1/main s1/w1]", got)
		}
		w.Stop()
	})

	t.Run("ends the flush loop and flushes what is queued when its context ends", func(t *testing.T) {
		world := newWriterWorld(t)
		file := filepath.Join(world.dir, "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		w := AttachHistoryWriter(ctx, HistoryWriterOptions{Store: world.store, History: h})
		world.writer = w
		world.store.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: sp("/w/app")})
		world.store.AddRequest(writerRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "r1", writerT0 }), nil)
		cancel()
		select {
		case <-w.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the flush loop never ended")
		}
		if count := tableCount(t, file, "requests"); count != 1 {
			t.Errorf("the context's end flushes the batch: got %d requests, want 1", count)
		}
	})
}

func TestWriterRouteCapture(t *testing.T) {
	t.Run("links a route line's prompt hash and headers, and stores each capture once in the history", func(t *testing.T) {
		env := writerEnv(t)
		t0 := time.Date(2026, 1, 15, 6, 30, 0, 0, time.UTC).UnixMilli()
		t1 := t0 + 1_000
		hash := strings.Repeat("a", 64)
		payload := `{"system":"be brief","tools":[{"name":"Bash"}]}`
		var packed bytes.Buffer
		gzipWriter := gzip.NewWriter(&packed)
		if _, err := gzipWriter.Write([]byte(payload)); err != nil {
			t.Fatalf("gzip the capture: %v", err)
		}
		if err := gzipWriter.Close(); err != nil {
			t.Fatalf("close the capture stream: %v", err)
		}
		gz := base64.StdEncoding.EncodeToString(packed.Bytes())
		route := map[string]any{
			"ts": writerISO(t1), "event": "route", "session_id": "s9", "model": "glm-5.3",
			"upstream": "http://127.0.0.1:8787", "route": "provider", "status": 200,
			"latency_ms": 42, "prompt_hash": hash,
			"headers": map[string]any{"request-id": "req_1", "retry-after": "7", "set-cookie": "nope"},
		}
		routeAgain := map[string]any{}
		for key, value := range route {
			routeAgain[key] = value
		}
		routeAgain["ts"] = writerISO(t0)
		captureLine := map[string]any{
			"ts": writerISO(t0), "event": "capture", "plugin": "zai", "prompt_hash": hash, "gz": gz,
		}
		spoolFile := filepath.Join(paths.SpoolDir(t.Context(), env), "2026-01-15.jsonl")
		writerWriteText(t, spoolFile, writerJSONL(t, routeAgain, captureLine))
		world := newWriterWorld(t)
		h, err := OpenHistory(filepath.Join(world.dir, "history.db"), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		world.writer = AttachHistoryWriter(t.Context(), HistoryWriterOptions{Store: world.store, History: h})
		manual := manualWatcher(t, env, world.store, t0)

		// the request carries the hash and only the allow-listed headers its line named
		routed := world.store.Requests(store.RequestFilter{Session: sp("s9")})
		if len(routed) != 1 {
			t.Fatalf("the routed requests: got %d, want 1", len(routed))
		}
		wantHeaders := &shared.Headers{{Name: "request-id", Value: "req_1"}, {Name: "retry-after", Value: "7"}}
		if routed[0].PromptHash == nil || *routed[0].PromptHash != hash {
			t.Errorf("the routed request's hash: got %v", routed[0].PromptHash)
		}
		if !reflect.DeepEqual(routed[0].Headers, wantHeaders) {
			t.Errorf("the routed request's headers: got %v, want %v", routed[0].Headers, wantHeaders)
		}
		world.writer.Flush()
		capture, err := h.Capture(routed[0].ID)
		if err != nil {
			t.Fatalf("read the capture: %v", err)
		}
		if capture == nil || capture.Hash != hash {
			t.Fatalf("the capture: got %+v", capture)
		}
		writerEqJSON(t, "the capture's system", jsonText(capture.System), `"be brief"`)
		writerEqJSON(t, "the capture's tools", jsonText(capture.Tools), `[{"name":"Bash"}]`)
		if capture.Bytes != int64(len(payload)) {
			t.Errorf("the capture's bytes: got %d, want %d", capture.Bytes, len(payload))
		}
		if !reflect.DeepEqual(capture.Headers, wantHeaders) {
			t.Errorf("the capture's headers: got %v", capture.Headers)
		}

		// the same capture line again (a second router saw the prompt, say) keeps the first
		// row as it was
		writerAppendText(t, spoolFile, writerJSONL(t, map[string]any{
			"ts": writerISO(t1), "event": "capture", "plugin": "zai", "prompt_hash": hash, "gz": gz,
		}))
		manual.tick(1_000)
		world.writer.Flush()
		again, err := h.Capture(routed[0].ID)
		if err != nil {
			t.Fatalf("read the capture again: %v", err)
		}
		if again == nil || again.Hash != hash {
			t.Errorf("the kept capture: got %+v", again)
		}

		// a request whose route line named no hash has no capture to read
		nowhere, err := h.Capture("route:nowhere")
		if err != nil {
			t.Fatalf("read the nowhere capture: %v", err)
		}
		if nowhere != nil {
			t.Errorf("the nowhere capture: got %+v, want none", nowhere)
		}
	})
}
