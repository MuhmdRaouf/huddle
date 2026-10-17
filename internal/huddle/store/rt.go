// The sqlite driver face: one connection to a channel's file, the whole surface the store
// drives — statements without parameters (schema, pragmas, transaction control) and
// parameterised queries.
package store

import (
	"context"
	"database/sql"

	// the sqlite driver, FTS5 included, registered under the name "sqlite"
	_ "modernc.org/sqlite"
)

// Row is one result row, keyed by column name, in SQLite's own types: TEXT string, INTEGER
// int64, REAL float64, BLOB []byte, NULL nil.
type Row map[string]any

// DB is an open channel database on exactly one connection: the server is the file's only
// writer, and a second connection would see none of this one's open transaction.
type DB struct {
	pool *sql.DB
	conn *sql.Conn
}

// SQLite opens file, creating it when missing; ":memory:" is an in-memory database.
func SQLite(ctx context.Context, file string) (*DB, error) {
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	conn, err := pool.Conn(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool, conn: conn}, nil
}

// Exec runs one or more statements without parameters.
func (d *DB) Exec(ctx context.Context, stmts string) error {
	_, err := d.conn.ExecContext(ctx, stmts)
	return err
}

// All runs one statement and returns every row it yields; an answer with no rows is an empty
// slice, never nil.
func (d *DB) All(ctx context.Context, query string, params ...any) ([]Row, error) {
	rows, err := d.conn.QueryContext(ctx, query, bind(params)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []Row{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := make(Row, len(cols))
		for i, c := range cols {
			r[c] = vals[i]
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns the first row, nil when the answer holds none.
func (d *DB) Get(ctx context.Context, query string, params ...any) (Row, error) {
	rows, err := d.All(ctx, query, params...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// Run runs one write statement with parameters.
func (d *DB) Run(ctx context.Context, stmt string, params ...any) error {
	_, err := d.conn.ExecContext(ctx, stmt, bind(params)...)
	return err
}

// Close ends the session.
func (d *DB) Close() error {
	err := d.conn.Close()
	if poolErr := d.pool.Close(); err == nil {
		err = poolErr
	}
	return err
}

// bind prepares parameters the way both drivers accept them: a boolean binds as 1/0; nil,
// integers, reals, strings and bytes pass through as they are.
func bind(params []any) []any {
	out := make([]any, len(params))
	for i, p := range params {
		if b, ok := p.(bool); ok {
			if b {
				out[i] = int64(1)
			} else {
				out[i] = int64(0)
			}
			continue
		}
		out[i] = p
	}
	return out
}
