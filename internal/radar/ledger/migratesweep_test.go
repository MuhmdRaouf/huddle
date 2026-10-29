package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The migrate sweep: a fixture history.db holds rows at fixed ages around the settle age,
// and every test sweeps them into a ledger in a temp directory only.

// day is one day, the unit the fixture's ages are written in.
const day = 24 * time.Hour

// The fixture rows' ids, one per match shape.
const (
	codexRowID  = "codex:sweep:1" // a harness record, files under its own id (rule 1)
	mergedRowID = "msg_sweep"     // a merged copy-first row, its own id on an empty ledger (rule 5)
	reqRowID    = "req_sweep"     // a transcript request id, files under its own id (rule 3)
	headerReqID = "req_h_sweep"   // the header row's request-id header (rule 2)
)

// sweepNow is the moment every sweep test runs at: fixed, so the ages are exact.
func sweepNow() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }

// sweepRecords answers the five requests rows the sweep fixture stores, one per match
// shape at its own age: a harness record 30 days old, a merged copy-first row 9 days old,
// a route-only hash row 8 days and 11 seconds old (just settled), a transcript request id
// 8 days minus a minute old (still inside the window) and a header row an hour old.
func sweepRecords(now time.Time) []shared.RequestRecord {
	at := func(age time.Duration) int64 { return now.Add(-age).UnixMilli() }
	routeID := func(session, model string, age time.Duration) string {
		return "route:" + session + ":main:" + model + ":" + strconv.FormatInt(at(age), 10) + ":200"
	}
	return []shared.RequestRecord{
		{
			ID: codexRowID, Harness: "codex", SessionID: "s1",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: at(30 * day),
			Tokens: shared.Tokens{Input: 10, Output: 5}, Totals: true,
		},
		{
			ID: mergedRowID, SessionID: "sess-sweep", AgentID: "main",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: at(9 * day),
			LatencyMs: i64p(900), Tokens: shared.Tokens{Input: 100, Output: 20},
			Provider: "Z.ai", Route: strp("provider"), Via: strp("zai"), Status: strp("200"),
			PromptHash: strp("ph-sweep"),
		},
		{
			ID:        routeID("sess-b", "claude-sonnet-5-5", 8*day+11*time.Second),
			SessionID: "sess-b", AgentID: "main",
			Model: "claude-sonnet-5-5", Upstream: "api.anthropic.com", Ts: at(8*day + 11*time.Second),
			LatencyMs: i64p(800), Tokens: shared.Tokens{Input: 30, Output: 6},
			Route: strp("anthropic"), Status: strp("200"), PromptHash: strp("ph-settled"),
		},
		{
			ID: reqRowID, SessionID: "sess-t", AgentID: "main",
			Model: "claude-sonnet-5-5", Upstream: "api.anthropic.com", Ts: at(8*day - time.Minute),
			Tokens: shared.Tokens{Input: 11, Output: 4},
		},
		{
			ID:        routeID("sess-h", "glm-5.3", time.Hour),
			SessionID: "sess-h", AgentID: "main",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: at(time.Hour),
			LatencyMs: i64p(1200), Tokens: shared.Tokens{Input: 50, Output: 10},
			Provider: "Z.ai", Route: strp("provider"), Via: strp("zai"), Status: strp("200"),
			Headers: &shared.Headers{{Name: "request-id", Value: headerReqID}},
		},
	}
}

// settledFiledIDs answers the ids the three settled fixture rows file under on an empty
// ledger, in id order.
func settledFiledIDs(now time.Time) []string {
	return []string{
		codexRowID,
		mergedRowID,
		hashID("ph-settled", now.Add(-(8*day + 11*time.Second)).UnixMilli()),
	}
}

