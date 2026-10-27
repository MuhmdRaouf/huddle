package auth

// Device pairing: one-use links of five minutes, WebAuthn passkeys with user
// verification required, device sessions that roll forward while their device uses them and end
// after seven days without a request. The store is the real hub database over a temp file and the
// browser is the software authenticator beside this file, so every refusal below is proven
// against the same checks the server runs.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/go-webauthn/webauthn/protocol"
)

// pairHarness is one pairing core over a fresh hub database, with an injectable clock.
type pairHarness struct {
	p    *Pairing
	db   *hub.DB
	file string
	auth *softToken
	add  func(sec int64)
}

// newPairHarness wires the pairing core the way the server does: the home's hub database, the
// loopback RP, one soft authenticator.
func newPairHarness(t *testing.T) *pairHarness {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "hub.db")
	db, err := hub.Open(context.Background(), file)
	if err != nil {
		t.Fatalf("open hub.db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now, add := newClock()
	p, err := NewPairing(db, PairOptions{
		RPID: "localhost", Origins: []string{"http://localhost:18860"}, Now: now, Log: func(string) {},
	})
	if err != nil {
		t.Fatalf("new pairing: %v", err)
	}
	return &pairHarness{p: p, db: db, file: file, auth: newSoftToken(t), add: add}
}

// pair runs one whole pairing: mint, begin, create, finish.
func (h *pairHarness) pair(t *testing.T, name, origin string) (PairInvite, PairSession) {
	t.Helper()
	invite, err := h.p.Mint(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	creation, err := h.p.BeginPair(context.Background(), invite.Token, name)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	body := h.auth.create(t, creationOf(t, creation), origin, "")
	session, err := h.p.FinishPair(context.Background(), invite.Token, body)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	return invite, session
}

// statusOf reads a pairing error's HTTP status, failing the test when there was none.
func statusOf(t *testing.T, err error) int {
	t.Helper()
	var he *channel.HuddleError
	if !errors.As(err, &he) {
		t.Fatalf("error %v is no HuddleError", err)
	}
	return he.Status
}

// TestPairFlow pairs a device, signs it in again, uses its cookie, and dies with its removal:
// the whole of the card's done-when in one line each.
func TestPairFlow(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()

	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !regexp.MustCompile(`^[A-Z2-7]{26}$`).MatchString(invite.Token) {
		t.Errorf("token %q is not 26 base32 characters", invite.Token)
	}
	if code := PairCode(invite.Token); !regexp.MustCompile(`^[A-Z2-7]{3}-[A-Z2-7]{3}$`).MatchString(code) {
		t.Errorf("pairing code %q is not XXX-XXX of the token's first six characters", code)
	}
	if code := PairCode(invite.Token); code != invite.Token[:3]+"-"+invite.Token[3:6] {
		t.Errorf("pairing code %q does not come from the token's first six characters", code)
	}
	if invite.ExpiresIn != 300 {
		t.Errorf("token expires in %d s, want 300", invite.ExpiresIn)
	}

	creation, err := h.p.BeginPair(ctx, invite.Token, "iPhone")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	o := creationOf(t, creation)
	if creation.Response.RelyingParty.ID != "localhost" {
		t.Errorf("RP ID = %q, want localhost", creation.Response.RelyingParty.ID)
	}
	if creation.Response.RelyingParty.Name != "Huddle" {
		t.Errorf("RP name = %q, want Huddle", creation.Response.RelyingParty.Name)
	}
	sel := creation.Response.AuthenticatorSelection
	if sel.UserVerification != protocol.VerificationRequired {
		t.Errorf("user verification = %q, want required", sel.UserVerification)
	}
	if sel.ResidentKey != protocol.ResidentKeyRequirementPreferred {
		t.Errorf("resident key = %q, want preferred", sel.ResidentKey)
	}

	session, err := h.p.FinishPair(ctx, invite.Token, h.auth.create(t, o, "http://localhost:18860", ""))
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if session.Device != "iPhone" {
		t.Errorf("paired device = %q, want iPhone", session.Device)
	}
	if session.ExpiresIn != 7*24*60*60 {
		t.Errorf("session lasts %d s, want 604800", session.ExpiresIn)
	}
	if !strings.HasPrefix(session.Cookie, "hdev_") {
		t.Errorf("cookie %q carries no hdev_ mark", session.Cookie)
	}

	who, ok := h.p.Session(ctx, session.Cookie)
	if !ok || who.Name != "owner" || !who.Root || who.Device != "iPhone" {
		t.Fatalf("session resolves to %+v (ok %t), want the owner on device iPhone", who, ok)
	}

	// a later sign-in with the same passkey mints a fresh session
	assertion, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	challenge := wireText(t, assertion.Response.Challenge)
	if len(assertion.Response.AllowedCredentials) != 0 {
		t.Error("login offers an allow list; a resident key needs none (the device names itself)")
	}
	second, err := h.p.FinishLogin(ctx, h.auth.get(t, challenge, "localhost", "http://localhost:18860", o.userHandleB64, false))
	if err != nil {
		t.Fatalf("login finish: %v", err)
	}
	if second.Device != "iPhone" || second.Cookie == session.Cookie {
		t.Fatalf("second session = %+v, want a fresh cookie on iPhone", second)
	}
	if who, ok := h.p.Session(ctx, second.Cookie); !ok || who.Device != "iPhone" {
		t.Fatalf("second session resolves to %+v (ok %t)", who, ok)
	}

	devices, err := h.p.Devices(ctx)
	if err != nil {
		t.Fatalf("list devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "iPhone" {
		t.Fatalf("devices = %+v, want one iPhone", devices)
	}
	if devices[0].Added == "" || devices[0].Seen == "" {
		t.Errorf("device row = %+v, want an added and a seen moment", devices[0])
	}

	// removal kills both sessions at once: the old cookie answers nobody
	if !h.p.Remove(ctx, "iPhone") {
		t.Fatal("remove iPhone answered false")
	}
	if _, ok := h.p.Session(ctx, session.Cookie); ok {
		t.Error("the paired session outlived its device's removal")
	}
	if _, ok := h.p.Session(ctx, second.Cookie); ok {
		t.Error("the signed-in session outlived its device's removal")
	}
	if h.p.Remove(ctx, "iPhone") {
		t.Error("removing iPhone twice answered true")
	}
}

// TestPairWritesAuditAndAlert proves the pairing and the sign-in leave their audit rows and the
// pairing raises the alert event push will send.
func TestPairWritesAuditAndAlert(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	h.pair(t, "iPad", "http://localhost:18860")

	// one later sign-in, so its audit row lands beside the pairing's
	assertion, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	if _, err := h.p.FinishLogin(ctx, h.auth.get(t, wireText(t, assertion.Response.Challenge),
		"localhost", "http://localhost:18860", h.pairHandle(t, "iPad"), false)); err != nil {
		t.Fatalf("login finish: %v", err)
	}

	rows, err := readRows(t, h.file, "SELECT actor, action, target FROM audit ORDER BY id")
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	want := [][3]string{{"iPad", "pair", "iPad"}, {"iPad", "signin", "iPad"}}
	if len(rows) != len(want) {
		t.Fatalf("audit rows = %v, want %v", rows, want)
	}
	for i, w := range want {
		got := [3]string{rows[i]["actor"].(string), rows[i]["action"].(string), rows[i]["target"].(string)}
		if got != w {
			t.Errorf("audit row %d = %v, want %v", i, got, w)
		}
	}

	alerts, err := readRows(t, h.file, "SELECT kind, json FROM alerts")
	if err != nil {
		t.Fatalf("read alerts: %v", err)
	}
	if len(alerts) != 1 || alerts[0]["kind"] != "alert.device_paired" {
		t.Fatalf("alerts = %v, want one alert.device_paired", alerts)
	}
	if body, _ := alerts[0]["json"].(string); !strings.Contains(body, `"iPad"`) {
		t.Errorf("alert body %q does not name the device", body)
	}
}

// TestPairTokenIsOneUse refuses a second registration off the same link, however it comes: a
// fresh begin, or a replay of the very body that succeeded.
func TestPairTokenIsOneUse(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	creation, err := h.p.BeginPair(ctx, invite.Token, "iPhone")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	body := h.auth.create(t, creationOf(t, creation), "http://localhost:18860", "")
	if _, err := h.p.FinishPair(ctx, invite.Token, body); err != nil {
		t.Fatalf("first finish: %v", err)
	}
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.BeginPair(ctx, invite.Token, " Pixel "); return err })); code != 401 {
		t.Errorf("begin on a used link = %d, want 401", code)
	}
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.FinishPair(ctx, invite.Token, body); return err })); code != 401 {
		t.Errorf("replayed finish = %d, want 401", code)
	}
	if devices, err := h.p.Devices(ctx); err != nil || len(devices) != 1 {
		t.Errorf("devices = %v (%v), want the one the link paid for", devices, err)
	}
}

