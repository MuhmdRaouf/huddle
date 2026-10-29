package server

// The differential test ADR-5 asks for: the same requests read out of history.db and
// ledger.db, the seven billing columns and the four capture columns compared column by
// column. The two stores join through the request-id header — the ledger's row id IS the
// header's value, and history holds that value either as the row's own id (a request only
// the transcript named) or inside the headers JSON the route line recorded (a request a
// route line saw).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// w1IDs are the requests the fixture seeds: one per join shape, a transcript-only one whose
// run total only its result event names, a transcript-only subagent call, a route line the
// upstream answered 429, a merged one whose two copies disagree on every billing key, and
// the two turns of a codex rollout plus one opencode message, filed under their own record
// ids.
var w1IDs = []string{
	"req_w1_a", "req_w1_b", "req_w1_c", "req_w2_d", "req_w3_e", "req_w5_f", "req_w6_g",
	"codex:" + w1CodexSessionID + ":0", "codex:" + w1CodexSessionID + ":1", "opencode:msg_aaa",
	"req_w7_h",
}

// w1CodexSessionID is the session the codex fixture rollout names.
const w1CodexSessionID = "3f9d1c9e-8c2a-4b7e-9a51-0f5c2a81d001"

// w1SeedCodex copies the codex fixture rollout into a temp CODEX_HOME, at the path the
// collector's own date rule builds.
func w1SeedCodex(t *testing.T, codexHome string) {
	t.Helper()
	rollout := filepath.Join(codexHome, "sessions", "2026", "10", "16",
		"rollout-2026-10-16T10-00-00-"+w1CodexSessionID+".jsonl")
	blob, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", "codex", "rollout-sample.jsonl"))
	if err != nil {
		t.Fatalf("read the codex fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(rollout), err)
	}
	if err := os.WriteFile(rollout, blob, 0o600); err != nil {
		t.Fatalf("write the rollout: %v", err)
	}
}

// w1SeedOpencode builds a temp database holding opencode's two tables and one completed
// assistant message that reported a cost and the four counters.
func w1SeedOpencode(t *testing.T, file string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(file), err)
	}
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the opencode fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	const sessionDDL = "CREATE TABLE IF NOT EXISTS `session` (" +
		"`id` text PRIMARY KEY NOT NULL," +
		"`project_id` text NOT NULL," +
		"`workspace_id` text," +
		"`parent_id` text," +
		"`slug` text NOT NULL," +
		"`directory` text NOT NULL," +
		"`path` text," +
		"`title` text NOT NULL," +
		"`version` text NOT NULL," +
		"`share_url` text," +
		"`summary_additions` integer," +
		"`summary_deletions` integer," +
		"`summary_files` integer," +
		"`summary_diffs` text," +
		"`metadata` text," +
		"`cost` real DEFAULT 0 NOT NULL," +
		"`tokens_input` integer DEFAULT 0 NOT NULL," +
		"`tokens_output` integer DEFAULT 0 NOT NULL," +
		"`tokens_reasoning` integer DEFAULT 0 NOT NULL," +
		"`tokens_cache_read` integer DEFAULT 0 NOT NULL," +
		"`tokens_cache_write` integer DEFAULT 0 NOT NULL," +
		"`revert` text," +
		"`permission` text," +
		"`agent` text," +
		"`model` text," +
		"`time_created` integer NOT NULL," +
		"`time_updated` integer NOT NULL," +
		"`time_compacting` integer," +
		"`time_archived` integer)"
	const messageDDL = "CREATE TABLE IF NOT EXISTS `message` (" +
		"`id` text PRIMARY KEY NOT NULL," +
		"`session_id` text NOT NULL REFERENCES `session` (`id`) ON DELETE CASCADE," +
		"`time_created` integer NOT NULL," +
		"`time_updated` integer NOT NULL," +
		"`data` text NOT NULL)"
	if _, err := db.Exec(sessionDDL + ";" + messageDDL); err != nil {
		t.Fatalf("create the opencode tables: %v", err)
	}
	at := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := db.Exec(
		"INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) "+
			"VALUES ('ses_w1', 'prj_1', 'fix-the-login-flow', '/w/app', 'Fix the login flow', '1.17.7', ?, ?)",
		at-60_000, at-60_000); err != nil {
		t.Fatalf("insert the session: %v", err)
	}
	data := fmt.Sprintf(`{"id":"msg_aaa","role":"assistant","parentID":"msg_000","modelID":"claude-sonnet-5-5",`+
		`"providerID":"anthropic","mode":"build","agent":"build","path":{"cwd":"/w/app","root":"/w/app"},`+
		`"cost":0.0123,"tokens":{"input":900,"output":400,"reasoning":50,"cache":{"read":300,"write":40}},`+
		`"time":{"created":%d,"completed":%d}}`, at, at+1_000)
	if _, err := db.Exec(
		"INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES ('msg_aaa', 'ses_w1', ?, ?, ?)",
		at, at+1_000, data); err != nil {
		t.Fatalf("insert the message: %v", err)
	}
}

