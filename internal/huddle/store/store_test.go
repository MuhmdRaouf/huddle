package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// freshStore is the fixture of tests/db.ts: one fresh SQLite file per test channel, in its own
// temp directory, schema initialised.
func freshStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	return s
}

// objects lists the schema objects a file holds.
func objects(t *testing.T, q Q) map[string]string {
	t.Helper()
	rows, err := q.All(context.Background(), "SELECT name, type FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r["name"].(string)] = r["type"].(string)
	}
	return out
}

// ftslessDB simulates a SQLite built without FTS5: every statement runs except the task index's
// CREATE VIRTUAL TABLE, which fails the way a missing module does.
type ftslessDB struct{ *DB }

func (f ftslessDB) Exec(ctx context.Context, stmts string) error {
	if strings.Contains(stmts, "CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts") {
		return errors.New("no such module: fts5")
	}
	return f.DB.Exec(ctx, stmts)
}

// schemaFailsDB fails the schema's statements, the way a file the database cannot write does.
type schemaFailsDB struct{ *DB }

func (f schemaFailsDB) Exec(ctx context.Context, stmts string) error {
	if strings.Contains(stmts, "CREATE TABLE IF NOT EXISTS meta") {
		return errors.New("attempt to write a readonly database")
	}
	return f.DB.Exec(ctx, stmts)
}

// brokenRun fails the statements that start with fail, the way a broken index does.
type brokenRun struct {
	Q
	fail string
}

func (b brokenRun) Run(ctx context.Context, stmt string, params ...any) error {
	if strings.HasPrefix(stmt, b.fail) {
		return errors.New("no such table: tasks_fts")
	}
	return b.Q.Run(ctx, stmt, params...)
}

// Open creates the file's directory chain, sets the channel pragmas and leaves the file usable.
func TestOpen(t *testing.T) {
	ctx := context.Background()
	t.Run("creates missing directories", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "channels", "proj")
		s, err := Open(ctx, filepath.Join(dir, "t.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer s.Close()
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode %o, want 700", info.Mode().Perm())
		}
		if _, err := os.Stat(filepath.Join(dir, "t.db")); err != nil {
			t.Fatalf("file: %v", err)
		}
	})
	t.Run("sets the pragmas", func(t *testing.T) {
		s := freshStore(t)
		for pragma, want := range map[string]any{
			"PRAGMA journal_mode": "wal",
			"PRAGMA synchronous":  int64(1),
			"PRAGMA foreign_keys": int64(1),
			// busy_timeout names its answer column "timeout"
			"PRAGMA busy_timeout": int64(5000),
		} {
			r, err := s.Get(context.Background(), pragma)
			if err != nil {
				t.Fatalf("%s: %v", pragma, err)
			}
			name := map[string]string{"PRAGMA busy_timeout": "timeout"}[pragma]
			if name == "" {
				name = strings.TrimPrefix(pragma, "PRAGMA ")
			}
			if r[name] != want {
				t.Errorf("%s = %v, want %v", pragma, r[name], want)
			}
		}
	})
	t.Run("memory needs no directory", func(t *testing.T) {
		s, err := Open(ctx, ":memory:")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer s.Close()
		if err := s.Run(ctx, "CREATE TABLE m (k TEXT)"); err != nil {
			t.Fatalf("run: %v", err)
		}
	})
	t.Run("fails where the file cannot live", func(t *testing.T) {
		blocked := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := Open(ctx, filepath.Join(blocked, "t.db")); err == nil {
			t.Error("a file in the way of the directory fails")
		}
		if _, err := Open(ctx, t.TempDir()); err == nil {
			t.Error("a directory is not a database file")
		}
		file := filepath.Join(t.TempDir(), "locked.db")
		if err := os.WriteFile(file, nil, 0o444); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := Open(ctx, file); err == nil {
			t.Error("a file the pragmas cannot write fails to open")
		}
	})
}

