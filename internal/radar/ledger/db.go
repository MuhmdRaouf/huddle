package ledger

import (
	"database/sql"
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
	// Run executes one statement.
	Run(query string, params ...any) error
	// Close closes the file.
	Close() error
}

// openPragmas ride in the DSN so every pooled connection opens with them set: the file is
// read while the watcher writes it, so WAL, and the busy timeout that waits out a writer.
const openPragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&" +
	"_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"

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

func (s *sqliteDB) Run(query string, params ...any) error {
	if _, err := s.pool.Exec(query, params...); err != nil {
		return fmt.Errorf("run %q: %w", query, err)
	}
	return nil
}

func (s *sqliteDB) Close() error { return s.pool.Close() }

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
