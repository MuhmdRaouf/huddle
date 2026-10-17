package history

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The migration scenarios, run against temp directories only, plus the committed fixture an
// earlier release's schema built.

// openDB opens a fresh database file in a temp directory.
func openDB(t *testing.T) (DB, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "history.db")
	db, err := OpenDB(file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, file
}

// openMigrated opens and migrates a fresh database, the way the history opener does.
func openMigrated(t *testing.T) (DB, string) {
	t.Helper()
	db, file := openDB(t)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, file
}

// textOf reads one column a row holds as a string.
func textOf(row map[string]SqlValue, key string) string {
	value, _ := row[key].(string)
	return value
}

// versionOf reads the schema version the meta table records.
func versionOf(t *testing.T, db DB) string {
	t.Helper()
	row, err := db.Get("SELECT value FROM meta WHERE key = 'schema_version'")
	if err != nil {
		t.Fatalf("read the schema version: %v", err)
	}
	if row == nil {
		t.Fatal("no schema_version row")
	}
	return textOf(row, "value")
}

// columnsOfTable answers the names a table's columns carry.
func columnsOfTable(t *testing.T, db DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.All("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	names := make(map[string]bool, len(rows))
	for _, row := range rows {
		names[textOf(row, "name")] = true
	}
	return names
}

// tableNames answers the names of every table the database holds, in creation order.
func tableNames(t *testing.T, db DB) []string {
	t.Helper()
	rows, err := db.All("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY rowid")
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, textOf(row, "name"))
	}
	return names
}

// masterSQL renders every sqlite_master row — its type, name and stored SQL text — so two
// databases' schemas compare as one byte-exact list.
func masterSQL(t *testing.T, db DB) []string {
	t.Helper()
	rows, err := db.All("SELECT type, name, sql FROM sqlite_master ORDER BY rowid")
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		sql, ok := row["sql"].(string)
		if !ok {
			sql = "<null>"
		}
		out = append(out, textOf(row, "type")+" "+textOf(row, "name")+" "+strconv.Quote(sql))
	}
	return out
}

// gzipText compresses text the way the writer stores a side.
func gzipText(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(text)); err != nil {
		t.Fatalf("gzip %q: %v", text, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip %q: %v", text, err)
	}
	return buf.Bytes()
}

// gunzipText reads a stored side back as text.
func gunzipText(t *testing.T, blob []byte) string {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	defer reader.Close()
	text, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(text)
}

// buildV1 writes the v1 shape by hand: no repo/branch/name/parent_session_id on nodes, no
// billing columns on requests.
func buildV1(t *testing.T) (DB, string) {
	t.Helper()
	db, file := openDB(t)
	if err := db.Exec(`
      CREATE TABLE nodes (
        id TEXT PRIMARY KEY, kind TEXT NOT NULL, parent_id TEXT, root_id TEXT NOT NULL,
        session_id TEXT NOT NULL, agent_id TEXT NOT NULL, label TEXT, agent_type TEXT, description TEXT,
        project TEXT, cwd TEXT, model TEXT, provider TEXT, tool_use_id TEXT, spawn_depth INTEGER,
        job_state TEXT, started_at INTEGER, last_at INTEGER, ended_at INTEGER);
      CREATE TABLE requests (
        id TEXT PRIMARY KEY, node_id TEXT NOT NULL, session_id TEXT, agent_id TEXT, model TEXT,
        upstream TEXT, ts INTEGER NOT NULL, latency_ms INTEGER, input INTEGER, output INTEGER,
        cache_read INTEGER, cache_write INTEGER, stop_reason TEXT, provider TEXT, route TEXT, via TEXT,
        totals INTEGER NOT NULL DEFAULT 0);
      CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      INSERT INTO meta (key, value) VALUES ('schema_version', '1');
    `); err != nil {
		t.Fatalf("write the v1 shape: %v", err)
	}
	if _, err := db.Run(
		"INSERT INTO nodes (id, kind, root_id, session_id, agent_id, label, last_at) " +
			"VALUES ('s1/main', 'main', 's1/main', 's1', 'main', 'before', 5)"); err != nil {
		t.Fatalf("insert the v1 node: %v", err)
	}
	if _, err := db.Run("INSERT INTO requests (id, node_id, ts, totals) VALUES ('r1', 's1/main', 5, 0)"); err != nil {
		t.Fatalf("insert the v1 request: %v", err)
	}
	return db, file
}

