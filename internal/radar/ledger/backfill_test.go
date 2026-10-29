package ledger

// The backfill: rows written by older Reconcile rules rebuilt from the facts the file
// holds, one BackfillBatch per transaction, resumable through the cursor ledger_meta keeps.
// Open never rebuilds a row: a hub start never waits on a backfill.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// routeFactSeed is the request id the one marshalled route fact carries; each row's facts
// swap it for the row's own id.
const routeFactSeed = "req_seed"

// routeFact is the smallest route fact Reconcile builds a row from: a provider answer with
// a model and an ok status, its request-id header naming the id.
func routeFact(id string) RouteFacts {
	return RouteFacts{Event: domain.RouteEvent{
		Ts:      "2026-10-01T12:00:00Z",
		Event:   domain.EventRoute,
		Plugin:  "zai",
		Model:   "glm-5.3",
		Route:   "provider",
		Status:  200,
		Headers: &map[string]string{"request-id": id},
	}}
}

// routeFactText marshals the route fact once, for the builder to reuse with the id changed.
func routeFactText(t *testing.T) string {
	t.Helper()
	text, err := json.Marshal(routeFact(routeFactSeed))
	if err != nil {
		t.Fatalf("marshal the route facts: %v", err)
	}
	return string(text)
}

// buildVersion3File writes file as a version-3 ledger holding rows rows: each a
// deliberately stale ledger_requests row beside the route fact that rebuilds it, all
// inserted in one transaction.
func buildVersion3File(t *testing.T, file string, rows int) {
	t.Helper()
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-3 file: %v", err)
	}
	defer func() { _ = pool.Close() }()
	if _, err := pool.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";" + ledgerFactsDDL + ";" +
		ledgerRequestsSessionIndex + ";PRAGMA user_version = 3"); err != nil {
		t.Fatalf("build the version-3 schema: %v", err)
	}
	ctx := context.Background()
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the file's transaction: %v", err)
	}
	rowStmt, err := tx.PrepareContext(ctx, "INSERT INTO ledger_requests "+
		"(id, anthropic_id, started_at, model, in_transcript) VALUES (?, ?, ?, 'stale', 0)")
	if err != nil {
		t.Fatalf("prepare the row insert: %v", err)
	}
	defer func() { _ = rowStmt.Close() }()
	factStmt, err := tx.PrepareContext(ctx, "INSERT INTO ledger_facts (id, route, transcript) VALUES (?, ?, '')")
	if err != nil {
		t.Fatalf("prepare the facts insert: %v", err)
	}
	defer func() { _ = factStmt.Close() }()
	facts := routeFactText(t)
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	for at := range rows {
		id := fmt.Sprintf("req_%06d", at)
		if _, err := rowStmt.ExecContext(ctx, id, id, started); err != nil {
			t.Fatalf("insert the row %s: %v", id, err)
		}
		if _, err := factStmt.ExecContext(ctx, id, strings.Replace(facts, routeFactSeed, id, 1)); err != nil {
			t.Fatalf("insert the facts of %s: %v", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the file: %v", err)
	}
}

// newVersion3File builds a version-3 ledger file in a fresh temp directory and answers its
// path.
func newVersion3File(t *testing.T, rows int) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ledger.db")
	buildVersion3File(t, file, rows)
	return file
}