// w1Upstream is the upstream a request's plugin names; the Anthropic passthrough for a line
// with none.
func w1Upstream(plugin string) string {
	switch plugin {
	case "zai":
		return "https://api.z.ai"
	case "kimi":
		return "https://api.moonshot.ai"
	default:
		return "https://api.anthropic.com"
	}
}

// w1RouteLine is one route line the router wrote for the request the header names, with the
// response headers given beside the request-id.
func w1RouteLine(t *testing.T, at time.Time, id, plugin, model, route string, usage map[string]any, headers map[string]string) string {
	t.Helper()
	pairs := map[string]string{"request-id": id}
	for name, value := range headers {
		pairs[name] = value
	}
	line := map[string]any{
		"ts": isoOf(at.UnixMilli()), "event": "route", "session_id": "s1",
		"model": model, "upstream": w1Upstream(plugin), "route": route,
		"status": 200, "latency_ms": 42, "usage": usage,
		"headers": pairs,
	}
	if plugin != "" {
		line["plugin"] = plugin
	}
	text, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the route line of %s: %v", id, err)
	}
	return string(text)
}

// w1FailedRouteLine is one route line the router wrote for a call the upstream answered with
// the given status and error text; the line reads as w1RouteLine's otherwise, zai's glm-5.3
// on the direct route.
func w1FailedRouteLine(t *testing.T, at time.Time, id string, status int, errText string, usage map[string]any, headers map[string]string) string {
	t.Helper()
	pairs := map[string]string{"request-id": id}
	for name, value := range headers {
		pairs[name] = value
	}
	line := map[string]any{
		"ts": isoOf(at.UnixMilli()), "event": "route", "session_id": "s1",
		"model": "glm-5.3", "upstream": w1Upstream("zai"), "route": "direct",
		"plugin": "zai", "status": status, "error": errText, "latency_ms": 42,
		"usage": usage, "headers": pairs,
	}
	text, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the route line of %s: %v", id, err)
	}
	return string(text)
}

// w1SubagentRouteLine is one route line the router wrote for a subagent call, its agent
// given: the line the store pairs with an id-less transcript copy by session, model, bare
// agent and the 10 s window.
func w1SubagentRouteLine(t *testing.T, at time.Time, id, agent, model string, usage map[string]any) string {
	t.Helper()
	line := map[string]any{
		"ts": isoOf(at.UnixMilli()), "event": "route", "session_id": "s1",
		"agent_id": agent, "model": model, "upstream": w1Upstream("zai"),
		"route": "provider", "plugin": "zai", "status": 200, "latency_ms": 42,
		"usage":   usage,
		"headers": map[string]string{"request-id": id},
	}
	text, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the route line of %s: %v", id, err)
	}
	return string(text)
}