// writeSweepHistory creates a history.db in a temp directory holding records, the way
// migrate_test.go's fixture does, and answers the file's path.
func writeSweepHistory(t *testing.T, records []shared.RequestRecord) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "history.db")
	h, err := history.OpenHistory(file, nil)
	if err != nil {
		t.Fatalf("open the fixture history: %v", err)
	}
	for _, record := range records {
		if err := h.PutRequest("sweep/"+record.ID, record); err != nil {
			t.Fatalf("put the fixture request %s: %v", record.ID, err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close the fixture history: %v", err)
	}
	return file
}

// openSweep opens the fixture's history reader and a fresh ledger, both closed when the
// test ends.
func openSweep(t *testing.T, records []shared.RequestRecord) (*Store, *HistoryReader) {
	t.Helper()
	reader := openHistoryReader(t, writeSweepHistory(t, records))
	return openLedger(t), reader
}

// ledgerIDs answers the ledger's request ids in id order.
func ledgerIDs(t *testing.T, db DB) []string {
	t.Helper()
	rows, err := db.All("SELECT id FROM ledger_requests ORDER BY id")
	if err != nil {
		t.Fatalf("read the ledger ids: %v", err)
	}
	var ids []string
	for _, row := range rows {
		ids = append(ids, textColumn(row, "id"))
	}
	return ids
}

// tableCount answers how many rows one table holds.
func tableCount(t *testing.T, db DB, table string) int64 {
	t.Helper()
	row, err := db.Get("SELECT COUNT(*) AS n FROM " + table)
	if err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return intColumn(row, "n")
}

// metaValue answers the value a ledger_meta key holds, "" when it holds none.
func metaValue(t *testing.T, db DB, key string) string {
	t.Helper()
	row, err := db.Get("SELECT value FROM ledger_meta WHERE key = ?", key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return textColumn(row, "value")
}

// savedMigrateCursor reads the cursor the sweep last saved.
func savedMigrateCursor(t *testing.T, db DB) HistoryCursor {
	t.Helper()
	cursor := HistoryCursor{ID: metaValue(t, db, migrateCursorIDKey)}
	cursor.Ts, _ = strconv.ParseInt(metaValue(t, db, migrateCursorTsKey), 10, 64)
	return cursor
}

// TestMigrateOnceFilesOnlySettledRows: one sweep files the three settled rows, counts the
// two younger ones as waiting, and lands the cursor on the last settled row.
func TestMigrateOnceFilesOnlySettledRows(t *testing.T) {
	st, reader := openSweep(t, sweepRecords(sweepNow()))
	ctx := context.Background()

	step, err := st.MigrateOnce(ctx, reader, sweepNow(), 1000)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if step.Filed+step.Matched != 3 {
		t.Errorf("Filed+Matched = %d+%d, want the 3 settled rows handled", step.Filed, step.Matched)
	}
	if step.Waiting != 2 {
		t.Errorf("Waiting = %d, want 2", step.Waiting)
	}
	if ids := ledgerIDs(t, st.db); !slices.Equal(ids, settledFiledIDs(sweepNow())) {
		t.Errorf("the ledger holds %v, want %v", ids, settledFiledIDs(sweepNow()))
	}
	wantCursor := HistoryCursor{
		Ts: sweepNow().Add(-(8*day + 11*time.Second)).UnixMilli(),
		ID: "route:sess-b:main:claude-sonnet-5-5:" +
			strconv.FormatInt(sweepNow().Add(-(8*day+11*time.Second)).UnixMilli(), 10) + ":200",
	}
	if cursor := savedMigrateCursor(t, st.db); cursor != wantCursor {
		t.Errorf("the cursor = %+v, want %+v, the last settled row", cursor, wantCursor)
	}
}

// TestMigrateOnceTwiceAddsNothing: a second sweep at the same moment finds nothing past
// its cursor and adds no row.
func TestMigrateOnceTwiceAddsNothing(t *testing.T) {
	st, reader := openSweep(t, sweepRecords(sweepNow()))
	ctx := context.Background()
	if _, err := st.MigrateOnce(ctx, reader, sweepNow(), 1000); err != nil {
		t.Fatalf("the first migrate: %v", err)
	}
	requests := tableCount(t, st.db, "ledger_requests")
	facts := tableCount(t, st.db, "ledger_facts")

	step, err := st.MigrateOnce(ctx, reader, sweepNow(), 1000)
	if err != nil {
		t.Fatalf("the second migrate: %v", err)
	}
	if step.Filed != 0 || step.Matched != 0 {
		t.Errorf("the second sweep answered (%d, %d), want (0, 0)", step.Filed, step.Matched)
	}
	if got := tableCount(t, st.db, "ledger_requests"); got != requests {
		t.Errorf("ledger_requests holds %d rows after the second sweep, want %d", got, requests)
	}
	if got := tableCount(t, st.db, "ledger_facts"); got != facts {
		t.Errorf("ledger_facts holds %d rows after the second sweep, want %d", got, facts)
	}
}

// TestMigrateOnceStartsOverOnANewVersionAndAddsNothing: a cursor saved under another
// rules version is ignored, so the sweep re-reads the settled rows from the start and
// files none of them twice.
func TestMigrateOnceStartsOverOnANewVersionAndAddsNothing(t *testing.T) {
	st, reader := openSweep(t, sweepRecords(sweepNow()))
	ctx := context.Background()
	if _, err := st.MigrateOnce(ctx, reader, sweepNow(), 1000); err != nil {
		t.Fatalf("the first migrate: %v", err)
	}
	if err := st.db.Run("INSERT OR REPLACE INTO ledger_meta (key, value) VALUES (?, ?)",
		migrateVersionKey, "0"); err != nil {
		t.Fatalf("reset the saved version: %v", err)
	}
	requests := tableCount(t, st.db, "ledger_requests")
	facts := tableCount(t, st.db, "ledger_facts")

	step, err := st.MigrateOnce(ctx, reader, sweepNow(), 1000)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if step.Filed != 0 {
		t.Errorf("Filed = %d, want 0: every settled row is held already", step.Filed)
	}
	if step.Matched != 3 {
		t.Errorf("Matched = %d, want 3", step.Matched)
	}
	if got := tableCount(t, st.db, "ledger_requests"); got != requests {
		t.Errorf("ledger_requests holds %d rows, want %d", got, requests)
	}
	if got := tableCount(t, st.db, "ledger_facts"); got != facts {
		t.Errorf("ledger_facts holds %d rows, want %d", got, facts)
	}
}

// TestMigrateOncePagesAndResumes: five settled rows two at a time take three batches —
// More true, true, false — and every row files exactly once.
func TestMigrateOncePagesAndResumes(t *testing.T) {
	var records []shared.RequestRecord
	for at, age := range []time.Duration{40 * day, 30 * day, 20 * day, 9 * day, 8*day + 11*time.Second} {
		records = append(records, shared.RequestRecord{
			ID:        fmt.Sprintf("req_page_%d", at),
			SessionID: "sess-p", AgentID: "main",
			Model: "glm-5.3", Upstream: "api.z.ai", Ts: sweepNow().Add(-age).UnixMilli(),
			Tokens: shared.Tokens{Input: 5, Output: 2},
		})
	}
	st, reader := openSweep(t, records)
	ctx := context.Background()

	var more []bool
	filed := 0
	for at := 0; ; at++ {
		if at == 10 {
			t.Fatal("the sweep never ran out of rows")
		}
		step, err := st.MigrateOnce(ctx, reader, sweepNow(), 2)
		if err != nil {
			t.Fatalf("migrate: %v", err)
		}
		more = append(more, step.More)
		filed += step.Filed
		if !step.More {
			break
		}
	}
	if !slices.Equal(more, []bool{true, true, false}) {
		t.Errorf("More over five rows two at a time = %v, want [true true false]", more)
	}
	if filed != 5 {
		t.Errorf("the batches filed %d rows, want 5, every row once", filed)
	}
	if ids := ledgerIDs(t, st.db); len(ids) != 5 {
		t.Errorf("the ledger holds %d rows, want 5", len(ids))
	}
}

// TestARowThatSettlesLaterIsFiled: two days on, the row that sat inside the window is
// settled, files under its own id, and one row still waits.
func TestARowThatSettlesLaterIsFiled(t *testing.T) {
	st, reader := openSweep(t, sweepRecords(sweepNow()))
	ctx := context.Background()
	first, err := st.MigrateOnce(ctx, reader, sweepNow(), 1000)
	if err != nil {
		t.Fatalf("the first migrate: %v", err)
	}
	if first.Waiting != 2 {
		t.Fatalf("Waiting = %d, want 2", first.Waiting)
	}

	second, err := st.MigrateOnce(ctx, reader, sweepNow().Add(2*day), 1000)
	if err != nil {
		t.Fatalf("the later migrate: %v", err)
	}
	if second.Filed != 1 || second.Matched != 0 {
		t.Errorf("the later sweep answered (%d, %d), want (1, 0)", second.Filed, second.Matched)
	}
	if second.Waiting != 1 {
		t.Errorf("Waiting = %d, want 1", second.Waiting)
	}
	if ids := ledgerIDs(t, st.db); !slices.Equal(ids, append(settledFiledIDs(sweepNow()), reqRowID)) {
		t.Errorf("the ledger holds %v, want the settled rows plus %s", ids, reqRowID)
	}
	wantCursor := HistoryCursor{Ts: sweepNow().Add(-(8*day - time.Minute)).UnixMilli(), ID: reqRowID}
	if cursor := savedMigrateCursor(t, st.db); cursor != wantCursor {
		t.Errorf("the cursor = %+v, want %+v", cursor, wantCursor)
	}
}

// TestACancelledBatchFilesNothingAndResumes: the context dying mid-batch rolls the whole
// batch back — no row, no cursor — and the next sweep files everything once.
func TestACancelledBatchFilesNothingAndResumes(t *testing.T) {
	st, reader := openSweep(t, sweepRecords(sweepNow()))
	ctx := context.Background()
	cancelling, cancel := context.WithCancel(ctx)
	defer cancel()
	// the context dies while the batch reads the merged row's facts, after the first row
	// filed, so the rollback has something to undo
	killed := &Store{db: &cancelAtFactsDB{DB: st.db, cancel: cancel, atID: mergedRowID}}
	if _, err := killed.MigrateOnce(cancelling, reader, sweepNow(), 1000); !errors.Is(err, context.Canceled) {
		t.Fatalf("the cancelled batch answered %v, want context.Canceled", err)
	}
	if got := tableCount(t, st.db, "ledger_requests"); got != 0 {
		t.Errorf("ledger_requests holds %d rows after the cancelled batch, want 0", got)
	}
	if got := tableCount(t, st.db, "ledger_facts"); got != 0 {
		t.Errorf("ledger_facts holds %d rows after the cancelled batch, want 0", got)
	}
	if metaValue(t, st.db, migrateCursorTsKey) != "" || metaValue(t, st.db, migrateCursorIDKey) != "" {
		t.Error("the cancelled batch left a cursor behind, want none")
	}

	step, err := st.MigrateOnce(ctx, reader, sweepNow(), 1000)
	if err != nil {
		t.Fatalf("the next migrate: %v", err)
	}
	if step.Filed != 3 {
		t.Errorf("the next sweep filed %d rows, want 3, every settled row once", step.Filed)
	}
	if ids := ledgerIDs(t, st.db); !slices.Equal(ids, settledFiledIDs(sweepNow())) {
		t.Errorf("the ledger holds %v, want %v", ids, settledFiledIDs(sweepNow()))
	}
}

// TestStartMigrateReportsEachBatch: the running sweep reports ok under ledger.migrate,
// hands each batch's step to progress, files the settled rows once, and stops when Stop
// says so. Without a store or a reader it starts nothing.
func TestStartMigrateReportsEachBatch(t *testing.T) {
	t.Run("reports each batch and stops", func(t *testing.T) {
		st, reader := openSweep(t, sweepRecords(sweepNow()))
		health := &recordingHealth{}
		var mu sync.Mutex
		var steps []MigrateStep
		handled := make(chan struct{})
		var once sync.Once
		progress := func(step MigrateStep) {
			mu.Lock()
			steps = append(steps, step)
			total := step.Filed + step.Matched
			mu.Unlock()
			if total >= 3 {
				once.Do(func() { close(handled) })
			}
		}
		run := StartMigrate(context.Background(), st, reader, func() time.Time { return sweepNow() },
			time.Millisecond, time.Millisecond, health, progress)
		if run == nil {
			t.Fatal("StartMigrate answered nil with a store and a reader")
		}

		select {
		case <-handled:
		case <-time.After(10 * time.Second):
			t.Fatal("the sweep never filed the settled rows")
		}
		run.Stop()
		select {
		case <-run.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("the sweep never stopped")
		}

		mu.Lock()
		filed := 0
		for _, step := range steps {
			filed += step.Filed
		}
		mu.Unlock()
		if filed != 3 {
			t.Errorf("the batches filed %d rows, want 3", filed)
		}
		oks := 0
		for _, record := range health.snapshot() {
			if record.name != MigrateHealthName {
				t.Errorf("the sweep reported under %q, want %s", record.name, MigrateHealthName)
				continue
			}
			if !record.ok {
				t.Errorf("the sweep reported failing: %v", record.err)
				continue
			}
			oks++
		}
		if oks == 0 {
			t.Error("the sweep never reported ok")
		}
	})

	t.Run("without a store or a reader it starts nothing", func(t *testing.T) {
		if run := StartMigrate(context.Background(), nil, nil, nil, 0, 0, nil, nil); run != nil {
			t.Fatalf("StartMigrate without a store or a reader = %+v, want nil", run)
		}
	})
}