// openLedgerFile opens file as the store, closing it at cleanup.
func openLedgerFile(t *testing.T, file string) *Store {
	t.Helper()
	st, err := Open(file)
	if err != nil {
		t.Fatalf("open %s: %v", file, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// userVersion reads the file's schema version.
func userVersion(t *testing.T, db DB) int {
	t.Helper()
	row, err := db.Get("PRAGMA user_version")
	if err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return int(intColumn(row, "user_version"))
}

// savedCursor reads the cursor the backfill last saved.
func savedCursor(t *testing.T, db DB) string {
	t.Helper()
	row, err := db.Get("SELECT value FROM ledger_meta WHERE key = ?", backfillCursorKey)
	if err != nil {
		t.Fatalf("read the backfill cursor: %v", err)
	}
	return textColumn(row, "value")
}

// oldRowCount answers how many rows still wait for a rebuild.
func oldRowCount(t *testing.T, db DB) int {
	t.Helper()
	row, err := db.Get("SELECT COUNT(*) AS n FROM ledger_requests WHERE reconciled < ?", ReconcileVersion)
	if err != nil {
		t.Fatalf("count the old rows: %v", err)
	}
	return int(intColumn(row, "n"))
}

// firstOldRow answers the lowest id still waiting for a rebuild, "" when none waits.
func firstOldRow(t *testing.T, db DB) string {
	t.Helper()
	row, err := db.Get("SELECT MIN(id) AS id FROM ledger_requests WHERE reconciled < ?", ReconcileVersion)
	if err != nil {
		t.Fatalf("read the oldest old row: %v", err)
	}
	return textColumn(row, "id")
}

// TestOpenStaysFastOnABigOldFile: Open raises a 130,000-row version-3 file to SchemaVersion
// without touching a row — the version step is schema work only, and the rows wait for the
// backfill.
func TestOpenStaysFastOnABigOldFile(t *testing.T) {
	file := newVersion3File(t, 130_000)

	started := time.Now()
	st := openLedgerFile(t, file)
	if took := time.Since(started); took > time.Second {
		t.Fatalf("Open took %s, want under 1s", took)
	}
	if version := userVersion(t, st.db); version != SchemaVersion {
		t.Errorf("user_version = %d, want %d", version, SchemaVersion)
	}
	rebuilt, err := st.db.Get("SELECT COUNT(*) AS n FROM ledger_requests WHERE reconciled = ?", ReconcileVersion)
	if err != nil {
		t.Fatalf("count the rebuilt rows: %v", err)
	}
	if n := intColumn(rebuilt, "n"); n != 0 {
		t.Errorf("Open left %d rows rebuilt, want none: Open does no row work", n)
	}
}

// TestTheBackfillRebuildsEveryOlderRow: 2,500 stale rows take three calls — 1000, 1000,
// 500 — and every row then equals Reconcile of its own facts, stamped with the rules
// version that rebuilt it. A call past the end answers 0 and false.
func TestTheBackfillRebuildsEveryOlderRow(t *testing.T) {
	st := openLedgerFile(t, newVersion3File(t, 2500))
	ctx := context.Background()

	var sizes []int
	for {
		rebuilt, more, err := st.BackfillOnce(ctx, BackfillBatch)
		if err != nil {
			t.Fatalf("backfill: %v", err)
		}
		sizes = append(sizes, rebuilt)
		if !more {
			break
		}
	}
	if !slices.Equal(sizes, []int{1000, 1000, 500}) {
		t.Errorf("the batches rebuilt %v, want [1000 1000 500]", sizes)
	}

	all, err := st.db.All("SELECT * FROM ledger_requests ORDER BY id")
	if err != nil {
		t.Fatalf("read every row: %v", err)
	}
	if len(all) != 2500 {
		t.Fatalf("the file holds %d rows, want 2500", len(all))
	}
	for _, found := range all {
		id := textColumn(found, "id")
		facts, err := factsOf(ctx, st.db, id)
		if err != nil {
			t.Fatalf("read the facts of %s: %v", id, err)
		}
		want, ok := Reconcile(facts)
		if !ok {
			t.Fatalf("the facts of %s reconcile to nothing", id)
		}
		if got := rowOf(found); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %+v, want %+v", id, got, want)
		}
		if intColumn(found, "reconciled") != ReconcileVersion {
			t.Errorf("%s carries reconciled %v, want %d", id, found["reconciled"], ReconcileVersion)
		}
	}

	rebuilt, more, err := st.BackfillOnce(ctx, BackfillBatch)
	if err != nil || rebuilt != 0 || more {
		t.Errorf("the call past the end answered (%d, %t, %v), want (0, false, nil)", rebuilt, more, err)
	}
}

// TestTheBackfillRebuildsTheBillingColumns: a row the version-1 rules wrote — reconciled 1 —
// whose route facts carry the four billing keys and the zai plugin, gains all six billing
// columns when the backfill rebuilds it, exactly as Reconcile answers them from those facts.
func TestTheBackfillRebuildsTheBillingColumns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	buildVersion4File(t, file)
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-4 file: %v", err)
	}
	fact, err := json.Marshal(RouteFacts{Event: domain.RouteEvent{
		Ts:      "2026-10-01T12:00:00Z",
		Event:   domain.EventRoute,
		Plugin:  "zai",
		Model:   "glm-5.3",
		Route:   "provider",
		Status:  200,
		Headers: &map[string]string{"request-id": "req_billing"},
		Usage: &domain.RouteUsage{
			"input_tokens":   float64(100),
			"output_tokens":  float64(20),
			"cache_creation": map[string]any{"ephemeral_1h_input_tokens": float64(32)},
			"speed":          "fast",
			"inference_geo":  "us",
			"service_tier":   "priority",
		},
	}})
	if err != nil {
		t.Fatalf("marshal the route facts: %v", err)
	}
	if _, err := pool.Exec(
		"INSERT INTO ledger_requests (id, anthropic_id, model, reconciled) VALUES ('req_billing', 'req_billing', 'stale', 1);"+
			"INSERT INTO ledger_facts (id, route, transcript) VALUES ('req_billing', ?, '')", string(fact)); err != nil {
		t.Fatalf("insert the billing row and its facts: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("close the file: %v", err)
	}

	st := openLedgerFile(t, file)
	ctx := context.Background()
	rebuilt, more, err := st.BackfillOnce(ctx, BackfillBatch)
	if err != nil || rebuilt != 1 || more {
		t.Fatalf("the batch answered (%d, %t, %v), want (1, false, nil)", rebuilt, more, err)
	}
	found, err := st.db.Get("SELECT * FROM ledger_requests WHERE id = 'req_billing'")
	if err != nil || found == nil {
		t.Fatalf("the rebuilt row = %+v, %v", found, err)
	}
	facts, err := factsOf(ctx, st.db, "req_billing")
	if err != nil {
		t.Fatalf("read the facts: %v", err)
	}
	want, ok := Reconcile(facts)
	if !ok {
		t.Fatal("the route facts reconcile to nothing")
	}
	got := rowOf(found)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the rebuilt row =\n %+v\nwant\n %+v", got, want)
	}
	if got.CacheWrite1h == nil || *got.CacheWrite1h != 32 {
		t.Errorf("cache_write_1h = %v, want 32", got.CacheWrite1h)
	}
	if got.Speed == nil || *got.Speed != "fast" {
		t.Errorf("speed = %v, want fast", got.Speed)
	}
	if got.Geo == nil || *got.Geo != "us" {
		t.Errorf("geo = %v, want us", got.Geo)
	}
	if got.ServiceTier == nil || *got.ServiceTier != "priority" {
		t.Errorf("service_tier = %v, want priority", got.ServiceTier)
	}
	if got.Provider != "Z.ai" {
		t.Errorf("provider = %q, want Z.ai", got.Provider)
	}
	if got.Via == nil || *got.Via != "zai" {
		t.Errorf("via = %v, want zai", got.Via)
	}
}

