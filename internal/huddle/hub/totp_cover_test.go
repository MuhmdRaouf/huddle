package hub

// TOTP storage against its own database: the lockout's window ages and locks on a fresh fifth
// failure, the removal drops the enrolment whole while a passkey's device and sessions ride on,
// the reads answer the row's own fields, and a statement that cannot run names its failure.

import (
	"context"
	"testing"
)

// totpCoverNow is one moment the lockout fixtures count their windows from.
const totpCoverNow = int64(1_800_000_000_000)

func TestTOTPFailAgesItsWindowAndLocksOnAFreshFifth(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", nil, totpCoverNow, "owner"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	now := totpCoverNow + 1000
	fail := func() bool {
		t.Helper()
		locked, err := db.TOTPFail(ctx, now, 60_000, 900_000)
		if err != nil {
			t.Fatalf("record the failure at %d: %v", now, err)
		}
		return locked
	}

	for i := 0; i < 4; i++ {
		if fail() {
			t.Fatalf("failure %d locked the window early", i+1)
		}
	}
	state, ok, err := db.TOTP(ctx)
	if err != nil || !ok {
		t.Fatalf("read the counters: %v (ok %t)", err, ok)
	}
	if state.Fails != 4 || state.WindowStart != now {
		t.Fatalf("the counters = fails %d window %d, want four inside one window", state.Fails, state.WindowStart)
	}

	// the window ages out on its own boundary: a failure a full window after the first starts a
	// fresh count of one, not a fifth that locks
	now += 60_000
	if fail() {
		t.Fatal("the failure that aged the window out locked it")
	}
	state, _, _ = db.TOTP(ctx)
	if state.Fails != 1 || state.WindowStart != now {
		t.Fatalf("after the window aged = fails %d window %d, want a fresh count of one", state.Fails, state.WindowStart)
	}

	for i := 0; i < 3; i++ {
		if fail() {
			t.Fatalf("failure %d of the fresh window locked early", i+2)
		}
	}
	now += 1000
	if !fail() {
		t.Fatal("the fifth failure inside the fresh window did not lock")
	}
	state, _, _ = db.TOTP(ctx)
	if state.Fails != 0 || state.WindowStart != 0 || state.LockedUntil != now+900_000 {
		t.Fatalf("the lock = fails %d window %d until %d, want cleared counters and the fifteen-minute end",
			state.Fails, state.WindowStart, state.LockedUntil)
	}

	// every refusal left its audit row
	rows, err := db.Audit(ctx, AuditFilter{Limit: 100})
	if err != nil {
		t.Fatalf("read the audit rows: %v", err)
	}
	failed := 0
	for _, row := range rows {
		if row.Action == "totp.failed" {
			failed++
		}
	}
	if failed != 9 {
		t.Fatalf("the audit holds %d totp.failed rows, want one per refusal (9)", failed)
	}
}

func TestTOTPFailWithoutAnEnrolmentCountsNothing(t *testing.T) {
	db := openForTest(t)
	if locked, err := db.TOTPFail(context.Background(), totpCoverNow, 60_000, 900_000); err != nil || locked {
		t.Fatalf("a failure against no enrolment = locked %t err %v, want nothing counted", locked, err)
	}
}

