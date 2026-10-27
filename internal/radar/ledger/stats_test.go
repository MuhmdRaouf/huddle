package ledger

// The body store's totals, the numbers the owner's switch page serves: how many requests
// the store keeps bodies for, and how much disk the store's own files hold. Every case
// runs against a router.db in a temp directory only.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openBodiesAt opens a router.db in a temp directory's ledger directory — the layout the
// radar state dir uses — and answers the store with that directory, closed when the test
// ends.
func openBodiesAt(t *testing.T) (*BodyStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ledger")
	st, err := OpenBodies(filepath.Join(dir, "router.db"), testBodyKey)
	if err != nil {
		t.Fatalf("open the body store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dir
}

// ownDisk stats the store's own files in dir — the database, its WAL and shared-memory
// siblings when present, and the day segments — and answers their total size. Files with
// any other name belong to another store.
func ownDisk(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	for _, name := range []string{"router.db", "router.db-wal", "router.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		total += info.Size()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("list %s: %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "blocks-") || !strings.HasSuffix(name, ".seg") {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		total += info.Size()
	}
	return total
}

// TestBodyStoreStatsCountsTheStoredRequests: the request count is distinct request ids,
// not body rows — one request's two sides count once.
func TestBodyStoreStatsCountsTheStoredRequests(t *testing.T) {
	t.Parallel()
	st, _ := openBodiesAt(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, put := range []struct{ id, side string }{
		{"req_1", "input"}, {"req_1", "output"}, {"req_2", "input"},
	} {
		if err := st.PutBody(ctx, put.id, put.side, []byte(`{"a":1}`), at); err != nil {
			t.Fatalf("put %s %s: %v", put.id, put.side, err)
		}
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Requests != 2 {
		t.Fatalf("requests = %d, want 2", stats.Requests)
	}
}

// TestBodyStoreStatsSizesItsOwnFiles: the disk is the size of the store's own files, and
// no other file in their directory counts — ledger.db, the ledger's own database, lives
// there too.
func TestBodyStoreStatsSizesItsOwnFiles(t *testing.T) {
	t.Parallel()
	st, dir := openBodiesAt(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	if err := st.PutBody(ctx, "req_1", "input", []byte(`{"a":1}`), at); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.db"), bytes.Repeat([]byte{0}, 4096), 0o600); err != nil {
		t.Fatalf("write ledger.db: %v", err)
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if want := ownDisk(t, dir); stats.DiskBytes != want {
		t.Fatalf("disk = %d, want the store files' %d (ledger.db stays out)", stats.DiskBytes, want)
	}
}
