package hub

// Device pairing (SPEC 7.3): a one-use link is spent by exactly one finish, the device it pays
// for lands with its session, the audit row and the alert event, and removing a device ends its
// sessions with it. The database holds digests only, so nothing here writes a token.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// pairNow is one moment the pairing fixtures build their times from.
var pairNow = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

// pairDevice answers one device the shape a WebAuthn enrolment carries.
func pairDevice(name string) Device {
	return Device{
		Name:           name,
		Handle:         []byte("handle-" + name),
		CredentialJSON: []byte(`{"type":"public-key","id":"cred-` + name + `"}`),
	}
}

// savePairing records one live pairing link.
func savePairing(t *testing.T, db *DB, digest string, expires time.Time) {
	t.Helper()
	if err := db.SavePairing(context.Background(), digest, expires.UnixMilli()); err != nil {
		t.Fatalf("save the pairing: %v", err)
	}
}

func TestSavePairingKeepsOnlyWhatABrowserCouldStillOpen(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest-live", pairNow.Add(10*time.Minute))
	savePairing(t, db, "digest-stale", pairNow.Add(-time.Minute))
	if !db.PairingLive(ctx, "digest-live", pairNow.UnixMilli()) {
		t.Error("an unexpired pairing reads dead")
	}
	if db.PairingLive(ctx, "digest-stale", pairNow.UnixMilli()) {
		t.Error("an expired pairing survived the sweep")
	}
	if db.PairingLive(ctx, "digest-never", pairNow.UnixMilli()) {
		t.Error("an unknown digest reads live")
	}
}

