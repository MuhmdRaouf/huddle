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
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// w1IDs are the five requests the fixture seeds: one per join shape, a transcript-only one
// whose run total only its result event names, and a transcript-only subagent call.
var w1IDs = []string{"req_w1_a", "req_w1_b", "req_w1_c", "req_w2_d", "req_w3_e"}

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

// w1HistoryRow is one history request row's billing and capture half, the columns nullable
// the way history stores them.
type w1HistoryRow struct {
	id                      string
	what                    sql.NullString
	thinking                sql.NullInt64
	headers                 sql.NullString
	totals                  int64
	cacheWrite1h            sql.NullInt64
	speed, geo, serviceTier sql.NullString
	provider, via           sql.NullString
	costUsd                 sql.NullFloat64
	nodeID                  string
}

// w1HistoryRowFor answers the history row the request id names: the row whose id is the id,
// else the one whose headers JSON carries it as the request-id — the two places the header
// lands.
func w1HistoryRowFor(t *testing.T, db *sql.DB, id string) w1HistoryRow {
	t.Helper()
	rows, err := db.Query("SELECT id, headers, what, thinking, totals, " +
		"cache_write_1h, speed, geo, service_tier, provider, via, cost_usd, node_id FROM requests")
	if err != nil {
		t.Fatalf("read the history requests: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var found w1HistoryRow
		var headers sql.NullString
		if err := rows.Scan(&found.id, &headers, &found.what, &found.thinking, &found.totals,
			&found.cacheWrite1h, &found.speed, &found.geo, &found.serviceTier,
			&found.provider, &found.via, &found.costUsd, &found.nodeID); err != nil {
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

// w1Compare compares the seven billing columns and the four capture columns of one request,
// column by column.
func w1Compare(t *testing.T, id string, h w1HistoryRow, row ledger.Row) {
	t.Helper()
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
}

// TestTheLedgerMatchesHistory: four requests — a zai one both sources saw with the four
// billing keys, a thinking count, a prompt and a Bash call; a kimi one only the route line
// saw; an Anthropic passthrough with a speed; and a transcript-only one whose run total its
// result event names — land the same ten values in history.db and ledger.db.
func TestTheLedgerMatchesHistory(t *testing.T) {
	world := makeEnv(t)
	ctx := t.Context()
	at := time.Now()

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
	)
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s1.jsonl"), strings.Join([]string{
		w1UserLine(t, at, "s1", "fix the flaky test"),
		w1TranscriptLine(t, at, "s1", "req_w1_a", "glm-4.6",
			w1WorkContent("On it — running the suite."), full),
	}, "\n")+"\n")
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s2.jsonl"), strings.Join([]string{
		w1TranscriptLine(t, at, "s2", "req_w2_d", "glm-5.3", w1TextContent("On it."), zero),
		w1ResultLine(t, at, "s2", result),
	}, "\n")+"\n")
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s1", "subagents", "agent-a7.jsonl"),
		w1TranscriptLine(t, at, "s1", "req_w3_e", "glm-5.3", w1TextContent("Scouting the suite."), zero)+"\n")

	app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
	historyPath := paths.HistoryPath(ctx, world.env)
	ledgerPath := paths.LedgerPath(ctx, world.env)
	waitFor(t, "the four requests in history.db and ledger.db", 10*time.Second, func() bool {
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
}
