package history

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

// The history schema: created in place with IF NOT EXISTS and versioned through the meta
// table, so an older radar refuses to read a database a newer one wrote. The pragmas run on
// every open, WAL included, since the file is read while the server writes it.

// SchemaVersion is the history schema's version, recorded in the meta table.
const SchemaVersion = 7

// One row per main session, subagent or job; every node of a tree carries its root's id.
const nodesDDL = `
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('main','subagent','job')),
  parent_id TEXT,
  root_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  label TEXT,
  agent_type TEXT,
  description TEXT,
  project TEXT,
  cwd TEXT,
  model TEXT,
  provider TEXT,
  tool_use_id TEXT,
  spawn_depth INTEGER,
  job_state TEXT,
  repo TEXT,
  branch TEXT,
  name TEXT,
  parent_session_id TEXT,
  started_at INTEGER,
  last_at INTEGER,
  ended_at INTEGER
)`

const nodesIndexes = `
CREATE INDEX IF NOT EXISTS nodes_root_id ON nodes(root_id);
CREATE INDEX IF NOT EXISTS nodes_parent_id ON nodes(parent_id);
CREATE INDEX IF NOT EXISTS nodes_last_at ON nodes(last_at)`

const requestsDDL = `
CREATE TABLE IF NOT EXISTS requests (
  id TEXT PRIMARY KEY,
  node_id TEXT NOT NULL,
  session_id TEXT,
  agent_id TEXT,
  model TEXT,
  upstream TEXT,
  ts INTEGER NOT NULL,
  latency_ms INTEGER,
  input INTEGER,
  output INTEGER,
  cache_read INTEGER,
  cache_write INTEGER,
  cache_write_1h INTEGER,
  speed TEXT,
  geo TEXT,
  service_tier TEXT,
  stop_reason TEXT,
  error TEXT,
  parent_agent_id TEXT,
  provider TEXT,
  route TEXT,
  via TEXT,
  what TEXT,
  thinking INTEGER,
  prompt_hash TEXT,
  headers TEXT,
  totals INTEGER NOT NULL DEFAULT 0
)`

const requestsIndexes = `
CREATE INDEX IF NOT EXISTS requests_node_ts ON requests(node_id, ts);
CREATE INDEX IF NOT EXISTS requests_ts ON requests(ts)`

// Request input/output text, gzipped; each side's plain text length sits beside it (in_len,
// out_len), so a read that only weighs sides never decompresses them, and bytes keeps the sum
// for the read-out.
const contentDDL = `
CREATE TABLE IF NOT EXISTS content (
  request_id TEXT PRIMARY KEY REFERENCES requests(id) ON DELETE CASCADE,
  input BLOB,
  output BLOB,
  bytes INTEGER NOT NULL,
  in_len INTEGER,
  out_len INTEGER
)`

const toolsDDL = `
CREATE TABLE IF NOT EXISTS tools (
  id TEXT PRIMARY KEY,
  node_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  agent_id TEXT,
  name TEXT NOT NULL,
  started_at INTEGER NOT NULL,
  duration_ms INTEGER,
  ok INTEGER NOT NULL,
  input_key TEXT
)`

// One row per distinct prompt capture: the gzip bytes exactly as the router sent them (never
// re-expanded on disk), the plain size its ISIZE trailer names, and when the first request
// carrying it was seen. Requests point at a row through their prompt_hash.
const capturesDDL = `
CREATE TABLE IF NOT EXISTS captures (
  hash TEXT PRIMARY KEY,
  gz BLOB NOT NULL,
  bytes INTEGER NOT NULL,
  first_ts INTEGER NOT NULL
)`

// Timeline events (a kind plus a JSON payload); node_id is null when the event names no node.
const eventsDDL = `
CREATE TABLE IF NOT EXISTS events (
  seq INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  kind TEXT NOT NULL,
  session_id TEXT,
  agent_id TEXT,
  label TEXT,
  payload TEXT NOT NULL,
  node_id TEXT
)`

const offsetsDDL = `
CREATE TABLE IF NOT EXISTS offsets (
  file TEXT PRIMARY KEY,
  offset INTEGER NOT NULL,
  size INTEGER NOT NULL,
  mtime INTEGER NOT NULL
)`

// The meta table is created first: the version check must work on a database that has nothing
// else yet.
const metaDDL = "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"

// tableColumns names the columns one table gains.
type tableColumns struct {
	table   string
	columns []string
}

// columnBatch is one version's batch of added columns, keyed by table.
type columnBatch struct {
	version int
	tables  []tableColumns
}

// The columns each later version added to tables an older database already has; every one
// nullable, so ADD COLUMN needs no default. Batched by the version that introduced them, in
// the order the batches run.
var versionColumns = []columnBatch{
	{
		version: 2,
		tables: []tableColumns{
			{"nodes", []string{"repo TEXT", "branch TEXT", "name TEXT", "parent_session_id TEXT"}},
			{"requests", []string{"cache_write_1h INTEGER", "speed TEXT", "geo TEXT", "service_tier TEXT"}},
		},
	},
	{version: 3, tables: []tableColumns{{"requests", []string{"what TEXT", "thinking INTEGER"}}}},
	{version: 4, tables: []tableColumns{{"requests", []string{"error TEXT"}}}},
	{version: 5, tables: []tableColumns{{"requests", []string{"parent_agent_id TEXT"}}}},
	{version: 6, tables: []tableColumns{{"content", []string{"in_len INTEGER", "out_len INTEGER"}}}},
	{version: 7, tables: []tableColumns{{"requests", []string{"prompt_hash TEXT", "headers TEXT"}}}},
}