// TestPairTokenExpires refuses a link past its five minutes, at the begin and at the finish:
// a finish whose link aged out between the two (or was spent by a racing finish) meets the same
// 401 the begin does.
func TestPairTokenExpires(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	h.add(301)
	if _, err := h.p.BeginPair(ctx, invite.Token, "iPhone"); err == nil {
		t.Fatal("begin on an expired link succeeded, want 401")
	} else if code := statusOf(t, err); code != 401 {
		t.Errorf("begin on an expired link = %d, want 401", code)
	}

	// the expiry lands between the begin and the finish: the ceremony is open, the store says no
	invite, err = h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	creation, err := h.p.BeginPair(ctx, invite.Token, "iPhone")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.db.SavePairing(ctx, PairDigest(invite.Token), h.p.nowMS()-1); err != nil {
		t.Fatalf("age the pairing out: %v", err)
	}
	body := h.auth.create(t, creationOf(t, creation), "http://localhost:18860", "")
	if _, err := h.p.FinishPair(ctx, invite.Token, body); err == nil {
		t.Fatal("finish on a spent link succeeded, want 401")
	} else if code := statusOf(t, err); code != 401 {
		t.Errorf("finish on a spent link = %d, want 401", code)
	}
	if devices, err := h.p.Devices(ctx); err != nil || len(devices) != 0 {
		t.Errorf("devices after the refused finish = %v (%v), want none", devices, err)
	}
}

