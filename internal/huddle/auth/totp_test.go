package auth

// Authenticator-app sign-in (RFC 6238): one enrolment per hub, codes spent once, ±1 step of
// clock skew, one-time recovery codes kept as digests, and a lockout that answers five failed
// codes in five minutes with fifteen quiet minutes. The store is the real hub database over a
// temp file and the codes come from a hand-rolled RFC 6238 in this file, so the core is proven
// against the specification rather than against the library it uses.

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/go-webauthn/webauthn/protocol"
)

// rfc6238 answers the code the specification draws for one secret at one step: HMAC-SHA1,
// dynamic truncation, six digits.
func rfc6238(t *testing.T, secret string, step int64) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		t.Fatalf("decode the secret: %v", err)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	at := int(sum[len(sum)-1] & 0x0f)
	code := (uint32(sum[at])&0x7f)<<24 | uint32(sum[at+1])<<16 | uint32(sum[at+2])<<8 | uint32(sum[at+3])
	return fmt.Sprintf("%06d", code%1_000_000)
}

// totpHarness is one TOTP core (and the pairing core that resolves the sessions it mints) over
// a fresh hub database, with an injectable clock.
type totpHarness struct {
	t    *TOTP
	p    *Pairing
	db   *hub.DB
	file string
	add  func(sec int64)
	step func() int64
}

// newTOTPHarness wires the cores the way the server does: one hub database, the loopback
// relying party, the machine name the otpauth URI carries.
func newTOTPHarness(t *testing.T) *totpHarness {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "hub.db")
	db, err := hub.Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open hub.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now, add := clockAt(1_700_000_000_000)
	p, err := NewPairing(db, PairOptions{
		RPID: "localhost", Origins: []string{"http://localhost:18860"}, Now: now, Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("new pairing: %v", err)
	}
	return &totpHarness{
		t:    NewTOTP(db, TOTPOptions{Machine: "book", Now: now, Log: func(string) {}}),
		p:    p,
		db:   db,
		file: file,
		add:  add,
		step: func() int64 { return now().Unix() / 30 },
	}
}

// pairHandle answers one device's WebAuthn user handle as the wire spells it.
func (h *totpHarness) pairHandle(t *testing.T, name string) string {
	t.Helper()
	rows, err := readRows(t, h.file, "SELECT handle FROM devices WHERE name = ?", name)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read the device: %v (%d rows)", err, len(rows))
	}
	handle, _ := rows[0]["handle"].([]byte)
	return b64(handle)
}

// enrol finishes one enrolment and answers the secret it enrolled and its recovery codes.
func (h *totpHarness) enrol(t *testing.T) (string, []string) {
	t.Helper()
	begun, err := h.t.BeginEnrol(context.Background())
	if err != nil {
		t.Fatalf("begin the enrolment: %v", err)
	}
	recovery, err := h.t.FinishEnrol(context.Background(), "owner", rfc6238(t, begun.Secret, h.step()))
	if err != nil {
		t.Fatalf("finish the enrolment: %v", err)
	}
	return begun.Secret, recovery
}

// dropTable removes one table from the harness's database, so a statement naming it fails the
// way a broken file does.
func (h *totpHarness) dropTable(t *testing.T, table string) {
	t.Helper()
	st, err := store.Open(context.Background(), h.file)
	if err != nil {
		t.Fatalf("open the harness database: %v", err)
	}
	defer st.Close()
	if err := st.Run(context.Background(), "DROP TABLE "+table); err != nil {
		t.Fatalf("drop %s: %v", table, err)
	}
}

// TestNewTOTPDefaultsItsClockAndLog proves the bare constructor stands on its own: no clock and
// no log wired still answers over the hub's database.
func TestNewTOTPDefaultsItsClockAndLog(t *testing.T) {
	h := newTOTPHarness(t)
	core := NewTOTP(h.db, TOTPOptions{Machine: "book"})
	if core.Enrolled(context.Background()) {
		t.Error("a bare core over an empty hub reads enrolled")
	}
}

