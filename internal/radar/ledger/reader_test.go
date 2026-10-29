package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The ledger reader: read-only pages of one session's requests, run against a ledger.db in
// a temp directory only.

// openStore opens a ledger.db in a temp directory and answers the store and its file path,
// closed when the test ends.
func openStore(t *testing.T) (*Store, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "ledger.db")
	st, err := Open(file)
	if err != nil {
		t.Fatalf("open the ledger: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, file
}

// openReader opens a Reader over file, closed when the test ends.
func openReader(t *testing.T, file string) *Reader {
	t.Helper()
	r, err := OpenReader(file)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// sessionRow builds one row of session "s1" started at started ms, with a non-nil ttfb and
// reported cost and a nil turn index, so both pointer shapes round-trip.
func sessionRow(started int64) Row {
	return Row{
		ID:              fmt.Sprintf("req_s1_%05d", started),
		StartedAt:       started,
		TtfbMs:          i64p(started + 1),
		DurationMs:      started + 2,
		Model:           "glm-5.3-flash",
		Upstream:        "api.z.ai",
		SessionID:       "s1",
		Outcome:         "ok",
		Status:          200,
		CostUSD:         0.5,
		CostReportedUSD: f64p(1.25),
	}
}

// TestReader reads one session's requests back through a reader that never writes and
// never creates the file it reads.
func TestReader(t *testing.T) {
	t.Run("pages one session newest first", func(t *testing.T) {
		st, file := openStore(t)
		ctx := context.Background()
		var want []Row
		for started := int64(25000); started >= 1000; started -= 1000 {
			row := sessionRow(started)
			if err := st.Upsert(ctx, row); err != nil {
				t.Fatalf("upsert %s: %v", row.ID, err)
			}
			want = append(want, row)
		}
		// another session's rows interleave s1's start times; they never come back
		for started := int64(1000); started <= 5000; started += 1000 {
			row := Row{ID: fmt.Sprintf("req_s2_%05d", started), StartedAt: started, SessionID: "s2"}
			if err := st.Upsert(ctx, row); err != nil {
				t.Fatalf("upsert %s: %v", row.ID, err)
			}
		}

		r := openReader(t, file)
		var got []Row
		var counts []int
		before := int64(0)
		for {
			rows, err := r.RequestsOfSession(ctx, "s1", before, 10)
			if err != nil {
				t.Fatalf("page before %d: %v", before, err)
			}
			counts = append(counts, len(rows))
			got = append(got, rows...)
			if len(rows) == 0 {
				break
			}
			before = rows[len(rows)-1].StartedAt
		}
		if !reflect.DeepEqual(counts, []int{10, 10, 5, 0}) {
			t.Errorf("page sizes %v, want [10 10 5 0]", counts)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("pages =\n %+v\nwant\n %+v", got, want)
		}
		first := got[0]
		if first.TurnIndex != nil {
			t.Errorf("the first row's turn index = %v, want nil", *first.TurnIndex)
		}
		if first.TtfbMs == nil || *first.TtfbMs != 25001 {
			t.Errorf("the first row's ttfb = %v, want 25001", first.TtfbMs)
		}
		if first.CostReportedUSD == nil || *first.CostReportedUSD != 1.25 {
			t.Errorf("the first row's reported cost = %v, want 1.25", first.CostReportedUSD)
		}
	})

	t.Run("holds the limit to 1..1000", func(t *testing.T) {
		st, file := openStore(t)
		ctx := context.Background()
		for started := int64(1); started <= 1001; started++ {
			row := Row{ID: fmt.Sprintf("req_many_%05d", started), StartedAt: started, SessionID: "many"}
			if err := st.Upsert(ctx, row); err != nil {
				t.Fatalf("upsert %s: %v", row.ID, err)
			}
		}
		r := openReader(t, file)

		rows, err := r.RequestsOfSession(ctx, "many", 0, 0)
		if err != nil {
			t.Fatalf("limit 0: %v", err)
		}
		if len(rows) != 1 {
			t.Errorf("limit 0 answered %d rows, want 1", len(rows))
		}
		rows, err = r.RequestsOfSession(ctx, "many", 0, 5000)
		if err != nil {
			t.Fatalf("limit 5000: %v", err)
		}
		if len(rows) != 1000 {
			t.Errorf("limit 5000 answered %d rows, want 1000", len(rows))
		}
	})

	t.Run("never writes", func(t *testing.T) {
		st, file := openStore(t)
		ctx := context.Background()
		for started := int64(1); started <= 3; started++ {
			if err := st.Upsert(ctx, sessionRow(started*1000)); err != nil {
				t.Fatalf("upsert: %v", err)
			}
		}
		r := openReader(t, file)

		if _, err := r.pool.ExecContext(ctx, "DELETE FROM ledger_requests"); err == nil {
			t.Fatal("the reader's pool deleted every row, want the delete refused")
		}
		var n int64
		if err := r.pool.QueryRowContext(ctx, "SELECT COUNT(*) FROM ledger_requests").Scan(&n); err != nil {
			t.Fatalf("count the rows: %v", err)
		}
		if n != 3 {
			t.Errorf("the ledger holds %d rows after the refused delete, want 3", n)
		}
	})

	t.Run("never creates a missing file", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := OpenReader(filepath.Join(dir, "ledger.db")); err == nil {
			t.Fatal("OpenReader opened a missing ledger, want an error")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read the directory: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("the directory holds %v after the failed open, want nothing", entries)
		}
	})

	t.Run("reads while the writer writes", func(t *testing.T) {
		st, file := openStore(t)
		ctx := context.Background()
		for started := int64(1000); started <= 5000; started += 1000 {
			if err := st.Upsert(ctx, sessionRow(started)); err != nil {
				t.Fatalf("upsert: %v", err)
			}
		}
		r := openReader(t, file)

		wrote := make(chan error, 1)
		go func() {
			for started := int64(1); started <= 200; started++ {
				row := Row{ID: fmt.Sprintf("req_live_%05d", started), StartedAt: started, SessionID: "live"}
				if err := st.Upsert(ctx, row); err != nil {
					wrote <- err
					return
				}
			}
			wrote <- nil
		}()
		for page := 0; page < 20; page++ {
			if _, err := r.RequestsOfSession(ctx, "s1", 0, 10); err != nil {
				t.Fatalf("page %d while the writer writes: %v", page, err)
			}
		}
		if err := <-wrote; err != nil {
			t.Errorf("the writer hit %v while the reader read", err)
		}
	})

	t.Run("the session query uses its index", func(t *testing.T) {
		_, file := openStore(t)
		r := openReader(t, file)

		rows, err := r.pool.Query("EXPLAIN QUERY PLAN "+
			"SELECT * FROM ledger_requests WHERE session_id = ? AND started_at < ? "+
			"ORDER BY started_at DESC, id DESC LIMIT ?", "s1", int64(math.MaxInt64), 10)
		if err != nil {
			t.Fatalf("explain the session query: %v", err)
		}
		defer rows.Close()
		used := false
		for rows.Next() {
			var id, parent, notused int64
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatalf("scan the plan: %v", err)
			}
			if strings.Contains(detail, "ledger_requests_session") {
				used = true
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read the plan: %v", err)
		}
		if !used {
			t.Error("the session query's plan never names ledger_requests_session")
		}
	})

	t.Run("an old file gains the index", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "ledger.db")
		old, err := sql.Open("sqlite", file)
		if err != nil {
			t.Fatalf("open the old file: %v", err)
		}
		if _, err := old.Exec(ledgerRequestsDDL + ";" + ledgerRequestsIndex + ";" + ledgerFactsDDL +
			";PRAGMA user_version = 2"); err != nil {
			t.Fatalf("build the version-2 file: %v", err)
		}
		if err := old.Close(); err != nil {
			t.Fatalf("close the old file: %v", err)
		}

		st, err := Open(file)
		if err != nil {
			t.Fatalf("open the version-2 file with the writer: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })

		row, err := st.db.Get("PRAGMA user_version")
		if err != nil {
			t.Fatalf("read user_version: %v", err)
		}
		if version := row["user_version"]; version != int64(SchemaVersion) {
			t.Errorf("user_version = %v, want %d", version, SchemaVersion)
		}
		found, err := st.db.Get("SELECT name FROM sqlite_master " +
			"WHERE type = 'index' AND name = 'ledger_requests_session'")
		if err != nil {
			t.Fatalf("look for the session index: %v", err)
		}
		if found == nil {
			t.Error("the version-2 file gained no ledger_requests_session index")
		}
	})
}
