package hub

// Satellite credential storage: a hub mints one credential per satellite machine and
// keeps only its sha256 digest, the same shape a pairing token takes, so a leaked database names
// no live credential. The digest has no expiry of its own: the owner revokes a satellite by
// removing its row, the way a device's removal ends its sessions.

import (
	"context"
	"fmt"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// SaveSatellite records one satellite credential's digest with the machine it belongs to, and
// writes the audit row and the alert event a satellite added owes.
func (d *DB) SaveSatellite(ctx context.Context, digest, name string, nowMS int64) error {
	return d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx,
			"INSERT OR REPLACE INTO satellites (digest, name, added, seen) VALUES (?, ?, ?, ?)",
			digest, name, nowMS, nowMS); err != nil {
			return err
		}
		if err := auditRow(ctx, q, nowMS, "terminal", "satellite", name); err != nil {
			return err
		}
		body, err := satelliteAlertBody(name, nowMS)
		if err != nil {
			return err
		}
		return q.Run(ctx, "INSERT INTO alerts (at, kind, json) VALUES (?, ?, ?)",
			nowMS, "alert.satellite_added", body)
	})
}

// SatelliteSession resolves a credential's digest to its machine, and refreshes the machine's
// last-seen moment when it is a minute old or older — a read per request, a write at most once a
// minute, the same bound a device's seen carries.
func (d *DB) SatelliteSession(ctx context.Context, digest string, nowMS int64) (string, bool, error) {
	name, live := "", false
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		row, err := q.Get(ctx, "SELECT name FROM satellites WHERE digest = ?", digest)
		if err != nil || row == nil {
			return err
		}
		name, _ = row["name"].(string)
		live = true
		return q.Run(ctx, "UPDATE satellites SET seen = ? WHERE name = ? AND seen <= ?",
			nowMS, name, nowMS-deviceSeenWindow)
	})
	return name, live, err
}

// satelliteAlertBody renders the alert event's body for a satellite added.
func satelliteAlertBody(name string, nowMS int64) (string, error) {
	body, err := domain.MarshalCompactJSON(struct {
		Machine string `json:"machine"`
		At      string `json:"at"`
	}{name, time.UnixMilli(nowMS).UTC().Format("2006-01-02T15:04:05.000Z")})
	if err != nil {
		return "", fmt.Errorf("encode the satellite alert: %w", err)
	}
	return string(body), nil
}
