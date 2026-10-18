package auth

// Authenticator-app sign-in (RFC 6238) beside the passkey: one enrolment per hub — a secret the
// owner's authenticator app holds, ten one-time recovery codes kept as sha256 digests — and a
// code sign-in that mints the same rolling device session a passkey does, named for the browser
// it came from. Every comparison over a code runs in constant time and the candidates are all
// weighed before any is named, so nothing about which step matched leaks in the timing; the
// secret and the codes reach no log line, no audit row and no error body. Five failed codes in
// five minutes lock code sign-in for fifteen: the hub has one owner, so the lock is global.

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"rsc.io/qr"
)

// The authenticator's own constants: the step, digits and hash every app supports (RFC 6238's
// defaults), the skew one side of the step either way, the recovery codes' count, the lockout's
// window and length, and how long a begun enrolment waits for its confirming code.
const (
	TOTPStepSeconds = 30
	TOTPSkewSteps   = 1
	TOTPRecoveryN   = 10

	TOTPFailWindowMS int64 = 5 * 60 * 1000
	TOTPLockMS       int64 = 15 * 60 * 1000
	TOTPEnrolMS      int64 = 5 * 60 * 1000
)

// TotpLockedMsg is the lockout's one answer, the word the card fixes.
const TotpLockedMsg = "Too many tries. Wait 15 minutes or use a passkey."

// TotpWrongMsg is a refused code's answer: no word in it names which check failed.
const TotpWrongMsg = "That code did not work. Check your app, or use a recovery code."

// TOTPOptions shapes the core: the machine name the otpauth URI carries (what the owner's app
// lists the hub as), the clock and the log.
type TOTPOptions struct {
	Machine string
	Now     func() time.Time
	Log     func(string)
}

// TOTPEnrol is what a begin answers: the otpauth URI the app scans, the secret for manual entry,
// and the QR as rows of 1 (dark) and 0 (light) the page turns into an SVG.
type TOTPEnrol struct {
	URI    string   `json:"uri"`
	Secret string   `json:"secret"`
	QR     []string `json:"qr"`
}

// TOTP is the authenticator core over the home's hub database. The begun enrolment lives in
// memory only: nothing reaches the database before a code proves the owner's app holds the
// secret, and a restart simply asks for a fresh one.
type TOTP struct {
	db      *hub.DB
	now     func() time.Time
	log     func(string)
	machine string

	mu      sync.Mutex
	pending *pendingEnrol
}

// pendingEnrol is one begun enrolment waiting for its confirming code.
type pendingEnrol struct {
	secret   string
	deadline int64
}

// NewTOTP builds the authenticator core over the home's hub database.
func NewTOTP(db *hub.DB, o TOTPOptions) *TOTP {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
	return &TOTP{db: db, now: o.Now, log: o.Log, machine: o.Machine}
}

// nowMS is the clock, in whole milliseconds.
func (t *TOTP) nowMS() int64 { return t.now().UnixMilli() }

// Enrolled answers whether the hub holds an authenticator enrolment.
func (t *TOTP) Enrolled(ctx context.Context) bool {
	_, ok, err := t.db.TOTP(ctx)
	return err == nil && ok
}

// BeginEnrol draws one secret and answers what the owner's app needs: the otpauth URI, the
// secret for manual entry and its QR. Nothing is stored — the enrolment lands only when a code
// from the app verifies the secret (FinishEnrol), and a begun enrolment nobody confirms is worth
// nothing after five minutes.
func (t *TOTP) BeginEnrol(ctx context.Context) (TOTPEnrol, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: "Huddle", AccountName: t.machine,
		Period: TOTPStepSeconds, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return TOTPEnrol{}, &channel.HuddleError{Status: 500, Msg: "the authenticator secret could not be drawn"}
	}
	code, err := qr.Encode(key.URL(), qr.L)
	if err != nil {
		return TOTPEnrol{}, &channel.HuddleError{Status: 500, Msg: "the enrolment's QR code could not be drawn"}
	}
	rows := make([]string, code.Size)
	for y := range rows {
		var row strings.Builder
		for x := 0; x < code.Size; x++ {
			if code.Black(x, y) {
				row.WriteByte('1')
			} else {
				row.WriteByte('0')
			}
		}
		rows[y] = row.String()
	}
	t.mu.Lock()
	t.pending = &pendingEnrol{secret: key.Secret(), deadline: t.nowMS() + TOTPEnrolMS}
	t.mu.Unlock()
	return TOTPEnrol{URI: key.URL(), Secret: key.Secret(), QR: rows}, nil
}