// TestTheBackfillStampsARowWithNoFacts: a row no source ever spoke for — no facts row
// behind it — cannot be rebuilt, so the backfill stamps it with the rules version and
// leaves every other column as it stands, and the next call picks no row.
func TestTheBackfillStampsARowWithNoFacts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ledger.db")
	pool, err := sql.Open("sqlite", file)
	if err != nil {
		t.Fatalf("open the version-3 file: %v", err)
	}
	if _, err := pool.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";" + ledgerFactsDDL + ";" +
		ledgerRequestsSessionIndex + ";PRAGMA user_version = 3"); err != nil {
		t.Fatalf("build the version-3 schema: %v", err)
	}
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	if _, err := pool.Exec("INSERT INTO ledger_requests "+
		"(id, anthropic_id, started_at, model, in_transcript) VALUES ('req_none', 'req_none', ?, 'stale', 0)",
		started); err != nil {
		t.Fatalf("insert the facts-less row: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("close the file: %v", err)
	}

	st := openLedgerFile(t, file)
	ctx := context.Background()
	rebuilt, more, err := st.BackfillOnce(ctx, BackfillBatch)
	if err != nil || rebuilt != 1 || more {
		t.Fatalf("the batch answered (%d, %t, %v), want (1, false, nil)", rebuilt, more, err)
	}
	found, err := st.db.Get("SELECT * FROM ledger_requests WHERE id = 'req_none'")
	if err != nil || found == nil {
		t.Fatalf("the stamped row = %+v, %v", found, err)
	}
	if intColumn(found, "reconciled") != ReconcileVersion {
		t.Errorf("reconciled = %v, want %d", found["reconciled"], ReconcileVersion)
	}
	// the stamp is all the row gained: everything else is exactly what the file held
	for column, want := range map[string]any{
		"id": "req_none", "anthropic_id": "req_none", "model": "stale",
		"started_at": started, "in_transcript": int64(0),
	} {
		if found[column] != want {
			t.Errorf("%s = %v (%T), want %v", column, found[column], found[column], want)
		}
	}
	if found["lane"] != nil {
		t.Errorf("lane = %v, want it untouched", found["lane"])
	}

	rebuilt, more, err = st.BackfillOnce(ctx, BackfillBatch)
	if err != nil || rebuilt != 0 || more {
		t.Errorf("the second call answered (%d, %t, %v), want (0, false, nil)", rebuilt, more, err)
	}
}