func TestPairFinishSpendsTheLinkAndRecordsTheDevice(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest-one", pairNow.Add(10*time.Minute))
	sessionExpires := pairNow.Add(12 * time.Hour)
	if err := db.PairFinish(ctx, "digest-one", pairDevice("iphone"), "session-digest", sessionExpires.UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	devices, err := db.Devices(ctx)
	if err != nil || len(devices) != 1 {
		t.Fatalf("devices = %+v (%v), want the one paired device", devices, err)
	}
	if devices[0].Name != "iphone" || string(devices[0].Handle) != "handle-iphone" || !strings.Contains(string(devices[0].CredentialJSON), "cred-iphone") {
		t.Errorf("the paired device = %+v", devices[0])
	}
	if devices[0].AddedMS != pairNow.UnixMilli() || devices[0].SeenMS != pairNow.UnixMilli() {
		t.Errorf("added/seen = %d/%d, want the pairing's moment", devices[0].AddedMS, devices[0].SeenMS)
	}
	// the session the browser earned answers its device while it lives
	name, live, err := db.DeviceSession(ctx, "session-digest", pairNow.Add(time.Second).UnixMilli())
	if err != nil || !live || name != "iphone" {
		t.Fatalf("DeviceSession = (%q, %v, %v), want iphone live", name, live, err)
	}
	// the link is spent: a second finish answers PairingGoneError and records nothing
	err = db.PairFinish(ctx, "digest-one", pairDevice("other"), "other-digest", sessionExpires.UnixMilli(), pairNow.UnixMilli())
	var gone PairingGoneError
	if !errors.As(err, &gone) {
		t.Fatalf("a second finish = %v, want PairingGoneError", err)
	}
	if msg := gone.Error(); msg != "the pairing link is gone" {
		t.Errorf("the refusal = %q", msg)
	}
	if _, live, _ := db.DeviceSession(ctx, "other-digest", pairNow.Add(time.Second).UnixMilli()); live {
		t.Error("a spent link paid for a second session")
	}
	// a link that was never there is gone too
	err = db.PairFinish(ctx, "digest-never", pairDevice("third"), "third-digest", sessionExpires.UnixMilli(), pairNow.UnixMilli())
	if !errors.As(err, &gone) {
		t.Errorf("an unknown link's finish = %v, want PairingGoneError", err)
	}
	// the pairing wrote its audit row and its alert event
	audit, err := db.Audit(ctx, AuditFilter{})
	if err != nil || len(audit) == 0 {
		t.Fatalf("audit = %v (%v), want the pair row", audit, err)
	}
	if audit[0].Action != "pair" || audit[0].Actor != "iphone" || audit[0].Target != "iphone" {
		t.Errorf("the audit row = %+v, want the pair of iphone", audit[0])
	}
	alerts, err := db.Alerts(ctx, AlertsFilter{})
	if err != nil || len(alerts) == 0 {
		t.Fatalf("alerts = %v (%v), want the pairing alert", alerts, err)
	}
	if alerts[0].Kind != "alert.device_paired" || alerts[0].Body == nil || !strings.Contains(alerts[0].Body.EncodeJSON(), "iphone") {
		t.Errorf("the alert = %+v, want alert.device_paired naming iphone", alerts[0])
	}
}

func TestLoginFinishUpdatesTheCredentialAndMovesTheSession(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest", pairNow.Add(10*time.Minute))
	if err := db.PairFinish(ctx, "digest", pairDevice("mac"), "old-session", pairNow.Add(time.Hour).UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	updated := Device{Name: "mac", Handle: []byte("handle-mac"), CredentialJSON: []byte(`{"counter":7}`)}
	if err := db.LoginFinish(ctx, updated, "new-session", pairNow.Add(12*time.Hour).UnixMilli(), pairNow.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatalf("LoginFinish: %v", err)
	}
	devices, _ := db.Devices(ctx)
	if len(devices) != 1 || !strings.Contains(string(devices[0].CredentialJSON), `"counter":7`) {
		t.Fatalf("devices = %+v, want the credential's new counter", devices)
	}
	if devices[0].SeenMS != pairNow.Add(time.Minute).UnixMilli() {
		t.Errorf("seen = %d, want the sign-in's moment", devices[0].SeenMS)
	}
	for _, digest := range []string{"new-session", "old-session"} {
		if _, live, _ := db.DeviceSession(ctx, digest, pairNow.Add(2*time.Minute).UnixMilli()); !live {
			t.Errorf("session %s reads dead after the sign-in", digest)
		}
	}
	// the sign-in wrote its own audit row beside the pairing's
	audit, _ := db.Audit(ctx, AuditFilter{})
	if len(audit) != 2 || audit[0].Action != "signin" {
		t.Fatalf("audit = %+v, want the pair row then the sign-in", audit)
	}
}

func TestDeviceLookupsAnswerByNameHandleAndNothingElse(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest", pairNow.Add(10*time.Minute))
	if err := db.PairFinish(ctx, "digest", pairDevice("ipad"), "session", pairNow.Add(time.Hour).UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	byName, ok, err := db.DeviceByName(ctx, "ipad")
	if err != nil || !ok || byName.Name != "ipad" {
		t.Fatalf("DeviceByName = (%+v, %v, %v), want ipad", byName, ok, err)
	}
	byHandle, ok, err := db.DeviceByHandle(ctx, []byte("handle-ipad"))
	if err != nil || !ok || byHandle.Name != "ipad" {
		t.Fatalf("DeviceByHandle = (%+v, %v, %v), want ipad", byHandle, ok, err)
	}
	if _, ok, err := db.DeviceByName(ctx, "nobody"); err != nil || ok {
		t.Errorf("DeviceByName(nobody) = (%v, %v), want none", ok, err)
	}
	if _, ok, err := db.DeviceByHandle(ctx, []byte("nobody")); err != nil || ok {
		t.Errorf("DeviceByHandle(nobody) = (%v, %v), want none", ok, err)
	}
	if listed, err := db.Devices(ctx); err != nil || len(listed) != 1 {
		t.Errorf("Devices = %+v (%v), want the one device", listed, err)
	}
}

func TestDeviceRemoveEndsTheDeviceAndItsSessions(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest", pairNow.Add(10*time.Minute))
	if err := db.PairFinish(ctx, "digest", pairDevice("phone"), "session", pairNow.Add(time.Hour).UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	removed, err := db.DeviceRemove(ctx, "nobody", pairNow.Add(time.Minute).UnixMilli())
	if err != nil || removed {
		t.Fatalf("removing an unknown device = (%v, %v), want false", removed, err)
	}
	removed, err = db.DeviceRemove(ctx, "phone", pairNow.Add(time.Minute).UnixMilli())
	if err != nil || !removed {
		t.Fatalf("removing the device = (%v, %v), want it gone", removed, err)
	}
	if _, live, _ := db.DeviceSession(ctx, "session", pairNow.Add(2*time.Minute).UnixMilli()); live {
		t.Error("the removed device's session survived the removal")
	}
	if _, ok, _ := db.DeviceByName(ctx, "phone"); ok {
		t.Error("the removed device is still listed")
	}
	// the removal wrote its revoke row
	audit, _ := db.Audit(ctx, AuditFilter{})
	if audit[0].Action != "revoke" || audit[0].Actor != "terminal" || audit[0].Target != "phone" {
		t.Errorf("the audit row = %+v, want the revoke of phone", audit[0])
	}
}

func TestDeviceSessionRefreshesAStaleLastSeen(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest", pairNow.Add(10*time.Minute))
	if err := db.PairFinish(ctx, "digest", pairDevice("pad"), "session", pairNow.Add(time.Hour).UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	later := pairNow.Add(10 * time.Minute).UnixMilli()
	// a device seen a minute ago is not rewritten; one seen ten minutes ago is
	if err := db.st.Run(ctx, "UPDATE devices SET seen = ? WHERE name = ?", later-deviceSeenWindow, "pad"); err != nil {
		t.Fatalf("age the device: %v", err)
	}
	if _, live, err := db.DeviceSession(ctx, "session", later); err != nil || !live {
		t.Fatalf("DeviceSession = (_, %v, %v), want live", live, err)
	}
	row, err := db.st.Get(ctx, "SELECT seen FROM devices WHERE name = ?", "pad")
	if err != nil || row == nil {
		t.Fatalf("the device row: %v %v", row, err)
	}
	if row["seen"] != later {
		t.Errorf("seen = %v, want the request's moment %d", row["seen"], later)
	}
	// a digest of no session answers none
	if _, live, err := db.DeviceSession(ctx, "no-session", later); err != nil || live {
		t.Errorf("DeviceSession(no-session) = (_, %v, %v), want none", live, err)
	}
	if _, live, err := db.DeviceSession(ctx, "session", pairNow.Add(2*time.Hour).UnixMilli()); err != nil || live {
		t.Errorf("an expired session = (_, %v, %v), want none", live, err)
	}
}

// TestSessionExpiredNamesTheKnownButSpentSessions proves the split the sign-in reason reads: a
// digest whose row holds an expiry that passed is expired, a digest no row holds is not (a
// removed device's sessions went with it, the same answer as a live one), and a live session is
// never expired.
func TestSessionExpiredNamesTheKnownButSpentSessions(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest", pairNow.Add(10*time.Minute))
	granted := pairNow.UnixMilli()
	expires := granted + (7 * 24 * time.Hour).Milliseconds()
	if err := db.PairFinish(ctx, "digest", pairDevice("phone"), "session", expires, granted); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	if expired, err := db.SessionExpired(ctx, "session", granted+time.Hour.Milliseconds()); err != nil || expired {
		t.Errorf("a live session = (%v, %v), want not expired", expired, err)
	}
	if expired, err := db.SessionExpired(ctx, "session", expires+1); err != nil || !expired {
		t.Errorf("a spent session = (%v, %v), want expired", expired, err)
	}
	if expired, err := db.SessionExpired(ctx, "no-session", granted); err != nil || expired {
		t.Errorf("a digest of no row = (%v, %v), want not expired", expired, err)
	}
}

// TestSessionRenewWritesOnlyWhenDue proves the one-write-per-hour rule at the storage layer: a
// session renews when its last renewal is older than the window (or it never had one), refuses
// when it is not due, and a session that is expired or gone writes nothing.
func TestSessionRenewWritesOnlyWhenDue(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	savePairing(t, db, "digest", pairNow.Add(10*time.Minute))
	granted := pairNow.UnixMilli()
	if err := db.PairFinish(ctx, "digest", pairDevice("phone"), "session", granted+(7*24*time.Hour).Milliseconds(), granted); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}

	// inside the window: no write, and the row keeps its grant
	hour := int64(time.Hour.Milliseconds())
	inside := granted + hour - 1000
	if renewed, err := db.SessionRenew(ctx, "session", inside, hour, inside+(7*24*time.Hour).Milliseconds()); err != nil || renewed {
		t.Errorf("a renewal inside the window = (%v, %v), want none", renewed, err)
	}

	// past the window: the expiry and the renewal moment both move
	due := granted + hour + 1000
	newExpires := due + (7 * 24 * time.Hour).Milliseconds()
	if renewed, err := db.SessionRenew(ctx, "session", due, hour, newExpires); err != nil || !renewed {
		t.Fatalf("a renewal past the window = (%v, %v), want one", renewed, err)
	}
	row, err := db.st.Get(ctx, "SELECT expires, renewed FROM device_sessions WHERE digest = 'session'")
	if err != nil || row == nil {
		t.Fatalf("read the session: %v %v", row, err)
	}
	if row["expires"] != newExpires || row["renewed"] != due {
		t.Errorf("the renewed row = %v, want expires %d renewed %d", row, newExpires, due)
	}

	// an expired session and a digest of no session write nothing
	late := newExpires + 1000
	if renewed, err := db.SessionRenew(ctx, "session", late, hour, late+(7*24*time.Hour).Milliseconds()); err != nil || renewed {
		t.Errorf("an expired session renewed = (%v, %v)", renewed, err)
	}
	if renewed, err := db.SessionRenew(ctx, "no-session", due, hour, newExpires); err != nil || renewed {
		t.Errorf("a digest of no session renewed = (%v, %v)", renewed, err)
	}
}

// TestMigration16KeepsExistingSessionsAndDuesThem builds the schema the previous release wrote
// (a device_sessions table with no renewal column), opens it with this build, and proves the
// session it holds stays valid and reads due for its first renewal.
func TestMigration16KeepsExistingSessionsAndDuesThem(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hub.db")
	st, err := store.Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open the v15 store: %v", err)
	}
	if err := st.Run(context.Background(), "CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatalf("meta: %v", err)
	}
	for n := 0; n < 15; n++ {
		if err := st.Run(context.Background(), migrations[n]); err != nil {
			t.Fatalf("migration %d: %v", n+1, err)
		}
	}
	if err := st.Run(context.Background(), "INSERT INTO meta (key, value) VALUES ('schema_version', '15')"); err != nil {
		t.Fatalf("version: %v", err)
	}
	at := pairNow.UnixMilli()
	if err := st.Run(context.Background(),
		"INSERT INTO devices (name, handle, credential, added, seen) VALUES ('phone', ?, '{}', ?, ?)",
		[]byte("handle-phone"), at, at); err != nil {
		t.Fatalf("the old device row: %v", err)
	}
	expires := at + (12 * time.Hour).Milliseconds()
	if err := st.Run(context.Background(),
		"INSERT INTO device_sessions (digest, device, expires) VALUES ('session', 'phone', ?)", expires); err != nil {
		t.Fatalf("the old session row: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close the v15 store: %v", err)
	}

	db, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open with this build: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if name, live, err := db.DeviceSession(ctx, "session", at+time.Second.Milliseconds()); err != nil || !live || name != "phone" {
		t.Fatalf("the migrated session = (%q, %v, %v), want phone live", name, live, err)
	}
	// the row the old build wrote carries no renewal moment: its next request renews it
	hour := int64(time.Hour.Milliseconds())
	later := at + time.Second.Milliseconds()
	if renewed, err := db.SessionRenew(ctx, "session", later, hour, later+(7*24*time.Hour).Milliseconds()); err != nil || !renewed {
		t.Errorf("a session with no renewal moment = (%v, %v), want renewed", renewed, err)
	}
}
