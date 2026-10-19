// The persistent history: one SQLite file holding whole session trees (main session,
// subagents, jobs), their requests with gzipped input/output text, tool calls, events and
// file offsets. It survives restarts and outlives the transcripts and worktrees, so the
// dashboard can show what ran after the raw data is gone. All methods are synchronous
// after open, every write is a plain statement, and a read groups its roll-ups over the
// trees an IN list names — one query per 400 roots, never one per root.
package history

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// End is the endedAt field of an upsert: NoEnd (the zero value) keeps the stored end,
// ClearEnd clears a stale one, EndAt stamps it.
type End struct {
	action uint8
	at     int64
}

// NoEnd leaves the stored end as it is.
func NoEnd() End { return End{} }

// ClearEnd clears a stale end (a session that came back).
func ClearEnd() End { return End{action: endClear} }

// EndAt stamps the end, keeping the later of it and the stored one.
func EndAt(at int64) End { return End{action: endStamp, at: at} }

const (
	endClear = uint8(1)
	endStamp = uint8(2)
)

// NodeUpsert is one node write: a null or absent field keeps the stored value on merge.
// The workspace fields (repo, branch, name, parentSessionId) name the tree a session ran
// in after the worktrees are deleted.
type NodeUpsert struct {
	ID              string
	Kind            shared.NodeKind
	SessionID       string
	AgentID         string
	ParentID        *string
	Label           *string
	AgentType       *string
	Description     *string
	Project         *string
	Cwd             *string
	Model           *string
	Provider        *string
	ToolUseID       *string
	SpawnDepth      *int64
	JobState        *string
	Repo            *string
	Branch          *string
	Name            *string
	ParentSessionID *string
	StartedAt       *int64
	LastAt          *int64
	EndedAt         End
}

// Scope selects which trees a roots read lists: the live ones or the ended ones.
type Scope string

const (
	ScopeLive    Scope = "live"
	ScopeHistory Scope = "history"
)

// RootsQuery is one roots read: the scope, the clock, the page and its filters.
// ExcludeSessions names the sessions the history scope keeps out — the registry's live
// ones, so a session that is open but quiet for a while never reads as ended. It applies
// in the query, so paging and counts stay exact.
type RootsQuery struct {
	Scope           Scope
	Now             int64
	LiveMs          int64
	Limit           int64
	Before          *int64
	Search          *string
	Repo            *string
	ExcludeSessions []string
}

// UnattachedQuery is what the one Unattached group needs: the clock that tells a
// still-running job from an ended one.
type UnattachedQuery struct {
	Now    int64
	LiveMs int64
}

// TreeQuery is the clock a tree read orders liveness by; a nil field takes the default.
type TreeQuery struct {
	Now    *int64
	LiveMs *int64
}

// RequestCursor is one request's place in the newest-first order.
type RequestCursor struct {
	Ts int64
	ID string
}

// RequestsQuery is one requests read: by node, by tree, or the whole table, paged by
// keyset.
type RequestsQuery struct {
	NodeID *string
	RootID *string
	Before *RequestCursor
	Limit  int64
}

// ContextQuery is one page of a rebuilt conversation: the newest `limit` messages, or the
// ones before `cursor`.
type ContextQuery struct {
	Limit  int64
	Cursor *string
}

// FlowQuery is one token-flow read: the window, the width of a bar and how many bars the
// chart draws.
type FlowQuery struct {
	From     int64
	To       int64
	BucketMs int64
	Buckets  int64
}

// StatsQuery is one stats read; ExcludeSessions carries the live exclusions the rail's
// History badge counts with.
type StatsQuery struct {
	Now             int64
	LiveMs          int64
	ExcludeSessions []string
}

// ContentSides is one content write: each side nil keeps the stored one.
type ContentSides struct {
	Input  *string
	Output *string
}

// ContentAnswer is one request's stored sides, decompressed, with the plain byte count.
type ContentAnswer struct {
	Input  *string
	Output *string
	Bytes  int64
}

// FileOffset is one watched file's read position.
type FileOffset struct {
	Offset int64
	Size   int64
	Mtime  int64
}

// CostRow is the fields one request's cost estimate reads, joined with the node and tree
// it rolled into. A request of an unattached job answers to the Unattached group's id,
// not to its job's own.
type CostRow struct {
	NodeID       string
	RootID       string
	Model        *string
	Upstream     *string
	Ts           int64
	Input        *int64
	Output       *int64
	CacheRead    *int64
	CacheWrite   *int64
	CacheWrite1h *int64
	Speed        *string
	Geo          *string
	ServiceTier  *string
}

// RepoRow is one repo top-level trees ran in, with its root count.
type RepoRow struct {
	Repo  string
	Roots int64
}

// Stats is the store's size, its row counts, and how many ended sessions history holds.
type Stats struct {
	Bytes    int64
	Nodes    int64
	Requests int64
	Roots    int64
}

// StoredSide is one side of a content row as the merge rule reads it: the blob and its
// plain text length.
type StoredSide struct {
	Blob []byte
	Len  *int64
}

// SideOfNext decides one side of a rewrite by the stored length before anything
// decompresses: the stored blob stands while the incoming text is not strictly longer, so
// a streamed message rewritten in place wins and a short retry echo never erases what it
// held — and a kept side is reused as it stands, gzipped fresh only when it grew. A nil
// incoming keeps the stored side whatever it is.
func SideOfNext(stored StoredSide, incoming *string) StoredSide {
	if incoming == nil {
		return stored
	}
	length := int64(len(*incoming))
	if stored.Blob != nil && derep(stored.Len, 0) >= length {
		return stored
	}
	return StoredSide{Blob: gzipBytes(*incoming), Len: &length}
}

// History is the persistent store the dashboard's History view reads. A transaction and
// the statements inside it run on one goroutine; statements from other goroutines
// serialize per statement on the single connection the driver holds.
type History struct {
	db    DB
	depth int
	tx    sync.Mutex
	now   func() time.Time
}

// OpenHistory opens the history file (creating its 0700 directory first, the file
// chmodded 0600), migrates it, and answers the store. A non-nil underlying database
// becomes the store's database, so a caller can wrap one and count what a read costs.
func OpenHistory(file string, underlying DB) (*History, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, fmt.Errorf("create the directory of %s: %w", file, err)
	}
	db := underlying
	if db == nil {
		opened, err := OpenDB(file)
		if err != nil {
			return nil, err
		}
		db = opened
	}
	if err := Migrate(db); err != nil {
		if underlying == nil {
			_ = db.Close()
		}
		return nil, fmt.Errorf("migrate %s: %w", file, err)
	}
	if err := os.Chmod(file, 0o600); err != nil {
		return nil, fmt.Errorf("chmod %s: %w", file, err)
	}
	return &History{db: db, now: time.Now}, nil
}

