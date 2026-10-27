package satellite

// The hub relays the owner's answers with the decision it owes this machine (SPEC 6.7): the
// machine's item takes them beside the decision, the machine's own switches and the same
// answer checks rule there, and a plain tool's allow travels whole with none.

import (
	"context"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// askItem grows one open permission item on the machine, the way its own hook's post does.
func askItem(t *testing.T, db *hub.DB, ctx context.Context, session, tool, input string, now time.Time) int64 {
	t.Helper()
	id, err := db.CreatePermissionItem(ctx, session, tool, []byte(input), now)
	if err != nil {
		t.Fatalf("create the item: %v", err)
	}
	return id
}

// offerAllow encodes the hub's allow for one item — with the answers it carries — and applies
// it the way the machine's link applies a handout.
func offerAllow(t *testing.T, link *Link, db *hub.DB, ctx context.Context, id int64, session string,
	answers map[string]string,
) (string, string) {
	t.Helper()
	key, err := db.KeyOfItem(ctx, id)
	if err != nil || key == "" {
		t.Fatalf("the item's key = %q, %v", key, err)
	}
	payload, err := hub.EncodeSatPayload(hub.SatRefusalBody{
		Key: key, SessionID: session, Decision: "allow", Answers: answers,
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return link.apply(ctx, hub.SatHandout{ID: id, Kind: hub.SatDecision, Payload: payload})
}

// itemAfter reads one item back once it is answered.
func itemAfter(t *testing.T, db *hub.DB, ctx context.Context, id int64, now time.Time) *hub.PermissionItem {
	t.Helper()
	item, err := db.PermissionItemByID(ctx, id, now.Add(time.Second))
	if err != nil || item == nil {
		t.Fatalf("read the item back: %v", err)
	}
	return item
}

func TestARelayedAllowCarriesTheAnswers(t *testing.T) {
	bdd.Feature(t, "A relayed allow carries the answers", func(f *bdd.F) {
		f.Scenario("the hub's allow with answers lands on the machine's item", func(s *bdd.S) {
			var (
				fake           *fakeHub
				link           *Link
				db             *hub.DB
				id             int64
				result, reason string
				now            = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			)
			s.Given("a machine asking one question", func(t *testing.T) {
				fake = startFakeHub(s.T)
				link, db = linkOn(s.T, fake.srv.URL)
				id = askItem(t, db, context.Background(), "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"},{"label":"SQLite"}]}]}`, now)
			})
			s.When("the hub allows it naming the answer", func(t *testing.T) {
				result, reason = offerAllow(t, link, db, context.Background(), id, "s-1",
					map[string]string{"Which database?": "Postgres"})
			})
			s.Then("the handout is answered delivered", func(t *testing.T) {
				if result != hub.SatDelivered || reason != "" {
					t.Fatalf("the allow = (%s, %q), want delivered", result, reason)
				}
			})
			s.And("the machine's item reads answered with the answers beside it", func(t *testing.T) {
				item := itemAfter(t, db, context.Background(), id, now)
				if item.State != hub.StateAnswered || item.Decision != "allow" ||
					item.Answers["Which database?"] != "Postgres" || len(item.Answers) != 1 {
					t.Fatalf("the item = %s/%s/%v, want answered allow carrying Which database? Postgres",
						item.State, item.Decision, item.Answers)
				}
			})
		})

		f.Scenario("an answer naming no question refuses the handout", func(s *bdd.S) {
			var (
				fake           *fakeHub
				link           *Link
				db             *hub.DB
				id             int64
				result, reason string
				now            = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			)
			s.Given("a machine asking one question", func(t *testing.T) {
				fake = startFakeHub(s.T)
				link, db = linkOn(s.T, fake.srv.URL)
				id = askItem(t, db, context.Background(), "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"}]}]}`, now)
			})
			s.When("the hub allows it naming a question the item never asked", func(t *testing.T) {
				result, reason = offerAllow(t, link, db, context.Background(), id, "s-1",
					map[string]string{"Which server?": "Apache"})
			})
			s.Then("the handout is refused naming the stray question", func(t *testing.T) {
				if result != hub.SatRefused || reason != `answers: no question reads "Which server?"` {
					t.Fatalf("the allow = (%s, %q), want refused naming Which server?", result, reason)
				}
			})
			s.And("the machine's item reads the deny with that reason", func(t *testing.T) {
				item := itemAfter(t, db, context.Background(), id, now)
				if item.State != hub.StateAnswered || item.Decision != "deny" || item.Message != reason {
					t.Fatalf("the item = %s/%s/%q, want the deny carrying the reason",
						item.State, item.Decision, item.Message)
				}
			})
		})

		f.Scenario("an answer naming a .ssh path keeps the path floor at the machine", func(s *bdd.S) {
			var (
				fake           *fakeHub
				link           *Link
				db             *hub.DB
				id             int64
				result, reason string
				now            = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			)
			s.Given("a machine asking a plain question", func(t *testing.T) {
				fake = startFakeHub(s.T)
				link, db = linkOn(s.T, fake.srv.URL)
				id = askItem(t, db, context.Background(), "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which key should the deploy use?","options":[{"label":"The old one"}]}]}`, now)
			})
			s.When("the hub allows it with an answer naming a path under .ssh", func(t *testing.T) {
				result, reason = offerAllow(t, link, db, context.Background(), id, "s-1",
					map[string]string{"Which key should the deploy use?": "~/.ssh/id_ed25519"})
			})
			s.Then("the handout is refused naming the path floor", func(t *testing.T) {
				if result != hub.SatRefused ||
					reason != "refused: the input names a .ssh, .gnupg or secrets path; allow it in the terminal" {
					t.Fatalf("the allow = (%s, %q), want refused at the path floor", result, reason)
				}
			})
			s.And("the machine's item reads the deny with that reason", func(t *testing.T) {
				item := itemAfter(t, db, context.Background(), id, now)
				if item.State != hub.StateAnswered || item.Decision != "deny" || item.Message != reason {
					t.Fatalf("the item = %s/%s/%q, want the deny carrying the reason",
						item.State, item.Decision, item.Message)
				}
			})
		})

		f.Scenario("remote permissions off refuse a relayed question too", func(s *bdd.S) {
			var (
				fake           *fakeHub
				link           *Link
				db             *hub.DB
				id             int64
				result, reason string
				now            = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			)
			s.Given("a machine with remote permissions off, asking one question", func(t *testing.T) {
				fake = startFakeHub(s.T)
				link, db = linkOn(s.T, fake.srv.URL)
				h := home.Home{Root: link.home.Root}
				cfg, err := home.LoadConfig(h)
				if err != nil {
					t.Fatalf("load the config: %v", err)
				}
				cfg.Remote.Permissions = false
				if err := home.SaveConfig(h, cfg); err != nil {
					t.Fatalf("save the config: %v", err)
				}
				id = askItem(t, db, context.Background(), "s-1", "AskUserQuestion",
					`{"questions":[{"question":"Which database?","options":[{"label":"Postgres"}]}]}`, now)
			})
			s.When("the hub allows the question with an answer", func(t *testing.T) {
				result, reason = offerAllow(t, link, db, context.Background(), id, "s-1",
					map[string]string{"Which database?": "Postgres"})
			})
			s.Then("the handout is refused naming the off switch", func(t *testing.T) {
				if result != hub.SatRefused || reason != "remote permissions are off on this machine" {
					t.Fatalf("the allow = (%s, %q), want refused at the off switch", result, reason)
				}
			})
		})

		f.Scenario("a plain tool's allow travels whole with no answers", func(s *bdd.S) {
			var (
				fake           *fakeHub
				link           *Link
				db             *hub.DB
				id             int64
				result, reason string
				now            = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			)
			s.Given("a machine with a Bash call waiting on the owner", func(t *testing.T) {
				fake = startFakeHub(s.T)
				link, db = linkOn(s.T, fake.srv.URL)
				id = askItem(t, db, context.Background(), "s-1", "Bash", `{"command":"npm test"}`, now)
			})
			s.When("the hub allows it carrying no answers", func(t *testing.T) {
				result, reason = offerAllow(t, link, db, context.Background(), id, "s-1", nil)
			})
			s.Then("the handout is answered delivered and the item reads the allow", func(t *testing.T) {
				if result != hub.SatDelivered || reason != "" {
					t.Fatalf("the allow = (%s, %q), want delivered", result, reason)
				}
				item := itemAfter(t, db, context.Background(), id, now)
				if item.State != hub.StateAnswered || item.Decision != "allow" || len(item.Answers) != 0 {
					t.Fatalf("the item = %s/%s/%v, want answered allow with no answers",
						item.State, item.Decision, item.Answers)
				}
			})
		})
	})
}
