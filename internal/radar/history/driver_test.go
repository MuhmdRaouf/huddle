package history

import (
	"bytes"
	"path/filepath"
	"testing"
)

// The driver's surface over a temp directory: the statements the history tests run against a
// freshly opened database.

func TestDB(t *testing.T) {
	t.Run("exposes exec, all, get and run with and without parameters", func(t *testing.T) {
		db, _ := openMigrated(t)
		one, err := db.Get("SELECT 1 AS one")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got := one["one"]; got != int64(1) {
			t.Errorf("one = %v (%T), want int64 1", got, got)
		}
		none, err := db.All("SELECT 1 AS one WHERE 0")
		if err != nil {
			t.Fatalf("all: %v", err)
		}
		if len(none) != 0 {
			t.Errorf("all matched %d rows, want 0", len(none))
		}
		changes, err := db.Run("INSERT INTO offsets (file, offset, size, mtime) VALUES (?, ?, ?, ?)",
			"f", int64(1), int64(2), int64(3))
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if changes != 1 {
			t.Errorf("changes = %d, want 1", changes)
		}
		rows, err := db.All("SELECT file FROM offsets WHERE file = ?", "f")
		if err != nil {
			t.Fatalf("all with parameters: %v", err)
		}
		if len(rows) != 1 {
			t.Errorf("all matched %d rows, want 1", len(rows))
		}
	})

	t.Run("reads a missing row as nil", func(t *testing.T) {
		db, _ := openMigrated(t)
		row, err := db.Get("SELECT 1 AS one WHERE 0")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if row != nil {
			t.Errorf("row = %v, want nil", row)
		}
	})

	t.Run("reads integer columns as int64, text as string and blobs as bytes", func(t *testing.T) {
		db, _ := openMigrated(t)
		if _, err := db.Run("INSERT INTO offsets (file, offset, size, mtime) VALUES (?, ?, ?, ?)",
			"f", int64(7), int64(2), int64(3)); err != nil {
			t.Fatalf("insert an offset: %v", err)
		}
		count, err := db.Get("SELECT COUNT(*) AS c FROM offsets")
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if got, ok := count["c"].(int64); !ok || got != 1 {
			t.Errorf("count = %v (%T), want int64 1", count["c"], count["c"])
		}
		offset, err := db.Get("SELECT file, offset FROM offsets WHERE file = ?", "f")
		if err != nil {
			t.Fatalf("read the offset: %v", err)
		}
		if _, ok := offset["file"].(string); !ok {
			t.Errorf("file = %v (%T), want a string", offset["file"], offset["file"])
		}
		if got, ok := offset["offset"].(int64); !ok || got != 7 {
			t.Errorf("offset = %v (%T), want int64 7", offset["offset"], offset["offset"])
		}
		if _, err := db.Run("INSERT INTO requests (id, node_id, ts, totals) VALUES ('r1', 'n', 1, 0)"); err != nil {
			t.Fatalf("insert a request: %v", err)
		}
		if _, err := db.Run("INSERT INTO content (request_id, input, output, bytes) VALUES (?, ?, NULL, 0)",
			"r1", []byte{0x1f, 0x8b}); err != nil {
			t.Fatalf("insert a blob: %v", err)
		}
		content, err := db.Get("SELECT input FROM content WHERE request_id = ?", "r1")
		if err != nil {
			t.Fatalf("read the blob: %v", err)
		}
		got, ok := content["input"].([]byte)
		if !ok || !bytes.Equal(got, []byte{0x1f, 0x8b}) {
			t.Errorf("input = %v (%T), want the bytes 1f 8b", content["input"], content["input"])
		}
	})

	t.Run("reports a failing statement", func(t *testing.T) {
		db, _ := openMigrated(t)
		if err := db.Exec("THIS IS NOT SQL"); err == nil {
			t.Error("exec passed a broken statement")
		}
		if _, err := db.All("SELECT * FROM no_such_table"); err == nil {
			t.Error("all passed a broken statement")
		}
		if _, err := db.Get("SELECT * FROM no_such_table"); err == nil {
			t.Error("get passed a broken statement")
		}
		if _, err := db.Run("INSERT INTO no_such_table (a) VALUES (1)"); err == nil {
			t.Error("run passed a broken statement")
		}
	})

	t.Run("refuses a path that cannot hold a database", func(t *testing.T) {
		if _, err := OpenDB(t.TempDir()); err == nil {
			t.Error("opening a directory passed")
		}
	})

	t.Run("enforces the foreign keys the open pragmas set", func(t *testing.T) {
		db, _ := openMigrated(t)
		if _, err := db.Run("INSERT INTO requests (id, node_id, ts, totals) VALUES ('r1', 'n', 1, 0)"); err != nil {
			t.Fatalf("insert a request: %v", err)
		}
		if _, err := db.Run(
			"INSERT INTO content (request_id, input, output, bytes) VALUES ('r1', NULL, NULL, 0)"); err != nil {
			t.Fatalf("insert content: %v", err)
		}
		if _, err := db.Run("DELETE FROM requests WHERE id = 'r1'"); err != nil {
			t.Fatalf("delete the request: %v", err)
		}
		row, err := db.Get("SELECT COUNT(*) AS c FROM content")
		if err != nil {
			t.Fatalf("count the content: %v", err)
		}
		if got := row["c"]; got != int64(0) {
			t.Errorf("content rows = %v, want the cascade to leave none", got)
		}
	})
}

func TestOpenDBFile(t *testing.T) {
	t.Run("creates the file a fresh open answers", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "history.db")
		db, err := OpenDB(file)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if _, err := db.Get("SELECT 1 AS one"); err != nil {
			t.Fatalf("the fresh file answers a statement: %v", err)
		}
	})
}