// TestDeviceNameFromUASpellsThePlatform pins the word a code sign-in names its browser — the
// platform the user agent spells, with the phone named ahead of the Mac OS X it also carries.
func TestDeviceNameFromUASpellsThePlatform(t *testing.T) {
	for _, c := range []struct{ ua, want string }{
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X)", "iPhone"},
		{"Mozilla/5.0 (iPad; CPU OS 17_4 like Mac OS X)", "iPad"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8)", "Android"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", "Mac"},
		{"Mozilla/5.0 (Mac OS X 14_4)", "Mac"},
		{"Mozilla/5.0 (X11; Linux x86_64)", "Linux"},
		{"Mozilla/5.0", "Browser"},
		{"curl/8.6.0", "Browser"},
	} {
		if got := DeviceNameFromUA(c.ua); got != c.want {
			t.Errorf("DeviceNameFromUA(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}

// TestASpentCodeAnswersAsAWrongOne proves what the race's loser is told: a code another request
// spent first answers the wrong-code word and counts against the lockout, and any other failure
// passes through untouched.
func TestASpentCodeAnswersAsAWrongOne(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	h.enrol(t)

	passthrough := errors.New("the write went nowhere")
	if err := h.t.spentAsWrong(ctx, h.t.nowMS(), passthrough); !errors.Is(err, passthrough) {
		t.Fatalf("a plain failure came back as %v, want itself", err)
	}
	err := h.t.spentAsWrong(ctx, h.t.nowMS(), hub.CodeSpentError{})
	if statusOf(t, err) != 401 || !strings.Contains(err.Error(), TotpWrongMsg) {
		t.Fatalf("a spent code answered %v, want the wrong-code word", err)
	}

	// three more counted failures leave the count at four: the spend is the fifth, and it locks
	for i := 0; i < 3; i++ {
		_, _ = h.t.SignIn(ctx, "000000", "Mac")
	}
	if err := h.t.spentAsWrong(ctx, h.t.nowMS(), hub.CodeSpentError{}); statusOf(t, err) != 429 {
		t.Fatalf("the locking spend answered %v, want the lockout's 429", err)
	}
}

// TestAMatchWeighsOnlyWhatAnAppCouldHold proves the two refusals before any weighing: a secret
// no app could hold names no step, and a code of the wrong length is not weighed at all.
func TestAMatchWeighsOnlyWhatAnAppCouldHold(t *testing.T) {
	if _, ok := totpMatch("not a secret an app holds", 100, -1, "123456"); ok {
		t.Error("a secret no app could hold named a match")
	}
	if _, ok := totpMatch("JBSWY3DPEHPK3PXP", 100, -1, "12345"); ok {
		t.Error("a five-digit code matched")
	}
}

// TestABeginWhoseQRCannotBeDrawnAnswersItsFailure proves the one begin that cannot become a QR
// — a machine name longer than any code carries — is answered, not panicked on.
func TestABeginWhoseQRCannotBeDrawnAnswersItsFailure(t *testing.T) {
	h := newTOTPHarness(t)
	core := NewTOTP(h.db, TOTPOptions{Machine: strings.Repeat("book ", 1200)})
	if _, err := core.BeginEnrol(context.Background()); statusOf(t, err) != 500 {
		t.Fatalf("a begin whose QR cannot be drawn answered %v, want the 500", err)
	}
}

// TestTOTPCallsNameABrokenTable proves a statement that cannot run names its failure instead of
// answering half a sign-in: every table the codes touch, dropped one at a time.
func TestTOTPCallsNameABrokenTable(t *testing.T) {
	ctx := context.Background()

	enrolTable := newTOTPHarness(t)
	begun, err := enrolTable.t.BeginEnrol(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	enrolTable.dropTable(t, "totp")
	if _, err := enrolTable.t.SignIn(ctx, "123456", "Mac"); err == nil {
		t.Error("a sign-in over a broken enrolment table succeeded")
	}
	if _, err := enrolTable.t.Remove(ctx, "owner"); err == nil {
		t.Error("a removal over a broken enrolment table succeeded")
	}
	if _, err := enrolTable.t.FinishEnrol(ctx, "owner", rfc6238(t, begun.Secret, enrolTable.step())); err == nil {
		t.Error("a finish over a broken enrolment table succeeded")
	}

	recoveryTable := newTOTPHarness(t)
	recoveryTable.enrol(t)
	recoveryTable.dropTable(t, "totp_recovery")
	if _, err := recoveryTable.t.SignIn(ctx, "123456", "Mac"); err == nil {
		t.Error("a wrong code over a broken recovery table succeeded")
	}

	devicesTable := newTOTPHarness(t)
	secret, recovery := devicesTable.enrol(t)
	devicesTable.dropTable(t, "devices")
	if _, err := devicesTable.t.SignIn(ctx, rfc6238(t, secret, devicesTable.step()), "Mac"); err == nil {
		t.Error("a right code signed in over a broken devices table")
	}
	if _, err := devicesTable.t.SignIn(ctx, recovery[0], "Mac"); err == nil {
		t.Error("a recovery code signed in over a broken devices table")
	}

	auditTable := newTOTPHarness(t)
	auditTable.enrol(t)
	auditTable.dropTable(t, "audit")
	if _, err := auditTable.t.SignIn(ctx, "123456", "Mac"); err == nil {
		t.Error("a wrong code counted itself without its audit row")
	}
}

// TestTOTPEnrolStoresOnlyWhatTheSpecificationAllows proves the enrolment's two halves: a wrong
// code stores nothing at all, and the right code stores the secret with ten recovery codes that
// answer the shape and never repeat.
func TestTOTPEnrolStoresOnlyWhatTheSpecificationAllows(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()

	begun, err := h.t.BeginEnrol(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !strings.HasPrefix(begun.URI, "otpauth://totp/Huddle:book?") {
		t.Errorf("the uri %q is not the otpauth form for Huddle on book", begun.URI)
	}
	if !strings.Contains(begun.URI, "issuer=Huddle") || !strings.Contains(begun.URI, "secret="+begun.Secret) {
		t.Errorf("the uri %q misses its issuer or its secret", begun.URI)
	}
	if _, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(begun.Secret)); err != nil {
		t.Errorf("the secret %q is not base32: %v", begun.Secret, err)
	}
	if len(begun.QR) == 0 || len(begun.QR[0]) != len(begun.QR) {
		t.Errorf("the qr is no square: %d rows, first row %d wide", len(begun.QR), len(begun.QR[0]))
	}

	// a wrong code stores nothing: the home still reads un-enrolled
	if _, err := h.t.FinishEnrol(ctx, "owner", "000000"); err == nil {
		t.Fatal("a wrong code finished the enrolment")
	}
	if h.t.Enrolled(ctx) {
		t.Error("a wrong code left the hub enrolled")
	}

	// the right code enrols and answers ten recovery codes of xxxx-xxxx, base32 lowercase
	secret, recovery := h.enrol(t)
	if !h.t.Enrolled(ctx) {
		t.Fatal("the right code left the hub un-enrolled")
	}
	if len(recovery) != 10 {
		t.Fatalf("the enrolment answered %d recovery codes, want 10", len(recovery))
	}
	shape := regexp.MustCompile(`^[a-z2-7]{4}-[a-z2-7]{4}$`)
	seen := map[string]bool{}
	for _, code := range recovery {
		if !shape.MatchString(code) {
			t.Errorf("the recovery code %q is not xxxx-xxxx of lowercase base32", code)
		}
		if seen[code] {
			t.Errorf("the recovery code %q appears twice", code)
		}
		seen[code] = true
	}
	if secret == "" {
		t.Error("the enrolment answered no secret")
	}

	// the digests are what the database holds: no recovery code and no code anywhere in it
	row, ok, err := h.db.TOTP(ctx)
	if err != nil || !ok {
		t.Fatalf("read the enrolment row: %v (ok %t)", err, ok)
	}
	if row.Secret != secret {
		t.Errorf("the stored secret is the enrolment's own")
	}
	digests, err := h.db.TOTPRecovery(ctx)
	if err != nil {
		t.Fatalf("read the recovery digests: %v", err)
	}
	if len(digests) != 10 {
		t.Fatalf("the database holds %d recovery rows, want 10", len(digests))
	}
	for _, code := range recovery {
		for _, digest := range digests {
			if strings.Contains(digest, code) {
				t.Errorf("a recovery digest carries its code")
			}
		}
	}
}

// TestTOTPSignInSpendsEachCodeOnce proves the sign-in's core rule: a code mints one device
// session exactly once, the same code the second time is refused, one step of skew either way
// still signs in, and two steps away does not.
func TestTOTPSignInSpendsEachCodeOnce(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	secret, _ := h.enrol(t)

	signInWith := func(code string) error {
		_, err := h.t.SignIn(ctx, code, "Mac")
		return err
	}

	if err := signInWith(rfc6238(t, secret, h.step())); err != nil {
		t.Fatalf("a valid code did not sign in: %v", err)
	}
	if err := signInWith(rfc6238(t, secret, h.step())); err == nil {
		t.Fatal("the same code signed in a second time")
	}
	if statusOf(t, signInWith(rfc6238(t, secret, h.step()))) != 401 {
		t.Error("a spent code's refusal is not the plain 401")
	}

	// one step of skew either way signs in; two steps away does not
	h.add(90)
	fresh := h.step()
	if err := signInWith(rfc6238(t, secret, fresh-1)); err != nil {
		t.Fatalf("a code one step behind did not sign in: %v", err)
	}
	if err := signInWith(rfc6238(t, secret, fresh+1)); err != nil {
		t.Fatalf("a code one step ahead did not sign in: %v", err)
	}
	if err := signInWith(rfc6238(t, secret, fresh+2)); err == nil {
		t.Fatal("a code two steps away signed in")
	}

	// the codes of the steps the skew already covered are spent for good
	if err := signInWith(rfc6238(t, secret, fresh)); err == nil {
		t.Fatal("a code the accepted step already covered signed in again")
	}
	h.add(60)
	if err := signInWith(rfc6238(t, secret, h.step())); err != nil {
		t.Fatalf("the fresh code did not sign in: %v", err)
	}
}

// TestTOTPSignInMintsADeviceSession proves the session a code earns is a device session like a
// passkey's: named for the browser, resolvable by the pairing core, and listed with the devices.
func TestTOTPSignInMintsADeviceSession(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	secret, _ := h.enrol(t)

	session, err := h.t.SignIn(ctx, rfc6238(t, secret, h.step()), "Mac")
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	if session.Device != "Mac" {
		t.Errorf("the session's device = %q, want Mac", session.Device)
	}
	if session.ExpiresIn != PairSessionMS/1000 {
		t.Errorf("the session lasts %d s, want the seven days of every device session", session.ExpiresIn)
	}
	who, ok := h.p.Session(ctx, session.Cookie)
	if !ok || who.Device != "Mac" || !who.Root {
		t.Fatalf("the cookie resolves to %+v (ok %t), want the owner on device Mac", who, ok)
	}
	devices, err := h.p.Devices(ctx)
	if err != nil {
		t.Fatalf("list the devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "Mac" {
		t.Fatalf("the devices are %+v, want Mac alone", devices)
	}

	// a second sign-in from the same browser reuses the row: one Mac, two sessions
	if _, err := h.t.SignIn(ctx, rfc6238(t, secret, h.step()+1), "Mac"); err != nil {
		t.Fatalf("sign in again: %v", err)
	}
	devices, err = h.p.Devices(ctx)
	if err != nil {
		t.Fatalf("list the devices again: %v", err)
	}
	if len(devices) != 1 {
		t.Errorf("the second sign-in grew the list to %+v, want the one reused row", devices)
	}
}

// TestTOTPRecoveryCodesWorkOnce proves the fallback: each recovery code signs in exactly once,
// in either of its spellings, and a spent one is refused like a wrong code.
func TestTOTPRecoveryCodesWorkOnce(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	_, recovery := h.enrol(t)

	session, err := h.t.SignIn(ctx, recovery[0], "Mac")
	if err != nil {
		t.Fatalf("a recovery code did not sign in: %v", err)
	}
	if _, ok := h.p.Session(ctx, session.Cookie); !ok {
		t.Fatal("the recovery sign-in's cookie resolves to no session")
	}
	if err := func() error { _, err := h.t.SignIn(ctx, recovery[0], "Mac"); return err }(); err == nil {
		t.Fatal("a spent recovery code signed in again")
	}
	// the shape's other spellings work: upper case and the dash left out
	if _, err := h.t.SignIn(ctx, strings.ToUpper(recovery[1]), "Mac"); err != nil {
		t.Fatalf("an upper-case recovery code did not sign in: %v", err)
	}
	if _, err := h.t.SignIn(ctx, strings.ReplaceAll(recovery[2], "-", ""), "Mac"); err != nil {
		t.Fatalf("a dashless recovery code did not sign in: %v", err)
	}
}

// TestTOTPFiveFailuresLockFifteenMinutes proves the lockout with the fake clock: five wrong
// codes inside five minutes lock code sign-in for fifteen, the lock refuses even the right
// code, and once it lifts a valid code signs in again.
func TestTOTPFiveFailuresLockFifteenMinutes(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	secret, _ := h.enrol(t)

	for i := 0; i < 4; i++ {
		if _, err := h.t.SignIn(ctx, "000000", "Mac"); err == nil {
			t.Fatalf("wrong code %d signed in", i)
		}
	}
	// four failures inside the window: a valid code still works and clears the count
	h.add(30)
	if _, err := h.t.SignIn(ctx, rfc6238(t, secret, h.step()), "Mac"); err != nil {
		t.Fatalf("a valid code after four failures was refused: %v", err)
	}

	// five failures inside one window lock the fifteenth minute in
	for i := 0; i < 4; i++ {
		_, _ = h.t.SignIn(ctx, "000000", "Mac")
	}
	_, err := h.t.SignIn(ctx, "000000", "Mac")
	if err == nil {
		t.Fatal("the fifth failure signed in")
	}
	if statusOf(t, err) != 429 {
		t.Fatalf("the fifth failure's answer is status %d, want the lockout's 429", statusOf(t, err))
	}

	// the lock refuses even the right code, with the wait-a-while word
	_, err = h.t.SignIn(ctx, rfc6238(t, secret, h.step()), "Mac")
	if err == nil {
		t.Fatal("a valid code signed in while locked")
	}
	if statusOf(t, err) != 429 || !strings.Contains(err.Error(), "Too many tries") {
		t.Fatalf("the lock's answer = %v, want the too-many-tries word", err)
	}

	// a wrong code while locked does not grow the lock
	h.add(60)
	_, _ = h.t.SignIn(ctx, "000000", "Mac")

	// fifteen minutes later the lock has lifted
	h.add(15 * 60)
	if _, err := h.t.SignIn(ctx, rfc6238(t, secret, h.step()), "Mac"); err != nil {
		t.Fatalf("a valid code after the lock lifted was refused: %v", err)
	}
}

// TestTOTPRemovalEndsItsSessions proves the off switch: the removal drops the enrolment, every
// recovery code and every session a code started, and the device rows the codes created with
// them — while a passkey's device and its sessions stay.
func TestTOTPRemovalEndsItsSessions(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	secret, _ := h.enrol(t)

	// a passkey device beside the code-born one, with its own session
	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint the pairing link: %v", err)
	}
	key := newSoftToken(t)
	creation, err := h.p.BeginPair(ctx, invite.Token, "iPhone")
	if err != nil {
		t.Fatalf("begin the pairing: %v", err)
	}
	body := key.create(t, creationOf(t, creation), "http://localhost:18860", "")
	paired, err := h.p.FinishPair(ctx, invite.Token, body)
	if err != nil {
		t.Fatalf("finish the pairing: %v", err)
	}

	byCode, err := h.t.SignIn(ctx, rfc6238(t, secret, h.step()), "Mac")
	if err != nil {
		t.Fatalf("sign in by code: %v", err)
	}

	removed, err := h.t.Remove(ctx, "owner")
	if err != nil || !removed {
		t.Fatalf("remove the enrolment: %v (removed %t)", err, removed)
	}
	if h.t.Enrolled(ctx) {
		t.Error("the hub still reads enrolled after the removal")
	}
	if _, ok := h.p.Session(ctx, byCode.Cookie); ok {
		t.Error("a code-started session outlived the removal")
	}
	if _, ok := h.p.Session(ctx, paired.Cookie); !ok {
		t.Error("the removal ended the passkey device's session too")
	}
	devices, err := h.p.Devices(ctx)
	if err != nil {
		t.Fatalf("list the devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "iPhone" {
		t.Errorf("the devices after the removal are %+v, want the passkey's alone", devices)
	}
	if digests, err := h.db.TOTPRecovery(ctx); err != nil || len(digests) != 0 {
		t.Errorf("the recovery rows survived the removal: %d (err %v)", len(digests), err)
	}

	// a second removal answers that there is nothing to remove
	if removed, err := h.t.Remove(ctx, "owner"); err != nil || removed {
		t.Fatalf("the second removal = %t (err %v), want false", removed, err)
	}

	// with nothing enrolled, every code is just wrong
	if _, err := h.t.SignIn(ctx, rfc6238(t, secret, h.step()), "Mac"); err == nil {
		t.Fatal("a code signed in against no enrolment")
	}
}

// TestSessionPasskeyLandsOnTheCodeDevice proves the sheet a code sign-in offers: a signed-in
// session registers a platform passkey on its own device row — no pairing link, the name and
// the handle kept — and the row that could only sign in by code can now sign in by touch.
func TestSessionPasskeyLandsOnTheCodeDevice(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	secret, _ := h.enrol(t)

	session, err := h.t.SignIn(ctx, rfc6238(t, secret, h.step()), "Mac")
	if err != nil {
		t.Fatalf("sign in by code: %v", err)
	}
	handleBefore := h.pairHandle(t, "Mac")
	if h.db.DeviceHasPasskey(ctx, "Mac") {
		t.Fatal("a code-born device row already reads as holding a passkey")
	}

	key := newSoftToken(t)
	creation, err := h.p.BeginSessionPasskey(ctx, "Mac")
	if err != nil {
		t.Fatalf("begin the session passkey: %v", err)
	}
	sel := creation.Response.AuthenticatorSelection
	if sel.AuthenticatorAttachment != protocol.Platform {
		t.Errorf("the attachment = %q, want the device's own platform authenticator", sel.AuthenticatorAttachment)
	}
	if sel.ResidentKey != protocol.ResidentKeyRequirementRequired {
		t.Errorf("the resident key = %q, want required", sel.ResidentKey)
	}
	if sel.UserVerification != protocol.VerificationRequired {
		t.Errorf("the user verification = %q, want required", sel.UserVerification)
	}
	o := creationOf(t, creation)
	if o.userHandleB64 != handleBefore {
		t.Errorf("the ceremony enrols user handle %s, want the device's own %s", o.userHandleB64, handleBefore)
	}

	if _, err := h.p.FinishSessionPasskey(ctx, "Mac",
		key.create(t, o, "http://localhost:18860", "")); err != nil {
		t.Fatalf("finish the session passkey: %v", err)
	}
	if !h.db.DeviceHasPasskey(ctx, "Mac") {
		t.Fatal("the device row holds no passkey after the finish")
	}
	if after := h.pairHandle(t, "Mac"); after != handleBefore {
		t.Errorf("the finish moved the device's handle: %s then %s", handleBefore, after)
	}
	if _, ok := h.p.Session(ctx, session.Cookie); !ok {
		t.Error("the finish ended the code session it ran in")
	}

	// the saved passkey signs in on its own: the resident key names its device
	assertion, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	if _, err := h.p.FinishLogin(ctx, key.get(t, wireText(t, assertion.Response.Challenge),
		"localhost", "http://localhost:18860", handleBefore, false)); err != nil {
		t.Fatalf("login finish with the saved passkey: %v", err)
	}

	// the audit row names the passkey landing on the device
	rows, err := readRows(t, h.file, "SELECT actor, action, target FROM audit ORDER BY id")
	if err != nil {
		t.Fatalf("read the audit rows: %v", err)
	}
	foundAdd := false
	for _, row := range rows {
		if row["action"] == "passkey.add" && row["target"] == "Mac" && row["actor"] == "Mac" {
			foundAdd = true
		}
	}
	if !foundAdd {
		t.Errorf("no audit row names the passkey landing on Mac: %v", rows)
	}

	// a session that names no device row begins nothing
	if _, err := h.p.BeginSessionPasskey(ctx, "ghost"); err == nil {
		t.Fatal("a begin for a device that does not exist succeeded")
	}
}

// TestTOTPFinishedEnrolmentExpires proves a begun enrolment nobody finishes is worth nothing
// five minutes later: the pending secret ages out and the finish refuses.
func TestTOTPFinishedEnrolmentExpires(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	begun, err := h.t.BeginEnrol(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	h.add(6 * 60)
	if _, err := h.t.FinishEnrol(ctx, "owner", rfc6238(t, begun.Secret, h.step())); err == nil {
		t.Fatal("a finish on an expired enrolment stored it")
	}
	if h.t.Enrolled(ctx) {
		t.Error("the expired enrolment landed anyway")
	}
}

// TestTOTPKeepsNoSecretOutOfItsRow proves the one place a secret lives is the database row:
// the log lines and the audit rows of a sign-in name no code, no secret and no recovery code.
func TestTOTPKeepsNoSecretOutOfItsRow(t *testing.T) {
	h := newTOTPHarness(t)
	ctx := context.Background()
	secret, recovery := h.enrol(t)

	logs := []string{}
	now, _ := clockAt(1_700_000_000_000)
	core := NewTOTP(h.db, TOTPOptions{Machine: "book", Now: now, Log: func(line string) { logs = append(logs, line) }})
	_, err := core.SignIn(ctx, "123456", "Mac")
	if err == nil {
		t.Fatal("a wrong code signed in")
	}
	for _, line := range logs {
		for _, word := range []string{"123456", secret} {
			if strings.Contains(line, word) {
				t.Errorf("a log line carries the code or the secret: %q", line)
			}
		}
	}
	rows, err := h.db.Audit(ctx, hub.AuditFilter{Limit: 500})
	if err != nil {
		t.Fatalf("read the audit rows: %v", err)
	}
	for _, code := range recovery[:3] {
		if _, err := core.SignIn(ctx, code, "Mac"); err != nil {
			t.Fatalf("a recovery code did not sign in: %v", err)
		}
	}
	for _, row := range rows {
		for _, word := range append([]string{"123456", secret}, recovery...) {
			if strings.Contains(row.Action, word) || strings.Contains(row.Target, word) ||
				strings.Contains(row.Actor, word) {
				t.Errorf("an audit row carries a secret: %+v", row)
			}
		}
	}
	if _, err := core.SignIn(ctx, recovery[0], "Mac"); err == nil {
		t.Fatal("a spent recovery code signed in again")
	}
}
