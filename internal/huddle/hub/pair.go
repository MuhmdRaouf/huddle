package hub

// Device pairing storage: the pairing table holds sha256 digests only — a token
// never lands in the database, so a leaked file names no live link. Every finish runs in one
// transaction that consumes the pairing with the device it pays for, so a link registers one
// device however two requests race, and removing a device drops its sessions with it (the
// foreign key cascades), which is what makes a revocation immediate.

import (
	"context"
	"fmt"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// Device is one paired browser as the database holds it: the name the owner calls it, the
// WebAuthn user handle its passkey answers to, the credential record as JSON (the public key,
// the signature counter, the flags), and the moments it was added and last seen.
type Device struct {
	Name           string
	Handle         []byte
	CredentialJSON []byte
	AddedMS        int64
	SeenMS         int64
}

// PairingGoneError names a pairing that is no longer there to spend: used, expired or swept.
type PairingGoneError struct{}

func (PairingGoneError) Error() string { return "the pairing link is gone" }

// deviceSeenWindow is how long a device's last-seen moment may stay stale before a request
// writes it again: seeing a device is a read per request, a write at most once a minute.
const deviceSeenWindow int64 = 60_000

// SavePairing records one pairing token's digest with its expiry, sweeping the expired ones so
// the table holds only what a browser could still open.
func (d *DB) SavePairing(ctx context.Context, digest string, expiresMS int64) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM pairings WHERE expires <= ?", expiresMS); err != nil {
			return err
		}
		return q.Run(ctx, "INSERT OR REPLACE INTO pairings (digest, expires) VALUES (?, ?)", digest, expiresMS)
	})
}

// PairingLive answers whether an unexpired pairing holds this digest.
func (d *DB) PairingLive(ctx context.Context, digest string, nowMS int64) bool {
	row, err := d.st.Get(ctx, "SELECT 1 FROM pairings WHERE digest = ? AND expires > ?", digest, nowMS)
	return err == nil && row != nil
}

// PairFinish consumes one pairing and records the device it pays for with the session the
// browser earned, the audit row and the alert event, in one transaction. A pairing already
// spent answers PairingGoneError and nothing is written. The session is born stepped up: the
// ceremony's user verification is itself an assertion.
func (d *DB) PairFinish(ctx context.Context, digest string, dev Device, sessionDigest string,
	sessionExpiresMS, nowMS int64) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM pairings WHERE digest = ? AND expires > ?", digest, nowMS); err != nil {
			return err
		}
		spent, err := changesOf(ctx, q)
		if err != nil {
			return err
		}
		if !spent {
			return PairingGoneError{}
		}
		if err := q.Run(ctx,
			"INSERT INTO devices (name, handle, credential, added, seen) VALUES (?, ?, ?, ?, ?)",
			dev.Name, dev.Handle, string(dev.CredentialJSON), nowMS, nowMS); err != nil {
			return err
		}
		if err := q.Run(ctx,
			"INSERT INTO device_sessions (digest, device, expires, stepup_at, renewed) VALUES (?, ?, ?, ?, ?)",
			sessionDigest, dev.Name, sessionExpiresMS, nowMS, nowMS); err != nil {
			return err
		}
		if err := auditRow(ctx, q, nowMS, dev.Name, "pair", dev.Name); err != nil {
			return err
		}
		body, err := pairAlertBody(dev.Name, nowMS)
		if err != nil {
			return err
		}
		return q.Run(ctx, "INSERT INTO alerts (at, kind, json) VALUES (?, ?, ?)",
			nowMS, "alert.device_paired", body)
	})
}

// LoginFinish records a device's sign-in: the credential's new signature counter (a cloned
// authenticator's stale counter is what betrays it next time) and the session it earned, with
// the audit row. The sign-in is an assertion, so the session starts stepped up.
func (d *DB) LoginFinish(ctx context.Context, dev Device, sessionDigest string, sessionExpiresMS, nowMS int64) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "UPDATE devices SET credential = ?, seen = ? WHERE name = ?",
			string(dev.CredentialJSON), nowMS, dev.Name); err != nil {
			return err
		}
		if err := q.Run(ctx,
			"INSERT OR REPLACE INTO device_sessions (digest, device, expires, stepup_at, renewed) VALUES (?, ?, ?, ?, ?)",
			sessionDigest, dev.Name, sessionExpiresMS, nowMS, nowMS); err != nil {
			return err
		}
		return auditRow(ctx, q, nowMS, dev.Name, "signin", dev.Name)
	})
}

