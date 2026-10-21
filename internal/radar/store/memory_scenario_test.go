package store

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/testkit"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// The store's memory behaviour as scenarios: what the hub's process holds while radar
// keeps every request and tool call the dashboard reads.

func TestTheStoreStaysLightWhileRadarHoldsItsRecordsScenarios(t *testing.T) {
	bdd.Feature(t, "the hub stays light while radar holds its records", func(f *bdd.F) {
		f.Scenario("24,000 requests of 2 KiB each keep the retained heap bounded", func(s *bdd.S) {
			const (
				sessions    = 200
				agentsPer   = 3
				requestsPer = 40
				toolsPer    = 20
				whatBytes   = 2 << 10
				// halfway between the 102 MiB the copies retained and the 68 MiB the
				// pointers retain; the detector's own allocations ride along under -race,
				// so the bound scales there and still pins
				boundMB   = 85
				raceScale = 4
			)
			var (
				st            *Store
				what          string
				before, after runtime.MemStats
			)
			s.Given("a collected baseline from before the store exists", func(t *testing.T) {
				runtime.GC()
				runtime.ReadMemStats(&before)
			})
			s.When("the store takes 200 sessions of 3 agents, 40 requests and 20 tools each", func(t *testing.T) {
				what = strings.Repeat("huddle ", whatBytes/7)
				st = newTestStore()
				now := ts0
				for session := 0; session < sessions; session++ {
					id := fmt.Sprintf("s%03d", session)
					for agent := 0; agent < agentsPer; agent++ {
						agentID := "main"
						if agent > 0 {
							agentID = fmt.Sprintf("sub-%d", agent)
						}
						for call := 0; call < requestsPer; call++ {
							now++
							st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
								r.ID = fmt.Sprintf("%s-%s-%03d", id, agentID, call)
								r.SessionID = id
								r.AgentID = agentID
								r.Ts = now
								// every request its own 2 KiB line, the way a captured prompt rides
								line := fmt.Sprintf("%s %s-%s-%03d", what, id, agentID, call)
								r.What = &line
							}), nil)
						}
						for call := 0; call < toolsPer; call++ {
							now++
							st.AddToolCall(makeTool(func(tool *shared.ToolCallRecord) {
								tool.ID = fmt.Sprintf("%s-%s-tool-%03d", id, agentID, call)
								tool.SessionID = id
								tool.AgentID = str(agentID)
								tool.StartedAt = now
							}))
						}
					}
				}
			})
			s.Then("every session's detail and one newest-first page read back", func(t *testing.T) {
				for session := 0; session < sessions; session++ {
					if st.SessionDetail(fmt.Sprintf("s%03d", session)) == nil {
						t.Fatalf("no detail for s%03d", session)
					}
				}
				if got := st.Requests(RequestFilter{}); len(got) != defaultPage {
					t.Fatalf("the newest-first page holds %d records, want %d", len(got), defaultPage)
				}
			})
			s.And("the retained heap stays under the bound", func(t *testing.T) {
				limit := uint64(boundMB) << 20
				if testkit.RaceEnabled {
					limit *= raceScale
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				grew := after.HeapAlloc - before.HeapAlloc
				t.Logf("retained heap growth: %d MiB (bound %d MiB)", grew>>20, limit>>20)
				if grew >= limit {
					t.Fatalf("24,000 requests of 2 KiB grew the retained heap %d MiB; want under %d MiB",
						grew>>20, limit>>20)
				}
				runtime.KeepAlive(st)
				runtime.KeepAlive(what)
			})
		})
	})
}
