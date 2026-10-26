package hub

// Signing out at the storage layer: one session digest ends alone, all of them end together, and
// the device itself — its passkey credential and any TOTP enrolment — survives both.

import (
	"context"
	"testing"
	"time"
)

// pairTwoSessions pairs phone once and signs it in again, so two live sessions of one device
// exist: session-a from the pairing, session-b from the sign-in.
func pairTwoSessions(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	savePairing(t, db, "digest-pair", pairNow.Add(10*time.Minute))
	if err := db.PairFinish(ctx, "digest-pair", pairDevice("phone"), "session-a",
		pairNow.Add(time.Hour).UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	if err := db.LoginFinish(ctx, pairDevice("phone"), "session-b",
		pairNow.Add(time.Hour).UnixMilli(), pairNow.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatalf("LoginFinish: %v", err)
	}
}

// sessionLive says whether a session digest answers live.
func sessionLive(t *testing.T, db *DB, digest string) bool {
	t.Helper()
	_, live, err := db.DeviceSession(context.Background(), digest, pairNow.Add(2*time.Minute).UnixMilli())
	if err != nil {
		t.Fatalf("DeviceSession(%s): %v", digest, err)
	}
	return live
}

func TestSessionRemoveEndsOneSessionOfADevice(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	pairTwoSessions(t, db)

	removed, err := db.SessionRemove(ctx, "session-a")
	if err != nil || !removed {
		t.Fatalf("SessionRemove = (%v, %v), want the session gone", removed, err)
	}
	if sessionLive(t, db, "session-a") {
		t.Error("the removed session still answers live")
	}
	if !sessionLive(t, db, "session-b") {
		t.Error("the device's other session died with the one removed")
	}
	if listed, err := db.Devices(ctx); err != nil || len(listed) != 1 || listed[0].Name != "phone" {
		t.Errorf("devices = %+v (%v), want phone still paired", listed, err)
	}

	// a digest with no session, and one already ended, answer false and write nothing
	if removed, err := db.SessionRemove(ctx, "session-a"); err != nil || removed {
		t.Errorf("SessionRemove of an ended session = (%v, %v), want false", removed, err)
	}
	if removed, err := db.SessionRemove(ctx, "no-session"); err != nil || removed {
		t.Errorf("SessionRemove of an unknown digest = (%v, %v), want false", removed, err)
	}
	if !sessionLive(t, db, "session-b") {
		t.Error("an empty removal took the surviving session with it")
	}
}

func TestSessionsRemoveAllEndsEverySessionAndKeepsTheDevice(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	pairTwoSessions(t, db)
	// a second device's session and an enrolment the sweep must leave alone
	savePairing(t, db, "digest-pad", pairNow.Add(10*time.Minute))
	if err := db.PairFinish(ctx, "digest-pad", pairDevice("pad"), "session-pad",
		pairNow.Add(time.Hour).UnixMilli(), pairNow.UnixMilli()); err != nil {
		t.Fatalf("PairFinish: %v", err)
	}
	if err := db.st.Run(ctx,
		"INSERT INTO totp (secret, last_step, fails, window_start, locked_until) VALUES ('secret', 0, 0, 0, 0)"); err != nil {
		t.Fatalf("enrol totp: %v", err)
	}

	gone, err := db.SessionsRemoveAll(ctx)
	if err != nil || gone != 3 {
		t.Fatalf("SessionsRemoveAll = (%d, %v), want 3", gone, err)
	}
	for _, digest := range []string{"session-a", "session-b", "session-pad"} {
		if sessionLive(t, db, digest) {
			t.Errorf("session %s survived the sweep", digest)
		}
	}
	if listed, err := db.Devices(ctx); err != nil || len(listed) != 2 {
		t.Fatalf("devices = %+v (%v), want both still paired", listed, err)
	}
	for _, device := range []string{"phone", "pad"} {
		if _, ok, _ := db.DeviceByName(ctx, device); !ok {
			t.Errorf("device %s is missing from the listing", device)
		}
	}
	row, err := db.st.Get(ctx, "SELECT id FROM totp WHERE id = 1")
	if err != nil || row == nil {
		t.Errorf("the TOTP enrolment = (%v, %v), want it kept", row, err)
	}
	// an empty store sweeps nothing
	if gone, err := db.SessionsRemoveAll(ctx); err != nil || gone != 0 {
		t.Errorf("a second sweep = (%d, %v), want 0", gone, err)
	}
}