// TestPairRefusesBadNames keeps device names to the shape the owner can type and list.
func TestPairRefusesBadNames(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	h.pair(t, "iPhone", "http://localhost:18860")
	for _, name := range []string{"", " iPhone", "iPhone!", "a-very-long-device-name-over-thirty-two"} {
		invite, err := h.p.Mint(ctx)
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if _, err := h.p.BeginPair(ctx, invite.Token, name); err == nil {
			t.Fatalf("begin with name %q succeeded, want 400", name)
		} else if code := statusOf(t, err); code != 400 {
			t.Errorf("begin with name %q = %d, want 400", name, code)
		}
	}
	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := h.p.BeginPair(ctx, invite.Token, "iPhone"); err == nil {
		t.Fatal("begin with a taken name succeeded, want 409")
	} else if code := statusOf(t, err); code != 409 {
		t.Errorf("begin with a taken name = %d, want 409", code)
	}
}

// TestPairRefusesAWrongChallengeAndOrigin proves the ceremony checks what the browser signed:
// a challenge that is not the one this ceremony issued, and an origin that is not ours.
func TestPairRefusesAWrongChallengeAndOrigin(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	creation, err := h.p.BeginPair(ctx, invite.Token, "iPhone")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	o := creationOf(t, creation)
	wrong := h.auth.create(t, o, "http://localhost:18860", "c3RyYW5nZXINoTBHZXFFbUJWOFFmc0hZeHZxNzZhR0Y=")
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.FinishPair(ctx, invite.Token, wrong); return err })); code != 400 {
		t.Errorf("finish on a wrong challenge = %d, want 400", code)
	}
	foreign := h.auth.create(t, o, "https://evil.example", "")
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.FinishPair(ctx, invite.Token, foreign); return err })); code != 400 {
		t.Errorf("finish on a foreign origin = %d, want 400", code)
	}
	garbage := []byte(`{"id":"not","response":{}}`)
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.FinishPair(ctx, invite.Token, garbage); return err })); code != 400 {
		t.Errorf("finish on garbage = %d, want 400", code)
	}
	// the link stays good for a right answer after the refused ones
	if _, err := h.p.FinishPair(ctx, invite.Token, h.auth.create(t, o, "http://localhost:18860", "")); err != nil {
		t.Fatalf("finish after refusals: %v", err)
	}
}

