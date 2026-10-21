package history

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
)

// The history surface over temp directories: the opener's file rules, the reads a v1
// upgrade answers, and every query the store serves.

// i64p and sp are the usual pointer-of helpers.
func i64p(v int64) *int64 { return &v }
func sp(v string) *string { return &v }

// openHistoryAt opens a fresh history file in the test's temp directory.
func openHistoryAt(t *testing.T) *History {
	t.Helper()
	h, err := OpenHistory(filepath.Join(t.TempDir(), "history.db"), nil)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// nodeUpsert is the base node every case builds on.
func nodeUpsert() NodeUpsert {
	return NodeUpsert{ID: "s1/main", Kind: shared.NodeMain, SessionID: "s1", AgentID: "main"}
}

// requestRecord is the base request every case builds on.
func requestRecord() shared.RequestRecord {
	latency := int64(10)
	return shared.RequestRecord{
		ID:        "r1",
		SessionID: "s1",
		AgentID:   "main",
		Model:     "glm-5.3-flash",
		Upstream:  "https://api.z.ai",
		Ts:        1000,
		LatencyMs: &latency,
		Tokens:    shared.Tokens{Input: 10, Output: 4},
		Provider:  "zai",
	}
}

// countedAll remembers one read: the SQL and how many rows it answered.
type countedAll struct {
	sql  string
	rows int
}

// countingDB wraps a database, remembering every read, so a test can pin the query count
// and the row bound.
type countingDB struct {
	DB
	alls []countedAll
	gets []string
}

func (c *countingDB) All(query string, params ...SqlValue) ([]map[string]SqlValue, error) {
	rows, err := c.DB.All(query, params...)
	c.alls = append(c.alls, countedAll{sql: query, rows: len(rows)})
	return rows, err
}

func (c *countingDB) Get(query string, params ...SqlValue) (map[string]SqlValue, error) {
	c.gets = append(c.gets, query)
	return c.DB.Get(query, params...)
}

const (
	nowMs  = int64(1_000_000)
	liveMs = int64(60_000)
)

func historyQ() RootsQuery {
	return RootsQuery{Scope: ScopeHistory, Now: nowMs, LiveMs: liveMs, Limit: 50}
}

func liveQ() RootsQuery {
	return RootsQuery{Scope: ScopeLive, Now: nowMs, LiveMs: liveMs, Limit: 50}
}

// rootIDs lists the ids of a roots page.
func rootIDs(roots []shared.RootSummary) []string {
	ids := make([]string, 0, len(roots))
	for _, root := range roots {
		ids = append(ids, root.ID)
	}
	return ids
}

// treeNodeIDs lists the ids of a tree's rows.
func treeNodeIDs(rows []shared.TreeNodeRow) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// firstTreeRow answers a tree's first row, failing the test when the tree is empty.
func firstTreeRow(t *testing.T, rows []shared.TreeNodeRow) shared.TreeNodeRow {
	t.Helper()
	if len(rows) == 0 {
		t.Fatal("the tree holds no rows")
	}
	return rows[0]
}

// requestIDs lists the ids of a request page.
func requestIDs(rows []shared.RequestRecord) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func TestOpenHistory(t *testing.T) {
	t.Run("creates a nested parent directory and leaves the database in WAL mode", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "nested", "deeper", "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		n := nodeUpsert()
		n.LastAt = i64p(nowMs)
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert node: %v", err)
		}
		if err := h.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		for _, dir := range []string{filepath.Join(dir, "nested"), filepath.Join(dir, "nested", "deeper")} {
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("stat %s: %v", dir, err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Errorf("%s mode = %o, want 700", dir, got)
			}
		}
		probe, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = probe.Close() })
		row, err := probe.Get("PRAGMA journal_mode")
		if err != nil {
			t.Fatalf("read the journal mode: %v", err)
		}
		if got := textOf(row, "journal_mode"); got != "wal" {
			t.Errorf("journal_mode = %q, want wal", got)
		}
	})

	t.Run("keeps the file at 0600", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		if err := h.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatalf("stat the file: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("file mode = %o, want 600", got)
		}
	})

	t.Run("upgrades a v1 database in place, keeping its rows", func(t *testing.T) {
		db, file := buildV1(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close the writer: %v", err)
		}
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		r2 := requestRecord()
		r2.ID = "r2"
		r2.Ts = 9
		r2.Speed = sp("fast")
		r2.CacheWrite1h = i64p(3)
		r2.Status = sp("200")
		if err := h.PutRequest("s1/main", r2); err != nil {
			t.Fatalf("put request: %v", err)
		}
		rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read requests: %v", err)
		}
		if want := []string{"r2", "r1"}; !reflect.DeepEqual(requestIDs(rows), want) {
			t.Errorf("requests = %v, want %v", requestIDs(rows), want)
		}
		if len(rows) < 2 {
			t.Fatal("the widened table read back fewer requests than it held")
		}
		if got := rows[0].Speed; got == nil || *got != "fast" {
			t.Errorf("speed = %v, want fast", got)
		}
		if got := rows[0].CacheWrite1h; got == nil || *got != 3 {
			t.Errorf("cacheWrite1h = %v, want 3", got)
		}
		if got := rows[0].Status; got == nil || *got != "200" {
			t.Errorf("status = %v, want 200", got)
		}
		if got := rows[1].Speed; got != nil {
			t.Errorf("old request speed = %v, want nil", got)
		}
		ag := nodeUpsert()
		ag.ID = "s1/ag"
		ag.Kind = shared.NodeSubagent
		ag.ParentID = sp("s1/main")
		ag.AgentID = "ag"
		ag.Repo = sp("/w/app")
		ag.Name = sp("scout")
		if err := h.UpsertNode(ag); err != nil {
			t.Fatalf("upsert the subagent: %v", err)
		}
		tree, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		var agent *shared.TreeNodeRow
		for at := range tree {
			if tree[at].ID == "s1/ag" {
				agent = &tree[at]
			}
		}
		if agent == nil {
			t.Fatal("the tree holds no s1/ag row")
		}
		if got := agent.Repo; got == nil || *got != "/w/app" {
			t.Errorf("repo = %v, want /w/app", got)
		}
		if got := agent.Name; got == nil || *got != "scout" {
			t.Errorf("name = %v, want scout", got)
		}
		if got := agent.ParentSessionID; got != nil {
			t.Errorf("parentSessionId = %v, want nil", got)
		}
		head := firstTreeRow(t, tree)
		if got := head.Label; got == nil || *got != "before" {
			t.Errorf("label = %v, want before", got)
		}
		if got := head.LastAt; got != 9 {
			t.Errorf("lastAt = %d, want 9", got)
		}
		probe, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = probe.Close() })
		if got, want := versionOf(t, probe), strconv.Itoa(SchemaVersion); got != want {
			t.Errorf("version = %q, want %q", got, want)
		}
	})
}

func TestNodes(t *testing.T) {
	t.Run("merges: minimum started_at, maximum last_at, null keeps the stored value", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		first := nodeUpsert()
		first.StartedAt = i64p(500)
		first.LastAt = i64p(900)
		first.Cwd = sp("/work/app")
		first.Model = sp("m1")
		first.Label = sp("first")
		upsert(first)
		second := nodeUpsert()
		second.StartedAt = i64p(300)
		second.LastAt = i64p(1200)
		second.Model = sp("m2")
		upsert(second)
		third := nodeUpsert()
		third.Label = nil
		third.Cwd = nil
		upsert(third)
		tree, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		row := firstTreeRow(t, tree)
		if got := row.StartedAt; got == nil || *got != 300 {
			t.Errorf("startedAt = %v, want 300", got)
		}
		if got := row.LastAt; got != 1200 {
			t.Errorf("lastAt = %d, want 1200", got)
		}
		if got := row.Cwd; got == nil || *got != "/work/app" {
			t.Errorf("cwd = %v, want /work/app", got)
		}
		if got := row.Model; got == nil || *got != "m2" {
			t.Errorf("model = %v, want m2", got)
		}
		if got := row.Label; got == nil || *got != "first" {
			t.Errorf("label = %v, want first", got)
		}
		// a stored null takes the incoming value instead of keeping it
		fresh := nodeUpsert()
		fresh.ID = "s2/main"
		fresh.SessionID = "s2"
		upsert(fresh)
		stamped := nodeUpsert()
		stamped.ID = "s2/main"
		stamped.SessionID = "s2"
		stamped.StartedAt = i64p(77)
		stamped.LastAt = i64p(88)
		upsert(stamped)
		tree, err = h.Tree("s2/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the second tree: %v", err)
		}
		if got := firstTreeRow(t, tree).StartedAt; got == nil || *got != 77 {
			t.Errorf("second startedAt = %v, want 77", got)
		}
		if got := firstTreeRow(t, tree).LastAt; got != 88 {
			t.Errorf("second lastAt = %d, want 88", got)
		}
	})

	t.Run("ends a node and revives it when a later write moves last_at past the end", func(t *testing.T) {
		h := openHistoryAt(t)
		n := nodeUpsert()
		n.LastAt = i64p(100)
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.EndNode("s1/main", 800); err != nil {
			t.Fatalf("end: %v", err)
		}
		endedAt := func() *int64 {
			t.Helper()
			tree, err := h.Tree("s1/main", TreeQuery{})
			if err != nil {
				t.Fatalf("read the tree: %v", err)
			}
			return firstTreeRow(t, tree).EndedAt
		}
		if got := endedAt(); got == nil || *got != 800 {
			t.Errorf("endedAt = %v, want 800", got)
		}
		if err := h.EndNode("s1/main", 500); err != nil {
			t.Fatalf("end earlier: %v", err)
		}
		// an earlier end does not move it back
		if got := endedAt(); got == nil || *got != 800 {
			t.Errorf("endedAt = %v, want 800", got)
		}
		revived := nodeUpsert()
		revived.LastAt = i64p(900)
		if err := h.UpsertNode(revived); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got := endedAt(); got != nil {
			t.Errorf("endedAt = %v, want nil", got)
		}
	})

	t.Run("moves the root_id of the node and its descendants when the parent changes", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		main := nodeUpsert()
		main.ID = "a/main"
		upsert(main)
		ag := nodeUpsert()
		ag.ID = "a/ag"
		ag.Kind = shared.NodeSubagent
		ag.ParentID = sp("a/main")
		ag.AgentID = "ag"
		ag.LastAt = i64p(50)
		upsert(ag)
		sub := nodeUpsert()
		sub.ID = "a/ag/sub"
		sub.Kind = shared.NodeSubagent
		sub.ParentID = sp("a/ag")
		sub.AgentID = "sub"
		sub.LastAt = i64p(40)
		upsert(sub)
		b := nodeUpsert()
		b.ID = "b/main"
		b.SessionID = "b"
		upsert(b)
		moved := ag
		moved.ParentID = sp("b/main")
		upsert(moved)
		ids := func(root string) []string {
			t.Helper()
			tree, err := h.Tree(root, TreeQuery{})
			if err != nil {
				t.Fatalf("read the tree: %v", err)
			}
			return treeNodeIDs(tree)
		}
		if want := []string{"a/main"}; !reflect.DeepEqual(ids("a/main"), want) {
			t.Errorf("a/main tree = %v, want %v", ids("a/main"), want)
		}
		if want := []string{"b/main", "a/ag", "a/ag/sub"}; !reflect.DeepEqual(ids("b/main"), want) {
			t.Errorf("b/main tree = %v, want %v", ids("b/main"), want)
		}
	})

	t.Run("adopts children that named their parent before it existed", func(t *testing.T) {
		h := openHistoryAt(t)
		job := nodeUpsert()
		job.ID = "job:zai:1"
		job.Kind = shared.NodeJob
		job.ParentID = sp("a/main")
		job.SessionID = "s"
		job.AgentID = "job"
		if err := h.UpsertNode(job); err != nil {
			t.Fatalf("upsert the job: %v", err)
		}
		tree, err := h.Tree("a/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if len(tree) != 0 {
			t.Errorf("tree = %v, want empty", treeNodeIDs(tree))
		}
		main := nodeUpsert()
		main.ID = "a/main"
		if err := h.UpsertNode(main); err != nil {
			t.Fatalf("upsert the parent: %v", err)
		}
		tree, err = h.Tree("a/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if want := []string{"a/main", "job:zai:1"}; !reflect.DeepEqual(treeNodeIDs(tree), want) {
			t.Errorf("tree = %v, want %v", treeNodeIDs(tree), want)
		}
	})

	t.Run("bumps last_at on putRequest, never lowering it with an older request", func(t *testing.T) {
		h := openHistoryAt(t)
		n := nodeUpsert()
		n.LastAt = i64p(100)
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		put := func(id string, ts int64) {
			t.Helper()
			r := requestRecord()
			r.ID = id
			r.Ts = ts
			if err := h.PutRequest("s1/main", r); err != nil {
				t.Fatalf("put request %s: %v", id, err)
			}
		}
		lastAt := func() int64 {
			t.Helper()
			tree, err := h.Tree("s1/main", TreeQuery{})
			if err != nil {
				t.Fatalf("read the tree: %v", err)
			}
			return firstTreeRow(t, tree).LastAt
		}
		put("r1", 400)
		if got := lastAt(); got != 400 {
			t.Errorf("lastAt = %d, want 400", got)
		}
		put("r2", 200)
		if got := lastAt(); got != 400 {
			t.Errorf("lastAt = %d, want 400", got)
		}
	})
}

