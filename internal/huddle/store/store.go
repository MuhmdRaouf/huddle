// The storage behind a channel: one SQLite file (WAL, foreign keys) with FTS5 indexes for
// search (« » around the matches in snippets). A SQLite without FTS5 still works: FTS stays
// false and search falls back to LIKE.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Q is what a query runs on: the store itself, or the transaction it opened.
type Q interface {
	All(ctx context.Context, query string, params ...any) ([]Row, error)
	Get(ctx context.Context, query string, params ...any) (Row, error)
	Run(ctx context.Context, stmt string, params ...any) error
}

// TaskDoc is one task's effective text (owner edits applied), the unit of the task search
// index.
type TaskDoc struct {
	ID    string
	Title string
	Body  string
}

// dbDriver is the driver face the store drives: Q for queries, Exec for the raw
// multi-statement SQL of schema and transaction control, Close to end the session.
type dbDriver interface {
	Q
	Exec(ctx context.Context, stmts string) error
	Close() error
}

// ddl creates a fresh channel database. {ID} is the integer key that numbers rows; added lists
// columns a channel file may lack — opening the file adds them.
const ddl = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS sessions (
  name TEXT PRIMARY KEY, label TEXT, role TEXT, joined_at TEXT NOT NULL, last_seen TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'idle', task TEXT, step TEXT,
  control TEXT NOT NULL DEFAULT 'run' CHECK (control IN ('run','pause')), control_by TEXT, control_at TEXT,
  cursor INTEGER NOT NULL DEFAULT 0, parent TEXT, context TEXT, brief TEXT
);
CREATE TABLE IF NOT EXISTS events (
  seq {ID}, ts TEXT NOT NULL, from_name TEXT NOT NULL, to_name TEXT,
  topic TEXT NOT NULL, ref TEXT, msg TEXT, data TEXT, reply_to INTEGER, needs_reply INTEGER NOT NULL DEFAULT 0, key TEXT
);
CREATE INDEX IF NOT EXISTS events_to ON events(to_name, seq);
CREATE INDEX IF NOT EXISTS events_reply ON events(reply_to, from_name);
CREATE INDEX IF NOT EXISTS events_open ON events(needs_reply, seq) WHERE needs_reply = 1;
CREATE UNIQUE INDEX IF NOT EXISTS events_key ON events(from_name, key) WHERE key IS NOT NULL;
CREATE TABLE IF NOT EXISTS phases (n INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT);
CREATE TABLE IF NOT EXISTS tasks (
  id TEXT PRIMARY KEY, phase INTEGER NOT NULL DEFAULT 0, ord INTEGER NOT NULL DEFAULT 0, title TEXT NOT NULL,
  owner TEXT, status TEXT NOT NULL DEFAULT 'todo' CHECK (status IN ('todo','doing','done','blocked','skipped')),
  after TEXT NOT NULL DEFAULT '[]', body TEXT NOT NULL DEFAULT '{}', origin TEXT NOT NULL DEFAULT 'session',
  created_by TEXT, created_at TEXT NOT NULL, status_by TEXT, status_at TEXT, status_note TEXT
);
CREATE INDEX IF NOT EXISTS tasks_order ON tasks(phase, ord);
CREATE TABLE IF NOT EXISTS edits (
  task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, field TEXT NOT NULL, value TEXT NOT NULL,
  by TEXT, at TEXT NOT NULL, PRIMARY KEY (task_id, field)
);
CREATE TABLE IF NOT EXISTS notes (
  id {ID}, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  kind TEXT NOT NULL, body TEXT NOT NULL, by TEXT, resolved INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS notes_task ON notes(task_id);
CREATE TABLE IF NOT EXISTS knowledge (
  id {ID}, by TEXT NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
  tags TEXT NOT NULL DEFAULT '[]', refs TEXT NOT NULL DEFAULT '[]', task_id TEXT, supersedes INTEGER, created_at TEXT NOT NULL,
  hits INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS touches (
  session TEXT NOT NULL, repo TEXT NOT NULL, path TEXT NOT NULL, at TEXT NOT NULL, warned_at TEXT,
  PRIMARY KEY (session, repo, path)
);
CREATE INDEX IF NOT EXISTS touches_path ON touches(repo, path, at);
CREATE TABLE IF NOT EXISTS reservations (
  path TEXT PRIMARY KEY, repo TEXT NOT NULL DEFAULT '', rel TEXT NOT NULL DEFAULT '',
  holder TEXT NOT NULL, note TEXT, created TEXT NOT NULL, expires TEXT NOT NULL
);
`

// added: [table, column definition] pairs an older channel file may lack, in the order they
// were added. touches holds which files a session edited; knowledge rows grow the bookkeeping
// of verification and of the move to the server-wide store.
var added = [][2]string{
	{"sessions", "parent TEXT"}, {"sessions", "context TEXT"}, {"sessions", "brief TEXT"},
	{"knowledge", "verified_by TEXT"}, {"knowledge", "verified_at TEXT"}, {"knowledge", "moved_to INTEGER"}, {"knowledge", "origin TEXT"},
}

// Store is one channel's SQLite file. One connection serves it, so the Q a transaction hands
// its callback is the connection the transaction itself runs on.
type Store struct {
	FTS   bool
	db    dbDriver
	txMu  sync.Mutex   // one connection, one user: a transaction and any statement exclude each other
	depth atomic.Int32 // open transactions: a transaction inside one is a savepoint
}

var _ Q = (*Store)(nil)

func newStore(d dbDriver) *Store {
	return &Store{FTS: true, db: d}
}

// Open opens one channel's SQLite file, creating the file and its directory chain when
// missing; ":memory:" is an in-memory database.
func Open(ctx context.Context, file string) (*Store, error) {
	if file != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return nil, err
		}
	}
	db, err := SQLite(ctx, file)
	if err != nil {
		return nil, err
	}
	if err := db.Exec(ctx, "PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL; PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, err
	}
	return newStore(db), nil
}

func (s *Store) All(ctx context.Context, query string, params ...any) ([]Row, error) {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return s.db.All(ctx, query, params...)
}

func (s *Store) Get(ctx context.Context, query string, params ...any) (Row, error) {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return s.db.Get(ctx, query, params...)
}

func (s *Store) Run(ctx context.Context, stmt string, params ...any) error {
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return s.db.Run(ctx, stmt, params...)
}

// txCtx marks the context a transaction hands its callback, so a Tx called inside that
// callback knows it is a savepoint of the transaction that holds the lock, not a second one.
type txCtx struct{ store *Store }

// Tx runs fn in one transaction; fn must query through the Q it is given, never the store. The
// transaction holds the store's lock, so one connection's BEGIN, its statements and its COMMIT
// are one user's, whatever else this database serves meanwhile — a satellite link's handler and
// the dashboard's write land one after the other, never inside each other. A Tx called with the
// context of a running transaction's callback is a savepoint of that transaction: the lock is
// already this call chain's. A rollback that itself fails is swallowed: the callback's error is
// the one to report.
func (s *Store) Tx(ctx context.Context, fn func(ctx context.Context, q Q) error) error {
	if _, inside := ctx.Value(txCtx{s}).(bool); inside && s.depth.Load() > 0 {
		return s.tx(ctx, fn)
	}
	s.txMu.Lock()
	defer s.txMu.Unlock()
	return s.tx(context.WithValue(ctx, txCtx{s}, true), fn)
}

// tx is one BEGIN..COMMIT (or SAVEPOINT..RELEASE, when a transaction already runs on this call
// chain), with the lock already held by the caller when it is not the outermost.
func (s *Store) tx(ctx context.Context, fn func(ctx context.Context, q Q) error) error {
	name := ""
	if depth := s.depth.Load(); depth > 0 {
		name = fmt.Sprintf("sp%d", depth)
	}
	begin, end := "BEGIN", "COMMIT"
	if name != "" {
		begin, end = "SAVEPOINT "+name, "RELEASE "+name
	}
	if err := s.db.Exec(ctx, begin); err != nil {
		return err
	}
	s.depth.Add(1)
	err := fn(ctx, s.db)
	s.depth.Add(-1)
	// The end runs whatever became of the caller's context: a request cancelled mid-transaction
	// still closes it, or the store's one connection stays inside it and refuses every BEGIN.
	done := context.WithoutCancel(ctx)
	if err == nil {
		if err = s.db.Exec(done, end); err == nil {
			return nil
		}
	}
	if name != "" {
		_ = s.db.Exec(done, "ROLLBACK TO "+name+"; RELEASE "+name)
	} else {
		_ = s.db.Exec(done, "ROLLBACK")
	}
	return err
}

// Close ends the session.
func (s *Store) Close() error {
	return s.db.Close()
}

// Init creates the schema when the file is fresh and adds the columns an older file lacks.
// The FTS5 indexes come last: a SQLite built without FTS5 leaves FTS false and search falls
// back to LIKE.
func (s *Store) Init(ctx context.Context) error {
	if err := s.db.Exec(ctx, strings.ReplaceAll(ddl, "{ID}", "INTEGER PRIMARY KEY AUTOINCREMENT")); err != nil {
		return err
	}
	for _, c := range added {
		// a column the file already has fails, and the file keeps the one it has
		_ = s.db.Exec(ctx, "ALTER TABLE "+c[0]+" ADD COLUMN "+c[1])
	}
	if err := s.db.Exec(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS tasks_fts USING fts5(id UNINDEXED, title, body, tokenize='porter unicode61')`); err != nil {
		s.FTS = false
		return nil
	}
	return s.db.Exec(ctx, `CREATE VIRTUAL TABLE IF NOT EXISTS knowledge_fts USING fts5(title, body, tags, content='knowledge', content_rowid='id', tokenize='porter unicode61')`)
}