// TestLoginRefusals covers the sign-in's refusals: a replayed assertion, a signature no key of
// ours verifies, and a user handle no device holds.
func TestLoginRefusals(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, first := h.pair(t, "iPhone", "http://localhost:18860")
	handle := h.pairHandle(t, "iPhone")

	assertion, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	challenge := wireText(t, assertion.Response.Challenge)
	body := h.auth.get(t, challenge, "localhost", "http://localhost:18860", handle, false)
	if _, err := h.p.FinishLogin(ctx, body); err != nil {
		t.Fatalf("login finish: %v", err)
	}
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.FinishLogin(ctx, body); return err })); code != 400 {
		t.Errorf("replayed login = %d, want 400", code)
	}

	again, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	challenge = wireText(t, again.Response.Challenge)
	broken := h.auth.get(t, challenge, "localhost", "http://localhost:18860", handle, true)
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.FinishLogin(ctx, broken); return err })); code != 400 {
		t.Errorf("login with a broken signature = %d, want 400", code)
	}

	third, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	challenge = wireText(t, third.Response.Challenge)
	stranger := h.auth.get(t, challenge, "localhost", "http://localhost:18860", b64(make([]byte, 32)), false)
	if code := statusOf(t, mustErr(t, func() error { _, err := h.p.FinishLogin(ctx, stranger); return err })); code != 400 {
		t.Errorf("login with an unknown handle = %d, want 400", code)
	}
	if _, ok := h.p.Session(ctx, first.Cookie); !ok {
		t.Error("the refused logins ended the paired session")
	}
}

// TestSessionEndsAfterSevenDaysIdle proves the idle window is the server's, not only the
// cookie's: seven days without a request end the session even though each request rolls it.
func TestSessionEndsAfterSevenDaysIdle(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, session := h.pair(t, "iPhone", "http://localhost:18860")
	if _, ok := h.p.Session(ctx, session.Cookie); !ok {
		t.Fatal("the fresh session does not resolve")
	}
	h.add(7*24*60*60 + 1)
	if _, ok := h.p.Session(ctx, session.Cookie); ok {
		t.Error("the session outlived seven days without a request")
	}
}

