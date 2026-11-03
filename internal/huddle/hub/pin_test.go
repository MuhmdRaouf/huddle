package hub

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// A session pinned to a board keeps the pin across a hub restart, and a later report from the
// same session leaves it alone.
func TestThePinSurvivesAHubRestart(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "hub.db")
	db, err := Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	rep := Report{SessionID: "s-pin", Machine: "box", Project: "/shop", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:10.000Z"}
	if err := db.Report(ctx, rep); err != nil {
		t.Fatalf("report: %v", err)
	}
	if found, err := db.SetBoard(ctx, "s-pin", "login"); err != nil || !found {
		t.Fatalf("pin = %v, %v, want the session found", found, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err = Open(ctx, file)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rep.LastEventAt = "2026-10-16T09:00:20.000Z"
	if err := db.Report(ctx, rep); err != nil {
		t.Fatalf("report after the restart: %v", err)
	}
	row, err := db.SessionRow(ctx, "s-pin", readNow)
	if err != nil || row == nil {
		t.Fatalf("session row = %v, %v", row, err)
	}
	if row.Board != "login" {
		t.Errorf("board after restart and a report = %q, want login", row.Board)
	}
}

func TestSetBoardOfAnUnknownSessionFindsNone(t *testing.T) {
	db := openForTest(t)
	found, err := db.SetBoard(context.Background(), "nobody", "login")
	if err != nil || found {
		t.Errorf("pin of an unknown session = %v, %v, want not found and no error", found, err)
	}
}

func TestAnEmptyBoardClearsThePin(t *testing.T) {
	ctx := context.Background()
	db := openForTest(t)
	if err := db.Report(ctx, Report{SessionID: "s-pin", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:10.000Z"}); err != nil {
		t.Fatal(err)
	}
	for _, board := range []string{"login", ""} {
		if _, err := db.SetBoard(ctx, "s-pin", board); err != nil {
			t.Fatal(err)
		}
	}
	row, _ := db.SessionRow(ctx, "s-pin", readNow)
	if row.Board != "" {
		t.Errorf("board after clearing = %q, want empty", row.Board)
	}
}

// Migration 30 only adds the board column: a hub.db written at schema 29 opens, keeps its
// session rows and reads them with no pin.
func TestMigration30KeepsExistingSessionRowsUnpinned(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "hub.db")
	st, err := store.Open(ctx, file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Run(ctx, "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatalf("the meta table: %v", err)
	}
	for n, migration := range migrations[:29] {
		if err := st.Run(ctx, migration); err != nil {
			t.Fatalf("migration %d: %v", n+1, err)
		}
	}
	if err := st.Run(ctx, "INSERT OR REPLACE INTO meta (key, value) VALUES ('schema_version', '29')"); err != nil {
		t.Fatalf("mark schema 29: %v", err)
	}
	if err := st.Run(ctx,
		"INSERT INTO sessions (session_id, project, last_event, last_event_at) VALUES ('s-old', '/shop', 'Stop', '2026-10-16T09:00:25.000Z')"); err != nil {
		t.Fatalf("the old session row: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err := Open(ctx, file)
	if err != nil {
		t.Fatalf("open at schema 30: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	row, err := db.SessionRow(ctx, "s-old", readNow)
	if err != nil || row == nil {
		t.Fatalf("the old session row = %v, %v, want it kept", row, err)
	}
	if row.Project != "/shop" || row.Board != "" {
		t.Errorf("the old row = project %q board %q, want /shop and no pin", row.Project, row.Board)
	}
}