// buildV5 writes the v5 shape with one gzipped content row: content carries no per-side
// lengths yet.
func buildV5(t *testing.T) (DB, string) {
	t.Helper()
	db, file := openDB(t)
	if err := db.Exec(`
      CREATE TABLE requests (
        id TEXT PRIMARY KEY, node_id TEXT NOT NULL, session_id TEXT, agent_id TEXT, model TEXT,
        upstream TEXT, ts INTEGER NOT NULL, latency_ms INTEGER, input INTEGER, output INTEGER,
        cache_read INTEGER, cache_write INTEGER, cache_write_1h INTEGER, speed TEXT, geo TEXT,
        service_tier TEXT, stop_reason TEXT, error TEXT, parent_agent_id TEXT, provider TEXT,
        route TEXT, via TEXT, what TEXT, thinking INTEGER, totals INTEGER NOT NULL DEFAULT 0);
      CREATE TABLE content (
        request_id TEXT PRIMARY KEY REFERENCES requests(id) ON DELETE CASCADE,
        input BLOB, output BLOB, bytes INTEGER NOT NULL);
      CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      INSERT INTO meta (key, value) VALUES ('schema_version', '5');
    `); err != nil {
		t.Fatalf("write the v5 shape: %v", err)
	}
	if _, err := db.Run("INSERT INTO requests (id, node_id, ts, totals) VALUES ('r1', 's1/main', 5, 0)"); err != nil {
		t.Fatalf("insert the request: %v", err)
	}
	if _, err := db.Run("INSERT INTO content (request_id, input, output, bytes) VALUES ('r1', ?, ?, ?)",
		gzipText(t, "input side"), gzipText(t, "out"), int64(len("input side")+len("out"))); err != nil {
		t.Fatalf("insert the content: %v", err)
	}
	return db, file
}

