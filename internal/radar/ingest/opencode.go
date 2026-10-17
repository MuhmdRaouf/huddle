// The opencode collector: opencode keeps its sessions in one SQLite database,
// ~/.local/share/opencode/opencode.db ($OPENCODE_DATA names the file outright,
// $XDG_DATA_HOME the data root). Its tables (opencode v1.17.7,
// packages/core/src/session/sql.ts): session(id, project_id, workspace_id, parent_id, slug,
// directory, path, title, version, share_url, summary_additions, summary_deletions,
// summary_files, summary_diffs, metadata, cost, tokens_input, tokens_output,
// tokens_reasoning, tokens_cache_read, tokens_cache_write, revert, permission, agent,
// model, time_created, time_updated, time_compacting, time_archived) and message(id,
// session_id, time_created, time_updated, data). The message row's data JSON
// (packages/core/src/v1/session.ts, SessionV1.Info) carries an assistant message's modelID,
// providerID, cost and tokens {input, output, reasoning, cache:{read, write}} — one
// assistant message is one model response, so one request record, its harness-reported
// cost winning over radar's estimate. input includes the cached share, so the uncached
// input is input - cache.read, as the Codex and Claude usage blocks read.
package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// opencodeOverlapMs re-reads this much behind the watermark: a message written a moment
// before it is still caught, and the seen-set keeps it from landing twice.
const opencodeOverlapMs = 60_000

// OpencodeCollector reads opencode's message rows into request records tagged with the
// opencode harness. The database opens read-only — opencode owns the file — and stays open;
// a watermark plus a seen-set answer each new message exactly once.
type OpencodeCollector struct {
	env   domain.EnvLookup
	db    *opencodeDB
	water int64
	seen  boundedMap[struct{}]
}

// NewOpencodeCollector builds the collector over the database the env names.
func NewOpencodeCollector(env domain.EnvLookup) *OpencodeCollector {
	return &OpencodeCollector{env: env}
}

// Collect answers the assistant messages written since the last poll. A missing database
// reads as a machine without opencode: no records, no error.
func (c *OpencodeCollector) Collect(ctx context.Context, sinceCutoff int64) ([]shared.RequestRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file := paths.OpencodeDBPath(c.env)
	if _, err := os.Stat(file); err != nil {
		return nil, nil
	}
	if c.db == nil {
		db, err := openOpencodeDB(file)
		if err != nil {
			return nil, err
		}
		c.db = db
	}
	rows, err := c.db.query(
		"SELECT m.id AS id, m.session_id AS sessionId, m.time_created AS ts, "+
			"m.time_updated AS updated, m.data AS data, s.directory AS directory "+
			"FROM message m JOIN session s ON s.id = m.session_id "+
			"WHERE m.time_updated >= ? ORDER BY m.time_updated, m.id", c.water-opencodeOverlapMs)
	if err != nil {
		return nil, err
	}
	var records []shared.RequestRecord
	for _, row := range rows {
		messageID := rowTextOf(row, "id")
		if messageID == "" {
			continue
		}
		if _, read := c.seen.get(messageID); read {
			continue
		}
		if updated := rowWholeOf(row, "updated"); updated > c.water {
			c.water = updated
		}
		// opencode writes an assistant row when the reply starts and fills its usage in when
		// it completes; the completion bumps time_updated, so a reply read mid-stream is read
		// again then instead of being marked seen with no tokens
		if opencodeInFlight(row) {
			continue
		}
		c.seen.set(messageID, struct{}{}, 4096)
		record, ok := opencodeRecordOf(messageID, row)
		if !ok {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

// opencodeDB is the read-only SQLite surface the collector reads through: one query,
// fully materialised rows keyed by column name, so no cursor is ever held open.
type opencodeDB struct {
	pool *sql.DB
}

// openOpencodeDB opens another tool's database file read-only — nothing this handle runs
// may ever write to it. The URI form is what lets mode=ro reach SQLite.
func openOpencodeDB(file string) (*opencodeDB, error) {
	pool, err := sql.Open("sqlite", "file:"+file+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s read-only: %w", file, err)
	}
	pool.SetMaxOpenConns(1)
	if err := pool.Ping(); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("open %s read-only: %w", file, err)
	}
	return &opencodeDB{pool: pool}, nil
}

// query answers every row a query matches, an empty slice when it matches none.
func (db *opencodeDB) query(query string, params ...any) ([]map[string]any, error) {
	rows, err := db.pool.Query(query, params...)
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", query, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", query, err)
	}
	out := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		scan := make([]any, len(columns))
		for at := range values {
			scan[at] = &values[at]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, fmt.Errorf("query %q: %w", query, err)
		}
		row := make(map[string]any, len(columns))
		for at, name := range columns {
			row[name] = values[at]
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query %q: %w", query, err)
	}
	return out, nil
}