// Init creates every table, index and FTS5 index of a channel file, numbers rows with the {ID}
// keys, and is safe on a file that already has the schema.
func TestInit(t *testing.T) {
	ctx := context.Background()
	t.Run("creates every table and index", func(t *testing.T) {
		s := freshStore(t)
		got := objects(t, s)
		for name, kind := range map[string]string{
			"meta": "table", "sessions": "table", "events": "table", "phases": "table",
			"tasks": "table", "edits": "table", "notes": "table", "knowledge": "table",
			"touches": "table", "tasks_fts": "table", "knowledge_fts": "table",
			"events_to": "index", "events_reply": "index", "events_open": "index", "events_key": "index",
			"tasks_order": "index", "notes_task": "index", "touches_path": "index",
		} {
			if got[name] != kind {
				t.Errorf("%s is a %q, want a %s", name, got[name], kind)
			}
		}
		if !s.FTS {
			t.Error("FTS is true on a driver that carries FTS5")
		}
	})
	t.Run("numbers rows with the integer keys", func(t *testing.T) {
		s := freshStore(t)
		for _, stmt := range []string{
			"INSERT INTO events (ts, from_name, topic) VALUES ('2026-01-01T00:00:00.000Z', 'a', 'task')",
			"INSERT INTO events (ts, from_name, topic) VALUES ('2026-01-01T00:00:01.000Z', 'a', 'task')",
			"INSERT INTO knowledge (by, kind, title, body, created_at) VALUES ('a', 'fact', 't', 'b', '2026-01-01T00:00:00.000Z')",
		} {
			if err := s.Run(ctx, stmt); err != nil {
				t.Fatalf("run: %v", err)
			}
		}
		for query, want := range map[string]int64{
			"SELECT seq AS v FROM sqlite_sequence WHERE name = 'events'":    2,
			"SELECT seq AS v FROM sqlite_sequence WHERE name = 'knowledge'": 1,
		} {
			r, err := s.Get(ctx, query)
			if err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			if r["v"] != want {
				t.Errorf("%s = %v, want %d", query, r["v"], want)
			}
		}
	})
	t.Run("is safe on a file that already has the schema", func(t *testing.T) {
		s := freshStore(t)
		before := objects(t, s)
		if err := s.Init(ctx); err != nil {
			t.Fatalf("second init: %v", err)
		}
		after := objects(t, s)
		if len(before) != len(after) {
			t.Fatalf("schema objects %v before, %v after", len(before), len(after))
		}
		for name, kind := range before {
			if after[name] != kind {
				t.Errorf("%s changed from %s to %s", name, kind, after[name])
			}
		}
	})
	t.Run("adds the columns an older file lacks", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "old.db")
		db, err := SQLite(ctx, file)
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		// the schema before those columns existed
		if err := db.Exec(ctx, `CREATE TABLE sessions (name TEXT PRIMARY KEY, label TEXT, role TEXT, joined_at TEXT NOT NULL, last_seen TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'idle', task TEXT, step TEXT,
  control TEXT NOT NULL DEFAULT 'run' CHECK (control IN ('run','pause')), control_by TEXT, control_at TEXT, cursor INTEGER NOT NULL DEFAULT 0);
CREATE TABLE knowledge (id INTEGER PRIMARY KEY AUTOINCREMENT, by TEXT NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
  tags TEXT NOT NULL DEFAULT '[]', refs TEXT NOT NULL DEFAULT '[]', task_id TEXT, supersedes INTEGER, created_at TEXT NOT NULL,
  hits INTEGER NOT NULL DEFAULT 0);`); err != nil {
			t.Fatalf("old schema: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}
		s, err := Open(ctx, file)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer s.Close()
		if err := s.Init(ctx); err != nil {
			t.Fatalf("init: %v", err)
		}
		for table, want := range map[string][]string{
			"sessions":  {"parent", "context", "brief"},
			"knowledge": {"verified_by", "verified_at", "moved_to", "origin"},
		} {
			rows, err := s.All(ctx, "SELECT name FROM pragma_table_info(?)", table)
			if err != nil {
				t.Fatalf("table_info(%s): %v", table, err)
			}
			cols := map[string]bool{}
			for _, r := range rows {
				cols[r["name"].(string)] = true
			}
			for _, c := range want {
				if !cols[c] {
					t.Errorf("%s has no %s column after init", table, c)
				}
			}
		}
	})
	t.Run("still opens when FTS5 is missing", func(t *testing.T) {
		db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		defer db.Close()
		s := newStore(ftslessDB{db})
		if err := s.Init(ctx); err != nil {
			t.Fatalf("init: %v", err)
		}
		if s.FTS {
			t.Fatal("FTS is false when the task index cannot be created")
		}
		if got := objects(t, s); got["knowledge_fts"] != "" {
			t.Error("the knowledge index is not created when the task one failed")
		}
	})
	t.Run("fails when the schema cannot be written", func(t *testing.T) {
		db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		defer db.Close()
		s := newStore(schemaFailsDB{db})
		if err := s.Init(ctx); err == nil {
			t.Fatal("a schema that cannot be written fails init")
		}
	})
}