func TestTOTPRemoveDropsTheEnrolmentWholeAndSparesThePasskeys(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", []string{"abcd-efgh"}, totpCoverNow, "owner"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if err := db.TOTPSignInCode(ctx, 10, "Mac", []byte("h-mac"), "code-session", totpCoverNow+900_000, totpCoverNow); err != nil {
		t.Fatalf("sign in by code: %v", err)
	}
	if err := db.st.Run(ctx,
		"INSERT INTO devices (name, handle, credential, added, seen) VALUES ('iPhone', ?, ?, ?, ?)",
		[]byte("h-iphone"), []byte(`{"type":"public-key","id":"cred"}`), totpCoverNow, totpCoverNow); err != nil {
		t.Fatalf("seed the passkey device: %v", err)
	}
	if err := db.st.Run(ctx,
		"INSERT INTO device_sessions (digest, device, expires, stepup_at, renewed, method) "+
			"VALUES ('pk-session', 'iPhone', ?, ?, ?, 'passkey')",
		totpCoverNow+900_000, totpCoverNow, totpCoverNow); err != nil {
		t.Fatalf("seed the passkey session: %v", err)
	}

	removed, err := db.TOTPRemove(ctx, totpCoverNow+2000, "owner")
	if err != nil || !removed {
		t.Fatalf("remove the enrolment: %v (removed %t)", err, removed)
	}
	if _, ok, err := db.TOTP(ctx); err != nil || ok {
		t.Errorf("the enrolment row survived: ok %t err %v", ok, err)
	}
	if digests, err := db.TOTPRecovery(ctx); err != nil || len(digests) != 0 {
		t.Errorf("the recovery rows survived: %d (err %v)", len(digests), err)
	}
	if _, live, _ := db.DeviceSession(ctx, "code-session", totpCoverNow+2000); live {
		t.Error("a code-started session outlived the removal")
	}
	if _, live, _ := db.DeviceSession(ctx, "pk-session", totpCoverNow+2000); !live {
		t.Error("the removal ended a passkey session")
	}
	if !db.DeviceHasPasskey(ctx, "iPhone") {
		t.Error("the removal dropped the passkey's device row")
	}
	if db.DeviceHasPasskey(ctx, "Mac") {
		t.Error("a code-born device row outlived the removal")
	}
	rows, err := db.Audit(ctx, AuditFilter{Limit: 100})
	if err != nil {
		t.Fatalf("read the audit rows: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.Action == "totp.remove" && row.Actor == "owner" {
			found = true
		}
	}
	if !found {
		t.Error("no audit row names the removal")
	}

	// a second removal answers that there is nothing to remove
	if removed, err := db.TOTPRemove(ctx, totpCoverNow+3000, "owner"); err != nil || removed {
		t.Errorf("the second removal = %t (err %v), want false", removed, err)
	}
}

func TestDeviceHasPasskeyNamesTheRowsACodeCouldNotTouch(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if err := db.st.Run(ctx,
		"INSERT INTO devices (name, handle, credential, added, seen) VALUES (?, ?, ?, ?, ?), (?, ?, ?, ?, ?)",
		"Mac", []byte("h1"), noCredential, totpCoverNow, totpCoverNow,
		"iPhone", []byte("h2"), []byte(`{"type":"public-key","id":"cred"}`), totpCoverNow, totpCoverNow); err != nil {
		t.Fatalf("seed the devices: %v", err)
	}
	if db.DeviceHasPasskey(ctx, "Mac") {
		t.Error("a code-born row reads as holding a passkey")
	}
	if !db.DeviceHasPasskey(ctx, "iPhone") {
		t.Error("a paired row reads as holding no passkey")
	}
	if db.DeviceHasPasskey(ctx, "ghost") {
		t.Error("a name no row holds reads as a passkey")
	}
	dropTable(t, db, "devices")
	if db.DeviceHasPasskey(ctx, "iPhone") {
		t.Error("a broken devices table reads as a passkey")
	}
}

func TestTOTPReadsAnswerTheRowTheEnrolmentHolds(t *testing.T) {
	db := openForTest(t)
	ctx := context.Background()
	if _, ok, err := db.TOTP(ctx); err != nil || ok {
		t.Fatalf("the empty hub reads enrolled: ok %t err %v", ok, err)
	}
	if digests, err := db.TOTPRecovery(ctx); err != nil || len(digests) != 0 {
		t.Fatalf("the empty hub holds recovery rows: %d (err %v)", len(digests), err)
	}
	if err := db.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", []string{"bbbb-dddd", "aaaa-cccc"}, totpCoverNow, "owner"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	state, ok, err := db.TOTP(ctx)
	if err != nil || !ok || state.Secret != "JBSWY3DPEHPK3PXP" || state.LastStep != 0 ||
		state.Fails != 0 || state.WindowStart != 0 || state.LockedUntil != 0 {
		t.Fatalf("the enrolment row = %+v (ok %t err %v), want the secret over cleared counters", state, ok, err)
	}
	digests, err := db.TOTPRecovery(ctx)
	if err != nil {
		t.Fatalf("read the recovery rows: %v", err)
	}
	want := []string{TOTPDigest("aaaa-cccc"), TOTPDigest("bbbb-dddd")}
	if len(digests) != 2 || digests[0] != want[0] || digests[1] != want[1] {
		t.Fatalf("the recovery rows = %v, want the digests in a stable order %v", digests, want)
	}

	// a second enrolment replaces the first whole
	if err := db.TOTPEnrol(ctx, "KRSXG5CTMVRXEZLU", []string{"zzzz-yyyy"}, totpCoverNow, "owner"); err != nil {
		t.Fatalf("re-enrol: %v", err)
	}
	if state, _, _ := db.TOTP(ctx); state.Secret != "KRSXG5CTMVRXEZLU" {
		t.Errorf("the second enrolment left the first secret %q", state.Secret)
	}
	if digests, _ := db.TOTPRecovery(ctx); len(digests) != 1 || digests[0] != TOTPDigest("zzzz-yyyy") {
		t.Errorf("the second enrolment left the first recovery rows: %v", digests)
	}
}

