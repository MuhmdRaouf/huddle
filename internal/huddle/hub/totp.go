package hub

// Authenticator-app storage (RFC 6238): one enrolment row per hub holding the shared secret with
// the last step a code paid for and the lockout's counters, the sha256 digests of the one-time
// recovery codes, and the sessions a code minted — tagged 'totp' so the enrolment's removal ends
// them while the passkeys' sessions ride on. A code-born device row carries the credential
// 'null' until a passkey lands on it, and those rows go when the enrolment does.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// TOTPState is the one enrolment row: the shared secret, the last step a code was accepted for,
// and the lockout's counters — how many codes failed inside the window that started when the
// first of them did, and the moment the lock ends (0 while none runs).
type TOTPState struct {
	Secret      string
	LastStep    int64
	Fails       int64
	WindowStart int64
	LockedUntil int64
}

// noCredential is the credential a code-born device row holds: JSON null, the value an absent
// WebAuthn credential decodes to. Only a real assertion ever reads a row's credential, and no
// code-born row has one to assert with.
const noCredential = "null"

// TOTP answers the enrolment row; ok is false when the hub holds none.
func (d *DB) TOTP(ctx context.Context) (TOTPState, bool, error) {
	row, err := d.st.Get(ctx,
		"SELECT secret, last_step, fails, window_start, locked_until FROM totp WHERE id = 1")
	if err != nil || row == nil {
		return TOTPState{}, false, err
	}
	state := TOTPState{Secret: textOfRow(row, "secret")}
	state.LastStep = intOfRow(row, "last_step")
	state.Fails = intOfRow(row, "fails")
	state.WindowStart = intOfRow(row, "window_start")
	state.LockedUntil = intOfRow(row, "locked_until")
	return state, true, nil
}

// TOTPRecovery answers the recovery codes' digests, the oldest first.
func (d *DB) TOTPRecovery(ctx context.Context) ([]string, error) {
	rows, err := d.st.All(ctx, "SELECT digest FROM totp_recovery ORDER BY added, digest")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, textOfRow(row, "digest"))
	}
	return out, nil
}

// TOTPEnrol writes the enrolment whole — the secret, the fresh recovery digests, and the lockout
// reset — replacing any enrolment the row already held, with the audit row, in one transaction.
func (d *DB) TOTPEnrol(ctx context.Context, secret string, recovery []string, nowMS int64, actor string) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM totp"); err != nil {
			return err
		}
		if err := q.Run(ctx, "DELETE FROM totp_recovery"); err != nil {
			return err
		}
		if err := q.Run(ctx,
			"INSERT INTO totp (id, secret, last_step, fails, window_start, locked_until) VALUES (1, ?, 0, 0, 0, 0)",
			secret); err != nil {
			return err
		}
		for _, code := range recovery {
			if err := q.Run(ctx, "INSERT INTO totp_recovery (digest, added) VALUES (?, ?)",
				TOTPDigest(code), nowMS); err != nil {
				return err
			}
		}
		return auditRow(ctx, q, nowMS, actor, "totp.enrol", "owner")
	})
}

// CodeSpentError is a sign-in whose code another request spent first, or that arrived while code
// sign-in is locked: the caller answers it like a wrong code.
type CodeSpentError struct{}

func (CodeSpentError) Error() string { return "the code is already spent" }

// TOTPSignInCode records one accepted code: the step it paid for becomes the last accepted
// (never moving backwards), the lockout's counters clear, and the browser's device row — found
// by name or born here with no passkey — carries the session the code earned, with the audit
// row, in one transaction.
func (d *DB) TOTPSignInCode(ctx context.Context, step int64, device string, handle []byte,
	sessionDigest string, sessionExpiresMS, nowMS int64) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		// the step check lives in the write itself: two requests carrying one code race to
		// this row, and only the first moves last_step past it
		if err := q.Run(ctx,
			"UPDATE totp SET last_step = ?, fails = 0, window_start = 0 WHERE id = 1 AND last_step < ? AND locked_until <= ?",
			step, step, nowMS); err != nil {
			return err
		}
		if changed, err := changesOf(ctx, q); err != nil {
			return err
		} else if !changed {
			return CodeSpentError{}
		}
		return totpSession(ctx, q, device, handle, sessionDigest, sessionExpiresMS, nowMS)
	})
}

