package hub

// The registry's own database: one row per reported event, one per session, kept by numbered
// migrations that run at open. The store is exercised over real SQLite files in temp homes.

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

func TestOpenRunsEveryMigrationAndRecordsTheVersion(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hub.db")
	db, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	row, err := db.st.Get(context.Background(), "SELECT value FROM meta WHERE key = 'schema_version'")
	if err != nil {
		t.Fatalf("read the version: %v", err)
	}
	if row == nil || row["value"] != strconv.Itoa(SchemaVersion) {
		t.Errorf("schema_version = %v, want %d", row, SchemaVersion)
	}
	for _, table := range []string{"events", "sessions", "pairings", "devices", "device_sessions", "audit", "alerts", "items", "messages", "push_acks", "satellites", "push_subscriptions", "sat_queue", "links", "digests"} {
		if _, err := db.st.Get(context.Background(), "SELECT 1 FROM "+table+" LIMIT 1"); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// a second open finds the schema already there and changes nothing
	again, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	if row, err = again.st.Get(context.Background(), "SELECT value FROM meta WHERE key = 'schema_version'"); err != nil || row["value"] != strconv.Itoa(SchemaVersion) {
		t.Errorf("schema_version after reopen = %v (%v), want %d", row, err, SchemaVersion)
	}
}

func TestOpenRefusesADatabaseANewerHuddleWrote(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hub.db")
	st, err := store.Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Run(context.Background(), "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatalf("meta: %v", err)
	}
	newerVersion := strconv.Itoa(SchemaVersion + 1)
	if err := st.Run(context.Background(), "INSERT INTO meta (key, value) VALUES ('schema_version', ?)", newerVersion); err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err = Open(context.Background(), file)
	var newer *NewerSchemaError
	if err == nil {
		t.Fatal("open succeeded, want the newer-schema refusal")
	}
	if !errors.As(err, &newer) || newer.Version != SchemaVersion+1 {
		t.Errorf("open error = %v, want NewerSchemaError %d", err, SchemaVersion+1)
	}
	if msg := newer.Error(); !strings.Contains(msg, "newer huddle") || !strings.Contains(msg, strconv.Itoa(SchemaVersion+1)) {
		t.Errorf("the refusal = %q, want it to name the newer huddle and its schema", msg)
	}
}

func TestReportStoresEveryEventAndUpsertsTheSession(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	reports := []Report{
		{SessionID: "s-1", Machine: "box", Project: "/repo", Title: "Fix the checkout flow", LastEvent: "SessionStart", LastEventAt: "2026-10-16T09:00:00.000Z"},
		{SessionID: "s-1", Task: "ship the fix", Model: "glm-4.7", ContextPercent: 12.5, Pid: 4212, LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:01.000Z"},
		// a report that carries none of the softer fields keeps what the session already said
		{SessionID: "s-1", LastEvent: "PostToolUse", LastEventAt: "2026-10-16T09:00:02.000Z"},
	}
	for _, rep := range reports {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.LastEvent, err)
		}
	}
	events, err := db.st.All(ctx, "SELECT session_id, at, kind, json FROM events ORDER BY seq")
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d rows, want 4", len(events))
	}
	for i, want := range []string{"SessionStart", "UserPromptSubmit", "PostToolUse"} {
		if events[i]["kind"] != want {
			t.Errorf("event %d kind = %v, want %s", i, events[i]["kind"], want)
		}
		if events[i]["session_id"] != "s-1" {
			t.Errorf("event %d session = %v, want s-1", i, events[i]["session_id"])
		}
	}
	if at := events[0]["at"]; at != "2026-10-16T09:00:00.000Z" {
		t.Errorf("event 0 at = %v, want the report's own time", at)
	}
	if body, _ := events[2]["json"].(string); body == "" || body[0] != '{' {
		t.Errorf("event 2 json = %q, want the whole report as JSON", body)
	}
	row, err := db.st.Get(ctx, "SELECT * FROM sessions WHERE session_id = 's-1'")
	if err != nil {
		t.Fatalf("read the session: %v", err)
	}
	if row == nil {
		t.Fatal("no session row after three reports")
	}
	// the title is the first one said (SPEC 5.1: first prompt, until the session sets one); the
	// task, model and the rest are the newest said
	if row["title"] != "Fix the checkout flow" {
		t.Errorf("title = %v, want the first one", row["title"])
	}
	if row["task"] != "ship the fix" {
		t.Errorf("task = %v, want the newest one", row["task"])
	}
	if row["model"] != "glm-4.7" || row["context_percent"] != float64(12.5) {
		t.Errorf("model/context = %v/%v, want glm-4.7/12.5", row["model"], row["context_percent"])
	}
	if row["last_event"] != "PostToolUse" || row["last_event_at"] != "2026-10-16T09:00:02.000Z" {
		t.Errorf("last event = %v at %v, want PostToolUse at 09:00:02", row["last_event"], row["last_event_at"])
	}
	if row["machine"] != "box" || row["project"] != "/repo" || row["pid"] != int64(4212) {
		t.Errorf("machine/project/pid = %v/%v/%v", row["machine"], row["project"], row["pid"])
	}
}

func TestReportKeepsEachSessionToItself(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for _, rep := range []Report{
		{SessionID: "s-a", Task: "a's task", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:00.000Z"},
		{SessionID: "s-b", Task: "b's task", LastEvent: "Stop", LastEventAt: "2026-10-16T09:00:01.000Z"},
	} {
		if err := db.Report(ctx, rep); err != nil {
			t.Fatalf("report %s: %v", rep.SessionID, err)
		}
	}
	rows, err := db.st.All(ctx, "SELECT session_id, task FROM sessions ORDER BY session_id")
	if err != nil {
		t.Fatalf("read sessions: %v", err)
	}
	if len(rows) != 2 || rows[0]["task"] != "a's task" || rows[1]["task"] != "b's task" {
		t.Errorf("sessions = %v, want one row per session with its own task", rows)
	}
}

// openForTest answers an open registry database closed with the test.
func openForTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