// w1IdlessSubagentLine is one assistant line a subagent transcript wrote with no requestId:
// the record rides the message's own id, so only the store's closest fit pairs it with the
// route line.
func w1IdlessSubagentLine(t *testing.T, at time.Time, session, model string, content []any, usage map[string]any) string {
	t.Helper()
	line := map[string]any{
		"timestamp": isoOf(at.UnixMilli()), "type": "assistant", "sessionId": session,
		"message": map[string]any{
			"role": "assistant", "id": "msg_sub1", "model": model, "stop_reason": "end_turn",
			"content": content,
			"usage":   usage,
		},
	}
	text, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the subagent line of %s: %v", session, err)
	}
	return string(text)
}

// w1SessionIDs answers the request ids one store holds for the session, the count gate's
// failure line when the two stores disagree.
func w1SessionIDs(t *testing.T, db *sql.DB, table, session string) []string {
	t.Helper()
	rows, err := db.Query("SELECT id FROM "+table+" WHERE session_id = ? ORDER BY id", session)
	if err != nil {
		t.Fatalf("read the ids of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan the ids of %s: %v", table, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the ids of %s: %v", table, err)
	}
	return ids
}

// w1UserLine is one user entry a transcript logs before its assistant line: the request's
// logged input, the text prompt the what line names.
func w1UserLine(t *testing.T, at time.Time, session, text string) string {
	t.Helper()
	line := map[string]any{
		"timestamp": isoOf(at.UnixMilli()), "type": "user", "sessionId": session,
		"message": map[string]any{"role": "user", "content": text},
	}
	text2, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the user line of %s: %v", session, err)
	}
	return string(text2)
}

// w1TranscriptLine is one assistant line a transcript wrote for the request the id names,
// with the content blocks and usage given.
func w1TranscriptLine(t *testing.T, at time.Time, session, id, model string, content []any, usage map[string]any) string {
	t.Helper()
	line := map[string]any{
		"timestamp": isoOf(at.UnixMilli()), "type": "assistant", "sessionId": session,
		"requestId": id,
		"message": map[string]any{
			"role": "assistant", "id": "msg_" + id, "model": model, "stop_reason": "end_turn",
			"content": content,
			"usage":   usage,
		},
	}
	text, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the transcript line of %s: %v", id, err)
	}
	return string(text)
}

// w1TextContent is the one text block a text-only answer carries.
func w1TextContent(text string) []any {
	return []any{map[string]any{"type": "text", "text": text}}
}

// w1WorkContent is a text block plus the Bash tool_use the answer ended with: the output
// side of the what line.
func w1WorkContent(text string) []any {
	return []any{
		map[string]any{"type": "text", "text": text},
		map[string]any{"type": "tool_use", "id": "toolu_" + "w1a", "name": "Bash",
			"input": map[string]any{"command": "npm test"}},
	}
}

// w1ResultLine is the closing result event a transcript writes for its session's run, its
// usage the run's total.
func w1ResultLine(t *testing.T, at time.Time, session string, usage map[string]any) string {
	t.Helper()
	line := map[string]any{
		"timestamp": isoOf(at.UnixMilli()), "type": "result", "subtype": "success",
		"is_error": false, "duration_ms": 31000, "sessionId": session, "usage": usage,
	}
	text, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the result line of %s: %v", session, err)
	}
	return string(text)
}

// w1Held says whether the ledger holds a row for every id.
func w1LedgerHolds(file string, ids []string) bool {
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		return false
	}
	defer func() { _ = db.Close() }()
	for _, id := range ids {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM ledger_requests WHERE id = ?", id).Scan(&n); err != nil || n == 0 {
			return false
		}
	}
	return true
}

// w1HistoryHolds says whether history holds a row for every id, by the row's own id or by
// the request-id its headers JSON names.
func w1HistoryHolds(file string, ids []string) bool {
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		return false
	}
	defer func() { _ = db.Close() }()
	for _, id := range ids {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM requests WHERE id = ? OR instr(headers, ?) > 0",
			id, fmt.Sprintf(`"request-id":%q`, id)).Scan(&n); err != nil || n == 0 {
			return false
		}
	}
	return true
}