func TestAll(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	for _, k := range []string{"a", "b"} {
		if err := s.Run(ctx, "INSERT INTO meta (key, value) VALUES (?,?)", k, strings.ToUpper(k)); err != nil {
			t.Fatalf("run: %v", err)
		}
	}
	rows, err := s.All(ctx, "SELECT key, value FROM meta ORDER BY key")
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(rows) != 2 || rows[0]["key"] != "a" || rows[0]["value"] != "A" {
		t.Fatalf("rows: %v", rows)
	}
}

func TestGet(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	if err := s.Run(ctx, "INSERT INTO meta (key, value) VALUES ('k', 'v')"); err != nil {
		t.Fatalf("run: %v", err)
	}
	r, err := s.Get(ctx, "SELECT value FROM meta WHERE key = ?", "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r["value"] != "v" {
		t.Fatalf("value: %v", r)
	}
	r, err = s.Get(ctx, "SELECT value FROM meta WHERE key = ?", "nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r != nil {
		t.Fatalf("no row is nil, got %v", r)
	}
}

func TestRun(t *testing.T) {
	s := freshStore(t)
	ctx := context.Background()
	if err := s.Run(ctx, "INSERT INTO events (ts, from_name, topic, needs_reply) VALUES (?,?,?,?)",
		"2026-01-01T00:00:00.000Z", "a", "task", true); err != nil {
		t.Fatalf("run: %v", err)
	}
	r, err := s.Get(ctx, "SELECT needs_reply FROM events")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if r["needs_reply"] != int64(1) {
		t.Fatalf("a boolean writes 1, got %v", r["needs_reply"])
	}
}

