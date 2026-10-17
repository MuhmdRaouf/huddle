// Pairing, passkeys and device sessions (SPEC 7.3): one-use links of five minutes, WebAuthn
// credentials with user verification required, and twelve-hour cookie sessions the owner
// revokes by removing the device. Nothing here writes crypto by hand: the ceremonies run
// through go-webauthn, the tokens and sessions come from crypto/rand, and only sha256 digests
// ever reach the database. A pairing token is hashed before any lookup — like every
// password-reset token — so there is no secret comparison to time: the digest is the key, and
// a leaked database names no live link.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/go-webauthn/webauthn/protocol"
	webauthn "github.com/go-webauthn/webauthn/webauthn"
)

// The session cookie's two names: __Host-huddle on a TLS listener (the prefix is the browser's
// own promise the cookie never moves origin), plain huddle on the loopback listener, where a
// browser refuses a Secure cookie over plain http.
const (
	PairHostCookie  = "__Host-huddle"
	PairPlainCookie = "huddle"
)

// The pairing clocks SPEC 7.3 fixes: a link lives five minutes, a session twelve hours. The
// ceremony's own challenge lives only as long as the link beside it.
const (
	PairLinkMS    int64 = 5 * 60 * 1000
	PairSessionMS int64 = 12 * 60 * 60 * 1000
)

// PairTokenRe is a pairing token's shape: 26 characters of base32, 130 bits of crypto/rand.
var PairTokenRe = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// SatelliteRe is a satellite credential's shape (SPEC 6.7): the hsat_ prefix then url-safe
// base64. Only the credential's sha256 digest is ever looked up, so the shape is a cheap guard
// before the hash, not a secret comparison.
var SatelliteRe = regexp.MustCompile(`^hsat_[A-Za-z0-9_-]{20,64}$`)