// Transaction runs fn inside one immediate transaction: the first call opens it, a nested
// call joins the open one, and an error from fn rolls everything back.
func (h *History) Transaction(fn func() error) error {
	h.tx.Lock()
	if h.depth > 0 {
		h.tx.Unlock()
		return fn()
	}
	h.depth = 1
	h.tx.Unlock()
	defer func() {
		h.tx.Lock()
		h.depth = 0
		h.tx.Unlock()
	}()
	if _, err := h.db.Run("BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	out := fn()
	if out != nil {
		// the statement that failed may have rolled the transaction back already
		_, _ = h.db.Run("ROLLBACK")
		return out
	}
	if _, err := h.db.Run("COMMIT"); err != nil {
		_, _ = h.db.Run("ROLLBACK")
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// The nodes table's read and write shapes.

const nodeColumns = "id, kind, parent_id, root_id, session_id, agent_id, label, agent_type, description, " +
	"project, cwd, model, provider, tool_use_id, spawn_depth, job_state, repo, branch, name, " +
	"parent_session_id, started_at, last_at, ended_at"

const insertNode = "INSERT INTO nodes (id, kind, parent_id, root_id, session_id, agent_id, label, agent_type, description, " +
	"project, cwd, model, provider, tool_use_id, spawn_depth, job_state, repo, branch, name, " +
	"parent_session_id, started_at, last_at, ended_at) " +
	"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"

const updateNode = "UPDATE nodes SET kind = ?, parent_id = ?, root_id = ?, session_id = ?, agent_id = ?, label = ?, " +
	"agent_type = ?, description = ?, project = ?, cwd = ?, model = ?, provider = ?, tool_use_id = ?, " +
	"spawn_depth = ?, job_state = ?, repo = ?, branch = ?, name = ?, parent_session_id = ?, " +
	"started_at = ?, last_at = ?, ended_at = ? WHERE id = ?"

// nodeRow is one nodes row, column names and all.
type nodeRow struct {
	id              string
	kind            string
	parentID        *string
	rootID          string
	sessionID       string
	agentID         string
	label           *string
	agentType       *string
	description     *string
	project         *string
	cwd             *string
	model           *string
	provider        *string
	toolUseID       *string
	spawnDepth      *int64
	jobState        *string
	repo            *string
	branch          *string
	name            *string
	parentSessionID *string
	startedAt       *int64
	lastAt          *int64
	endedAt         *int64
}

func nodeRowOf(row map[string]SqlValue) nodeRow {
	return nodeRow{
		id:              rowString(row, "id"),
		kind:            rowString(row, "kind"),
		parentID:        rowText(row, "parent_id"),
		rootID:          rowString(row, "root_id"),
		sessionID:       rowString(row, "session_id"),
		agentID:         rowString(row, "agent_id"),
		label:           rowText(row, "label"),
		agentType:       rowText(row, "agent_type"),
		description:     rowText(row, "description"),
		project:         rowText(row, "project"),
		cwd:             rowText(row, "cwd"),
		model:           rowText(row, "model"),
		provider:        rowText(row, "provider"),
		toolUseID:       rowText(row, "tool_use_id"),
		spawnDepth:      rowInt64(row, "spawn_depth"),
		jobState:        rowText(row, "job_state"),
		repo:            rowText(row, "repo"),
		branch:          rowText(row, "branch"),
		name:            rowText(row, "name"),
		parentSessionID: rowText(row, "parent_session_id"),
		startedAt:       rowInt64(row, "started_at"),
		lastAt:          rowInt64(row, "last_at"),
		endedAt:         rowInt64(row, "ended_at"),
	}
}

// UpsertNode inserts or merges one node, then re-points the subtree when the parent moved
// or the node was adopted.
func (h *History) UpsertNode(n NodeUpsert) error {
	stored, err := h.getNodeRow(n.ID)
	if err != nil {
		return err
	}
	isNew := stored == nil
	var next nodeRow
	if isNew {
		next = nodeFromUpsert(n)
	} else {
		next = mergeNode(*stored, n)
	}
	parentChanged := !isNew && !sameText(stored.parentID, next.parentID)
	if err := h.writeNode(next, isNew); err != nil {
		return err
	}
	root, err := h.rootOf(next.id)
	if err != nil {
		return err
	}
	// a fresh node may be adopted by children that named it as parent before it existed
	if isNew || parentChanged || root != next.rootID {
		return h.reRoot(next.id, root)
	}
	return nil
}

// EndNode stamps the node's end, keeping the later of it and the stored one.
func (h *History) EndNode(id string, at int64) error {
	if _, err := h.db.Run(
		"UPDATE nodes SET ended_at = CASE WHEN ended_at IS NULL OR ended_at < ? THEN ? ELSE ended_at END "+
			"WHERE id = ?", at, at, id); err != nil {
		return fmt.Errorf("end the node %s: %w", id, err)
	}
	return nil
}

func (h *History) getNodeRow(id string) (*nodeRow, error) {
	row, err := h.db.Get("SELECT "+nodeColumns+" FROM nodes WHERE id = ?", id)
	if err != nil {
		return nil, fmt.Errorf("read the node %s: %w", id, err)
	}
	if row == nil {
		return nil, nil
	}
	parsed := nodeRowOf(row)
	return &parsed, nil
}

// nodeFromUpsert stores an upsert as a fresh row; an absent or nil field is SQL NULL.
func nodeFromUpsert(n NodeUpsert) nodeRow {
	return nodeRow{
		id:              n.ID,
		kind:            string(n.Kind),
		parentID:        n.ParentID,
		rootID:          derep(n.ParentID, n.ID),
		sessionID:       n.SessionID,
		agentID:         n.AgentID,
		label:           n.Label,
		agentType:       n.AgentType,
		description:     n.Description,
		project:         n.Project,
		cwd:             n.Cwd,
		model:           n.Model,
		provider:        n.Provider,
		toolUseID:       n.ToolUseID,
		spawnDepth:      n.SpawnDepth,
		jobState:        n.JobState,
		repo:            n.Repo,
		branch:          n.Branch,
		name:            n.Name,
		parentSessionID: n.ParentSessionID,
		startedAt:       n.StartedAt,
		lastAt:          n.LastAt,
		endedAt:         endOfUpsert(n.EndedAt),
	}
}

// endOfUpsert is the end a fresh row stores: a stamp writes it, anything else leaves it
// unset.
func endOfUpsert(e End) *int64 {
	if e.action == endStamp {
		return &e.at
	}
	return nil
}

// mergeNode folds one upsert into a stored row: a non-null incoming field overwrites;
// started_at keeps the minimum and last_at the maximum.
func mergeNode(s nodeRow, n NodeUpsert) nodeRow {
	lastAt := maxOfInt(s.lastAt, n.LastAt)
	// a write after the end means the node came back; an explicit null ends the same, a
	// stamp sets it
	endedAt := s.endedAt
	if s.endedAt != nil && lastAt != nil && *lastAt > *s.endedAt {
		endedAt = nil
	}
	switch n.EndedAt.action {
	case endClear:
		endedAt = nil
	case endStamp:
		stamp := n.EndedAt.at
		endedAt = maxOfInt(endedAt, &stamp)
	}
	return nodeRow{
		id:              s.id,
		kind:            string(n.Kind),
		parentID:        pickText(n.ParentID, s.parentID),
		rootID:          s.rootID,
		sessionID:       n.SessionID,
		agentID:         n.AgentID,
		label:           pickText(n.Label, s.label),
		agentType:       pickText(n.AgentType, s.agentType),
		description:     pickText(n.Description, s.description),
		project:         pickText(n.Project, s.project),
		cwd:             pickText(n.Cwd, s.cwd),
		model:           pickText(n.Model, s.model),
		provider:        pickText(n.Provider, s.provider),
		toolUseID:       pickText(n.ToolUseID, s.toolUseID),
		spawnDepth:      pickInt64(n.SpawnDepth, s.spawnDepth),
		jobState:        pickText(n.JobState, s.jobState),
		repo:            pickText(n.Repo, s.repo),
		branch:          pickText(n.Branch, s.branch),
		name:            pickText(n.Name, s.name),
		parentSessionID: pickText(n.ParentSessionID, s.parentSessionID),
		startedAt:       minOfInt(s.startedAt, n.StartedAt),
		lastAt:          lastAt,
		endedAt:         endedAt,
	}
}

// nodeValues lists the row's writable columns in the UPDATE order.
func nodeValues(row nodeRow) []SqlValue {
	return []SqlValue{
		row.kind,
		sqlText(row.parentID),
		row.rootID,
		row.sessionID,
		row.agentID,
		sqlText(row.label),
		sqlText(row.agentType),
		sqlText(row.description),
		sqlText(row.project),
		sqlText(row.cwd),
		sqlText(row.model),
		sqlText(row.provider),
		sqlText(row.toolUseID),
		sqlInt64(row.spawnDepth),
		sqlText(row.jobState),
		sqlText(row.repo),
		sqlText(row.branch),
		sqlText(row.name),
		sqlText(row.parentSessionID),
		sqlInt64(row.startedAt),
		sqlInt64(row.lastAt),
		sqlInt64(row.endedAt),
	}
}

func (h *History) writeNode(row nodeRow, isNew bool) error {
	if isNew {
		params := append([]SqlValue{row.id}, nodeValues(row)...)
		if _, err := h.db.Run(insertNode, params...); err != nil {
			return fmt.Errorf("insert the node %s: %w", row.id, err)
		}
		return nil
	}
	params := append(nodeValues(row), row.id)
	if _, err := h.db.Run(updateNode, params...); err != nil {
		return fmt.Errorf("update the node %s: %w", row.id, err)
	}
	return nil
}

// Every node of the tree rooted at the parameter; plain UNION (not UNION ALL) stops a
// stray parent cycle.
const treeIds = "WITH RECURSIVE tree(id) AS (SELECT ? UNION SELECT n.id FROM nodes n JOIN tree ON n.parent_id = tree.id)"

// maxDepth caps a parent chain walk, so a stray cycle cannot spin.
const maxDepth = 64

// rootOf walks parent links up to the top of the tree. A chain ending at a parent that
// has no row yet leaves the node its own root; inserting that parent re-roots the subtree.
func (h *History) rootOf(id string) (string, error) {
	current := id
	for depth := 0; depth < maxDepth; depth++ {
		row, err := h.db.Get("SELECT parent_id FROM nodes WHERE id = ?", current)
		if err != nil {
			return "", fmt.Errorf("read the parent of %s: %w", current, err)
		}
		if row == nil {
			return current, nil
		}
		parent := rowText(row, "parent_id")
		if parent == nil || *parent == current {
			return current, nil
		}
		parentRow, err := h.db.Get("SELECT parent_id FROM nodes WHERE id = ?", *parent)
		if err != nil {
			return "", fmt.Errorf("read the parent of %s: %w", *parent, err)
		}
		if parentRow == nil {
			return current, nil
		}
		current = *parent
	}
	return current, nil
}

// reRoot points the node and every descendant at the new root (a re-parent moves the
// whole subtree).
func (h *History) reRoot(id, root string) error {
	if _, err := h.db.Run(treeIds+" UPDATE nodes SET root_id = ? WHERE id IN (SELECT id FROM tree)",
		id, root); err != nil {
		return fmt.Errorf("re-root %s at %s: %w", id, root, err)
	}
	return nil
}

// The requests table.

// upsertRequest is ON CONFLICT DO UPDATE, not REPLACE: re-putting a request must not
// cascade-delete its content row.
const upsertRequest = "INSERT INTO requests (id, node_id, session_id, agent_id, model, upstream, ts, latency_ms, " +
	"input, output, cache_read, cache_write, cache_write_1h, speed, geo, service_tier, stop_reason, " +
	"error, parent_agent_id, provider, route, via, status, harness, cost_usd, what, thinking, prompt_hash, headers, totals) " +
	"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) " +
	"ON CONFLICT(id) DO UPDATE SET node_id = excluded.node_id, session_id = excluded.session_id, " +
	"agent_id = excluded.agent_id, model = excluded.model, upstream = excluded.upstream, ts = excluded.ts, " +
	"latency_ms = excluded.latency_ms, input = excluded.input, output = excluded.output, " +
	"cache_read = excluded.cache_read, cache_write = excluded.cache_write, " +
	"cache_write_1h = excluded.cache_write_1h, speed = excluded.speed, geo = excluded.geo, " +
	"service_tier = excluded.service_tier, stop_reason = excluded.stop_reason, error = excluded.error, " +
	"parent_agent_id = excluded.parent_agent_id, provider = excluded.provider, " +
	"route = excluded.route, via = excluded.via, status = excluded.status, harness = excluded.harness, " +
	"cost_usd = excluded.cost_usd, what = excluded.what, " +
	"thinking = excluded.thinking, prompt_hash = excluded.prompt_hash, headers = excluded.headers, " +
	"totals = excluded.totals"

const requestColumns = "id, node_id, session_id, agent_id, model, upstream, ts, latency_ms, input, output, " +
	"cache_read, cache_write, cache_write_1h, speed, geo, service_tier, stop_reason, error, " +
	"parent_agent_id, provider, route, via, status, harness, cost_usd, what, thinking, prompt_hash, headers, totals"

// requestRow is one requests row, column names and all.
type requestRow struct {
	id            string
	sessionID     *string
	agentID       *string
	model         *string
	upstream      *string
	ts            int64
	latencyMs     *int64
	input         *int64
	output        *int64
	cacheRead     *int64
	cacheWrite    *int64
	cacheWrite1h  *int64
	speed         *string
	geo           *string
	serviceTier   *string
	stopReason    *string
	errorText     *string
	parentAgentID *string
	provider      *string
	route         *string
	via           *string
	status        *string
	harness       *string
	costUSD       *float64
	what          *string
	thinking      *int64
	promptHash    *string
	headers       *string
	totals        int64
}

func requestRowOf(row map[string]SqlValue) requestRow {
	return requestRow{
		id:            rowString(row, "id"),
		sessionID:     rowText(row, "session_id"),
		agentID:       rowText(row, "agent_id"),
		model:         rowText(row, "model"),
		upstream:      rowText(row, "upstream"),
		ts:            derep(rowInt64(row, "ts"), 0),
		latencyMs:     rowInt64(row, "latency_ms"),
		input:         rowInt64(row, "input"),
		output:        rowInt64(row, "output"),
		cacheRead:     rowInt64(row, "cache_read"),
		cacheWrite:    rowInt64(row, "cache_write"),
		cacheWrite1h:  rowInt64(row, "cache_write_1h"),
		speed:         rowText(row, "speed"),
		geo:           rowText(row, "geo"),
		serviceTier:   rowText(row, "service_tier"),
		stopReason:    rowText(row, "stop_reason"),
		errorText:     rowText(row, "error"),
		parentAgentID: rowText(row, "parent_agent_id"),
		provider:      rowText(row, "provider"),
		route:         rowText(row, "route"),
		via:           rowText(row, "via"),
		status:        rowText(row, "status"),
		harness:       rowText(row, "harness"),
		costUSD:       rowFloat64(row, "cost_usd"),
		what:          rowText(row, "what"),
		thinking:      rowInt64(row, "thinking"),
		promptHash:    rowText(row, "prompt_hash"),
		headers:       rowText(row, "headers"),
		totals:        derep(rowInt64(row, "totals"), 0),
	}
}

// harnessOf is the harness a request row stores: an untagged record is a Claude Code one.
func harnessOf(r shared.RequestRecord) string {
	if r.Harness == "" {
		return shared.HarnessClaude
	}
	return r.Harness
}

// PutRequest stores one request, then bumps its node's last_at: a request after the
// node's end is a session that came back, so the end clears.
func (h *History) PutRequest(nodeID string, r shared.RequestRecord) error {
	params := []SqlValue{
		r.ID,
		nodeID,
		r.SessionID,
		r.AgentID,
		r.Model,
		r.Upstream,
		r.Ts,
		sqlInt64(r.LatencyMs),
		r.Tokens.Input,
		r.Tokens.Output,
		r.Tokens.CacheRead,
		r.Tokens.CacheWrite,
		sqlInt64(r.CacheWrite1h),
		sqlText(r.Speed),
		sqlText(r.Geo),
		sqlText(r.ServiceTier),
		sqlText(r.StopReason),
		sqlText(r.Error),
		sqlText(r.ParentAgentID),
		r.Provider,
		sqlText(r.Route),
		sqlText(r.Via),
		sqlText(r.Status),
		harnessOf(r),
		sqlFloat64(r.CostUSD),
		sqlText(r.What),
		sqlInt64(r.Tokens.Thinking),
	}
	params = append(params, captureParams(r)...)
	totals := int64(0)
	if r.Totals {
		totals = 1
	}
	params = append(params, totals)
	if _, err := h.db.Run(upsertRequest, params...); err != nil {
		return fmt.Errorf("put the request %s: %w", r.ID, err)
	}
	if _, err := h.db.Run(
		"UPDATE nodes SET last_at = CASE WHEN last_at IS NULL OR last_at < ? THEN ? ELSE last_at END, "+
			"ended_at = CASE WHEN ended_at IS NOT NULL AND ended_at < ? THEN NULL ELSE ended_at END WHERE id = ?",
		r.Ts, r.Ts, r.Ts, nodeID); err != nil {
		return fmt.Errorf("bump the node of %s: %w", r.ID, err)
	}
	return nil
}

// captureParams answers the two columns a request's capture link is stored in: its hash,
// and its headers as the JSON the row keeps, the pairs in the record's own order (null
// when the record carried neither).
func captureParams(r shared.RequestRecord) []SqlValue {
	var headers SqlValue
	if r.Headers != nil {
		encoded, err := domain.MarshalCompactJSON(r.Headers)
		if err == nil {
			headers = string(encoded)
		}
	}
	return []SqlValue{sqlText(r.PromptHash), headers}
}

// toRequest answers the record one row holds: the columns the row may not have stored sit
// only when not null, and thinking rides along only when the provider reported it.
func toRequest(row requestRow) shared.RequestRecord {
	r := shared.RequestRecord{
		ID:        row.id,
		SessionID: derep(row.sessionID, ""),
		AgentID:   derep(row.agentID, ""),
		Model:     derep(row.model, ""),
		Upstream:  derep(row.upstream, ""),
		Ts:        row.ts,
		LatencyMs: row.latencyMs,
		Tokens: shared.Tokens{
			Input:      derep(row.input, 0),
			Output:     derep(row.output, 0),
			CacheRead:  derep(row.cacheRead, 0),
			CacheWrite: derep(row.cacheWrite, 0),
			Thinking:   row.thinking,
		},
		StopReason: row.stopReason,
		Provider:   derep(row.provider, ""),
	}
	if row.route != nil {
		r.Route = row.route
	}
	if row.via != nil {
		r.Via = row.via
	}
	if row.status != nil {
		r.Status = row.status
	}
	// claude rows keep no harness: absence reads as Claude Code, as it prints
	if row.harness != nil && *row.harness != shared.HarnessClaude {
		r.Harness = *row.harness
	}
	if row.costUSD != nil {
		r.CostUSD = row.costUSD
	}
	if row.what != nil {
		r.What = row.what
	}
	if row.errorText != nil {
		r.Error = row.errorText
	}
	if row.parentAgentID != nil {
		r.ParentAgentID = row.parentAgentID
	}
	if row.promptHash != nil {
		r.PromptHash = row.promptHash
	}
	if headers := headersOf(row.headers); headers != nil {
		r.Headers = headers
	}
	if row.totals == 1 {
		r.Totals = true
	}
	if row.cacheWrite1h != nil {
		r.CacheWrite1h = row.cacheWrite1h
	}
	if row.speed != nil {
		r.Speed = row.speed
	}
	if row.geo != nil {
		r.Geo = row.geo
	}
	if row.serviceTier != nil {
		r.ServiceTier = row.serviceTier
	}
	return r
}

// headersOf reads the headers a request row's JSON says; nil when it says nothing
// readable. The pairs keep the order the stored object spelled, and only the
// string-valued names survive.
func headersOf(text *string) *shared.Headers {
	if text == nil {
		return nil
	}
	var headers shared.Headers
	if err := json.Unmarshal([]byte(*text), &headers); err != nil || headers == nil {
		return nil
	}
	return &headers
}

// The content table: each side gzipped, with its plain length beside it.

// gzipBytes compresses one side the way the store writes it.
func gzipBytes(text string) []byte {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	_, _ = writer.Write([]byte(text))
	_ = writer.Close()
	return buf.Bytes()
}

// gunzipBytes reads a stored side back as text.
func gunzipBytes(blob []byte) (string, error) {
	reader, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return "", fmt.Errorf("open the gzip stream: %w", err)
	}
	defer reader.Close()
	text, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("read the gzip stream: %w", err)
	}
	return string(text), nil
}

// freshSide gzips a side a first write carries.
func freshSide(text *string) StoredSide {
	if text == nil {
		return StoredSide{}
	}
	length := int64(len(*text))
	return StoredSide{Blob: gzipBytes(*text), Len: &length}
}

// PutContent stores one request's sides, each merging on its own through SideOfNext: the
// stored lengths decide before anything is decompressed, a kept side's blob goes back as
// it stands, and only the side that grew is gzipped.
func (h *History) PutContent(requestID string, c ContentSides) error {
	stored, err := h.db.Get("SELECT input, output, in_len, out_len FROM content WHERE request_id = ?", requestID)
	if err != nil {
		return fmt.Errorf("read the content of %s: %w", requestID, err)
	}
	var in, out StoredSide
	if stored == nil {
		in = freshSide(c.Input)
		out = freshSide(c.Output)
	} else {
		in = SideOfNext(StoredSide{Blob: rowBytes(stored, "input"), Len: rowInt64(stored, "in_len")}, c.Input)
		out = SideOfNext(StoredSide{Blob: rowBytes(stored, "output"), Len: rowInt64(stored, "out_len")}, c.Output)
	}
	if _, err := h.db.Run(
		"INSERT OR REPLACE INTO content (request_id, input, output, bytes, in_len, out_len) VALUES (?, ?, ?, ?, ?, ?)",
		requestID, sqlBytes(in.Blob), sqlBytes(out.Blob), derep(in.Len, 0)+derep(out.Len, 0),
		sqlInt64(in.Len), sqlInt64(out.Len)); err != nil {
		return fmt.Errorf("put the content of %s: %w", requestID, err)
	}
	return nil
}

// Content answers one request's stored sides; nil when no content row carries the id.
func (h *History) Content(requestID string) (*ContentAnswer, error) {
	row, err := h.db.Get("SELECT input, output, bytes FROM content WHERE request_id = ?", requestID)
	if err != nil {
		return nil, fmt.Errorf("read the content of %s: %w", requestID, err)
	}
	if row == nil {
		return nil, nil
	}
	input, err := ungzipSide(rowBytes(row, "input"))
	if err != nil {
		return nil, fmt.Errorf("read the input of %s: %w", requestID, err)
	}
	output, err := ungzipSide(rowBytes(row, "output"))
	if err != nil {
		return nil, fmt.Errorf("read the output of %s: %w", requestID, err)
	}
	return &ContentAnswer{Input: input, Output: output, Bytes: derep(rowInt64(row, "bytes"), 0)}, nil
}

// ungzipSide reads one stored side; a nil blob is a nil side.
func ungzipSide(blob []byte) (*string, error) {
	if blob == nil {
		return nil, nil
	}
	text, err := gunzipBytes(blob)
	if err != nil {
		return nil, err
	}
	return &text, nil
}

// The captures table.

// PutCapture stores one distinct prompt's gzip bytes, stored once per hash: the first
// request carrying it wins, and the bytes go down exactly as they came, never
// re-expanded.
func (h *History) PutCapture(c shared.CaptureRecord) error {
	blob, err := base64.StdEncoding.DecodeString(c.Gz)
	if err != nil {
		return fmt.Errorf("decode the capture %s: %w", c.Hash, err)
	}
	if _, err := h.db.Run("INSERT OR IGNORE INTO captures (hash, gz, bytes, first_ts) VALUES (?, ?, ?, ?)",
		c.Hash, blob, IsizeOf(blob), c.FirstTs); err != nil {
		return fmt.Errorf("put the capture %s: %w", c.Hash, err)
	}
	return nil
}

// Capture answers one request's captured prompt, decompressed on read; nil when the
// request, its hash or its content is not in the store (a capture the router never wrote,
// or one that failed to parse).
func (h *History) Capture(requestID string) (*shared.CaptureAnswer, error) {
	row, err := h.db.Get("SELECT prompt_hash, headers FROM requests WHERE id = ?", requestID)
	if err != nil {
		return nil, fmt.Errorf("read the request %s: %w", requestID, err)
	}
	if row == nil {
		return nil, nil
	}
	hash := rowText(row, "prompt_hash")
	if hash == nil {
		return nil, nil
	}
	stored, err := h.db.Get("SELECT gz, bytes FROM captures WHERE hash = ?", *hash)
	if err != nil {
		return nil, fmt.Errorf("read the capture %s: %w", *hash, err)
	}
	if stored == nil {
		return nil, nil
	}
	text, err := gunzipBytes(rowBytes(stored, "gz"))
	if err != nil {
		return nil, fmt.Errorf("read the capture %s: %w", *hash, err)
	}
	payload, err := jsfmt.DecodeValue([]byte(text))
	if err != nil || payload == nil || payload.Kind() != jsfmt.KindObject {
		return nil, nil
	}
	return &shared.CaptureAnswer{
		Hash:    *hash,
		System:  shared.JSON{Value: fieldOf(payload, "system")},
		Tools:   shared.JSON{Value: fieldOf(payload, "tools")},
		Bytes:   derep(rowInt64(stored, "bytes"), 0),
		Headers: headersOf(rowText(row, "headers")),
	}, nil
}

// fieldOf answers an object's field, nil when the object does not carry it.
func fieldOf(object *jsfmt.Value, key string) *jsfmt.Value {
	if object == nil {
		return nil
	}
	fields := object.Fields()
	for at, name := range object.Keys() {
		if name == key && at < len(fields) {
			return fields[at]
		}
	}
	return nil
}

// Tools, events and offsets.

// PutTool stores one tool call.
func (h *History) PutTool(nodeID string, t shared.ToolCallRecord) error {
	ok := int64(0)
	if t.Ok {
		ok = 1
	}
	if _, err := h.db.Run(
		"INSERT OR REPLACE INTO tools (id, node_id, session_id, agent_id, name, started_at, duration_ms, ok, "+
			"input_key) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		t.ID, nodeID, t.SessionID, sqlText(t.AgentID), t.Name, t.StartedAt, sqlInt64(t.DurationMs), ok,
		sqlText(t.InputKey)); err != nil {
		return fmt.Errorf("put the tool %s: %w", t.ID, err)
	}
	return nil
}

// AddEvent stores one timeline event; a duplicate seq is ignored.
func (h *History) AddEvent(nodeID *string, e shared.EventRecord) error {
	payload, err := payloadText(e.Payload)
	if err != nil {
		return err
	}
	if _, err := h.db.Run(
		"INSERT OR IGNORE INTO events (seq, ts, kind, session_id, agent_id, label, payload, node_id) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		e.Seq, e.Ts, e.Kind, sqlText(e.SessionID), sqlText(e.AgentID), sqlText(e.Label), payload,
		sqlText(nodeID)); err != nil {
		return fmt.Errorf("add the event %d: %w", e.Seq, err)
	}
	return nil
}

// payloadText renders an event's payload as the JSON the row stores: null when it says
// nothing, an order-keeping value when it already is one.
func payloadText(payload any) (string, error) {
	if payload == nil {
		return "null", nil
	}
	if value, ok := payload.(*jsfmt.Value); ok {
		return value.EncodeJSON(), nil
	}
	encoded, err := domain.MarshalCompactJSON(payload)
	if err != nil {
		return "", fmt.Errorf("encode the payload: %w", err)
	}
	return string(encoded), nil
}

// Offset answers one watched file's stored read position; nil when none is stored.
func (h *History) Offset(file string) (*FileOffset, error) {
	row, err := h.db.Get("SELECT offset, size, mtime FROM offsets WHERE file = ?", file)
	if err != nil {
		return nil, fmt.Errorf("read the offset of %s: %w", file, err)
	}
	if row == nil {
		return nil, nil
	}
	return &FileOffset{
		Offset: derep(rowInt64(row, "offset"), 0),
		Size:   derep(rowInt64(row, "size"), 0),
		Mtime:  derep(rowInt64(row, "mtime"), 0),
	}, nil
}

// SetOffset stores one watched file's read position.
func (h *History) SetOffset(file string, o FileOffset) error {
	if _, err := h.db.Run("INSERT OR REPLACE INTO offsets (file, offset, size, mtime) VALUES (?, ?, ?, ?)",
		file, o.Offset, o.Size, o.Mtime); err != nil {
		return fmt.Errorf("set the offset of %s: %w", file, err)
	}
	return nil
}

// Roots, the Unattached group, and trees.

// unattachedTrees names the trees of the jobs no node claims: a job is top-level exactly
// when nothing holds its parent row — no origin session was ever written, or its row is
// gone. The history view shows them all as the one Unattached group (a job whose origin
// session exists reads inside that session's tree instead).
const unattachedTrees = "(SELECT id FROM nodes j WHERE j.kind = 'job' AND NOT EXISTS (SELECT 1 FROM nodes p WHERE p.id = j.parent_id))"

// liveSQL names when a node counts as live: it moved within the window and nothing ended
// it; a tree is live when any node is.
const liveSQL = "last_at IS NOT NULL AND last_at >= ? AND ended_at IS NULL"

// defaultLiveMs is the live window a tree read uses when the caller gives no clock.
const defaultLiveMs = 5 * 60_000

// likeOf wraps a search term for LIKE, escaping the wildcards.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func likeOf(term string) string {
	return "%" + likeEscaper.Replace(term) + "%"
}

