package server

// The session's tasks view shows the kanban cards the session works on: the cards of the
// board at the session's working directory whose holder is the session's member.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/kanbantab"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestASessionsTasksShowTheCardsItHolds(t *testing.T) {
	bdd.Feature(t, "a session's tasks show the cards it holds", func(f *bdd.F) {
		f.Scenario("only the card the member holds in progress answers", func(s *bdd.S) {
			var (
				srv  *childServer
				repo string
				code int
				body string
			)
			s.Given("a session in a channel, working in a repo whose board holds three cards — "+
				"one held by the member in progress, one held by another actor, one held by the member in done",
				func(t *testing.T) {
					// the server outlives this step: it starts under the scenario's own T, so
					// its cleanup waits for every step, not this one's end
					srv = startServer(s.T, nil)
					if code, out := srv.post(t, "/api/channels", `{"name":"build"}`, nil); code != 200 {
						t.Fatalf("create the channel: %d %s", code, out)
					}
					srv.op(t, "build", "join", "api", "{}")
					// the repo lives in the scenario's own temp dir: a step's temp dir is
					// deleted when the step ends, before the screen ever asks
					repo = filepath.Join(s.T.TempDir(), "repo")
					writeTasksFeatureCard(t, repo, "in-progress", "GATE-1", "swr", "api", "web.kf", "Free the gate")
					writeTasksBoard(t, repo, "ready", "OTHER-2", "web", "Someone else's card")
					writeTasksBoard(t, repo, "done", "MINE-3", "api", "Already finished")
					reportTasksSession(t, srv, "s-hold", "build", "api", repo, "")
				})
			s.When("the screen asks the session's tasks", func(t *testing.T) {
				code, _, body = srv.get(t, "/api/sessions/s-hold/tasks", nil)
			})
			s.Then("exactly the card the member holds in progress answers, with its id, title and column", func(t *testing.T) {
				if code != 200 {
					t.Fatalf("tasks: %d %s", code, body)
				}
				cards, _ := bodyMap(t, body)["cards"].([]any)
				if len(cards) != 1 {
					t.Fatalf("cards = %v, want exactly GATE-1", cards)
				}
				card, _ := cards[0].(map[string]any)
				if card["id"] != "GATE-1" || card["title"] != "Free the gate" || card["column"] != "in-progress" {
					t.Errorf("the card = %v, want GATE-1 Free the gate in-progress", card)
				}
			})
			s.And("the card held by another actor and the finished one stay out", func(t *testing.T) {
				raw, _ := bodyMap(t, body)["cards"].([]any)
				for _, entry := range raw {
					card, _ := entry.(map[string]any)
					if card["id"] == "OTHER-2" || card["id"] == "MINE-3" {
						t.Errorf("the answer holds %v, want only GATE-1", card)
					}
				}
			})
			s.And("the answer names the card's cluster and the board's key", func(t *testing.T) {
				cards, _ := bodyMap(t, body)["cards"].([]any)
				card, _ := cards[0].(map[string]any)
				if card["cluster"] != "swr" {
					t.Errorf("cluster = %v, want swr", card["cluster"])
				}
				tab := kanbantab.Mount(context.Background(), kanbantab.Options{
					Roots: func(context.Context) []string { return []string{repo} },
				})
				want := tab.BoardKey(context.Background(), filepath.Join(repo, ".agents", "kanban"))
				if got := bodyMap(t, body)["board"]; got != want {
					t.Errorf("board = %v, want the boards list's key %s", got, want)
				}
			})
		})
	})
}