// TestSessionWhyNamesHowTheSignInEnded proves the line the signed-out page reads: nothing while
// the cookie vouches (or when there is no cookie at all), "expired" once the seven days passed,
// "gone" once the device's removal took the session with it.
func TestSessionWhyNamesHowTheSignInEnded(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, session := h.pair(t, "iPhone", "http://localhost:18860")
	if why := h.p.SessionWhy(ctx, session.Cookie); why != "" {
		t.Errorf("a live session's why = %q, want none", why)
	}
	if why := h.p.SessionWhy(ctx, ""); why != "" {
		t.Errorf("no cookie's why = %q, want none", why)
	}
	h.add(7*24*60*60 + 1)
	if why := h.p.SessionWhy(ctx, session.Cookie); why != "expired" {
		t.Errorf("an idle-expired session's why = %q, want expired", why)
	}
	_, second := h.pair(t, "Pixel", "http://localhost:18860")
	if !h.p.Remove(ctx, "Pixel") {
		t.Fatal("remove Pixel answered false")
	}
	if why := h.p.SessionWhy(ctx, second.Cookie); why != "gone" {
		t.Errorf("a removed device's why = %q, want gone", why)
	}
	if why := h.p.SessionWhy(ctx, "hdev_nobodyholdsthis"); why != "gone" {
		t.Errorf("an unknown cookie's why = %q, want gone", why)
	}
}

// TestSessionRenewsAtMostOnceAnHour proves the rolling life's edges: inside the hour after the
// last renewal a request writes nothing, one past the hour rolls the expiry and the renewal
// moment seven days on, and a session the database no longer holds — expired or removed with its
// device — renews nothing.
func TestSessionRenewsAtMostOnceAnHour(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, session := h.pair(t, "iPhone", "http://localhost:18860")
	granted := h.p.nowMS()

	h.add(60*60 - 1)
	if _, ok := h.p.Session(ctx, session.Cookie); !ok {
		t.Fatal("the session died inside its renewal hour")
	}
	if h.p.Renew(ctx, session.Cookie) {
		t.Error("a session inside its renewal hour renewed")
	}
	if expires, renewed := h.sessionRow(t, session.Cookie); expires != granted+PairSessionMS || renewed != granted {
		t.Errorf("the row moved inside the hour: expires %d renewed %d, want %d and %d",
			expires, renewed, granted+PairSessionMS, granted)
	}

	h.add(2) // one second past the hour
	if !h.p.Renew(ctx, session.Cookie) {
		t.Error("a session past its renewal hour did not renew")
	}
	rolled := h.p.nowMS()
	if expires, renewed := h.sessionRow(t, session.Cookie); expires != rolled+PairSessionMS || renewed != rolled {
		t.Errorf("the row after the renewal: expires %d renewed %d, want %d and %d",
			expires, renewed, rolled+PairSessionMS, rolled)
	}

	// the rolled session answers a request that is seven days later still, then dies idle
	h.add(7*24*60*60 - 2*60*60)
	if _, ok := h.p.Session(ctx, session.Cookie); !ok {
		t.Fatal("the rolled session died before its new expiry")
	}
	h.add(2*60*60 + 1)
	if _, ok := h.p.Session(ctx, session.Cookie); ok {
		t.Error("the session outlived seven days without a renewal")
	}
	if h.p.Renew(ctx, session.Cookie) {
		t.Error("an expired session renewed")
	}
}

// TestARemovedDevicesSessionRenewsNothing proves the renewal never reaches a session the
// database says is gone: removing the device ends it, and its cookie renews nothing after.
func TestARemovedDevicesSessionRenewsNothing(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	_, session := h.pair(t, "iPhone", "http://localhost:18860")
	h.add(2 * 60 * 60) // past the renewal hour, so a live session would renew
	if !h.p.Remove(ctx, "iPhone") {
		t.Fatal("remove iPhone answered false")
	}
	if _, ok := h.p.Session(ctx, session.Cookie); ok {
		t.Error("the removed device's session survived")
	}
	if h.p.Renew(ctx, session.Cookie) {
		t.Error("a removed device's session renewed")
	}
}