func TestMigration(t *testing.T) {
	t.Run("is a no-op the second time and records the version", func(t *testing.T) {
		db, _ := openMigrated(t)
		// a node row the way the writer leaves one, so the second pass has data to keep
		if _, err := db.Run(
			"INSERT INTO nodes (id, kind, root_id, session_id, agent_id, last_at) " +
				"VALUES ('s1/main', 'main', 's1/main', 's1', 'main', 5)"); err != nil {
			t.Fatalf("insert a node: %v", err)
		}
		if err := Migrate(db); err != nil {
			t.Fatalf("migrate a second time: %v", err)
		}
		if got, want := versionOf(t, db), strconv.Itoa(SchemaVersion); got != want {
			t.Errorf("version = %q, want %q", got, want)
		}
		if got := tableNames(t, db); len(got) != 8 {
			t.Errorf("tables = %v, want 8", got)
		}
		row, err := db.Get("SELECT last_at FROM nodes WHERE id = 's1/main'")
		if err != nil {
			t.Fatalf("read the node: %v", err)
		}
		if got := row["last_at"]; got != int64(5) {
			t.Errorf("last_at = %v, want 5", got)
		}
	})

	t.Run("refuses a database written by a newer radar", func(t *testing.T) {
		db, file := openMigrated(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		probe, err := OpenDB(file)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		t.Cleanup(func() { _ = probe.Close() })
		if _, err := probe.Run("UPDATE meta SET value = ? WHERE key = 'schema_version'",
			strconv.Itoa(SchemaVersion+1)); err != nil {
			t.Fatalf("write a newer version: %v", err)
		}
		err = Migrate(probe)
		var newer *NewerSchemaError
		if !errors.As(err, &newer) {
			t.Fatalf("migrate error = %v, want a newer-schema refusal", err)
		}
		if newer.Version != SchemaVersion+1 {
			t.Errorf("refused version = %d, want %d", newer.Version, SchemaVersion+1)
		}
		want := "newer radar (schema " + strconv.Itoa(SchemaVersion+1) + ")"
		if err != nil && !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	})

	t.Run("gives a fresh database every v2 column", func(t *testing.T) {
		db, _ := openMigrated(t)
		for table, want := range map[string][]string{
			"nodes":    {"repo", "branch", "name", "parent_session_id"},
			"requests": {"cache_write_1h", "speed", "geo", "service_tier"},
			"content":  {"in_len", "out_len"},
		} {
			columns := columnsOfTable(t, db, table)
			for _, name := range want {
				if !columns[name] {
					t.Errorf("%s lacks the column %s", table, name)
				}
			}
		}
	})

	t.Run("upgrades a v1 database in place, keeping its rows", func(t *testing.T) {
		db, _ := buildV1(t)
		if err := Migrate(db); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		// a request through the widened columns, the way the writer puts one
		if _, err := db.Run(
			"INSERT INTO requests (id, node_id, ts, totals, speed, cache_write_1h) " +
				"VALUES ('r2', 's1/main', 9, 0, 'fast', 3)"); err != nil {
			t.Fatalf("insert the new request: %v", err)
		}
		rows, err := db.All("SELECT id FROM requests ORDER BY ts DESC")
		if err != nil {
			t.Fatalf("read the requests: %v", err)
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, textOf(row, "id"))
		}
		if want := []string{"r2", "r1"}; !reflect.DeepEqual(ids, want) {
			t.Errorf("requests = %v, want %v", ids, want)
		}
		fresh, err := db.Get("SELECT speed, cache_write_1h FROM requests WHERE id = 'r2'")
		if err != nil {
			t.Fatalf("read the new request: %v", err)
		}
		if got := fresh["speed"]; got != "fast" {
			t.Errorf("speed = %v, want fast", got)
		}
		if got := fresh["cache_write_1h"]; got != int64(3) {
			t.Errorf("cache_write_1h = %v, want 3", got)
		}
		old, err := db.Get("SELECT speed FROM requests WHERE id = 'r1'")
		if err != nil {
			t.Fatalf("read the old request: %v", err)
		}
		if got := old["speed"]; got != nil {
			t.Errorf("speed = %v, want nil", got)
		}
		// a subagent carrying the workspace columns the v2 batch added
		if _, err := db.Run(
			"INSERT INTO nodes (id, kind, parent_id, root_id, session_id, agent_id, repo, name, last_at) " +
				"VALUES ('s1/ag', 'subagent', 's1/main', 's1/main', 's1', 'ag', '/w/app', 'scout', 9)"); err != nil {
			t.Fatalf("insert the subagent: %v", err)
		}
		ag, err := db.Get("SELECT repo, name, parent_session_id FROM nodes WHERE id = 's1/ag'")
		if err != nil {
			t.Fatalf("read the subagent: %v", err)
		}
		if got := ag["repo"]; got != "/w/app" {
			t.Errorf("repo = %v, want /w/app", got)
		}
		if got := ag["name"]; got != "scout" {
			t.Errorf("name = %v, want scout", got)
		}
		if got := ag["parent_session_id"]; got != nil {
			t.Errorf("parent_session_id = %v, want nil", got)
		}
		// the row written before the upgrade survived it
		main, err := db.Get("SELECT label FROM nodes WHERE id = 's1/main'")
		if err != nil {
			t.Fatalf("read the old node: %v", err)
		}
		if got := main["label"]; got != "before" {
			t.Errorf("label = %v, want before", got)
		}
		if got, want := versionOf(t, db), strconv.Itoa(SchemaVersion); got != want {
			t.Errorf("version = %q, want %q", got, want)
		}
	})

	t.Run("backfills a v5 content table's side lengths from the gzip trailers, keeping the blobs", func(t *testing.T) {
		db, _ := buildV5(t)
		before, err := db.Get("SELECT input, output FROM content WHERE request_id = 'r1'")
		if err != nil {
			t.Fatalf("read the stored blobs: %v", err)
		}
		input, _ := before["input"].([]byte)
		output, _ := before["output"].([]byte)
		if err := Migrate(db); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		row, err := db.Get("SELECT in_len, out_len, input, output, bytes FROM content WHERE request_id = 'r1'")
		if err != nil {
			t.Fatalf("read the migrated row: %v", err)
		}
		if got := row["in_len"]; got != int64(len("input side")) {
			t.Errorf("in_len = %v, want %d", got, len("input side"))
		}
		if got := row["out_len"]; got != int64(len("out")) {
			t.Errorf("out_len = %v, want %d", got, len("out"))
		}
		storedIn, _ := row["input"].([]byte)
		storedOut, _ := row["output"].([]byte)
		if !bytes.Equal(storedIn, input) {
			t.Error("the input blob changed")
		}
		if !bytes.Equal(storedOut, output) {
			t.Error("the output blob changed")
		}
		if got := gunzipText(t, storedIn); got != "input side" {
			t.Errorf("input reads back as %q, want %q", got, "input side")
		}
		if got := gunzipText(t, storedOut); got != "out" {
			t.Errorf("output reads back as %q, want %q", got, "out")
		}
		if got := row["bytes"]; got != int64(len("input side")+len("out")) {
			t.Errorf("bytes = %v, want %d", got, len("input side")+len("out"))
		}
	})

	t.Run("leaves the database in WAL mode", func(t *testing.T) {
		db, file := openMigrated(t)
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
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
}

// legacyFixture is the database an earlier release's schema built on a new file, frozen as
// the compatibility proof: the migration must open it, read its version and tables, and
// leave its sqlite_master SQL byte for byte.
const legacyFixture = "testdata/history_legacy.sqlite"

// fixtureCopy copies the fixture into the test's temp directory, so a migration never writes
// into testdata.
func fixtureCopy(t *testing.T) string {
	t.Helper()
	blob, err := os.ReadFile(legacyFixture)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	file := filepath.Join(t.TempDir(), "history_legacy.sqlite")
	if err := os.WriteFile(file, blob, 0o600); err != nil {
		t.Fatalf("copy the fixture: %v", err)
	}
	return file
}

func TestLegacyDatabase(t *testing.T) {
	t.Run("matches the legacy database's sqlite_master SQL byte for byte on a new database", func(t *testing.T) {
		from, err := OpenDB(fixtureCopy(t))
		if err != nil {
			t.Fatalf("open the fixture copy: %v", err)
		}
		t.Cleanup(func() { _ = from.Close() })
		fresh, _ := openMigrated(t)
		want := masterSQL(t, from)
		got := masterSQL(t, fresh)
		for at := 0; at < len(got) && at < len(want); at++ {
			if got[at] != want[at] {
				t.Errorf("sqlite_master row %d:\n got:  %s\n want: %s", at+1, got[at], want[at])
			}
		}
		if len(got) != len(want) {
			t.Errorf("sqlite_master holds %d rows, want %d", len(got), len(want))
		}
	})

	t.Run("opens a database an earlier release created, reading its version and every table", func(t *testing.T) {
		before, err := OpenDB(fixtureCopy(t))
		if err != nil {
			t.Fatalf("open the pristine copy: %v", err)
		}
		t.Cleanup(func() { _ = before.Close() })
		db, err := OpenDB(fixtureCopy(t))
		if err != nil {
			t.Fatalf("open the migrated copy: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := Migrate(db); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		if got, want := versionOf(t, db), strconv.Itoa(SchemaVersion); got != want {
			t.Errorf("version = %q, want %q", got, want)
		}
		want := []string{"meta", "nodes", "requests", "content", "tools", "captures", "events", "offsets"}
		if got := tableNames(t, db); !reflect.DeepEqual(got, want) {
			t.Errorf("tables = %v, want %v", got, want)
		}
		if got, pristine := masterSQL(t, db), masterSQL(t, before); !reflect.DeepEqual(got, pristine) {
			t.Error("migrating the legacy database changed its schema")
		}
	})
}

// failingAt fails every statement whose SQL contains the marker and answers the rest from the
// database it wraps, so each of Migrate's failure branches is reachable without pinning the
// order of its statements.
type failingAt struct {
	DB
	marker string
}

func (f failingAt) Exec(query string) error {
	if strings.Contains(query, f.marker) {
		return errors.New("boom")
	}
	return f.DB.Exec(query)
}

func (f failingAt) All(query string, params ...SqlValue) ([]map[string]SqlValue, error) {
	if strings.Contains(query, f.marker) {
		return nil, errors.New("boom")
	}
	return f.DB.All(query, params...)
}

func (f failingAt) Get(query string, params ...SqlValue) (map[string]SqlValue, error) {
	if strings.Contains(query, f.marker) {
		return nil, errors.New("boom")
	}
	return f.DB.Get(query, params...)
}

func (f failingAt) Run(query string, params ...SqlValue) (int64, error) {
	if strings.Contains(query, f.marker) {
		return 0, errors.New("boom")
	}
	return f.DB.Run(query, params...)
}

func TestMigrateFailure(t *testing.T) {
	t.Run("stops at the first failing statement", func(t *testing.T) {
		cases := []struct {
			name   string
			marker string
			build  func(t *testing.T) (DB, string)
		}{
			{"the journal pragma", "PRAGMA journal_mode", openDB},
			{"the version read", "SELECT value FROM meta", openDB},
			{"the tables", "CREATE TABLE IF NOT EXISTS nodes", openDB},
			{"the column read", "PRAGMA table_info", openDB},
			{"a column add", "ALTER TABLE", buildV1},
			{"the content read", "SELECT request_id, input, output FROM content", buildV5},
			{"a length backfill", "UPDATE content SET in_len", buildV5},
			{"the version write", "INSERT OR REPLACE INTO meta", openDB},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				db, _ := tc.build(t)
				if err := Migrate(failingAt{DB: db, marker: tc.marker}); err == nil {
					t.Fatal("migrate passed a failing statement")
				}
			})
		}
	})
}
