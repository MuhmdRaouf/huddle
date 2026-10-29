package ledger

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The history reader: read-only pages of history.db's requests, run against a fixture
// history.db in a temp directory only.

// openHistoryReader opens a HistoryReader over file, closed when the test ends.
func openHistoryReader(t *testing.T, file string) *HistoryReader {
	t.Helper()
	r, err := OpenHistoryReader(file)
	if err != nil {
		t.Fatalf("open the history reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// TestHistoryReaderPagesInIDOrder: paging from the last id reads every row once, in id
// order, with the fields read as the row stored them.
func TestHistoryReaderPagesInIDOrder(t *testing.T) {
	r := openHistoryReader(t, writeFixtureHistory(t))
	ctx := context.Background()

	var got []HistoryRow
	after := ""
	for {
		rows, err := r.HistoryPage(ctx, after, 3)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		got = append(got, rows...)
		if len(rows) == 0 {
			break
		}
		after = rows[len(rows)-1].ID
	}
	wantIDs := []string{
		"codex:s1:1",
		"msg_m1",
		"msg_sub1",
		"req_t1",
		"route:sess-a::glm-5.3:1785747624000:429",
		"route:sess-a:main:glm-5.3:1785747626000:200",
		"route:sess-b:main:claude-sonnet-5-5:1785747630000:200",
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("the pages answered %d rows, want %d", len(got), len(wantIDs))
	}
	byID := map[string]HistoryRow{}
	for at, row := range got {
		if row.ID != wantIDs[at] {
			t.Errorf("row %d = %s, want %s", at, row.ID, wantIDs[at])
		}
		byID[row.ID] = row
	}
	// spot-check the shapes: the header row carries its header, the merged row its via
	// and 1h cache write, the codex row its totals and no 1h write
	if header := byID["route:sess-a:main:glm-5.3:1785747626000:200"]; len(header.Headers) != 1 ||
		header.Headers["request-id"] != "req_h1" {
		t.Errorf("the header row's headers = %v, want request-id req_h1", header.Headers)
	}
	if hashRow := byID["route:sess-b:main:claude-sonnet-5-5:1785747630000:200"]; hashRow.Headers != nil {
		t.Errorf("the hash row's headers = %v, want none", hashRow.Headers)
	}
	merged := byID["msg_m1"]
	if merged.CacheWrite1h == nil || *merged.CacheWrite1h != 5 {
		t.Errorf("the merged row's 1h cache write = %v, want 5", merged.CacheWrite1h)
	}
	if merged.Via == nil || *merged.Via != "zai" {
		t.Errorf("the merged row's via = %v, want zai", merged.Via)
	}
	codex := byID["codex:s1:1"]
	if codex.CacheWrite1h != nil {
		t.Errorf("the codex row's 1h cache write = %v, want nil", codex.CacheWrite1h)
	}
	if !codex.Totals {
		t.Error("the codex row's totals = false, want true")
	}
}

// TestHistoryReaderNeverWrites: the reader's pool refuses a write, and a missing file
// fails to open instead of being created.
func TestHistoryReaderNeverWrites(t *testing.T) {
	t.Run("refuses an insert", func(t *testing.T) {
		r := openHistoryReader(t, writeFixtureHistory(t))
		ctx := context.Background()
		if _, err := r.pool.ExecContext(ctx,
			"INSERT INTO requests (id, node_id, ts) VALUES ('req_new', 'n', 1)"); err == nil {
			t.Fatal("the reader's pool inserted a row, want the insert refused")
		}
		var n int64
		if err := r.pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests").Scan(&n); err != nil {
			t.Fatalf("count the rows: %v", err)
		}
		if n != 7 {
			t.Errorf("the history holds %d rows after the refused insert, want 7", n)
		}
	})

	t.Run("a missing file is an error", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := OpenHistoryReader(filepath.Join(dir, "history.db")); err == nil {
			t.Fatal("OpenHistoryReader opened a missing history.db, want an error")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read the directory: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("the directory holds %v after the failed open, want nothing", entries)
		}
	})
}
