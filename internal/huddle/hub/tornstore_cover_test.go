package hub

// The routes' torn-database refusals: a statement that cannot run names its failure instead of
// answering half a read. The broken store is a real database with one table dropped, so each
// statement's own error branch runs.

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// dropTable removes one table from the test's own database, so every statement naming it fails
// the way a broken file does.
func dropTable(t *testing.T, db *DB, table string) {
	t.Helper()
	if err := db.st.Run(context.Background(), "DROP TABLE "+table); err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
}

// dbWithout answers a fresh hub database with one table dropped.
func dbWithout(t *testing.T, table string) *DB {
	t.Helper()
	db := openForTest(t)
	dropTable(t, db, table)
	return db
}

func TestQueueMessageNamesAFailedWrite(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	dropTable(t, db, "messages")
	if _, err := db.QueueMessage(ctx, MessageIn{SessionID: "s-1", FromKind: FromOwner, Text: "x"}, "2026-10-16T09:00:00.000Z"); err == nil {
		t.Fatal("a write to a broken messages table succeeded")
	}
	if _, err := db.TakeMessages(ctx, "s-1", "2026-10-16T09:00:00.000Z"); err == nil {
		t.Fatal("a read from a broken messages table succeeded")
	}
	if _, err := db.PendingOf(ctx, "shop", "api"); err == nil {
		t.Fatal("a pending read joining a broken messages table succeeded")
	}
	sessions := dbWithout(t, "sessions")
	if _, err := sessions.SessionOfMember(ctx, "shop", "api"); err == nil {
		t.Fatal("a member read from a broken sessions table succeeded")
	}
	acks := dbWithout(t, "push_acks")
	if err := acks.RecordPushAck(ctx, "shop", "api", listening, "2026-10-16T09:00:00.000Z"); err == nil {
		t.Fatal("an ack write to a broken push_acks table succeeded")
	}
	if got := acks.routeOfMember(ctx, "shop", "api", readNow); got != RouteNextPrompt {
		t.Errorf("a route read from a broken push_acks table read %s", got)
	}
}

func TestPermissionRoutesNameAFailedStatement(t *testing.T) {
	ctx := context.Background()
	db := dbWithout(t, "items")
	if _, err := db.CreatePermissionItem(ctx, "s-1", "Bash", []byte(`{"command":"npm test"}`), permNow); err == nil {
		t.Fatal("a write to a broken items table succeeded")
	}
	if _, err := db.PermissionItemByID(ctx, 1, permNow); err == nil {
		t.Fatal("a read from a broken items table succeeded")
	}
	if _, err := db.DecidePermission(ctx, 1, "allow", nil, "", isoPerm(permNow), permNow); err == nil {
		t.Fatal("a decision on a broken items table succeeded")
	}
	if _, err := db.WithdrawPermissions(ctx, "s-1", isoPerm(permNow)); err == nil {
		t.Fatal("a withdrawal on a broken items table succeeded")
	}
	if _, err := db.ExpirePermissions(ctx, permNow); err == nil {
		t.Fatal("an expiry sweep on a broken items table succeeded")
	}
	if err := db.TouchPoll(ctx, 1, isoPerm(permNow)); err == nil {
		t.Fatal("a poll touch on a broken items table succeeded")
	}
	if _, err := db.Items(ctx, permNow); err == nil {
		t.Fatal("the shared list read from a broken items table")
	}
	if _, err := db.OpenItemSessions(ctx, permNow); err == nil {
		t.Fatal("the waiting sessions read from a broken items table")
	}
}