// Devices lists the paired devices, the oldest first.
func (d *DB) Devices(ctx context.Context) ([]Device, error) {
	rows, err := d.st.All(ctx, "SELECT name, handle, credential, added, seen FROM devices ORDER BY added, name")
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(rows))
	for _, row := range rows {
		out = append(out, deviceOf(row))
	}
	return out, nil
}

// DeviceByHandle finds the device a WebAuthn user handle names.
func (d *DB) DeviceByHandle(ctx context.Context, handle []byte) (Device, bool, error) {
	row, err := d.st.Get(ctx, "SELECT name, handle, credential, added, seen FROM devices WHERE handle = ?", handle)
	if err != nil || row == nil {
		return Device{}, false, err
	}
	return deviceOf(row), true, nil
}

// DeviceByName finds a device by the name the owner calls it.
func (d *DB) DeviceByName(ctx context.Context, name string) (Device, bool, error) {
	row, err := d.st.Get(ctx, "SELECT name, handle, credential, added, seen FROM devices WHERE name = ?", name)
	if err != nil || row == nil {
		return Device{}, false, err
	}
	return deviceOf(row), true, nil
}

// DeviceRemove drops a device and every session of it, with the audit row. The removal is what
// a revocation is: the next request with one of its cookies finds no session and is refused.
func (d *DB) DeviceRemove(ctx context.Context, name string, nowMS int64) (bool, error) {
	removed := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM devices WHERE name = ?", name); err != nil {
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
		return auditRow(ctx, q, nowMS, "terminal", "revoke", name)
	})
	return removed, err
}

// SessionRemove ends one device session by its digest and answers whether a session went; the
// device itself and its other sessions stay.
func (d *DB) SessionRemove(ctx context.Context, digest string) (bool, error) {
	removed := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM device_sessions WHERE digest = ?", digest); err != nil {
			return err
		}
		gone, err := changesOf(ctx, q)
		if err != nil {
			return err
		}
		removed = gone
		return nil
	})
	return removed, err
}

// SessionsRemoveAll ends every device session and answers how many went; every paired device
// keeps its row, its passkey credential and any TOTP enrolment.
func (d *DB) SessionsRemoveAll(ctx context.Context) (int64, error) {
	gone := int64(0)
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "DELETE FROM device_sessions"); err != nil {
			return err
		}
		row, err := q.Get(ctx, "SELECT changes() AS n")
		if err != nil {
			return err
		}
		gone, _ = row["n"].(int64)
		return nil
	})
	return gone, err
}

// DeviceSession resolves a session digest to its device while the session lives, and refreshes
// the device's last-seen moment when it is a minute old or older. The session table is keyed by
// the digest, so a removed device's sessions are already gone with it.
func (d *DB) DeviceSession(ctx context.Context, digest string, nowMS int64) (string, bool, error) {
	device, live := "", false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		row, err := q.Get(ctx,
			"SELECT device FROM device_sessions WHERE digest = ? AND expires > ?", digest, nowMS)
		if err != nil || row == nil {
			return err
		}
		device, _ = row["device"].(string)
		live = true
		return q.Run(ctx, "UPDATE devices SET seen = ? WHERE name = ? AND seen <= ?",
			nowMS, device, nowMS-deviceSeenWindow)
	})
	return device, live, err
}

// SessionExpired answers whether a session digest is still known but no longer live: its row
// holds an expiry that has passed. A digest no row holds (a removed device's sessions went with
// the device) answers false, the same as a live one — the live check stays the caller's.
func (d *DB) SessionExpired(ctx context.Context, digest string, nowMS int64) (bool, error) {
	row, err := d.st.Get(ctx,
		"SELECT 1 FROM device_sessions WHERE digest = ? AND expires <= ?", digest, nowMS)
	if err != nil || row == nil {
		return false, err
	}
	return true, nil
}

// SessionRenew rolls one session's expiry forward when its holder keeps using it, at most once
// per renewal window: the write lands only on a live session whose last renewal is older than
// renewEveryMS (a NULL renewal moment — a session an older build granted — always reads due) and
// only when the new expiry is later than the one it holds. A removed device's sessions are
// already gone with it, so there is nothing to write.
func (d *DB) SessionRenew(ctx context.Context, digest string, nowMS, renewEveryMS, expiresMS int64) (bool, error) {
	renewed := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx,
			"UPDATE device_sessions SET expires = ?, renewed = ? WHERE digest = ? AND expires > ? AND expires < ? AND (renewed IS NULL OR renewed < ?)",
			expiresMS, nowMS, digest, nowMS, expiresMS, nowMS-renewEveryMS); err != nil {
			return err
		}
		changed, err := changesOf(ctx, q)
		if err != nil {
			return err
		}
		renewed = changed
		return nil
	})
	return renewed, err
}