// rootsWhere builds the WHERE of a roots read: top-level main sessions only — live ones
// with a live node, in history the ended ones. Jobs are never roots: they read through
// their session's tree, and the ones no session claims come back as the one Unattached
// group from Unattached.
func rootsWhere(q RootsQuery) (string, []SqlValue) {
	clauses := []string{"n.parent_id IS NULL", "n.kind = 'main'"}
	params := []SqlValue{}
	at := "(SELECT 1 FROM nodes c WHERE c.root_id = n.id AND " + liveSQL + ")"
	if q.Scope == ScopeLive {
		clauses = append(clauses, "EXISTS "+at)
	} else {
		clauses = append(clauses, "NOT EXISTS "+at)
	}
	params = append(params, q.Now-q.LiveMs)
	if q.Scope == ScopeHistory && len(q.ExcludeSessions) > 0 {
		clauses = append(clauses, "n.session_id NOT IN ("+marks(len(q.ExcludeSessions))+")")
		for _, session := range q.ExcludeSessions {
			params = append(params, session)
		}
	}
	if q.Before != nil {
		clauses = append(clauses, "n.last_at < ?")
		params = append(params, *q.Before)
	}
	if q.Repo != nil {
		clauses = append(clauses, "n.repo = ?")
		params = append(params, *q.Repo)
	}
	if q.Search != nil {
		clauses = append(clauses,
			"(n.project LIKE ? ESCAPE '\\' OR n.label LIKE ? ESCAPE '\\' OR n.model LIKE ? ESCAPE '\\' "+
				"OR n.cwd LIKE ? ESCAPE '\\' OR n.name LIKE ? ESCAPE '\\')")
		pattern := likeOf(*q.Search)
		for range 5 {
			params = append(params, pattern)
		}
	}
	return strings.Join(clauses, " AND "), params
}