func TestSessionReadsNameAFailedStatement(t *testing.T) {
	ctx := context.Background()
	db := dbWithout(t, "sessions")
	if _, err := db.Sessions(ctx, SessionsFilter{}, readNow); err == nil {
		t.Fatal("a sessions read from a broken table succeeded")
	}
	if _, err := db.SessionRow(ctx, "s-1", readNow); err == nil {
		t.Fatal("a session row read from a broken table succeeded")
	}
	if _, err := db.Session(ctx, "s-1", readNow); err == nil {
		t.Fatal("a session detail read from a broken table succeeded")
	}
	// a session that is not registered answers none
	whole := openForTest(t)
	if row, err := whole.SessionRow(ctx, "s-none", readNow); err != nil || row != nil {
		t.Errorf("an unregistered session = (%v, %v), want none", row, err)
	}
	// a registered session whose items table broke still reads, and reads no waiting item: a
	// broken list never blocks a session read
	if err := whole.Report(ctx, Report{SessionID: "s-here", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"}); err != nil {
		t.Fatalf("report: %v", err)
	}
	dropTable(t, whole, "items")
	if row, err := whole.SessionRow(ctx, "s-here", readNow); err != nil || row == nil || row.State != StateWorking {
		t.Errorf("a read with a broken items table = (%+v, %v), want the row reading working", row, err)
	}
}

func TestTheRegistryWritesNameAFailedStatement(t *testing.T) {
	db := dbWithout(t, "sessions")
	if _, err := db.MarkEndedPids(context.Background(), "2026-10-16T09:00:00.000Z", func(context.Context, int) PidInfo { return PidInfo{} }); err == nil {
		t.Fatal("the pid check on a broken sessions table succeeded")
	}
	if _, err := db.OpenTheSession(context.Background(), "s-1", "2026-10-16T09:00:00.000Z"); err == nil {
		t.Fatal("an open of a broken sessions table succeeded")
	}
	dropTable(t, db, "items")
	if err := db.SyncBlocked(context.Background(), nil, "2026-10-16T09:00:00.000Z"); err == nil {
		t.Fatal("a sync on a broken items table succeeded")
	}
	// a Stop for a session with no prompt grew no turn item, and a report whose events table
	// broke refuses whole
	dropTable(t, db, "events")
	if err := db.Report(context.Background(), Report{SessionID: "s-1", LastEvent: "UserPromptSubmit", LastEventAt: "2026-10-16T09:00:00.000Z"}); err == nil {
		t.Fatal("a report into a broken events table succeeded")
	}
}

func TestDeviceRoutesNameAFailedStatement(t *testing.T) {
	ctx := context.Background()
	devices := dbWithout(t, "devices")
	if _, err := devices.Devices(ctx); err == nil {
		t.Fatal("a devices read from a broken table succeeded")
	}
	if _, _, err := devices.DeviceByName(ctx, "x"); err == nil {
		t.Fatal("a device lookup on a broken devices table succeeded")
	}
	if _, _, err := devices.DeviceByHandle(ctx, []byte("x")); err == nil {
		t.Fatal("a device lookup on a broken devices table succeeded")
	}
	if _, err := devices.DeviceRemove(ctx, "x", 0); err == nil {
		t.Fatal("a removal on a broken devices table succeeded")
	}
	if err := devices.LoginFinish(ctx, pairDevice("x"), "d", 0, 0); err == nil {
		t.Fatal("a sign-in on a broken devices table succeeded")
	}
	pairings := dbWithout(t, "pairings")
	if err := pairings.SavePairing(ctx, "d", 0); err == nil {
		t.Fatal("a pairing save on a broken pairings table succeeded")
	}
	if pairings.PairingLive(ctx, "d", 0) {
		t.Error("a pairing read from a broken pairings table read live")
	}
	sessions := dbWithout(t, "device_sessions")
	if _, _, err := sessions.DeviceSession(ctx, "d", 0); err == nil {
		t.Fatal("a session lookup on a broken device_sessions table succeeded")
	}
	if err := sessions.PairFinish(ctx, "d", pairDevice("y"), "s", 0, 0); err == nil {
		t.Fatal("a finish on a broken device_sessions table succeeded")
	}
}

func TestTheAuditRoutesNameAFailedStatement(t *testing.T) {
	ctx := context.Background()
	audit := dbWithout(t, "audit")
	if _, err := audit.Record(ctx, 0, "a", "b", "c", nil); err == nil {
		t.Fatal("an audit write to a broken audit table succeeded")
	}
	if _, err := audit.Audit(ctx, AuditFilter{}); err == nil {
		t.Fatal("an audit read from a broken audit table succeeded")
	}
	alerts := dbWithout(t, "alerts")
	if _, err := alerts.RaiseAlert(ctx, 0, "kind", nil); err == nil {
		t.Fatal("an alert raise on a broken alerts table succeeded")
	}
	if _, err := alerts.Alerts(ctx, AlertsFilter{}); err == nil {
		t.Fatal("an alerts read from a broken alerts table succeeded")
	}
	if _, err := alerts.MarkAlertsRead(ctx, 1, 0); err == nil {
		t.Fatal("a read-marking on a broken alerts table succeeded")
	}
	satellites := dbWithout(t, "satellites")
	if err := satellites.SaveSatellite(ctx, "digest", "mbp", 0); err == nil {
		t.Fatal("a satellite save on a broken satellites table succeeded")
	}
}

// TestOpenRefusesAFileThatIsNotADatabase pins the open's refusals: a directory in the file's
// place fails at the store, and a meta version that parses as no number migrates from the
// start instead of refusing.
func TestOpenRefusesAFileThatIsNotADatabase(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(context.Background(), dir); err == nil {
		t.Fatal("a directory opened as a hub database")
	}
	file := filepath.Join(dir, "weird.db")
	st, err := store.Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Run(context.Background(), "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatalf("meta: %v", err)
	}
	if err := st.Run(context.Background(), "INSERT INTO meta (key, value) VALUES ('schema_version', 'not a number')"); err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("a torn version refused the file: %v", err)
	}
	defer db.Close()
	if row, err := db.st.Get(context.Background(), "SELECT value FROM meta WHERE key = 'schema_version'"); err != nil || row["value"] != strconv.Itoa(SchemaVersion) {
		t.Errorf("schema_version = %v (%v), want %d after the migration from the start", row, err, SchemaVersion)
	}
}