func TestContent(t *testing.T) {
	t.Run("round-trips text including nulls and multi-byte characters, gzipped on disk", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.PutRequest("s1/main", requestRecord()); err != nil {
			t.Fatalf("put request: %v", err)
		}
		text := "héllo ← 世界"
		if err := h.PutContent("r1", ContentSides{Input: &text}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		stored, err := h.Content("r1")
		if err != nil {
			t.Fatalf("read content: %v", err)
		}
		if stored == nil {
			t.Fatal("no content row came back")
		}
		if stored.Input == nil || *stored.Input != text {
			t.Errorf("input = %v, want %q", stored.Input, text)
		}
		if stored.Output != nil {
			t.Errorf("output = %v, want nil", stored.Output)
		}
		if got, want := stored.Bytes, int64(len(text)); got != want {
			t.Errorf("bytes = %d, want %d", got, want)
		}
		if err := h.PutContent("r1", ContentSides{Output: sp("out")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		stored, err = h.Content("r1")
		if err != nil {
			t.Fatalf("read content: %v", err)
		}
		if stored == nil {
			t.Fatal("no content row came back")
		}
		if stored.Input == nil || *stored.Input != text {
			t.Errorf("input = %v, want %q", stored.Input, text)
		}
		if stored.Output == nil || *stored.Output != "out" {
			t.Errorf("output = %v, want out", stored.Output)
		}
		if got, want := stored.Bytes, int64(len(text)+len("out")); got != want {
			t.Errorf("bytes = %d, want %d", got, want)
		}
		missing, err := h.Content("missing")
		if err != nil {
			t.Fatalf("read missing: %v", err)
		}
		if missing != nil {
			t.Errorf("missing content = %v, want nil", missing)
		}
		if err := h.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		row, err := db.Get("SELECT input, output FROM content WHERE request_id = 'r1'")
		if err != nil {
			t.Fatalf("read the blob: %v", err)
		}
		for _, side := range []string{"input", "output"} {
			blob, _ := row[side].([]byte)
			if len(blob) < 2 || blob[0] != 0x1f || blob[1] != 0x8b {
				t.Errorf("%s is not stored as gzip: %v", side, blob)
			}
		}
	})

	t.Run("drops content when its request row goes (ON DELETE CASCADE)", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.PutRequest("s1/main", requestRecord()); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.PutContent("r1", ContentSides{Input: sp("in"), Output: sp("out")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the second connection: %v", err)
		}
		if _, err := db.Run("DELETE FROM requests WHERE id = 'r1'"); err != nil {
			t.Fatalf("delete the request: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		stored, err := h.Content("r1")
		if err != nil {
			t.Fatalf("read content: %v", err)
		}
		if stored != nil {
			t.Errorf("content = %v, want nil", stored)
		}
	})

	t.Run("merges a streamed rewrite by length and keeps each side's plain text length beside the blob", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.PutRequest("s1/main", requestRecord()); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.PutContent("r1", ContentSides{Input: sp("hello")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		// both sides grew
		if err := h.PutContent("r1", ContentSides{Input: sp("hello there"), Output: sp("an answer")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		content := func() *ContentAnswer {
			t.Helper()
			stored, err := h.Content("r1")
			if err != nil {
				t.Fatalf("read content: %v", err)
			}
			if stored == nil {
				t.Fatal("content went missing")
			}
			return stored
		}
		stored := content()
		if stored.Input == nil || *stored.Input != "hello there" {
			t.Errorf("input = %v, want hello there", stored.Input)
		}
		if stored.Output == nil || *stored.Output != "an answer" {
			t.Errorf("output = %v, want an answer", stored.Output)
		}
		if got, want := stored.Bytes, int64(len("hello there")+len("an answer")); got != want {
			t.Errorf("bytes = %d, want %d", got, want)
		}
		// a short retry echo never erases
		if err := h.PutContent("r1", ContentSides{Input: sp("no")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		if got := content().Input; got == nil || *got != "hello there" {
			t.Errorf("input = %v, want hello there", got)
		}
		// equal length keeps the stored side
		if err := h.PutContent("r1", ContentSides{Input: sp("hello _____"), Output: sp("answer!")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		if got := content().Input; got == nil || *got != "hello there" {
			t.Errorf("input = %v, want hello there", got)
		}
		// a null side keeps what is stored
		if err := h.PutContent("r1", ContentSides{}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		stored = content()
		if stored.Input == nil || *stored.Input != "hello there" {
			t.Errorf("input = %v, want hello there", stored.Input)
		}
		if stored.Output == nil || *stored.Output != "an answer" {
			t.Errorf("output = %v, want an answer", stored.Output)
		}
		if got, want := stored.Bytes, int64(len("hello there")+len("an answer")); got != want {
			t.Errorf("bytes = %d, want %d", got, want)
		}
		if err := h.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		row, err := db.Get("SELECT in_len, out_len FROM content WHERE request_id = 'r1'")
		if err != nil {
			t.Fatalf("read the lengths: %v", err)
		}
		if got := row["in_len"]; got != int64(len("hello there")) {
			t.Errorf("in_len = %v, want %d", got, len("hello there"))
		}
		if got := row["out_len"]; got != int64(len("an answer")) {
			t.Errorf("out_len = %v, want %d", got, len("an answer"))
		}
	})
}

func TestSideOfNext(t *testing.T) {
	stored := func() (StoredSide, string) {
		text := "stored text"
		return StoredSide{Blob: gzipText(t, text), Len: i64p(int64(len(text)))}, text
	}

	t.Run("hands the stored blob back untouched when the incoming text is not longer", func(t *testing.T) {
		side, text := stored()
		short := SideOfNext(side, sp("short"))
		if !reflect.DeepEqual(short, side) {
			t.Error("a shorter incoming text changed the stored side")
		}
		same := SideOfNext(side, sp(text))
		if !reflect.DeepEqual(same, side) {
			t.Error("an equal-length incoming text changed the stored side")
		}
		kept := SideOfNext(side, nil)
		if !reflect.DeepEqual(kept, side) {
			t.Error("a null incoming text changed the stored side")
		}
	})

	t.Run("gzips only a side that grew, and a missing stored side takes the incoming text", func(t *testing.T) {
		side, _ := stored()
		grown := SideOfNext(side, sp("stored text and more"))
		if reflect.DeepEqual(grown, side) {
			t.Error("a grown side kept the stored blob")
		}
		if grown.Blob == nil || gunzipText(t, grown.Blob) != "stored text and more" {
			t.Error("the grown blob does not read back as the new text")
		}
		if grown.Len == nil || *grown.Len != int64(len("stored text and more")) {
			t.Errorf("grown len = %v, want %d", grown.Len, len("stored text and more"))
		}
		empty := StoredSide{}
		took := SideOfNext(empty, sp("fresh"))
		if took.Len == nil || *took.Len != 5 {
			t.Errorf("fresh len = %v, want 5", took.Len)
		}
		if took.Blob == nil || gunzipText(t, took.Blob) != "fresh" {
			t.Error("the fresh blob does not read back as fresh")
		}
		if got := SideOfNext(empty, nil); !reflect.DeepEqual(got, empty) {
			t.Error("a null incoming text changed an empty side")
		}
	})
}

func TestOffsetsToolsEvents(t *testing.T) {
	t.Run("stores and replaces one offset per file", func(t *testing.T) {
		h := openHistoryAt(t)
		none, err := h.Offset("spool/a.jsonl")
		if err != nil {
			t.Fatalf("read offset: %v", err)
		}
		if none != nil {
			t.Errorf("offset = %v, want nil", none)
		}
		if err := h.SetOffset("spool/a.jsonl", FileOffset{Offset: 10, Size: 5, Mtime: 99}); err != nil {
			t.Fatalf("set offset: %v", err)
		}
		offset, err := h.Offset("spool/a.jsonl")
		if err != nil {
			t.Fatalf("read offset: %v", err)
		}
		if offset == nil {
			t.Fatal("no offset came back")
		}
		if want := (FileOffset{Offset: 10, Size: 5, Mtime: 99}); *offset != want {
			t.Errorf("offset = %v, want %v", *offset, want)
		}
		if err := h.SetOffset("spool/a.jsonl", FileOffset{Offset: 20, Size: 5, Mtime: 99}); err != nil {
			t.Fatalf("set offset: %v", err)
		}
		offset, err = h.Offset("spool/a.jsonl")
		if err != nil {
			t.Fatalf("read offset: %v", err)
		}
		if offset == nil {
			t.Fatal("no offset came back")
		}
		if offset.Offset != 20 {
			t.Errorf("offset = %d, want 20", offset.Offset)
		}
	})

	t.Run("keeps tool calls and ignores a duplicate event seq", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		t1 := shared.ToolCallRecord{ID: "t1", SessionID: "s1", AgentID: sp("main"), Name: "Bash",
			StartedAt: 5, DurationMs: i64p(1), Ok: true, InputKey: sp("k1")}
		if err := h.PutTool("s1/main", t1); err != nil {
			t.Fatalf("put tool: %v", err)
		}
		t2 := shared.ToolCallRecord{ID: "t2", SessionID: "s1", Name: "Read", StartedAt: 6, Ok: false}
		if err := h.PutTool("s1/main", t2); err != nil {
			t.Fatalf("put tool: %v", err)
		}
		first := shared.EventRecord{Seq: 1, Ts: 7, Kind: "PostToolUse", SessionID: sp("s1"),
			AgentID: sp("main"), Payload: map[string]any{"a": 1}}
		if err := h.AddEvent(sp("s1/main"), first); err != nil {
			t.Fatalf("add event: %v", err)
		}
		dup := shared.EventRecord{Seq: 1, Ts: 7, Kind: "PostToolUse", Payload: map[string]any{"a": 1}}
		if err := h.AddEvent(nil, dup); err != nil {
			t.Fatalf("add duplicate event: %v", err)
		}
		second := shared.EventRecord{Seq: 2, Ts: 8, Kind: "Notice", Label: sp("limit")}
		if err := h.AddEvent(nil, second); err != nil {
			t.Fatalf("add event: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		tools, err := db.Get("SELECT COUNT(*) AS c FROM tools")
		if err != nil {
			t.Fatalf("count tools: %v", err)
		}
		if got := tools["c"]; got != int64(2) {
			t.Errorf("tools = %v, want 2", got)
		}
		toolRows, err := db.All("SELECT id, ok, input_key FROM tools ORDER BY id")
		if err != nil {
			t.Fatalf("read tools: %v", err)
		}
		wantTools := []map[string]SqlValue{
			{"id": "t1", "ok": int64(1), "input_key": "k1"},
			{"id": "t2", "ok": int64(0), "input_key": nil},
		}
		if !reflect.DeepEqual(toolRows, wantTools) {
			t.Errorf("tools = %v, want %v", toolRows, wantTools)
		}
		events, err := db.Get("SELECT COUNT(*) AS c FROM events")
		if err != nil {
			t.Fatalf("count events: %v", err)
		}
		if got := events["c"]; got != int64(2) {
			t.Errorf("events = %v, want 2", got)
		}
		eventRows, err := db.All("SELECT seq, node_id, payload FROM events ORDER BY seq")
		if err != nil {
			t.Fatalf("read events: %v", err)
		}
		wantEvents := []map[string]SqlValue{
			{"seq": int64(1), "node_id": "s1/main", "payload": `{"a":1}`},
			{"seq": int64(2), "node_id": nil, "payload": "null"},
		}
		if !reflect.DeepEqual(eventRows, wantEvents) {
			t.Errorf("events = %v, want %v", eventRows, wantEvents)
		}
	})
}

func TestRoots(t *testing.T) {
	t.Run("splits live trees from history: main sessions only, a job never a top-level root", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		a := nodeUpsert()
		a.ID = "a/main"
		a.StartedAt = i64p(1)
		a.LastAt = i64p(nowMs)
		a.Cwd = sp("/work/app")
		a.Project = sp("app")
		upsert(a)
		b := nodeUpsert()
		b.ID = "b/main"
		b.SessionID = "b"
		b.StartedAt = i64p(1)
		b.LastAt = i64p(nowMs - 10*liveMs)
		upsert(b)
		if err := h.EndNode("b/main", nowMs-9*liveMs); err != nil {
			t.Fatalf("end b/main: %v", err)
		}
		// a job of the ended session reads inside that session's tree, never beside it
		job1 := nodeUpsert()
		job1.ID = "job:zai:1"
		job1.Kind = shared.NodeJob
		job1.ParentID = sp("b/main")
		job1.SessionID = "b"
		job1.AgentID = "job"
		job1.LastAt = i64p(nowMs - 11*liveMs)
		job1.JobState = sp("awaiting_review")
		upsert(job1)
		job2 := nodeUpsert()
		job2.ID = "job:zai:2"
		job2.Kind = shared.NodeJob
		job2.SessionID = "zai:2"
		job2.AgentID = "job"
		job2.LastAt = i64p(nowMs - 12*liveMs)
		job2.JobState = sp("done")
		upsert(job2)
		live, err := h.Roots(liveQ())
		if err != nil {
			t.Fatalf("read live roots: %v", err)
		}
		if want := []string{"a/main"}; !reflect.DeepEqual(rootIDs(live), want) {
			t.Errorf("live roots = %v, want %v", rootIDs(live), want)
		}
		ended, err := h.Roots(historyQ())
		if err != nil {
			t.Fatalf("read history roots: %v", err)
		}
		if want := []string{"b/main"}; !reflect.DeepEqual(rootIDs(ended), want) {
			t.Errorf("history roots = %v, want %v", rootIDs(ended), want)
		}
		tree, err := h.Tree("b/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if want := []string{"b/main", "job:zai:1"}; !reflect.DeepEqual(treeNodeIDs(tree), want) {
			t.Errorf("b/main tree = %v, want %v", treeNodeIDs(tree), want)
		}
	})

	t.Run("rolls the jobs no session claims into one Unattached group root", func(t *testing.T) {
		h := openHistoryAt(t)
		job1 := nodeUpsert()
		job1.ID = "job:zai:1"
		job1.Kind = shared.NodeJob
		job1.SessionID = "zai:1"
		job1.AgentID = "job"
		job1.LastAt = i64p(nowMs)
		if err := h.UpsertNode(job1); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r1 := requestRecord()
		r1.Ts = nowMs - 1000
		r1.Tokens = shared.Tokens{Input: 6}
		if err := h.PutRequest("job:zai:1", r1); err != nil {
			t.Fatalf("put request: %v", err)
		}
		// a job of a job hangs under its parent job: still one group
		job2 := nodeUpsert()
		job2.ID = "job:zai:2"
		job2.Kind = shared.NodeJob
		job2.ParentID = sp("job:zai:1")
		job2.SessionID = "zai:2"
		job2.AgentID = "job"
		job2.LastAt = i64p(nowMs - 500)
		if err := h.UpsertNode(job2); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		// a job whose origin session is gone from the store reads as unclaimed too
		job3 := nodeUpsert()
		job3.ID = "job:kimi:3"
		job3.Kind = shared.NodeJob
		job3.ParentID = sp("gone/main")
		job3.SessionID = "kimi:3"
		job3.AgentID = "job"
		job3.StartedAt = i64p(nowMs - 40*liveMs)
		job3.LastAt = i64p(nowMs - 2*liveMs)
		if err := h.UpsertNode(job3); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r2 := requestRecord()
		r2.ID = "r2"
		r2.SessionID = "kimi:3"
		r2.Ts = nowMs - 2*liveMs
		if err := h.PutRequest("job:kimi:3", r2); err != nil {
			t.Fatalf("put request: %v", err)
		}
		group, err := h.Unattached(UnattachedQuery{Now: nowMs, LiveMs: liveMs})
		if err != nil {
			t.Fatalf("read the group: %v", err)
		}
		if group == nil {
			t.Fatal("no unattached group")
		}
		if group.ID != shared.OutsideSession {
			t.Errorf("id = %q, want %q", group.ID, shared.OutsideSession)
		}
		if group.SessionID != shared.OutsideSession {
			t.Errorf("sessionId = %q, want %q", group.SessionID, shared.OutsideSession)
		}
		if group.Kind != shared.NodeJob {
			t.Errorf("kind = %q, want job", group.Kind)
		}
		if group.Name == nil || *group.Name != shared.UnattachedName {
			t.Errorf("name = %v, want %q", group.Name, shared.UnattachedName)
		}
		if group.ParentID != nil {
			t.Errorf("parentId = %v, want nil", group.ParentID)
		}
		if group.Repo != nil {
			t.Errorf("repo = %v, want nil", group.Repo)
		}
		if group.Nodes != 3 {
			t.Errorf("nodes = %d, want 3", group.Nodes)
		}
		// the zai pair moved within the live window, the kimi job did not
		if group.LiveNodes != 2 {
			t.Errorf("liveNodes = %d, want 2", group.LiveNodes)
		}
		if group.Requests != 2 {
			t.Errorf("requests = %d, want 2", group.Requests)
		}
		if group.Tokens.Input != 16 || group.Tokens.Output != 4 {
			t.Errorf("tokens = %+v, want input 16 output 4", group.Tokens)
		}
		if got := group.StartedAt; got == nil || *got != nowMs-40*liveMs {
			t.Errorf("startedAt = %v, want %d", got, nowMs-40*liveMs)
		}
		if group.LastAt != nowMs {
			t.Errorf("lastAt = %d, want %d", group.LastAt, nowMs)
		}
		var counted int64
		for _, count := range group.Activity.Counts {
			counted += count
		}
		if counted != 2 {
			t.Errorf("activity counts sum to %d, want 2", counted)
		}
		later, err := h.Unattached(UnattachedQuery{Now: nowMs + 10*liveMs, LiveMs: liveMs})
		if err != nil {
			t.Fatalf("read the group again: %v", err)
		}
		if later == nil || later.LiveNodes != 0 {
			t.Errorf("later liveNodes = %v, want 0", later)
		}
	})

	t.Run("answers null from unattached() when every job found its session", func(t *testing.T) {
		h := openHistoryAt(t)
		n := nodeUpsert()
		n.LastAt = i64p(nowMs)
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		job := nodeUpsert()
		job.ID = "job:zai:1"
		job.Kind = shared.NodeJob
		job.ParentID = sp("s1/main")
		job.AgentID = "job"
		job.LastAt = i64p(nowMs)
		if err := h.UpsertNode(job); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		group, err := h.Unattached(UnattachedQuery{Now: nowMs, LiveMs: liveMs})
		if err != nil {
			t.Fatalf("read the group: %v", err)
		}
		if group != nil {
			t.Errorf("group = %+v, want nil", group)
		}
	})

	t.Run("reads the Unattached group's tree and requests through the group's id", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		job1 := nodeUpsert()
		job1.ID = "job:zai:1"
		job1.Kind = shared.NodeJob
		job1.SessionID = "zai:1"
		job1.AgentID = "job"
		job1.LastAt = i64p(nowMs)
		upsert(job1)
		job2 := nodeUpsert()
		job2.ID = "job:zai:2"
		job2.Kind = shared.NodeJob
		job2.ParentID = sp("job:zai:1")
		job2.SessionID = "zai:2"
		job2.AgentID = "job"
		job2.LastAt = i64p(nowMs - liveMs - 1)
		upsert(job2)
		r1 := requestRecord()
		r1.SessionID = "zai:2"
		r1.Ts = nowMs - liveMs - 1
		if err := h.PutRequest("job:zai:2", r1); err != nil {
			t.Fatalf("put request: %v", err)
		}
		// a claimed job stays out of the group
		a := nodeUpsert()
		a.ID = "a/main"
		a.LastAt = i64p(nowMs - liveMs)
		upsert(a)
		job3 := nodeUpsert()
		job3.ID = "job:zai:3"
		job3.Kind = shared.NodeJob
		job3.ParentID = sp("a/main")
		job3.SessionID = "zai:3"
		job3.AgentID = "job"
		job3.LastAt = i64p(nowMs - liveMs)
		upsert(job3)
		// live first, then by recency: the parent job outranks its quiet child
		tree, err := h.Tree(shared.OutsideSession, TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if want := []string{"job:zai:1", "job:zai:2"}; !reflect.DeepEqual(treeNodeIDs(tree), want) {
			t.Errorf("group tree = %v, want %v", treeNodeIDs(tree), want)
		}
		requests, err := h.RequestsOf(RequestsQuery{RootID: sp(shared.OutsideSession), Limit: 10})
		if err != nil {
			t.Fatalf("read requests: %v", err)
		}
		if want := []string{"r1"}; !reflect.DeepEqual(requestIDs(requests), want) {
			t.Errorf("group requests = %v, want %v", requestIDs(requests), want)
		}
		rows, err := h.CostRows([]string{shared.OutsideSession})
		if err != nil {
			t.Fatalf("read cost rows: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("cost rows = %d, want 1", len(rows))
		}
		if rows[0].RootID != shared.OutsideSession {
			t.Errorf("cost row rootId = %q, want %q", rows[0].RootID, shared.OutsideSession)
		}
	})

	t.Run("rolls requests, tokens and node counts up over the whole tree", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		put := func(nodeID string, r shared.RequestRecord) {
			t.Helper()
			if err := h.PutRequest(nodeID, r); err != nil {
				t.Fatalf("put request %s: %v", r.ID, err)
			}
		}
		a := nodeUpsert()
		a.ID = "a/main"
		a.StartedAt = i64p(1)
		a.LastAt = i64p(nowMs)
		upsert(a)
		ag := nodeUpsert()
		ag.ID = "a/ag"
		ag.Kind = shared.NodeSubagent
		ag.ParentID = sp("a/main")
		ag.AgentID = "ag"
		ag.LastAt = i64p(nowMs - 1000)
		upsert(ag)
		job := nodeUpsert()
		job.ID = "job:zai:1"
		job.Kind = shared.NodeJob
		job.ParentID = sp("a/main")
		job.SessionID = "a"
		job.AgentID = "job"
		job.LastAt = i64p(nowMs - 2000)
		upsert(job)
		r1 := requestRecord()
		r1.ID = "r1"
		r1.Ts = nowMs - 3000
		r1.Tokens = shared.Tokens{Input: 10, Output: 4}
		put("a/main", r1)
		r2 := requestRecord()
		r2.ID = "r2"
		r2.Ts = nowMs - 2000
		r2.Tokens = shared.Tokens{Input: 7, Output: 2}
		put("a/ag", r2)
		r3 := requestRecord()
		r3.ID = "r3"
		r3.Ts = nowMs - 1000
		r3.Tokens = shared.Tokens{Input: 1, Output: 1}
		put("job:zai:1", r3)
		roots, err := h.Roots(liveQ())
		if err != nil {
			t.Fatalf("read roots: %v", err)
		}
		if len(roots) != 1 {
			t.Fatalf("roots = %d, want 1", len(roots))
		}
		row := roots[0]
		if row.ID != "a/main" {
			t.Errorf("id = %q, want a/main", row.ID)
		}
		if row.Requests != 3 {
			t.Errorf("requests = %d, want 3", row.Requests)
		}
		if row.Nodes != 3 {
			t.Errorf("nodes = %d, want 3", row.Nodes)
		}
		if row.LiveNodes != 3 {
			t.Errorf("liveNodes = %d, want 3", row.LiveNodes)
		}
		if row.LastAt != nowMs {
			t.Errorf("lastAt = %d, want %d", row.LastAt, nowMs)
		}
		if row.Tokens.Input != 18 || row.Tokens.Output != 7 || row.Tokens.CacheRead != 0 || row.Tokens.CacheWrite != 0 {
			t.Errorf("tokens = %+v, want input 18 output 7", row.Tokens)
		}
	})

	t.Run("pages roots by last_at with before", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(id, session string, lastAt int64) {
			t.Helper()
			n := nodeUpsert()
			n.ID = id
			n.SessionID = session
			n.LastAt = i64p(lastAt)
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", id, err)
			}
		}
		upsert("c/main", "c", 300)
		upsert("d/main", "d", 200)
		upsert("e/main", "e", 100)
		page := func(before *int64) []shared.RootSummary {
			t.Helper()
			roots, err := h.Roots(RootsQuery{Scope: ScopeHistory, Now: nowMs, LiveMs: liveMs, Limit: 2, Before: before})
			if err != nil {
				t.Fatalf("read the page: %v", err)
			}
			return roots
		}
		first := page(nil)
		if want := []string{"c/main", "d/main"}; !reflect.DeepEqual(rootIDs(first), want) {
			t.Errorf("first page = %v, want %v", rootIDs(first), want)
		}
		if len(first) < 2 {
			t.Fatalf("first page holds %d roots, want 2", len(first))
		}
		before := first[1].LastAt
		second := page(&before)
		if want := []string{"e/main"}; !reflect.DeepEqual(rootIDs(second), want) {
			t.Errorf("second page = %v, want %v", rootIDs(second), want)
		}
	})

	t.Run("rolls a page of roots up with three queries per chunk, never three per root", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		writer, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open the writer: %v", err)
		}
		for at := int64(1); at <= 6; at++ {
			n := nodeUpsert()
			n.ID = fmt.Sprintf("t%d/main", at)
			n.SessionID = fmt.Sprintf("t%d", at)
			n.LastAt = i64p(600 - at)
			if err := writer.UpsertNode(n); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			r := requestRecord()
			r.ID = fmt.Sprintf("r%d", at)
			r.SessionID = n.SessionID
			r.Ts = 600 - at
			if err := writer.PutRequest(n.ID, r); err != nil {
				t.Fatalf("put request: %v", err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close the writer: %v", err)
		}
		under, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the database: %v", err)
		}
		counting := &countingDB{DB: under}
		h, err := OpenHistory(file, counting)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		alls := len(counting.alls)
		roots, err := h.Roots(RootsQuery{Scope: ScopeHistory, Now: nowMs, LiveMs: liveMs, Limit: 6})
		if err != nil {
			t.Fatalf("read roots: %v", err)
		}
		if len(roots) != 6 {
			t.Fatalf("roots = %d, want 6", len(roots))
		}
		first := roots[0]
		if first.ID != "t1/main" || first.Requests != 1 || first.Nodes != 1 || first.Tokens.Input != 10 {
			t.Errorf("first root = %+v, want t1/main with one request of 10 input tokens", first)
		}
		// the page plus the three roll-up shapes: a per-root read would run 1 + 3×6
		if got := len(counting.alls) - alls; got != 4 {
			t.Errorf("the page read ran %d queries, want 4", got)
		}
	})

	t.Run("keeps the roll-ups exact across a 400-root chunk boundary", func(t *testing.T) {
		h := openHistoryAt(t)
		for at := int64(1); at <= 401; at++ {
			n := nodeUpsert()
			n.ID = fmt.Sprintf("t%d/main", at)
			n.SessionID = fmt.Sprintf("t%d", at)
			n.LastAt = i64p(600 - at)
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			r := requestRecord()
			r.ID = fmt.Sprintf("r%d", at)
			r.SessionID = n.SessionID
			r.Ts = 600 - at
			if err := h.PutRequest(n.ID, r); err != nil {
				t.Fatalf("put request: %v", err)
			}
		}
		roots, err := h.Roots(RootsQuery{Scope: ScopeHistory, Now: nowMs, LiveMs: liveMs, Limit: 401})
		if err != nil {
			t.Fatalf("read roots: %v", err)
		}
		if len(roots) != 401 {
			t.Fatalf("roots = %d, want 401", len(roots))
		}
		var first, last *shared.RootSummary
		for at := range roots {
			switch roots[at].ID {
			case "t1/main":
				first = &roots[at]
			case "t401/main":
				last = &roots[at]
			}
		}
		if first == nil || last == nil {
			t.Fatal("the page lost a root at the chunk boundary")
		}
		for name, root := range map[string]*shared.RootSummary{"first": first, "last": last} {
			if root.Requests != 1 || root.Nodes != 1 || root.Tokens.Input != 10 {
				t.Errorf("%s root = %+v, want one request of 10 input tokens", name, root)
			}
			var busy bool
			for _, count := range root.Activity.Counts {
				if count > 0 {
					busy = true
				}
			}
			if !busy {
				t.Errorf("%s root has an empty activity", name)
			}
		}
	})

	t.Run("searches project, label, model and cwd case-insensitively", func(t *testing.T) {
		h := openHistoryAt(t)
		a := nodeUpsert()
		a.ID = "a/main"
		a.LastAt = i64p(nowMs)
		a.Project = sp("app")
		a.Cwd = sp("/work/app")
		a.Label = sp("Fix the bug")
		if err := h.UpsertNode(a); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		b := nodeUpsert()
		b.ID = "b/main"
		b.SessionID = "b"
		b.LastAt = i64p(nowMs)
		b.Cwd = sp("/other/site")
		if err := h.UpsertNode(b); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		search := func(term string) []string {
			t.Helper()
			q := liveQ()
			q.Search = sp(term)
			roots, err := h.Roots(q)
			if err != nil {
				t.Fatalf("read roots: %v", err)
			}
			return rootIDs(roots)
		}
		if got := search("APP"); !reflect.DeepEqual(got, []string{"a/main"}) {
			t.Errorf("search APP = %v, want [a/main]", got)
		}
		if got := search("the bug"); !reflect.DeepEqual(got, []string{"a/main"}) {
			t.Errorf("search 'the bug' = %v, want [a/main]", got)
		}
		if got := search("site"); !reflect.DeepEqual(got, []string{"b/main"}) {
			t.Errorf("search site = %v, want [b/main]", got)
		}
		if got := search("%zzz"); len(got) != 0 {
			t.Errorf("search %%zzz = %v, want none", got)
		}
	})

	t.Run("groups each tree's requests into 48 activity buckets, live window or whole span", func(t *testing.T) {
		h := openHistoryAt(t)
		// an ended tree: its 80-minute run stretches start-to-last over 48 buckets of 100,000 ms
		a := nodeUpsert()
		a.ID = "a/main"
		a.StartedAt = i64p(nowMs - 4_800_000)
		a.LastAt = i64p(nowMs)
		if err := h.UpsertNode(a); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r1 := requestRecord()
		r1.ID = "r1"
		r1.Ts = nowMs - 4_750_000
		r1.Model = "glm-5.3"
		if err := h.PutRequest("a/main", r1); err != nil {
			t.Fatalf("put request: %v", err)
		}
		r2 := requestRecord()
		r2.ID = "r2"
		r2.Ts = nowMs - 30_000
		r2.Model = "kimi-k2.5"
		if err := h.PutRequest("a/main", r2); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.EndNode("a/main", nowMs); err != nil {
			t.Fatalf("end: %v", err)
		}
		// a live tree: its window is the last liveMs ending now, 60,000 ms here in buckets of 1,250
		b := nodeUpsert()
		b.ID = "b/main"
		b.SessionID = "b"
		b.StartedAt = i64p(nowMs - 4_800_000)
		b.LastAt = i64p(nowMs)
		if err := h.UpsertNode(b); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r3 := requestRecord()
		r3.ID = "r3"
		r3.SessionID = "b"
		r3.Ts = nowMs - 30_000
		if err := h.PutRequest("b/main", r3); err != nil {
			t.Fatalf("put request: %v", err)
		}
		find := func(q RootsQuery, id string) *shared.RootSummary {
			t.Helper()
			roots, err := h.Roots(q)
			if err != nil {
				t.Fatalf("read roots: %v", err)
			}
			for at := range roots {
				if roots[at].ID == id {
					return &roots[at]
				}
			}
			return nil
		}
		ended := find(historyQ(), "a/main")
		if ended == nil {
			t.Fatal("the ended root is missing")
		}
		if ended.Activity.BucketMs != 100_000 {
			t.Errorf("ended bucketMs = %d, want 100000", ended.Activity.BucketMs)
		}
		if ended.Activity.Counts[0] != 1 {
			t.Errorf("ended counts[0] = %d, want 1", ended.Activity.Counts[0])
		}
		if ended.Activity.Counts[47] != 1 {
			t.Errorf("ended counts[47] = %d, want 1", ended.Activity.Counts[47])
		}
		if ended.Activity.Models[0] != "glm-5.3" {
			t.Errorf("ended models[0] = %q, want glm-5.3", ended.Activity.Models[0])
		}
		if ended.Activity.Models[47] != "kimi-k2.5" {
			t.Errorf("ended models[47] = %q, want kimi-k2.5", ended.Activity.Models[47])
		}
		live := find(liveQ(), "b/main")
		if live == nil {
			t.Fatal("the live root is missing")
		}
		if live.Activity.BucketMs != 1_250 {
			t.Errorf("live bucketMs = %d, want 1250", live.Activity.BucketMs)
		}
		if live.Activity.Counts[24] != 1 {
			t.Errorf("live counts[24] = %d, want 1", live.Activity.Counts[24])
		}
		if live.Activity.Models[24] != "glm-5.3-flash" {
			t.Errorf("live models[24] = %q, want glm-5.3-flash", live.Activity.Models[24])
		}
		// a tree without requests is 48 silent buckets
		c := nodeUpsert()
		c.ID = "c/main"
		c.SessionID = "c"
		c.StartedAt = i64p(nowMs - 1000)
		c.LastAt = i64p(nowMs)
		if err := h.UpsertNode(c); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		silent := find(liveQ(), "c/main")
		if silent == nil {
			t.Fatal("the silent root is missing")
		}
		for at, count := range silent.Activity.Counts {
			if count != 0 {
				t.Errorf("silent counts[%d] = %d, want 0", at, count)
			}
		}
		for at, model := range silent.Activity.Models {
			if model != "" {
				t.Errorf("silent models[%d] = %q, want empty", at, model)
			}
		}
	})
}

func TestTree(t *testing.T) {
	t.Run("orders the root first, then live before ended, then newest last_at", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		a := nodeUpsert()
		a.ID = "a/main"
		a.LastAt = i64p(nowMs - 100)
		upsert(a)
		ended := nodeUpsert()
		ended.ID = "a/ended"
		ended.Kind = shared.NodeSubagent
		ended.ParentID = sp("a/main")
		ended.AgentID = "ended"
		ended.LastAt = i64p(nowMs - 200)
		upsert(ended)
		live := nodeUpsert()
		live.ID = "a/live"
		live.Kind = shared.NodeSubagent
		live.ParentID = sp("a/main")
		live.AgentID = "live"
		live.LastAt = i64p(nowMs)
		upsert(live)
		if err := h.EndNode("a/ended", nowMs-199); err != nil {
			t.Fatalf("end: %v", err)
		}
		r1 := requestRecord()
		r1.Ts = nowMs
		if err := h.PutRequest("a/live", r1); err != nil {
			t.Fatalf("put request: %v", err)
		}
		tree, err := h.Tree("a/main", TreeQuery{Now: i64p(nowMs), LiveMs: i64p(liveMs)})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if want := []string{"a/main", "a/live", "a/ended"}; !reflect.DeepEqual(treeNodeIDs(tree), want) {
			t.Errorf("tree = %v, want %v", treeNodeIDs(tree), want)
		}
		if len(tree) != 3 {
			t.Fatal("the tree does not hold three rows")
		}
		wantLive := []bool{true, true, false}
		for at, row := range tree {
			if row.Live != wantLive[at] {
				t.Errorf("tree[%d].live = %v, want %v", at, row.Live, wantLive[at])
			}
		}
		if tree[1].Requests != 1 {
			t.Errorf("a/live requests = %d, want 1", tree[1].Requests)
		}
		if tree[2].Requests != 0 {
			t.Errorf("a/ended requests = %d, want 0", tree[2].Requests)
		}
		missing, err := h.Tree("missing", TreeQuery{})
		if err != nil {
			t.Fatalf("read a missing tree: %v", err)
		}
		if len(missing) != 0 {
			t.Errorf("missing tree = %v, want empty", treeNodeIDs(missing))
		}
	})
}

func TestRequestsOf(t *testing.T) {
	t.Run("reads by node and by tree with keyset paging and no duplicates across pages", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		a := nodeUpsert()
		a.ID = "a/main"
		upsert(a)
		ag := nodeUpsert()
		ag.ID = "a/ag"
		ag.Kind = shared.NodeSubagent
		ag.ParentID = sp("a/main")
		ag.AgentID = "ag"
		upsert(ag)
		put := func(nodeID, id string, ts int64) {
			t.Helper()
			r := requestRecord()
			r.ID = id
			r.Ts = ts
			if nodeID == "a/ag" {
				r.AgentID = "ag"
			}
			if err := h.PutRequest(nodeID, r); err != nil {
				t.Fatalf("put request %s: %v", id, err)
			}
		}
		put("a/main", "r1", 100)
		put("a/main", "r3", 100)
		put("a/main", "r2", 200)
		put("a/ag", "r4", 300)
		byNode, err := h.RequestsOf(RequestsQuery{NodeID: sp("a/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read by node: %v", err)
		}
		if want := []string{"r2", "r3", "r1"}; !reflect.DeepEqual(requestIDs(byNode), want) {
			t.Errorf("by node = %v, want %v", requestIDs(byNode), want)
		}
		page := func(before *RequestCursor) []shared.RequestRecord {
			t.Helper()
			rows, err := h.RequestsOf(RequestsQuery{RootID: sp("a/main"), Limit: 2, Before: before})
			if err != nil {
				t.Fatalf("read the page: %v", err)
			}
			return rows
		}
		first := page(nil)
		if want := []string{"r4", "r2"}; !reflect.DeepEqual(requestIDs(first), want) {
			t.Errorf("first page = %v, want %v", requestIDs(first), want)
		}
		second := page(&RequestCursor{Ts: 200, ID: "r2"})
		if want := []string{"r3", "r1"}; !reflect.DeepEqual(requestIDs(second), want) {
			t.Errorf("second page = %v, want %v", requestIDs(second), want)
		}
		combined := append(append([]shared.RequestRecord{}, first...), second...)
		if want := []string{"r4", "r2", "r3", "r1"}; !reflect.DeepEqual(requestIDs(combined), want) {
			t.Errorf("the pages = %v, want %v with no duplicates", requestIDs(combined), want)
		}
		everything, err := h.RequestsOf(RequestsQuery{Limit: 10})
		if err != nil {
			t.Fatalf("read everything: %v", err)
		}
		if want := []string{"r4", "r2", "r3", "r1"}; !reflect.DeepEqual(requestIDs(everything), want) {
			t.Errorf("unfiltered = %v, want %v", requestIDs(everything), want)
		}
	})

	t.Run("reads a sparse row with null columns as a well-formed record", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the second connection: %v", err)
		}
		if _, err := db.Run("INSERT INTO requests (id, node_id, ts, totals) VALUES ('raw', 's1/main', 5, 0)"); err != nil {
			t.Fatalf("insert the raw row: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read requests: %v", err)
		}
		want := []shared.RequestRecord{{
			ID: "raw", SessionID: "", AgentID: "", Model: "", Upstream: "",
			Ts: 5, Tokens: shared.Tokens{}, Provider: "",
		}}
		if !reflect.DeepEqual(rows, want) {
			t.Errorf("rows = %+v, want %+v", rows, want)
		}
	})

	t.Run("round-trips every request field, what, thinking, route, via and totals", func(t *testing.T) {
		h := openHistoryAt(t)
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		full := requestRecord()
		full.ID = "full"
		full.Route = sp("provider")
		full.Via = sp("zai")
		full.Totals = true
		full.StopReason = sp("end_turn")
		latency := int64(42)
		full.LatencyMs = &latency
		thinking := int64(1)
		full.Tokens = shared.Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Thinking: &thinking}
		what := "↳ prompt: fix the limiter  → Edit store.ts, Bash git status"
		full.What = &what
		if err := h.PutRequest("s1/main", full); err != nil {
			t.Fatalf("put the full request: %v", err)
		}
		plain := requestRecord()
		plain.ID = "plain"
		if err := h.PutRequest("s1/main", plain); err != nil {
			t.Fatalf("put the plain request: %v", err)
		}
		rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read requests: %v", err)
		}
		want := []shared.RequestRecord{plain, full}
		if !reflect.DeepEqual(rows, want) {
			t.Errorf("rows = %+v, want %+v", rows, want)
		}
	})
}

// turns is how many turns the conversation fixture holds.
const turns = 120

// conversationFile writes a node whose every request carries a JSON input and output side,
// ts ascending from 1000, and answers its file.
func conversationFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "history.db")
	h, err := OpenHistory(file, nil)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	if err := h.UpsertNode(nodeUpsert()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for at := 1; at <= turns; at++ {
		r := requestRecord()
		r.ID = fmt.Sprintf("r%d", at)
		r.Ts = int64(1000 + at)
		if err := h.PutRequest("s1/main", r); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.PutContent(r.ID, ContentSides{
			Input:  sp(fmt.Sprintf(`[{"type":"text","text":"in %d"}]`, at)),
			Output: sp(fmt.Sprintf(`[{"type":"text","text":"out %d"}]`, at)),
		}); err != nil {
			t.Fatalf("put content: %v", err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return file
}

// slimTurn is one turn as the paging checks compare it.
type slimTurn struct {
	role      string
	requestID string
	text      string
}

// blockText reads a block's text field.
func blockText(t *testing.T, block shared.JSON) string {
	t.Helper()
	if block.Value == nil {
		t.Fatal("a block holds no JSON value")
	}
	for at, key := range block.Value.Keys() {
		if key != "text" {
			continue
		}
		fields := block.Value.Fields()
		if at >= len(fields) {
			t.Fatal("a block's fields do not pair with its keys")
		}
		return fields[at].Text()
	}
	t.Fatal("a block carries no text")
	return ""
}

// expectedTurns lists the conversation as the reader sees it: one turn per stored side, the
// target's answer never there.
func expectedTurns() []slimTurn {
	out := make([]slimTurn, 0, 2*turns-1)
	for at := 1; at <= turns; at++ {
		out = append(out, slimTurn{role: "user", requestID: fmt.Sprintf("r%d", at), text: fmt.Sprintf("in %d", at)})
		if at < turns {
			out = append(out, slimTurn{role: "assistant", requestID: fmt.Sprintf("r%d", at), text: fmt.Sprintf("out %d", at)})
		}
	}
	return out
}

// slimTurnsOf pares messages down to what the paging checks compare.
func slimTurnsOf(t *testing.T, messages []shared.ContextMessage) []slimTurn {
	t.Helper()
	out := make([]slimTurn, 0, len(messages))
	for _, message := range messages {
		if len(message.Blocks) == 0 {
			t.Fatalf("message %s holds no blocks", message.RequestID)
		}
		out = append(out, slimTurn{role: message.Role, requestID: message.RequestID,
			text: blockText(t, message.Blocks[0])})
	}
	return out
}

func TestContext(t *testing.T) {
	t.Run("serves one page from a bounded read, never the whole conversation", func(t *testing.T) {
		file := conversationFile(t)
		under, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the database: %v", err)
		}
		counting := &countingDB{DB: under}
		h, err := OpenHistory(file, counting)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		alls := len(counting.alls)
		gets := len(counting.gets)
		page, err := h.Context(fmt.Sprintf("r%d", turns), ContextQuery{Limit: 40})
		if err != nil {
			t.Fatalf("read the page: %v", err)
		}
		read := counting.alls[alls:]
		// one page query — an unbounded read would select every row of the node
		if len(read) != 1 {
			t.Errorf("the page read ran %d queries, want 1", len(read))
		}
		// limit + 2 rows, never the whole conversation
		if len(read) > 0 && read[0].rows > 42 {
			t.Errorf("the page read fetched %d rows, want at most 42", read[0].rows)
		}
		// the target and the sides' one aggregate
		if got := len(counting.gets) - gets; got != 2 {
			t.Errorf("the page read ran %d get queries, want 2", got)
		}
		if page == nil {
			t.Fatal("no page")
		}
		if len(page.Messages) != 40 {
			t.Errorf("the page holds %d messages, want 40", len(page.Messages))
		}
		if page.Totals.Messages != 2*turns-1 {
			t.Errorf("totals hold %d messages, want %d", page.Totals.Messages, 2*turns-1)
		}
		// the sides weigh what is stored — the JSON of each block list — minus the target's own answer
		var bytes int64
		for at := 1; at <= turns; at++ {
			bytes += int64(len(fmt.Sprintf(`[{"type":"text","text":"in %d"}]`, at)))
			bytes += int64(len(fmt.Sprintf(`[{"type":"text","text":"out %d"}]`, at)))
		}
		answer := int64(len(fmt.Sprintf(`[{"type":"text","text":"out %d"}]`, turns)))
		if got, want := page.Totals.ApproxTokens, (bytes-answer+2)/4; got != want {
			t.Errorf("approxTokens = %d, want %d", got, want)
		}
	})

	t.Run("pages the whole conversation through next, oldest page first, in the stored order", func(t *testing.T) {
		h, err := OpenHistory(conversationFile(t), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		whole, err := h.Context(fmt.Sprintf("r%d", turns), ContextQuery{Limit: 1000})
		if err != nil {
			t.Fatalf("read the whole conversation: %v", err)
		}
		if whole == nil {
			t.Fatal("no conversation came back")
		}
		// pagedWhole walks every page, prepending each to the front
		var paged []shared.ContextMessage
		var cursor *string
		for page := 0; page < 40; page++ {
			answer, err := h.Context(fmt.Sprintf("r%d", turns), ContextQuery{Limit: 7, Cursor: cursor})
			if err != nil {
				t.Fatalf("read a page: %v", err)
			}
			if answer == nil || len(answer.Messages) == 0 {
				break
			}
			paged = append(answer.Messages, paged...)
			cursor = answer.Next
			if cursor == nil {
				break
			}
		}
		if want := expectedTurns(); !reflect.DeepEqual(slimTurnsOf(t, whole.Messages), want) {
			t.Errorf("the whole conversation = %v, want %v", slimTurnsOf(t, whole.Messages), want)
		}
		if want := expectedTurns(); !reflect.DeepEqual(slimTurnsOf(t, paged), want) {
			t.Errorf("the paged conversation = %v, want %v", slimTurnsOf(t, paged), want)
		}
	})

	t.Run("answers an empty page to a cursor the node never named", func(t *testing.T) {
		h, err := OpenHistory(conversationFile(t), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		page, err := h.Context(fmt.Sprintf("r%d", turns), ContextQuery{Limit: 40, Cursor: sp("1:r999:0")})
		if err != nil {
			t.Fatalf("read the page: %v", err)
		}
		if page == nil {
			t.Fatal("no page")
		}
		if len(page.Messages) != 0 {
			t.Errorf("the page holds %v, want none", page.Messages)
		}
		if page.Next != nil {
			t.Errorf("next = %q, want nil", *page.Next)
		}
	})
}

func TestPruneClearStats(t *testing.T) {
	t.Run("removes only trees where every node is old, rows included, and returns the count", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		upsert := func(n NodeUpsert) {
			t.Helper()
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", n.ID, err)
			}
		}
		o := nodeUpsert()
		o.ID = "o/main"
		o.SessionID = "o"
		o.LastAt = i64p(10)
		upsert(o)
		oag := nodeUpsert()
		oag.ID = "o/ag"
		oag.Kind = shared.NodeSubagent
		oag.ParentID = sp("o/main")
		oag.AgentID = "ag"
		oag.LastAt = i64p(20)
		upsert(oag)
		old := requestRecord()
		old.ID = "old"
		old.Ts = 10
		if err := h.PutRequest("o/main", old); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.PutContent("old", ContentSides{Input: sp("in"), Output: sp("out")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		tool := shared.ToolCallRecord{ID: "t1", SessionID: "o", AgentID: sp("main"), Name: "Bash",
			StartedAt: 10, DurationMs: i64p(1), Ok: true}
		if err := h.PutTool("o/main", tool); err != nil {
			t.Fatalf("put tool: %v", err)
		}
		event := shared.EventRecord{Seq: 1, Ts: 10, Kind: "Notice", SessionID: sp("o")}
		if err := h.AddEvent(sp("o/main"), event); err != nil {
			t.Fatalf("add event: %v", err)
		}
		y := nodeUpsert()
		y.ID = "y/main"
		y.SessionID = "y"
		y.LastAt = i64p(5000)
		upsert(y)
		recent := requestRecord()
		recent.ID = "new"
		recent.Ts = 5000
		if err := h.PutRequest("y/main", recent); err != nil {
			t.Fatalf("put request: %v", err)
		}
		// a tree whose subagent has no last_at is never stale
		n := nodeUpsert()
		n.ID = "n/main"
		n.SessionID = "n"
		n.LastAt = i64p(5)
		upsert(n)
		nag := nodeUpsert()
		nag.ID = "n/ag"
		nag.Kind = shared.NodeSubagent
		nag.ParentID = sp("n/main")
		nag.AgentID = "ag"
		upsert(nag)
		pruned, err := h.Prune(1000)
		if err != nil {
			t.Fatalf("prune: %v", err)
		}
		if pruned != 1 {
			t.Errorf("prune removed %d trees, want 1", pruned)
		}
		pruned, err = h.Prune(1000)
		if err != nil {
			t.Fatalf("prune again: %v", err)
		}
		if pruned != 0 {
			t.Errorf("second prune removed %d trees, want 0", pruned)
		}
		stats, err := h.Stats(StatsQuery{Now: 1_700_000_000_000, LiveMs: 15 * 60_000})
		if err != nil {
			t.Fatalf("read stats: %v", err)
		}
		if stats.Nodes != 3 || stats.Requests != 1 {
			t.Errorf("stats = %+v, want 3 nodes and 1 request", stats)
		}
		dropped, err := h.Content("old")
		if err != nil {
			t.Fatalf("read the pruned content: %v", err)
		}
		if dropped != nil {
			t.Errorf("pruned content = %v, want nil", dropped)
		}
		rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("y/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read requests: %v", err)
		}
		if want := []string{"new"}; !reflect.DeepEqual(requestIDs(rows), want) {
			t.Errorf("requests = %v, want %v", requestIDs(rows), want)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		for _, table := range []string{"tools", "events"} {
			row, err := db.Get(fmt.Sprintf("SELECT COUNT(*) AS c FROM %s", table))
			if err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if got := row["c"]; got != int64(0) {
				t.Errorf("%s holds %v rows, want 0", table, got)
			}
		}
		prunedNodes, err := db.Get("SELECT COUNT(*) AS c FROM nodes WHERE root_id = 'o/main'")
		if err != nil {
			t.Fatalf("count the pruned tree: %v", err)
		}
		if got := prunedNodes["c"]; got != int64(0) {
			t.Errorf("the pruned tree holds %v nodes, want 0", got)
		}
	})

	t.Run("empties every table but meta on clear", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		n := nodeUpsert()
		n.LastAt = i64p(nowMs)
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.PutRequest("s1/main", requestRecord()); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.PutContent("r1", ContentSides{Input: sp("in")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		if err := h.SetOffset("f", FileOffset{Offset: 1, Size: 1, Mtime: 1}); err != nil {
			t.Fatalf("set offset: %v", err)
		}
		if err := h.Clear(); err != nil {
			t.Fatalf("clear: %v", err)
		}
		stats, err := h.Stats(StatsQuery{Now: 1_700_000_000_000, LiveMs: 15 * 60_000})
		if err != nil {
			t.Fatalf("read stats: %v", err)
		}
		if stats.Nodes != 0 || stats.Requests != 0 {
			t.Errorf("stats = %+v, want nothing", stats)
		}
		offset, err := h.Offset("f")
		if err != nil {
			t.Fatalf("read offset: %v", err)
		}
		if offset != nil {
			t.Errorf("offset = %v, want nil", offset)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if got, want := versionOf(t, db), strconv.Itoa(SchemaVersion); got != want {
			t.Errorf("version = %q, want %q", got, want)
		}
		content, err := db.Get("SELECT COUNT(*) AS c FROM content")
		if err != nil {
			t.Fatalf("count content: %v", err)
		}
		if got := content["c"]; got != int64(0) {
			t.Errorf("content holds %v rows, want 0", got)
		}
	})

	t.Run("counts file bytes, nodes and requests", func(t *testing.T) {
		h := openHistoryAt(t)
		stats, err := h.Stats(StatsQuery{Now: 1_700_000_000_000, LiveMs: 15 * 60_000})
		if err != nil {
			t.Fatalf("read stats: %v", err)
		}
		if stats.Nodes != 0 || stats.Requests != 0 {
			t.Errorf("stats = %+v, want nothing", stats)
		}
		if stats.Bytes <= 0 {
			t.Errorf("bytes = %d, want more than 0", stats.Bytes)
		}
		n := nodeUpsert()
		n.LastAt = i64p(nowMs)
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.PutRequest("s1/main", requestRecord()); err != nil {
			t.Fatalf("put request: %v", err)
		}
		stats, err = h.Stats(StatsQuery{Now: 1_700_000_000_000, LiveMs: 15 * 60_000})
		if err != nil {
			t.Fatalf("read stats: %v", err)
		}
		if stats.Nodes != 1 || stats.Requests != 1 {
			t.Errorf("stats = %+v, want 1 node and 1 request", stats)
		}
	})
}

func TestTransactions(t *testing.T) {
	t.Run("commits, rolls back on throw, and runs nested calls without a second BEGIN", func(t *testing.T) {
		h := openHistoryAt(t)
		kept := nodeUpsert()
		kept.ID = "kept"
		if err := h.Transaction(func() error {
			return h.UpsertNode(kept)
		}); err != nil {
			t.Fatalf("commit: %v", err)
		}
		dropped := nodeUpsert()
		dropped.ID = "dropped"
		boom := fmt.Errorf("boom")
		if err := h.Transaction(func() error {
			if err := h.UpsertNode(dropped); err != nil {
				return err
			}
			return boom
		}); err != boom {
			t.Fatalf("rollback error = %v, want boom", err)
		}
		nested := nodeUpsert()
		nested.ID = "nested"
		if err := h.Transaction(func() error {
			return h.Transaction(func() error {
				return h.UpsertNode(nested)
			})
		}); err != nil {
			t.Fatalf("nested: %v", err)
		}
		roots, err := h.Roots(historyQ())
		if err != nil {
			t.Fatalf("read roots: %v", err)
		}
		ids := rootIDs(roots)
		for _, want := range []string{"kept", "nested"} {
			found := false
			for _, id := range ids {
				if id == want {
					found = true
				}
			}
			if !found {
				t.Errorf("roots = %v, want %s among them", ids, want)
			}
		}
		for _, id := range ids {
			if id == "dropped" {
				t.Errorf("roots = %v, the rolled-back node survived", ids)
			}
		}
	})
}

func TestEndUpsert(t *testing.T) {
	t.Run("stamps the later end, clears on demand, and keeps without one", func(t *testing.T) {
		h := openHistoryAt(t)
		stamp := func(end End) *int64 {
			t.Helper()
			n := nodeUpsert()
			n.EndedAt = end
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			tree, err := h.Tree("s1/main", TreeQuery{})
			if err != nil {
				t.Fatalf("read the tree: %v", err)
			}
			return firstTreeRow(t, tree).EndedAt
		}
		// a fresh node stores no end: the stamp lands on the second upsert, the way the
		// writer always sends one
		if got := stamp(NoEnd()); got != nil {
			t.Errorf("endedAt = %v, want nil on a fresh node", got)
		}
		if got := stamp(EndAt(800)); got == nil || *got != 800 {
			t.Errorf("endedAt = %v, want 800", got)
		}
		// an earlier stamp does not move it back
		if got := stamp(EndAt(500)); got == nil || *got != 800 {
			t.Errorf("endedAt = %v, want 800", got)
		}
		if got := stamp(NoEnd()); got == nil || *got != 800 {
			t.Errorf("endedAt = %v, want 800", got)
		}
		if got := stamp(ClearEnd()); got != nil {
			t.Errorf("endedAt = %v, want nil", got)
		}
		// a stamp after a clear sets it again
		if got := stamp(EndAt(900)); got == nil || *got != 900 {
			t.Errorf("endedAt = %v, want 900", got)
		}
	})
}

func TestServedName(t *testing.T) {
	t.Run("shows a path-shaped name as the repo's own name", func(t *testing.T) {
		h := openHistoryAt(t)
		n := nodeUpsert()
		n.Name = sp("/work/claude-code-plugins")
		n.LastAt = i64p(nowMs)
		if err := h.UpsertNode(n); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		tree, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if got := firstTreeRow(t, tree).Name; got == nil || *got != "claude-code-plugins" {
			t.Errorf("name = %v, want claude-code-plugins", got)
		}
	})
}

func TestFlow(t *testing.T) {
	t.Run("counts requests and token kinds per bucket, clamped into the last bar", func(t *testing.T) {
		h := openHistoryAt(t)
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		put := func(id string, ts int64, tokens shared.Tokens) {
			t.Helper()
			r := requestRecord()
			r.ID = id
			r.Ts = ts
			r.Tokens = tokens
			if err := h.PutRequest("s1/main", r); err != nil {
				t.Fatalf("put request %s: %v", id, err)
			}
		}
		put("a", 50, shared.Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4})
		put("b", 150, shared.Tokens{Input: 10})
		put("c", 950, shared.Tokens{Output: 7})
		// outside the window on either side never counts
		put("before", -10, shared.Tokens{Input: 100})
		put("after", 1200, shared.Tokens{Input: 100})
		series, err := h.Flow(FlowQuery{From: 0, To: 1000, BucketMs: 100, Buckets: 10})
		if err != nil {
			t.Fatalf("read the flow: %v", err)
		}
		if series.From != 0 || series.To != 1000 || series.BucketMs != 100 {
			t.Errorf("window = %+v, want the query's own", series)
		}
		wantRequests := []int64{1, 1, 0, 0, 0, 0, 0, 0, 0, 1}
		if !reflect.DeepEqual(series.Requests, wantRequests) {
			t.Errorf("requests = %v, want %v", series.Requests, wantRequests)
		}
		wantInput := []int64{1, 10, 0, 0, 0, 0, 0, 0, 0, 0}
		if !reflect.DeepEqual(series.Kinds.Input, wantInput) {
			t.Errorf("input = %v, want %v", series.Kinds.Input, wantInput)
		}
		wantOutput := []int64{2, 0, 0, 0, 0, 0, 0, 0, 0, 7}
		if !reflect.DeepEqual(series.Kinds.Output, wantOutput) {
			t.Errorf("output = %v, want %v", series.Kinds.Output, wantOutput)
		}
		if series.Kinds.CacheRead[0] != 3 || series.Kinds.CacheWrite[0] != 4 {
			t.Errorf("cache kinds = %v %v, want 3 and 4 in the first bar",
				series.Kinds.CacheRead[0], series.Kinds.CacheWrite[0])
		}
		// a request past the capped bucket count lands in the last bar, not off the chart
		put("far", 2600, shared.Tokens{Input: 5})
		capped, err := h.Flow(FlowQuery{From: 0, To: 3000, BucketMs: 1000, Buckets: 2})
		if err != nil {
			t.Fatalf("read the capped flow: %v", err)
		}
		if got := capped.Requests[0]; got != 3 { // 50, 150 and 950 all sit in the first bar
			t.Errorf("capped first bar = %d, want 3", got)
		}
		if got := capped.Requests[1]; got != 2 { // 1200 in its own bar, 2600 clamped into it
			t.Errorf("capped last bar = %d, want 2", got)
		}
		empty, err := h.Flow(FlowQuery{From: 5000, To: 6000, BucketMs: 100, Buckets: 4})
		if err != nil {
			t.Fatalf("read the empty flow: %v", err)
		}
		for at, count := range empty.Requests {
			if count != 0 {
				t.Errorf("empty flow bar %d = %d, want 0", at, count)
			}
		}
	})
}

func TestRepos(t *testing.T) {
	t.Run("lists the repos of top-level main sessions, most-used first", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(id, session, repo string) {
			t.Helper()
			n := nodeUpsert()
			n.ID = id
			n.SessionID = session
			n.Repo = sp(repo)
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", id, err)
			}
		}
		upsert("a/main", "a", "/w/app")
		upsert("b/main", "b", "/w/app")
		upsert("c/main", "c", "/w/zzz")
		// a subagent's repo never counts: only top-level main sessions name roots
		sub := nodeUpsert()
		sub.ID = "a/ag"
		sub.Kind = shared.NodeSubagent
		sub.ParentID = sp("a/main")
		sub.AgentID = "ag"
		sub.Repo = sp("/w/other")
		if err := h.UpsertNode(sub); err != nil {
			t.Fatalf("upsert the subagent: %v", err)
		}
		// a node with no repo never counts
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert the repoless node: %v", err)
		}
		repos, err := h.Repos()
		if err != nil {
			t.Fatalf("read the repos: %v", err)
		}
		want := []RepoRow{{Repo: "/w/app", Roots: 2}, {Repo: "/w/zzz", Roots: 1}}
		if !reflect.DeepEqual(repos, want) {
			t.Errorf("repos = %v, want %v", repos, want)
		}
	})
}

// transcriptFile writes a small conversation: three requests, each with an input and an
// output side, ts ascending, and answers its file and the request ids.
func transcriptFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "history.db")
	h, err := OpenHistory(file, nil)
	if err != nil {
		t.Fatalf("open history: %v", err)
	}
	if err := h.UpsertNode(nodeUpsert()); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	for at := 1; at <= 3; at++ {
		r := requestRecord()
		r.ID = fmt.Sprintf("r%d", at)
		r.Ts = int64(100 + at)
		if err := h.PutRequest("s1/main", r); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.PutContent(r.ID, ContentSides{
			Input:  sp(fmt.Sprintf(`[{"type":"text","text":"in %d"}]`, at)),
			Output: sp(fmt.Sprintf(`[{"type":"text","text":"out %d"}]`, at)),
		}); err != nil {
			t.Fatalf("put content: %v", err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return file
}

func TestAgentTranscript(t *testing.T) {
	t.Run("answers nil for an unknown node and an empty conversation for a quiet one", func(t *testing.T) {
		h := openHistoryAt(t)
		missing, err := h.AgentTranscript("nope", ContextQuery{Limit: 10})
		if err != nil {
			t.Fatalf("read the unknown agent: %v", err)
		}
		if missing != nil {
			t.Errorf("unknown agent = %+v, want nil", missing)
		}
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		quiet, err := h.AgentTranscript("s1/main", ContextQuery{Limit: 10})
		if err != nil {
			t.Fatalf("read the quiet agent: %v", err)
		}
		if quiet == nil {
			t.Fatal("the quiet agent answered nil")
		}
		if quiet.RequestID != "" || len(quiet.Messages) != 0 || quiet.Next != nil {
			t.Errorf("quiet conversation = %+v, want an empty one", quiet)
		}
	})

	t.Run("serves the whole conversation, newest answer included, and pages it back", func(t *testing.T) {
		h, err := OpenHistory(transcriptFile(t), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		whole, err := h.AgentTranscript("s1/main", ContextQuery{Limit: 100})
		if err != nil {
			t.Fatalf("read the transcript: %v", err)
		}
		if whole.RequestID != "r3" {
			t.Errorf("requestId = %q, want r3", whole.RequestID)
		}
		if whole.NodeID != "s1/main" || whole.Model != "glm-5.3-flash" {
			t.Errorf("node = %q model = %q, want s1/main on glm-5.3-flash", whole.NodeID, whole.Model)
		}
		want := []slimTurn{
			{"user", "r1", "in 1"}, {"assistant", "r1", "out 1"},
			{"user", "r2", "in 2"}, {"assistant", "r2", "out 2"},
			{"user", "r3", "in 3"}, {"assistant", "r3", "out 3"},
		}
		if got := slimTurnsOf(t, whole.Messages); !reflect.DeepEqual(got, want) {
			t.Errorf("transcript = %v, want %v", got, want)
		}
		if whole.Next != nil {
			t.Errorf("next = %q, want nil at the conversation's start", *whole.Next)
		}
		// the totals weigh every stored side, and the usage is the newest request's own
		if whole.Totals.Messages != 6 {
			t.Errorf("messages = %d, want 6", whole.Totals.Messages)
		}
		var bytes int64
		for at := 1; at <= 3; at++ {
			bytes += int64(len(fmt.Sprintf(`[{"type":"text","text":"in %d"}]`, at)))
			bytes += int64(len(fmt.Sprintf(`[{"type":"text","text":"out %d"}]`, at)))
		}
		if got, want := whole.Totals.ApproxTokens, (bytes+2)/4; got != want {
			t.Errorf("approxTokens = %d, want %d", got, want)
		}
		if whole.Totals.Usage == nil || whole.Totals.Usage.Input != 10 || whole.Totals.Usage.Output != 4 {
			t.Errorf("usage = %+v, want the newest request's own", whole.Totals.Usage)
		}
		// a page walk over the same conversation rebuilds it exactly
		var paged []shared.ContextMessage
		var cursor *string
		for page := 0; page < 10; page++ {
			answer, err := h.AgentTranscript("s1/main", ContextQuery{Limit: 2, Cursor: cursor})
			if err != nil {
				t.Fatalf("read a page: %v", err)
			}
			if answer == nil || len(answer.Messages) == 0 {
				break
			}
			paged = append(answer.Messages, paged...)
			cursor = answer.Next
			if cursor == nil {
				break
			}
		}
		if got := slimTurnsOf(t, paged); !reflect.DeepEqual(got, want) {
			t.Errorf("paged transcript = %v, want %v", got, want)
		}
		// a cursor that is not one of the node's answers an empty page
		stray, err := h.AgentTranscript("s1/main", ContextQuery{Limit: 2, Cursor: sp("999:r999:0")})
		if err != nil {
			t.Fatalf("read a stray cursor: %v", err)
		}
		if stray == nil || len(stray.Messages) != 0 || stray.Next != nil {
			t.Errorf("stray cursor page = %+v, want an empty one", stray)
		}
		// a cursor that parses as nothing at all answers the same empty page
		for _, broken := range []string{"abc", "1:x", "1:x:2", "x:1:0"} {
			page, err := h.AgentTranscript("s1/main", ContextQuery{Limit: 2, Cursor: sp(broken)})
			if err != nil {
				t.Fatalf("read the broken cursor %q: %v", broken, err)
			}
			if page == nil || len(page.Messages) != 0 || page.Next != nil {
				t.Errorf("broken cursor %q answered %+v, want an empty page", broken, page)
			}
		}
	})
}

func TestContextCursors(t *testing.T) {
	t.Run("answers nil for an unknown request and empty pages for broken cursors", func(t *testing.T) {
		h, err := OpenHistory(conversationFile(t), nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		missing, err := h.Context("r999", ContextQuery{Limit: 10})
		if err != nil {
			t.Fatalf("read the unknown request: %v", err)
		}
		if missing != nil {
			t.Errorf("unknown request = %+v, want nil", missing)
		}
		for _, broken := range []string{"abc", "1:x", "1:x:2", "x:1:0", ":r1:0"} {
			page, err := h.Context("r120", ContextQuery{Limit: 10, Cursor: sp(broken)})
			if err != nil {
				t.Fatalf("read the broken cursor %q: %v", broken, err)
			}
			if page == nil || len(page.Messages) != 0 || page.Next != nil {
				t.Errorf("broken cursor %q answered %+v, want an empty page", broken, page)
			}
		}
	})

	t.Run("reads a plain-text side as one text block with a cut preview", func(t *testing.T) {
		h := openHistoryAt(t)
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r := requestRecord()
		if err := h.PutRequest("s1/main", r); err != nil {
			t.Fatalf("put request: %v", err)
		}
		// a side that never was JSON is one text block; its first line previews, cut at
		// 200 UTF-16 units with an ellipsis
		long := strings.Repeat("é", 250) + "\nsecond line"
		if err := h.PutContent("r1", ContentSides{Input: &long, Output: sp("plain answer")}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		// the target's own answer never counts, so a second request reads both of r1's
		// sides
		r2 := requestRecord()
		r2.ID = "r2"
		r2.Ts = 1001
		if err := h.PutRequest("s1/main", r2); err != nil {
			t.Fatalf("put request: %v", err)
		}
		if err := h.PutContent("r2", ContentSides{Input: sp(`[{"type":"text","text":"in 2"}]`)}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		answer, err := h.Context("r2", ContextQuery{Limit: 10})
		if err != nil {
			t.Fatalf("read the context: %v", err)
		}
		if len(answer.Messages) != 3 {
			t.Fatalf("messages = %d, want 3", len(answer.Messages))
		}
		user := answer.Messages[0]
		if user.Kind != "text" || user.Role != "user" {
			t.Errorf("user turn = %q %q, want a text block of the user", user.Kind, user.Role)
		}
		if want := strings.Repeat("é", 200) + "…"; user.Preview != want {
			t.Errorf("preview = %q, want the first line cut at 200 UTF-16 units", user.Preview)
		}
		if want := int64(len(long)); user.Bytes != want {
			t.Errorf("bytes = %d, want %d", user.Bytes, want)
		}
		assistant := answer.Messages[1]
		if assistant.Role != "assistant" || assistant.Kind != "text" || assistant.Preview != "plain answer" {
			t.Errorf("assistant turn = %q %q %q, want a text block previewing plain answer",
				assistant.Role, assistant.Kind, assistant.Preview)
		}
	})
}

func TestCapture(t *testing.T) {
	t.Run("stores one capture per hash and reads it back decompressed", func(t *testing.T) {
		h := openHistoryAt(t)
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r := requestRecord()
		hash := "h1"
		r.PromptHash = &hash
		r.Headers = &shared.Headers{
			{Name: "x-cache", Value: "hit"},
			{Name: "request-id", Value: "req_1"},
			{Name: "retry-after", Value: "7"},
		}
		if err := h.PutRequest("s1/main", r); err != nil {
			t.Fatalf("put request: %v", err)
		}
		// a request whose hash has no capture yet answers nil
		none, err := h.Capture("r1")
		if err != nil {
			t.Fatalf("read the missing capture: %v", err)
		}
		if none != nil {
			t.Errorf("missing capture = %+v, want nil", none)
		}
		payload := `{"system":"be brief","tools":[{"name":"Bash"}]}`
		capture := shared.CaptureRecord{Hash: hash, Gz: base64.StdEncoding.EncodeToString(gzipText(t, payload)), FirstTs: 5}
		if err := h.PutCapture(capture); err != nil {
			t.Fatalf("put capture: %v", err)
		}
		// the second capture under the same hash never replaces the first
		other := shared.CaptureRecord{Hash: hash, Gz: base64.StdEncoding.EncodeToString(gzipText(t, `{"system":"other"}`)), FirstTs: 9}
		if err := h.PutCapture(other); err != nil {
			t.Fatalf("put the duplicate capture: %v", err)
		}
		answer, err := h.Capture("r1")
		if err != nil {
			t.Fatalf("read the capture: %v", err)
		}
		if answer == nil {
			t.Fatal("no capture came back")
		}
		if answer.Hash != hash {
			t.Errorf("hash = %q, want %q", answer.Hash, hash)
		}
		if answer.System.Value == nil || answer.System.Value.Kind() != jsfmt.KindString ||
			answer.System.Value.Text() != "be brief" {
			t.Errorf("system = %v, want be brief", answer.System)
		}
		tools := answer.Tools.Value
		if tools == nil || tools.Kind() != jsfmt.KindArray || len(tools.Items()) != 1 {
			t.Fatalf("tools = %v, want one tool", answer.Tools)
		}
		if name := fieldText(tools.Items()[0], "name"); name != "Bash" {
			t.Errorf("tool name = %q, want Bash", name)
		}
		if want := int64(len(payload)); answer.Bytes != want {
			t.Errorf("bytes = %d, want %d", answer.Bytes, want)
		}
		if value, ok := answer.Headers.Get("x-cache"); !ok || value != "hit" {
			t.Errorf("headers = %v, want x-cache hit", answer.Headers)
		}
		encoded, err := answer.Headers.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"x-cache":"hit","request-id":"req_1","retry-after":"7"}`; string(encoded) != want {
			t.Errorf("headers = %s, want the line's own order: %s", encoded, want)
		}
	})

	t.Run("answers nil for unknown requests, hashless rows and unreadable payloads", func(t *testing.T) {
		h := openHistoryAt(t)
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		missing, err := h.Capture("nope")
		if err != nil {
			t.Fatalf("read the unknown request: %v", err)
		}
		if missing != nil {
			t.Errorf("unknown request = %+v, want nil", missing)
		}
		if err := h.PutRequest("s1/main", requestRecord()); err != nil {
			t.Fatalf("put request: %v", err)
		}
		hashless, err := h.Capture("r1")
		if err != nil {
			t.Fatalf("read the hashless request: %v", err)
		}
		if hashless != nil {
			t.Errorf("hashless request = %+v, want nil", hashless)
		}
		// a payload that is not an object never reads back as a capture
		arrayPayload := base64.StdEncoding.EncodeToString(gzipText(t, `[1,2]`))
		if err := h.PutCapture(shared.CaptureRecord{Hash: "h2", Gz: arrayPayload, FirstTs: 1}); err != nil {
			t.Fatalf("put capture: %v", err)
		}
		withHash := requestRecord()
		withHash.ID = "r2"
		withHash.PromptHash = sp("h2")
		if err := h.PutRequest("s1/main", withHash); err != nil {
			t.Fatalf("put request: %v", err)
		}
		notObject, err := h.Capture("r2")
		if err != nil {
			t.Fatalf("read the array capture: %v", err)
		}
		if notObject != nil {
			t.Errorf("array capture = %+v, want nil", notObject)
		}
		// a capture whose gzip is not JSON answers nil the same way
		junk := base64.StdEncoding.EncodeToString(gzipText(t, "not json"))
		if err := h.PutCapture(shared.CaptureRecord{Hash: "h3", Gz: junk, FirstTs: 1}); err != nil {
			t.Fatalf("put capture: %v", err)
		}
		withJunk := requestRecord()
		withJunk.ID = "r3"
		withJunk.PromptHash = sp("h3")
		if err := h.PutRequest("s1/main", withJunk); err != nil {
			t.Fatalf("put request: %v", err)
		}
		unreadable, err := h.Capture("r3")
		if err != nil {
			t.Fatalf("read the junk capture: %v", err)
		}
		if unreadable != nil {
			t.Errorf("junk capture = %+v, want nil", unreadable)
		}
		if err := h.PutCapture(shared.CaptureRecord{Hash: "h4", Gz: "!!not base64!!", FirstTs: 1}); err == nil {
			t.Error("a capture that is not base64 stored anyway")
		}
	})
}

func TestStoredHeaders(t *testing.T) {
	t.Run("round-trips headers in arrival order and drops the values that are not strings", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		withHeaders := requestRecord()
		withHeaders.ID = "hdr"
		withHeaders.Headers = &shared.Headers{{Name: "x-b", Value: "2"}, {Name: "x-a", Value: "1"}}
		if err := h.PutRequest("s1/main", withHeaders); err != nil {
			t.Fatalf("put request: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the second connection: %v", err)
		}
		for id, headers := range map[string]string{
			// one value that is not a string drops, the string ones survive
			"mixed": `{"x-a":"1","x-b":2}`,
			// text that is not JSON at all answers no headers
			"junk": "not json",
			// an array is not a headers object either
			"array": `[1]`,
		} {
			if _, err := db.Run("INSERT INTO requests (id, node_id, ts, totals, headers) VALUES (?, 's1/main', 5, 0, ?)",
				id, headers); err != nil {
				t.Fatalf("insert %s: %v", id, err)
			}
		}
		// the stored column holds the object with the record's own key order, the order a
		// JSON stringify keeps
		stored, err := db.Get("SELECT headers FROM requests WHERE id = 'hdr'")
		if err != nil {
			t.Fatalf("read the stored headers: %v", err)
		}
		if got := rowText(stored, "headers"); got == nil || *got != `{"x-b":"2","x-a":"1"}` {
			t.Errorf("stored headers = %v, want the record's own order", got)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read requests: %v", err)
		}
		byID := map[string]shared.RequestRecord{}
		for _, row := range rows {
			byID[row.ID] = row
		}
		inOrder := &shared.Headers{{Name: "x-b", Value: "2"}, {Name: "x-a", Value: "1"}}
		if got := byID["hdr"].Headers; !reflect.DeepEqual(got, inOrder) {
			t.Errorf("round-tripped headers = %v, want both pairs in order: %v", got, inOrder)
		}
		stringAlone := &shared.Headers{{Name: "x-a", Value: "1"}}
		if got := byID["mixed"].Headers; !reflect.DeepEqual(got, stringAlone) {
			t.Errorf("mixed headers = %v, want the string pair alone: %v", got, stringAlone)
		}
		if got := byID["junk"].Headers; got != nil {
			t.Errorf("junk headers = %v, want nil", got)
		}
		if got := byID["array"].Headers; got != nil {
			t.Errorf("array headers = %v, want nil", got)
		}
	})

	t.Run("reads a headers text an earlier release wrote unchanged", func(t *testing.T) {
		// JSON.stringify of the headers a stored record held: the route line's own
		// order, which an alphabetical key sort would lose
		ts := `{"x-ratelimit-remaining-requests":"99","content-type":"application/json",` +
			`"request-id":"req_9"}`
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the second connection: %v", err)
		}
		if _, err := db.Run("INSERT INTO requests (id, node_id, ts, totals, headers) VALUES (?, 's1/main', 5, 0, ?)",
			"ts", ts); err != nil {
			t.Fatalf("insert the stored row: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		rows, err := h.RequestsOf(RequestsQuery{NodeID: sp("s1/main"), Limit: 10})
		if err != nil {
			t.Fatalf("read requests: %v", err)
		}
		if len(rows) != 1 || rows[0].ID != "ts" {
			t.Fatalf("rows = %+v, want the stored row alone", rows)
		}
		encoded, err := rows[0].Headers.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != ts {
			t.Errorf("headers = %s, want the stored text unchanged: %s", encoded, ts)
		}
	})
}

func TestPayloads(t *testing.T) {
	t.Run("stores an order-keeping payload and refuses one it cannot encode", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		value, err := jsfmt.DecodeValue([]byte(`{"b":1,"a":2}`))
		if err != nil {
			t.Fatalf("decode the payload: %v", err)
		}
		if err := h.AddEvent(nil, shared.EventRecord{Seq: 1, Ts: 1, Kind: "Notice", Payload: value}); err != nil {
			t.Fatalf("add event: %v", err)
		}
		if err := h.AddEvent(nil, shared.EventRecord{Seq: 2, Ts: 2, Kind: "Notice", Payload: nil}); err != nil {
			t.Fatalf("add the null event: %v", err)
		}
		if err := h.AddEvent(nil, shared.EventRecord{Seq: 3, Ts: 3, Kind: "Notice", Payload: make(chan int)}); err == nil {
			t.Error("a payload that cannot encode stored anyway")
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		rows, err := db.All("SELECT payload FROM events ORDER BY seq")
		if err != nil {
			t.Fatalf("read events: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("events = %d, want 2", len(rows))
		}
		if got := rowString(rows[0], "payload"); got != `{"b":1,"a":2}` {
			t.Errorf("payload = %q, want the arrival order kept", got)
		}
		if got := rowString(rows[1], "payload"); got != "null" {
			t.Errorf("null payload = %q, want null", got)
		}
	})

	t.Run("answers an error for a stored side that is not gzip", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		h, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		t.Cleanup(func() { _ = h.Close() })
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.PutRequest("s1/main", requestRecord()); err != nil {
			t.Fatalf("put request: %v", err)
		}
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the second connection: %v", err)
		}
		if _, err := db.Run("INSERT INTO content (request_id, input, output, bytes) VALUES ('r1', ?, NULL, 2)",
			[]byte{0x00, 0xff}); err != nil {
			t.Fatalf("insert the raw side: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		if _, err := h.Content("r1"); err == nil {
			t.Error("a side that is not gzip read back anyway")
		}
		answer, err := h.Context("r1", ContextQuery{Limit: 10})
		if err == nil {
			t.Errorf("a corrupt side rebuilt a context anyway: %+v", answer)
		}
	})
}

func TestOpenHistoryFailures(t *testing.T) {
	t.Run("refuses a path whose directory cannot be created", func(t *testing.T) {
		dir := t.TempDir()
		blocker := filepath.Join(dir, "blocker")
		if err := os.WriteFile(blocker, []byte("a file"), 0o600); err != nil {
			t.Fatalf("write the blocker: %v", err)
		}
		if _, err := OpenHistory(filepath.Join(blocker, "nested", "history.db"), nil); err == nil {
			t.Error("a history opened under a plain file")
		}
	})

	t.Run("refuses a database that will not migrate", func(t *testing.T) {
		dir := t.TempDir()
		under, err := OpenDB(filepath.Join(dir, "history.db"))
		if err != nil {
			t.Fatalf("open the database: %v", err)
		}
		t.Cleanup(func() { _ = under.Close() })
		if _, err := OpenHistory(filepath.Join(dir, "history.db"), failingAt{DB: under, marker: "CREATE TABLE IF NOT EXISTS nodes"}); err == nil {
			t.Error("a history opened over a failing migration")
		}
	})
}

func TestRowReaders(t *testing.T) {
	t.Run("read every shape a column can hold", func(t *testing.T) {
		row := map[string]SqlValue{
			"nil":    nil,
			"int":    int64(7),
			"float":  2.5,
			"intgo":  9,
			"text":   "word",
			"null":   SqlValue(nil),
			"blob":   []byte{1, 2},
			"number": int64(3),
		}
		if got := rowInt64(row, "nil"); got != nil {
			t.Errorf("nil column = %v, want nil", got)
		}
		if got := rowInt64(row, "int"); got == nil || *got != 7 {
			t.Errorf("int column = %v, want 7", got)
		}
		if got := rowInt64(row, "float"); got == nil || *got != 2 {
			t.Errorf("float column = %v, want 2", got)
		}
		if got := rowInt64(row, "intgo"); got == nil || *got != 9 {
			t.Errorf("go int column = %v, want 9", got)
		}
		if got := rowInt64(row, "text"); got == nil || *got != 0 {
			t.Errorf("text column = %v, want 0", got)
		}
		if got := rowText(row, "text"); got == nil || *got != "word" {
			t.Errorf("text column = %v, want word", got)
		}
		if got := rowText(row, "nil"); got != nil {
			t.Errorf("null column = %v, want nil", got)
		}
		if got := rowString(row, "text"); got != "word" {
			t.Errorf("text column = %q, want word", got)
		}
		if got := rowString(row, "nil"); got != "" {
			t.Errorf("null column = %q, want empty", got)
		}
		if got := rowBytes(row, "blob"); !reflect.DeepEqual(got, []byte{1, 2}) {
			t.Errorf("blob column = %v, want 1 2", got)
		}
		if got := rowBytes(row, "number"); got != nil {
			t.Errorf("number column read as %v, want no blob", got)
		}
		if got := fieldOf(nil, "x"); got != nil {
			t.Errorf("field of nothing = %v, want nil", got)
		}
	})
}

func TestRootsFilters(t *testing.T) {
	t.Run("filters by the root's own repo and keeps live sessions out of history", func(t *testing.T) {
		h := openHistoryAt(t)
		upsert := func(id, session, repo string, lastAt int64) {
			t.Helper()
			n := nodeUpsert()
			n.ID = id
			n.SessionID = session
			n.Repo = sp(repo)
			n.LastAt = i64p(lastAt)
			if err := h.UpsertNode(n); err != nil {
				t.Fatalf("upsert %s: %v", id, err)
			}
		}
		upsert("a/main", "a", "/w/app", 100)
		upsert("b/main", "b", "/w/site", 100)
		byRepo := historyQ()
		byRepo.Repo = sp("/w/app")
		roots, err := h.Roots(byRepo)
		if err != nil {
			t.Fatalf("read roots by repo: %v", err)
		}
		if want := []string{"a/main"}; !reflect.DeepEqual(rootIDs(roots), want) {
			t.Errorf("roots by repo = %v, want %v", rootIDs(roots), want)
		}
		// the exclusion list keeps an open-but-quiet session from reading as ended, in the
		// page and in the stats count alike
		excluded := historyQ()
		excluded.ExcludeSessions = []string{"a"}
		roots, err = h.Roots(excluded)
		if err != nil {
			t.Fatalf("read roots with exclusions: %v", err)
		}
		if want := []string{"b/main"}; !reflect.DeepEqual(rootIDs(roots), want) {
			t.Errorf("roots with exclusions = %v, want %v", rootIDs(roots), want)
		}
		stats, err := h.Stats(StatsQuery{Now: nowMs, LiveMs: liveMs, ExcludeSessions: []string{"a"}})
		if err != nil {
			t.Fatalf("read stats: %v", err)
		}
		if stats.Roots != 1 {
			t.Errorf("stats roots = %d, want 1", stats.Roots)
		}
		statsAll, err := h.Stats(StatsQuery{Now: nowMs, LiveMs: liveMs})
		if err != nil {
			t.Fatalf("read stats without exclusions: %v", err)
		}
		if statsAll.Roots != 2 {
			t.Errorf("stats roots = %d, want 2", statsAll.Roots)
		}
	})
}

func TestBlockShapes(t *testing.T) {
	t.Run("reads the kind and preview off blocks that are not text objects", func(t *testing.T) {
		h := openHistoryAt(t)
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		r1 := requestRecord()
		if err := h.PutRequest("s1/main", r1); err != nil {
			t.Fatalf("put request: %v", err)
		}
		// a bare number block reads as an unknown kind with nothing to preview; the text
		// of a later block names the preview
		if err := h.PutContent("r1", ContentSides{
			Input:  sp(`[123,{"text":"later block"}]`),
			Output: sp(`[{"type":"tool_use","input":{"a":1}},{"text":"after the tool"}]`),
		}); err != nil {
			t.Fatalf("put content: %v", err)
		}
		r2 := requestRecord()
		r2.ID = "r2"
		r2.Ts = 1001
		if err := h.PutRequest("s1/main", r2); err != nil {
			t.Fatalf("put request: %v", err)
		}
		answer, err := h.Context("r2", ContextQuery{Limit: 10})
		if err != nil {
			t.Fatalf("read the context: %v", err)
		}
		if len(answer.Messages) != 2 {
			t.Fatalf("messages = %d, want 2", len(answer.Messages))
		}
		user := answer.Messages[0]
		if user.Kind != "unknown" {
			t.Errorf("a bare number block read as kind %q, want unknown", user.Kind)
		}
		if user.Preview != "later block" {
			t.Errorf("preview = %q, want the later block's text", user.Preview)
		}
		assistant := answer.Messages[1]
		if assistant.Kind != "tool_use" {
			t.Errorf("kind = %q, want tool_use", assistant.Kind)
		}
		if assistant.Preview != "after the tool" {
			t.Errorf("preview = %q, want the text after the tool block", assistant.Preview)
		}
	})
}

func TestSpawnDepthMerge(t *testing.T) {
	t.Run("keeps the stored spawn depth and takes one that was never set", func(t *testing.T) {
		h := openHistoryAt(t)
		first := nodeUpsert()
		first.SpawnDepth = i64p(3)
		if err := h.UpsertNode(first); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		again := nodeUpsert()
		if err := h.UpsertNode(again); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		tree, err := h.Tree("s1/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the tree: %v", err)
		}
		if got := firstTreeRow(t, tree).SpawnDepth; got == nil || *got != 3 {
			t.Errorf("spawnDepth = %v, want the stored 3", got)
		}
		fresh := nodeUpsert()
		fresh.ID = "s2/main"
		fresh.SessionID = "s2"
		fresh.SpawnDepth = i64p(1)
		if err := h.UpsertNode(fresh); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		tree, err = h.Tree("s2/main", TreeQuery{})
		if err != nil {
			t.Fatalf("read the second tree: %v", err)
		}
		if got := firstTreeRow(t, tree).SpawnDepth; got == nil || *got != 1 {
			t.Errorf("spawnDepth = %v, want 1", got)
		}
	})
}

func TestTransactionFailures(t *testing.T) {
	t.Run("answers a begin that fails and a commit that fails", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		writer, err := OpenHistory(file, nil)
		if err != nil {
			t.Fatalf("open the writer: %v", err)
		}
		if err := hUpsert(writer, nodeUpsert()); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close the writer: %v", err)
		}
		for _, tc := range []struct {
			name   string
			marker string
		}{
			{"a begin that fails", "BEGIN IMMEDIATE"},
			{"a commit that fails", "COMMIT"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				under, err := OpenDB(file)
				if err != nil {
					t.Fatalf("open the database: %v", err)
				}
				t.Cleanup(func() { _ = under.Close() })
				h, err := OpenHistory(file, failingAt{DB: under, marker: tc.marker})
				if err != nil {
					t.Fatalf("open history: %v", err)
				}
				t.Cleanup(func() { _ = h.Close() })
				n := nodeUpsert()
				n.ID = "tx"
				if err := h.Transaction(func() error { return h.UpsertNode(n) }); err == nil {
					t.Error("the transaction passed a failing statement")
				}
				// the node never survived a failed begin or a rolled-back commit
				probe, err := OpenDB(file)
				if err != nil {
					t.Fatalf("open the probe: %v", err)
				}
				t.Cleanup(func() { _ = probe.Close() })
				row, err := probe.Get("SELECT COUNT(*) AS c FROM nodes WHERE id = 'tx'")
				if err != nil {
					t.Fatalf("count: %v", err)
				}
				if got := row["c"]; got != int64(0) {
					t.Errorf("the node survived: %v rows", got)
				}
			})
		}
	})
}

// hUpsert stores one node, the shorthand the transaction tests use.
func hUpsert(h *History, n NodeUpsert) error {
	return h.UpsertNode(n)
}

func TestClose(t *testing.T) {
	t.Run("answers a close that fails", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "history.db")
		under, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open the database: %v", err)
		}
		// the wrapper refuses to close, so the connection underneath stays open: release it
		// for the temp dir's cleanup
		t.Cleanup(func() { _ = under.Close() })
		h, err := OpenHistory(file, refusingClose{under})
		if err != nil {
			t.Fatalf("open history: %v", err)
		}
		if err := h.Close(); err == nil {
			t.Error("the close passed a failing connection")
		}
	})
}

// refusingClose answers every statement from the database it wraps and refuses to close.
type refusingClose struct {
	DB
}

func (r refusingClose) Close() error { return errors.New("refused") }

// The JSON a row stores is JSON.stringify's bytes: a request's headers column and an event's
// payload column keep `<`, `>`, `&` and the two line separators raw, the way JSON.stringify
// writes them.
func TestStoredJSONColumnsKeepJSONStringifyBytes(t *testing.T) {
	t.Run("a request's headers column", func(t *testing.T) {
		h := openHistoryAt(t)
		if err := h.UpsertNode(nodeUpsert()); err != nil {
			t.Fatal(err)
		}
		r := requestRecord()
		r.Headers = &shared.Headers{{Name: "x-note", Value: "a<b>c&d e f"}}
		if err := h.PutRequest("s1/main", r); err != nil {
			t.Fatal(err)
		}
		row, err := h.db.Get("SELECT headers FROM requests WHERE id = ?", r.ID)
		if err != nil {
			t.Fatalf("read the row: %v", err)
		}
		if row == nil {
			t.Fatal("no request row came back")
		}
		want := "{\"x-note\":\"a<b>c&d e f\"}"
		if got := row["headers"]; got != want {
			t.Errorf("headers = %v, want %s", got, want)
		}
	})

	t.Run("an event's payload column", func(t *testing.T) {
		h := openHistoryAt(t)
		raw := []byte("{\"ts\":\"2026-10-15T05:00:00.000Z\",\"event\":\"Notification\"," +
			"\"session_id\":\"s1\",\"message\":\"a<b>c&d e f\"}")
		var line shared.SpoolLine
		if err := line.UnmarshalJSON(raw); err != nil {
			t.Fatal(err)
		}
		if err := h.AddEvent(nil, shared.EventRecord{Seq: 1, Ts: 5, Kind: "Notification",
			Payload: line}); err != nil {
			t.Fatal(err)
		}
		row, err := h.db.Get("SELECT payload FROM events WHERE seq = ?", int64(1))
		if err != nil {
			t.Fatalf("read the row: %v", err)
		}
		if row == nil {
			t.Fatal("no event row came back")
		}
		if got := row["payload"]; got != string(raw) {
			t.Errorf("payload = %v, want %s", got, raw)
		}
	})
}

// TestGzipBytesReusesItsCompressor pins what storing a side costs: a gzip.Writer carries
// about 1 MB of compressor state, so a writer per stored side made radar's catch-up the
// hub's main allocation. The pooled writer keeps one state across calls, round-trips every
// size whole, and writes exactly the bytes a fresh writer writes.
func TestGzipBytesReusesItsCompressor(t *testing.T) {
	fourKiB := strings.Repeat("huddle ", 585) // 4,095 bytes of plain text
	oneMiB := strings.Repeat("huddle ", 149_596)

	t.Run("round trips an empty, a 4 KiB and a 1 MiB side", func(t *testing.T) {
		for _, text := range []string{"", fourKiB, oneMiB} {
			got, err := gunzipBytes(gzipBytes(text))
			if err != nil {
				t.Fatalf("gunzip %d bytes: %v", len(text), err)
			}
			if got != text {
				t.Fatalf("a %d-byte side came back %d bytes", len(text), len(got))
			}
		}
	})

	t.Run("writes what a fresh gzip.Writer writes", func(t *testing.T) {
		var buf bytes.Buffer
		writer := gzip.NewWriter(&buf)
		_, _ = writer.Write([]byte(fourKiB))
		_ = writer.Close()
		if !bytes.Equal(gzipBytes(fourKiB), buf.Bytes()) {
			t.Fatal("the pooled writer's bytes differ from a fresh writer's")
		}
	})

	t.Run("2,000 sides of 4 KiB allocate under 64 MiB", func(t *testing.T) {
		limit := uint64(64 << 20)
		if testkit.RaceEnabled {
			// the detector's own allocations ride along (about 530 MiB here against about
			// 2 GiB with a writer per side), so the bound scales up and still pins the pool
			limit = 1 << 30
		}
		gzipBytes(fourKiB) // the pool warms: the first compressor's build is not the churn
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < 2_000; i++ {
			gzipBytes(fourKiB)
		}
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		if grew := after.TotalAlloc - before.TotalAlloc; grew >= limit {
			t.Fatalf("2,000 gzipBytes of 4 KiB allocated %d MiB; want under %d MiB", grew>>20, limit>>20)
		}
	})
}

// TestGzipBytesUnderConcurrency shares the pool across writers: sides gzipped from several
// goroutines at once round-trip whole, under -race.
func TestGzipBytesUnderConcurrency(t *testing.T) {
	text := strings.Repeat("huddle ", 585)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				got, err := gunzipBytes(gzipBytes(text))
				if err != nil {
					t.Errorf("gunzip: %v", err)
					return
				}
				if got != text {
					t.Errorf("a side came back %d bytes, want %d", len(got), len(text))
					return
				}
			}
		}()
	}
	wg.Wait()
}