// marks answers n positional parameter marks, comma-separated.
func marks(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Roots answers one page of top-level main sessions, each with the roll-ups over its
// whole tree.
func (h *History) Roots(q RootsQuery) ([]shared.RootSummary, error) {
	where, params := rootsWhere(q)
	rows, err := h.db.All("SELECT "+nodeColumns+" FROM nodes n WHERE "+where+" ORDER BY last_at DESC LIMIT ?",
		append(params, q.Limit)...)
	if err != nil {
		return nil, fmt.Errorf("read the roots: %w", err)
	}
	nodes := make([]nodeRow, 0, len(rows))
	for _, row := range rows {
		nodes = append(nodes, nodeRowOf(row))
	}
	rollups, err := h.rollupRoots(nodes, q.Scope, q.Now, q.LiveMs)
	if err != nil {
		return nil, err
	}
	out := make([]shared.RootSummary, 0, len(nodes))
	for _, row := range nodes {
		rollup := rollups[row.id]
		summary := shared.RootSummary{NodeFields: nodeFieldsOf(row)}
		summary.LastAt = rollup.lastAt
		summary.Requests = rollup.requests
		summary.Tokens = rollup.tokens
		summary.Nodes = rollup.nodes
		summary.LiveNodes = rollup.liveNodes
		if rollup.activity.BucketMs == 0 {
			summary.Activity = store.EmptyActivity(activityWindowOf(row, q.Scope, q.Now, q.LiveMs).bucketMs)
		} else {
			summary.Activity = rollup.activity
		}
		out = append(out, summary)
	}
	return out, nil
}

// rootRollup is one root's roll-ups over its whole tree.
type rootRollup struct {
	lastAt    int64
	requests  int64
	tokens    shared.Tokens
	nodes     int64
	liveNodes int64
	activity  shared.Activity
}

// requestTotals is one tree's or node's request roll-up as SQL grouped it.
type requestTotals struct {
	nodeID           string
	requestCount     *int64
	inputTokens      *int64
	outputTokens     *int64
	cacheReadTokens  *int64
	cacheWriteTokens *int64
}

func requestTotalsOf(row map[string]SqlValue) requestTotals {
	return requestTotals{
		nodeID:           rowString(row, "node_id"),
		requestCount:     rowInt64(row, "requestCount"),
		inputTokens:      rowInt64(row, "inputTokens"),
		outputTokens:     rowInt64(row, "outputTokens"),
		cacheReadTokens:  rowInt64(row, "cacheReadTokens"),
		cacheWriteTokens: rowInt64(row, "cacheWriteTokens"),
	}
}

// count answers the roll-up's request count, zero when there is no roll-up.
func (t *requestTotals) count() int64 {
	if t == nil {
		return 0
	}
	return derep(t.requestCount, 0)
}

// tokens answers a roll-up's token counts, zeroed when SQL said nothing.
func (t *requestTotals) tokens() shared.Tokens {
	if t == nil {
		return shared.Tokens{}
	}
	return shared.Tokens{
		Input:      derep(t.inputTokens, 0),
		Output:     derep(t.outputTokens, 0),
		CacheRead:  derep(t.cacheReadTokens, 0),
		CacheWrite: derep(t.cacheWriteTokens, 0),
	}
}

// rollupChunk is how many roots one batch of roll-up queries covers: the three shapes a
// per-root read ran — the tree's nodes, its requests, its activity — each grouped over an
// IN list, so a page costs three queries per 400 roots. Each root's activity window rides
// in a VALUES table its requests join.
const rollupChunk = 400

func (h *History) rollupRoots(rows []nodeRow, scope Scope, now, liveMs int64) (map[string]rootRollup, error) {
	rollups := make(map[string]rootRollup, len(rows))
	for at := 0; at < len(rows); at += rollupChunk {
		end := at + rollupChunk
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[at:end]
		ids := make([]SqlValue, 0, len(chunk))
		for _, row := range chunk {
			ids = append(ids, row.id)
		}
		list := marks(len(chunk))
		nodeRows, err := h.db.All(
			"SELECT root_id AS rootId, COUNT(*) AS nodes, SUM(CASE WHEN "+liveSQL+" THEN 1 ELSE 0 END) AS liveNodes, "+
				"MAX(last_at) AS lastAt FROM nodes WHERE root_id IN ("+list+") GROUP BY root_id",
			// the CASE's parameter sits in the SELECT, ahead of the IN list's in the SQL text
			append([]SqlValue{now - liveMs}, ids...)...)
		if err != nil {
			return nil, fmt.Errorf("roll the nodes up: %w", err)
		}
		nodes := make(map[string]map[string]SqlValue, len(nodeRows))
		for _, row := range nodeRows {
			nodes[rowString(row, "rootId")] = row
		}
		requestRows, err := h.db.All(
			"SELECT n.root_id AS rootId, COUNT(*) AS requestCount, SUM(r.input) AS inputTokens, "+
				"SUM(r.output) AS outputTokens, SUM(r.cache_read) AS cacheReadTokens, "+
				"SUM(r.cache_write) AS cacheWriteTokens FROM requests r JOIN nodes n ON n.id = r.node_id "+
				"WHERE n.root_id IN ("+list+") GROUP BY n.root_id", ids...)
		if err != nil {
			return nil, fmt.Errorf("roll the requests up: %w", err)
		}
		requests := make(map[string]*requestTotals, len(requestRows))
		for _, row := range requestRows {
			totals := requestTotalsOf(row)
			requests[rowString(row, "rootId")] = &totals
		}
		windows := make([]activityWindow, len(chunk))
		values := make([]string, 0, len(chunk))
		params := make([]SqlValue, 0, len(chunk)*3)
		for index, row := range chunk {
			windows[index] = activityWindowOf(row, scope, now, liveMs)
			values = append(values, "(?, ?, ?)")
			params = append(params, row.id, windows[index].start, windows[index].bucketMs)
		}
		bucketRows, err := h.db.All(
			"WITH w(root, start, bucket) AS (VALUES "+strings.Join(values, ", ")+") "+
				"SELECT w.root AS rootId, (r.ts - w.start) / w.bucket AS bucket, r.model, COUNT(*) AS c "+
				"FROM requests r JOIN nodes n ON n.id = r.node_id JOIN w ON n.root_id = w.root "+
				"WHERE r.ts >= w.start GROUP BY w.root, bucket, r.model", params...)
		if err != nil {
			return nil, fmt.Errorf("roll the activity up: %w", err)
		}
		buckets := make(map[string][]store.ActivityRow)
		for _, row := range bucketRows {
			rootID := rowString(row, "rootId")
			buckets[rootID] = append(buckets[rootID], store.ActivityRow{
				Bucket: derep(rowInt64(row, "bucket"), 0),
				Model:  rowText(row, "model"),
				C:      derep(rowInt64(row, "c"), 0),
			})
		}
		for index, row := range chunk {
			nodesRow := nodes[row.id]
			requestsRow := requests[row.id]
			rollups[row.id] = rootRollup{
				lastAt:    derep(rowInt64(nodesRow, "lastAt"), 0),
				requests:  requestsRow.count(),
				tokens:    requestsRow.tokens(),
				nodes:     derep(rowInt64(nodesRow, "nodes"), 0),
				liveNodes: derep(rowInt64(nodesRow, "liveNodes"), 0),
				activity:  store.ActivityFromRows(buckets[row.id], windows[index].bucketMs),
			}
		}
	}
	return rollups, nil
}

// Unattached answers the one Unattached group — every job no session claims, rolled up
// together — or nil when there is none. Its id is the unattached pseudo session, so the
// rail groups it on its own and its tree, requests and costs read through the same id.
func (h *History) Unattached(q UnattachedQuery) (*shared.RootSummary, error) {
	trees, err := h.db.All(
		"SELECT id, started_at FROM nodes j WHERE j.kind = 'job' " +
			"AND NOT EXISTS (SELECT 1 FROM nodes p WHERE p.id = j.parent_id)")
	if err != nil {
		return nil, fmt.Errorf("read the unattached trees: %w", err)
	}
	if len(trees) == 0 {
		return nil, nil
	}
	ids := make([]SqlValue, 0, len(trees))
	for _, tree := range trees {
		ids = append(ids, rowString(tree, "id"))
	}
	list := marks(len(ids))
	nodes, err := h.db.Get(
		"SELECT COUNT(*) AS nodes, SUM(CASE WHEN "+liveSQL+" THEN 1 ELSE 0 END) AS liveNodes, "+
			"MAX(last_at) AS lastAt, MIN(started_at) AS startedAt, MAX(ended_at) AS endedAt "+
			"FROM nodes WHERE root_id IN ("+list+")", append([]SqlValue{q.Now - q.LiveMs}, ids...)...)
	if err != nil {
		return nil, fmt.Errorf("roll the group's nodes up: %w", err)
	}
	requests, err := h.db.Get(
		"SELECT COUNT(*) AS requestCount, SUM(input) AS inputTokens, SUM(output) AS outputTokens, "+
			"SUM(cache_read) AS cacheReadTokens, SUM(cache_write) AS cacheWriteTokens FROM requests "+
			"WHERE node_id IN (SELECT id FROM nodes WHERE root_id IN ("+list+"))", ids...)
	if err != nil {
		return nil, fmt.Errorf("roll the group's requests up: %w", err)
	}
	// the group's span stretches from its earliest start to its last activity, as an ended
	// root's does
	startedAt := rowInt64(nodes, "startedAt")
	lastAt := derep(rowInt64(nodes, "lastAt"), 0)
	start := lastAt - q.LiveMs
	if start < 0 {
		start = 0
	}
	if startedAt != nil {
		start = *startedAt
	}
	bucketMs := ceilDiv(lastAt-start, shared.ActivityBuckets)
	if bucketMs < 1 {
		bucketMs = 1
	}
	bucketRows, err := h.db.All(
		"SELECT (r.ts - ?) / ? AS bucket, r.model, COUNT(*) AS c FROM requests r "+
			"WHERE r.node_id IN (SELECT id FROM nodes WHERE root_id IN ("+list+")) AND r.ts >= ? "+
			"GROUP BY bucket, r.model", append([]SqlValue{start, bucketMs}, append(ids, start)...)...)
	if err != nil {
		return nil, fmt.Errorf("roll the group's activity up: %w", err)
	}
	buckets := make([]store.ActivityRow, 0, len(bucketRows))
	for _, row := range bucketRows {
		buckets = append(buckets, store.ActivityRow{
			Bucket: derep(rowInt64(row, "bucket"), 0),
			Model:  rowText(row, "model"),
			C:      derep(rowInt64(row, "c"), 0),
		})
	}
	name := shared.UnattachedName
	groupRequests := requestTotalsOf(requests)
	return &shared.RootSummary{
		NodeFields: shared.NodeFields{
			ID:        shared.OutsideSession,
			Kind:      shared.NodeJob,
			SessionID: shared.OutsideSession,
			Name:      &name,
			StartedAt: startedAt,
			EndedAt:   rowInt64(nodes, "endedAt"),
			LastAt:    lastAt,
		},
		Requests:  derep(groupRequests.requestCount, 0),
		Tokens:    groupRequests.tokens(),
		Nodes:     derep(rowInt64(nodes, "nodes"), 0),
		LiveNodes: derep(rowInt64(nodes, "liveNodes"), 0),
		Activity:  store.ActivityFromRows(buckets, bucketMs),
	}, nil
}

// Tree answers every node of one tree, each with its own request roll-up and live flag.
// The Unattached group's tree is the union of the job trees no session claims.
func (h *History) Tree(rootID string, q TreeQuery) ([]shared.TreeNodeRow, error) {
	now := h.now().UnixMilli()
	if q.Now != nil {
		now = *q.Now
	}
	liveMs := int64(defaultLiveMs)
	if q.LiveMs != nil {
		liveMs = *q.LiveMs
	}
	unattached := ""
	if rootID == shared.OutsideSession {
		unattached = " OR root_id IN " + unattachedTrees
	}
	rows, err := h.db.All(
		"SELECT "+nodeColumns+", CASE WHEN "+liveSQL+" THEN 1 ELSE 0 END AS live FROM nodes "+
			"WHERE (root_id = ?"+unattached+") "+
			"ORDER BY (id = ?) DESC, live DESC, last_at DESC, id DESC",
		now-liveMs, rootID, rootID)
	if err != nil {
		return nil, fmt.Errorf("read the tree of %s: %w", rootID, err)
	}
	totalsRows, err := h.db.All(
		"SELECT node_id, COUNT(*) AS requestCount, SUM(input) AS inputTokens, SUM(output) AS outputTokens, "+
			"SUM(cache_read) AS cacheReadTokens, SUM(cache_write) AS cacheWriteTokens FROM requests "+
			"WHERE node_id IN (SELECT id FROM nodes WHERE root_id = ?"+unattached+") GROUP BY node_id", rootID)
	if err != nil {
		return nil, fmt.Errorf("roll the tree's requests up: %w", err)
	}
	totals := make(map[string]*requestTotals, len(totalsRows))
	for _, row := range totalsRows {
		parsed := requestTotalsOf(row)
		totals[parsed.nodeID] = &parsed
	}
	out := make([]shared.TreeNodeRow, 0, len(rows))
	for _, row := range rows {
		node := nodeRowOf(row)
		t := totals[node.id]
		out = append(out, shared.TreeNodeRow{
			NodeFields: nodeFieldsOf(node),
			Live:       derep(rowInt64(row, "live"), 0) == 1,
			Requests:   t.count(),
			Tokens:     t.tokens(),
		})
	}
	return out, nil
}

// activityWindow is one tree's activity window: a live one counts its requests in liveMs
// buckets ending now; an ended one stretches its whole span from start to last activity
// (a span nobody recorded falls back to the live window).
type activityWindow struct {
	start    int64
	bucketMs int64
}

func activityWindowOf(row nodeRow, scope Scope, now, liveMs int64) activityWindow {
	lastAt := derep(row.lastAt, 0)
	span := liveMs
	start := now - liveMs
	if scope != ScopeLive {
		start = derep(row.startedAt, max64(0, lastAt-liveMs))
		span = lastAt - start
	}
	bucketMs := ceilDiv(span, shared.ActivityBuckets)
	if bucketMs < 1 {
		bucketMs = 1
	}
	return activityWindow{start: start, bucketMs: bucketMs}
}

// servedName keeps a stored name that reads as an absolute path from showing as one: the
// card is named by the repo's own name ("app"), never by a path — the full cwd stays on
// the node for the tooltip.
func servedName(name *string) *string {
	if name == nil || !strings.HasPrefix(*name, "/") {
		return name
	}
	short := shared.RepoName(*name)
	return &short
}

func nodeFieldsOf(row nodeRow) shared.NodeFields {
	return shared.NodeFields{
		ID:              row.id,
		Kind:            shared.NodeKind(row.kind),
		ParentID:        row.parentID,
		SessionID:       row.sessionID,
		AgentID:         row.agentID,
		Label:           row.label,
		AgentType:       row.agentType,
		Description:     row.description,
		Project:         row.project,
		Cwd:             row.cwd,
		Model:           row.model,
		Provider:        row.provider,
		ToolUseID:       row.toolUseID,
		SpawnDepth:      row.spawnDepth,
		JobState:        row.jobState,
		Repo:            row.repo,
		Branch:          row.branch,
		Name:            servedName(row.name),
		ParentSessionID: row.parentSessionID,
		StartedAt:       row.startedAt,
		EndedAt:         row.endedAt,
		LastAt:          derep(row.lastAt, 0),
	}
}

// RequestsOf answers one page of requests, newest first: by node, by tree (the
// Unattached group's requests are the ones its job trees hold), or the whole table.
func (h *History) RequestsOf(q RequestsQuery) ([]shared.RequestRecord, error) {
	clauses := []string{}
	params := []SqlValue{}
	if q.NodeID != nil {
		clauses = append(clauses, "node_id = ?")
		params = append(params, *q.NodeID)
	} else if q.RootID != nil {
		unattached := ""
		if *q.RootID == shared.OutsideSession {
			unattached = " OR root_id IN " + unattachedTrees
		}
		clauses = append(clauses, "node_id IN (SELECT id FROM nodes WHERE root_id = ?"+unattached+")")
		params = append(params, *q.RootID)
	}
	if q.Before != nil {
		clauses = append(clauses, "(ts < ? OR (ts = ? AND id < ?))")
		params = append(params, q.Before.Ts, q.Before.Ts, q.Before.ID)
	}
	params = append(params, q.Limit)
	filter := ""
	if len(clauses) > 0 {
		filter = "WHERE " + strings.Join(clauses, " AND ") + " "
	}
	rows, err := h.db.All("SELECT "+requestColumns+" FROM requests "+filter+"ORDER BY ts DESC, id DESC LIMIT ?",
		params...)
	if err != nil {
		return nil, fmt.Errorf("read the requests: %w", err)
	}
	out := make([]shared.RequestRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, toRequest(requestRowOf(row)))
	}
	return out, nil
}