// FinishEnrol verifies the code against the begun enrolment's secret and stores the enrolment
// whole: the secret with a fresh set of recovery codes, kept as digests. The answer carries the
// ten recovery codes in plain exactly once — this is the only moment they exist — and a wrong
// code stores nothing and leaves the begun enrolment open for the next try.
func (t *TOTP) FinishEnrol(ctx context.Context, actor, code string) ([]string, error) {
	t.mu.Lock()
	pending := t.pending
	t.mu.Unlock()
	if pending == nil || pending.deadline <= t.nowMS() {
		return nil, &channel.HuddleError{Status: 400, Msg: "no authenticator set-up waits for a code: start again"}
	}
	if _, ok := totpMatch(pending.secret, t.step(), -1, code); !ok {
		return nil, &channel.HuddleError{Status: 400, Msg: TotpWrongMsg}
	}
	recovery := recoveryCodes()
	if err := t.db.TOTPEnrol(ctx, pending.secret, recovery, t.nowMS(), actor); err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.pending = nil
	t.mu.Unlock()
	t.log("authenticator sign-in enrolled")
	return recovery, nil
}

// SignIn verifies one code — an authenticator's or a recovery code's — and mints the browser a
// device session like a passkey's, named for the browser it came from. The lockout gates every
// attempt, the codes of the steps at or behind the last accepted one are spent, and a recovery
// code leaves with the sign-in it pays for.
func (t *TOTP) SignIn(ctx context.Context, code, device string) (PairSession, error) {
	state, ok, err := t.db.TOTP(ctx)
	if err != nil {
		return PairSession{}, err
	}
	nowMS := t.nowMS()
	if !ok || state.LockedUntil > nowMS {
		return PairSession{}, &channel.HuddleError{Status: 429, Msg: TotpLockedMsg}
	}
	given := totpNormalize(code)
	handle := randomBytes(32)
	cookie, sessionDigest, expires := t.freshSession()
	if step, matched := totpMatch(state.Secret, t.step(), state.LastStep, given); matched {
		if err := t.db.TOTPSignInCode(ctx, step, device, handle, sessionDigest, expires, nowMS); err != nil {
			return PairSession{}, t.spentAsWrong(ctx, nowMS, err)
		}
		t.log("device " + device + " signed in with an authenticator code")
		return PairSession{Device: device, Cookie: cookie, ExpiresIn: PairSessionMS / 1000}, nil
	}
	digests, err := t.db.TOTPRecovery(ctx)
	if err != nil {
		return PairSession{}, err
	}
	if digest, spent := recoveryMatch(digests, given); spent {
		if err := t.db.TOTPSignInRecovery(ctx, digest, device, handle, sessionDigest, expires, nowMS); err != nil {
			return PairSession{}, t.spentAsWrong(ctx, nowMS, err)
		}
		t.log("device " + device + " signed in with a recovery code")
		return PairSession{Device: device, Cookie: cookie, ExpiresIn: PairSessionMS / 1000}, nil
	}
	locked, err := t.db.TOTPFail(ctx, nowMS, TOTPFailWindowMS, TOTPLockMS)
	if err != nil {
		return PairSession{}, err
	}
	if locked {
		return PairSession{}, &channel.HuddleError{Status: 429, Msg: TotpLockedMsg}
	}
	return PairSession{}, &channel.HuddleError{Status: 401, Msg: TotpWrongMsg}
}

// spentAsWrong answers a code another request spent first the way a wrong code is answered, and
// counts it against the lockout like one; any other failure passes through.
func (t *TOTP) spentAsWrong(ctx context.Context, nowMS int64, err error) error {
	var spent hub.CodeSpentError
	if !errors.As(err, &spent) {
		return err
	}
	if locked, ferr := t.db.TOTPFail(ctx, nowMS, TOTPFailWindowMS, TOTPLockMS); ferr == nil && locked {
		return &channel.HuddleError{Status: 429, Msg: TotpLockedMsg}
	}
	return &channel.HuddleError{Status: 401, Msg: TotpWrongMsg}
}

