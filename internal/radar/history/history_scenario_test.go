package history

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// The history's memory behaviour as scenarios: what the hub's process lives through while
// radar catches up on a big transcript directory.

func TestTheHubStaysLightWhileRadarCatchesUpScenarios(t *testing.T) {
	bdd.Feature(t, "the hub stays light while radar catches up", func(f *bdd.F) {
		f.Scenario("a thousand stored sides keep the store's allocation bounded", func(s *bdd.S) {
			var (
				h             *History
				side          string
				before, after runtime.MemStats
			)
			s.Given("a store holding 1,000 requests and one 4 KiB side to store for each", func(t *testing.T) {
				h = openHistoryAt(s.T)
				side = strings.Repeat("huddle ", 585)
				if err := h.UpsertNode(nodeUpsert()); err != nil {
					t.Fatalf("upsert the node: %v", err)
				}
				for i := 0; i < 1_000; i++ {
					r := requestRecord()
					r.ID = fmt.Sprintf("req-%04d", i)
					if err := h.PutRequest("s1/main", r); err != nil {
						t.Fatalf("put %s: %v", r.ID, err)
					}
				}
			})
			s.When("the store takes 1,000 sides", func(t *testing.T) {
				runtime.ReadMemStats(&before)
				for i := 0; i < 1_000; i++ {
					id := fmt.Sprintf("req-%04d", i)
					if err := h.PutContent(id, ContentSides{Input: &side}); err != nil {
						t.Fatalf("put %s: %v", id, err)
					}
				}
				runtime.ReadMemStats(&after)
			})
			s.Then("the whole catch-up allocated under 64 MiB", func(t *testing.T) {
				limit := uint64(64 << 20)
				if testkit.RaceEnabled {
					// the detector's own allocations ride along (about 261 MiB here against
					// about 1 GiB with a writer per side), so the bound scales and still pins
					limit = 768 << 20
				}
				grew := after.TotalAlloc - before.TotalAlloc
				if grew >= limit {
					t.Fatalf("1,000 stored sides allocated %d MiB; want under %d MiB", grew>>20, limit>>20)
				}
			})
			s.And("the last side reads back whole", func(t *testing.T) {
				got, err := h.Content("req-0999")
				if err != nil {
					t.Fatalf("read the stored side: %v", err)
				}
				if got == nil || got.Input == nil || *got.Input != side {
					t.Fatal("the stored side did not read back whole")
				}
			})
		})
	})
}
