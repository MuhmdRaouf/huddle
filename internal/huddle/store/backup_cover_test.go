package store

// Copying and reading a database the caller must not edit (a legacy home's file): the backup
// copy holds the same rows through SQLite's own backup API, a read answers over a plain open,
// and a source that is not there refuses both instead of being created empty.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// seedChannel lays one channel database with a session and two events, closed so its file is
// the whole database.
func seedChannel(t *testing.T, file string) {
	t.Helper()
	s, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("init %s: %v", file, err)
	}
	for _, stmt := range []string{
		"INSERT INTO sessions (name, joined_at, last_seen) VALUES ('ada', '2026-10-16T09:00:00.000Z', '2026-10-16T09:00:00.000Z')",
		"INSERT INTO events (ts, from_name, topic) VALUES ('2026-10-16T09:00:00.000Z', 'ada', 'task')",
		"INSERT INTO events (ts, from_name, topic) VALUES ('2026-10-16T09:00:01.000Z', 'ada', 'task')",
	} {
		if err := s.Run(context.Background(), stmt); err != nil {
			t.Fatalf("seed %s: %v", file, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close %s: %v", file, err)
	}
}

func TestBackupFileCopiesEveryRowIntoAFreshFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	dst := filepath.Join(dir, "dst.db")
	seedChannel(t, src)
	if err := BackupFile(context.Background(), src, dst); err != nil {
		t.Fatalf("BackupFile: %v", err)
	}
	rows, err := ReadAll(context.Background(), dst, "SELECT name FROM sessions UNION ALL SELECT from_name FROM events ORDER BY 1")
	if err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("the copy holds %d rows, want the session and both events", len(rows))
	}
	// the source is only read: it still answers its rows after the copy
	rows, err = ReadAll(context.Background(), src, "SELECT COUNT(*) AS n FROM events")
	if err != nil {
		t.Fatalf("read the source: %v", err)
	}
	if rows[0]["n"] != int64(2) {
		t.Errorf("the source holds %v events after the copy, want 2", rows[0]["n"])
	}
}

func TestBackupFileRefusesAMissingSource(t *testing.T) {
	err := BackupFile(context.Background(), filepath.Join(t.TempDir(), "gone.db"), filepath.Join(t.TempDir(), "dst.db"))
	if err == nil {
		t.Fatal("backing up a missing database succeeded")
	}
	if !strings.Contains(err.Error(), "no database") {
		t.Errorf("the refusal = %v, want it to name the missing database", err)
	}
}

func TestReadAllAnswersRowsAndRefusesAMissingFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "old.db")
	seedChannel(t, file)
	rows, err := ReadAll(context.Background(), file, "SELECT name FROM sessions WHERE name = ?", "ada")
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(rows) != 1 || rows[0]["name"] != "ada" {
		t.Fatalf("rows = %v, want the one session", rows)
	}
	if _, err := ReadAll(context.Background(), filepath.Join(t.TempDir(), "gone.db"), "SELECT 1"); err == nil {
		t.Error("reading a missing database succeeded; it must not be created")
	}
}

// Raw runs its function with the pool's one connection: the face the backup API needs.
func TestRawHandsOverTheOneConnection(t *testing.T) {
	ctx := context.Background()
	db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("SQLite: %v", err)
	}
	defer db.Close()
	saw := false
	if err := db.Raw(func(driverConn any) error {
		if driverConn == nil {
			t.Error("the driver connection is nil")
		}
		saw = true
		return nil
	}); err != nil {
		t.Fatalf("Raw: %v", err)
	}
	if !saw {
		t.Error("the function never ran")
	}
	sentinel := errors.New("after")
	if err := db.Raw(func(any) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("Raw carried %v, want the function's own error", err)
	}
}
