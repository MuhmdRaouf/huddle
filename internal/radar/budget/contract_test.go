package budget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
)

// The contract with the provider plugins' routers that read budget-status.json: the
// document radar publishes and the shape their gate reads. The fixtures live in
// testdata/contract and the cc-plugins repository keeps an identical copy, so every byte
// of a change is reviewed twice.

// contractFixtureDir is the fixtures' directory, relative to this package.
const contractFixtureDir = "../../../testdata/contract"

// contractUpdatedAt is the moment the fixture pins: 2026-10-07T09:00:00.000Z.
const contractUpdatedAt = int64(1791363600000)

// contractStatus is the part of budget-status.json the fixture pins: the version, the
// freshness the routers' staleness gate reads, the scopes they refuse, and each spend
// row's id and share.
type contractStatus struct {
	Version   int             `json:"version"`
	UpdatedAt int64           `json:"updatedAt"`
	Stopped   []string        `json:"stopped"`
	Spend     []contractSpend `json:"spend"`
}

// contractSpend is one spend row as the fixture carries it.
type contractSpend struct {
	ID  string  `json:"id"`
	Pct float64 `json:"pct"`
}

// TestContractBudgetStatusShape publishes the status for the fixture's own providers —
// zai over its month limit and stopped, total under its own and running — and holds the
// written file against the fixture: both decode into the same shape and agree once the
// writer's own timestamp is normalised.
func TestContractBudgetStatusShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(contractFixtureDir, "budget-status.json"))
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	var want contractStatus
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("the fixture does not decode into radar's status shape: %v", err)
	}
	if want.Version != 1 || want.UpdatedAt != contractUpdatedAt || !reflect.DeepEqual(want.Stopped, []string{"provider:zai"}) {
		t.Fatalf("fixture = %+v, want version 1, updatedAt %d, stopped [provider:zai]", want, contractUpdatedAt)
	}
	if len(want.Spend) != 2 ||
		want.Spend[0] != (contractSpend{ID: "zai-month", Pct: 100}) ||
		want.Spend[1] != (contractSpend{ID: "total-month", Pct: 42.5}) {
		t.Fatalf("fixture spend = %+v, want [zai-month 100 total-month 42.5]", want.Spend)
	}

	// A ledger row of a million glm-5.3 input tokens costs $1.40, and both scopes count
	// every priced row: seventeen rows spend $23.80, so a $23.80 zai-month limit reads
	// exactly 100% (stopped) and a $56 total-month limit exactly 42.5% (running).
	rows := make([]cost.LedgerRow, 0, 17)
	for i := 0; i < 17; i++ {
		rows = append(rows, row(fmt.Sprintf("r%d", i), nowMs-1000, "glm-5.3"))
	}
	budgets := []Budget{
		{ID: "zai-month", Scope: "provider:zai", Period: PeriodMonth, LimitUsd: 23.8, Action: ActionStop},
		{ID: "total-month", Scope: "total", Period: PeriodMonth, LimitUsd: 56, Action: ActionStop},
	}
	ctx := context.Background()
	env := makeEnv(t)
	if err := WriteStatus(ctx, env.env, ComputeStatus(budgets, rows, nowMs)); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(BudgetStatusPath(ctx, env.env))
	if err != nil {
		t.Fatal(err)
	}
	var got contractStatus
	if err := json.Unmarshal(published, &got); err != nil {
		t.Fatalf("the published status does not decode into the fixture's shape: %v", err)
	}
	if got.UpdatedAt != nowMs {
		t.Errorf("published updatedAt = %d, want the write's clock %d", got.UpdatedAt, nowMs)
	}
	// UpdatedAt is the one field the writer must own: the time it wrote. Normalise it and
	// the two documents must be the same shape, stop for stop and share for share.
	got.UpdatedAt = want.UpdatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("published status = %+v, want the fixture's shape %+v", got, want)
	}
}

// TestContractFixturesAreTheSharedCopy pins each fixture's SHA-256. The cc-plugins
// repository holds the same table over its own copy of testdata/contract, so a format
// change lands in both repositories' fixtures in one reviewed change or fails here.
func TestContractFixturesAreTheSharedCopy(t *testing.T) {
	fixtures := []struct {
		name string
		sum  string
	}{
		{"budget-status.json", "b67cddb6a1d00261f1969f5b778171cbfa3451193b8a22ae29fe92ea0a424f99"},
		{"spool-health.jsonl", "6bd62fa50864ad11b6b702c84f7f635dd36ed90ed91cb08219e14b3f550e8565"},
		{"spool-route.jsonl", "b8b097274af45726ecbd2a035fccb43520372e4cf450af154bfdc164b1753fc8"},
	}
	for _, fixture := range fixtures {
		data, err := os.ReadFile(filepath.Join(contractFixtureDir, fixture.name))
		if err != nil {
			t.Fatalf("reading testdata/contract/%s: %v", fixture.name, err)
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != fixture.sum {
			t.Errorf("testdata/contract/%s = sha256:%s, want %s; the two repositories' copies must change together",
				fixture.name, got, fixture.sum)
		}
	}
}