// The rebuilt conversation.

// requestTarget is the request a context read rebuilds around, with the fields its cost
// estimate reads.
type requestTarget struct {
	nodeID     string
	ts         int64
	model      *string
	input      *int64
	output     *int64
	cacheRead  *int64
	cacheWrite *int64
}

// sideCounts is what one aggregate over stored sides answered.
type sideCounts struct {
	messages int64
	bytes    int64
}

// contextTotals answers the header numbers: how many turns, roughly how many tokens, what
// the request itself reported. The counts come from one aggregate over the stored side
// lengths — the whole conversation weighed, none of it decompressed; the target's own
// answer never counted, for the model never saw it.
func contextTotals(target requestTarget, turns sideCounts) shared.ContextTotals {
	input := derep(target.input, 0)
	output := derep(target.output, 0)
	cacheRead := derep(target.cacheRead, 0)
	cacheWrite := derep(target.cacheWrite, 0)
	var usage *shared.ContextUsage
	if input+output+cacheRead+cacheWrite != 0 {
		usage = &shared.ContextUsage{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite}
	}
	var cacheTokens int64
	if usage != nil {
		cacheTokens = usage.CacheRead + usage.CacheWrite
	}
	return shared.ContextTotals{
		Messages:     turns.messages,
		ApproxTokens: (turns.bytes + 2) / 4, // Math.round(bytes / 4), half up the way JS rounds
		Usage:        usage,
		CacheTokens:  cacheTokens,
	}
}

