package server

// The differential test ADR-5 asks for: the same requests read out of history.db and
// ledger.db, the six billing columns compared column by column. The two stores join through
// the request-id header — the ledger's row id IS the header's value, and history holds that
// value either as the row's own id (a request only the transcript named) or inside the
// headers JSON the route line recorded (a request a route line saw).

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// w1IDs are the three requests the fixture seeds, one per shape the join takes.
var w1IDs = []string{"req_w1_a", "req_w1_b", "req_w1_c"}

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

// w1RouteLine is one route line the router wrote for the request the header names.
func w1RouteLine(t *testing.T, at time.Time, id, plugin, model, route string, usage map[string]any) string {
	t.Helper()
	line := map[string]any{
		"ts": isoOf(at.UnixMilli()), "event": "route", "session_id": "s1",
		"model": model, "upstream": w1Upstream(plugin), "route": route,
		"status": 200, "latency_ms": 42, "usage": usage,
		"headers": map[string]string{"request-id": id},
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

// w1TranscriptLine is the assistant line a transcript wrote for the same request.
func w1TranscriptLine(t *testing.T, at time.Time, id, model string, usage map[string]any) string {
	t.Helper()
	line := map[string]any{
		"timestamp": isoOf(at.UnixMilli()), "type": "assistant", "sessionId": "s1",
		"requestId": id,
		"message": map[string]any{
			"role": "assistant", "id": "msg_" + id, "model": model, "stop_reason": "end_turn",
			"content": []any{map[string]any{"type": "text", "text": "On it."}},
			"usage":   usage,
		},
	}
	text, err := json.Marshal(line)
	if err != nil {
		t.Fatalf("marshal the transcript line of %s: %v", id, err)
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

// w1HistoryRow is the billing half of one history request row, the columns nullable the way
// history stores them.
type w1HistoryRow struct {
	id                      string
	cacheWrite1h            sql.NullInt64
	speed, geo, serviceTier sql.NullString
	provider, via           sql.NullString
}

// w1HistoryRowFor answers the history row the request id names: the row whose id is the id,
// else the one whose headers JSON carries it as the request-id — the two places the header
// lands.
func w1HistoryRowFor(t *testing.T, db *sql.DB, id string) w1HistoryRow {
	t.Helper()
	rows, err := db.Query("SELECT id, headers, cache_write_1h, speed, geo, service_tier, provider, via FROM requests")
	if err != nil {
		t.Fatalf("read the history requests: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var found w1HistoryRow
		var headers sql.NullString
		if err := rows.Scan(&found.id, &headers, &found.cacheWrite1h, &found.speed, &found.geo,
			&found.serviceTier, &found.provider, &found.via); err != nil {
			t.Fatalf("scan the history request row: %v", err)
		}
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

// w1Compare compares the six billing columns of one request, column by column.
func w1Compare(t *testing.T, id string, h w1HistoryRow, row ledger.Row) {
	t.Helper()
	w1One(t, id, "cache_write_1h", w1IntPtr(h.cacheWrite1h), row.CacheWrite1h)
	w1One(t, id, "speed", w1TextPtr(h.speed), row.Speed)
	w1One(t, id, "geo", w1TextPtr(h.geo), row.Geo)
	w1One(t, id, "service_tier", w1TextPtr(h.serviceTier), row.ServiceTier)
	w1One(t, id, "provider", w1TextPtr(h.provider), &row.Provider)
	w1One(t, id, "via", w1TextPtr(h.via), row.Via)
}

// TestTheLedgerMatchesHistory: three requests — a zai one both sources saw with all four
// billing keys, a kimi one only the route line saw with none, and an Anthropic passthrough
// with a speed — land the same six billing values in history.db and ledger.db.
func TestTheLedgerMatchesHistory(t *testing.T) {
	world := makeEnv(t)
	ctx := t.Context()
	at := time.Now()

	full := map[string]any{
		"input_tokens": 120, "output_tokens": 45,
		"cache_read_input_tokens": 7, "cache_creation_input_tokens": 9,
		"cache_creation": map[string]any{"ephemeral_1h_input_tokens": 32},
		"speed":          "fast", "inference_geo": "us", "service_tier": "priority",
	}
	seedSpool(t, world.state,
		spoolStart("s1", "/w/app"),
		w1RouteLine(t, at, "req_w1_a", "zai", "glm-4.6", "provider", full),
		w1RouteLine(t, at, "req_w1_b", "kimi", "mystery-1", "provider", map[string]any{
			"input_tokens": 30, "output_tokens": 12,
		}),
		w1RouteLine(t, at, "req_w1_c", "", "claude-sonnet-4-5", "anthropic", map[string]any{
			"input_tokens": 50, "output_tokens": 8, "speed": "fast",
		}),
	)
	writeText(t, filepath.Join(world.config, "projects", "-w-app", "s1.jsonl"),
		w1TranscriptLine(t, at, "req_w1_a", "glm-4.6", full)+"\n")

	app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
	historyPath := paths.HistoryPath(ctx, world.env)
	ledgerPath := paths.LedgerPath(ctx, world.env)
	waitFor(t, "the three requests in history.db and ledger.db", 10*time.Second, func() bool {
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
}
