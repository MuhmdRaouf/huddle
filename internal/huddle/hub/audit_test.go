package hub

// The audit log and the owner alerts (SPEC 7.7): one appended row per remote decision and
// security event, read back newest first, never updated or deleted by the app; the alerts carry
// a read state on top. Pairing, sign-in and removal rows land through pair.go's transactions;
// these cover the entry points the later decision routes call.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// msOf answers one moment's whole milliseconds, so a stored row's served time is the moment
// the test names.
func msOf(hour, minute, second int) int64 {
	return time.Date(2026, 10, 16, hour, minute, second, 0, time.UTC).UnixMilli()
}

// toolDetail is one decision's detail: what was allowed, never a credential or a challenge.
type toolDetail struct {
	Tool    string `json:"tool"`
	Session string `json:"session"`
}

func TestRecordAppendsExactlyOneRowPerAction(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	actions := []struct {
		atMS   int64
		actor  string
		action string
		target string
		detail any
	}{
		{msOf(9, 0, 0), "owner", "signin", "dashboard", nil},
		{msOf(9, 1, 0), "phone", "allow", "item-7", toolDetail{Tool: "Bash", Session: "s-1"}},
		{msOf(9, 2, 0), "phone", "deny", "item-8", nil},
	}
	for _, a := range actions {
		if _, err := db.Record(ctx, a.atMS, a.actor, a.action, a.target, a.detail); err != nil {
			t.Fatalf("record %s: %v", a.action, err)
		}
	}
	rows, err := db.st.All(ctx, "SELECT id, at, actor, action, target, detail FROM audit ORDER BY id")
	if err != nil {
		t.Fatalf("read the audit table: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("audit = %d rows after 3 actions, want exactly one per action", len(rows))
	}
	for i, a := range actions {
		if rows[i]["id"] != int64(i+1) {
			t.Errorf("row %d id = %v, want %d", i, rows[i]["id"], i+1)
		}
		if rows[i]["at"] != a.atMS || rows[i]["actor"] != a.actor ||
			rows[i]["action"] != a.action || rows[i]["target"] != a.target {
			t.Errorf("row %d = %v, want %v", i, rows[i], a)
		}
	}
	if detail, _ := rows[1]["detail"].(string); detail != `{"tool":"Bash","session":"s-1"}` {
		t.Errorf("row 1 detail = %q, want the compact JSON", detail)
	}
	if rows[0]["detail"] != nil {
		t.Errorf("row 0 detail = %v, want NULL for an action with no detail", rows[0]["detail"])
	}
}

func TestAuditServesTheRowsNewestFirstWithTheirDetail(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if _, err := db.Record(ctx, msOf(9, 0, 0), "owner", "signin", "dashboard", nil); err != nil {
		t.Fatalf("record the sign-in: %v", err)
	}
	if _, err := db.Record(ctx, msOf(9, 1, 0), "phone", "allow", "item-7",
		toolDetail{Tool: "Bash", Session: "s-1"}); err != nil {
		t.Fatalf("record the allow: %v", err)
	}
	rows, err := db.Audit(ctx, AuditFilter{})
	if err != nil {
		t.Fatalf("read the audit: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("audit = %d rows, want 2", len(rows))
	}
	if rows[0].ID != 2 || rows[0].Action != "allow" || rows[0].Actor != "phone" {
		t.Errorf("the newest row = id %d %s by %s, want id 2 allow by phone first",
			rows[0].ID, rows[0].Action, rows[0].Actor)
	}
	if rows[0].At != "2026-10-16T09:01:00.000Z" {
		t.Errorf("the newest row's time = %q, want the stored milliseconds as ISO", rows[0].At)
	}
	if rows[0].Detail == nil || rows[0].Detail.EncodeJSON() != `{"tool":"Bash","session":"s-1"}` {
		t.Errorf("the newest row's detail = %v, want the stored JSON served back", rows[0].Detail)
	}
	if rows[1].Detail != nil {
		t.Errorf("the oldest row's detail = %v, want nil for the NULL column", rows[1].Detail)
	}
}

func TestAuditPaginatesByCursorAndBoundsTheLimit(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	for i := range 5 {
		if _, err := db.Record(ctx, msOf(9, 0, i), "phone", "deny", "item", nil); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	rows, err := db.Audit(ctx, AuditFilter{Limit: 2})
	if err != nil {
		t.Fatalf("read the first page: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 5 || rows[1].ID != 4 {
		t.Fatalf("the first page = %v, want ids 5 then 4", auditIDs(rows))
	}
	before := rows[1].ID
	rows, err = db.Audit(ctx, AuditFilter{Limit: 2, Before: &before})
	if err != nil {
		t.Fatalf("read the second page: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != 3 || rows[1].ID != 2 {
		t.Fatalf("the second page = %v, want ids 3 then 2", auditIDs(rows))
	}
	oldest := int64(1)
	if rows, err = db.Audit(ctx, AuditFilter{Before: &oldest}); err != nil || len(rows) != 0 {
		t.Fatalf("before the oldest = %v (%v), want nothing", auditIDs(rows), err)
	}
	// the bound holds whatever the caller says: 0 takes the default, nothing under 1 serves
	// less than one row, nothing over 500 serves more
	for limit, want := range map[int]int{0: 5, -3: 1, 501: 5, 10000: 5} {
		rows, err = db.Audit(ctx, AuditFilter{Limit: limit})
		if err != nil || len(rows) != want {
			t.Fatalf("limit %d = %v (%v), want %d rows: a limit never empties or over-reads the log",
				limit, auditIDs(rows), err, want)
		}
	}
}

func TestAlertsCarryAReadStateThatOnlyMovesForward(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	first, err := db.RaiseAlert(ctx, msOf(9, 0, 0), "alert.device_paired", pairBody{Device: "phone"})
	if err != nil {
		t.Fatalf("raise the first alert: %v", err)
	}
	second, err := db.RaiseAlert(ctx, msOf(9, 5, 0), "alert.remote_allow", nil)
	if err != nil {
		t.Fatalf("raise the second alert: %v", err)
	}
	rows, err := db.Alerts(ctx, AlertsFilter{})
	if err != nil {
		t.Fatalf("read the alerts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("alerts = %d rows, want 2", len(rows))
	}
	if rows[0].ID != second || rows[0].Kind != "alert.remote_allow" || rows[0].ReadAt != nil {
		t.Errorf("the newest alert = id %d %s read %v, want id %d alert.remote_allow unread",
			rows[0].ID, rows[0].Kind, rows[0].ReadAt, second)
	}
	// a raise with nothing to say stores the empty object, never a broken row
	if rows[0].Body == nil || rows[0].Body.EncodeJSON() != "{}" {
		t.Errorf("the newest alert's body = %v, want the empty object", rows[0].Body)
	}
	if rows[1].ID != first || rows[1].ReadAt != nil || rows[1].Body == nil ||
		rows[1].Body.EncodeJSON() != `{"device":"phone"}` {
		t.Errorf("the oldest alert = id %d read %v body %v, want id %d unread with its body",
			rows[1].ID, rows[1].ReadAt, rows[1].Body, first)
	}
	if rows[1].At != "2026-10-16T09:00:00.000Z" {
		t.Errorf("the oldest alert's time = %q, want the stored milliseconds as ISO", rows[1].At)
	}
	marked, err := db.MarkAlertsRead(ctx, second, msOf(10, 0, 0))
	if err != nil || marked != 2 {
		t.Fatalf("mark through the newest = %d (%v), want both rows marked", marked, err)
	}
	// read state is monotonic: marking again, or through an older id, never unmarks or restamps
	marked, err = db.MarkAlertsRead(ctx, first, msOf(9, 30, 0))
	if err != nil || marked != 0 {
		t.Fatalf("mark through the oldest again = %d (%v), want nothing already read touched", marked, err)
	}
	rows, err = db.Alerts(ctx, AlertsFilter{})
	if err != nil {
		t.Fatalf("reread the alerts: %v", err)
	}
	for _, row := range rows {
		if row.ReadAt == nil || *row.ReadAt != "2026-10-16T10:00:00.000Z" {
			t.Errorf("alert %d read_at = %v, want the first marking kept", row.ID, row.ReadAt)
		}
	}
	if rows, err = db.Alerts(ctx, AlertsFilter{UnreadOnly: true}); err != nil || len(rows) != 0 {
		t.Fatalf("the unread filter = %v (%v), want nothing: both are read", alertIDs(rows), err)
	}
}

func TestRecordingAndRaisingRefuseADetailThatCannotBeEncoded(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	// a value JSON cannot name is refused, never stored half-written
	if _, err := db.Record(ctx, msOf(9, 0, 0), "phone", "allow", "item-1", make(chan int)); err == nil {
		t.Error("recording an unencodable detail succeeds, want the refusal")
	}
	if _, err := db.RaiseAlert(ctx, msOf(9, 0, 0), "alert.remote_allow", make(chan int)); err == nil {
		t.Error("raising an unencodable body succeeds, want the refusal")
	}
	for _, table := range []string{"audit", "alerts"} {
		rows, err := db.st.All(ctx, "SELECT 1 FROM "+table)
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		if len(rows) != 0 {
			t.Errorf("%s = %d rows, want nothing: the refusal stored no half-row", table, len(rows))
		}
	}
}

func TestAReadRefusesADetailColumnThatIsNotJSON(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.st.Run(ctx,
		"INSERT INTO audit (at, actor, action, target, detail) VALUES (?, 'phone', 'allow', 'item-1', '{not json')",
		msOf(9, 0, 0)); err != nil {
		t.Fatalf("seed a broken row: %v", err)
	}
	if _, err := db.Audit(ctx, AuditFilter{}); err == nil {
		t.Error("a detail column that is not JSON serves, want the refusal")
	}
}

func TestMarkingReadNamesItsOwnFailure(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.st.Run(ctx, "DROP TABLE alerts"); err != nil {
		t.Fatalf("drop the alerts table: %v", err)
	}
	if _, err := db.MarkAlertsRead(ctx, 1, msOf(10, 0, 0)); err == nil {
		t.Error("marking read without the table succeeds, want the error named")
	}
}

// forbidden is every statement shape no source under the huddle backend may run against the
// audit table: the log is append-only (SPEC 7.7), so a delete, an update or a drop anywhere in
// the app fails the test. The patterns live only here, and the scan skips test files, so this
// file never matches itself.
var forbidden = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bDELETE\s+FROM\s+audit\b`),
	regexp.MustCompile(`(?i)\bUPDATE\s+audit\b`),
	regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?audit\b`),
}

func TestNoSourceUnderTheHuddleBackendDeletesOrUpdatesAnAuditRow(t *testing.T) {
	scanned := 0
	for _, root := range []string{"..", "../../../cmd/huddle"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// fixtures and the tests themselves are not the app; everything else .go is
			if entry.IsDir() {
				if entry.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			scanned++
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, pattern := range forbidden {
				if pattern.MatchString(string(raw)) {
					t.Errorf("%s matches %s: the audit log is append-only", path, pattern)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if scanned == 0 {
		t.Fatal("no source file was scanned: the walk itself is broken")
	}
}

// pairBody is one pairing alert's body.
type pairBody struct {
	Device string `json:"device"`
}

// auditIDs and alertIDs list one read's row ids, for the failure messages.
func auditIDs(rows []AuditRow) []int64 {
	out := make([]int64, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out
}

func alertIDs(rows []AlertRow) []int64 {
	out := make([]int64, len(rows))
	for i, row := range rows {
		out[i] = row.ID
	}
	return out
}