// SessionSteppedUp answers whether a live device session holds an assertion at most one window
// old: the coverage a dangerous act rides on. The window comes from the caller, so
// the edges stay testable; a session that never asserted, and a digest no live session answers,
// read uncovered.
func (d *DB) SessionSteppedUp(ctx context.Context, digest string, nowMS, windowMS int64) (bool, error) {
	row, err := d.st.Get(ctx,
		"SELECT stepup_at FROM device_sessions WHERE digest = ? AND expires > ?", digest, nowMS)
	if err != nil || row == nil {
		return false, err
	}
	at, _ := row["stepup_at"].(int64)
	return at > 0 && nowMS-at <= windowMS, nil
}

// StepUpFinish records one step-up: the credential's fresh signature counter (the same
// clone-tripwire a sign-in keeps) and the session's new assertion moment, in one transaction.
// A session that died between the check and the write answers false and nothing is written.
func (d *DB) StepUpFinish(ctx context.Context, digest string, dev Device, atMS int64) (bool, error) {
	stamped := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "UPDATE devices SET credential = ?, seen = ? WHERE name = ?",
			string(dev.CredentialJSON), atMS, dev.Name); err != nil {
			return err
		}
		if err := q.Run(ctx,
			"UPDATE device_sessions SET stepup_at = ? WHERE digest = ? AND expires > ?",
			atMS, digest, atMS); err != nil {
			return err
		}
		changed, err := changesOf(ctx, q)
		if err != nil {
			return err
		}
		stamped = changed
		return nil
	})
	return stamped, err
}

// AnyDevice answers whether any device is paired: the fact that ends the solo waiver —
// while the home holds no passkey, the local sign-in stands as the assertion.
func (d *DB) AnyDevice(ctx context.Context) (bool, error) {
	row, err := d.st.Get(ctx, "SELECT 1 FROM devices LIMIT 1")
	return err == nil && row != nil, err
}

// PasskeyFinish lands one registration a signed-in session ran on its own device row: the
// credential the browser made, with the audit row, in one transaction. The row keeps its name
// and its handle, so its sessions live on. It answers false when the device is gone (removed
// between the ceremony's two halves), and nothing is written.
func (d *DB) PasskeyFinish(ctx context.Context, name string, credentialJSON []byte, nowMS int64) (bool, error) {
	landed := false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx, "UPDATE devices SET credential = ?, seen = ? WHERE name = ?",
			string(credentialJSON), nowMS, name); err != nil {
			return err
		}
		changed, err := changesOf(ctx, q)
		if err != nil {
			return err
		}
		landed = changed
		if !changed {
			return nil
		}
		return auditRow(ctx, q, nowMS, name, "passkey.add", name)
	})
	return landed, err
}

// changesOf answers how many rows the transaction's last statement changed, the way SQLite
// itself counts: a consume that changed nothing is a pairing already spent.
func changesOf(ctx context.Context, q store.Q) (bool, error) {
	row, err := q.Get(ctx, "SELECT changes() AS n")
	if err != nil {
		return false, err
	}
	n, _ := row["n"].(int64)
	return n > 0, nil
}

// auditRow writes one audit row; the app never deletes one.
func auditRow(ctx context.Context, q store.Q, atMS int64, actor, action, target string) error {
	return q.Run(ctx, "INSERT INTO audit (at, actor, action, target) VALUES (?, ?, ?, ?)",
		atMS, actor, action, target)
}

// deviceOf reads one devices row into its struct shape.
func deviceOf(row store.Row) Device {
	handle, _ := row["handle"].([]byte)
	credential, _ := row["credential"].(string)
	added, _ := row["added"].(int64)
	seen, _ := row["seen"].(int64)
	name, _ := row["name"].(string)
	return Device{
		Name: name, Handle: handle, CredentialJSON: []byte(credential), AddedMS: added, SeenMS: seen,
	}
}

// pairAlertBody renders the alert event's body for a newly paired device.
func pairAlertBody(device string, nowMS int64) (string, error) {
	body, err := domain.MarshalCompactJSON(struct {
		Device string `json:"device"`
		At     string `json:"at"`
	}{device, time.UnixMilli(nowMS).UTC().Format("2006-01-02T15:04:05.000Z")})
	if err != nil {
		return "", fmt.Errorf("encode the pairing alert: %w", err)
	}
	return string(body), nil
}
