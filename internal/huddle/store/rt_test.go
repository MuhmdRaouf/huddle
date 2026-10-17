package store

import (
	"context"
	"path/filepath"
	"testing"
)

// SQLite opens a file that does not exist yet, the way a fresh channel's database is born,
// and fails on a file it cannot open.
func TestSQLite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "t.db")
	db, err := SQLite(context.Background(), file)
	if err != nil {
		t.Fatalf("SQLite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := db.All(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("a query after Close must fail")
	}
	if _, err := SQLite(context.Background(), filepath.Join(t.TempDir(), "no-such-dir", "t.db")); err == nil {
		t.Fatal("a file in a missing directory fails to open")
	}
	if _, err := SQLite(context.Background(), t.TempDir()); err == nil {
		t.Fatal("a directory is not a database file")
	}
}

// Exec runs many statements in one call: schema files and pragma lines are whole strings.
func TestDBExec(t *testing.T) {
	ctx := context.Background()
	db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("SQLite: %v", err)
	}
	defer db.Close()
	if err := db.Exec(ctx, "PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		t.Fatalf("pragmas: %v", err)
	}
	if err := db.Exec(ctx, "CREATE TABLE a (k TEXT); CREATE INDEX ai ON a(k);"); err != nil {
		t.Fatalf("two statements: %v", err)
	}
	r, err := db.Get(ctx, "SELECT COUNT(*) AS n FROM sqlite_master WHERE name IN ('a','ai')")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r["n"] != int64(2) {
		t.Fatalf("both statements ran, sqlite_master holds %v objects, want 2", r["n"])
	}
}

// All keys every row by column name and keeps SQLite's own types: TEXT string, INTEGER int64,
// REAL float64, BLOB []byte, NULL nil. An empty answer is an empty slice, never nil.
func TestDBAll(t *testing.T) {
	ctx := context.Background()
	db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("SQLite: %v", err)
	}
	defer db.Close()
	if err := db.Exec(ctx, "CREATE TABLE t (s TEXT, i INTEGER, f REAL, b BLOB, n TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Run(ctx, "INSERT INTO t VALUES (?,?,?,?,?)", "s", 7, 0.5, []byte("by"), nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, err := db.All(ctx, "SELECT s, i, f, b, n FROM t")
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("one row, got %d", len(rows))
	}
	r := rows[0]
	if r["s"] != "s" || r["i"] != int64(7) || r["f"] != 0.5 || r["n"] != nil {
		t.Fatalf("scalar values: %v", r)
	}
	b, ok := r["b"].([]byte)
	if !ok || string(b) != "by" {
		t.Fatalf("blob value: %#v", r["b"])
	}
	rows, err = db.All(ctx, "SELECT s FROM t WHERE i = ?", 0)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Fatalf("no rows is an empty slice, got %#v", rows)
	}
}

// Get returns the first row, nil when the answer holds none.
func TestDBGet(t *testing.T) {
	ctx := context.Background()
	db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("SQLite: %v", err)
	}
	defer db.Close()
	if err := db.Exec(ctx, "CREATE TABLE t (k TEXT); INSERT INTO t VALUES ('a'); INSERT INTO t VALUES ('b');"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r, err := db.Get(ctx, "SELECT k FROM t")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r["k"] != "a" {
		t.Fatalf("first row, got %v", r["k"])
	}
	r, err = db.Get(ctx, "SELECT k FROM t WHERE k = ?", "zz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r != nil {
		t.Fatalf("no row is nil, got %v", r)
	}
	if _, err := db.Get(ctx, "SELECT nope FROM t"); err == nil {
		t.Fatal("a broken statement fails")
	}
}

// Run takes parameters the way both drivers bind them: a boolean as 1/0, nil as NULL.
func TestDBRun(t *testing.T) {
	ctx := context.Background()
	db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("SQLite: %v", err)
	}
	defer db.Close()
	if err := db.Exec(ctx, "CREATE TABLE t (yes BOOLEAN, no BOOLEAN, n TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Run(ctx, "INSERT INTO t VALUES (?,?,?)", true, false, nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	r, err := db.Get(ctx, "SELECT yes, no, n FROM t")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r["yes"] != int64(1) || r["no"] != int64(0) || r["n"] != nil {
		t.Fatalf("bound values: %v", r)
	}
}
