package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	_ "modernc.org/sqlite"
)

// DB is the synchronous SQLite surface the ledger's file answers: every query answers a
// fully materialised row, so no reader holds a cursor open across the next statement.
type DB interface {
	// Exec runs one or more statements, discarding the rows they produce.
	Exec(query string) error
	// Get answers the first row a query matches, or nil when it matches none.
	Get(query string, params ...any) (map[string]any, error)
	// All answers every row a query matches, fully materialised.
	All(query string, params ...any) ([]map[string]any, error)
	// Run executes one statement.
	Run(query string, params ...any) error
	// Tx runs fn inside one transaction on the file's connection: fn's statements commit
	// together, and any error from fn, or a panic, rolls them all back.
	Tx(fn func(tx DB) error) error
	// Close closes the file.
	Close() error
}

// openPragmas ride in the DSN so every pooled connection opens with them set: the file is
// read while the watcher writes it, so WAL, the busy timeout that waits out a writer, and
// checkpoint_fullfsync, because on macOS a plain fsync leaves the drive's cache unflushed
// and a power loss mid-checkpoint tears the main file.
const openPragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&" +
	"_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=checkpoint_fullfsync(1)"

// openDB opens (and creates when missing) the ledger's database file.
func openDB(file string) (DB, error) {
	pool, err := sql.Open("sqlite", file+"?"+openPragmas)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	// one connection serves every statement: the watcher files facts and rows from its one
	// goroutine, and a statement lands on the connection the last one used
	pool.SetMaxOpenConns(1)
	// the ping opens the first connection, so an unusable path fails here, on open, not on
	// the first statement
	if err := pool.Ping(); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	return &sqliteDB{pool: pool}, nil
}

// sqliteDB answers the DB surface from database/sql.
type sqliteDB struct{ pool *sql.DB }

func (s *sqliteDB) Exec(query string) error {
	if _, err := s.pool.Exec(query); err != nil {
		return fmt.Errorf("exec %q: %w", query, err)
	}
	return nil
}

func (s *sqliteDB) Get(query string, params ...any) (map[string]any, error) {
	rows, err := s.pool.Query(query, params...)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", query, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get %q: %w", query, err)
		}
		// no row: nil, the shape a missing row has always answered
		return nil, nil
	}
	return scanRow(rows)
}

func (s *sqliteDB) All(query string, params ...any) ([]map[string]any, error) {
	rows, err := s.pool.Query(query, params...)
	if err != nil {
		return nil, fmt.Errorf("all %q: %w", query, err)
	}
	defer rows.Close()
	return materialise(query, rows)
}

func (s *sqliteDB) Run(query string, params ...any) error {
	if _, err := s.pool.Exec(query, params...); err != nil {
		return fmt.Errorf("run %q: %w", query, err)
	}
	return nil
}

func (s *sqliteDB) Tx(fn func(tx DB) error) error {
	tx, err := s.pool.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin a transaction: %w", err)
	}
	committed := false
	// fn's error or panic rolls the transaction back; only a commit marks it done
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&txDB{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit a transaction: %w", err)
	}
	committed = true
	return nil
}

func (s *sqliteDB) Close() error { return s.pool.Close() }

// txDB answers the DB surface inside one transaction: every statement it takes commits
// with the transaction, or rolls back with it. A statement the transaction repeats — the
// backfill's facts read and row upsert, a thousand times a batch — is prepared once and
// reused, so a batch's window on the file's one connection stays one write long, not a
// thousand parses the watcher waits behind. The statements die with the transaction.
type txDB struct {
	tx   *sql.Tx
	stmt map[string]*sql.Stmt
}

// stmtFor answers the transaction's prepared form of the query, preparing it once.
func (t *txDB) stmtFor(query string) (*sql.Stmt, error) {
	if stmt, ok := t.stmt[query]; ok {
		return stmt, nil
	}
	stmt, err := t.tx.Prepare(query)
	if err != nil {
		return nil, err
	}
	if t.stmt == nil {
		t.stmt = make(map[string]*sql.Stmt)
	}
	t.stmt[query] = stmt
	return stmt, nil
}

func (t *txDB) Exec(query string) error {
	if _, err := t.tx.Exec(query); err != nil {
		return fmt.Errorf("exec %q: %w", query, err)
	}
	return nil
}

func (t *txDB) Get(query string, params ...any) (map[string]any, error) {
	stmt, err := t.stmtFor(query)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", query, err)
	}
	rows, err := stmt.Query(params...)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", query, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("get %q: %w", query, err)
		}
		return nil, nil
	}
	return scanRow(rows)
}

func (t *txDB) All(query string, params ...any) ([]map[string]any, error) {
	stmt, err := t.stmtFor(query)
	if err != nil {
		return nil, fmt.Errorf("all %q: %w", query, err)
	}
	rows, err := stmt.Query(params...)
	if err != nil {
		return nil, fmt.Errorf("all %q: %w", query, err)
	}
	defer rows.Close()
	return materialise(query, rows)
}

func (t *txDB) Run(query string, params ...any) error {
	stmt, err := t.stmtFor(query)
	if err != nil {
		return fmt.Errorf("run %q: %w", query, err)
	}
	if _, err := stmt.Exec(params...); err != nil {
		return fmt.Errorf("run %q: %w", query, err)
	}
	return nil
}

func (t *txDB) Tx(fn func(tx DB) error) error {
	return errors.New("already in a transaction")
}

func (t *txDB) Close() error {
	return errors.New("close the store, not a transaction")
}

// scanRow scans the row under the cursor into a map keyed by column name; a name two
// columns share keeps the last one, the way a row object builds its keys.
func scanRow(rows *sql.Rows) (map[string]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]any, len(columns))
	scan := make([]any, len(columns))
	for at := range values {
		scan[at] = &values[at]
	}
	if err := rows.Scan(scan...); err != nil {
		return nil, err
	}
	row := make(map[string]any, len(columns))
	for at, name := range columns {
		row[name] = values[at]
	}
	return row, nil
}

// materialise reads every row under the cursor into maps, so the caller never holds the
// cursor open past the answer.
func materialise(query string, rows *sql.Rows) ([]map[string]any, error) {
	var out []map[string]any
	for rows.Next() {
		found, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", query, err)
		}
		out = append(out, found)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%q: %w", query, err)
	}
	return out, nil
}