// previewChars is the characters of a preview line before it is cut.
const previewChars = 200

// contextTurn is one conversation turn with the paging side it came from.
type contextTurn struct {
	message shared.ContextMessage
	side    int
}

// contextRow is one request row with its stored sides.
type contextRow struct {
	id   string
	ts   int64
	cin  []byte
	cout []byte
}

// messageKeyOf answers the page cursor of one message: its request's ts and id plus which
// side of it; ids may hold colons.
func messageKeyOf(turn contextTurn) string {
	return fmt.Sprintf("%d:%s:%d", turn.message.Ts, turn.message.RequestID, turn.side)
}

// cursor is the page cursor of one message as its parts.
type cursor struct {
	ts        int64
	requestID string
	side      int
}

// cursorOf parses "<ts>:<requestId>:<side>"; the id may hold colons.
func cursorOf(raw string) (cursor, bool) {
	first := strings.Index(raw, ":")
	last := strings.LastIndex(raw, ":")
	if first < 0 || last <= first {
		return cursor{}, false
	}
	if !onlyDigits(raw[:first]) {
		return cursor{}, false
	}
	ts, err := strconv.ParseInt(raw[:first], 10, 64)
	if err != nil {
		return cursor{}, false
	}
	side := raw[last+1:]
	if side != "0" && side != "1" {
		return cursor{}, false
	}
	return cursor{ts: ts, requestID: raw[first+1 : last], side: int(side[0] - '0')}, true
}

// onlyDigits reports whether s is one or more ASCII digits.
func onlyDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cursorParts splits a transcript cursor "<ts>:<requestId>" at its first and last colon;
// the id itself may hold colons.
func cursorParts(raw string) (RequestCursor, bool) {
	first := strings.Index(raw, ":")
	last := strings.LastIndex(raw, ":")
	if first < 0 || last <= first {
		return RequestCursor{}, false
	}
	ts, err := strconv.ParseFloat(raw[:first], 64)
	if err != nil || math.IsNaN(ts) || math.IsInf(ts, 0) {
		return RequestCursor{}, false
	}
	return RequestCursor{Ts: int64(ts), ID: raw[first+1 : last]}, true
}

// contextPage is one page of messages plus the cursor of the page before it.
type contextPage struct {
	messages []shared.ContextMessage
	next     *string
}

// emptyPage is the page a cursor to nothing always answered.
func emptyPage() contextPage {
	return contextPage{messages: []shared.ContextMessage{}}
}

// sideBlocks reads a stored side as the blocks it carries: a JSON array as it stands,
// plain text as one text block.
func sideBlocks(text *string) []shared.JSON {
	if text == nil || *text == "" {
		return nil
	}
	if value, err := jsfmt.DecodeValue([]byte(*text)); err == nil && value.Kind() == jsfmt.KindArray {
		items := value.Items()
		blocks := make([]shared.JSON, 0, len(items))
		for _, item := range items {
			blocks = append(blocks, shared.JSON{Value: item})
		}
		return blocks
	}
	// a side that never was JSON is one text block
	return []shared.JSON{{Value: jsfmt.Object(
		[]string{"type", "text"},
		[]*jsfmt.Value{jsfmt.String("text"), jsfmt.String(*text)},
	)}}
}

// fieldText answers an object field's text, empty unless the field is a string.
func fieldText(object *jsfmt.Value, key string) string {
	if field := fieldOf(object, key); field != nil && field.Kind() == jsfmt.KindString {
		return field.Text()
	}
	return ""
}

// kindOf names what the first block is made of, for the collapsed row's kind.
func kindOf(blocks []shared.JSON) string {
	if len(blocks) == 0 || blocks[0].Value == nil || blocks[0].Value.Kind() != jsfmt.KindObject {
		return "unknown"
	}
	if kind := fieldText(blocks[0].Value, "type"); kind != "" {
		return kind
	}
	return "unknown"
}

// previewOf reads the first line of the first block that carries text, cut for a
// collapsed row; the cut counts UTF-16 units, the way the dashboard's own copy does.
func previewOf(blocks []shared.JSON) string {
	for _, block := range blocks {
		if block.Value == nil || block.Value.Kind() != jsfmt.KindObject {
			continue
		}
		text := fieldText(block.Value, "text")
		if text == "" {
			continue
		}
		line, _, _ := strings.Cut(text, "\n")
		if jsfmt.UTF16Len(line) > previewChars {
			return jsfmt.UTF16Slice(line, 0, previewChars) + "…"
		}
		return line
	}
	return ""
}

// turnOf answers one stored side as a turn, nil when the side holds nothing.
func turnOf(role string, row contextRow, side int, text *string) *contextTurn {
	blocks := sideBlocks(text)
	if len(blocks) == 0 {
		return nil
	}
	return &contextTurn{
		message: shared.ContextMessage{
			Role:      role,
			Kind:      kindOf(blocks),
			RequestID: row.id,
			Ts:        row.ts,
			Bytes:     int64(len(*text)),
			Preview:   previewOf(blocks),
			Blocks:    blocks,
		},
		side: side,
	}
}

// pushUserTurn adds the row's user-side turn, when the side holds one.
func (h *History) pushUserTurn(turns *[]contextTurn, row contextRow) error {
	text, err := ungzipSide(row.cin)
	if err != nil {
		return fmt.Errorf("read the input of %s: %w", row.id, err)
	}
	if user := turnOf("user", row, 0, text); user != nil {
		*turns = append(*turns, *user)
	}
	return nil
}

// pushAnswerTurn adds the row's answer turn, when the side holds one.
func (h *History) pushAnswerTurn(turns *[]contextTurn, row contextRow) error {
	text, err := ungzipSide(row.cout)
	if err != nil {
		return fmt.Errorf("read the output of %s: %w", row.id, err)
	}
	if answer := turnOf("assistant", row, 1, text); answer != nil {
		*turns = append(*turns, *answer)
	}
	return nil
}

// turnsOfRow answers one row's turns, newest first: the answer (unless the row is the
// target — its own answer came after the input the model saw), then the input.
func (h *History) turnsOfRow(requestID string, row contextRow) ([]contextTurn, error) {
	turns := []contextTurn{}
	if row.id != requestID {
		if err := h.pushAnswerTurn(&turns, row); err != nil {
			return nil, err
		}
	}
	if err := h.pushUserTurn(&turns, row); err != nil {
		return nil, err
	}
	return turns, nil
}

// rowTurns answers what one row hands a paged walk: its turns — the side before the
// cursor's turn alone on the cursor's own row — or ok false when the cursor names a side
// the row does not carry.
func (h *History) rowTurns(requestID string, row contextRow, at *cursor) ([]contextTurn, bool, error) {
	if at != nil && row.id == at.requestID && row.ts == at.ts {
		held := row.cin
		if at.side == 1 {
			held = row.cout
		}
		if held == nil {
			return nil, false, nil
		}
		turns := []contextTurn{}
		if at.side == 1 {
			if err := h.pushUserTurn(&turns, row); err != nil {
				return nil, false, err
			}
		}
		return turns, true, nil
	}
	turns, err := h.turnsOfRow(requestID, row)
	return turns, true, err
}

// pageTurns reads the rows as turns, newest first, capped one past the page; ok false
// when the cursor names a side a row lacks.
func (h *History) pageTurns(requestID string, rows []contextRow, at *cursor, limit int64) ([]contextTurn, bool, error) {
	turns := []contextTurn{}
	for _, row := range rows {
		next, ok, err := h.rowTurns(requestID, row, at)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			return nil, false, nil // the cursor names a side the row lacks
		}
		turns = append(turns, next...)
		if int64(len(turns)) > limit {
			break // the page and the next-older turn are in hand
		}
	}
	return turns, true, nil
}

// pageAnswerOf answers the page and its cursor: the newest `limit` turns, oldest first;
// next names the page's own oldest turn, and the page before it is every turn older than
// that.
func pageAnswerOf(turns []contextTurn, limit int64) contextPage {
	end := int(min64(limit, int64(len(turns))))
	page := append([]contextTurn{}, turns[:end]...)
	var next *string
	if int64(len(turns)) > limit && len(page) > 0 {
		key := messageKeyOf(page[len(page)-1]) // the page's oldest turn, before the reverse
		next = &key
	}
	for at, mid := 0, len(page)/2; at < mid; at++ {
		page[at], page[len(page)-1-at] = page[len(page)-1-at], page[at]
	}
	messages := make([]shared.ContextMessage, 0, len(page))
	for _, turn := range page {
		messages = append(messages, turn.message)
	}
	return contextPage{messages: messages, next: next}
}

// contextRows reads request rows with their stored sides.
func (h *History) contextRows(tail string, params ...SqlValue) ([]contextRow, error) {
	rows, err := h.db.All(
		"SELECT r.id, r.ts, c.input AS cin, c.output AS cout FROM requests r "+
			"LEFT JOIN content c ON c.request_id = r.id "+tail, params...)
	if err != nil {
		return nil, fmt.Errorf("read the conversation rows: %w", err)
	}
	out := make([]contextRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, contextRow{
			id:   rowString(row, "id"),
			ts:   derep(rowInt64(row, "ts"), 0),
			cin:  rowBytes(row, "cin"),
			cout: rowBytes(row, "cout"),
		})
	}
	return out, nil
}

