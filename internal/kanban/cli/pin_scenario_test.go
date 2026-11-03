package cli

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

func TestASessionPinsItselfToItsBranchsBoard(t *testing.T) {
	bdd.Feature(t, "A session pins itself to its branch's board", func(f *bdd.F) {
		f.Scenario("the branch feat/login names the login board; the pin scopes list and new", func(s *bdd.S) {
			var p *pinFixture
			s.Given("branch feat/login, the boards login and billing, and a card on each", func(t *testing.T) {
				p = newPinFixture(s.T, "feat/login", "s1")
				for id, cluster := range map[string]string{"LOGIN-1": "login", "BILL-1": "billing"} {
					if code, stderr := p.newCard(id, cluster); code != 0 {
						t.Fatalf("new %s = %d %q", id, code, stderr)
					}
				}
			})
			s.When("the session asks which board its branch names", func(t *testing.T) {
				code, stdout, stderr := p.run("board", "suggest", "--actor", "w1")
				if code != 0 || stdout != "login\n" {
					t.Fatalf("suggest = %d %q %q, want login", code, stdout, stderr)
				}
			})
			s.And("it pins itself with kanban board use login", func(t *testing.T) {
				if code, stdout, stderr := p.run("board", "use", "login", "--actor", "w1"); code != 0 {
					t.Fatalf("use = %d %q %q, want exit 0", code, stdout, stderr)
				}
			})
			s.Then("list shows only the login cards", func(t *testing.T) {
				_, stdout, _ := p.run("list")
				if !strings.Contains(stdout, "LOGIN-1") || strings.Contains(stdout, "BILL-1") {
					t.Errorf("list:\n%s\nwant only LOGIN-1", stdout)
				}
			})
			s.And("new without --cluster lands in login", func(t *testing.T) {
				if code, stderr := p.newCard("LOGIN-2", ""); code != 0 {
					t.Fatalf("new = %d %q, want exit 0", code, stderr)
				}
				if !strings.Contains(p.ticketText("backlog", "LOGIN-2"), "cluster: login") {
					t.Errorf("the new card is not on login:\n%s", p.ticketText("backlog", "LOGIN-2"))
				}
			})
		})
	})
}
