package server

// The sessions screen's live tray: the owner asks one session what is running in it and the
// server answers its running subagents and their running background shells, read straight from
// the transcripts the session's own reports named.

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestTheLiveTrayListsASessionsRunningWork(t *testing.T) {
	bdd.Feature(t, "The sessions screen's live tray lists a session's running work", func(f *bdd.F) {
		f.Scenario("the owner asks a session for its live tray", func(s *bdd.S) {
			var (
				srv  *childServer
				code int
				body string
			)
			s.Given("a session with one running subagent and one running shell", func(t *testing.T) {
				// the server and its transcripts outlive this step: they start under
				// the scenario's own T, so their cleanup waits for every step
				srv = startServer(s.T, nil)
				reportConvoSession(t, srv, "s-live", writeLiveSession(s.T))
			})
			s.When("the owner asks for the live tray", func(t *testing.T) {
				code, _, body = srv.get(t, "/api/sessions/s-live/live", nil)
			})
			s.Then("the tray lists that subagent and that shell", func(t *testing.T) {
				if code != 200 {
					t.Fatalf("the live tray answered %d: %s", code, body)
				}
				if !strings.Contains(body, `"id":"b2"`) || !strings.Contains(body, `"description":"Worker B"`) {
					t.Errorf("the tray = %s, want the running subagent b2", body)
				}
				if !strings.Contains(body, `"agent":"main"`) || !strings.Contains(body, `"id":"k7w2n9x4d1"`) {
					t.Errorf("the tray = %s, want the main's running shell", body)
				}
			})
			s.And("the finished worker and its shell stay out", func(t *testing.T) {
				if strings.Contains(body, `"Worker A"`) || strings.Contains(body, `"id":"q5f8t3h6j0"`) {
					t.Errorf("the tray = %s, want nothing of the finished a1", body)
				}
			})
		})
	})
}