// TOTPSignInRecovery records one spent recovery code — its digest leaves with the sign-in it
// paid for — beside the same session mint a code earns, with the audit row, in one transaction.
func (d *DB) TOTPSignInRecovery(ctx context.Context, digest, device string, handle []byte,
	sessionDigest string, sessionExpiresMS, nowMS int64) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM totp_recovery WHERE digest = ?", digest); err != nil {
			return err
		}
		// the delete is the spend: a second request with the same code finds nothing to delete
		if changed, err := changesOf(ctx, q); err != nil {
			return err
		} else if !changed {
			return CodeSpentError{}
		}
		if err := q.Run(ctx, "UPDATE totp SET fails = 0, window_start = 0 WHERE id = 1"); err != nil {
			return err
		}
		return totpSession(ctx, q, device, handle, sessionDigest, sessionExpiresMS, nowMS)
	})
}

// totpSession finds or births the browser's device row and carries the session the code earned,
// born stepped up like every code proof is (the code is the user verification itself).
func totpSession(ctx context.Context, q store.Q, device string, handle []byte,
	sessionDigest string, sessionExpiresMS, nowMS int64) error {
	row, err := q.Get(ctx, "SELECT 1 FROM devices WHERE name = ?", device)
	if err != nil {
		return err
	}
	if row == nil {
		if err := q.Run(ctx,
			"INSERT INTO devices (name, handle, credential, added, seen) VALUES (?, ?, ?, ?, ?)",
			device, handle, noCredential, nowMS, nowMS); err != nil {
			return err
		}
	} else if err := q.Run(ctx, "UPDATE devices SET seen = ? WHERE name = ?", nowMS, device); err != nil {
		return err
	}
	if err := q.Run(ctx,
		"INSERT INTO device_sessions (digest, device, expires, stepup_at, renewed, method) VALUES (?, ?, ?, ?, ?, 'totp')",
		sessionDigest, device, sessionExpiresMS, nowMS, nowMS); err != nil {
		return err
	}
	return auditRow(ctx, q, nowMS, device, "totp.signin", device)
}

// TOTPFail records one refused code against the lockout's window and answers whether this
// failure was the one that locked code sign-in, with the audit row.
func (d *DB) TOTPFail(ctx context.Context, nowMS, failWindowMS, lockMS int64) (bool, error) {
	locked := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		row, err := q.Get(ctx, "SELECT fails, window_start FROM totp WHERE id = 1")
		if err != nil || row == nil {
			return err
		}
		fails, window := intOfRow(row, "fails"), intOfRow(row, "window_start")
		if nowMS >= window+failWindowMS {
			// the last failure aged out of the window: this one starts a fresh count
			fails, window = 0, nowMS
		}
		fails++
		if fails >= 5 {
			locked = true
			if err := q.Run(ctx,
				"UPDATE totp SET fails = 0, window_start = 0, locked_until = ? WHERE id = 1", nowMS+lockMS); err != nil {
				return err
			}
		} else if err := q.Run(ctx,
			"UPDATE totp SET fails = ?, window_start = ? WHERE id = 1", fails, window); err != nil {
			return err
		}
		return auditRow(ctx, q, nowMS, "browser", "totp.failed", "totp")
	})
	return locked, err
}

// TOTPRemove drops the enrolment whole — the secret, the recovery digests, every session a code
// minted and the device rows no passkey ever landed on — with the audit row, in one transaction.
// It answers false when the hub held no enrolment to remove.
func (d *DB) TOTPRemove(ctx context.Context, nowMS int64, actor string) (bool, error) {
	removed := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM totp"); err != nil {
			return err
		}
		gone, err := changesOf(ctx, q)
		if err != nil {
			return err
		}
		removed = gone
		if !gone {
			return nil
		}
		if err := q.Run(ctx, "DELETE FROM totp_recovery"); err != nil {
			return err
		}
		// a code-born row with no passkey can never sign in again: it goes with its enrolment,
		// and its sessions with it (the foreign key cascades)
		if err := q.Run(ctx, "DELETE FROM devices WHERE credential = ?", noCredential); err != nil {
			return err
		}
		if err := q.Run(ctx, "DELETE FROM device_sessions WHERE method = 'totp'"); err != nil {
			return err
		}
		return auditRow(ctx, q, nowMS, actor, "totp.remove", "owner")
	})
	return removed, err
}

// DeviceHasPasskey answers whether a device row holds a WebAuthn credential: the fact that
// separates a browser a code signed in from one that can also touch a passkey.
func (d *DB) DeviceHasPasskey(ctx context.Context, name string) bool {
	row, err := d.st.Get(ctx, "SELECT credential FROM devices WHERE name = ?", name)
	if err != nil || row == nil {
		return false
	}
	return textOfRow(row, "credential") != noCredential
}

// TOTPDigest is a recovery code's stored form: the hex of its sha256 over the code's canonical
// spelling — lower case, no dash, no space — so the form the owner copies and the form they type
// by hand digest alike.
func TOTPDigest(code string) string {
	canonical := strings.ToLower(strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(code))
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}