// DeviceNameRe is a device name's shape: a letter or digit first, then up to 31 more of the
// characters a name a person gives a phone takes.
var DeviceNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,31}$`)

// base32Alphabet is the token's alphabet; 32 divides 256, so every byte of crypto/rand maps to
// a character with no rejection loop and no bias.
const base32Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// PairOptions shapes one pairing core: the relying party the passkeys belong to and the origins
// a browser may speak them from.
type PairOptions struct {
	// RPID is the relying party id: hub.host, else localhost on the loopback listener.
	RPID string
	// Origins are the origins the ceremonies accept: the hub's https origin and the loopback
	// spellings a browser on the machine itself opens.
	Origins []string
	Now     func() time.Time
	Log     func(string)
}

// PairInvite is one minted link: the token appears exactly once, here.
type PairInvite struct {
	Token     string `json:"token"`
	Code      string `json:"code"`
	ExpiresIn int64  `json:"expires_in"`
}

// PairSession is one earned cookie: which device it belongs to, the cookie's value (shown once,
// here, then only as a digest), and how long it lasts.
type PairSession struct {
	Device    string `json:"device"`
	Cookie    string `json:"cookie"`
	ExpiresIn int64  `json:"expires_in"`
}

// DeviceView is one paired device as the owner lists it.
type DeviceView struct {
	Name  string `json:"name"`
	Added string `json:"added"`
	Seen  string `json:"seen"`
}

// Pairing is the pairing and passkey core over the home's hub database. The pending ceremonies
// live in memory only — a challenge is spent with the response that answers it, and a restart
// simply asks the browser to begin again.
type Pairing struct {
	db  *hub.DB
	wa  *webauthn.WebAuthn
	now func() time.Time
	log func(string)

	mu      sync.Mutex
	pairs   map[string]*pendingPair  // keyed by the pairing digest
	logins  map[string]*pendingLogin // keyed by the challenge
	stepups map[string]*pendingLogin // keyed by the challenge
}

// pendingPair is one registration ceremony a link opened.
type pendingPair struct {
	name     string
	handle   []byte
	session  webauthn.SessionData
	deadline int64
}

// pendingLogin is one sign-in ceremony.
type pendingLogin struct {
	session  webauthn.SessionData
	deadline int64
}

// deviceUser is one device as go-webauthn sees it: its handle is the user handle, its name the
// display name, and the one credential the passkey made.
type deviceUser struct {
	d          hub.Device
	credential webauthn.Credential
}

func (u deviceUser) WebAuthnID() []byte          { return u.d.Handle }
func (u deviceUser) WebAuthnName() string        { return u.d.Name }
func (u deviceUser) WebAuthnDisplayName() string { return u.d.Name }
func (u deviceUser) WebAuthnCredentials() []webauthn.Credential {
	return []webauthn.Credential{u.credential}
}

// NewPairing builds the pairing core over the home's hub database.
func NewPairing(db *hub.DB, o PairOptions) (*Pairing, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID: o.RPID, RPDisplayName: "Huddle", RPOrigins: o.Origins,
	})
	if err != nil {
		return nil, fmt.Errorf("the passkey relying party %q is not usable: %w", o.RPID, err)
	}
	return &Pairing{
		db: db, wa: wa, now: o.Now, log: o.Log,
		pairs: map[string]*pendingPair{}, logins: map[string]*pendingLogin{},
		stepups: map[string]*pendingLogin{},
	}, nil
}

// nowMS is the clock, in whole milliseconds.
func (p *Pairing) nowMS() int64 { return p.now().UnixMilli() }

// Mint draws one pairing token and stores its digest with a five-minute life. The token itself
// never reaches the database; it appears exactly once, in the answer.
func (p *Pairing) Mint(ctx context.Context) (PairInvite, error) {
	raw := randomBytes(26)
	token := make([]byte, 26)
	for i, b := range raw {
		token[i] = base32Alphabet[b%32]
	}
	expires := p.nowMS() + PairLinkMS
	if err := p.db.SavePairing(ctx, PairDigest(string(token)), expires); err != nil {
		return PairInvite{}, err
	}
	p.log("pairing link minted, expires " + iso(expires))
	return PairInvite{Token: string(token), Code: PairCode(string(token)), ExpiresIn: PairLinkMS / 1000}, nil
}

// PairDigest is a pairing token's stored form: the hex of its sha256.
func PairDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// PairCode is the code both sides of a pairing compare: the token's first six characters as
// XXX-XXX. It names no secret worth stealing — six base32 characters cannot mint a session —
// it only lets the owner see the phone is reading the link it printed.
func PairCode(token string) string {
	if len(token) < 6 {
		return ""
	}
	return token[:3] + "-" + token[3:6]
}

// BeginPair opens one registration ceremony on a live link: the creation options a browser
// turns into a passkey. A used or expired link answers the same 401, so a guess learns nothing
// about which is which.
func (p *Pairing) BeginPair(ctx context.Context, token, name string) (*protocol.CredentialCreation, error) {
	if !PairTokenRe.MatchString(token) {
		return nil, &channel.HuddleError{Status: 401, Msg: "invalid or expired pairing link: run huddle pair for a new one"}
	}
	digest := PairDigest(token)
	if !p.db.PairingLive(ctx, digest, p.nowMS()) {
		return nil, &channel.HuddleError{Status: 401, Msg: "invalid or expired pairing link: run huddle pair for a new one"}
	}
	if !DeviceNameRe.MatchString(name) {
		return nil, &channel.HuddleError{Status: 400, Msg: "device name: a letter or digit first, up to 32 of them, spaces and dashes"}
	}
	if _, taken, err := p.db.DeviceByName(ctx, name); err != nil {
		return nil, err
	} else if taken {
		return nil, &channel.HuddleError{Status: 409, Msg: "a device named " + name + " is already paired: remove it first (huddle devices rm " + name + ")"}
	}
	handle := randomBytes(32)
	creation, session, err := p.wa.BeginRegistration(deviceUser{d: hub.Device{Name: name, Handle: handle}},
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		}))
	if err != nil {
		return nil, &channel.HuddleError{Status: 500, Msg: "the passkey ceremony could not begin"}
	}
	p.sweep()
	p.mu.Lock()
	p.pairs[digest] = &pendingPair{name: name, handle: handle, session: *session, deadline: p.nowMS() + PairLinkMS}
	p.mu.Unlock()
	return creation, nil
}

// FinishPair verifies one registration against the ceremony the link opened, and records the
// device with the session its cookie earns. The link is spent in the same transaction that
// writes the device, so it can pay for exactly one however two finishes race.
func (p *Pairing) FinishPair(ctx context.Context, token string, body []byte) (PairSession, error) {
	if !PairTokenRe.MatchString(token) {
		return PairSession{}, &channel.HuddleError{Status: 401, Msg: "invalid or expired pairing link: run huddle pair for a new one"}
	}
	digest := PairDigest(token)
	p.mu.Lock()
	pending := p.pairs[digest]
	p.mu.Unlock()
	if pending == nil || pending.deadline <= p.nowMS() {
		return PairSession{}, &channel.HuddleError{Status: 401, Msg: "invalid or expired pairing link: run huddle pair for a new one"}
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return PairSession{}, p.refused("the passkey response did not parse", err)
	}
	credential, err := p.wa.CreateCredential(deviceUser{d: hub.Device{Name: pending.name, Handle: pending.handle}},
		pending.session, parsed)
	if err != nil {
		return PairSession{}, p.refused("the passkey response did not verify", err)
	}
	record, err := json.Marshal(credential)
	if err != nil {
		return PairSession{}, err
	}
	cookie, sessionDigest, expires := p.freshSession()
	err = p.db.PairFinish(ctx, digest, hub.Device{
		Name: pending.name, Handle: pending.handle, CredentialJSON: record,
	}, sessionDigest, expires, p.nowMS())
	if err != nil {
		var gone hub.PairingGoneError
		if errors.As(err, &gone) {
			// another finish spent the link first, or it aged out between the checks: the same
			// word either way, so a racing guess learns nothing
			return PairSession{}, &channel.HuddleError{Status: 401, Msg: "invalid or expired pairing link: run huddle pair for a new one"}
		}
		return PairSession{}, err
	}
	p.mu.Lock()
	delete(p.pairs, digest)
	p.mu.Unlock()
	p.log("device " + pending.name + " paired")
	return PairSession{Device: pending.name, Cookie: cookie, ExpiresIn: PairSessionMS / 1000}, nil
}

// BeginLogin opens one discoverable sign-in: the resident key names its device, so the request
// offers no credential list for a thief to count.
func (p *Pairing) BeginLogin(ctx context.Context) (*protocol.CredentialAssertion, error) {
	assertion, session, err := p.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, &channel.HuddleError{Status: 500, Msg: "the passkey ceremony could not begin"}
	}
	p.sweep()
	p.mu.Lock()
	p.logins[session.Challenge] = &pendingLogin{session: *session, deadline: p.nowMS() + PairLinkMS}
	p.mu.Unlock()
	return assertion, nil
}

// FinishLogin verifies one assertion against the ceremony that issued its challenge and mints
// the device a fresh session. A replayed body finds its challenge already spent.
func (p *Pairing) FinishLogin(ctx context.Context, body []byte) (PairSession, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return PairSession{}, p.refused("the passkey response did not parse", err)
	}
	challenge := parsed.Response.CollectedClientData.Challenge
	p.mu.Lock()
	pending := p.logins[challenge]
	delete(p.logins, challenge) // one response spends its challenge, pass or fail
	p.mu.Unlock()
	if pending == nil || pending.deadline <= p.nowMS() {
		return PairSession{}, &channel.HuddleError{Status: 400, Msg: "this sign-in's challenge is spent or stale: begin again"}
	}
	device, credential, err := p.wa.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		found, ok, err := p.db.DeviceByHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("no device holds this handle")
		}
		stored := webauthn.Credential{}
		if err := json.Unmarshal(found.CredentialJSON, &stored); err != nil {
			return nil, err
		}
		return deviceUser{d: found, credential: stored}, nil
	}, pending.session, parsed)
	if err != nil {
		return PairSession{}, p.refused("the passkey response did not verify", err)
	}
	if credential.Authenticator.CloneWarning {
		// the counter went backwards: two authenticators hold this passkey, and neither signs
		// another session until the owner pairs a fresh one
		return PairSession{}, p.refused("this passkey's counter went backwards; it may be cloned", nil)
	}
	dev, ok := device.(deviceUser)
	if !ok {
		return PairSession{}, &channel.HuddleError{Status: 500, Msg: "the signing device lost its shape"}
	}
	record, err := json.Marshal(credential)
	if err != nil {
		return PairSession{}, err
	}
	cookie, sessionDigest, expires := p.freshSession()
	if err := p.db.LoginFinish(ctx, hub.Device{
		Name: dev.d.Name, Handle: dev.d.Handle, CredentialJSON: record,
	}, sessionDigest, expires, p.nowMS()); err != nil {
		return PairSession{}, err
	}
	p.log("device " + dev.d.Name + " signed in")
	return PairSession{Device: dev.d.Name, Cookie: cookie, ExpiresIn: PairSessionMS / 1000}, nil
}

// Session resolves one cookie to who it is: the owner, on the device it paired. A removed
// device has no sessions left to answer, which is the revocation.
func (p *Pairing) Session(ctx context.Context, cookie string) (Who, bool) {
	if cookie == "" {
		return Who{}, false
	}
	device, ok, err := p.db.DeviceSession(ctx, PairDigest(cookie), p.nowMS())
	if err != nil || !ok {
		return Who{}, false
	}
	return Who{Name: "owner", Root: true, Invite: true, Device: device}, true
}

// Devices lists the paired devices, the oldest first.
func (p *Pairing) Devices(ctx context.Context) ([]DeviceView, error) {
	list, err := p.db.Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DeviceView, 0, len(list))
	for _, d := range list {
		out = append(out, DeviceView{Name: d.Name, Added: iso(d.AddedMS), Seen: iso(d.SeenMS)})
	}
	return out, nil
}

// Remove drops a device and every session of it at once.
func (p *Pairing) Remove(ctx context.Context, name string) bool {
	removed, err := p.db.DeviceRemove(ctx, name, p.nowMS())
	if err != nil {
		p.log(fmt.Sprintf("could not remove device %s: %v", name, err))
		return false
	}
	if removed {
		p.log("device " + name + " removed")
	}
	return removed
}

// freshSession draws one cookie value with its digest and expiry.
func (p *Pairing) freshSession() (cookie, digest string, expires int64) {
	cookie = "hdev_" + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	return cookie, PairDigest(cookie), p.nowMS() + PairSessionMS
}

// refused logs the detail the operator needs and answers the stable word the caller sees: the
// library's error text names fields, never a challenge or a key, so it reaches the log and not
// the answer.
func (p *Pairing) refused(msg string, detail error) error {
	if detail != nil {
		p.log("pairing refused: " + detail.Error())
	}
	return &channel.HuddleError{Status: 400, Msg: msg}
}

// sweep drops the ceremonies past their deadline.
func (p *Pairing) sweep() {
	t := p.nowMS()
	p.mu.Lock()
	for key, pending := range p.pairs {
		if pending.deadline <= t {
			delete(p.pairs, key)
		}
	}
	for key, pending := range p.logins {
		if pending.deadline <= t {
			delete(p.logins, key)
		}
	}
	for key, pending := range p.stepups {
		if pending.deadline <= t {
			delete(p.stepups, key)
		}
	}
	p.mu.Unlock()
}

// PairCookieSet renders the session cookie's Set-Cookie line: __Host-huddle with Secure on a
// TLS listener, plain huddle without it on the loopback one (a browser refuses Secure over
// http). Both are HttpOnly, Path=/, SameSite=Strict, and last twelve hours.
func PairCookieSet(secure bool, value string) string {
	name := PairPlainCookie
	mark := ""
	if secure {
		name, mark = PairHostCookie, "; Secure"
	}
	return name + "=" + value + "; Path=/; Max-Age=" + fmt.Sprint(PairSessionMS/1000) +
		"; HttpOnly; SameSite=Strict" + mark
}

// PairCookie reads the session cookie a request carries, the secure name first.
func PairCookie(req *http.Request) string {
	for _, name := range []string{PairHostCookie, PairPlainCookie} {
		if cookie, err := req.Cookie(name); err == nil && cookie.Value != "" {
			return cookie.Value
		}
	}
	return ""
}

// StepUpWindowMS is how long one assertion covers the dangerous routes (SPEC 7.4).
const StepUpWindowMS int64 = 5 * 60 * 1000

// BeginStepUp opens one discoverable assertion ceremony (SPEC 7.4), the user-verified shape a
// sign-in runs. Its challenges live apart from the sign-ins', so neither ceremony can spend the
// other's response.
func (p *Pairing) BeginStepUp(ctx context.Context) (*protocol.CredentialAssertion, error) {
	assertion, session, err := p.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, &channel.HuddleError{Status: 500, Msg: "the passkey ceremony could not begin"}
	}
	p.sweep()
	p.mu.Lock()
	p.stepups[session.Challenge] = &pendingLogin{session: *session, deadline: p.nowMS() + PairLinkMS}
	p.mu.Unlock()
	return assertion, nil
}

// FinishStepUp verifies one assertion against the ceremony that issued its challenge and stamps
// the five minutes it buys onto the cookie's own session row: one browser's assertion never
// covers another's. The device whose passkey signs must be the one the session belongs to — a
// valid assertion from a neighbour's passkey covers nothing. The answer names the device, for
// the audit row the route that called owes.
func (p *Pairing) FinishStepUp(ctx context.Context, cookie string, body []byte) (string, error) {
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return "", p.refused("the passkey response did not parse", err)
	}
	challenge := parsed.Response.CollectedClientData.Challenge
	p.mu.Lock()
	pending := p.stepups[challenge]
	delete(p.stepups, challenge) // one response spends its challenge, pass or fail
	p.mu.Unlock()
	if pending == nil || pending.deadline <= p.nowMS() {
		return "", &channel.HuddleError{Status: 400, Msg: "this step-up's challenge is spent or stale: begin again"}
	}
	holder, live, err := p.db.DeviceSession(ctx, PairDigest(cookie), p.nowMS())
	if err != nil {
		return "", err
	}
	if !live {
		return "", &channel.HuddleError{Status: 401, Msg: "this browser session is not signed in: sign in again"}
	}
	user, credential, err := p.wa.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		found, ok, err := p.db.DeviceByHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("no device holds this handle")
		}
		stored := webauthn.Credential{}
		if err := json.Unmarshal(found.CredentialJSON, &stored); err != nil {
			return nil, err
		}
		return deviceUser{d: found, credential: stored}, nil
	}, pending.session, parsed)
	if err != nil {
		return "", p.refused("the passkey response did not verify", err)
	}
	if credential.Authenticator.CloneWarning {
		// the counter went backwards: two authenticators hold this passkey, and neither steps
		// another session up
		return "", p.refused("this passkey's counter went backwards; it may be cloned", nil)
	}
	dev, ok := user.(deviceUser)
	if !ok {
		return "", &channel.HuddleError{Status: 500, Msg: "the signing device lost its shape"}
	}
	if dev.d.Name != holder {
		return "", &channel.HuddleError{Status: 403, Msg: "this passkey belongs to another device: step up from its own browser"}
	}
	record, err := json.Marshal(credential)
	if err != nil {
		return "", err
	}
	stamped, err := p.db.StepUpFinish(ctx, PairDigest(cookie), hub.Device{
		Name: dev.d.Name, CredentialJSON: record,
	}, p.nowMS())
	if err != nil {
		return "", err
	}
	if !stamped {
		return "", &channel.HuddleError{Status: 401, Msg: "this browser session is not signed in: sign in again"}
	}
	p.log("device " + dev.d.Name + " stepped up")
	return dev.d.Name, nil
}

// StepUpDue answers whether the browser session a cookie names owes a fresh assertion (SPEC
// 7.4): no cookie, no live session, no assertion yet, or one older than the window all read
// due. The now comes from the caller, so the window's edges are testable; a read that cannot be
// answered reads due too — an error never waves a dangerous act through.
func (p *Pairing) StepUpDue(ctx context.Context, cookie string, nowMS int64) bool {
	if cookie == "" {
		return true
	}
	fresh, err := p.db.SessionSteppedUp(ctx, PairDigest(cookie), nowMS, StepUpWindowMS)
	if err != nil || !fresh {
		return true
	}
	return false
}

// AnyDevicePaired answers whether the home holds a passkey at all: the fact that ends the solo
// waiver, where the local sign-in stands as the assertion.
func (p *Pairing) AnyDevicePaired(ctx context.Context) bool {
	any, err := p.db.AnyDevice(ctx)
	return err == nil && any
}