// TestPairCookieNames serves both listeners: the TLS one takes __Host-huddle with Secure, the
// plain loopback one a name without the prefix and without Secure (a browser refuses Secure on
// http). Both are HttpOnly, SameSite=Strict, Path=/, seven days.
func TestPairCookieNames(t *testing.T) {
	secure := PairCookieSet(true, "hdev_value")
	for _, want := range []string{"__Host-huddle=hdev_value", "Path=/", "Max-Age=604800", "HttpOnly", "Secure", "SameSite=Strict"} {
		if !strings.Contains(secure, want) {
			t.Errorf("secure cookie %q misses %q", secure, want)
		}
	}
	plain := PairCookieSet(false, "hdev_value")
	if !strings.HasPrefix(plain, "huddle=hdev_value") {
		t.Errorf("plain cookie = %q, want the huddle name", plain)
	}
	for _, want := range []string{"Path=/", "Max-Age=604800", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain cookie %q misses %q", plain, want)
		}
	}
	if strings.Contains(plain, "Secure") {
		t.Errorf("plain cookie %q asks for Secure, which http loopback refuses", plain)
	}
}

// TestPairingStoresNoSecrets reads the hub database the pairing wrote and proves the token and
// the ceremony challenges never landed in it: only digests do.
func TestPairingStoresNoSecrets(t *testing.T) {
	h := newPairHarness(t)
	ctx := context.Background()
	invite, err := h.p.Mint(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	creation, err := h.p.BeginPair(ctx, invite.Token, "iPhone")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	o := creationOf(t, creation)
	if _, err := h.p.FinishPair(ctx, invite.Token, h.auth.create(t, o, "http://localhost:18860", "")); err != nil {
		t.Fatalf("finish: %v", err)
	}
	assertion, err := h.p.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("login begin: %v", err)
	}
	raw, err := readAllOf(h.file)
	if err != nil {
		t.Fatalf("read hub.db: %v", err)
	}
	for _, secret := range []string{invite.Token, o.challenge, wireText(t, assertion.Response.Challenge)} {
		if secret != "" && strings.Contains(raw, secret) {
			t.Errorf("the database holds the secret %q whole", secret[:6]+"…")
		}
	}
}

// pairHandle answers the user handle the device enrolled, as base64url text.
func (h *pairHarness) pairHandle(t *testing.T, name string) string {
	t.Helper()
	rows, err := readRows(t, h.file, "SELECT handle FROM devices WHERE name = ?", name)
	if err != nil || len(rows) != 1 {
		t.Fatalf("read the device: %v (%d rows)", err, len(rows))
	}
	handle, _ := rows[0]["handle"].([]byte)
	return b64(handle)
}

// sessionRow reads one session's expiry and renewal moment straight from the database, the shape
// the renewal's own writes are asserted against. A NULL renewal moment reads as 0.
func (h *pairHarness) sessionRow(t *testing.T, cookie string) (expires, renewed int64) {
	t.Helper()
	rows, err := readRows(t, h.file, "SELECT expires, renewed FROM device_sessions WHERE digest = ?",
		PairDigest(cookie))
	if err != nil || len(rows) != 1 {
		t.Fatalf("read the session row: %v (%d rows)", err, len(rows))
	}
	expires, _ = rows[0]["expires"].(int64)
	renewed, _ = rows[0]["renewed"].(int64)
	return expires, renewed
}

// readRows queries the pairing's own database through a second read-only connection, the way
// assertions about the stored rows run beside a live owner.
func readRows(t *testing.T, file, query string, params ...any) ([]store.Row, error) {
	t.Helper()
	st, err := store.Open(context.Background(), file)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.All(context.Background(), query, params...)
}

// readAllOf answers the database file and its WAL sidecar as one text blob, so a secret that
// only ever reached the log cannot hide in either.
func readAllOf(file string) (string, error) {
	var whole []byte
	for _, name := range []string{file, file + "-wal"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		whole = append(whole, raw...)
	}
	return string(whole), nil
}

// mustErr answers the error a call returned, failing the test when it returned none.
func mustErr(t *testing.T, call func() error) error {
	t.Helper()
	err := call()
	if err == nil {
		t.Fatal("the call succeeded, want a refusal")
	}
	return err
}