// opencodeRecordOf reads one message row into a request record; ok is false for anything
// but an assistant message that carried usage.
func opencodeRecordOf(messageID string, row map[string]any) (shared.RequestRecord, bool) {
	data, ok := row["data"].(string)
	if !ok {
		return shared.RequestRecord{}, false
	}
	message := parseEntry(data)
	if message == nil {
		return shared.RequestRecord{}, false
	}
	role, _ := stringField(message, "role")
	if role != "assistant" {
		return shared.RequestRecord{}, false
	}
	model, _ := stringField(message, "modelID")
	tokens, ok := objField(message, "tokens")
	if !ok {
		return shared.RequestRecord{}, false
	}
	cache, _ := objField(tokens, "cache")
	input := numberOrZero(tokens, "input")
	cacheRead := numberOrZero(cache, "read")
	output := numberOrZero(tokens, "output")
	cacheWrite := numberOrZero(cache, "write")
	cost := floatOrZero(message, "cost")
	if input == 0 && output == 0 && cacheRead == 0 && cacheWrite == 0 && cost == 0 {
		return shared.RequestRecord{}, false
	}
	record := shared.RequestRecord{
		ID:        "opencode:" + messageID,
		SessionID: rowTextOf(row, "sessionId"),
		AgentID:   "main",
		Model:     model,
		Ts:        rowWholeOf(row, "ts"),
		Tokens: shared.Tokens{
			Input:      max(input-cacheRead, 0),
			Output:     output,
			CacheRead:  cacheRead,
			CacheWrite: cacheWrite,
		},
		Provider: shared.ProviderOf(model),
		Harness:  shared.HarnessOpencode,
	}
	if reasoning := numberOrZero(tokens, "reasoning"); reasoning > 0 {
		record.Tokens.Thinking = &reasoning
	}
	if cost > 0 {
		record.CostUSD = &cost
	}
	if directory := rowTextOf(row, "directory"); directory != "" {
		record.Cwd = &directory
	}
	return record, true
}

// opencodeInFlight answers whether a message row is an assistant reply opencode has not
// completed yet: its time carries no completed stamp.
func opencodeInFlight(row map[string]any) bool {
	data, ok := row["data"].(string)
	if !ok {
		return false
	}
	message := parseEntry(data)
	if message == nil {
		return false
	}
	if role, _ := stringField(message, "role"); role != "assistant" {
		return false
	}
	stamps, ok := objField(message, "time")
	if !ok {
		return true
	}
	return numberOrZero(stamps, "completed") == 0
}

// rowTextOf answers the text one query row's column holds, empty when it holds anything
// else.
func rowTextOf(row map[string]any, key string) string {
	text, _ := row[key].(string)
	return text
}

// rowWholeOf answers the whole number one query row's column holds, 0 for anything else.
func rowWholeOf(row map[string]any, key string) int64 {
	switch value := row[key].(type) {
	case int64:
		return value
	case float64:
		return int64(value)
	}
	return 0
}

// floatOrZero reads one JSON object field as a finite float, 0 for anything else.
func floatOrZero(obj rawObject, key string) float64 {
	raw, ok := obj[key]
	if !ok || isNull(raw) {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0
	}
	return value
}
