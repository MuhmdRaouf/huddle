package history

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The reads a store warms from at start. They page through the file, so a load holds one
// page of rows at a time, and they only read: the file belongs to the writer.

// warmPage is how many rows one warm read pulls at once.
const warmPage = 1_000

var _ store.WarmSource = (*History)(nil)

// WarmRequests calls fn with the newest requests first, at most limit of them, until fn
// returns an error. written is the row's position in the file: a row keeps it when it is
// rewritten, so it follows the order the writer first stored the calls in.
func (h *History) WarmRequests(limit int, fn func(record shared.RequestRecord, written int64) error) error {
	var before *RequestCursor
	for limit > 0 {
		page := min(limit, warmPage)
		clause, params := "", []SqlValue{}
		if before != nil {
			clause = "WHERE (ts < ? OR (ts = ? AND id < ?)) "
			params = append(params, before.Ts, before.Ts, before.ID)
		}
		params = append(params, int64(page))
		rows, err := h.db.All("SELECT rowid AS pos, "+requestColumns+" FROM requests "+clause+
			"ORDER BY ts DESC, id DESC LIMIT ?", params...)
		if err != nil {
			return fmt.Errorf("read the requests: %w", err)
		}
		for _, row := range rows {
			if err := fn(toRequest(requestRowOf(row)), derep(rowInt64(row, "pos"), 0)); err != nil {
				return err
			}
		}
		if len(rows) < page {
			return nil
		}
		last := toRequest(requestRowOf(rows[len(rows)-1]))
		before = &RequestCursor{Ts: last.Ts, ID: last.ID}
		limit -= len(rows)
	}
	return nil
}

// WarmTools calls fn with the tool calls that started within the span of the newest
// shared.MaxRecords requests, the last written first, until fn returns an error.
func (h *History) WarmTools(fn func(nodeID string, t shared.ToolCallRecord) error) error {
	horizon := int64(math.MinInt64)
	row, err := h.db.Get("SELECT ts FROM requests ORDER BY ts DESC LIMIT 1 OFFSET ?", int64(shared.MaxRecords-1))
	if err != nil {
		return fmt.Errorf("read the tool horizon: %w", err)
	}
	if row != nil {
		horizon = derep(rowInt64(row, "ts"), horizon)
	}
	before := int64(math.MaxInt64)
	for {
		rows, err := h.db.All(
			"SELECT rowid AS pos, id, node_id, session_id, agent_id, name, started_at, duration_ms, ok, input_key "+
				"FROM tools WHERE started_at >= ? AND rowid < ? ORDER BY rowid DESC LIMIT ?",
			horizon, before, int64(warmPage))
		if err != nil {
			return fmt.Errorf("read the tools: %w", err)
		}
		for _, row := range rows {
			tool := shared.ToolCallRecord{
				ID:         rowString(row, "id"),
				SessionID:  rowString(row, "session_id"),
				AgentID:    rowText(row, "agent_id"),
				Name:       rowString(row, "name"),
				StartedAt:  derep(rowInt64(row, "started_at"), 0),
				DurationMs: rowInt64(row, "duration_ms"),
				Ok:         derep(rowInt64(row, "ok"), 0) == 1,
				InputKey:   rowText(row, "input_key"),
			}
			if err := fn(rowString(row, "node_id"), tool); err != nil {
				return err
			}
			before = derep(rowInt64(row, "pos"), before)
		}
		if len(rows) < warmPage {
			return nil
		}
	}
}

