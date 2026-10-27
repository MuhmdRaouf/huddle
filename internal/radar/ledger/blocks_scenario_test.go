package ledger

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// The body store's behaviour scenarios, run against a router.db in a temp directory only.

// TestBodyStoreScenarios pins the store's reason to exist: a conversation that resends its
// whole history every request is stored once, not fifty times.
func TestBodyStoreScenarios(t *testing.T) {
	t.Parallel()
	bdd.Feature(t, "A repeated block is stored once", func(f *bdd.F) {
		f.Scenario("50 requests that each resend the conversation so far", func(s *bdd.S) {
			const requests = 50
			const messageSize = 2048
			const systemSize = 40 * 1024

			ctx := context.Background()
			at := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
			dir := filepath.Join(s.T.TempDir(), "ledger")
			st, err := OpenBodies(filepath.Join(dir, "router.db"), testBodyKey)
			if err != nil {
				s.T.Fatalf("open the body store: %v", err)
			}
			s.T.Cleanup(func() { _ = st.Close() })

			system := strings.Repeat("rule ", systemSize/5)
			var bodies [][]byte
			var totalRaw int
			var conversation []string

			s.Given("50 requests that each add one 2 KB message under the same 40 KB system prompt", func(t *testing.T) {
				for turn := 1; turn <= requests; turn++ {
					prefix := fmt.Sprintf("turn %03d says: ", turn)
					conversation = append(conversation, prefix+strings.Repeat("x", messageSize-len(prefix)))
					body := scenarioBody(system, conversation)
					bodies = append(bodies, body)
					totalRaw += len(body)
				}
				if totalRaw == 0 {
					t.Fatal("no bodies were built")
				}
			})
			s.When("every request's body is put", func(t *testing.T) {
				for turn, body := range bodies {
					id := fmt.Sprintf("req_%03d", turn+1)
					if err := st.PutBody(ctx, id, "input", body, at); err != nil {
						t.Fatalf("put %s: %v", id, err)
					}
				}
			})
			s.Then("the segments on disk stay under a tenth of the bodies' raw bytes", func(t *testing.T) {
				onDisk := segmentBytes(t, dir)
				if onDisk == 0 {
					t.Fatal("no segment bytes were written")
				}
				if onDisk >= int64(totalRaw)/10 {
					t.Fatalf("%d bytes on disk is not under a tenth of the %d raw bytes",
						onDisk, totalRaw)
				}
				t.Logf("%d bytes on disk against %d raw bytes (%.1fx smaller)", onDisk, totalRaw,
					float64(totalRaw)/float64(onDisk))
			})
			s.And("request 50 reads back byte for byte", func(t *testing.T) {
				got, ok, err := st.Body(ctx, "req_050", "input")
				if err != nil {
					t.Fatalf("body of request 50: %v", err)
				}
				if !ok {
					t.Fatal("request 50 is not in the store")
				}
				if !bytes.Equal(got, bodies[requests-1]) {
					t.Fatalf("request 50 read back %d bytes, want its exact %d bytes",
						len(got), len(bodies[requests-1]))
				}
			})
		})
	})
}

// scenarioBody builds one chat body: the shared system prompt and the conversation so
// far, each message taken verbatim.
func scenarioBody(system string, messages []string) []byte {
	var body strings.Builder
	body.WriteString(`{"model":"m","system":` + jsonString(system) + `,"messages":[`)
	for at, message := range messages {
		if at > 0 {
			body.WriteString(",")
		}
		body.WriteString(`{"role":"user","content":` + jsonString(message) + `}`)
	}
	body.WriteString(`],"stream":true}`)
	return []byte(body.String())
}
