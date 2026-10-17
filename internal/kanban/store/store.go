// Package store is the kanban board's database: one kanban.db file holding every ticket
// keyed by id, the whole card text as one value. Numbered migrations bring the file to
// SchemaVersion at Open, and an older kanban refuses a newer file. Times are RFC 3339
// UTC strings the caller supplies; the store never reads the clock.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	// the sqlite driver, registered under the name "sqlite"
	_ "modernc.org/sqlite"
)

// openParams are the pragmas every connection opens with: a busy timeout so two kanban
// processes on one file wait out each other's writes instead of failing, write-ahead
// logging so a reader never blocks a writer, foreign keys on, and every transaction
// taking the write lock at BEGIN — a deferred one that reads and then writes fails at
// once with SQLITE_BUSY when another connection committed in between, whatever the
// busy timeout.
const openParams = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_txlock=immediate"

// q is what a statement runs on: the pool Open opened, or the one transaction a Tx runs
// its function on. *sql.DB and *sql.Tx both satisfy it.
type q interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Ticket is one card: the whole file text as it lays today.
type Ticket struct {
	ID        string
	Column    string
	Text      string
	UpdatedAt string
}

// Store is one kanban.db file.
type Store struct {
	db *sql.DB
	q  q
	tx *sql.Tx // the transaction Tx bound this store to; nil on the pool-backed store
}

// NewerError names a database a newer kanban wrote: an older one refuses it and leaves
// the file untouched.
type NewerError struct {
	File      string
	Version   int
	Supported int
}

func (e *NewerError) Error() string {
	return fmt.Sprintf("%s is version %d; this kanban reads up to %d", e.File, e.Version, e.Supported)
}

// NotFoundError names a ticket id the file holds no row for.
type NotFoundError struct {
	ID string
}

func (e *NotFoundError) Error() string { return "no ticket " + e.ID }

// Open opens the kanban.db at file, creating it and its parent directory when missing,
// and migrates it to SchemaVersion.
func Open(ctx context.Context, file string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", file+"?"+openParams)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, q: db}
	if err := s.migrate(ctx, file); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the file.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate brings the file to SchemaVersion, running every migration it lacks.
func (s *Store) migrate(ctx context.Context, file string) error {
	version, err := s.version(ctx)
	if err != nil {
		return err
	}
	if version > SchemaVersion {
		return &NewerError{File: file, Version: version, Supported: SchemaVersion}
	}
	for n := version; n < SchemaVersion; n++ {
		if err := s.apply(ctx, n+1, migrations[n]); err != nil {
			return fmt.Errorf("migration %d: %w", n+1, err)
		}
	}
	return nil
}

// version reads the file's schema version out of meta. A file with no meta table was
// never migrated, and a value that parses as no number reads as 0, so every migration
// applies.
func (s *Store) version(ctx context.Context) (int, error) {
	var fresh int
	if err := s.scalar(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'", &fresh); err != nil {
		return 0, fmt.Errorf("look for the meta table: %w", err)
	}
	if fresh == 0 {
		return 0, nil
	}
	var value string
	if err := s.scalar(ctx, "SELECT value FROM meta WHERE key = 'schema_version'", &value); err != nil {
		return 0, fmt.Errorf("read the schema version: %w", err)
	}
	if n, err := strconv.Atoi(value); err == nil {
		return n, nil
	}
	return 0, nil
}

// scalar runs one query and scans its first row's single column; a query answering no
// row leaves dest alone.
func (s *Store) scalar(ctx context.Context, query string, dest any) error {
	rows, err := s.q.QueryContext(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	if !rows.Next() {
		return rows.Err()
	}
	return rows.Scan(dest)
}

// apply runs one migration's statements and records the version it brought the file to,
// in one transaction: a crash leaves the file either before or after a migration, never
// half of either.
func (s *Store) apply(ctx context.Context, n int, statements string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, statements); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)", strconv.Itoa(n)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Get returns the ticket with the id.
func (s *Store) Get(ctx context.Context, id string) (Ticket, error) {
	rows, err := s.q.QueryContext(ctx, "SELECT id, col, text, updated_at FROM tickets WHERE id = ?", id)
	if err != nil {
		return Ticket{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Ticket{}, err
		}
		return Ticket{}, &NotFoundError{ID: id}
	}
	var t Ticket
	if err := rows.Scan(&t.ID, &t.Column, &t.Text, &t.UpdatedAt); err != nil {
		return Ticket{}, err
	}
	return t, nil
}

// Column returns the tickets of one column, ordered by id.
func (s *Store) Column(ctx context.Context, col string) ([]Ticket, error) {
	return s.selectTickets(ctx, "SELECT id, col, text, updated_at FROM tickets WHERE col = ? ORDER BY id", col)
}

// All returns every ticket, ordered by column then id.
func (s *Store) All(ctx context.Context) ([]Ticket, error) {
	return s.selectTickets(ctx, "SELECT id, col, text, updated_at FROM tickets ORDER BY col, id")
}

// selectTickets runs one query and scans its rows; an answer with no rows is an empty
// slice, never nil.
func (s *Store) selectTickets(ctx context.Context, query string, args ...any) ([]Ticket, error) {
	rows, err := s.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tickets := []Ticket{}
	for rows.Next() {
		var t Ticket
		if err := rows.Scan(&t.ID, &t.Column, &t.Text, &t.UpdatedAt); err != nil {
			return nil, err
		}
		tickets = append(tickets, t)
	}
	return tickets, rows.Err()
}

// Put writes the ticket whole: an id already in the file has its column, text and time
// replaced.
func (s *Store) Put(ctx context.Context, t Ticket) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO tickets (id, col, text, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET col = excluded.col, text = excluded.text, updated_at = excluded.updated_at`,
		t.ID, t.Column, t.Text, t.UpdatedAt)
	return err
}

// Move moves one ticket to toCol with the text and time given.
func (s *Store) Move(ctx context.Context, id, toCol, text, at string) error {
	res, err := s.q.ExecContext(ctx,
		"UPDATE tickets SET col = ?, text = ?, updated_at = ? WHERE id = ?", toCol, text, at, id)
	if err != nil {
		return err
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if moved == 0 {
		return &NotFoundError{ID: id}
	}
	return nil
}

// Delete removes the ticket with the id.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.q.ExecContext(ctx, "DELETE FROM tickets WHERE id = ?", id)
	if err != nil {
		return err
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if removed == 0 {
		return &NotFoundError{ID: id}
	}
	return nil
}

// GetMeta returns the value of one meta row; ok is false when the file holds no
// row for the key.
func (s *Store) GetMeta(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetMeta writes one meta row whole.
func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.q.ExecContext(ctx, "INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, value)
	return err
}

// DeleteMeta removes one meta row; a missing key is a NotFoundError naming it.
func (s *Store) DeleteMeta(ctx context.Context, key string) error {
	res, err := s.q.ExecContext(ctx, "DELETE FROM meta WHERE key = ?", key)
	if err != nil {
		return err
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if removed == 0 {
		return &NotFoundError{ID: key}
	}
	return nil
}

// Tx runs fn against a store bound to one transaction: the writes fn makes land whole
// or not at all. A nil from fn commits; any other error rolls the transaction back and
// comes back from Tx. A Tx on a tx-bound store runs in that same transaction, so only
// the outermost one commits.
func (s *Store) Tx(ctx context.Context, fn func(tx *Store) error) error {
	if s.tx != nil {
		return fn(s)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	inner := &Store{db: s.db, q: tx, tx: tx}
	if err := fn(inner); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