// IndexTasks replaces the search index of the tasks named in ids (nil ids is the whole index)
// with rows' text, through the Q the caller's transaction handed it.
func (s *Store) IndexTasks(ctx context.Context, q Q, rows []TaskDoc, ids []string) error {
	if !s.FTS {
		return nil
	}
	if ids == nil {
		if err := q.Run(ctx, "DELETE FROM tasks_fts"); err != nil {
			return err
		}
	} else {
		for _, id := range ids {
			if err := q.Run(ctx, "DELETE FROM tasks_fts WHERE id=?", id); err != nil {
				return err
			}
		}
	}
	for _, r := range rows {
		if err := q.Run(ctx, "INSERT INTO tasks_fts (id, title, body) VALUES (?,?,?)", r.ID, r.Title, r.Body); err != nil {
			return err
		}
	}
	return nil
}

// fts5 turns the words of a query into an FTS5 prefix match joined by op: " " matches every
// word, " OR " any word. A quote inside a word doubles, the FTS5 escape.
func fts5(query string, op string) string {
	words := splitSpace(query)
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"*`
	}
	return strings.Join(out, op)
}

// SearchTasks answers the tasks matching every word, {id, title, hit} best first, 80 at most;
// nil when the index cannot answer, the signal to fall back to LIKE.
func (s *Store) SearchTasks(ctx context.Context, query string) []Row {
	if !s.FTS {
		return nil
	}
	rows, err := s.All(ctx, `SELECT id, title, snippet(tasks_fts, 2, '«', '»', ' … ', 14) AS hit FROM tasks_fts WHERE tasks_fts MATCH ? ORDER BY rank LIMIT 80`, fts5(query, " "))
	if err != nil {
		return nil
	}
	return rows
}