// contextPageOf reads one page of the conversation backwards from the target (or from
// the cursor's row) with a LIMIT, so only the rows the page shows are ever decompressed:
// each fetched row yields its answer then its input, newest first, until `limit` turns
// and the one older turn `next` names are in hand — the walk fetches `limit + 2` rows,
// and a row yields two turns at most, so that always fits. A cursor this node never named
// (or one naming a side the row does not carry) answers an empty page, as a cursor to
// nothing always has.
func (h *History) contextPageOf(requestID string, target requestTarget, q ContextQuery) (contextPage, error) {
	limit := max64(q.Limit, 0)
	var at *cursor
	if q.Cursor != nil {
		parsed, ok := cursorOf(*q.Cursor)
		if !ok {
			return emptyPage(), nil
		}
		at = &parsed
	}
	bound := RequestCursor{Ts: target.ts, ID: requestID}
	if at != nil {
		bound = RequestCursor{Ts: at.ts, ID: at.requestID}
	}
	rows, err := h.contextRows(
		"WHERE r.node_id = ? AND (r.ts < ? OR (r.ts = ? AND r.id <= ?)) ORDER BY r.ts DESC, r.id DESC LIMIT ?",
		target.nodeID, bound.Ts, bound.Ts, bound.ID, limit+2)
	if err != nil {
		return contextPage{}, err
	}
	// a cursor this node never named — one of another node's rows, say — answers the
	// empty page
	if at != nil && (len(rows) == 0 || rows[0].id != at.requestID || rows[0].ts != at.ts) {
		return emptyPage(), nil
	}
	turns, ok, err := h.pageTurns(requestID, rows, at, limit)
	if err != nil {
		return contextPage{}, err
	}
	if !ok {
		return emptyPage(), nil
	}
	return pageAnswerOf(turns, limit), nil
}

// Context answers one page of the conversation a request sat in, rebuilt from the stored
// sides of its node's requests, paged from the newest message backwards; nil when no
// request row carries the id.
func (h *History) Context(requestID string, q ContextQuery) (*shared.ContextAnswer, error) {
	row, err := h.db.Get(
		"SELECT node_id, ts, model, input, output, cache_read, cache_write FROM requests WHERE id = ?", requestID)
	if err != nil {
		return nil, fmt.Errorf("read the request %s: %w", requestID, err)
	}
	if row == nil {
		return nil, nil
	}
	target := requestTarget{
		nodeID:     rowString(row, "node_id"),
		ts:         derep(rowInt64(row, "ts"), 0),
		model:      rowText(row, "model"),
		input:      rowInt64(row, "input"),
		output:     rowInt64(row, "output"),
		cacheRead:  rowInt64(row, "cache_read"),
		cacheWrite: rowInt64(row, "cache_write"),
	}
	counted, err := h.db.Get(
		"SELECT COALESCE(SUM(CASE WHEN c.input IS NOT NULL THEN 1 ELSE 0 END + "+
			"CASE WHEN c.output IS NOT NULL AND r.id != ? THEN 1 ELSE 0 END), 0) AS messages, "+
			"COALESCE(SUM(CASE WHEN r.id = ? THEN COALESCE(c.in_len, 0) "+
			"ELSE COALESCE(c.in_len, 0) + COALESCE(c.out_len, 0) END), 0) AS bytes "+
			"FROM requests r LEFT JOIN content c ON c.request_id = r.id "+
			"WHERE r.node_id = ? AND (r.ts < ? OR (r.ts = ? AND r.id <= ?))",
		requestID, requestID, target.nodeID, target.ts, target.ts, requestID)
	if err != nil {
		return nil, fmt.Errorf("weigh the conversation of %s: %w", requestID, err)
	}
	page, err := h.contextPageOf(requestID, target, q)
	if err != nil {
		return nil, err
	}
	return &shared.ContextAnswer{
		RequestID: requestID,
		NodeID:    target.nodeID,
		Model:     derep(target.model, ""),
		Totals: contextTotals(target, sideCounts{
			messages: derep(rowInt64(counted, "messages"), 0),
			bytes:    derep(rowInt64(counted, "bytes"), 0),
		}),
		Messages: page.messages,
		Next:     page.next,
	}, nil
}

// AgentTranscript answers one page of an agent's whole conversation — every stored turn
// of its requests, the newest answer included — paged the same way; nil when no node
// carries the id, an empty conversation when the node holds no stored requests (an agent
// that never ran, not an unknown one).
func (h *History) AgentTranscript(nodeID string, q ContextQuery) (*shared.ContextAnswer, error) {
	node, err := h.db.Get("SELECT model FROM nodes WHERE id = ?", nodeID)
	if err != nil {
		return nil, fmt.Errorf("read the node %s: %w", nodeID, err)
	}
	if node == nil {
		return nil, nil
	}
	row, err := h.db.Get(
		"SELECT id, ts, model, input, output, cache_read, cache_write FROM requests "+
			"WHERE node_id = ? ORDER BY ts DESC, id DESC LIMIT 1", nodeID)
	if err != nil {
		return nil, fmt.Errorf("read the newest request of %s: %w", nodeID, err)
	}
	if row == nil {
		// the node exists but nothing was stored under it: an empty conversation, not an
		// unknown agent
		return &shared.ContextAnswer{
			RequestID: "",
			NodeID:    nodeID,
			Model:     derep(rowText(node, "model"), ""),
			Messages:  []shared.ContextMessage{},
		}, nil
	}
	page, err := h.transcriptPage(nodeID, q)
	if err != nil {
		return nil, err
	}
	// the totals count every stored side in SQL, never by unzipping the whole conversation
	sides, err := h.db.Get(
		"SELECT COUNT(c.input) + COUNT(c.output) AS messages, COALESCE(SUM(c.bytes), 0) AS bytes "+
			"FROM requests r JOIN content c ON c.request_id = r.id WHERE r.node_id = ?", nodeID)
	if err != nil {
		return nil, fmt.Errorf("weigh the transcript of %s: %w", nodeID, err)
	}
	model := ""
	if named := rowText(row, "model"); named != nil {
		model = *named
	} else if named := rowText(node, "model"); named != nil {
		model = *named
	}
	target := requestTarget{
		nodeID:     nodeID,
		ts:         derep(rowInt64(row, "ts"), 0),
		model:      rowText(row, "model"),
		input:      rowInt64(row, "input"),
		output:     rowInt64(row, "output"),
		cacheRead:  rowInt64(row, "cache_read"),
		cacheWrite: rowInt64(row, "cache_write"),
	}
	return &shared.ContextAnswer{
		RequestID: rowString(row, "id"),
		NodeID:    nodeID,
		Model:     model,
		Totals: contextTotals(target, sideCounts{
			messages: derep(rowInt64(sides, "messages"), 0),
			bytes:    derep(rowInt64(sides, "bytes"), 0),
		}),
		Messages: page.messages,
		Next:     page.next,
	}, nil
}

// transcriptPage reads one page of the node's whole conversation, oldest first, each
// request's input and its answer — the agent's transcript, its newest answer included,
// where the context view stops at the input. Only the requests the page needs are read
// and unzipped, newest first from the cursor's request: a main agent can hold thousands,
// and a live transcript asks again on every request it makes. The window widens only when
// sides without content leave it short of a page.
func (h *History) transcriptPage(nodeID string, q ContextQuery) (contextPage, error) {
	var at *RequestCursor
	if q.Cursor != nil {
		parsed, ok := cursorParts(*q.Cursor)
		if !ok {
			return emptyPage(), nil
		}
		at = &parsed
	}
	for take := max64(q.Limit, 1) + 1; ; take *= 4 {
		rows, err := h.newestRows(nodeID, at, take)
		if err != nil {
			return contextPage{}, err
		}
		// newestRows answers newest first; the page walks oldest first
		reversed := make([]contextRow, len(rows))
		for index, row := range rows {
			reversed[len(rows)-1-index] = row
		}
		turns, err := h.turnsOfRows(reversed)
		if err != nil {
			return contextPage{}, err
		}
		page := contextPageOfTurns(turns, q)
		// a page that starts at the window's first turn is the conversation's start only
		// when the window ran out of requests; otherwise older ones may hold turns
		if page.next != nil || int64(len(rows)) < take {
			return page, nil
		}
	}
}

// newestRows answers the node's newest `take` requests with their stored sides, from the
// cursor's request back.
func (h *History) newestRows(nodeID string, at *RequestCursor, take int64) ([]contextRow, error) {
	if at == nil {
		return h.contextRows("WHERE r.node_id = ? ORDER BY r.ts DESC, r.id DESC LIMIT ?", nodeID, take)
	}
	return h.contextRows(
		"WHERE r.node_id = ? AND (r.ts < ? OR (r.ts = ? AND r.id <= ?)) ORDER BY r.ts DESC, r.id DESC LIMIT ?",
		nodeID, at.Ts, at.Ts, at.ID, take)
}

// turnsOfRows answers each request's input and answer as turns, in the rows' order; empty
// sides leave no turn.
func (h *History) turnsOfRows(rows []contextRow) ([]contextTurn, error) {
	turns := []contextTurn{}
	for _, row := range rows {
		if err := h.pushUserTurn(&turns, row); err != nil {
			return nil, err
		}
		if err := h.pushAnswerTurn(&turns, row); err != nil {
			return nil, err
		}
	}
	return turns, nil
}

// contextPageOfTurns answers the newest `limit` of the turns in hand, or those before the
// cursor; next names the page before it, nil at the start.
func contextPageOfTurns(turns []contextTurn, q ContextQuery) contextPage {
	end := len(turns)
	if q.Cursor != nil {
		at := -1
		for index, turn := range turns {
			if messageKeyOf(turn) == *q.Cursor {
				at = index
				break
			}
		}
		if at < 0 {
			return emptyPage()
		}
		end = at
	}
	limit := int(max64(q.Limit, 0))
	start := end - limit
	if start < 0 {
		start = 0
	}
	messages := make([]shared.ContextMessage, 0, end-start)
	for _, turn := range turns[start:end] {
		messages = append(messages, turn.message)
	}
	var next *string
	if start > 0 {
		key := messageKeyOf(turns[start])
		next = &key
	}
	return contextPage{messages: messages, next: next}
}

// CostRows, flow, repos and stats.

