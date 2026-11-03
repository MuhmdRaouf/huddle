package hub

// The passkey half of the device tables: the assertion window a dangerous act rides on, the
// step-up that stamps it, the first passkey a signed-in session adds, whether any device is
// paired, and the per-device wake for a finished turn.

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// pairedDevice pairs one device with a session born stepped up at pairNow and answers the
// session's digest.
func pairedDevice(t *testing.T, db *DB, name string) string {
	t.Helper()
	savePairing(t, db, "pairing-"+name, pairNow.Add(10*time.Minute))
	session := "session-" + name
	if err := db.PairFinish(context.Background(), "pairing-"+name, pairDevice(name), session,
		pairNow.Add(12*time.Hour).UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	return session
}

// TestSessionSteppedUpCoversOnlyALiveSessionInsideTheWindow: a session earns its assertion at
// pairing; it reads covered up to the window's edge and uncovered past it, and a digest no live
// session answers, or one whose expiry passed, reads uncovered.
func TestSessionSteppedUpCoversOnlyALiveSessionInsideTheWindow(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	session := pairedDevice(t, db, "phone")
	window := (5 * time.Minute).Milliseconds()
	edge := pairNow.UnixMilli() + window

	for _, c := range []struct {
		name   string
		digest string
		now    int64
		want   bool
	}{
		{"just after the assertion", session, pairNow.UnixMilli() + 1, true},
		{"at the window's edge", session, edge, true},
		{"one millisecond past the window", session, edge + 1, false},
		{"a digest no session holds", "no-session", pairNow.UnixMilli() + 1, false},
		{"after the session expired", session, pairNow.Add(13 * time.Hour).UnixMilli(), false},
	} {
		got, err := db.SessionSteppedUp(ctx, c.digest, c.now, window)
		if err != nil || got != c.want {
			t.Errorf("%s: SessionSteppedUp = (%v, %v), want %v", c.name, got, err, c.want)
		}
	}
}

// TestSessionSteppedUpReadsASessionThatNeverAssertedAsUncovered: a session whose assertion
// moment was never set is uncovered at any time.
func TestSessionSteppedUpReadsASessionThatNeverAssertedAsUncovered(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	session := pairedDevice(t, db, "tablet")
	if err := db.st.Run(ctx, "UPDATE device_sessions SET stepup_at = NULL WHERE digest = ?", session); err != nil {
		t.Fatalf("clear the assertion: %v", err)
	}
	if got, err := db.SessionSteppedUp(ctx, session, pairNow.UnixMilli()+1, time.Hour.Milliseconds()); err != nil || got {
		t.Errorf("SessionSteppedUp = (%v, %v), want uncovered", got, err)
	}
}

// TestStepUpFinishStampsALiveSessionAndKeepsTheCountersCredential: a step-up moves the session's
// assertion forward so it reads covered again, and stores the credential's fresh signature
// counter and the moment the device was seen.
func TestStepUpFinishStampsALiveSessionAndKeepsTheCountersCredential(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	session := pairedDevice(t, db, "laptop")
	window := (5 * time.Minute).Milliseconds()
	later := pairNow.Add(time.Hour).UnixMilli()
	if covered, _ := db.SessionSteppedUp(ctx, session, later, window); covered {
		t.Fatal("the session read covered an hour after its only assertion")
	}

	fresh := Device{Name: "laptop", CredentialJSON: []byte(`{"counter":9}`)}
	stamped, err := db.StepUpFinish(ctx, session, fresh, later)
	if err != nil || !stamped {
		t.Fatalf("StepUpFinish = (%v, %v), want stamped", stamped, err)
	}
	if covered, err := db.SessionSteppedUp(ctx, session, later+1, window); err != nil || !covered {
		t.Errorf("after the step-up SessionSteppedUp = (%v, %v), want covered", covered, err)
	}
	device, ok, err := db.DeviceByName(ctx, "laptop")
	if err != nil || !ok {
		t.Fatalf("DeviceByName = (%v, %v)", ok, err)
	}
	if !strings.Contains(string(device.CredentialJSON), `"counter":9`) || device.SeenMS != later {
		t.Errorf("the device = %+v, want the new counter seen at %d", device, later)
	}
}

// TestStepUpFinishAnswersFalseForASessionThatIsGone: a session that expired or was never there
// is not stamped.
func TestStepUpFinishAnswersFalseForASessionThatIsGone(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	session := pairedDevice(t, db, "watch")
	fresh := Device{Name: "watch", CredentialJSON: []byte(`{"counter":2}`)}
	if stamped, err := db.StepUpFinish(ctx, "no-session", fresh, pairNow.UnixMilli()+1); err != nil || stamped {
		t.Errorf("an unknown session = (%v, %v), want not stamped", stamped, err)
	}
	if stamped, err := db.StepUpFinish(ctx, session, fresh, pairNow.Add(13*time.Hour).UnixMilli()); err != nil || stamped {
		t.Errorf("an expired session = (%v, %v), want not stamped", stamped, err)
	}
}

// TestStepUpFinishWritesNothingForADeadSession: a step-up whose session is expired, removed, or
// was never there answers false and leaves the device row, its credential and its seen moment,
// exactly as it was, so a session that dies between the check and the write moves no counter.
func TestStepUpFinishWritesNothingForADeadSession(t *testing.T) {
	ctx := context.Background()
	expired := pairNow.Add(13 * time.Hour).UnixMilli()
	live := pairNow.Add(time.Hour).UnixMilli()
	for _, c := range []struct {
		name   string
		digest func(session string) string
		kill   func(t *testing.T, db *DB, session string)
		at     int64
	}{
		{"an expired session", func(s string) string { return s }, func(*testing.T, *DB, string) {}, expired},
		{"a session at its exact expiry", func(s string) string { return s }, func(*testing.T, *DB, string) {},
			pairNow.Add(12 * time.Hour).UnixMilli()},
		{"a digest no session holds", func(string) string { return "no-session" }, func(*testing.T, *DB, string) {}, live},
		{"a removed session", func(s string) string { return s }, func(t *testing.T, db *DB, s string) {
			t.Helper()
			if gone, err := db.SessionRemove(ctx, s); err != nil || !gone {
				t.Fatalf("SessionRemove = (%v, %v)", gone, err)
			}
		}, live},
		{"every session removed", func(s string) string { return s }, func(t *testing.T, db *DB, _ string) {
			t.Helper()
			if gone, err := db.SessionsRemoveAll(ctx); err != nil || gone != 1 {
				t.Fatalf("SessionsRemoveAll = (%d, %v)", gone, err)
			}
		}, live},
	} {
		t.Run(c.name, func(t *testing.T) {
			db := openForTest(t)
			session := pairedDevice(t, db, "watch")
			c.kill(t, db, session)
			before := deviceRow(t, db, "watch")

			fresh := Device{Name: "watch", CredentialJSON: []byte(`{"counter":99}`)}
			stamped, err := db.StepUpFinish(ctx, c.digest(session), fresh, c.at)
			if err != nil || stamped {
				t.Fatalf("StepUpFinish = (%v, %v), want not stamped", stamped, err)
			}
			if after := deviceRow(t, db, "watch"); !reflect.DeepEqual(before, after) {
				t.Errorf("the device row = %v, want it untouched: %v", after, before)
			}
		})
	}
}

// deviceRow reads one device's whole row as the table holds it.
func deviceRow(t *testing.T, db *DB, name string) map[string]any {
	t.Helper()
	row, err := db.st.Get(context.Background(), "SELECT * FROM devices WHERE name = ?", name)
	if err != nil || row == nil {
		t.Fatalf("read the %s row = (%v, %v)", name, row, err)
	}
	return row
}

// TestAnyDeviceEndsTheSoloWaiverOnceADeviceIsPaired: no device is paired in a fresh home, one is
// after a pairing, and none again once it is removed.
func TestAnyDeviceEndsTheSoloWaiverOnceADeviceIsPaired(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if paired, err := db.AnyDevice(ctx); err != nil || paired {
		t.Fatalf("a fresh home = (%v, %v), want no device", paired, err)
	}
	pairedDevice(t, db, "phone")
	if paired, err := db.AnyDevice(ctx); err != nil || !paired {
		t.Fatalf("after a pairing = (%v, %v), want a device", paired, err)
	}
	if removed, err := db.DeviceRemove(ctx, "phone", pairNow.Add(time.Minute).UnixMilli()); err != nil || !removed {
		t.Fatalf("remove = (%v, %v)", removed, err)
	}
	if paired, err := db.AnyDevice(ctx); err != nil || paired {
		t.Errorf("after the removal = (%v, %v), want no device", paired, err)
	}
}

// TestPasskeyFinishLandsTheCredentialOnTheDeviceRowAndAudits: the credential a signed-in session
// registers replaces the device's, keeps its name and its sessions, and writes the passkey.add
// audit row; a device removed between the ceremony's halves answers false and writes nothing.
func TestPasskeyFinishLandsTheCredentialOnTheDeviceRowAndAudits(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	session := pairedDevice(t, db, "desk")
	at := pairNow.Add(time.Minute).UnixMilli()

	landed, err := db.PasskeyFinish(ctx, "desk", []byte(`{"id":"second-key"}`), at)
	if err != nil || !landed {
		t.Fatalf("PasskeyFinish = (%v, %v), want landed", landed, err)
	}
	device, ok, _ := db.DeviceByName(ctx, "desk")
	if !ok || !strings.Contains(string(device.CredentialJSON), "second-key") || device.SeenMS != at {
		t.Errorf("the device = %+v, want the new credential seen at %d", device, at)
	}
	if name, live, _ := db.DeviceSession(ctx, session, at); !live || name != "desk" {
		t.Errorf("the device's session = (%q, %v), want it kept", name, live)
	}
	audit, err := db.Audit(ctx, AuditFilter{})
	if err != nil || len(audit) == 0 || audit[0].Action != "passkey.add" || audit[0].Actor != "desk" || audit[0].Target != "desk" {
		t.Fatalf("audit = %+v (%v), want the passkey.add of desk first", audit, err)
	}

	before := len(audit)
	landed, err = db.PasskeyFinish(ctx, "ghost", []byte(`{}`), at)
	if err != nil || landed {
		t.Errorf("a removed device = (%v, %v), want not landed", landed, err)
	}
	if after, _ := db.Audit(ctx, AuditFilter{}); len(after) != before {
		t.Errorf("audit rows = %d, want the %d from before: a refused finish writes none", len(after), before)
	}
}

// TestDeviceTurnRecordsWhetherADeviceIsWokenForAFinishedTurn: a device starts off, turns on and
// off again on request, and a device that is not there reads off.
func TestDeviceTurnRecordsWhetherADeviceIsWokenForAFinishedTurn(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	pairedDevice(t, db, "phone")
	pairedDevice(t, db, "ipad")

	if on, err := db.DeviceTurn(ctx, "phone"); err != nil || on {
		t.Fatalf("a new device = (%v, %v), want off", on, err)
	}
	if err := db.SetDeviceTurn(ctx, "phone", true); err != nil {
		t.Fatalf("SetDeviceTurn on: %v", err)
	}
	if on, _ := db.DeviceTurn(ctx, "phone"); !on {
		t.Error("the phone reads off after it was turned on")
	}
	if on, _ := db.DeviceTurn(ctx, "ipad"); on {
		t.Error("turning the phone on turned the ipad on")
	}
	if err := db.SetDeviceTurn(ctx, "phone", false); err != nil {
		t.Fatalf("SetDeviceTurn off: %v", err)
	}
	if on, _ := db.DeviceTurn(ctx, "phone"); on {
		t.Error("the phone reads on after it was turned off")
	}
	if on, err := db.DeviceTurn(ctx, "ghost"); err != nil || on {
		t.Errorf("a device that is not there = (%v, %v), want off", on, err)
	}
}
