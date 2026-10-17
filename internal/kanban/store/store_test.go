package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	// the sqlite driver, registered under the name "sqlite"
	_ "modernc.org/sqlite"
)

// at and at2 are two stamps; the store never reads the clock, so tests name one.
const (
	at  = "2026-10-16T12:00:00Z"
	at2 = "2026-10-16T12:01:00Z"
)

// testFile is the one kanban.db path of a test, in its own temp directory.
func testFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "kanban.db")
}

// openAt opens the store at file and closes it when the test ends.
func openAt(t *testing.T, file string) *Store {
	t.Helper()
	s, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// open opens one test's own kanban.db.
func open(t *testing.T) *Store {
	t.Helper()
	return openAt(t, testFile(t))
}

// raw opens the file straight through the driver, without the store, so a test can lay
// a state the store itself would never write.
func raw(t *testing.T, file string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open %s raw: %v", file, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// setMetaVersion writes the version row by hand.
func setMetaVersion(t *testing.T, file, version string) {
	t.Helper()
	if _, err := raw(t, file).Exec(
		"INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', ?)", version); err != nil {
		t.Fatalf("set the version to %q: %v", version, err)
	}
}

// metaVersion reads the version row back.
func metaVersion(t *testing.T, file string) string {
	t.Helper()
	var got string
	if err := raw(t, file).QueryRow(
		"SELECT value FROM meta WHERE key = 'schema_version'").Scan(&got); err != nil {
		t.Fatalf("read the version: %v", err)
	}
	return got
}

// tableNames lists the file's tables in name order.
func tableNames(t *testing.T, file string) []string {
	t.Helper()
	rows, err := raw(t, file).Query(
		"SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
	if err != nil {
		t.Fatalf("list the tables: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan a table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list the tables: %v", err)
	}
	return names
}

func TestOpenCreatesTheFileAtTheCurrentVersion(t *testing.T) {
	file := testFile(t)
	openAt(t, file)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("stat %s: %v", file, err)
	}
	if got, want := metaVersion(t, file), strconv.Itoa(SchemaVersion); got != want {
		t.Fatalf("meta schema_version = %q, want %q", got, want)
	}
	if got, want := tableNames(t, file), []string{"meta", "tickets"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}
	if _, err := openAt(t, file).Get(context.Background(), "KBN-1"); !errors.As(err, new(*NotFoundError)) {
		t.Fatalf("Get on the fresh file = %v, want a *NotFoundError", err)
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	file := testFile(t)
	first := openAt(t, file)
	want := Ticket{ID: "KBN-1", Column: "backlog", Text: "card", UpdatedAt: at}
	if err := first.Put(context.Background(), want); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, err := openAt(t, file).Get(context.Background(), "KBN-1")
	if err != nil {
		t.Fatalf("get after the reopen: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ticket after the reopen = %+v, want %+v", got, want)
	}
	if got, want := metaVersion(t, file), strconv.Itoa(SchemaVersion); got != want {
		t.Fatalf("meta schema_version after the reopen = %q, want %q", got, want)
	}
}

func TestOpenRefusesANewerFile(t *testing.T) {
	file := testFile(t)
	if err := openAt(t, file).Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	setMetaVersion(t, file, strconv.Itoa(SchemaVersion+1))

	_, err := Open(context.Background(), file)
	var newer *NewerError
	if !errors.As(err, &newer) {
		t.Fatalf("Open = %v, want a *NewerError", err)
	}
	if newer.File != file || newer.Version != SchemaVersion+1 || newer.Supported != SchemaVersion {
		t.Fatalf("NewerError = %+v, want file %s, version %d, supported %d",
			newer, file, SchemaVersion+1, SchemaVersion)
	}
	want := fmt.Sprintf("%s is version %d; this kanban reads up to %d", file, SchemaVersion+1, SchemaVersion)
	if newer.Error() != want {
		t.Fatalf("message = %q, want %q", newer.Error(), want)
	}
	if got := metaVersion(t, file); got != strconv.Itoa(SchemaVersion+1) {
		t.Fatalf("version after the refusal = %q, want it untouched at %q", got, strconv.Itoa(SchemaVersion+1))
	}
}

func TestOpenReadsAVersionItCannotParseAsFresh(t *testing.T) {
	file := testFile(t)
	openAt(t, file)
	setMetaVersion(t, file, "not-a-number")

	_, err := Open(context.Background(), file)
	if err == nil || !strings.Contains(err.Error(), "migration 1") {
		t.Fatalf("Open = %v, want the migration 1 failure a meta table without a version owes", err)
	}
}

func TestPutGetRoundTripsTheWholeText(t *testing.T) {
	s := open(t)
	want := Ticket{
		ID:     "KBN-1",
		Column: "in-progress",
		Text: "---\nid: KBN-1\ntitle: Grüße aus Zürich — 世界 🗺️\n" +
			"---\n\n# Body\n\nA line with \"quotes\" and a\ttab.\n\n## Log\n- 12:00 actor note\n",
		UpdatedAt: at,
	}
	if err := s.Put(context.Background(), want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(context.Background(), "KBN-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPutReplacesAnExistingTicket(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Put(ctx, Ticket{ID: "KBN-1", Column: "backlog", Text: "first", UpdatedAt: at}); err != nil {
		t.Fatalf("put: %v", err)
	}
	want := Ticket{ID: "KBN-1", Column: "review", Text: "second", UpdatedAt: at2}
	if err := s.Put(ctx, want); err != nil {
		t.Fatalf("put again: %v", err)
	}
	got, err := s.Get(ctx, "KBN-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want the second write %+v", got, want)
	}
	if all, err := s.All(ctx); err != nil || len(all) != 1 {
		t.Fatalf("All = %v, %v; want the one replaced ticket", all, err)
	}
}

func TestColumnAndAllAreOrdered(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	for _, tk := range []Ticket{
		{ID: "KBN-3", Column: "backlog", Text: "three", UpdatedAt: at},
		{ID: "KBN-1", Column: "ready", Text: "one", UpdatedAt: at},
		{ID: "KBN-2", Column: "backlog", Text: "two", UpdatedAt: at},
		{ID: "KBN-5", Column: "ready", Text: "five", UpdatedAt: at},
		{ID: "KBN-4", Column: "backlog", Text: "four", UpdatedAt: at},
	} {
		if err := s.Put(ctx, tk); err != nil {
			t.Fatalf("put %s: %v", tk.ID, err)
		}
	}
	column, err := s.Column(ctx, "backlog")
	if err != nil {
		t.Fatalf("Column: %v", err)
	}
	if got, want := idsOf(column), []string{"KBN-2", "KBN-3", "KBN-4"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Column(backlog) ids = %v, want %v", got, want)
	}
	all, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if got, want := idsOf(all), []string{"KBN-2", "KBN-3", "KBN-4", "KBN-1", "KBN-5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("All ids = %v, want %v", got, want)
	}
	if empty, err := s.Column(ctx, "done"); err != nil || len(empty) != 0 {
		t.Fatalf("Column of an empty column = %v, %v; want none", empty, err)
	}
}

// idsOf lists the tickets' ids in order.
func idsOf(tickets []Ticket) []string {
	ids := make([]string, 0, len(tickets))
	for _, tk := range tickets {
		ids = append(ids, tk.ID)
	}
	return ids
}

func TestMoveChangesColumnAndText(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Put(ctx, Ticket{ID: "KBN-1", Column: "backlog", Text: "card", UpdatedAt: at}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Move(ctx, "KBN-1", "review", "card, moved", at2); err != nil {
		t.Fatalf("move: %v", err)
	}
	want := Ticket{ID: "KBN-1", Column: "review", Text: "card, moved", UpdatedAt: at2}
	got, err := s.Get(ctx, "KBN-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if column, err := s.Column(ctx, "backlog"); err != nil || len(column) != 0 {
		t.Fatalf("the old column still holds %v, %v; want it empty", column, err)
	}
}

func TestMoveOfAMissingTicketIsNotFound(t *testing.T) {
	s := open(t)
	err := s.Move(context.Background(), "KBN-9", "review", "card", at)
	var missing *NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("Move = %v, want a *NotFoundError", err)
	}
	if missing.ID != "KBN-9" || missing.Error() != "no ticket KBN-9" {
		t.Fatalf("NotFoundError = %+v, want the id KBN-9 and its message", missing)
	}
}

func TestDeleteRemovesAndAMissingOneIsNotFound(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Put(ctx, Ticket{ID: "KBN-1", Column: "backlog", Text: "card", UpdatedAt: at}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Delete(ctx, "KBN-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	err := s.Delete(ctx, "KBN-1")
	var missing *NotFoundError
	if !errors.As(err, &missing) || missing.ID != "KBN-1" {
		t.Fatalf("the second delete = %v, want a *NotFoundError for KBN-1", err)
	}
	if _, err := s.Get(ctx, "KBN-1"); !errors.As(err, new(*NotFoundError)) {
		t.Fatalf("Get after the delete = %v, want a *NotFoundError", err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if _, ok, err := s.GetMeta(ctx, "file:LOG"); err != nil || ok {
		t.Fatalf("GetMeta of a missing key = %q, %v, %v; want not found and no error", "", ok, err)
	}
	if err := s.SetMeta(ctx, "file:LOG", "one\n"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok, err := s.GetMeta(ctx, "file:LOG")
	if err != nil || !ok {
		t.Fatalf("GetMeta = %q, %v, %v; want found", got, ok, err)
	}
	if got != "one\n" {
		t.Fatalf("GetMeta = %q, want %q", got, "one\n")
	}
	if err := s.SetMeta(ctx, "file:LOG", "one\ntwo\n"); err != nil {
		t.Fatalf("set again: %v", err)
	}
	if got, ok, _ := s.GetMeta(ctx, "file:LOG"); got != "one\ntwo\n" || !ok {
		t.Fatalf("GetMeta after the second set = %q, %v, want it replaced", got, ok)
	}
	if err := s.DeleteMeta(ctx, "file:LOG"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := s.GetMeta(ctx, "file:LOG"); err != nil || ok {
		t.Fatalf("GetMeta after the delete = %v, %v; want not found", ok, err)
	}
}

func TestDeleteMetaOfAMissingKeyIsNotFound(t *testing.T) {
	s := open(t)
	err := s.DeleteMeta(context.Background(), "file:LOG")
	var missing *NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("DeleteMeta = %v, want a *NotFoundError", err)
	}
	if missing.ID != "file:LOG" || missing.Error() != "no ticket file:LOG" {
		t.Fatalf("NotFoundError = %+v, want the key file:LOG and its message", missing)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	s := open(t)
	sentinel := errors.New("stop")
	err := s.Tx(context.Background(), func(tx *Store) error {
		if err := tx.Put(context.Background(), Ticket{ID: "KBN-1", Column: "backlog", Text: "one", UpdatedAt: at}); err != nil {
			return err
		}
		if err := tx.Put(context.Background(), Ticket{ID: "KBN-2", Column: "ready", Text: "two", UpdatedAt: at}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx = %v, want the callback's error", err)
	}
	for _, id := range []string{"KBN-1", "KBN-2"} {
		if _, got := s.Get(context.Background(), id); !errors.As(got, new(*NotFoundError)) {
			t.Fatalf("Get %s after the rollback = %v, want a *NotFoundError", id, got)
		}
	}
}

func TestTxCommitsOnNil(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	want := Ticket{ID: "KBN-1", Column: "backlog", Text: "card", UpdatedAt: at}
	err := s.Tx(ctx, func(tx *Store) error {
		return tx.Put(ctx, want)
	})
	if err != nil {
		t.Fatalf("Tx: %v", err)
	}
	got, err := s.Get(ctx, "KBN-1")
	if err != nil {
		t.Fatalf("get after the commit: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTxInsideTxRunsInTheSameTransaction(t *testing.T) {
	s := open(t)
	sentinel := errors.New("stop")
	err := s.Tx(context.Background(), func(tx *Store) error {
		if err := tx.Put(context.Background(), Ticket{ID: "KBN-1", Column: "backlog", Text: "one", UpdatedAt: at}); err != nil {
			return err
		}
		if err := tx.Tx(context.Background(), func(inner *Store) error {
			return inner.Put(context.Background(), Ticket{ID: "KBN-2", Column: "ready", Text: "two", UpdatedAt: at})
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Tx = %v, want the outer callback's error", err)
	}
	for _, id := range []string{"KBN-1", "KBN-2"} {
		if _, got := s.Get(context.Background(), id); !errors.As(got, new(*NotFoundError)) {
			t.Fatalf("Get %s after the rollback = %v, want the inner write gone with the outer one too", id, got)
		}
	}
}

func TestTwoStoresTxSerialise(t *testing.T) {
	file := testFile(t)
	first := openAt(t, file)
	second := openAt(t, file)
	ctx := context.Background()
	if err := first.Put(ctx, Ticket{ID: "KBN-1", Column: "backlog", Text: "0", UpdatedAt: at}); err != nil {
		t.Fatalf("put: %v", err)
	}
	// bump reads the row, holds the transaction open past the other one's whole
	// read-write cycle, then writes the value plus one: only a transaction that
	// holds the write lock from BEGIN serialises the two.
	bump := func(s *Store) error {
		return s.Tx(ctx, func(tx *Store) error {
			row, err := tx.Get(ctx, "KBN-1")
			if err != nil {
				return err
			}
			n, err := strconv.Atoi(row.Text)
			if err != nil {
				return err
			}
			time.Sleep(200 * time.Millisecond)
			row.Text = strconv.Itoa(n + 1)
			row.UpdatedAt = at2
			return tx.Put(ctx, row)
		})
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i, s := range []*Store{first, second} {
		go func(i int, s *Store) {
			defer wg.Done()
			errs[i] = bump(s)
		}(i, s)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("store %d's Tx: %v", i, err)
		}
	}
	row, err := first.Get(ctx, "KBN-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Text != "2" {
		t.Fatalf("the row holds %q after both transactions, want \"2\"", row.Text)
	}
}

func TestTwoStoresOnOneFileSeeEachOthersWrites(t *testing.T) {
	file := testFile(t)
	writer := openAt(t, file)
	reader := openAt(t, file)
	want := Ticket{ID: "KBN-1", Column: "backlog", Text: "card", UpdatedAt: at}
	if err := writer.Put(context.Background(), want); err != nil {
		t.Fatalf("put through the first store: %v", err)
	}
	got, err := reader.Get(context.Background(), "KBN-1")
	if err != nil {
		t.Fatalf("get through the second store: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the second store read %+v, want %+v", got, want)
	}
}

// errBoom is what failingQ answers for the statement it is told to fail.
var errBoom = errors.New("boom")

// failingQ fails every statement whose SQL holds the marker and passes the rest to the
// real one, so a method's error branch is reachable without pinning a driver quirk.
type failingQ struct {
	underlying q
	marker     string
}

func (f failingQ) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, f.marker) {
		return nil, errBoom
	}
	return f.underlying.ExecContext(ctx, query, args...)
}

func (f failingQ) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, f.marker) {
		return nil, errBoom
	}
	return f.underlying.QueryContext(ctx, query, args...)
}

func (f failingQ) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return f.underlying.QueryRowContext(ctx, query, args...)
}

// failed binds a store to a q that fails the statements holding marker.
func failed(s *Store, marker string) *Store {
	return &Store{db: s.db, q: failingQ{underlying: s.db, marker: marker}}
}

func TestFailingStatementsComeBackAsErrors(t *testing.T) {
	s := open(t)
	cases := []struct {
		name   string
		marker string
		call   func(s *Store) error
	}{
		{"Put", "INSERT INTO tickets", func(x *Store) error {
			return x.Put(context.Background(), Ticket{ID: "KBN-1"})
		}},
		{"Move", "UPDATE tickets", func(x *Store) error {
			return x.Move(context.Background(), "KBN-1", "review", "card", at)
		}},
		{"Delete", "DELETE FROM tickets", func(x *Store) error {
			return x.Delete(context.Background(), "KBN-1")
		}},
		{"Get", "WHERE id = ?", func(x *Store) error {
			_, err := x.Get(context.Background(), "KBN-1")
			return err
		}},
		{"Column", "WHERE col = ?", func(x *Store) error {
			_, err := x.Column(context.Background(), "backlog")
			return err
		}},
		{"All", "ORDER BY col, id", func(x *Store) error {
			_, err := x.All(context.Background())
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(failed(s, tc.marker)); !errors.Is(err, errBoom) {
				t.Fatalf("%s = %v, want boom", tc.name, err)
			}
		})
	}
}

func TestAClosedStoreRefusesTx(t *testing.T) {
	s := open(t)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Tx(context.Background(), func(*Store) error { return nil }); err == nil {
		t.Fatal("a Tx on a closed store passed")
	}
}

func TestMigrateFailsThroughAFailingRead(t *testing.T) {
	t.Run("the meta lookup", func(t *testing.T) {
		file := testFile(t)
		db := raw(t, file)
		s := &Store{db: db, q: failingQ{underlying: db, marker: "sqlite_master"}}
		if err := s.migrate(context.Background(), file); !errors.Is(err, errBoom) {
			t.Fatalf("migrate = %v, want boom", err)
		}
	})
	t.Run("the version read", func(t *testing.T) {
		file := testFile(t)
		db := raw(t, file)
		if _, err := db.Exec("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
			t.Fatalf("lay the meta table: %v", err)
		}
		s := &Store{db: db, q: failingQ{underlying: db, marker: "FROM meta WHERE key"}}
		if err := s.migrate(context.Background(), file); !errors.Is(err, errBoom) {
			t.Fatalf("migrate = %v, want boom", err)
		}
	})
}

func TestApplyRollsBackAFailedMigration(t *testing.T) {
	t.Run("the statements fail", func(t *testing.T) {
		file := testFile(t)
		db := raw(t, file)
		if _, err := db.Exec("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
			t.Fatalf("lay the meta table: %v", err)
		}
		s := &Store{db: db, q: db}
		if err := s.apply(context.Background(), 1, migrations[0]); err == nil {
			t.Fatal("apply ran migration 1 over an existing meta table")
		}
		if got, want := tableNames(t, file), []string{"meta"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("tables after the failed migration = %v, want %v", got, want)
		}
	})
	t.Run("the version write fails", func(t *testing.T) {
		file := testFile(t)
		db := raw(t, file)
		for _, stmt := range []string{
			"CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
			"CREATE TRIGGER refuse BEFORE INSERT ON meta BEGIN SELECT RAISE(ABORT, 'no'); END;",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("lay the state: %v", err)
			}
		}
		s := &Store{db: db, q: db}
		if err := s.apply(context.Background(), 1, "SELECT 1"); err == nil {
			t.Fatal("apply wrote the version through a refusing trigger")
		}
	})
}
