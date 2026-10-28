package history

import (
	"database/sql"
	"fmt"
)

// SqlValue is one bound parameter or one column value: a string, an int64, a float64, a
// []byte blob or nil. Integer columns read as int64.
type SqlValue = any

// DB is the synchronous SQLite surface the history store reads and writes through: every
// query answers fully materialised rows, so no reader ever holds a cursor open across the
// next statement.
type DB interface {
	// Exec runs one or more statements, discarding the rows they produce.
	Exec(query string) error
	// All answers every row a query matches, an empty slice when it matches none.
	All(query string, params ...SqlValue) ([]map[string]SqlValue, error)
	// Get answers the first row a query matches, or nil when it matches none.
	Get(query string, params ...SqlValue) (map[string]SqlValue, error)
	// Run executes one statement and answers how many rows it changed.
	Run(query string, params ...SqlValue) (int64, error)
	Close() error
}

// openPragmas ride in the DSN so every pooled connection opens with them set: the file is
// read while the server writes it, so WAL, the busy timeout that waits out a writer, and
// checkpoint_fullfsync, because on macOS a plain fsync leaves the drive's cache unflushed
// and a power loss mid-checkpoint tears the main file.
const openPragmas = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&" +
	"_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=checkpoint_fullfsync(1)"

// OpenDB opens (and creates when missing) the database file.
func OpenDB(file string) (DB, error) {
	pool, err := sql.Open("sqlite", file+"?"+openPragmas)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	// one connection serves every statement: a transaction's BEGIN and COMMIT, and every
	// statement between them, must land on the same connection
	pool.SetMaxOpenConns(1)
	// the ping opens the first connection, so an unusable path fails here, on open, the way
	// the runtimes' open does, not on the first statement
	if err := pool.Ping(); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("open %s: %w", file, err)
	}
	return &sqliteDB{pool: pool}, nil
}

// sqliteDB answers the DB surface from database/sql.
type sqliteDB struct {
	pool *sql.DB
}

func (s *sqliteDB) Exec(query string) error {
	if _, err := s.pool.Exec(query); err != nil {
		return fmt.Errorf("exec %q: %w", query, err)
	}
	return nil
}

func (s *sqliteDB) All(query string, params ...SqlValue) ([]map[string]SqlValue, error) {
	rows, err := s.pool.Query(query, params...)
	if err != nil {
		return nil, fmt.Errorf("all %q: %w", query, err)
	}
	defer rows.Close()
	out := []map[string]SqlValue{}
	for rows.Next() {
		row, err := rowOf(rows)
		if err != nil {
			return nil, fmt.Errorf("all %q: %w", query, err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("all %q: %w", query, err)
	}
	return out, nil
}

func (s *sqliteDB) Get(query string, params ...SqlValue) (map[string]SqlValue, error) {
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
	row, err := rowOf(rows)
	if err != nil {
		return nil, fmt.Errorf("get %q: %w", query, err)
	}
	return row, nil
}

func (s *sqliteDB) Run(query string, params ...SqlValue) (int64, error) {
	result, err := s.pool.Exec(query, params...)
	if err != nil {
		return 0, fmt.Errorf("run %q: %w", query, err)
	}
	changes, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("run %q: %w", query, err)
	}
	return changes, nil
}

func (s *sqliteDB) Close() error {
	return s.pool.Close()
}

// rowOf scans the row under the cursor into a map keyed by column name; a name two columns
// share keeps the last one, the way a row object builds its keys.
func rowOf(rows *sql.Rows) (map[string]SqlValue, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]SqlValue, len(columns))
	scan := make([]any, len(columns))
	for at := range values {
		scan[at] = &values[at]
	}
	if err := rows.Scan(scan...); err != nil {
		return nil, err
	}
	row := make(map[string]SqlValue, len(columns))
	for at, name := range columns {
		row[name] = values[at]
	}
	return row, nil
}
