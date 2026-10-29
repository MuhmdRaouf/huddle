package ledger

// The schema's versioned steps: a file an older Open wrote gains the columns a later
// version added, in place.

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
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

// buildVersion7File writes file as a version-7 ledger holding one priced row, the shape a
// version-7 file sits at between Opens.
func buildVersion7File(t *testing.T, file string) {
	t.Helper()
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-7 file: %v", err)
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
		"ALTER TABLE ledger_requests ADD COLUMN node_id TEXT NOT NULL DEFAULT '';" +
		"PRAGMA user_version = 7"); err != nil {
		t.Fatalf("build the version-7 schema: %v", err)
	}
	if _, err := pool.Exec(
		"INSERT INTO ledger_requests (id, anthropic_id, model, cost_usd, cost_reported_usd) " +
			"VALUES ('req_priced', 'req_priced', 'glm-5.3', 0.25, 0.5)"); err != nil {
		t.Fatalf("insert the priced row: %v", err)
	}
}

// buildVersion8File writes file as a version-8 ledger holding one stale row and one facts
// row, the shape a version-8 file sits at between Opens.
func buildVersion8File(t *testing.T, file string) {
	t.Helper()
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-8 file: %v", err)
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
		"ALTER TABLE ledger_requests ADD COLUMN node_id TEXT NOT NULL DEFAULT '';" +
		"ALTER TABLE ledger_requests RENAME COLUMN cost_usd TO price_usd;" +
		"ALTER TABLE ledger_requests RENAME COLUMN cost_reported_usd TO harness_cost_usd;" +
		"PRAGMA user_version = 8"); err != nil {
		t.Fatalf("build the version-8 schema: %v", err)
	}
	if _, err := pool.Exec(
		"INSERT INTO ledger_requests (id, anthropic_id, model) VALUES ('req_old', 'req_old', 'stale');" +
			"INSERT INTO ledger_facts (id, route, transcript) VALUES ('req_old', '', '{\"request_id\":\"req_old\"}')"); err != nil {
		t.Fatalf("insert the held rows: %v", err)
	}
}

// TestOpenRaisesAVersion8FileToTheHarnessFactsColumn: Open brings a version-8 file up to
// SchemaVersion, its ledger_facts gains the harness column the harness records file under,
// and the facts row it already held reads an empty one.
func TestOpenRaisesAVersion8FileToTheHarnessFactsColumn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	buildVersion8File(t, file)
	st := openLedgerFile(t, file)

	if version := userVersion(t, st.db); version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
	found, err := st.db.All("PRAGMA table_info(ledger_facts)")
	if err != nil {
		t.Fatalf("read the facts table's columns: %v", err)
	}
	names := make([]string, 0, len(found))
	for _, column := range found {
		names = append(names, textColumn(column, "name"))
	}
	if !slices.Contains(names, "harness") {
		t.Errorf("ledger_facts holds no harness column after Open (has %v)", names)
	}
	held, err := st.db.Get("SELECT harness FROM ledger_facts WHERE id = 'req_old'")
	if err != nil || held == nil {
		t.Fatalf("read the held facts row: %+v, %v", held, err)
	}
	if harness := textColumn(held, "harness"); harness != "" {
		t.Errorf("harness = %q, want the default empty string", harness)
	}
}

// TestOpenRaisesAVersion7FileToTheTwoMoneyColumns: Open brings a version-7 file up to
// SchemaVersion, its two money columns read price_usd and harness_cost_usd — one meaning
// each — and the priced row it already held reads back through Get with both values.
func TestOpenRaisesAVersion7FileToTheTwoMoneyColumns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	buildVersion7File(t, file)
	st := openLedgerFile(t, file)

	if version := userVersion(t, st.db); version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
	names := ledgerRequestColumns(t, st.db)
	held := map[string]bool{}
	for _, name := range names {
		held[name] = true
	}
	for _, want := range []string{"price_usd", "harness_cost_usd"} {
		if !held[want] {
			t.Errorf("ledger_requests holds no %s column after Open (has %v)", want, names)
		}
	}
	for _, gone := range []string{"cost_usd", "cost_reported_usd"} {
		if held[gone] {
			t.Errorf("ledger_requests still holds a %s column after Open (has %v)", gone, names)
		}
	}
	row, err := st.Get(context.Background(), "req_priced")
	if err != nil || row == nil {
		t.Fatalf("read the held row: %+v, %v", row, err)
	}
	if row.PriceUSD != 0.25 {
		t.Errorf("PriceUSD = %v, want 0.25", row.PriceUSD)
	}
	if row.HarnessCostUSD == nil || *row.HarnessCostUSD != 0.5 {
		t.Errorf("HarnessCostUSD = %v, want 0.5", row.HarnessCostUSD)
	}
}