// NewerSchemaError names a database written by a newer radar: an older one refuses it.
type NewerSchemaError struct {
	Version int
}

func (e *NewerSchemaError) Error() string {
	return fmt.Sprintf("radar history was written by a newer radar (schema %d); update radar", e.Version)
}

// columnsOf answers the column names a table already has, remembered per migrate so the
// pragma runs once per table.
func columnsOf(db DB, seen map[string]map[string]bool, table string) (map[string]bool, error) {
	if have, ok := seen[table]; ok {
		return have, nil
	}
	rows, err := db.All("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	have := make(map[string]bool, len(rows))
	for _, row := range rows {
		name, _ := row["name"].(string)
		have[name] = true
	}
	seen[table] = have
	return have, nil
}

// addTableColumns adds every named column of one table the database lacks.
func addTableColumns(db DB, seen map[string]map[string]bool, table tableColumns) error {
	have, err := columnsOf(db, seen, table.table)
	if err != nil {
		return err
	}
	for _, column := range table.columns {
		name, _, _ := strings.Cut(column, " ")
		if have[name] {
			continue
		}
		if err := db.Exec("ALTER TABLE " + table.table + " ADD COLUMN " + column); err != nil {
			return fmt.Errorf("widen %s with %s: %w", table.table, name, err)
		}
	}
	return nil
}

// addMissingColumns adds every column the tables lack from every batch above the version on
// disk; a fresh database already has them all and skips the ALTERs.
func addMissingColumns(db DB, version int) error {
	seen := map[string]map[string]bool{}
	for _, batch := range versionColumns {
		if batch.version <= version {
			continue // a database of this version already has the batch
		}
		for _, table := range batch.tables {
			if err := addTableColumns(db, seen, table); err != nil {
				return err
			}
		}
	}
	return nil
}

// IsizeOf reads the uncompressed size a gzip member names in its ISIZE trailer (its last four
// bytes, little-endian; exact below 4 GiB, and every stored side is capped far under that).
// Reads nothing but the trailer.
func IsizeOf(blob []byte) int64 {
	if len(blob) < 4 {
		return 0
	}
	return int64(binary.LittleEndian.Uint32(blob[len(blob)-4:]))
}

// backfillSideLengths fills the side lengths of rows an older radar wrote without them,
// reading each blob's ISIZE trailer — a one-time pass, so no later read ever decompresses a
// side just to weigh it.
func backfillSideLengths(db DB) error {
	rows, err := db.All("SELECT request_id, input, output FROM content WHERE in_len IS NULL OR out_len IS NULL")
	if err != nil {
		return fmt.Errorf("read the content rows without side lengths: %w", err)
	}
	for _, row := range rows {
		var inLen, outLen SqlValue
		if blob, ok := row["input"].([]byte); ok {
			inLen = IsizeOf(blob)
		}
		if blob, ok := row["output"].([]byte); ok {
			outLen = IsizeOf(blob)
		}
		if _, err := db.Run("UPDATE content SET in_len = ?, out_len = ? WHERE request_id = ?",
			inLen, outLen, row["request_id"]); err != nil {
			return fmt.Errorf("backfill the side lengths of %v: %w", row["request_id"], err)
		}
	}
	return nil
}

// Migrate brings an opened database to SchemaVersion; it refuses one written by a newer
// radar.
func Migrate(db DB) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if err := db.Exec(pragma); err != nil {
			return fmt.Errorf("set %s: %w", pragma, err)
		}
	}
	if err := db.Exec(metaDDL); err != nil {
		return fmt.Errorf("create the meta table: %w", err)
	}
	row, err := db.Get("SELECT value FROM meta WHERE key = 'schema_version'")
	if err != nil {
		return fmt.Errorf("read the schema version: %w", err)
	}
	version := 0
	if row != nil {
		value, _ := row["value"].(string)
		// a value that parses as no number reads as version 0: every batch applies
		if number, err := strconv.ParseFloat(value, 64); err == nil {
			version = int(number)
		}
	}
	if version > SchemaVersion {
		return &NewerSchemaError{Version: version}
	}
	if err := db.Exec(nodesDDL + ";" + nodesIndexes + ";" + requestsDDL + ";" + requestsIndexes + ";" +
		contentDDL + ";" + toolsDDL + ";" + capturesDDL + ";" + eventsDDL + ";" + offsetsDDL); err != nil {
		return fmt.Errorf("create the schema: %w", err)
	}
	// older tables keep their old shape through CREATE IF NOT EXISTS; widen them before
	// anything reads
	if version < SchemaVersion {
		if err := addMissingColumns(db, version); err != nil {
			return err
		}
		if err := backfillSideLengths(db); err != nil {
			return err
		}
	}
	if _, err := db.Run("INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)",
		strconv.Itoa(SchemaVersion)); err != nil {
		return fmt.Errorf("record the schema version: %w", err)
	}
	return nil
}