// w1HistoryRow is one history request row's billing, capture and outcome half, the columns
// nullable the way history stores them.
type w1HistoryRow struct {
	id                      string
	ts                      sql.NullInt64
	what                    sql.NullString
	thinking                sql.NullInt64
	headers                 sql.NullString
	totals                  int64
	cacheWrite1h            sql.NullInt64
	speed, geo, serviceTier sql.NullString
	provider, via           sql.NullString
	costUsd                 sql.NullFloat64
	nodeID                  string
	sessionID, agentID      sql.NullString
	stopReason, status      sql.NullString
}

// w1HistoryRowFor answers the history row the request id names: the row whose id is the id,
// else the one whose headers JSON carries it as the request-id — the two places the header
// lands.
func w1HistoryRowFor(t *testing.T, db *sql.DB, id string) w1HistoryRow {
	t.Helper()
	rows, err := db.Query("SELECT id, ts, headers, what, thinking, totals, " +
		"cache_write_1h, speed, geo, service_tier, provider, via, cost_usd, node_id, " +
		"session_id, agent_id, stop_reason, status FROM requests")
	if err != nil {
		t.Fatalf("read the history requests: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var found w1HistoryRow
		var headers sql.NullString
		if err := rows.Scan(&found.id, &found.ts, &headers, &found.what, &found.thinking, &found.totals,
			&found.cacheWrite1h, &found.speed, &found.geo, &found.serviceTier,
			&found.provider, &found.via, &found.costUsd, &found.nodeID,
			&found.sessionID, &found.agentID, &found.stopReason, &found.status); err != nil {
			t.Fatalf("scan the history request row: %v", err)
		}
		found.headers = headers
		if found.id == id || w1HeaderRequestID(headers) == id {
			return found
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the history requests: %v", err)
	}
	t.Fatalf("history holds no row for %s", id)
	return w1HistoryRow{}
}

// w1HeaderRequestID reads the request-id a stored headers JSON names, "" when it names none.
func w1HeaderRequestID(headers sql.NullString) string {
	if !headers.Valid {
		return ""
	}
	var parsed shared.Headers
	if err := json.Unmarshal([]byte(headers.String), &parsed); err != nil {
		return ""
	}
	value, _ := parsed.Get("request-id")
	return value
}

// w1TextPtr lifts a nullable history column into the pointer shape the ledger's Row reads.
func w1TextPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

// w1TextOrEmpty folds a nullable history column into the pointer shape the ledger's Row
// reads, a NULL and an "" counting as the same.
func w1TextOrEmpty(value sql.NullString) *string {
	text := ""
	if value.Valid {
		text = value.String
	}
	return &text
}

// w1IntPtr lifts a nullable history column into the pointer shape the ledger's Row reads.
func w1IntPtr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

// w1FloatPtr lifts a nullable history column into the pointer shape the ledger's Row reads.
func w1FloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}

// w1BoolPtr lifts a stored 0/1 into the pointer shape the ledger's Row reads.
func w1BoolPtr(on bool) *bool { return &on }

// w1Show renders a compared value for a failure line, nil as <nil>.
func w1Show[T any](value *T) string {
	if value == nil {
		return "<nil>"
	}
	return fmt.Sprint(*value)
}

// w1Same answers whether two optional values hold the same thing.
func w1Same[T comparable](history, led *T) bool {
	if history == nil || led == nil {
		return history == led
	}
	return *history == *led
}

// w1One compares one column, naming a mismatch the way the brief asks: "<id> <column>:
// history=<v> ledger=<v>".
func w1One[T comparable](t *testing.T, id, column string, history, led *T) {
	t.Helper()
	if !w1Same(history, led) {
		t.Errorf("%s %s: history=%s ledger=%s", id, column, w1Show(history), w1Show(led))
	}
}

// w1Headers compares the allow-listed response headers parsed, never byte-wise: history
// keeps the pairs in arrival order and the ledger's map marshals its keys sorted. A NULL
// equals a nil map.
func w1Headers(t *testing.T, id string, history sql.NullString, led map[string]string) {
	t.Helper()
	want := map[string]string{}
	if history.Valid {
		if err := json.Unmarshal([]byte(history.String), &want); err != nil {
			t.Fatalf("parse history's headers for %s: %v", id, err)
		}
	}
	if !maps.Equal(want, led) {
		t.Errorf("%s headers: history=%v ledger=%v", id, want, led)
	}
}

// w1Compare compares the session and agent a request sits under, the seven billing columns,
// the four capture columns and the two outcome columns (stop_reason and status) of one
// request, column by column.
func w1Compare(t *testing.T, id string, h w1HistoryRow, row ledger.Row) {
	t.Helper()
	w1One(t, id, "session_id", w1TextPtr(h.sessionID), &row.SessionID)
	w1One(t, id, "agent_id", w1TextPtr(h.agentID), &row.AgentID)
	w1One(t, id, "cache_write_1h", w1IntPtr(h.cacheWrite1h), row.CacheWrite1h)
	w1One(t, id, "speed", w1TextPtr(h.speed), row.Speed)
	w1One(t, id, "geo", w1TextPtr(h.geo), row.Geo)
	w1One(t, id, "service_tier", w1TextPtr(h.serviceTier), row.ServiceTier)
	w1One(t, id, "provider", w1TextPtr(h.provider), &row.Provider)
	w1One(t, id, "via", w1TextPtr(h.via), row.Via)
	w1One(t, id, "harness_cost_usd", w1FloatPtr(h.costUsd), row.HarnessCostUSD)
	w1One(t, id, "what", w1TextPtr(h.what), row.What)
	w1One(t, id, "thinking", w1IntPtr(h.thinking), row.Thinking)
	w1Headers(t, id, h.headers, row.Headers)
	w1One(t, id, "totals", w1BoolPtr(h.totals == 1), &row.Totals)
	w1One(t, id, "node_id", &h.nodeID, &row.NodeID)
	// The one known difference (ADR-5 point 4): history files a failed route's status text
	// into stop_reason (routeStatus in ingest/watch.go), while the ledger keeps stop_reason
	// for the model's own stop reason alone and tells the failure by status, outcome and
	// error_code. When history's stop_reason is its status text and that status is not 2xx,
	// the ledger's stop_reason must be "" — nothing else counts as agreement. Every other id
	// must agree, a NULL and an "" counting as the same.
	filesStatus := false
	if h.stopReason.Valid && h.status.Valid && h.stopReason.String == h.status.String {
		code, err := strconv.Atoi(h.status.String)
		filesStatus = err != nil || code < 200 || code >= 300
	}
	if filesStatus {
		if row.StopReason != "" {
			t.Errorf("%s stop_reason: history=%s ledger=%s, want \"\"", id, h.stopReason.String, row.StopReason)
		}
	} else {
		w1One(t, id, "stop_reason", w1TextOrEmpty(h.stopReason), &row.StopReason)
	}
	// status: history keeps the TEXT the route line printed, the ledger the same number as an
	// int64; a NULL history status pairs with the ledger's 0 — no route line named one.
	status := "0"
	if h.status.Valid {
		status = h.status.String
	}
	led := strconv.FormatInt(row.Status, 10)
	w1One(t, id, "status", &status, &led)
}

// TestTheLedgerMatchesHistory: the seeded requests — a zai one both sources saw with the
// four billing keys, a thinking count, a prompt and a Bash call; a kimi one only the route
// line saw; an Anthropic passthrough with a speed; a zai route line the upstream answered
// 429; a merged one whose two copies disagree on every billing key; a transcript-only one
// whose run total its result event names; and a codex rollout plus an opencode message
// under their own record ids — land the same values in history.db and ledger.db.
func TestTheLedgerMatchesHistory(t *testing.T) {
	world := makeEnv(t)
	ctx := t.Context()
	at := time.Now()

	// the harness collectors read their own stores out of the env, both inside the temp home
	world.env["CODEX_HOME"] = filepath.Join(world.home, "codex")
	world.env["OPENCODE_DATA"] = filepath.Join(world.home, "opencode", "opencode.db")
	w1SeedCodex(t, world.env["CODEX_HOME"])
	w1SeedOpencode(t, world.env["OPENCODE_DATA"])

	full := map[string]any{
		"input_tokens": 120, "output_tokens": 45,
		"cache_read_input_tokens": 7, "cache_creation_input_tokens": 9,
		"cache_creation":        map[string]any{"ephemeral_1h_input_tokens": 32},
		"output_tokens_details": map[string]any{"thinking_tokens": 16},
		"speed":                 "fast", "inference_geo": "us", "service_tier": "priority",
	}
	zero := map[string]any{"input_tokens": 0, "output_tokens": 0}
	result := map[string]any{
		"input_tokens": 300, "output_tokens": 90,
		"cache_read_input_tokens": 1500, "cache_creation_input_tokens": 100,
	}
	// req_w6_g: the router's usage block and the transcript's copy disagree on every billing
	// key — the route line is the billing answer both stores must read.
	routeUsage := map[string]any{
		"input_tokens": 200, "output_tokens": 40,
		"cache_read_input_tokens": 0, "cache_creation_input_tokens": 50,
		"cache_creation":        map[string]any{"ephemeral_1h_input_tokens": 40},
		"output_tokens_details": map[string]any{"thinking_tokens": 12},
		"speed":                 "fast", "inference_geo": "us", "service_tier": "priority",
	}
	transcriptUsage := map[string]any{
		"input_tokens": 200, "output_tokens": 40,
		"cache_read_input_tokens": 0, "cache_creation_input_tokens": 50,
		"cache_creation":        map[string]any{"ephemeral_1h_input_tokens": 10},
		"output_tokens_details": map[string]any{"thinking_tokens": 3},
		"speed":                 "standard", "service_tier": "default",
	}
	seedSpool(t, world.state,
		spoolStart("s1", "/w/app"),
		spoolStart("s2", "/w/app"),
		// the hook told the watcher how s2's calls leave, before the transcript line below
		fmt.Sprintf(`{"ts":%q,"event":"UserPromptSubmit","session_id":"s2","base_url":"http://127.0.0.1:18999"}`,
			isoOf(at.Add(-time.Second).UnixMilli())),
		w1RouteLine(t, at, "req_w1_a", "zai", "glm-4.6", "provider", full,
			map[string]string{"x-ratelimit-remaining": "99"}),
		w1RouteLine(t, at, "req_w1_b", "kimi", "mystery-1", "provider", map[string]any{
			"input_tokens": 30, "output_tokens": 12,
		}, map[string]string{"x-ratelimit-remaining": "99"}),
		w1RouteLine(t, at, "req_w1_c", "", "claude-sonnet-4-5", "anthropic", map[string]any{
			"input_tokens": 50, "output_tokens": 8, "speed": "fast",
		}, nil),
		// at +11s the line sits outside the store's 10 s match window (MatchMs in
		// store/merge.go), so history never pairs it with req_w3_e's transcript copy
		w1FailedRouteLine(t, at.Add(11*time.Second), "req_w5_f", 429, "rate limited by api.z.ai",
			map[string]any{"input_tokens": 100, "output_tokens": 0},
			map[string]string{"retry-after": "30"}),
		w1RouteLine(t, at.Add(30*time.Second), "req_w6_g", "zai", "glm-4.6", "provider", routeUsage, nil),
		// the subagent call's route line, 60 s after the turns above so no other line is in
		// its match window: history pairs it with the id-less copy below by closest fit
		w1SubagentRouteLine(t, at.Add(60*time.Second), "req_w7_h", "agent-sub1", "glm-5.3",
			map[string]any{"input_tokens": 80, "output_tokens": 22}),
	)
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s1.jsonl"), strings.Join([]string{
		w1UserLine(t, at, "s1", "fix the flaky test"),
		w1TranscriptLine(t, at, "s1", "req_w1_a", "glm-4.6",
			w1WorkContent("On it — running the suite."), full),
		w1TranscriptLine(t, at.Add(30*time.Second), "s1", "req_w6_g", "glm-4.6",
			w1TextContent("Done."), transcriptUsage),
	}, "\n")+"\n")
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s2.jsonl"), strings.Join([]string{
		w1TranscriptLine(t, at, "s2", "req_w2_d", "glm-5.3", w1TextContent("On it."), zero),
		w1ResultLine(t, at, "s2", result),
	}, "\n")+"\n")
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s1", "subagents", "agent-a7.jsonl"),
		w1TranscriptLine(t, at, "s1", "req_w3_e", "glm-5.3", w1TextContent("Scouting the suite."), zero)+"\n")
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s1", "subagents", "agent-sub1.jsonl"),
		w1IdlessSubagentLine(t, at.Add(59*time.Second), "s1", "glm-5.3", w1TextContent("Watching the suite."),
			map[string]any{"input_tokens": 80, "output_tokens": 22})+"\n")

	app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
	historyPath := paths.HistoryPath(ctx, world.env)
	ledgerPath := paths.LedgerPath(ctx, world.env)
	waitFor(t, "the seeded requests in history.db and ledger.db", 10*time.Second, func() bool {
		return w1HistoryHolds(historyPath, w1IDs) && w1LedgerHolds(ledgerPath, w1IDs)
	})
	app.Stop()

	led, err := ledger.Open(ledgerPath)
	if err != nil {
		t.Fatalf("reopen the ledger: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	history, err := sql.Open("sqlite", "file:"+historyPath+"?mode=ro")
	if err != nil {
		t.Fatalf("open the history: %v", err)
	}
	t.Cleanup(func() { _ = history.Close() })

	for _, id := range w1IDs {
		row, err := led.Get(ctx, id)
		if err != nil {
			t.Fatalf("read the ledger row of %s: %v", id, err)
		}
		if row == nil {
			t.Fatalf("the ledger holds no row for %s", id)
		}
		w1Compare(t, id, w1HistoryRowFor(t, history, id), *row)
	}

	// req_w6_g's billing answer, pinned: the router's usage block wins over the transcript's
	// copy, and a shared wrong value must not read as agreement
	billed, err := led.Get(ctx, "req_w6_g")
	if err != nil || billed == nil {
		t.Fatalf("read the ledger row of req_w6_g: %+v, %v", billed, err)
	}
	if billed.Speed == nil || *billed.Speed != "fast" {
		t.Errorf("req_w6_g speed = %s, want fast", w1Show(billed.Speed))
	}
	if billed.ServiceTier == nil || *billed.ServiceTier != "priority" {
		t.Errorf("req_w6_g service_tier = %s, want priority", w1Show(billed.ServiceTier))
	}
	if billed.CacheWrite1h == nil || *billed.CacheWrite1h != 40 {
		t.Errorf("req_w6_g cache_write_1h = %s, want 40", w1Show(billed.CacheWrite1h))
	}

	// The count gate: per session the ledger holds exactly the requests history holds — a
	// second row for one call is a double count the column compares above cannot see.
	for _, session := range []string{"s1", "s2"} {
		var ledCount, historyCount int
		if err := history.QueryRow("SELECT COUNT(*) FROM requests WHERE session_id = ?", session).Scan(&historyCount); err != nil {
			t.Fatalf("count history's requests of %s: %v", session, err)
		}
		ledDB, err := sql.Open("sqlite", "file:"+ledgerPath+"?mode=ro")
		if err != nil {
			t.Fatalf("open the ledger for the count: %v", err)
		}
		if err := ledDB.QueryRow("SELECT COUNT(*) FROM ledger_requests WHERE session_id = ?", session).Scan(&ledCount); err != nil {
			t.Fatalf("count the ledger's requests of %s: %v", session, err)
		}
		if ledCount != historyCount {
			t.Fatalf("session %s request count: ledger=%d history=%d\nledger ids: %v\nhistory ids: %v",
				session, ledCount, historyCount,
				w1SessionIDs(t, ledDB, "ledger_requests", session), w1SessionIDs(t, history, "requests", session))
		}
		_ = ledDB.Close()
	}

	// A transcript-only request starts when its transcript says, the same instant both
	// stores hold, so started_at agrees for them; a harness record starts when the record
	// says, on both sides too. A merged request keeps history's first copy's ts while the
	// ledger keeps the route's start, so its numbers only print.
	for _, id := range append([]string{"req_w2_d", "req_w3_e"},
		"codex:"+w1CodexSessionID+":0", "opencode:msg_aaa") {
		held := w1HistoryRowFor(t, history, id)
		row, err := led.Get(ctx, id)
		if err != nil {
			t.Fatalf("read the ledger row of %s: %v", id, err)
		}
		if row == nil {
			t.Fatalf("the ledger holds no row for %s", id)
		}
		w1One(t, id, "started_at", w1IntPtr(held.ts), &row.StartedAt)
	}
	mergedHistory := w1HistoryRowFor(t, history, "req_w1_a")
	merged, err := led.Get(ctx, "req_w1_a")
	if err != nil || merged == nil {
		t.Fatalf("read the ledger row of req_w1_a: %+v, %v", merged, err)
	}
	t.Logf("req_w1_a started_at: history=%d ledger=%d", mergedHistory.ts.Int64, merged.StartedAt)

	// the ground w1Compare cannot pin alone: a shared nil or empty would read as agreement
	via, err := led.Get(ctx, "req_w2_d")
	if err != nil || via == nil {
		t.Fatalf("read the ledger row of req_w2_d: %+v, %v", via, err)
	}
	if via.Via == nil || *via.Via != "127.0.0.1:18999" {
		t.Errorf("req_w2_d via = %s, want 127.0.0.1:18999", w1Show(via.Via))
	}
	sub, err := led.Get(ctx, "req_w3_e")
	if err != nil || sub == nil {
		t.Fatalf("read the ledger row of req_w3_e: %+v, %v", sub, err)
	}
	if sub.NodeID != "s1/a7" {
		t.Errorf("req_w3_e node_id: ledger=%s", sub.NodeID)
	}
	if h := w1HistoryRowFor(t, history, "req_w3_e"); h.nodeID != "s1/a7" {
		t.Errorf("req_w3_e node_id: history=%s", h.nodeID)
	}

	// the failed call's outcome, pinned field by field: a shared empty value must never read
	// as agreement
	limited, err := led.Get(ctx, "req_w5_f")
	if err != nil || limited == nil {
		t.Fatalf("read the ledger row of req_w5_f: %+v, %v", limited, err)
	}
	if limited.Outcome != "http_error" {
		t.Errorf("req_w5_f outcome = %q, want http_error", limited.Outcome)
	}
	if limited.Status != 429 {
		t.Errorf("req_w5_f status = %d, want 429", limited.Status)
	}
	if limited.ErrorCode != "rate limited by api.z.ai" {
		t.Errorf("req_w5_f error_code = %q, want rate limited by api.z.ai", limited.ErrorCode)
	}
	if limited.StopReason != "" {
		t.Errorf("req_w5_f stop_reason = %q, want \"\"", limited.StopReason)
	}
	if limited.Headers["retry-after"] != "30" {
		t.Errorf("req_w5_f retry-after = %q, want 30", limited.Headers["retry-after"])
	}
	limitedHistory := w1HistoryRowFor(t, history, "req_w5_f")
	if !limitedHistory.stopReason.Valid || limitedHistory.stopReason.String != "429" {
		t.Errorf("req_w5_f history stop_reason = %s, want 429", w1Show(w1TextPtr(limitedHistory.stopReason)))
	}
	if !limitedHistory.status.Valid || limitedHistory.status.String != "429" {
		t.Errorf("req_w5_f history status = %s, want 429", w1Show(w1TextPtr(limitedHistory.status)))
	}
	routed, err := led.Get(ctx, "req_w1_b")
	if err != nil || routed == nil {
		t.Fatalf("read the ledger row of req_w1_b: %+v, %v", routed, err)
	}
	if routed.Outcome != "ok" {
		t.Errorf("req_w1_b outcome = %q, want ok", routed.Outcome)
	}
	if routed.Status != 200 {
		t.Errorf("req_w1_b status = %d, want 200", routed.Status)
	}
}