// SearchKnowledge answers the knowledge rows matching any word, with a snippet of the body in
// hit and a relevance score (higher is better); none when the index cannot answer.
func (s *Store) SearchKnowledge(ctx context.Context, query string, limit int) []Row {
	rows, err := s.All(ctx, `SELECT k.*, snippet(knowledge_fts, 1, '«', '»', ' … ', 18) AS hit, -f.rank AS score FROM knowledge_fts f JOIN knowledge k ON k.id = f.rowid
        WHERE knowledge_fts MATCH ? ORDER BY rank LIMIT ?`, fts5(query, " OR "), limit)
	if err != nil {
		return []Row{}
	}
	return rows
}

// IndexKnowledge adds one new knowledge row to the index, its tags joined by spaces.
func (s *Store) IndexKnowledge(ctx context.Context, q Q, r Row, tags []string) error {
	if !s.FTS {
		return nil
	}
	return q.Run(ctx, "INSERT INTO knowledge_fts (rowid, title, body, tags) VALUES (?,?,?,?)", r["id"], r["title"], r["body"], strings.Join(tags, " "))
}

// splitSpace splits on runs of whitespace the way String.prototype.split(/\s+/) does:
// whitespace at either end yields empty strings at the ends, and an empty string yields one
// empty token.
func splitSpace(s string) []string {
	var parts []string
	start, inSpace := 0, false
	for i, r := range s {
		switch {
		case isSpace(r):
			if !inSpace {
				parts = append(parts, s[start:i])
				inSpace = true
			}
		case inSpace:
			start, inSpace = i, false
		}
	}
	return append(parts, s[start:])
}

// isSpace matches JavaScript's \s class.
func isSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}