// CostRows answers every request of the named trees with the fields a cost estimate
// reads, in a few batched queries: one query per 400 trees, never one per row — the
// page's cost roll-up reads every request it names once, joined with the node and tree
// each landed on, and the route sums prices in memory.
func (h *History) CostRows(rootIDs []string) ([]CostRow, error) {
	rows := []CostRow{}
	for at := 0; at < len(rootIDs); at += rollupChunk {
		end := at + rollupChunk
		if end > len(rootIDs) {
			end = len(rootIDs)
		}
		chunk := rootIDs[at:end]
		group := ""
		containsUnattached := false
		for _, id := range chunk {
			if id == shared.OutsideSession {
				containsUnattached = true
			}
		}
		if containsUnattached {
			group = " OR n.root_id IN " + unattachedTrees
		}
		params := []SqlValue{shared.OutsideSession}
		for _, id := range chunk {
			params = append(params, id)
		}
		got, err := h.db.All(
			"SELECT n.id AS nodeId, CASE WHEN n.root_id IN "+unattachedTrees+" THEN ? ELSE n.root_id END AS rootId, "+
				"r.model, r.upstream, r.ts, r.input, r.output, "+
				"r.cache_read AS cacheRead, r.cache_write AS cacheWrite, r.cache_write_1h AS cacheWrite1h, "+
				"r.speed, r.geo, r.service_tier "+
				"FROM requests r JOIN nodes n ON n.id = r.node_id WHERE n.root_id IN ("+marks(len(chunk))+")"+group,
			params...)
		if err != nil {
			return nil, fmt.Errorf("read the cost rows: %w", err)
		}
		for _, row := range got {
			rows = append(rows, CostRow{
				NodeID:       rowString(row, "nodeId"),
				RootID:       rowString(row, "rootId"),
				Model:        rowText(row, "model"),
				Upstream:     rowText(row, "upstream"),
				Ts:           derep(rowInt64(row, "ts"), 0),
				Input:        rowInt64(row, "input"),
				Output:       rowInt64(row, "output"),
				CacheRead:    rowInt64(row, "cacheRead"),
				CacheWrite:   rowInt64(row, "cacheWrite"),
				CacheWrite1h: rowInt64(row, "cacheWrite1h"),
				Speed:        rowText(row, "speed"),
				Geo:          rowText(row, "geo"),
				ServiceTier:  rowText(row, "service_tier"),
			})
		}
	}
	return rows, nil
}

// Flow answers requests and token kinds per bucket over one window: one GROUP BY, the
// bucket the truncating division of the offset by the bar width, and the scalar MIN
// clamping the right edge (and a capped bucket count) into the last bar, so nothing is
// lost.
func (h *History) Flow(q FlowQuery) (shared.FlowSeries, error) {
	count := int(max64(q.Buckets, 0))
	requests := make([]int64, count)
	kinds := shared.FlowKinds{
		Input:      make([]int64, count),
		Output:     make([]int64, count),
		CacheRead:  make([]int64, count),
		CacheWrite: make([]int64, count),
	}
	rows, err := h.db.All(
		"SELECT MIN(CAST((ts - ?) / ? AS INTEGER), ?) AS bucket, COUNT(*) AS requests, SUM(input) AS input, "+
			"SUM(output) AS output, SUM(cache_read) AS cacheRead, SUM(cache_write) AS cacheWrite "+
			"FROM requests WHERE ts >= ? AND ts <= ? GROUP BY bucket",
		q.From, q.BucketMs, q.Buckets-1, q.From, q.To)
	if err != nil {
		return shared.FlowSeries{}, fmt.Errorf("read the flow: %w", err)
	}
	for _, row := range rows {
		at := int(derep(rowInt64(row, "bucket"), 0))
		if at < 0 || at >= count {
			continue
		}
		requests[at] = derep(rowInt64(row, "requests"), 0)
		kinds.Input[at] = derep(rowInt64(row, "input"), 0)
		kinds.Output[at] = derep(rowInt64(row, "output"), 0)
		kinds.CacheRead[at] = derep(rowInt64(row, "cacheRead"), 0)
		kinds.CacheWrite[at] = derep(rowInt64(row, "cacheWrite"), 0)
	}
	return shared.FlowSeries{From: q.From, To: q.To, BucketMs: q.BucketMs, Requests: requests, Kinds: kinds}, nil
}

// Repos answers the repos top-level trees ran in, with their root counts, most-used
// first; the picker names roots, and this lists exactly what Roots can.
func (h *History) Repos() ([]RepoRow, error) {
	rows, err := h.db.All(
		"SELECT repo, COUNT(*) AS roots FROM nodes WHERE parent_id IS NULL AND kind = 'main' AND repo IS NOT NULL " +
			"GROUP BY repo ORDER BY roots DESC, repo ASC")
	if err != nil {
		return nil, fmt.Errorf("read the repos: %w", err)
	}
	out := make([]RepoRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, RepoRow{Repo: rowString(row, "repo"), Roots: derep(rowInt64(row, "roots"), 0)})
	}
	return out, nil
}

// Stats answers the store's size, its row counts, and how many ended sessions history
// holds. The roots count runs the same predicate Roots pages by, as one COUNT: the
// badge's number is the list's, not an approximation that drifts the moment a live
// session is kept out.
func (h *History) Stats(q StatsQuery) (Stats, error) {
	pages, err := h.db.Get("PRAGMA page_count")
	if err != nil {
		return Stats{}, fmt.Errorf("read the page count: %w", err)
	}
	size, err := h.db.Get("PRAGMA page_size")
	if err != nil {
		return Stats{}, fmt.Errorf("read the page size: %w", err)
	}
	nodes, err := h.db.Get("SELECT COUNT(*) AS c FROM nodes")
	if err != nil {
		return Stats{}, fmt.Errorf("count the nodes: %w", err)
	}
	requests, err := h.db.Get("SELECT COUNT(*) AS c FROM requests")
	if err != nil {
		return Stats{}, fmt.Errorf("count the requests: %w", err)
	}
	where, params := rootsWhere(RootsQuery{
		Scope:           ScopeHistory,
		Now:             q.Now,
		LiveMs:          q.LiveMs,
		ExcludeSessions: q.ExcludeSessions,
	})
	roots, err := h.db.Get("SELECT COUNT(*) AS c FROM nodes n WHERE "+where, params...)
	if err != nil {
		return Stats{}, fmt.Errorf("count the roots: %w", err)
	}
	return Stats{
		Bytes:    derep(rowInt64(pages, "page_count"), 0) * derep(rowInt64(size, "page_size"), 0),
		Nodes:    derep(rowInt64(nodes, "c"), 0),
		Requests: derep(rowInt64(requests, "c"), 0),
		Roots:    derep(rowInt64(roots, "c"), 0),
	}, nil
}

// Prune, clear and close.

// Prune removes whole trees nothing in has touched since cutoff; a tree with one recent
// node survives. It answers how many trees went.
func (h *History) Prune(cutoff int64) (int, error) {
	stale, err := h.db.All(
		"SELECT id FROM nodes r WHERE r.parent_id IS NULL AND r.last_at IS NOT NULL AND r.last_at < ? "+
			"AND NOT EXISTS (SELECT 1 FROM nodes c WHERE c.root_id = r.id AND (c.last_at IS NULL OR c.last_at >= ?))",
		cutoff, cutoff)
	if err != nil {
		return 0, fmt.Errorf("read the stale trees: %w", err)
	}
	for _, root := range stale {
		id := rowString(root, "id")
		// content goes with its request row, through ON DELETE CASCADE
		for _, statement := range []string{
			"DELETE FROM events WHERE node_id IN (SELECT id FROM nodes WHERE root_id = ?)",
			"DELETE FROM tools WHERE node_id IN (SELECT id FROM nodes WHERE root_id = ?)",
			"DELETE FROM requests WHERE node_id IN (SELECT id FROM nodes WHERE root_id = ?)",
			"DELETE FROM nodes WHERE root_id = ?",
		} {
			if _, err := h.db.Run(statement, id); err != nil {
				return 0, fmt.Errorf("prune the tree of %s: %w", id, err)
			}
		}
	}
	return len(stale), nil
}

// Clear empties every table but meta.
func (h *History) Clear() error {
	for _, table := range []string{"content", "requests", "tools", "events", "offsets", "nodes", "captures"} {
		if _, err := h.db.Run("DELETE FROM " + table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (h *History) Close() error {
	if err := h.db.Close(); err != nil {
		return fmt.Errorf("close the history: %w", err)
	}
	return nil
}

// Row and parameter helpers.

// rowString reads one column as a string, empty when the column is null.
func rowString(row map[string]SqlValue, key string) string {
	if value, ok := row[key].(string); ok {
		return value
	}
	return ""
}

// rowText reads one column as a nullable string.
func rowText(row map[string]SqlValue, key string) *string {
	if row[key] == nil {
		return nil
	}
	text, _ := row[key].(string)
	return &text
}

// rowInt64 reads one column as a nullable integer.
func rowInt64(row map[string]SqlValue, key string) *int64 {
	switch value := row[key].(type) {
	case nil:
		return nil
	case int64:
		return &value
	case float64:
		parsed := int64(value)
		return &parsed
	case int:
		parsed := int64(value)
		return &parsed
	}
	parsed := int64(0)
	return &parsed
}

// rowBytes reads one column as a blob.
func rowBytes(row map[string]SqlValue, key string) []byte {
	blob, _ := row[key].([]byte)
	return blob
}

// rowFloat64 reads one column as a nullable float.
func rowFloat64(row map[string]SqlValue, key string) *float64 {
	switch value := row[key].(type) {
	case nil:
		return nil
	case float64:
		return &value
	case int64:
		parsed := float64(value)
		return &parsed
	}
	parsed := float64(0)
	return &parsed
}

// sqlText and its kin bind a nullable value as a parameter.
func sqlText(v *string) SqlValue {
	if v == nil {
		return nil
	}
	return *v
}

// sqlFloat64 binds a nullable float as a parameter.
func sqlFloat64(v *float64) SqlValue {
	if v == nil {
		return nil
	}
	return *v
}

func sqlInt64(v *int64) SqlValue {
	if v == nil {
		return nil
	}
	return *v
}

func sqlBytes(v []byte) SqlValue {
	if v == nil {
		return nil
	}
	return v
}

// derep answers the value a pointer holds, or the fallback when it is nil.
func derep[T any](v *T, fallback T) T {
	if v == nil {
		return fallback
	}
	return *v
}

// sameText answers whether two nullable strings name the same value.
func sameText(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// pickText answers the merge rule for one string field: a non-nil incoming value wins,
// nil keeps the stored one.
func pickText(incoming, stored *string) *string {
	if incoming != nil {
		return incoming
	}
	return stored
}

// pickInt64 answers the merge rule for one integer field.
func pickInt64(incoming, stored *int64) *int64 {
	if incoming != nil {
		return incoming
	}
	return stored
}

// minOfInt answers the minimum of two nullable integers; a nil side yields the other.
func minOfInt(a, b *int64) *int64 {
	if b == nil {
		return a
	}
	if a == nil {
		return b
	}
	if *b < *a {
		return b
	}
	return a
}

// maxOfInt answers the maximum of two nullable integers; a nil side yields the other.
func maxOfInt(a, b *int64) *int64 {
	if b == nil {
		return a
	}
	if a == nil {
		return b
	}
	if *b > *a {
		return b
	}
	return a
}

func max64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

func min64(a, b int64) int64 {
	if b < a {
		return b
	}
	return a
}

// ceilDiv divides a non-negative span by d rounding up.
func ceilDiv(span, d int64) int64 {
	return (span + d - 1) / d
}
