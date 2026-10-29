package ledger

// The schema's versioned steps: a file an older Open wrote gains the columns a later
// version added, in place.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// buildVersion4File writes file as a version-4 ledger: the version-3 tables plus the
// reconciled column and the ledger_meta table, stamped version 4 — the shape Open of the
// previous schema left behind.
func buildVersion4File(t *testing.T, file string) {
	t.Helper()
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-4 file: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";" + ledgerFactsDDL + ";" +
		ledgerRequestsSessionIndex + ";" +
		"ALTER TABLE ledger_requests ADD COLUMN reconciled INTEGER NOT NULL DEFAULT 0;" +
		"CREATE TABLE IF NOT EXISTS ledger_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);" +
		"PRAGMA user_version = 4"); err != nil {
		t.Fatalf("build the version-4 schema: %v", err)
	}
}

// ledgerRequestColumns lists the ledger_requests columns a file holds.
func ledgerRequestColumns(t *testing.T, db DB) []string {
	t.Helper()
	columns, err := db.All("PRAGMA table_info(ledger_requests)")
	if err != nil {
		t.Fatalf("read the table's columns: %v", err)
	}
	names := make([]string, 0, len(columns))
	for _, found := range columns {
		names = append(names, textColumn(found, "name"))
	}
	return names
}

// TestOpenRaisesAVersion4FileToTheBillingColumns: Open brings a version-4 file up to
// SchemaVersion, and its ledger_requests gains the six billing columns history carries.
func TestOpenRaisesAVersion4FileToTheBillingColumns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	buildVersion4File(t, file)
	st := openLedgerFile(t, file)

	if version := userVersion(t, st.db); version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
	names := ledgerRequestColumns(t, st.db)
	held := map[string]bool{}
	for _, name := range names {
		held[name] = true
	}
	for _, want := range []string{"cache_write_1h", "speed", "geo", "service_tier", "provider", "via"} {
		if !held[want] {
			t.Errorf("ledger_requests holds no %s column after Open (has %v)", want, names)
		}
	}
}

// buildVersion5Schema writes file as a version-5 ledger: the version-3 tables plus the
// reconciled column, the ledger_meta table and the six billing columns, stamped version 5 —
// the shape Open of the previous schema left behind, rows and facts to add.
func buildVersion5Schema(t *testing.T, file string) {
	t.Helper()
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-5 file: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";" + ledgerFactsDDL + ";" +
		ledgerRequestsSessionIndex + ";" +
		"ALTER TABLE ledger_requests ADD COLUMN reconciled INTEGER NOT NULL DEFAULT 0;" +
		"CREATE TABLE IF NOT EXISTS ledger_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);" +
		"ALTER TABLE ledger_requests ADD COLUMN cache_write_1h INTEGER;" +
		"ALTER TABLE ledger_requests ADD COLUMN speed TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN geo TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN service_tier TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN provider TEXT NOT NULL DEFAULT '';" +
		"ALTER TABLE ledger_requests ADD COLUMN via TEXT;" +
		"PRAGMA user_version = 5"); err != nil {
		t.Fatalf("build the version-5 schema: %v", err)
	}
}

// buildVersion5File writes file as a version-5 ledger holding one stale row, the shape a
// version-5 file sits at between Opens.
func buildVersion5File(t *testing.T, file string) {
	t.Helper()
	buildVersion5Schema(t, file)
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-5 file: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(
		"INSERT INTO ledger_requests (id, anthropic_id, model) VALUES ('req_old', 'req_old', 'stale')"); err != nil {
		t.Fatalf("insert the stale row: %v", err)
	}
}

// TestOpenRaisesAVersion5FileToTheCaptureColumns: Open brings a version-5 file up to
// SchemaVersion, its ledger_requests gains the four capture columns history carries, and the
// row it already held reads totals 0 — the column's default.
func TestOpenRaisesAVersion5FileToTheCaptureColumns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	buildVersion5File(t, file)
	st := openLedgerFile(t, file)

	if version := userVersion(t, st.db); version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
	names := ledgerRequestColumns(t, st.db)
	held := map[string]bool{}
	for _, name := range names {
		held[name] = true
	}
	for _, want := range []string{"what", "thinking", "headers", "totals"} {
		if !held[want] {
			t.Errorf("ledger_requests holds no %s column after Open (has %v)", want, names)
		}
	}
	found, err := st.db.Get("SELECT totals FROM ledger_requests WHERE id = 'req_old'")
	if err != nil || found == nil {
		t.Fatalf("read the held row: %+v, %v", found, err)
	}
	if totals := intColumn(found, "totals"); totals != 0 {
		t.Errorf("totals = %d, want the default 0", totals)
	}
}

// buildVersion6Schema writes file as a version-6 ledger: the version-3 tables plus every
// column the steps to version 6 added, stamped version 6 — the shape Open of the previous
// schema left behind, rows and facts to add.
func buildVersion6Schema(t *testing.T, file string) {
	t.Helper()
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-6 file: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";" + ledgerFactsDDL + ";" +
		ledgerRequestsSessionIndex + ";" +
		"ALTER TABLE ledger_requests ADD COLUMN reconciled INTEGER NOT NULL DEFAULT 0;" +
		"CREATE TABLE IF NOT EXISTS ledger_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);" +
		"ALTER TABLE ledger_requests ADD COLUMN cache_write_1h INTEGER;" +
		"ALTER TABLE ledger_requests ADD COLUMN speed TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN geo TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN service_tier TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN provider TEXT NOT NULL DEFAULT '';" +
		"ALTER TABLE ledger_requests ADD COLUMN via TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN what TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN thinking INTEGER;" +
		"ALTER TABLE ledger_requests ADD COLUMN headers TEXT;" +
		"ALTER TABLE ledger_requests ADD COLUMN totals INTEGER NOT NULL DEFAULT 0;" +
		"PRAGMA user_version = 6"); err != nil {
		t.Fatalf("build the version-6 schema: %v", err)
	}
}

// buildVersion6File writes file as a version-6 ledger holding one stale row, the shape a
// version-6 file sits at between Opens.
func buildVersion6File(t *testing.T, file string) {
	t.Helper()
	buildVersion6Schema(t, file)
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-6 file: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(
		"INSERT INTO ledger_requests (id, anthropic_id, model) VALUES ('req_old', 'req_old', 'stale')"); err != nil {
		t.Fatalf("insert the stale row: %v", err)
	}
}

// TestOpenRaisesAVersion6FileToTheNodeColumn: Open brings a version-6 file up to
// SchemaVersion, its ledger_requests gains the node a request lands on, and the row it
// already held reads an empty one.
func TestOpenRaisesAVersion6FileToTheNodeColumn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	buildVersion6File(t, file)
	st := openLedgerFile(t, file)

	if version := userVersion(t, st.db); version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
	names := ledgerRequestColumns(t, st.db)
	held := map[string]bool{}
	for _, name := range names {
		held[name] = true
	}
	if !held["node_id"] {
		t.Errorf("ledger_requests holds no node_id column after Open (has %v)", names)
	}
	found, err := st.db.Get("SELECT node_id FROM ledger_requests WHERE id = 'req_old'")
	if err != nil || found == nil {
		t.Fatalf("read the held row: %+v, %v", found, err)
	}
	if node := textColumn(found, "node_id"); node != "" {
		t.Errorf("node_id = %q, want the default empty string", node)
	}
}
