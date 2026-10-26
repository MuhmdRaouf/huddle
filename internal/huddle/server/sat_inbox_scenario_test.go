package server

// The owner's words to a session on a satellite reach the session at once: the machine's own
// server posts them through the session's inbox socket, the way the hub's own server does for a
// session on the hub's machine.

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestAnOwnersMessageReachesASatelliteSessionAtOnce(t *testing.T) {
	bdd.Feature(t, "An owner's message reaches a session on a satellite at once", func(f *bdd.F) {
		f.Scenario("a send through the hub is posted through the session's inbox socket", func(s *bdd.S) {
			var (
				rig    *satelliteInboxRig
				status int
				body   string
			)
			s.Given("a hub paired with a satellite whose session listens on an inbox socket", func(t *testing.T) {
				// both servers outlive this step: the rig starts under the scenario's own T,
				// so its cleanup waits for every step, not this one's end
				rig = startSatelliteInboxRig(s.T)
			})
			s.When("the owner sends the session a message through the hub", func(t *testing.T) {
				status, body = call(t, rig.hubBase, http.MethodPost, "/api/send", "hub-token", "",
					`{"sessions":["s-1"],"text":"ship it today"}`)
			})
			s.Then("the send answers the satellite route", func(t *testing.T) {
				if status != 200 || !strings.Contains(body, `"route":"satellite"`) {
					t.Fatalf("the send answered %d: %s", status, body)
				}
			})
			s.And("the session's socket carries the owner's words with no prompt", func(t *testing.T) {
				var raw []byte
				select {
				case raw = <-rig.lines:
				case <-time.After(10 * time.Second):
					t.Fatalf("the session's socket never heard the owner: %s / %s",
						rig.hubLog.joined(), rig.sat.log.joined())
				}
				line := inboxOwnerLine(t, raw)
				if line.Type != "user" || line.SessionID != "s-1" ||
					line.Message.Role != "user" || !strings.Contains(line.Message.Content, "ship it today") {
					t.Fatalf("the socket carried %+v, want the owner's words for s-1", line)
				}
			})
		})
	})
}
