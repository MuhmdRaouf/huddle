package board

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A .agents/tickets/<feature>/ folder beside the board creates no board: only
// BOARDS and the clusters cards carry name boards.
func TestATicketsFolderAloneMakesNoBoard(t *testing.T) {
	f := newFixture(t)
	tickets := filepath.Join(filepath.Dir(f.root), "tickets", "login")
	if err := os.MkdirAll(tickets, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := f.b.Boards(); len(got) != 0 {
		t.Fatalf("Boards = %+v, want none", got)
	}
	if msg := f.b.RequireBoard("login"); msg == nil {
		t.Fatal("RequireBoard(login) = nil, want the refusal")
	}
}

// A create records the board once; a second create answers the existing-board
// line and writes nothing; a name that is no feature word is refused.
func TestCreateBoardRegistersOnceAndRefusesBadNames(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	lines := mustLines(t, func() ([]string, error) {
		return f.b.CreateBoard(ctx, Args{Actor: "lead", ID: "login"})
	})
	if len(lines) != 1 || lines[0] != "board login created" {
		t.Fatalf("create = %q, want the created line", lines)
	}
	got := f.b.Boards()
	if len(got) != 1 || got[0].Name != "login" || got[0].CreatedBy != "lead" || got[0].CreatedAt != stamp {
		t.Fatalf("Boards = %+v, want login by lead at %s", got, stamp)
	}
	if err := f.b.RequireBoard("login"); err != nil {
		t.Fatalf("RequireBoard(login) = %v, want nil once created", err)
	}
	logBefore := f.logText()
	lines = mustLines(t, func() ([]string, error) {
		return f.b.CreateBoard(ctx, Args{Actor: "dev", ID: "login"})
	})
	if len(lines) != 1 || lines[0] != "board login already exists" {
		t.Fatalf("second create = %q, want the already-exists line", lines)
	}
	if f.logText() != logBefore || len(f.b.Boards()) != 1 || f.b.Boards()[0].CreatedBy != "lead" {
		t.Fatal("the second create changed the record or the LOG")
	}
	for _, bad := range []string{"", "Bad_Name", "a b c", "ab-cd", `x"y`} {
		msg := mustRefuse(t, func() ([]string, error) {
			return f.b.CreateBoard(ctx, Args{Actor: "lead", ID: bad})
		})
		if !strings.Contains(msg, "board name") {
			t.Errorf("name %q: refusal = %q, want it to explain the board name", bad, msg)
		}
	}
	if len(f.b.Boards()) != 1 {
		t.Fatalf("Boards after the refusals = %+v, want only login", f.b.Boards())
	}
}

// Cards whose cluster has no BOARDS entry list as boards, so a board that
// predates the record keeps working; the record and the clusters in use are
// one sorted list without duplicates.
func TestBoardsUnionTheRecordAndTheClustersInUse(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("backlog", "WEB-1", "id: WEB-1\ncluster: web")
	f.card("done", "WEB-2", "id: WEB-2\ncluster: web")
	mustLines(t, func() ([]string, error) { return f.b.CreateBoard(ctx, Args{Actor: "lead", ID: "web"}) })
	mustLines(t, func() ([]string, error) { return f.b.CreateBoard(ctx, Args{Actor: "lead", ID: "auth"}) })
	var names []string
	for _, e := range f.b.Boards() {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "auth,web" {
		t.Fatalf("Boards = %v, want auth,web", names)
	}
	f.card("ready", "OPS-1", "id: OPS-1\ncluster: ops")
	if err := f.b.RequireBoard("ops"); err != nil {
		t.Fatalf("RequireBoard(ops) = %v, want nil for a cluster in use", err)
	}
}

// The BOARDS record rides the migration into meta, a db board creates and reads
// boards through it, and the export writes the record back out.
func TestBoardsRideMigrationAndExport(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	mustLines(t, func() ([]string, error) { return f.b.CreateBoard(ctx, Args{Actor: "lead", ID: "login"}) })
	want, err := os.ReadFile(filepath.Join(f.root, "BOARDS"))
	if err != nil {
		t.Fatalf("read BOARDS: %v", err)
	}

	mustLines(t, func() ([]string, error) { return MigrateDB(ctx, f.b) })
	if _, err := os.Stat(filepath.Join(f.root, "BOARDS")); err == nil {
		t.Fatal("BOARDS still sits at the root after the migration")
	}
	db, closeBoard, err := Open(ctx, Board{Root: f.root, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("reopen as a db board: %v", err)
	}
	t.Cleanup(func() { _ = closeBoard() })
	if got := db.Boards(); len(got) != 1 || got[0].Name != "login" {
		t.Fatalf("the db board's Boards = %+v, want login", got)
	}
	mustLines(t, func() ([]string, error) { return db.CreateBoard(ctx, Args{Actor: "lead", ID: "auth"}) })
	if _, err := os.Stat(filepath.Join(f.root, "BOARDS")); err == nil {
		t.Fatal("a db board wrote BOARDS as a file")
	}

	out := filepath.Join(t.TempDir(), "exported")
	mustLines(t, func() ([]string, error) { return ExportMarkdown(ctx, db, out) })
	data, err := os.ReadFile(filepath.Join(out, "BOARDS"))
	if err != nil || bytes.Equal(data, want) || !strings.Contains(string(data), `"auth"`) {
		t.Fatalf("the exported BOARDS = %q, %v; want both boards", data, err)
	}
}
