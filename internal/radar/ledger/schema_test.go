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
