package server

// The guard for ADR-5's rule that no credential reaches a store in clear text: a route line
// whose headers carry a bearer token and a cookie files its ledger facts only after the
// allow-list runs again, so the raw bytes of ledger.db never hold either.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/MuhmdRaouf/huddle/internal/radar/ledger"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// The fakes the guard hunts for: obvious non-secrets, written here so no real credential
// ever takes part.
const (
	guardRequestID = "req_secret_guard"
	guardBearer    = "sk-guard-0000-not-real"
	guardCookie    = "session=guard-cookie-not-real"
)

// guardLedgerHolds says whether ledger_requests holds the row the request id names.
func guardLedgerHolds(file, id string) bool {
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		return false
	}
	defer func() { _ = db.Close() }()
	var n int
	return db.QueryRow("SELECT COUNT(*) FROM ledger_requests WHERE id = ?", id).Scan(&n) == nil && n > 0
}

// TestASecretHeaderNeverReachesTheLedger: one route line names a request id beside an
// authorization header and a cookie; the request lands in ledger.db and neither fake secret
// appears in the raw bytes of the database file or the -wal and -shm files beside it.
func TestASecretHeaderNeverReachesTheLedger(t *testing.T) {
	world := makeEnv(t)
	ctx := t.Context()
	route := fmt.Sprintf(`{"ts":%q,"event":"route","session_id":"s1","model":"glm-4.6",`+
		`"upstream":"https://api.z.ai","route":"provider","status":200,"latency_ms":42,`+
		`"headers":{"request-id":%q,"authorization":"Bearer %s","cookie":%q}}`,
		isoOf(time.Now().UnixMilli()), guardRequestID, guardBearer, guardCookie)
	seedSpool(t, world.state, spoolStart("s1", "/w/app"), route)

	ledgerPath := paths.LedgerPath(ctx, world.env)
	app := mustStart(t, world.env, AppOptions{Version: "test", SinceMs: dayMs, IntervalMs: quietMs})
	waitFor(t, fmt.Sprintf("the %s row in ledger.db", guardRequestID), 10*time.Second, func() bool {
		return guardLedgerHolds(ledgerPath, guardRequestID)
	})
	app.Stop()

	base := filepath.Base(ledgerPath)
	for _, name := range []string{base, base + "-wal", base + "-shm"} {
		body, err := os.ReadFile(filepath.Join(filepath.Dir(ledgerPath), name))
		if err != nil {
			if os.IsNotExist(err) {
				continue // a closed database leaves no -wal or -shm behind
			}
			t.Fatalf("read %s: %v", name, err)
		}
		for _, secret := range []string{guardBearer, guardCookie} {
			if strings.Contains(string(body), secret) {
				t.Errorf("%s holds %q in clear text", name, secret)
			}
		}
	}

	// the row exists, so the guard cannot pass by ingesting nothing
	led, err := ledger.Open(ledgerPath)
	if err != nil {
		t.Fatalf("reopen the ledger: %v", err)
	}
	t.Cleanup(func() { _ = led.Close() })
	if row, err := led.Get(ctx, guardRequestID); err != nil || row == nil {
		t.Fatalf("the ledger holds no row for %s: %+v, %v", guardRequestID, row, err)
	}
}