// TestTheBackfillResumesAfterAKill: the cursor a finished batch saved survives the store
// closing, so the next batch starts after it; a batch cancelled midway leaves no row
// rebuilt and the cursor unchanged.
func TestTheBackfillResumesAfterAKill(t *testing.T) {
	file := newVersion3File(t, 2500)
	ctx := context.Background()

	st, err := Open(file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rebuilt, more, err := st.BackfillOnce(ctx, BackfillBatch)
	if err != nil || rebuilt != 1000 || !more {
		t.Fatalf("the first batch answered (%d, %t, %v), want (1000, true, nil)", rebuilt, more, err)
	}
	if err := st.Close(); err != nil { // the kill: only what the batch committed is saved
		t.Fatalf("close the store: %v", err)
	}

	reopened, err := Open(file)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if cursor := savedCursor(t, reopened.db); cursor != "req_000999" {
		t.Errorf("backfill.cursor = %q, want req_000999, the 1,000th id", cursor)
	}

	// a batch cancelled midway rolls back whole: the context dies while the batch reads the
	// facts of req_001100, and nothing it touched stays
	cancelling, cancel := context.WithCancel(ctx)
	defer cancel()
	killed := &Store{db: &cancelAtFactsDB{DB: reopened.db, cancel: cancel, atID: "req_001100"}}
	if _, _, err := killed.BackfillOnce(cancelling, BackfillBatch); !errors.Is(err, context.Canceled) {
		t.Errorf("the cancelled batch answered %v, want context.Canceled", err)
	}
	if waiting := oldRowCount(t, reopened.db); waiting != 1500 {
		t.Errorf("%d rows still wait after the cancelled batch, want the 1,500 from before it", waiting)
	}
	if cursor := savedCursor(t, reopened.db); cursor != "req_000999" {
		t.Errorf("backfill.cursor = %q after the cancelled batch, want req_000999 unchanged", cursor)
	}

	// the next batch starts after the saved cursor: the 1,001st row is its first
	if rebuilt, more, err := reopened.BackfillOnce(ctx, BackfillBatch); err != nil || rebuilt != 1000 || !more {
		t.Fatalf("the batch after the kill answered (%d, %t, %v), want (1000, true, nil)", rebuilt, more, err)
	}
	if first := firstOldRow(t, reopened.db); first != "req_002000" {
		t.Errorf("the oldest row still waiting is %q, want req_002000: the batch started after the cursor", first)
	}
}

// TestStartBackfillReportsAndEnds: the running backfill reports one ok per batch under
// ledger.backfill and closes Done when no older row is left; without a store it starts
// nothing.
func TestStartBackfillReportsAndEnds(t *testing.T) {
	t.Run("reports each batch ok and ends", func(t *testing.T) {
		st := openLedgerFile(t, newVersion3File(t, 2500))
		health := &recordingHealth{}
		run := StartBackfill(context.Background(), st, time.Millisecond, health)
		t.Cleanup(run.Stop)

		select {
		case <-run.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the backfill never ended")
		}
		oks := 0
		for _, record := range health.snapshot() {
			if record.name != BackfillHealthName {
				t.Errorf("the backfill reported under %q, want %s", record.name, BackfillHealthName)
				continue
			}
			if !record.ok {
				t.Errorf("the backfill reported failing: %v", record.err)
				continue
			}
			oks++
		}
		if oks < 3 {
			t.Errorf("the backfill reported ok %d times, want at least 3, one per batch", oks)
		}
	})

	t.Run("a nil store starts nothing", func(t *testing.T) {
		if run := StartBackfill(context.Background(), nil, 0, nil); run != nil {
			t.Fatalf("StartBackfill without a store = %+v, want nil", run)
		}
	})
}

// TestTheWatcherKeepsWritingDuringABackfill: while the backfill walks 20,000 old rows, the
// watcher keeps writing — 50 AddRoute calls from its one goroutine, each back inside a
// second and none lost — because a batch holds the file's one connection only for its own
// transaction.
func TestTheWatcherKeepsWritingDuringABackfill(t *testing.T) {
	st := openLedgerFile(t, newVersion3File(t, 20_000))
	run := StartBackfill(context.Background(), st, 0, nil)
	t.Cleanup(run.Stop)

	ctx := context.Background()
	for at := range 50 {
		id := fmt.Sprintf("new_%03d", at)
		started := time.Now()
		err := st.AddRoute(ctx, routeFact(id))
		if err != nil {
			t.Fatalf("AddRoute %s: %v", id, err)
		}
		if took := time.Since(started); took > time.Second {
			t.Fatalf("AddRoute %s took %s, want under 1s", id, took)
		}
	}
	for at := range 50 {
		id := fmt.Sprintf("new_%03d", at)
		if row, err := st.Get(ctx, id); err != nil || row == nil {
			t.Fatalf("%s did not land: %+v, %v", id, row, err)
		}
	}
}

// TestTxRollsBackOnError: an error from fn rolls back every statement fn ran — none of
// them reaches the file.
func TestTxRollsBackOnError(t *testing.T) {
	st := openLedger(t)
	boom := errors.New("boom")
	err := st.db.Tx(func(tx DB) error {
		if err := tx.Run("INSERT INTO ledger_requests (id) VALUES ('tx_row')"); err != nil {
			return err
		}
		if err := tx.Run("INSERT INTO ledger_meta (key, value) VALUES ('tx_key', 'tx_value')"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("the transaction answered %v, want boom", err)
	}
	for _, probe := range []struct{ what, query string }{
		{"the request row", "SELECT id FROM ledger_requests WHERE id = 'tx_row'"},
		{"the meta key", "SELECT value FROM ledger_meta WHERE key = 'tx_key'"},
	} {
		found, err := st.db.Get(probe.query)
		if err != nil {
			t.Fatalf("look for %s: %v", probe.what, err)
		}
		if found != nil {
			t.Errorf("%s survived the rollback", probe.what)
		}
	}
}

// cancelAtFactsDB answers the database another DB serves, cancelling its context while the
// batch reads one chosen id's facts: the deterministic midway cancellation. A transaction
// hands fn a counting copy, since the batch reads through the transaction's DB.
type cancelAtFactsDB struct {
	DB
	cancel func()
	atID   string
}

func (d *cancelAtFactsDB) Tx(fn func(tx DB) error) error {
	return d.DB.Tx(func(tx DB) error {
		return fn(&cancelAtFactsDB{DB: tx, cancel: d.cancel, atID: d.atID})
	})
}

func (d *cancelAtFactsDB) Get(query string, params ...any) (map[string]any, error) {
	row, err := d.DB.Get(query, params...)
	if d.atID != "" && len(params) == 1 && params[0] == d.atID {
		d.cancel()
		d.atID = ""
	}
	return row, err
}
