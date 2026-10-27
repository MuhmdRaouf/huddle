package server

// The owner answers Claude's questions from the phone (SPEC 6.2): an AskUserQuestion allow
// carries the answers, keyed by each question's exact text, a question needs no remote-allow
// entry, and the path floor still reads everything the input and the answers carry.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestAnAskUserQuestionAllowCarriesTheAnswers(t *testing.T) {
	bdd.Feature(t, "An AskUserQuestion allow carries the answers", func(f *bdd.F) {
		f.Scenario("the owner allows a question with answers", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a session asking one question", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				id = permitPost(t, srv, "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"},{"label":"SQLite"}]}]}`)
			})
			s.When("the owner allows it naming the answer", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"Which database?":"Postgres"}}`, nil)
			})
			s.Then("the decision is taken", func(t *testing.T) {
				if code != 200 {
					t.Fatalf("the allow = %d %s, want 200", code, out)
				}
			})
			s.And("the hook's next wait carries the answers back beside the decision", func(t *testing.T) {
				wcode, _, wout := srv.get(t, fmt.Sprintf("/api/needs/%d/wait", id), nil)
				if wcode != 200 {
					t.Fatalf("the wait = %d %s, want 200", wcode, wout)
				}
				answers, _ := bodyMap(t, wout)["answers"].(map[string]any)
				if answers["Which database?"] != "Postgres" {
					t.Errorf("the wait's answers = %v, want Which database? Postgres", bodyMap(t, wout)["answers"])
				}
			})
		})

		f.Scenario("a second decide loses the race whatever it carries", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a question the owner has already allowed with an answer", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				id = permitPost(t, srv, "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"}]}]}`)
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"Which database?":"Postgres"}}`, nil)
				if code != 200 {
					t.Fatalf("the first allow = %d %s", code, out)
				}
			})
			s.When("a second decide answers it again", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"Which database?":"SQLite"}}`, nil)
			})
			s.Then("the race is lost with the already-answered refusal", func(t *testing.T) {
				if code != 409 || !strings.Contains(out, "already answered") {
					t.Fatalf("the second decide = %d %s, want 409 already answered", code, out)
				}
			})
		})

		f.Scenario("an answer naming no question is refused naming it", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a session asking one question", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				id = permitPost(t, srv, "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"}]}]}`)
			})
			s.When("the owner allows it naming a question the item never asked", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"Which server?":"Apache"}}`, nil)
			})
			s.Then("the allow is refused naming the stray question", func(t *testing.T) {
				msg, _ := bodyMap(t, out)["error"].(string)
				if code != 400 || msg != `answers: no question reads "Which server?"` {
					t.Fatalf("the allow = %d %s, want 400 naming Which server?", code, out)
				}
			})
		})

		f.Scenario("a plain tool takes no answers", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a Bash call waiting on the owner", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				id = permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
			})
			s.When("the owner allows it with answers", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"which?":"that one"}}`, nil)
			})
			s.Then("the allow is refused: only a question takes answers", func(t *testing.T) {
				if code != 400 || !strings.Contains(out, "answers: only a question takes answers") {
					t.Fatalf("the allow = %d %s, want 400 only a question takes answers", code, out)
				}
			})
		})

		f.Scenario("answers on a deny are refused", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a Bash call waiting on the owner", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				id = permitPost(t, srv, "s-1", "Bash", `{"command":"npm test"}`)
			})
			s.When("the owner denies it with answers", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"deny","answers":{"which?":"that one"}}`, nil)
			})
			s.Then("the deny is refused: only an allow takes answers", func(t *testing.T) {
				if code != 400 || !strings.Contains(out, "answers: only an allow takes answers") {
					t.Fatalf("the deny = %d %s, want 400 only an allow takes answers", code, out)
				}
			})
		})

		f.Scenario("a question passes a list that names no question tool, and the audit reads the exemption", func(s *bdd.S) {
			var (
				srv    *childServer
				id     int64
				code   int
				out    string
				exempt any
			)
			s.Given("a machine whose allow list names no AskUserQuestion", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				permitConfig(t, srv, "[remote]\nallow = [\"Bash(npm test*)\"]\n")
				id = permitPost(t, srv, "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"}]}]}`)
			})
			s.When("the owner allows the question with an answer", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"Which database?":"Postgres"}}`, nil)
			})
			s.Then("the decision is taken without any list entry", func(t *testing.T) {
				if code != 200 {
					t.Fatalf("the allow = %d %s, want 200", code, out)
				}
			})
			s.And("the audit row reads why the question needed no entry", func(t *testing.T) {
				acode, _, aout := srv.get(t, "/api/audit", nil)
				if acode != 200 {
					t.Fatalf("the audit log = %d %s", acode, aout)
				}
				rows, _ := bodyMap(t, aout)["entries"].([]any)
				for _, row := range rows {
					if m, ok := row.(map[string]any); ok && m["action"] == "allow" && m["target"] == fmt.Sprintf("%d", id) {
						detail, _ := m["detail"].(map[string]any)
						exempt = detail["exempt"]
					}
				}
				if exempt != "allow list: AskUserQuestion" {
					t.Errorf("the audit row = %v, want detail exempt allow list: AskUserQuestion", rows)
				}
			})
		})

		f.Scenario("a question naming a secrets path keeps the path floor", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a session asking to read a secrets path", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				id = permitPost(t, srv, "s-1", "AskUserQuestion",
					`{"questions":[{"question":"read secrets/prod.env now?","options":[{"label":"Yes"}]}]}`)
			})
			s.When("the owner allows it with an answer", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"read secrets/prod.env now?":"Yes"}}`, nil)
			})
			s.Then("the allow is refused naming the path floor", func(t *testing.T) {
				if code != 403 || !strings.Contains(out, "refused: the input names a .ssh, .gnupg or secrets path") {
					t.Fatalf("the allow = %d %s, want 403 at the path floor", code, out)
				}
			})
		})

		f.Scenario("an answer naming a .ssh path keeps the path floor", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a session asking a plain question", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				id = permitPost(t, srv, "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which key should the deploy use?","options":[{"label":"The old one"}]}]}`)
			})
			s.When("the owner answers with a path under .ssh", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"Which key should the deploy use?":"~/.ssh/id_ed25519"}}`, nil)
			})
			s.Then("the allow is refused naming the path floor", func(t *testing.T) {
				if code != 403 || !strings.Contains(out, "refused: the input names a .ssh, .gnupg or secrets path") {
					t.Fatalf("the allow = %d %s, want 403 at the path floor", code, out)
				}
			})
		})

		f.Scenario("remote permissions off refuse a question's allow too", func(s *bdd.S) {
			var (
				srv  *childServer
				id   int64
				code int
				out  string
			)
			s.Given("a machine with remote permissions off", func(t *testing.T) {
				srv = startServer(s.T, nil) // the rig outlives the steps: it starts under the scenario's own T
				permitConfig(t, srv, "[remote]\npermissions = false\n")
				id = permitPost(t, srv, "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"}]}]}`)
			})
			s.When("the owner allows the question with an answer", func(t *testing.T) {
				code, out = srv.post(t, fmt.Sprintf("/api/needs/%d/decide", id),
					`{"decision":"allow","answers":{"Which database?":"Postgres"}}`, nil)
			})
			s.Then("the allow is refused naming the off switch", func(t *testing.T) {
				if code != 403 || !strings.Contains(out, "remote permissions are off") {
					t.Fatalf("the allow = %d %s, want 403 remote permissions are off", code, out)
				}
			})
		})
	})
}
