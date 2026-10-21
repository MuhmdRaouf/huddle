package cost

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// The ledger's memory behaviour as a scenario: what a full window of day files — 35 days,
// 4,000 requests a day — leaves in the heap once the dashboard has read its rows.

func TestLedgerMemoryScenarios(t *testing.T) {
	bdd.Feature(t, "the hub stays light while the cost ledger holds 35 days", func(f *bdd.F) {
		f.Scenario("a full 35-day ledger of 140,000 rows stays under a fixed heap bound", func(s *bdd.S) {
			const (
				days    = 35
				rowsDay = 4_000
				total   = days * rowsDay
				now     = int64(1_800_000_000_000) // the fixed clock the whole scenario runs on
			)
			var (
				e             ledgerEnv
				ledger        *Ledger // scenario-scoped: the ledger must stay reachable while Then measures
				before, after runtime.MemStats
			)
			s.Given("35 day files of 4,000 rows over 300 sessions, 4 agents and 3 models, every 3rd row priced", func(t *testing.T) {
				e = makeLedgerEnv(s.T) // the temp home must outlive the Given step
				models := []string{"claude-sonnet-5-5", "zai/glm-5.3", "kimi/kimi-k3"}
				agents := []string{"main", "build", "explore", "review"}
				session := func(n int) string { return fmt.Sprintf("550e8400-e29b-41d4-a716-%012d", n) }
				for day := range days {
					var file strings.Builder
					file.WriteString(`{"version":1,"rows":{`)
					for n := range rowsDay {
						if n > 0 {
							file.WriteByte(',')
						}
						id := fmt.Sprintf("req-%02d-%04d", day, n)
						ts := now - int64(day)*dayMs + int64(n)*1_000
						base := fmt.Sprintf("%q:[%d,%q,%q,%q,%d,%d,0,0", id, ts, models[n%3],
							session(n%300), agents[n%4], 1_000+n, 10+n%97)
						if n%3 == 0 {
							file.WriteString(base + `,null,"fast","us","priority"]`)
						} else {
							file.WriteString(base + `,null,null,null,null]`)
						}
					}
					file.WriteString(`},"sessions":{`)
					for n := range 300 {
						if n > 0 {
							file.WriteByte(',')
						}
						file.WriteString(fmt.Sprintf(
							`%q:{"project":"proj-a","title":"Session %03d","repo":"/w/repo","parentSessionId":null,"name":null}`,
							session(n), n))
					}
					file.WriteString(`},"agents":{`)
					for n, agent := range agents {
						if n > 0 {
							file.WriteByte(',')
						}
						file.WriteString(fmt.Sprintf(
							`"550e8400-e29b-41d4-a716-000000000000:%s":{"name":%q,"type":"Explore","description":"does its part"}`,
							agent, agent))
					}
					file.WriteString(`}}`)
					key := DayKey(now - int64(day)*dayMs)
					writeText(t, filepath.Join(LedgerDir(t.Context(), e.env), key+".json"), file.String())
				}
			})
			s.When("the ledger loads the whole window and the views read its rows", func(t *testing.T) {
				runtime.GC() // a clean baseline: the Given step's build garbage must not count
				runtime.ReadMemStats(&before)
				ledger = NewLedger(t.Context(), e.env, atMs(now))
				rows := ledger.Rows(0)
				if len(rows) != total {
					t.Fatalf("rows = %d, want %d", len(rows), total)
				}
			})
			s.And("every row really made it in, names included", func(t *testing.T) {
				names := ledger.Names()
				if len(names.Sessions) != 300 || len(names.Agents) != 4 {
					t.Fatalf("names: got %d sessions and %d agents, want 300 and 4",
						len(names.Sessions), len(names.Agents))
				}
			})
			s.Then("the heap grew under a fixed bound", func(t *testing.T) {
				runtime.GC()
				runtime.ReadMemStats(&after)
				ledger.Version() // measured; the ledger must stay live until here, not till its last use in When
				// halfway between the 96 MiB the pre-intern ledger measured and the 83 MiB the
				// interned one does: a regression to per-row string copies trips the bound.
				limit := uint64(89 << 20)
				if testkit.RaceEnabled {
					// the detector's own allocations ride along, so the bound scales and still pins
					limit *= 4
				}
				grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
				t.Logf("a 140,000-row ledger grew the heap %d MiB of the %d MiB bound", grew>>20, limit>>20)
				if grew >= int64(limit) {
					t.Fatalf("a 140,000-row ledger grew the heap %d MiB, want under %d MiB", grew>>20, limit>>20)
				}
			})
		})
	})
}
