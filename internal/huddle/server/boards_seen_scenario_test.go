package server

// The hub's board list remembers: every board a live session reported stays
// listed after the session ends, for thirty days from the last sighting, and
// never past a directory that is gone.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// seenFixture is one home with its registry and a clock the scenario moves.
type seenFixture struct {
	t     *testing.T
	home  string
	reg   *hub.DB
	clock time.Time
	srv   *server
}

func newSeenFixture(t *testing.T) *seenFixture {
	t.Helper()
	homeRoot := t.TempDir()
	reg, err := hub.Open(context.Background(), home.Home{Root: homeRoot}.HubDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	f := &seenFixture{t: t, home: homeRoot, reg: reg,
		clock: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	f.srv = &server{registry: reg, now: func() time.Time { return f.clock }, o: Options{Home: homeRoot}}
	return f
}

// boardRepo answers one repo with a board and reports its session live now.
func (f *seenFixture) boardRepo(name, session string) string {
	f.t.Helper()
	repo := filepath.Join(f.t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if _, err := board.Init(context.Background(), filepath.Join(repo, ".agents", "kanban"), "", f.clock); err != nil {
		f.t.Fatal(err)
	}
	f.report(session, repo, "UserPromptSubmit")
	return repo
}

// report lands one session report in the registry at the fixture's clock.
func (f *seenFixture) report(session, cwd, event string) {
	f.t.Helper()
	err := f.reg.Report(context.Background(), hub.Report{
		SessionID: session, Cwd: cwd, LastEvent: event,
		LastEventAt: f.clock.UTC().Format("2006-01-02T15:04:05.000Z"),
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// listed answers the board roots and cwds boardRoots serves.
func (f *seenFixture) listed() []string {
	f.t.Helper()
	out := f.srv.boardRoots(context.Background())
	if out == nil {
		out = []string{}
	}
	return out
}

// seenBoards decodes the home's remembered-board store.
func seenBoards(t *testing.T, homeRoot string) map[string]int64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(homeRoot, "kanban-boards.json"))
	if err != nil {
		t.Fatalf("read the remembered boards: %v", err)
	}
	var doc struct {
		Boards []struct {
			Root string `json:"root"`
			Seen int64  `json:"seen"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	seen := map[string]int64{}
	for _, b := range doc.Boards {
		seen[b.Root] = b.Seen
	}
	return seen
}

func TestTheHubListsEveryBoardASessionHasReported(t *testing.T) {
	bdd.Feature(t, "the hub lists every board a session has reported", func(f *bdd.F) {
		f.Scenario("two sessions in two repos both show", func(s *bdd.S) {
			var (
				fix   *seenFixture
				alpha string
				beta  string
				got   []string
			)
			s.Given("two live sessions, each working in its own repo with a board", func(t *testing.T) {
				fix = newSeenFixture(s.T)
				alpha = fix.boardRepo("alpha", "s-alpha")
				beta = fix.boardRepo("beta", "s-beta")
			})
			s.When("the hub lists its boards", func(t *testing.T) {
				got = fix.listed()
			})
			s.Then("both repos' working dirs answer", func(t *testing.T) {
				for _, want := range []string{alpha, beta} {
					if !slices.Contains(got, want) {
						t.Errorf("the board roots = %v, want %s among them", got, want)
					}
				}
			})
		})

		f.Scenario("a board stays listed after its session ends", func(s *bdd.S) {
			var (
				fix   *seenFixture
				repo  string
				root  string
				first []string
				again []string
			)
			s.Given("a live session working in a repo with a board, which the hub has listed once", func(t *testing.T) {
				fix = newSeenFixture(s.T)
				repo = fix.boardRepo("alpha", "s-alpha")
				root = filepath.Join(repo, ".agents", "kanban")
				first = fix.listed()
			})
			s.When("the session ends and the hub lists its boards again", func(t *testing.T) {
				fix.clock = fix.clock.Add(time.Minute)
				fix.report("s-alpha", repo, "SessionEnd")
				again = fix.listed()
			})
			s.Then("the board is still listed, remembered from the live sighting", func(t *testing.T) {
				if !slices.Contains(first, repo) {
					t.Errorf("the first list = %v, want the repo's cwd among them", first)
				}
				if !slices.Contains(again, root) {
					t.Errorf("the list after the end = %v, want the remembered board %s among them", again, root)
				}
			})
			s.And("the sighting is in the home's store", func(t *testing.T) {
				seen := seenBoards(t, fix.home)
				if _, ok := seen[root]; !ok {
					t.Errorf("the store = %v, want %s recorded", seen, root)
				}
			})
		})

		f.Scenario("a board not seen for 31 days, or whose directory is gone, is no longer listed", func(s *bdd.S) {
			var (
				fix   *seenFixture
				alpha string
				beta  string
				got   []string
			)
			s.Given("two live sessions with boards, which the hub has listed once", func(t *testing.T) {
				fix = newSeenFixture(s.T)
				alpha = fix.boardRepo("alpha", "s-alpha")
				beta = fix.boardRepo("beta", "s-beta")
				fix.listed()
			})
			s.When("both sessions end, two days pass, and beta's board directory is deleted", func(t *testing.T) {
				fix.clock = fix.clock.Add(time.Minute)
				fix.report("s-alpha", filepath.Join(alpha, ".agents", "kanban"), "SessionEnd")
				fix.report("s-beta", filepath.Join(beta, ".agents", "kanban"), "SessionEnd")
				fix.clock = fix.clock.Add(48 * time.Hour)
				if err := os.RemoveAll(filepath.Join(beta, ".agents")); err != nil {
					t.Fatal(err)
				}
				got = fix.listed()
			})
			s.Then("the board whose directory is gone is not listed and the other one is, by its remembered root", func(t *testing.T) {
				if slices.Contains(got, beta) || slices.Contains(got, filepath.Join(beta, ".agents", "kanban")) {
					t.Errorf("the list = %v, want the deleted board gone", got)
				}
				want := filepath.Join(alpha, ".agents", "kanban")
				if !slices.Contains(got, want) {
					t.Errorf("the list = %v, want the remembered %s among them", got, want)
				}
			})
			s.When("31 days pass from the sighting", func(t *testing.T) {
				fix.clock = fix.clock.Add(29 * 24 * time.Hour)
				got = fix.listed()
			})
			s.Then("the board last seen 31 days ago is not listed either", func(t *testing.T) {
				if len(got) != 0 {
					t.Errorf("the list = %v, want it empty past the thirty days", got)
				}
			})
		})
	})
}
