package hub

// Satellite credentials: a credential's sha256 digest is the only thing the database
// holds, so a leaked file names no live credential; resolving one answers the machine it belongs
// to while the row lives, and minting writes the audit row and the alert event a satellite
// added owes.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"
)

// satelliteDB opens one hub database in a temp home.
func satelliteDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// digestOf renders a credential's stored form: the hex of its sha256, the same digest the auth
// package's pairing tokens take.
func digestOf(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:])
}

// satelliteCredential draws one credential the shape a minted one takes.
func satelliteCredential(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("draw: %v", err)
	}
	return "hsat_" + base64.RawURLEncoding.EncodeToString(raw)
}

// TestSatelliteSessionResolvesTheDigest proves the two halves every request runs: a saved digest
// resolves to its machine while it lives, and nothing else does.
func TestSatelliteSessionResolvesTheDigest(t *testing.T) {
	db := satelliteDB(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	credential := satelliteCredential(t)

	if name, ok, err := db.SatelliteSession(ctx, digestOf(credential), now); err != nil || ok || name != "" {
		t.Fatalf("an unsaved credential answered (%q, %v, %v), want empty and false", name, ok, err)
	}
	if err := db.SaveSatellite(ctx, digestOf(credential), "mbp", now); err != nil {
		t.Fatalf("save: %v", err)
	}
	name, ok, err := db.SatelliteSession(ctx, digestOf(credential), now)
	if err != nil || !ok || name != "mbp" {
		t.Fatalf("the saved credential answered (%q, %v, %v), want mbp and true", name, ok, err)
	}
	second := satelliteCredential(t)
	if err := db.SaveSatellite(ctx, digestOf(second), "studio", now); err != nil {
		t.Fatalf("save a second: %v", err)
	}
	if name, ok, _ := db.SatelliteSession(ctx, digestOf(second), now+1000); !ok || name != "studio" {
		t.Fatalf("the second credential answered (%q, %v), want studio and true", name, ok)
	}
}

// TestSaveSatelliteWritesAuditAndAlert pins what minting owes: one audit row naming the
// machine, one alert event for the owner's pushed alerts.
func TestSaveSatelliteWritesAuditAndAlert(t *testing.T) {
	db := satelliteDB(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	if err := db.SaveSatellite(ctx, digestOf(satelliteCredential(t)), "mbp", now); err != nil {
		t.Fatalf("save: %v", err)
	}
	row, err := db.st.Get(ctx, "SELECT actor, action, target FROM audit ORDER BY id DESC LIMIT 1")
	if err != nil || row == nil {
		t.Fatalf("no audit row: %v", err)
	}
	if row["action"] != "satellite" || row["target"] != "mbp" {
		t.Fatalf("the audit row = %v, want the satellite mbp", row)
	}
	row, err = db.st.Get(ctx, "SELECT kind FROM alerts ORDER BY id DESC LIMIT 1")
	if err != nil || row == nil {
		t.Fatalf("no alert row: %v", err)
	}
	if row["kind"] != "alert.satellite_added" {
		t.Fatalf("the alert kind = %v, want alert.satellite_added", row)
	}
}