func TestTOTPDigestSpellsACodeOneWay(t *testing.T) {
	want := TOTPDigest("abcd-efgh")
	for _, spelling := range []string{"ABCD-EFGH", "abcd efgh", "abcdefgh", "abcd-\tefgh", " abcd-efgh "} {
		if got := TOTPDigest(spelling); got != want {
			t.Errorf("the spelling %q digests %s, want the canonical %s", spelling, got, want)
		}
	}
	if TOTPDigest("abcd-efgi") == want {
		t.Error("a different code digests to the same form")
	}
	if got := (CodeSpentError{}).Error(); got != "the code is already spent" {
		t.Errorf("the spent error says %q", got)
	}
}

func TestTOTPStatementsNameAFailedTable(t *testing.T) {
	ctx := context.Background()

	bare := dbWithout(t, "totp")
	if _, _, err := bare.TOTP(ctx); err == nil {
		t.Error("a read from a broken totp table succeeded")
	}
	if err := bare.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", nil, 0, "owner"); err == nil {
		t.Error("an enrolment on a broken totp table succeeded")
	}
	if err := bare.TOTPSignInCode(ctx, 1, "Mac", []byte("h"), "s", 0, 0); err == nil {
		t.Error("a code sign-in on a broken totp table succeeded")
	}
	if _, err := bare.TOTPFail(ctx, 0, 0, 0); err == nil {
		t.Error("a failure counted on a broken totp table succeeded")
	}
	if _, err := bare.TOTPRemove(ctx, 0, "owner"); err == nil {
		t.Error("a removal on a broken totp table succeeded")
	}

	rec := dbWithout(t, "totp_recovery")
	if _, err := rec.TOTPRecovery(ctx); err == nil {
		t.Error("a read from a broken totp_recovery table succeeded")
	}
	if err := rec.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", []string{"abcd-efgh"}, 0, "owner"); err == nil {
		t.Error("an enrolment on a broken totp_recovery table succeeded")
	}
	if err := rec.TOTPSignInRecovery(ctx, "digest", "Mac", []byte("h"), "s", 0, 0); err == nil {
		t.Error("a recovery spend on a broken totp_recovery table succeeded")
	}

	// the removal's own sweep, one broken table at a time, over an enrolment that landed
	for _, table := range []string{"totp_recovery", "devices", "device_sessions"} {
		db := openForTest(t)
		if err := db.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", []string{"abcd-efgh"}, 0, "owner"); err != nil {
			t.Fatalf("enrol over %s: %v", table, err)
		}
		dropTable(t, db, table)
		if _, err := db.TOTPRemove(ctx, 1, "owner"); err == nil {
			t.Errorf("a removal over a broken %s table succeeded", table)
		}
	}

	dev := openForTest(t)
	if err := dev.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", nil, 0, "owner"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	dropTable(t, dev, "devices")
	if err := dev.TOTPSignInCode(ctx, 1, "Mac", []byte("h"), "s", 0, 0); err == nil {
		t.Error("a code sign-in onto a broken devices table succeeded")
	}

	sess := openForTest(t)
	if err := sess.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", nil, 0, "owner"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	dropTable(t, sess, "device_sessions")
	if err := sess.TOTPSignInCode(ctx, 1, "Mac", []byte("h"), "s", 0, 0); err == nil {
		t.Error("a session mint onto a broken device_sessions table succeeded")
	}

	aud := openForTest(t)
	if err := aud.TOTPEnrol(ctx, "JBSWY3DPEHPK3PXP", []string{"abcd-efgh"}, 0, "owner"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	dropTable(t, aud, "audit")
	if err := aud.TOTPSignInCode(ctx, 1, "Mac", []byte("h"), "s", 0, 0); err == nil {
		t.Error("a sign-in without its audit row succeeded")
	}
	if _, err := aud.TOTPFail(ctx, 1, 2, 3); err == nil {
		t.Error("a counted failure without its audit row succeeded")
	}
	if _, err := aud.TOTPRemove(ctx, 1, "owner"); err == nil {
		t.Error("a removal without its audit row succeeded")
	}
}