// Remove drops the enrolment whole: the secret, the recovery codes, every session a code minted
// and the device rows no passkey ever landed on. It answers false when nothing is enrolled.
func (t *TOTP) Remove(ctx context.Context, actor string) (bool, error) {
	removed, err := t.db.TOTPRemove(ctx, t.nowMS(), actor)
	if err != nil {
		return false, err
	}
	if removed {
		t.log("authenticator sign-in removed; its sessions ended")
	}
	return removed, nil
}

// freshSession draws one cookie value with its digest and expiry, the passkey session's own.
func (t *TOTP) freshSession() (cookie, digest string, expires int64) {
	cookie = "hdev_" + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	return cookie, PairDigest(cookie), t.nowMS() + PairSessionMS
}

// step is the RFC 6238 counter the clock names now.
func (t *TOTP) step() int64 { return t.now().Unix() / TOTPStepSeconds }

// totpMatch weighs the code against every candidate step — the skew window around now, each one
// only if it sits ahead of the last accepted step — and names a match only after all were
// weighed: no early exit, every comparison constant-time, so the timing says nothing about which
// step (or whether any) matched. lastStep -1 admits every step: a fresh enrolment's own check.
func totpMatch(secret string, step, lastStep int64, code string) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	matched, found := int64(0), 0
	for _, s := range []int64{step - TOTPSkewSteps, step, step + TOTPSkewSteps} {
		want, err := totp.GenerateCodeCustom(secret, time.Unix(s*TOTPStepSeconds, 0), totp.ValidateOpts{
			Period: TOTPStepSeconds, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			continue
		}
		eq := subtle.ConstantTimeCompare([]byte(want), []byte(code))
		ahead := 0
		if s > lastStep {
			ahead = 1
		}
		if eq&ahead == 1 && found == 0 {
			matched, found = s, 1
		}
	}
	return matched, found == 1
}

// recoveryMatch weighs the given code's digest against every recovery digest the hub holds and
// answers the one it equals, spending none of the comparisons early: the set's size is the
// only thing its timing names.
func recoveryMatch(digests []string, code string) (string, bool) {
	given := hub.TOTPDigest(code)
	matched := ""
	found := 0
	for _, digest := range digests {
		eq := subtle.ConstantTimeCompare([]byte(digest), []byte(given))
		if eq == 1 && found == 0 {
			matched, found = digest, 1
		}
	}
	return matched, found == 1
}

// totpNormalize folds what a person might type into the canonical form: lower case, no spaces,
// no dashes — a recovery code's xxxx-xxxx and an app's grouped digits both land as stored.
func totpNormalize(code string) string {
	return strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.ToLower(code))
}

// recoveryLower is the lowercase base32 alphabet a recovery code draws from: 32 symbols, the
// set every base32 spelling shares, in the case a person reads best.
const recoveryLower = "abcdefghijklmnopqrstuvwxyz234567"

// recoveryCodes draws ten one-time codes of xxxx-xxxx: one crypto/rand byte per character, its
// low five bits naming the symbol — a uniform pick, since 32 divides 256, with no bias and no
// rejection loop.
func recoveryCodes() []string {
	codes := make([]string, 0, TOTPRecoveryN)
	for i := 0; i < TOTPRecoveryN; i++ {
		raw := randomBytes(8)
		chars := make([]byte, 8)
		for j, b := range raw {
			chars[j] = recoveryLower[b&31]
		}
		codes = append(codes, string(chars[:4])+"-"+string(chars[4:]))
	}
	return codes
}

// DeviceNameFromUA names the browser a code sign-in belongs to, the same word the pair screen
// offers the device it pairs: the platform the user agent spells, or Browser.
func DeviceNameFromUA(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"):
		return "iPhone"
	case strings.Contains(ua, "iPad"):
		return "iPad"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Macintosh"), strings.Contains(ua, "Mac OS X"):
		return "Mac"
	case strings.Contains(ua, "Linux"):
		return "Linux"
	default:
		return "Browser"
	}
}