// WarmEvents calls fn with the newest limit timeline events, oldest first, until fn returns
// an error. A payload reads back as the spool line it was written from.
func (h *History) WarmEvents(limit int, fn func(shared.EventRecord) error) error {
	row, err := h.db.Get("SELECT MIN(seq) AS seq FROM (SELECT seq FROM events ORDER BY seq DESC LIMIT ?)", int64(limit))
	if err != nil {
		return fmt.Errorf("read the first warm event: %w", err)
	}
	after := derep(rowInt64(row, "seq"), 0) - 1
	for {
		rows, err := h.db.All(
			"SELECT seq, ts, kind, session_id, agent_id, label, payload FROM events WHERE seq > ? ORDER BY seq LIMIT ?",
			after, int64(warmPage))
		if err != nil {
			return fmt.Errorf("read the events: %w", err)
		}
		for _, row := range rows {
			event := shared.EventRecord{
				Seq:       derep(rowInt64(row, "seq"), 0),
				Ts:        derep(rowInt64(row, "ts"), 0),
				Kind:      rowString(row, "kind"),
				SessionID: rowText(row, "session_id"),
				AgentID:   rowText(row, "agent_id"),
				Label:     rowText(row, "label"),
				Payload:   spoolLineOf(rowString(row, "payload")),
			}
			if err := fn(event); err != nil {
				return err
			}
			after = event.Seq
		}
		if len(rows) < warmPage {
			return nil
		}
	}
}

// spoolLineOf reads an event's stored payload back as the line it was; a payload that is
// not a line reads as none.
func spoolLineOf(text string) any {
	var line shared.SpoolLine
	if err := json.Unmarshal([]byte(text), &line); err != nil {
		return nil
	}
	return line
}

// WarmNodes calls fn with the nodes of the newest shared.MaxSessions sessions, the sessions
// before their subagents, until fn returns an error.
func (h *History) WarmNodes(fn func(store.WarmNode) error) error {
	rows, err := h.db.All(
		"SELECT n.kind, n.session_id, n.agent_id, n.agent_type, n.description, n.cwd, n.model, n.repo, n.branch, "+
			"n.name, n.parent_session_id, n.started_at, n.last_at, n.ended_at, p.agent_id AS parent_agent "+
			"FROM nodes n LEFT JOIN nodes p ON p.id = n.parent_id "+
			"WHERE n.session_id IN (SELECT session_id FROM nodes WHERE kind IN ('main', 'job') "+
			"ORDER BY COALESCE(last_at, 0) DESC, id LIMIT ?) "+
			"ORDER BY CASE n.kind WHEN 'subagent' THEN 1 ELSE 0 END, COALESCE(n.started_at, n.last_at, 0), n.id",
		int64(shared.MaxSessions))
	if err != nil {
		return fmt.Errorf("read the nodes: %w", err)
	}
	for _, row := range rows {
		if err := fn(warmNodeOf(row)); err != nil {
			return err
		}
	}
	return nil
}

// warmNodeOf maps a nodes row onto the store's upsert inputs: a main or job node is a
// session and its lead agent, a subagent node is an agent.
func warmNodeOf(row map[string]SqlValue) store.WarmNode {
	sessionID := rowString(row, "session_id")
	agent := store.AgentUpsert{
		SessionID: sessionID,
		ID:        rowString(row, "agent_id"),
		Model:     rowText(row, "model"),
	}
	node := store.WarmNode{Agent: agent, LastAt: rowInt64(row, "last_at"), EndedAt: rowInt64(row, "ended_at")}
	kind := shared.NodeKind(rowString(row, "kind"))
	if kind == shared.NodeSubagent {
		subagent := shared.KindSubagent
		node.Agent.Kind = &subagent
		node.Agent.Name = rowText(row, "name")
		node.Agent.AgentType = rowText(row, "agent_type")
		node.Agent.Description = rowText(row, "description")
		if parent := rowText(row, "parent_agent"); parent != nil {
			node.Agent.ParentID = &parent
		}
		return node
	}
	session := store.SessionUpsert{
		ID:              sessionID,
		Cwd:             rowText(row, "cwd"),
		StartedAt:       rowInt64(row, "started_at"),
		AgentName:       rowText(row, "name"),
		Branch:          rowText(row, "branch"),
		Repo:            rowText(row, "repo"),
		ParentSessionID: rowText(row, "parent_session_id"),
	}
	lead := shared.KindMain
	if kind == shared.NodeJob {
		external := true
		session.External = &external
		lead = shared.KindExternal
	}
	node.Agent.Kind = &lead
	node.Session = &session
	return node
}
