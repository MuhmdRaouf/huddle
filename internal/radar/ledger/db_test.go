package ledger

import (
	"path/filepath"
	"testing"
)

// The ledger's raw database file, opened in a temp directory only.

// TestOpenDBFlushesCheckpointsPastTheDiskCache: the open DSN sets checkpoint_fullfsync, and
// it holds on the pooled connection after a write.
func TestOpenDBFlushesCheckpointsPastTheDiskCache(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	row, err := db.Get("PRAGMA checkpoint_fullfsync")
	if err != nil {
		t.Fatalf("read the pragma: %v", err)
	}
	if got := row["checkpoint_fullfsync"]; got != int64(1) {
		t.Fatalf("checkpoint_fullfsync = %v (%T), want int64 1", got, got)
	}
	// one connection serves every statement, so the pragma the open set must still hold
	// after a write has run on it
	if err := db.Exec("CREATE TABLE t (x)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	row, err = db.Get("PRAGMA checkpoint_fullfsync")
	if err != nil {
		t.Fatalf("read the pragma after the write: %v", err)
	}
	if got := row["checkpoint_fullfsync"]; got != int64(1) {
		t.Fatalf("checkpoint_fullfsync after the write = %v (%T), want int64 1", got, got)
	}
}