// Tx commits when the callback succeeds and rolls the whole thing back when it fails; a
// transaction inside one is a savepoint, so the inner rollback keeps the outer's writes.
func TestTx(t *testing.T) {
	ctx := context.Background()
	t.Run("commits when the callback succeeds", func(t *testing.T) {
		s := freshStore(t)
		err := s.Tx(ctx, func(ctx context.Context, q Q) error {
			return q.Run(ctx, "INSERT INTO meta (key, value) VALUES ('k', 'v')")
		})
		if err != nil {
			t.Fatalf("tx: %v", err)
		}
		r, err := s.Get(ctx, "SELECT value FROM meta WHERE key = 'k'")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if r["value"] != "v" {
			t.Fatalf("the write committed, got %v", r)
		}
	})
	t.Run("rolls back when the callback fails", func(t *testing.T) {
		s := freshStore(t)
		sentinel := errors.New("no")
		err := s.Tx(ctx, func(ctx context.Context, q Q) error {
			if err := q.Run(ctx, "INSERT INTO meta (key, value) VALUES ('k', 'v')"); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("tx error %v, want the callback's", err)
		}
		r, err := s.Get(ctx, "SELECT value FROM meta WHERE key = 'k'")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if r != nil {
			t.Fatalf("the write rolled back, got %v", r)
		}
	})
	t.Run("an inner rollback keeps the outer's writes", func(t *testing.T) {
		s := freshStore(t)
		sentinel := errors.New("inner")
		err := s.Tx(ctx, func(ctx context.Context, q Q) error {
			if err := q.Run(ctx, "INSERT INTO meta (key, value) VALUES ('outer', '1')"); err != nil {
				return err
			}
			inner := s.Tx(ctx, func(ctx context.Context, q Q) error {
				if err := q.Run(ctx, "INSERT INTO meta (key, value) VALUES ('inner', '1')"); err != nil {
					return err
				}
				return sentinel
			})
			if !errors.Is(inner, sentinel) {
				return fmt.Errorf("inner tx: %w", inner)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("tx: %v", err)
		}
		for key, want := range map[string]bool{"outer": true, "inner": false} {
			r, err := s.Get(ctx, "SELECT value FROM meta WHERE key = ?", key)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if (r != nil) != want {
				t.Errorf("%s present = %v, want %v", key, r != nil, want)
			}
		}
	})
	t.Run("an inner commit joins the outer transaction", func(t *testing.T) {
		s := freshStore(t)
		err := s.Tx(ctx, func(ctx context.Context, q Q) error {
			if err := q.Run(ctx, "INSERT INTO meta (key, value) VALUES ('outer', '1')"); err != nil {
				return err
			}
			return s.Tx(ctx, func(ctx context.Context, q Q) error {
				return q.Run(ctx, "INSERT INTO meta (key, value) VALUES ('inner', '1')")
			})
		})
		if err != nil {
			t.Fatalf("tx: %v", err)
		}
		r, err := s.Get(ctx, "SELECT COUNT(*) AS n FROM meta")
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if r["n"] != int64(2) {
			t.Fatalf("both writes committed, got %v rows", r["n"])
		}
	})
	t.Run("fails when the connection already holds a transaction", func(t *testing.T) {
		s := freshStore(t)
		if err := s.db.Exec(ctx, "BEGIN"); err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer s.db.Exec(ctx, "ROLLBACK")
		if err := s.Tx(ctx, func(ctx context.Context, q Q) error { return nil }); err == nil {
			t.Fatal("a second BEGIN on the connection fails")
		}
	})
}

func TestClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Init(context.Background()); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := s.Get(context.Background(), "SELECT 1"); err == nil {
		t.Fatal("a query after Close must fail")
	}
}

// IndexTasks replaces the index of the given tasks, or the whole index when ids is nil.
func TestIndexTasks(t *testing.T) {
	ctx := context.Background()
	docs := []TaskDoc{
		{ID: "a", Title: "Alpha", Body: "first"},
		{ID: "b", Title: "Beta", Body: "second"},
	}
	t.Run("without ids it rebuilds the whole index", func(t *testing.T) {
		s := freshStore(t)
		if err := s.IndexTasks(ctx, s, docs, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		if err := s.IndexTasks(ctx, s, []TaskDoc{{ID: "c", Title: "Gamma", Body: "third"}}, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		r, err := s.Get(ctx, "SELECT COUNT(*) AS n FROM tasks_fts")
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if r["n"] != int64(1) {
			t.Fatalf("the index holds %v rows, want 1", r["n"])
		}
	})
	t.Run("with ids it replaces only those tasks", func(t *testing.T) {
		s := freshStore(t)
		if err := s.IndexTasks(ctx, s, docs, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		if err := s.IndexTasks(ctx, s, []TaskDoc{{ID: "a", Title: "Alpha", Body: "rewritten"}}, []string{"a"}); err != nil {
			t.Fatalf("index: %v", err)
		}
		for id, want := range map[string]string{"a": "rewritten", "b": "second"} {
			r, err := s.Get(ctx, "SELECT body FROM tasks_fts WHERE id = ?", id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if r["body"] != want {
				t.Errorf("%s body %v, want %q", id, r["body"], want)
			}
		}
	})
	t.Run("indexes through the transaction it is given", func(t *testing.T) {
		s := freshStore(t)
		sentinel := errors.New("after")
		err := s.Tx(ctx, func(ctx context.Context, q Q) error {
			if err := s.IndexTasks(ctx, q, docs, nil); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("tx error %v, want the callback's", err)
		}
		if hits := s.SearchTasks(ctx, "first"); len(hits) != 0 {
			t.Fatalf("a rolled-back index holds nothing, got %v", hits)
		}
	})
	t.Run("does nothing when FTS5 is missing", func(t *testing.T) {
		db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		defer db.Close()
		s := newStore(ftslessDB{db})
		if err := s.Init(ctx); err != nil {
			t.Fatalf("init: %v", err)
		}
		if err := s.IndexTasks(ctx, s, docs, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		if got := objects(t, s); got["tasks_fts"] != "" {
			t.Error("no index is created without FTS5")
		}
	})
	t.Run("fails when the index is gone", func(t *testing.T) {
		s := freshStore(t)
		if err := s.db.Exec(ctx, "DROP TABLE tasks_fts"); err != nil {
			t.Fatalf("drop: %v", err)
		}
		if err := s.IndexTasks(ctx, s, docs, nil); err == nil {
			t.Fatal("writing a missing index fails")
		}
		if err := s.IndexTasks(ctx, s, docs, []string{"a"}); err == nil {
			t.Fatal("deleting from a missing index fails")
		}
	})
	t.Run("carries an insert error out", func(t *testing.T) {
		s := freshStore(t)
		if err := s.IndexTasks(ctx, brokenRun{s, "INSERT INTO tasks_fts"}, docs, nil); err == nil {
			t.Fatal("an insert that fails surfaces")
		}
	})
}

// SearchTasks answers the tasks matching every word, best first, with the match marked « » in a
// snippet of the body; nil when the index cannot answer and the caller falls back to LIKE.
func TestSearchTasks(t *testing.T) {
	ctx := context.Background()
	t.Run("matches every word with the match marked in a snippet", func(t *testing.T) {
		s := freshStore(t)
		docs := []TaskDoc{
			{ID: "t1", Title: "Router watch", Body: "restart the router cleanly"},
			{ID: "t2", Title: "Docs", Body: "write the changelog"},
		}
		if err := s.IndexTasks(ctx, s, docs, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		hits := s.SearchTasks(ctx, "router restart")
		if len(hits) != 1 || hits[0]["id"] != "t1" {
			t.Fatalf("hits: %v", hits)
		}
		if hit, _ := hits[0]["hit"].(string); !strings.Contains(hit, "«router»") {
			t.Fatalf("hit %q marks the match", hit)
		}
	})
	t.Run("truncates a long snippet with an ellipsis", func(t *testing.T) {
		s := freshStore(t)
		body := "one two three four five six seven eight nine ten eleven twelve thirteen router fifteen sixteen seventeen eighteen nineteen twenty"
		if err := s.IndexTasks(ctx, s, []TaskDoc{{ID: "t1", Title: "Long", Body: body}}, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		hits := s.SearchTasks(ctx, "router")
		if len(hits) != 1 {
			t.Fatalf("hits: %v", hits)
		}
		hit, _ := hits[0]["hit"].(string)
		if !strings.Contains(hit, "«router»") || !strings.Contains(hit, "…") {
			t.Fatalf("hit %q marks the match and truncates", hit)
		}
	})
	t.Run("orders the best match first", func(t *testing.T) {
		s := freshStore(t)
		docs := []TaskDoc{
			{ID: "thin", Title: "Thin", Body: "router"},
			{ID: "dense", Title: "Dense", Body: "router router router"},
		}
		if err := s.IndexTasks(ctx, s, docs, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		hits := s.SearchTasks(ctx, "router")
		if len(hits) != 2 || hits[0]["id"] != "dense" {
			t.Fatalf("hits: %v", hits)
		}
	})
	t.Run("answers nothing when no task matches", func(t *testing.T) {
		s := freshStore(t)
		if err := s.IndexTasks(ctx, s, []TaskDoc{{ID: "t1", Title: "Docs", Body: "write the changelog"}}, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		if hits := s.SearchTasks(ctx, "zebra"); hits == nil || len(hits) != 0 {
			t.Fatalf("an empty answer is empty, not nil: %v", hits)
		}
	})
	t.Run("cannot answer when the index is gone", func(t *testing.T) {
		s := freshStore(t)
		if err := s.db.Exec(ctx, "DROP TABLE tasks_fts"); err != nil {
			t.Fatalf("drop: %v", err)
		}
		if hits := s.SearchTasks(ctx, "anything"); hits != nil {
			t.Fatalf("a failed search answers nil, got %v", hits)
		}
	})
	t.Run("cannot answer when FTS5 is missing", func(t *testing.T) {
		db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		defer db.Close()
		s := newStore(ftslessDB{db})
		if err := s.Init(ctx); err != nil {
			t.Fatalf("init: %v", err)
		}
		if hits := s.SearchTasks(ctx, "router"); hits != nil {
			t.Fatalf("a store without FTS5 answers nil, got %v", hits)
		}
	})
}

// SearchKnowledge answers the knowledge rows matching any word, a snippet of the body in hit and
// a relevance score, higher first; none when the index cannot answer.
func TestSearchKnowledge(t *testing.T) {
	ctx := context.Background()
	// seedKnowledge adds one knowledge row and indexes it, the way the writer does: both inside
	// one transaction.
	seedKnowledge := func(t *testing.T, s *Store, id int64, title, body string, tags ...string) {
		t.Helper()
		err := s.Tx(ctx, func(ctx context.Context, q Q) error {
			if err := q.Run(ctx, "INSERT INTO knowledge (id, by, kind, title, body, tags, created_at) VALUES (?,?,?,?,?,?,?)",
				id, "opus", "fact", title, body, "[]", "2026-01-01T00:00:00.000Z"); err != nil {
				return err
			}
			return s.IndexKnowledge(ctx, q, Row{"id": id, "title": title, "body": body}, tags)
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t.Run("matches any word, snippets the body and scores it", func(t *testing.T) {
		s := freshStore(t)
		seedKnowledge(t, s, 1, "Ports", "the router ports for providers", "routing")
		seedKnowledge(t, s, 2, "Uptime", "the router never sleeps", "ops")
		rows := s.SearchKnowledge(ctx, "router ports", 10)
		if len(rows) != 2 {
			t.Fatalf("rows: %v", rows)
		}
		if rows[0]["title"] != "Ports" {
			t.Fatalf("a row matching more words comes first: %v", rows)
		}
		if hit, _ := rows[0]["hit"].(string); !strings.Contains(hit, "«ports»") {
			t.Fatalf("hit %q marks the match", hit)
		}
		if score, _ := rows[0]["score"].(float64); score <= 0 {
			t.Fatalf("score %v is positive, higher better", rows[0]["score"])
		}
		if rows[0]["by"] != "opus" || rows[0]["kind"] != "fact" {
			t.Fatalf("the row carries every knowledge column: %v", rows[0])
		}
	})
	t.Run("searches the tags too", func(t *testing.T) {
		s := freshStore(t)
		seedKnowledge(t, s, 1, "Ports", "the router ports", "routing", "infra")
		rows := s.SearchKnowledge(ctx, "infra", 10)
		if len(rows) != 1 || rows[0]["title"] != "Ports" {
			t.Fatalf("rows: %v", rows)
		}
	})
	t.Run("limits the answer", func(t *testing.T) {
		s := freshStore(t)
		seedKnowledge(t, s, 1, "One", "router one")
		seedKnowledge(t, s, 2, "Two", "router two")
		if rows := s.SearchKnowledge(ctx, "router", 1); len(rows) != 1 {
			t.Fatalf("one row, got %v", rows)
		}
	})
	t.Run("answers none when the index cannot answer", func(t *testing.T) {
		db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		defer db.Close()
		s := newStore(ftslessDB{db})
		if err := s.Init(ctx); err != nil {
			t.Fatalf("init: %v", err)
		}
		if rows := s.SearchKnowledge(ctx, "router", 10); len(rows) != 0 {
			t.Fatalf("no rows, got %v", rows)
		}
	})
}

// IndexKnowledge adds one new knowledge row to the index under its own id, its tags joined by
// spaces.
func TestIndexKnowledge(t *testing.T) {
	ctx := context.Background()
	t.Run("indexes the row under its id", func(t *testing.T) {
		s := freshStore(t)
		if err := s.Run(ctx, "INSERT INTO knowledge (id, by, kind, title, body, created_at) VALUES (7,'opus','fact','Ports','the router ports','2026-01-01T00:00:00.000Z')"); err != nil {
			t.Fatalf("run: %v", err)
		}
		if err := s.IndexKnowledge(ctx, s, Row{"id": int64(7), "title": "Ports", "body": "the router ports"}, []string{"routing", "infra"}); err != nil {
			t.Fatalf("index: %v", err)
		}
		rows := s.SearchKnowledge(ctx, "infra", 10)
		if len(rows) != 1 || rows[0]["id"] != int64(7) {
			t.Fatalf("rows: %v", rows)
		}
	})
	t.Run("does nothing when FTS5 is missing", func(t *testing.T) {
		db, err := SQLite(ctx, filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		defer db.Close()
		s := newStore(ftslessDB{db})
		if err := s.Init(ctx); err != nil {
			t.Fatalf("init: %v", err)
		}
		if err := s.IndexKnowledge(ctx, s, Row{"id": int64(1), "title": "t", "body": "b"}, nil); err != nil {
			t.Fatalf("index: %v", err)
		}
		if rows := s.SearchKnowledge(ctx, "b", 10); len(rows) != 0 {
			t.Fatalf("nothing indexed, got %v", rows)
		}
	})
}

// TestOpenLegacyChannel opens a channel file an earlier release's server wrote, holding rows
// in every table, and reads and searches it unchanged. The fixture is frozen: it is the file
// that release wrote.
func TestOpenLegacyChannel(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "channel_legacy.sqlite")
	data, err := os.ReadFile("testdata/channel_legacy.sqlite")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(file, data, fs.FileMode(0o600)); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	before := func() map[string]string {
		db, err := SQLite(ctx, file)
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		defer db.Close()
		return objects(t, db)
	}()
	s, err := Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Run("leaves the schema untouched", func(t *testing.T) {
		after := objects(t, s)
		// everything the earlier release wrote keeps its shape; the current schema may add
		// objects of its own (opening the file upgrades it), but it changes nothing that was there
		for name, kind := range before {
			if after[name] != kind {
				t.Errorf("%s changed from %s to %s", name, kind, after[name])
			}
		}
	})
	t.Run("reads every table's rows", func(t *testing.T) {
		for table, want := range map[string]int64{
			"meta": 2, "sessions": 1, "events": 2, "phases": 1, "tasks": 2,
			"edits": 1, "notes": 1, "knowledge": 2, "touches": 1,
		} {
			r, err := s.Get(ctx, "SELECT COUNT(*) AS n FROM "+table)
			if err != nil {
				t.Fatalf("count %s: %v", table, err)
			}
			if r["n"] != want {
				t.Errorf("%s holds %v rows, want %d", table, r["n"], want)
			}
		}
		r, err := s.Get(ctx, "SELECT label, context FROM sessions WHERE name = 'ada'")
		if err != nil {
			t.Fatalf("session: %v", err)
		}
		if r["label"] != "Ada" || r["context"] != "{\"repo\":\"x\"}" {
			t.Fatalf("session row: %v", r)
		}
	})
	t.Run("searches what the fixture indexed", func(t *testing.T) {
		hits := s.SearchTasks(ctx, "router")
		if len(hits) != 1 || hits[0]["id"] != "t1" {
			t.Fatalf("task hits: %v", hits)
		}
		if hit, _ := hits[0]["hit"].(string); !strings.Contains(hit, "«router»") {
			t.Fatalf("hit %q marks the match", hit)
		}
		if hits := s.SearchTasks(ctx, "changelog"); len(hits) != 1 || hits[0]["id"] != "t2" {
			t.Fatalf("task hits: %v", hits)
		}
		rows := s.SearchKnowledge(ctx, "kimi", 10)
		if len(rows) != 1 || rows[0]["title"] != "Router ports" {
			t.Fatalf("knowledge rows: %v", rows)
		}
		if hit, _ := rows[0]["hit"].(string); !strings.Contains(hit, "«kimi»") {
			t.Fatalf("hit %q marks the match", hit)
		}
		if rows := s.SearchKnowledge(ctx, "guillemets", 10); len(rows) != 1 || rows[0]["title"] != "Search snippets" {
			t.Fatalf("knowledge rows: %v", rows)
		}
	})
}
