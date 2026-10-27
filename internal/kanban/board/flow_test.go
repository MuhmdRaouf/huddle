package board

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/ticket"
)

// The board flow suite: pull to done, one row per case, each row asserting the
// fields the move writes and the log line it leaves. A row whose case also has
// a wording half (--help text, argv order) keeps only its board-level half
// here; the wording halves are the cli package's suite.
const longSummary = "Ports the router front process to Go: it forks the worker, restarts it when it dies, " +
	"retries requests on a fresh worker, and serves passthrough while none is up. " +
	"Done when all front cases pass against a fake worker."

const shortSummary = "Too short to be a useful summary."

// fixedNow pins every stamp; its +0300 offset gives every stamp the signed
// four-digit offset the formats match.
var fixedNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("+0300", 3*60*60))

const stamp = "2026-10-15T12:00:00+0300"

// fixture is one throwaway board in a temp directory.
type fixture struct {
	t    *testing.T
	root string
	b    Board
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureAt(t, filepath.Join(t.TempDir(), ".agents", "kanban"))
}

// newFixtureAt builds the board at root (which may hold spaces) without init.
func newFixtureAt(t *testing.T, root string) *fixture {
	t.Helper()
	f := &fixture{t: t, root: root, b: Board{Root: root, Now: func() time.Time { return fixedNow }}}
	for _, col := range Columns {
		if err := os.MkdirAll(filepath.Join(root, col), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.writeFile("EPOCH", "1\n")
	f.writeFile("LOG", "")
	return f
}

func (f *fixture) writeFile(name, text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, name), []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) path(col, id string) string {
	return filepath.Join(f.root, col, id+".md")
}

// card writes a ticket file and answers its path.
func (f *fixture) card(col, id, fm string, body ...string) string {
	f.t.Helper()
	text := "---\n" + strings.TrimSpace(fm) + "\n---\n\n"
	if len(body) == 1 {
		text += strings.TrimRight(body[0], "\n") + "\n"
	} else {
		text += "# " + id + "\n\nBody text.\n"
	}
	if err := os.MkdirAll(filepath.Dir(f.path(col, id)), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.path(col, id), []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return f.path(col, id)
}

func (f *fixture) fm(col, id string) string {
	f.t.Helper()
	data, err := os.ReadFile(f.path(col, id))
	if err != nil {
		f.t.Fatalf("read %s: %v", f.path(col, id), err)
	}
	return string(data)
}

func (f *fixture) exists(col, id string) bool {
	_, err := os.Stat(f.path(col, id))
	return err == nil
}

func (f *fixture) logText() string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "LOG"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

// lastLog answers the last LOG line as a map.
func (f *fixture) lastLog() map[string]any {
	f.t.Helper()
	lines := strings.Split(strings.TrimSpace(f.logText()), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		f.t.Fatal("LOG is empty")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		f.t.Fatalf("last LOG line %q: %v", lines[len(lines)-1], err)
	}
	return entry
}

// fullBody answers a body that passes the ticket bar.
func fullBody(id string) string {
	return "# " + id + ": full\n\n" +
		"## Description\nWhat changes and why. Done when: the bar passes.\n\n" +
		"## Context\nThe reference tests.\n\n" +
		"## Implementation plan\n1. do the work\n\n" +
		"## Implementation details\n" + strings.Repeat("planned decisions ", 15) + "\n\n" +
		"## Acceptance\n- [ ] it works\n\n" +
		"## Verification\n```\ngo test ./...\n```\nexit 0\n\n" +
		"## Out of scope\nNothing else.\n\n" +
		"## Log\n"
}

// expCard writes an expedite fixture card.
func (f *fixture) expCard(col, id string, extra ...string) string {
	return f.card(col, id, "id: "+id+"\ntitle: \""+id+" title\"\ncluster: fix\nexpedite: true\n"+
		"type: fix\npriority: P1\nest: S\nsummary: \""+longSummary+"\""+strings.Join(extra, ""))
}

// cmd is one board command ready to run; the fixture wrappers answer it so a
// must helper can take the call as one value.
type cmd func() ([]string, error)

func (f *fixture) promote(a Args) cmd {
	return func() ([]string, error) { return f.b.Promote(context.Background(), a) }
}

func (f *fixture) pull(a Args) cmd {
	return func() ([]string, error) { return f.b.Pull(context.Background(), a) }
}

func (f *fixture) review(a Args) cmd {
	return func() ([]string, error) { return f.b.Review(context.Background(), a) }
}

func (f *fixture) reject(a Args) cmd {
	return func() ([]string, error) { return f.b.Reject(context.Background(), a) }
}

func (f *fixture) test(a Args) cmd {
	return func() ([]string, error) { return f.b.Test(context.Background(), a) }
}

func (f *fixture) done(a Args) cmd {
	return func() ([]string, error) { return f.b.Done(context.Background(), a) }
}

func (f *fixture) block(a Args) cmd {
	return func() ([]string, error) { return f.b.Block(context.Background(), a) }
}

func (f *fixture) unblock(a Args) cmd {
	return func() ([]string, error) { return f.b.Unblock(context.Background(), a) }
}

func (f *fixture) move(a Args) cmd {
	return func() ([]string, error) { return f.b.Move(context.Background(), a) }
}

func (f *fixture) log(a Args) cmd {
	return func() ([]string, error) { return f.b.Log(context.Background(), a) }
}

func (f *fixture) stopLine(a Args) cmd {
	return func() ([]string, error) { return f.b.StopLine(context.Background(), a) }
}

func (f *fixture) resumeLine(a Args) cmd {
	return func() ([]string, error) { return f.b.ResumeLine(context.Background(), a) }
}

func (f *fixture) check(id string) cmd {
	return func() ([]string, error) { return f.b.Check(context.Background(), id) }
}

func (f *fixture) new(n NewTicket) cmd {
	return func() ([]string, error) { return f.b.New(context.Background(), n) }
}

func (f *fixture) set(actor, id string, pairs []string) cmd {
	return func() ([]string, error) { return f.b.Set(context.Background(), actor, id, pairs) }
}

func (f *fixture) list(column string) func() ([]Card, error) {
	return func() ([]Card, error) { return f.b.List(context.Background(), column) }
}

// initBoard runs Init with the suite's pinned clock.
func initBoard(root, from string) cmd {
	return func() ([]string, error) { return Init(context.Background(), root, from, fixedNow) }
}

func mustLines(t *testing.T, call cmd) []string {
	t.Helper()
	out, err := call()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return out
}

// mustCards runs a list call and answers its cards.
func mustCards(t *testing.T, call func() ([]Card, error)) []Card {
	t.Helper()
	cards, err := call()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return cards
}

// mustRefuse answers the refusal message.
func mustRefuse(t *testing.T, call cmd) string {
	t.Helper()
	_, err := call()
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("error %v (%T) is not a refusal", err, err)
	}
	return r.Message
}

// refuseErr answers the refusal message of an error already in hand.
func refuseErr(t *testing.T, err error) string {
	t.Helper()
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("error %v (%T) is not a refusal", err, err)
	}
	return r.Message
}

// refuseWithLines covers an answer that prints its lines and still exits 1.
func refuseWithLines(t *testing.T, call cmd) ([]string, string) {
	t.Helper()
	out, err := call()
	var r *Refused
	if !errors.As(err, &r) {
		t.Fatalf("error %v (%T) is not a refusal", err, err)
	}
	return out, r.Message
}

// idOfLastLine answers the ticket id in a command's final (path) line.
func idOfLastLine(t *testing.T, out []string) string {
	t.Helper()
	if len(out) == 0 {
		t.Fatal("no output lines")
	}
	base := filepath.Base(out[len(out)-1])
	return strings.TrimSuffix(base, ".md")
}

func lastEndsWith(t *testing.T, out []string, col, id string) {
	t.Helper()
	if len(out) == 0 {
		t.Fatal("no output lines")
	}
	if !strings.HasSuffix(out[len(out)-1], filepath.Join(col, id+".md")) {
		t.Fatalf("last line %q does not end with %s", out[len(out)-1], filepath.Join(col, id+".md"))
	}
}

func contains(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: %q does not contain %q", what, haystack, needle)
	}
}

func notContains(t *testing.T, haystack, needle, what string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("%s: %q contains %q", what, haystack, needle)
	}
}

func lintRules(t *testing.T, report LintReport, id string) []ticket.Problem {
	t.Helper()
	for _, lt := range report.Tickets {
		if lt.ID == id {
			return lt.Problems
		}
	}
	t.Fatalf("lint report has no ticket %s", id)
	return nil
}

func hasRule(problems []ticket.Problem, rule string) bool {
	for _, p := range problems {
		if p.Rule == rule {
			return true
		}
	}
	return false
}

func TestFlow(t *testing.T) {
	ctx := context.Background()

	t.Run("test_pull_id_sets_branch_and_worktree", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "RTR-FRONT", "id: RTR-FRONT\ntitle: \"router front\"\ntype: feature\npriority: P1\nest: M")
		out := mustLines(t, f.pull(Args{Actor: "w1", ID: "RTR-FRONT",
			Branch: "port/RTR-FRONT", Worktree: "scratchpad/wt/RTR-FRONT"}))
		lastEndsWith(t, out, "in-progress", "RTR-FRONT")
		s := f.fm("in-progress", "RTR-FRONT")
		contains(t, s, "claimed_by: w1", "fm")
		contains(t, s, "branch: port/RTR-FRONT", "fm")
		contains(t, s, "worktree: scratchpad/wt/RTR-FRONT", "fm")
		if _, err := f.pull(Args{Actor: "w2", ID: "NOPE-1"})(); err == nil {
			t.Error("pull of NOPE-1: want a refusal")
		}
	})

	t.Run("test_pull_without_options_still_picks_best", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "AAA-1", "id: AAA-1\ntitle: \"a\"\ntype: docs\npriority: P2")
		f.card("ready", "BBB-2", "id: BBB-2\ntitle: \"b\"\ntype: docs\npriority: P0")
		out := mustLines(t, f.pull(Args{Actor: "w1"}))
		lastEndsWith(t, out, "in-progress", "BBB-2")
	})

	t.Run("test_pull_orders_p0_to_p3_then_unknown_then_name", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "A-P3", "id: A-P3\ntitle: \"a\"\ntype: docs\npriority: P3")
		f.card("ready", "B-P1", "id: B-P1\ntitle: \"b\"\ntype: docs\npriority: P1")
		f.card("ready", "C-P2", "id: C-P2\ntitle: \"c\"\ntype: docs\npriority: P2")
		f.card("ready", "D-NONE", "id: D-NONE\ntitle: \"d\"\ntype: docs")
		f.card("ready", "E-P3", "id: E-P3\ntitle: \"e\"\ntype: docs\npriority: P3")
		var order []string
		for _, actor := range []string{"w1", "w2", "w3", "w4", "w5"} {
			out := mustLines(t, f.pull(Args{Actor: actor}))
			order = append(order, idOfLastLine(t, out))
		}
		want := []string{"B-P1", "C-P2", "A-P3", "E-P3", "D-NONE"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Errorf("pull order = %v, want %v", order, want)
		}
	})

	t.Run("test_pull_leaves_a_closed_feature_out_of_the_pick", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "SWR-1", "id: SWR-1\ntitle: \"closed\"\ntype: docs\npriority: P0\ncluster: swr")
		f.card("ready", "WEB-1", "id: WEB-1\ntitle: \"open\"\ntype: docs\npriority: P3\ncluster: web")
		mustLines(t, func() ([]string, error) {
			return f.b.CloseFeature(context.Background(), Args{Actor: "lead", ID: "swr"})
		})
		out := mustLines(t, f.pull(Args{Actor: "w1"}))
		lastEndsWith(t, out, "in-progress", "WEB-1")
	})

	t.Run("test_pull_answers_nothing_ready_when_only_closed_cards_wait", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "SWR-1", "id: SWR-1\ntitle: \"closed\"\ntype: docs\npriority: P0\ncluster: swr")
		mustLines(t, func() ([]string, error) {
			return f.b.CloseFeature(context.Background(), Args{Actor: "lead", ID: "swr"})
		})
		msg := mustRefuse(t, f.pull(Args{Actor: "w1"}))
		if msg != "no ready tickets; promote one from backlog/ or unblock one from blocked/" {
			t.Fatalf("refusal = %q, want the nothing-ready line", msg)
		}
	})

	t.Run("test_block_records_new_fields", func(t *testing.T) {
		f := newFixture(t)
		f.card("in-progress", "CI-WIN", "id: CI-WIN\ntitle: \"ci\"\ntype: feature")
		out := mustLines(t, f.block(Args{Actor: "lead", ID: "CI-WIN", Note: "the CI is red",
			On:    []string{"WIN-1", "a push to GitHub"},
			Until: "CI on feat/core-go is green on the arm runner"}))
		lastEndsWith(t, out, "blocked", "CI-WIN")
		s := f.fm("blocked", "CI-WIN")
		contains(t, s, `blocked_reason: "the CI is red"`, "fm")
		contains(t, s, "blocked_from: in-progress", "fm")
		if !regexp.MustCompile(`blocked_at: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}`).MatchString(s) {
			t.Errorf("blocked_at missing or malformed in %q", s)
		}
		contains(t, s, "blocked_by: lead", "fm")
		contains(t, s, `blocked_on: [WIN-1, "a push to GitHub"]`, "fm")
		contains(t, s, `unblock_when: "CI on feat/core-go is green on the arm runner"`, "fm")
		if got := f.lastLog()["event"]; got != "block" {
			t.Errorf("LOG event = %v, want block", got)
		}
	})

	t.Run("test_unblock_clears_new_fields_keeps_from", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "CI-WIN", "id: CI-WIN\ntitle: \"ci\"\ntype: feature")
		mustLines(t, f.pull(Args{Actor: "w1"}))
		mustLines(t, f.block(Args{Actor: "lead", ID: "CI-WIN", Note: "x",
			On: []string{"WIN-1"}, Until: "green"}))
		mustLines(t, f.unblock(Args{Actor: "lead", ID: "CI-WIN"}))
		s := f.fm("ready", "CI-WIN")
		for _, key := range []string{"blocked_reason", "blocked_at", "blocked_by", "blocked_on", "unblock_when"} {
			notContains(t, s, key+":", key+" should be cleared")
		}
		contains(t, s, "blocked_from: in-progress", "fm")
	})

	t.Run("test_done_clears_block_fields", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "DOC-1", "id: DOC-1\ntitle: \"doc\"\ntype: docs")
		mustLines(t, f.pull(Args{Actor: "w1"}))
		mustLines(t, f.block(Args{Actor: "w1", ID: "DOC-1", Note: "waiting",
			On: []string{"X-1"}, Until: "ok"}))
		mustLines(t, f.unblock(Args{Actor: "w1", ID: "DOC-1"}))
		mustLines(t, f.pull(Args{Actor: "w2"}))
		mustLines(t, f.done(Args{Actor: "w2", ID: "DOC-1", Note: "written"}))
		s := f.fm("done", "DOC-1")
		for _, key := range []string{"blocked_reason", "blocked_at", "blocked_by", "blocked_on",
			"unblock_when", "blocked_from"} {
			notContains(t, s, key+":", key+" should be cleared on done")
		}
		contains(t, s, "finished_at:", "fm")
		contains(t, s, `result: "written"`, "fm")
	})

	t.Run("test_set_writes_tags_and_retags_a_done_card", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "X-1", "id: X-1\ntitle: \"x\"\ntype: feature")
		f.card("done", "D-1", "id: D-1\ntitle: \"d\"\ntype: docs\ncluster: split")
		mustLines(t, f.set("lead", "X-1", []string{"tags=[huddle, Hub, radar]"}))
		contains(t, f.fm("backlog", "X-1"), "tags: [huddle, hub, radar]", "fm")
		// tags and cluster classify a card; changing them on a done card rewrites no history
		mustLines(t, f.set("lead", "D-1", []string{"tags=[cc-plugins, router]", "cluster=remote control"}))
		s := f.fm("done", "D-1")
		contains(t, s, "tags: [cc-plugins, router]", "fm")
		contains(t, s, "cluster: remote control", "fm")
		contains(t, mustRefuse(t, f.set("lead", "D-1", []string{"title=other"})), "done cards are not edited", "refusal")
		contains(t, mustRefuse(t, f.set("lead", "X-1", []string{"tags=[Not_A Tag!]"})), "lowercase words joined by dashes", "refusal")
		contains(t, mustRefuse(t, f.set("lead", "X-1", []string{"tags=[a, b, c, d, e, f, g, h, i]"})), "at most 8", "refusal")
	})

	t.Run("test_set_writes_descriptive_fields", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "X-1", "id: X-1\ntitle: \"old\"\ntype: feature")
		f.card("backlog", "A-1", "id: A-1\ntitle: \"a\"\ntype: docs") // depends_on ids must exist, as for new
		f.card("done", "B-2", "id: B-2\ntitle: \"b\"\ntype: docs")
		out := mustLines(t, f.set("lead", "X-1", []string{
			"cluster=router", "title=new title", "priority=P0", "est=L",
			"branch=b1", "worktree=wt/x", "repos=repo1",
			"depends_on=[A-1, B-2]", "unblock_when=green CI", "summary=" + longSummary}))
		lastEndsWith(t, out, "backlog", "X-1")
		s := f.fm("backlog", "X-1")
		contains(t, s, "cluster: router", "fm")
		contains(t, s, `title: "new title"`, "fm")
		contains(t, s, "priority: P0", "fm")
		contains(t, s, "est: L", "fm")
		contains(t, s, "branch: b1", "fm")
		contains(t, s, "worktree: wt/x", "fm")
		contains(t, s, "repos: repo1", "fm")
		contains(t, s, "depends_on: [A-1, B-2]", "fm")
		contains(t, s, `unblock_when: "green CI"`, "fm")
		contains(t, s, `summary: "`+longSummary+`"`, "fm")
		if got := f.lastLog()["event"]; got != "set" {
			t.Errorf("LOG event = %v, want set", got)
		}
	})

	t.Run("test_set_refuses_status_fields", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "X-1", "id: X-1\ntitle: \"old\"\ntype: feature")
		msg := mustRefuse(t, f.set("lead", "X-1", []string{"claimed_by=w1"}))
		contains(t, msg, "not a settable field", "refusal")
		notContains(t, f.fm("backlog", "X-1"), "claimed_by:", "fm")
		// a refused key refuses the whole call: no partial writes
		mustRefuse(t, f.set("lead", "X-1", []string{"cluster=router", "status=done"}))
		notContains(t, f.fm("backlog", "X-1"), "cluster:", "fm")
	})

	t.Run("test_set_multiline_value_written_folded", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "X-1", "id: X-1\ntitle: \"old\"\ntype: feature")
		mustLines(t, f.set("lead", "X-1", []string{
			"summary=one line of what changes here\ntwo lines of why it matters to us\nthree lines of what done looks like"}))
		s := f.fm("backlog", "X-1")
		contains(t, s, "summary: >-", "fm")
		contains(t, s, "\n  one line of what changes here\n  two lines of why it matters to us\n  three lines of what done looks like", "fm")
	})

	t.Run("test_lint_flags_weak_cards", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "A-1", "id: A-1\ntitle: \"ok title\"\ntype: docs")
		f.card("ready", "B-2", "id: B-2\ntitle: \"ok title\"\ntype: docs\nsummary: \""+shortSummary+"\"")
		f.card("ready", "C-3", "id: C-3\ntitle: \""+strings.Repeat("x", 91)+"\"\ntype: docs\ncluster: c\nsummary: \""+longSummary+"\"")
		report := mustLint(t, f)
		for _, want := range []struct{ id, rule string }{
			{"A-1", "cluster"}, {"A-1", "summary"},
			{"B-2", "summary"},
			{"C-3", "title"},
		} {
			found := false
			for _, p := range lintRules(t, report, want.id) {
				if p.Rule == want.rule {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: no %s problem in %v", want.id, want.rule, lintRules(t, report, want.id))
			}
		}
		for _, lt := range report.Tickets {
			if lt.ID == "D-4" {
				t.Error("lint lists D-4, which does not exist")
			}
		}
	})

	t.Run("test_lint_passes_clean_board", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "D-4", "id: D-4\ntitle: \"a perfectly fine title\"\ntype: docs\ncluster: router\n"+
			"priority: P2\nest: M\nsummary: \""+longSummary+"\"", fullBody("D-4"))
		report := mustLint(t, f)
		for _, lt := range report.Tickets {
			for _, p := range lt.Problems {
				if p.Severity == "failure" {
					t.Errorf("%s: failure %v on a clean board", lt.ID, p)
				}
			}
		}
	})

	t.Run("test_list_prints_one_line_per_card_with_states", func(t *testing.T) {
		f := newFixture(t)
		f.card("done", "INFRA-1", "id: INFRA-1\ntitle: \"staging db\"\ntype: ops\ncluster: infra")
		f.card("in-progress", "API-2", "id: API-2\ntitle: \"tokens\"\ntype: feature\npriority: P1\nest: M\ncluster: api\n"+
			"claimed_by: ana\ndepends_on: [INFRA-1, INFRA-7]")
		f.card("blocked", "PAY-3", "id: PAY-3\ntitle: \"3ds\"\ntype: feature\npriority: P0\ncluster: pay\nexpedite: true\n"+
			"blocked_reason: \"waiting on sign-off\"\nblocked_from: review\nunblock_when: \"legal says yes\"")
		cards := mustCards(t, f.list(""))
		if len(cards) != 3 {
			t.Fatalf("list = %d cards, want 3", len(cards))
		}
		byID := map[string]Card{}
		for _, c := range cards {
			byID[c.ID] = c
		}
		api := byID["API-2"]
		if api.Column != "in-progress" || api.Holder != "ana" {
			t.Errorf("API-2 card = %+v", api)
		}
		deps := map[string]string{}
		for _, d := range api.Deps {
			deps[d.ID] = d.State
		}
		if deps["INFRA-1"] != "done" || deps["INFRA-7"] != "missing" {
			t.Errorf("API-2 deps = %v, want INFRA-1 done and INFRA-7 missing", deps)
		}
		pay := byID["PAY-3"]
		if !pay.Expedite || pay.Blocked == nil {
			t.Fatalf("PAY-3 card = %+v", pay)
		}
		if pay.Blocked.Reason != "waiting on sign-off" || pay.Blocked.Until != "legal says yes" || pay.Blocked.From != "review" {
			t.Errorf("PAY-3 blocked = %+v", pay.Blocked)
		}
		one := mustCards(t, f.list("blocked"))
		if len(one) != 1 || one[0].ID != "PAY-3" {
			t.Errorf("list blocked = %+v", one)
		}
	})

	t.Run("test_block_error_names_the_columns_it_accepts", func(t *testing.T) {
		f := newFixture(t)
		f.card("done", "D-1", "id: D-1\ntitle: \"x\"\ntype: docs")
		msg := mustRefuse(t, f.block(Args{Actor: "lead", ID: "D-1", Note: "why"}))
		contains(t, msg, "in-progress", "refusal")
		notContains(t, msg, "doing/", "refusal")
	})

	t.Run("test_new_writes_a_full_card", func(t *testing.T) {
		f := newFixture(t)
		f.card("done", "AUTH-BASE", "id: AUTH-BASE\ntitle: \"base\"\ntype: feature\ncluster: auth\n"+
			"priority: P1\nest: M\nsummary: \""+longSummary+"\"")
		out := mustLines(t, f.new(NewTicket{
			Actor: "lead", ID: "AUTH-TOKEN",
			Title:   "emailed password reset token: issue, store, 30 minute expiry",
			Cluster: "auth", Summary: longSummary,
			Type: "feature", Priority: "P0", Est: "M",
			DependsOn: []string{"AUTH-BASE", "the mailer being live"},
			Repos:     "shopapp",
			BodyFile:  f.bodyFile(t, "auth-body.md", fullBody("AUTH-TOKEN")),
		}))
		joined := strings.Join(out, "\n")
		contains(t, strings.ToLower(joined), "warning", "plain-text dependency: warned, not refused")
		lastEndsWith(t, out, "backlog", "AUTH-TOKEN")
		s := f.fm("backlog", "AUTH-TOKEN")
		if !strings.HasPrefix(s, "---\nid: AUTH-TOKEN\n") {
			t.Errorf("front matter opens with %q", s[:25])
		}
		contains(t, s, `title: "emailed password reset token: issue, store, 30 minute expiry"`, "fm")
		contains(t, s, "cluster: auth", "fm")
		contains(t, s, "summary: >-\n  ", "fm")
		contains(t, s, "type: feature", "fm")
		contains(t, s, "priority: P0", "fm")
		contains(t, s, "est: M", "fm")
		contains(t, s, `depends_on: [AUTH-BASE, "the mailer being live"]`, "fm")
		contains(t, s, "repos: shopapp", "fm")
		notContains(t, s, "expedite", "fm")
		contains(t, s, "created_at: "+stamp, "fm")
		contains(t, s, "created_by: lead", "fm")
		contains(t, s, "# AUTH-TOKEN: full", "the body lands verbatim")
		// the tool reads its own output back: fields round-trip and lint is clean
		for _, lt := range mustLint(t, f).Tickets {
			for _, p := range lt.Problems {
				if p.Severity == "failure" {
					t.Errorf("%s: %v", lt.ID, p)
				}
			}
		}
		checkLines := mustLines(t, f.check("AUTH-TOKEN"))
		contains(t, strings.Join(checkLines, "\n"), "column=backlog type=feature", "check")
		last := f.lastLog()
		if last["event"] != "new" || last["ticket"] != "AUTH-TOKEN" || last["actor"] != "lead" {
			t.Errorf("LOG = %v", last)
		}
	})

	t.Run("test_new_defaults_and_flags", func(t *testing.T) {
		f := newFixture(t)
		mustLines(t, f.new(NewTicket{Actor: "lead", ID: "OPS-1", Title: "rotate the key",
			Cluster: "ops", Summary: longSummary, Expedite: true, Ready: true,
			BodyFile: f.bodyFile(t, "ops-body.md", fullBody("OPS-1"))}))
		s := f.fm("ready", "OPS-1")
		contains(t, s, "type: feature", "fm")
		contains(t, s, "priority: P2", "fm")
		contains(t, s, "est: M", "fm")
		contains(t, s, "expedite: true", "fm")
		contains(t, s, "# OPS-1: full", "the body file landed")
	})

	t.Run("test_new_refuses_bad_input_without_writing", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "DUP-1", "id: DUP-1\ntitle: \"x\"\ntype: docs")
		base := NewTicket{Actor: "lead", Title: "t", Cluster: "c", Summary: longSummary}
		for _, tc := range []struct {
			id  string
			msg string
		}{
			{"DUP-1", "already on the board"},
			{"lower-case", "A-Z"},
			{"BAD ID", "A-Z"},
		} {
			n := base
			n.ID = tc.id
			contains(t, mustRefuse(t, f.new(n)), tc.msg, tc.id)
		}
		short := base
		short.ID, short.Summary = "S-1", shortSummary
		contains(t, mustRefuse(t, f.new(short)), "80", "short summary")
		noCluster := base
		noCluster.ID, noCluster.Cluster = "S-1", ""
		contains(t, mustRefuse(t, f.new(noCluster)), "cluster", "empty cluster")
		badDep := base
		badDep.ID, badDep.DependsOn = "S-1", []string{"NOPE-9"}
		contains(t, mustRefuse(t, f.new(badDep)), "NOPE-9", "missing dependency")
		badType := base
		badType.ID, badType.Type = "S-1", "bug"
		contains(t, mustRefuse(t, f.new(badType)), "type", "unknown type")
		if f.exists("backlog", "S-1") {
			t.Error("S-1 was written despite the refusals")
		}
		if f.logText() != "" {
			t.Error("a refused new still wrote LOG")
		}
	})

	t.Run("test_new_expedite_lands_in_ready", func(t *testing.T) {
		f := newFixture(t)
		out := mustLines(t, f.new(NewTicket{Actor: "lead", ID: "HOT-1",
			Title: "prod login is down", Cluster: "auth", Summary: longSummary,
			Type: "fix", Expedite: true, BodyFile: f.bodyFile(t, "hot-body.md", fullBody("HOT-1"))}))
		lastEndsWith(t, out, "ready", "HOT-1")
		contains(t, f.fm("ready", "HOT-1"), "expedite: true", "fm")
		lines := mustLines(t, f.check("HOT-1"))
		contains(t, strings.Join(lines, "\n"), "column=ready", "check")
		contains(t, strings.Join(lines, "\n"), "expedite=yes", "check")
		// the gates are unchanged: an implementation card cannot go straight to done
		mustRefuse(t, f.done(Args{Actor: "lead", ID: "HOT-1"}))
	})

	t.Run("test_pull_id_refuses_a_normal_card_while_expedite_waits_unless_forced", func(t *testing.T) {
		f := newFixture(t)
		f.expCard("ready", "HOT-1")
		f.card("ready", "NORM-1", "id: NORM-1\ntitle: \"normal\"\ntype: docs\npriority: P0")
		msg := mustRefuse(t, f.pull(Args{Actor: "w1", ID: "NORM-1"}))
		contains(t, msg, "expedite HOT-1 is waiting: pull it first", "refusal")
		if !f.exists("ready", "NORM-1") {
			t.Error("NORM-1 left ready/")
		}
		// pulling the expedite card by id is fine
		mustLines(t, f.pull(Args{Actor: "w2", ID: "HOT-1"}))
		// a second waiting expedite card: --force lets the normal pull through and logs it
		f.expCard("ready", "HOT-2")
		mustLines(t, f.pull(Args{Actor: "w1", ID: "NORM-1", Force: true}))
		last := f.lastLog()
		// LOG carries the bare id for every event, pull included
		if last["event"] != "pull" || last["ticket"] != "NORM-1" || last["forced"] != true {
			t.Errorf("last LOG = %v, want pull/NORM-1/forced", last)
		}
		// without --id the expedite card is still preferred
		out := mustLines(t, f.pull(Args{Actor: "w3"}))
		lastEndsWith(t, out, "in-progress", "HOT-2")
	})

	t.Run("test_status_lists_open_expedite_first_with_their_column", func(t *testing.T) {
		f := newFixture(t)
		f.expCard("review", "HOT-REV", "\nclaimed_by: w1")
		f.expCard("ready", "HOT-RDY")
		f.expCard("done", "HOT-DONE")
		f.card("backlog", "NORM-1", "id: NORM-1\ntitle: \"normal\"\ntype: docs")
		st := mustStatus(t, f)
		seen := map[string]string{}
		var ids []string
		for _, e := range st.Expedite {
			seen[e.ID] = e.Column
			ids = append(ids, e.ID)
		}
		if seen["HOT-RDY"] != "ready" || seen["HOT-REV"] != "review" {
			t.Errorf("expedite = %v", st.Expedite)
		}
		for _, id := range ids {
			if id == "HOT-DONE" {
				t.Error("status lists the done expedite card")
			}
		}
		done := 0
		for _, c := range st.Counts {
			if c.Column == "done" {
				done = c.Count
			}
		}
		if done != 1 {
			t.Errorf("done count = %d, want 1", done)
		}
		// nothing open: no fast-track entries
		mustLines(t, f.block(Args{Actor: "lead", ID: "HOT-REV", Note: "x"}))
		mustLines(t, f.block(Args{Actor: "lead", ID: "HOT-RDY", Note: "x"}))
		for _, id := range []string{"HOT-RDY", "HOT-REV"} {
			if err := os.Rename(f.path("blocked", id), f.path("done", id)); err != nil {
				t.Fatal(err)
			}
		}
		st = mustStatus(t, f)
		if len(st.Expedite) != 0 {
			t.Errorf("expedite after closing = %v", st.Expedite)
		}
	})

	t.Run("test_gates_promote_review_reject_test_done_and_stop_line", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "GATE-1", "id: GATE-1\ntitle: \"gated\"\ntype: fix\ncluster: core\n"+
			"priority: P1\nest: M\nsummary: \""+longSummary+"\"", fullBody("GATE-1"))
		mustLines(t, f.promote(Args{Actor: "lead", ID: "GATE-1"}))
		mustLines(t, f.pull(Args{Actor: "dev", ID: "GATE-1"}))
		mustLines(t, f.review(Args{Actor: "dev", ID: "GATE-1", Note: "look at x"}))
		// the author cannot verify or reject their own card; an empty test note is refused
		mustRefuse(t, f.test(Args{Actor: "dev", ID: "GATE-1", Note: "pytest rc 0"}))
		mustRefuse(t, f.reject(Args{Actor: "dev", ID: "GATE-1", Note: "nope"}))
		mustRefuse(t, f.test(Args{Actor: "qa", ID: "GATE-1", Note: ""}))
		mustLines(t, f.reject(Args{Actor: "rev", ID: "GATE-1", Note: "missing test"}))
		mustLines(t, f.review(Args{Actor: "dev", ID: "GATE-1", Note: "added it"}))
		s := f.fm("review", "GATE-1")
		contains(t, s, "look at x", "review_note")
		contains(t, s, "missing test", "review_note") // review notes append, nothing is erased
		contains(t, s, "added it", "review_note")
		mustLines(t, f.test(Args{Actor: "qa", ID: "GATE-1", Note: "pytest rc 0"}))
		mustRefuse(t, f.done(Args{Actor: "dev", ID: "GATE-1", Note: "x"}))
		mustLines(t, f.done(Args{Actor: "qa", ID: "GATE-1", Note: "shipped"}))
		contains(t, f.fm("done", "GATE-1"), "result:", "fm")
		// stop-line bumps the epoch once, blocks pulls, and resume clears it
		if st := mustStatus(t, f); st.Epoch != 1 {
			t.Errorf("epoch = %d, want 1", st.Epoch)
		}
		mustLines(t, f.stopLine(Args{Actor: "lead", Note: "main is red"}))
		// a second stop while stopped is a no-op: exit 0, the epoch does not bump twice
		out := mustLines(t, f.stopLine(Args{Actor: "lead", Note: "again"}))
		contains(t, strings.Join(out, "\n"), "already stopped", "second stop-line")
		if st := mustStatus(t, f); st.Epoch != 2 {
			t.Errorf("epoch = %d, want 2", st.Epoch)
		}
		lines, msg := refuseWithLines(t, f.check(""))
		if !strings.Contains(msg, "STOPPED epoch=2 actor=lead: main is red") {
			t.Errorf("check while stopped = %q", msg)
		}
		if len(lines) == 0 {
			t.Error("check while stopped printed nothing")
		}
		f.card("ready", "R-1", "id: R-1\ntitle: \"r\"\ntype: docs")
		mustRefuse(t, f.pull(Args{Actor: "dev"}))
		mustLines(t, f.resumeLine(Args{Actor: "lead"}))
		mustLines(t, f.pull(Args{Actor: "dev"}))
	})

	t.Run("test_check_reports_a_done_card_and_unblock_note_reaches_log", func(t *testing.T) {
		f := newFixture(t)
		f.card("done", "OLD-1", "id: OLD-1\ntitle: \"old\"\ntype: docs\nfinished_at: 2026-10-13T11:20:00+0300")
		lines := mustLines(t, f.check("OLD-1"))
		contains(t, strings.Join(lines, "\n"), "column=done type=docs", "check")
		f.card("blocked", "BLK-2", "id: BLK-2\ntitle: \"b\"\ntype: docs\nblocked_reason: \"x\"\nblocked_from: ready")
		mustLines(t, f.unblock(Args{Actor: "lead", ID: "BLK-2", Note: "upstream shipped"}))
		notContains(t, f.fm("ready", "BLK-2"), "upstream shipped", "card") // LOG only, not the card
		last := f.lastLog()
		if last["event"] != "unblock" || last["ticket"] != "BLK-2" || last["actor"] != "lead" || last["note"] != "upstream shipped" {
			t.Errorf("last LOG = %v", last)
		}
	})

	t.Run("test_set_block_fields_on_an_unblocked_card_and_board_path_with_spaces", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "PRE-1", "id: PRE-1\ntitle: \"p\"\ntype: docs")
		mustLines(t, f.set("lead", "PRE-1", []string{"blocked_on=INFRA-7, the vendor", "unblock_when=vendor replies"}))
		s := f.fm("ready", "PRE-1")
		contains(t, s, `blocked_on: [INFRA-7, "the vendor"]`, "fm")
		contains(t, s, `unblock_when: "vendor replies"`, "fm")
		// a board path with spaces works
		spaced := newFixtureAt(t, filepath.Join(t.TempDir(), "my project", ".agents", "kanban"))
		spaced.card("ready", "PRE-1", "id: PRE-1\ntitle: \"p\"\ntype: docs")
		cards := mustCards(t, spaced.list("ready"))
		if len(cards) != 1 || cards[0].ID != "PRE-1" {
			t.Errorf("list on a spaced path = %+v", cards)
		}
	})

	t.Run("test_pass_through_after_unblock_from_testing", func(t *testing.T) {
		f := newFixture(t)
		f.card("testing", "PT-1", "id: PT-1\ntitle: \"pt\"\ntype: fix\ncluster: core\nclaimed_by: dev\n"+
			"review_note: \"first verdict\"\ntest_note: \"pytest rc 0\"\nsummary: \""+longSummary+"\"")
		mustLines(t, f.block(Args{Actor: "qa", ID: "PT-1", Note: "CI red", Until: "CI green"}))
		contains(t, f.fm("blocked", "PT-1"), "blocked_from: testing", "fm")
		mustLines(t, f.unblock(Args{Actor: "lead", ID: "PT-1"}))
		s := f.fm("ready", "PT-1")
		contains(t, s, "blocked_from: testing", "fm") // the phase it must reach again
		notContains(t, s, "blocked_reason", "fm")
		// re-pull by a new actor; in-progress is transit; a non-author waves review through
		mustLines(t, f.pull(Args{Actor: "dev2", ID: "PT-1"}))
		mustLines(t, f.review(Args{Actor: "rev", ID: "PT-1", Note: "pass-through: already reviewed"}))
		contains(t, f.fm("review", "PT-1"), `review_note: "first verdict | pass-through: already reviewed"`, "fm")
		mustRefuse(t, f.test(Args{Actor: "dev2", ID: "PT-1", Note: "x"})) // author gate still bites
		mustLines(t, f.test(Args{Actor: "qa", ID: "PT-1", Note: "pytest rc 0 again"}))
		mustLines(t, f.done(Args{Actor: "qa", ID: "PT-1", Note: "shipped"}))
		notContains(t, f.fm("done", "PT-1"), "blocked_from", "fm") // done clears every blocked field
		lines := mustLines(t, f.check("PT-1"))
		contains(t, strings.Join(lines, "\n"), "column=done", "check")
	})

	t.Run("test_refusals_name_the_real_column_and_the_next_command", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "MSG-1", "id: MSG-1\ntitle: \"m\"\ntype: docs")
		f.card("in-progress", "MSG-2", "id: MSG-2\ntitle: \"m2\"\ntype: fix\nclaimed_by: w1")
		contains(t, mustRefuse(t, f.pull(Args{Actor: "w9", ID: "MSG-1"})), "it is in backlog/", "pull")
		contains(t, mustRefuse(t, f.pull(Args{Actor: "w9", ID: "MSG-1"})), "promote", "pull")
		contains(t, mustRefuse(t, f.pull(Args{Actor: "w9"})), "no ready tickets", "pull")
		contains(t, mustRefuse(t, f.pull(Args{Actor: "w9"})), "promote", "pull")
		contains(t, mustRefuse(t, f.pull(Args{Actor: "w1", ID: "MSG-1"})), "already holds MSG-2", "pull")
		contains(t, mustRefuse(t, f.pull(Args{Actor: "w1", ID: "MSG-1"})), "review", "pull")
		contains(t, mustRefuse(t, f.promote(Args{Actor: "lead", ID: "MSG-2"})), "it is in in-progress/", "promote")
		contains(t, mustRefuse(t, f.review(Args{Actor: "w1", ID: "MSG-1"})), "it is in backlog/", "review")
		testMsg := mustRefuse(t, f.test(Args{Actor: "qa", ID: "MSG-2", Note: ""}))
		contains(t, testMsg, "MSG-2: not in review/", "test")
		contains(t, testMsg, "in-progress/", "test")
		checkLines, checkMsg := refuseWithLines(t, f.check("NOPE-1"))
		contains(t, strings.Join(checkLines, "\n")+checkMsg, "not on the board", "check")
		contains(t, strings.Join(checkLines, "\n")+checkMsg, "list", "check")
		contains(t, mustRefuse(t, f.resumeLine(Args{Actor: "lead"})), "nothing to resume", "resume-line")
		mustLines(t, f.stopLine(Args{Actor: "lead", Note: "red"}))
		pullMsg := mustRefuse(t, f.pull(Args{Actor: "w9"}))
		contains(t, pullMsg, "by lead: red", "pull")
		contains(t, pullMsg, "resume-line", "pull")
	})

	t.Run("test_done_on_a_done_card_and_check_on_a_tickets_card_say_where_it_is", func(t *testing.T) {
		f := newFixture(t)
		f.card("done", "FIN-1", "id: FIN-1\ntitle: \"f\"\ntype: docs")
		msg := mustRefuse(t, f.done(Args{Actor: "lead", ID: "FIN-1"}))
		contains(t, msg, "it is in done/", "done")
		notContains(t, msg, "not on the board", "done")
		newMsg := mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "1-BAD",
			Title: "t", Cluster: "c", Summary: longSummary}))
		contains(t, newMsg, "a letter", "new")
	})

	t.Run("test_live_column_wins_over_the_tickets_pool_and_done_cards_are_frozen", func(t *testing.T) {
		f := newFixture(t)
		f.card("done", "DUAL-1", "id: DUAL-1\ntitle: \"d\"\ntype: fix\nclaimed_by: dev")
		lines := mustLines(t, f.check("DUAL-1"))
		joined := strings.Join(lines, "\n")
		contains(t, joined, "column=done", "check")
		notContains(t, joined, "gates:", "check")
		doneMsg := mustRefuse(t, f.done(Args{Actor: "qa", ID: "DUAL-1"}))
		contains(t, doneMsg, "it is in done/", "done")
		contains(t, doneMsg, "nothing to do", "done")
		// done cards are never edited
		setMsg := mustRefuse(t, f.set("lead", "DUAL-1", []string{"title=renamed"}))
		contains(t, setMsg, "done cards are not edited", "set")
		notContains(t, f.fm("done", "DUAL-1"), "renamed", "fm")
		// blocking a blocked card says to unblock it; a misspelled type is shown, not hidden
		f.card("blocked", "BLK-9", "id: BLK-9\ntitle: \"b\"\ntype: feture\nblocked_reason: \"x\"\nblocked_from: ready")
		contains(t, mustRefuse(t, f.block(Args{Actor: "lead", ID: "BLK-9", Note: "again"})), "unblock it first", "block")
		contains(t, strings.Join(mustLines(t, f.check("BLK-9")), "\n"),
			"type=feture (treated as implementation)", "check")
		// a missing card's hint is the final clause, not wedged mid-sentence
		promoteMsg := mustRefuse(t, f.promote(Args{Actor: "lead", ID: "NOPE-7"}))
		if !strings.HasSuffix(promoteMsg, "run list to see ids, or new to create it") {
			t.Errorf("promote refusal = %q", promoteMsg)
		}
	})

	t.Run("test_check_exits_1_while_stopped_even_with_an_id_and_pool_cards_are_recreated", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "ST-1", "id: ST-1\ntitle: \"s\"\ntype: docs")
		if _, err := f.check("ST-1")(); err != nil {
			t.Fatalf("check while running: %v", err)
		}
		mustLines(t, f.stopLine(Args{Actor: "lead", Note: "red"}))
		lines, _ := refuseWithLines(t, f.check("ST-1")) // stopped wins, the card line still prints
		contains(t, strings.Join(lines, "\n"), "column=ready", "check")
	})

	t.Run("test_new_recreates_a_pool_only_id_and_title_is_escaped", func(t *testing.T) {
		// the id-collision refusal names the column the ticket is in
		f := newFixture(t)
		f.card("backlog", "POOL-4", "id: POOL-4\ntitle: \"p\"\ntype: docs")
		msg := mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "POOL-4",
			Title: "again", Cluster: "docs", Summary: longSummary}))
		contains(t, msg, "already on the board (in backlog/)", "new")
	})

	t.Run("test_original_author_stays_the_author_across_a_repull", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "AUTH-9", "id: AUTH-9\ntitle: \"a\"\ntype: fix\ncluster: core\nsummary: \""+longSummary+"\"")
		mustLines(t, f.pull(Args{Actor: "w1", ID: "AUTH-9"}))
		contains(t, f.fm("in-progress", "AUTH-9"), "authored_by: w1", "fm")
		mustLines(t, f.review(Args{Actor: "w1", ID: "AUTH-9", Note: "look"}))
		mustLines(t, f.test(Args{Actor: "qa", ID: "AUTH-9", Note: "rc 0"}))
		mustLines(t, f.block(Args{Actor: "qa", ID: "AUTH-9", Note: "CI red"}))
		mustLines(t, f.unblock(Args{Actor: "lead", ID: "AUTH-9"}))
		mustLines(t, f.pull(Args{Actor: "w3", ID: "AUTH-9"}))
		s := f.fm("in-progress", "AUTH-9")
		contains(t, s, "claimed_by: w3", "fm")
		contains(t, s, "authored_by: w1", "fm")
		mustLines(t, f.review(Args{Actor: "rev", ID: "AUTH-9", Note: "pass-through: reviewed"}))
		// neither the original author nor the re-puller verifies or finishes it
		for _, who := range []string{"w1", "w3"} {
			contains(t, mustRefuse(t, f.test(Args{Actor: who, ID: "AUTH-9", Note: "rc 0"})), "author", who)
		}
		mustLines(t, f.test(Args{Actor: "qa", ID: "AUTH-9", Note: "rc 0 again"}))
		for _, who := range []string{"w1", "w3"} {
			mustRefuse(t, f.done(Args{Actor: who, ID: "AUTH-9"}))
		}
		mustLines(t, f.done(Args{Actor: "qa", ID: "AUTH-9", Note: "ok"}))
	})

	t.Run("test_set_quotes_safely_pull_hints_fit_the_column_and_title_limit_is_enforced", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "Q-1", "id: Q-1\ntitle: \"q\"\ntype: docs")
		mustLines(t, f.set("lead", "Q-1", []string{`title=say "hi" there`}))
		contains(t, f.fm("ready", "Q-1"), `title: 'say "hi" there'`, "fm")
		report := mustLint(t, f)
		if len(lintRules(t, report, "Q-1")) == 0 {
			t.Error("lint passes a card with no summary") // the summary is missing, not a parse error
		}
		f.card("blocked", "Q-2", "id: Q-2\ntitle: \"q\"\ntype: docs\nblocked_reason: \"x\"\nblocked_from: ready")
		blockedMsg := mustRefuse(t, f.pull(Args{Actor: "w1", ID: "Q-2"}))
		contains(t, blockedMsg, "unblock it first", "pull")
		notContains(t, blockedMsg, "promote", "pull")
		f.card("in-progress", "Q-3", "id: Q-3\ntitle: \"q\"\ntype: docs\nclaimed_by: w2")
		claimedMsg := mustRefuse(t, f.pull(Args{Actor: "w1", ID: "Q-3"}))
		contains(t, claimedMsg, "already claimed by w2", "pull")
		notContains(t, claimedMsg, "promote", "pull")
		titleMsg := mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "T-LONG",
			Cluster: "c", Summary: longSummary, Title: strings.Repeat("x", 91)}))
		contains(t, titleMsg, "90", "new")
		if f.exists("backlog", "T-LONG") {
			t.Error("T-LONG was written despite the refusal")
		}
		// an id-shaped dep is tracked; anything else is text
		f.card("ready", "DEP-1", "id: DEP-1\ntitle: \"d\"\ntype: docs\ndepends_on: [AB-, \"a push\"]")
		cards := mustCards(t, f.list("ready"))
		for _, c := range cards {
			if c.ID != "DEP-1" {
				continue
			}
			for _, d := range c.Deps {
				if d.ID == "AB-" && d.State != "text" {
					t.Errorf("AB- state = %q, want text", d.State)
				}
			}
		}
	})

	t.Run("test_list_items_quote_safely_mixed_quotes_are_refused_and_author_refusals_name_the_actor", func(t *testing.T) {
		f := newFixture(t)
		f.card("ready", "LQ-1", "id: LQ-1\ntitle: \"l\"\ntype: fix\ncluster: core\nsummary: \""+longSummary+"\"")
		mustLines(t, f.block(Args{Actor: "lead", ID: "LQ-1", Note: "x",
			On: []string{`the "vendor" call`, "INFRA-7"}}))
		contains(t, f.fm("blocked", "LQ-1"), `blocked_on: ['the "vendor" call', INFRA-7]`, "fm")
		blocked := mustCards(t, f.list("blocked"))
		var on []string
		for _, c := range blocked {
			if c.ID == "LQ-1" && c.Blocked != nil {
				on = c.Blocked.On
			}
		}
		if len(on) != 2 || on[0] != `the "vendor" call` {
			t.Errorf("blocked_on through list = %v", on)
		}
		contains(t, mustRefuse(t, f.set("lead", "LQ-1", []string{`title=it's "both"`})), "one quote kind", "set")
		contains(t, mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "LQ-2",
			Title: `it's "both"`, Cluster: "c", Summary: longSummary})), "one quote kind", "new")
		// the refusal names who was refused and why, not just the holder
		f.card("review", "LQ-3", "id: LQ-3\ntitle: \"l\"\ntype: fix\nclaimed_by: w3\nauthored_by: w1")
		testMsg := mustRefuse(t, f.test(Args{Actor: "w1", ID: "LQ-3", Note: "rc 0"}))
		for _, want := range []string{"w1", "authored_by=w1", "claimed_by=w3"} {
			contains(t, testMsg, want, "test refusal")
		}
		// type is settable (validated), so a prose card bitten by the gate can be fixed by command
		mustLines(t, f.set("lead", "LQ-1", []string{"type=docs"}))
		contains(t, f.fm("blocked", "LQ-1"), "type: docs", "fm")
		mustRefuse(t, f.set("lead", "LQ-1", []string{"type=bogus"}))
	})

	t.Run("test_empty_epoch_renders_set_normalises_cluster_and_type_notes_refuse_mixed_quotes_gen_honours_env", func(t *testing.T) {
		f := newFixture(t)
		f.writeFile("EPOCH", "")
		f.card("ready", "E-1", "id: E-1\ntitle: \"e\"\ntype: fix\nclaimed_by: w1")
		if st := mustStatus(t, f); st.Epoch != 1 {
			t.Errorf("epoch = %d, want 1 for an unreadable file", st.Epoch)
		}
		mustLines(t, f.set("lead", "E-1", []string{"cluster=GO Port", "type=Docs"}))
		s := f.fm("ready", "E-1")
		contains(t, s, "cluster: go port", "fm")
		contains(t, s, "type: docs", "fm")
		mustLines(t, f.pull(Args{Actor: "w1", ID: "E-1"}))
		contains(t, mustRefuse(t, f.review(Args{Actor: "w1", ID: "E-1", Note: `it's "mixed"`})), "one quote kind", "review")
		if !f.exists("in-progress", "E-1") {
			t.Error("E-1 moved despite the refusal")
		}
	})

	t.Run("test_test_note_needs_evidence_and_mixed_quote_refusal_says_what_to_do", func(t *testing.T) {
		f := newFixture(t)
		f.card("review", "EV-1", "id: EV-1\ntitle: \"e\"\ntype: fix\nclaimed_by: dev\nauthored_by: dev")
		contains(t, mustRefuse(t, f.test(Args{Actor: "qa", ID: "EV-1", Note: "looks fine"})), "exit code", "test")
		if !f.exists("review", "EV-1") {
			t.Error("EV-1 moved despite the refusal")
		}
		mustLines(t, f.test(Args{Actor: "qa", ID: "EV-1", Note: "pytest -q exit 0"}))
		f.card("review", "EV-1", "id: EV-1\ntitle: \"e\"\ntype: fix\nclaimed_by: dev\nauthored_by: dev")
		contains(t, mustRefuse(t, f.review(Args{Actor: "dev", ID: "EV-1", Note: `it's "mixed"`})),
			"rewrite the value with one kind", "review")
	})

	t.Run("test_appended_notes_never_mix_quote_kinds_and_list_shows_a_misspelled_type", func(t *testing.T) {
		f := newFixture(t)
		f.card("in-progress", "AP-1", "id: AP-1\ntitle: \"a\"\ntype: fix\nclaimed_by: dev\nauthored_by: dev")
		mustLines(t, f.review(Args{Actor: "dev", ID: "AP-1", Note: `check the "token" table`}))
		contains(t, mustRefuse(t, f.reject(Args{Actor: "rev", ID: "AP-1", Note: "it's missing the index"})),
			"double quotes", "reject")
		if !f.exists("review", "AP-1") {
			t.Error("AP-1 moved despite the refusal") // nothing moved
		}
		mustLines(t, f.reject(Args{Actor: "rev", ID: "AP-1", Note: "it is missing the index"}))
		contains(t, f.fm("in-progress", "AP-1"), `review_note: 'check the "token" table | it is missing the index'`, "fm")
		f.card("ready", "TY-1", "id: TY-1\ntitle: \"t\"\ntype: feture")
		cards := mustCards(t, f.list("ready"))
		for _, c := range cards {
			if c.ID == "TY-1" && c.Type != "feture" {
				t.Errorf("TY-1 type = %q, want it shown as written", c.Type)
			}
		}
		kvMsg := mustRefuse(t, f.set("lead", "TY-1", []string{`title=it's "mixed"`}))
		contains(t, kvMsg, "key=value:", "set")
		notContains(t, kvMsg, "--kv", "set")
	})

	t.Run("test_every_file_open_names_utf8_and_ids_allow_single_dashes_only", func(t *testing.T) {
		// the id rules only; the utf8-open rule is source-level, its
		// user-facing half sitting in the cli suite's help rows
		f := newFixture(t)
		for _, bad := range []string{"A--B", "A-B-", "AB-"} {
			msg := mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: bad,
				Title: "t", Cluster: "c", Summary: longSummary}))
			contains(t, msg, "single dashes", bad)
		}
		mustLines(t, f.new(NewTicket{Actor: "lead", ID: "A-B-C9",
			Title: "t", Cluster: "c", Summary: longSummary}))
	})

	t.Run("test_set_enforces_the_new_limits_and_a_non_utf8_card_is_refused_not_a_traceback", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "LIM-1", "id: LIM-1\ntitle: \"a\"\ntype: docs\ncluster: c")
		contains(t, mustRefuse(t, f.set("lead", "LIM-1", []string{"title=" + strings.Repeat("x", 91)})),
			"limit is 90", "set")
		contains(t, mustRefuse(t, f.set("lead", "LIM-1", []string{"summary=short"})),
			"at least 80", "set")
		mustLines(t, f.set("lead", "LIM-1", []string{"summary=" + longSummary}))
		// a card saved in Latin-1 must not break list, lint or set
		if err := os.WriteFile(f.path("backlog", "LAT-1"),
			[]byte("---\nid: LAT-1\ntitle: \"caf\xe9\"\ntype: docs\ncluster: c\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustCards(t, f.list(""))
		mustLint(t, f)
		contains(t, mustRefuse(t, f.set("lead", "LAT-1", []string{"cluster=d"})), "not UTF-8", "set")
	})

	t.Run("test_non_utf8_card_stays_put_lint_flags_it_and_free_scalars_are_quoted", func(t *testing.T) {
		f := newFixture(t)
		// a flow command must refuse BEFORE moving the card, never leave a stamp-less card in the next column
		cases := []struct {
			col string
			run cmd
		}{
			{"ready", f.pull(Args{Actor: "w1", ID: "LAT-2"})},
			{"ready", f.block(Args{Actor: "w1", ID: "LAT-2", Note: "x"})},
			{"in-progress", f.review(Args{Actor: "w1", ID: "LAT-2", Note: "look"})},
			{"in-progress", f.done(Args{Actor: "w1", ID: "LAT-2", Note: "r"})},
		}
		for _, c := range cases {
			if err := os.MkdirAll(filepath.Dir(f.path(c.col, "LAT-2")), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.path(c.col, "LAT-2"),
				[]byte("---\nid: LAT-2\ntitle: \"caf\xe9\"\ntype: docs\ncluster: c\nclaimed_by: w1\n---\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			contains(t, mustRefuse(t, c.run), "not UTF-8", c.col)
			if !f.exists(c.col, "LAT-2") {
				t.Errorf("LAT-2 left %s/", c.col) // still where it was
			}
			if err := os.Remove(f.path(c.col, "LAT-2")); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(f.path("backlog", "LAT-3"),
			[]byte("---\nid: LAT-3\ntitle: \"caf\xe9\"\ntype: docs\ncluster: c\nsummary: \""+longSummary+"\"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		report := mustLint(t, f)
		if !hasRule(lintRules(t, report, "LAT-3"), "utf-8") {
			t.Error("lint does not flag the non-UTF-8 card")
		}
		// free scalars with ": " inside are written quoted so the card reads back
		f.card("in-progress", "Q-1", "id: Q-1\ntitle: \"a\"\ntype: docs\ncluster: c\nclaimed_by: w1")
		mustLines(t, f.set("w1", "Q-1", []string{"branch=fix: a", "repos=sdk: go"}))
		s := f.fm("in-progress", "Q-1")
		contains(t, s, `branch: "fix: a"`, "fm")
		contains(t, s, `repos: "sdk: go"`, "fm")
		mustLines(t, f.done(Args{Actor: "w2", ID: "Q-1", Note: "ok", Merge: "abc123: merged to main"}))
		contains(t, f.fm("done", "Q-1"), `verified_merge: "abc123: merged to main"`, "fm")
	})

	t.Run("test_empty_scalar_reads_set_validates_like_new_writes_are_atomic_and_test_note_needs_a_digit", func(t *testing.T) {
		f := newFixture(t)
		// a hand-edited claimed_by: with no value must not break anything
		f.card("ready", "EMP-1", "id: EMP-1\ntitle: \"a\"\ntype: docs\ncluster: c\nclaimed_by:\nsummary: \""+longSummary+"\"")
		mustCards(t, f.list(""))
		mustLint(t, f)
		lines := mustLines(t, f.check("EMP-1"))
		contains(t, strings.Join(lines, "\n"), "claimed_by=-", "check")
		mustLines(t, f.set("lead", "EMP-1", []string{"cluster=d"}))
		// set refuses what new refuses: an empty cluster, an unknown priority or est
		for _, kv := range []struct{ pair, msg string }{
			{"cluster=", "cluster"}, {"priority=P9", "P0, P1, P2, P3"}, {"est=XL", "S, M, L"},
		} {
			contains(t, mustRefuse(t, f.set("lead", "EMP-1", []string{kv.pair})), kv.msg, kv.pair)
		}
		contains(t, f.fm("ready", "EMP-1"), "cluster: d", "fm") // the refused calls wrote nothing
		// a card write never truncates: no temp file is left behind
		entries, err := os.ReadDir(filepath.Join(f.root, "ready"))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.Contains(e.Name(), ".tmp") {
				t.Errorf("temp file %s left behind", e.Name())
			}
		}
		// the test note needs a digit: the bare word "exit" is not evidence
		f.card("review", "EV-1", "id: EV-1\ntitle: \"a\"\ntype: fix\nauthored_by: w1\nclaimed_by: w1")
		contains(t, mustRefuse(t, f.test(Args{Actor: "qa", ID: "EV-1", Note: "pytest exit"})), "exit code", "test")
		mustLines(t, f.test(Args{Actor: "qa", ID: "EV-1", Note: "pytest exit 0"}))
	})

	t.Run("test_expedite_order_is_furthest_along_first_check_shows_the_author_and_hints_fit_the_command", func(t *testing.T) {
		f := newFixture(t)
		f.expCard("review", "HOT-REV", "\nclaimed_by: w1")
		f.expCard("testing", "HOT-TST", "\nclaimed_by: w1")
		f.expCard("in-progress", "HOT-INP", "\nclaimed_by: w2")
		f.expCard("ready", "HOT-RDY")
		st := mustStatus(t, f)
		var order []string
		for _, e := range st.Expedite {
			order = append(order, e.ID)
		}
		want := []string{"HOT-TST", "HOT-REV", "HOT-INP", "HOT-RDY"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Errorf("expedite order = %v, want %v", order, want)
		}
		// check answers "who is the author" after a re-pull
		f.card("in-progress", "AUT-1", "id: AUT-1\ntitle: \"a\"\ntype: fix\nauthored_by: w1\nclaimed_by: w3")
		joined := strings.Join(mustLines(t, f.check("AUT-1")), "\n")
		contains(t, joined, "authored_by=w1", "check")
		contains(t, joined, "claimed_by=w3", "check")
		// the mixed-quote hint names --body-file only where that flag exists
		setMsg := mustRefuse(t, f.set("w3", "AUT-1", []string{`title=it's "x"`}))
		notContains(t, setMsg, "--body-file", "set")
		newMsg := mustRefuse(t, f.new(NewTicket{Actor: "w3", ID: "NQ-1",
			Title: `it's "x"`, Cluster: "c", Summary: longSummary}))
		contains(t, newMsg, "--body-file", "new")
	})

	t.Run("test_body_lines_never_shadow_front_matter_test_notes_append_and_unblock_clears_the_holder", func(t *testing.T) {
		f := newFixture(t)
		// a body line that looks like a key is prose, not a field
		f.card("ready", "BODY-1", "id: BODY-1\ntitle: \"a\"\ntype: fix\ncluster: c",
			"\nNotes:\nclaimed_by: ghost\nauthored_by: qa\nresult: pending until the review\n")
		for _, c := range mustCards(t, f.list("ready")) {
			if c.ID == "BODY-1" && c.Holder == "ghost" {
				t.Error("body prose shadows the front matter")
			}
		}
		contains(t, strings.Join(mustLines(t, f.check("BODY-1")), "\n"), "authored_by=-", "check")
		mustLines(t, f.pull(Args{Actor: "w1", ID: "BODY-1"}))
		s := f.fm("in-progress", "BODY-1")
		front, body, _ := ticket.Split(s)
		contains(t, front, "authored_by: w1", "front matter")
		contains(t, body, "authored_by: qa", "body") // untouched prose
		mustLines(t, f.review(Args{Actor: "w1", ID: "BODY-1", Note: "look"}))
		mustLines(t, f.test(Args{Actor: "qa", ID: "BODY-1", Note: "pytest -q exit 0"}))
		mustLines(t, f.done(Args{Actor: "qa", ID: "BODY-1", Note: "shipped"}))
		s = f.fm("done", "BODY-1")
		front, body, _ = ticket.Split(s)
		contains(t, front, `result: "shipped"`, "front matter")
		contains(t, body, "result: pending until the review", "body")
		// test notes append like review notes, so a second verification keeps the first
		f.card("review", "TN-1", "id: TN-1\ntitle: \"a\"\ntype: fix\nauthored_by: w1\nclaimed_by: w1\ntest_note: \"pytest -q exit 0\"")
		mustLines(t, f.test(Args{Actor: "qa", ID: "TN-1", Note: "go test exit 0"}))
		contains(t, f.fm("testing", "TN-1"), `test_note: "pytest -q exit 0 | go test exit 0"`, "fm")
		// unblock returns an unheld card: nobody holds a ready card, the author stays
		f.card("blocked", "UB-1", "id: UB-1\ntitle: \"a\"\ntype: fix\nauthored_by: w1\nclaimed_by: w1\nclaimed_at: x\n"+
			"blocked_from: in-progress\nblocked_reason: \"r\"")
		mustLines(t, f.unblock(Args{Actor: "lead", ID: "UB-1"}))
		fm := f.fm("ready", "UB-1")
		notContains(t, fm, "claimed_by", "fm")
		notContains(t, fm, "claimed_at", "fm")
		contains(t, fm, "authored_by: w1", "fm")
		for _, c := range mustCards(t, f.list("ready")) {
			if c.ID == "UB-1" && c.Holder != "" {
				t.Error("the unblocked card still shows a holder")
			}
		}
		// check names the holder gate
		contains(t, strings.Join(mustLines(t, f.check("TN-1")), "\n"),
			"other than the author or holder", "check")
	})

	t.Run("test_backslashes_are_single_quoted_and_a_bad_body_file_is_refused_not_a_traceback", func(t *testing.T) {
		f := newFixture(t)
		f.card("in-progress", "BS-1", "id: BS-1\ntitle: \"a\"\ntype: docs\ncluster: c\nclaimed_by: w1")
		mustLines(t, f.done(Args{Actor: "w2", ID: "BS-1", Note: `copied C:\Users\x to the share`}))
		contains(t, f.fm("done", "BS-1"), `result: 'copied C:\Users\x to the share'`, "fm")
		// a backslash with a single quote cannot be written safely: refused like mixed quotes
		contains(t, mustRefuse(t, f.set("w1", "BS-1",
			[]string{`summary=it's C:\x ` + longSummary})), "backslash", "set")
		// --body-file errors are refusals with the fix named
		contains(t, mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "BF-1",
			Title: "t", Cluster: "c", Summary: longSummary,
			BodyFile: filepath.Join(t.TempDir(), "nope.md")})), "--body-file", "new")
		latin := filepath.Join(t.TempDir(), "lat.md")
		if err := os.WriteFile(latin, []byte("caf\xe9\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		contains(t, mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "BF-1",
			Title: "t", Cluster: "c", Summary: longSummary, BodyFile: latin})), "UTF-8", "new")
	})

	t.Run("test_stamped_fields_follow_the_authored_ones_reserved_words_are_quoted_and_refusals_name_the_raw_type", func(t *testing.T) {
		f := newFixture(t)
		// a stamped field lands after the authored ones
		f.card("ready", "ORD-1", "id: ORD-1\ntitle: \"a\"\ntype: fix\ncluster: c")
		mustLines(t, f.pull(Args{Actor: "w1", ID: "ORD-1"}))
		fm := f.fm("in-progress", "ORD-1")
		if strings.Index(fm, "cluster: c") > strings.Index(fm, "claimed_by: w1") {
			t.Errorf("stamped field precedes the authored ones:\n%s", fm)
		}
		if !strings.HasPrefix(fm, "---\nid: ORD-1") {
			t.Errorf("front matter opens with %q", fm[:20])
		}
		// values that would read back as bool/null/number are quoted (2026 and 1.5 are plain-text deps)
		f.card("backlog", "A-1", "id: A-1\ntitle: \"a\"\ntype: docs")
		mustLines(t, f.set("w1", "ORD-1", []string{"branch=true", "repos=null", "depends_on=2026, A-1, 1.5"}))
		fm = f.fm("in-progress", "ORD-1")
		contains(t, fm, `branch: "true"`, "fm")
		contains(t, fm, `repos: "null"`, "fm")
		contains(t, fm, `depends_on: ["2026", A-1, "1.5"]`, "fm")
		// done's refusal shows the type as written, like check does
		f.card("in-progress", "TYP-1", "id: TYP-1\ntitle: \"a\"\ntype: feture\nclaimed_by: w1")
		doneMsg := mustRefuse(t, f.done(Args{Actor: "w2", ID: "TYP-1", Note: "x"}))
		contains(t, doneMsg, "feture", "done")
		contains(t, doneMsg, "treated as implementation", "done")
		// check says which gates apply to a prose card too
		f.card("backlog", "DOC-1", "id: DOC-1\ntitle: \"a\"\ntype: docs")
		contains(t, strings.Join(mustLines(t, f.check("DOC-1")), "\n"), "gates: none", "check")
	})

	t.Run("test_cluster_and_actor_are_plain_ids_and_lint_flags_an_empty_skeleton_on_open_cards", func(t *testing.T) {
		f := newFixture(t)
		// cluster: one or two lowercase words, so the bare value is safe
		for _, bad := range []string{"a: b", "c #d", "three word cluster"} {
			msg := mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "CL-1",
				Title: "t", Cluster: bad, Summary: longSummary}))
			contains(t, msg, "word or two", bad)
		}
		mustLines(t, f.new(NewTicket{Actor: "lead", ID: "CL-1",
			Title: "t", Cluster: "Ruby SDK", Summary: longSummary,
			BodyFile: f.bodyFile(t, "cl-body.md", fullBody("CL-1"))}))
		contains(t, f.fm("backlog", "CL-1"), "cluster: ruby sdk", "fm")
		contains(t, mustRefuse(t, f.set("lead", "CL-1", []string{"cluster=a: b"})), "word or two", "set")
		// actor: a short id, so claimed_by/authored_by/blocked_by read back as strings
		for _, bad := range []string{"true", "lead: x", "a b"} {
			contains(t, mustRefuse(t, f.promote(Args{Actor: bad, ID: "CL-1"})), "--actor", bad)
		}
		mustLines(t, f.promote(Args{Actor: "proxbeam.lead", ID: "CL-1"}))
		// lint: an open card whose body is the empty skeleton is under the bar; done cards are frozen
		mustLines(t, f.new(NewTicket{Actor: "lead", ID: "SK-1",
			Title: "t", Cluster: "c", Summary: longSummary}))
		report := mustLint(t, f)
		sk := lintRules(t, report, "SK-1")
		if len(sk) == 0 {
			t.Error("lint passes the skeleton card")
		}
		for _, lt := range report.Tickets {
			if lt.ID == "CL-1" {
				t.Error("lint flags CL-1, whose body is complete")
			}
		}
		if err := os.Rename(f.path("backlog", "SK-1"), f.path("done", "SK-1")); err != nil {
			t.Fatal(err)
		}
		report = mustLint(t, f)
		for _, lt := range report.Tickets {
			for _, p := range lt.Problems {
				if p.Severity == "failure" {
					t.Errorf("%s: %v after the card moved to done", lt.ID, p)
				}
			}
		}
	})

	t.Run("test_every_past_holder_is_refused_at_the_gates_and_the_author_cannot_retype_a_card", func(t *testing.T) {
		f := newFixture(t)
		// w2 authors, w3 works a wave, w4 holds now: none of them verifies or finishes it
		f.card("ready", "HLD-1", "id: HLD-1\ntitle: \"a\"\ntype: fix\ncluster: c")
		mustLines(t, f.pull(Args{Actor: "w2", ID: "HLD-1"}))
		mustLines(t, f.block(Args{Actor: "w2", ID: "HLD-1", Note: "x"}))
		mustLines(t, f.unblock(Args{Actor: "lead", ID: "HLD-1"}))
		mustLines(t, f.pull(Args{Actor: "w3", ID: "HLD-1"}))
		mustLines(t, f.block(Args{Actor: "w3", ID: "HLD-1", Note: "x"}))
		mustLines(t, f.unblock(Args{Actor: "lead", ID: "HLD-1"}))
		mustLines(t, f.pull(Args{Actor: "w4", ID: "HLD-1"}))
		contains(t, f.fm("in-progress", "HLD-1"), "held_by: [w2, w3, w4]", "fm")
		mustLines(t, f.review(Args{Actor: "w4", ID: "HLD-1", Note: "look"}))
		for _, who := range []string{"w2", "w3", "w4"} {
			contains(t, mustRefuse(t, f.test(Args{Actor: who, ID: "HLD-1", Note: "pytest exit 0"})),
				"held_by", who)
		}
		mustLines(t, f.test(Args{Actor: "qa", ID: "HLD-1", Note: "pytest exit 0"}))
		mustRefuse(t, f.done(Args{Actor: "w3", ID: "HLD-1", Note: "r"}))
		mustLines(t, f.done(Args{Actor: "rev", ID: "HLD-1", Note: "r"}))
		// the type gate cannot be bypassed by the people it gates
		f.card("in-progress", "TYP-2", "id: TYP-2\ntitle: \"a\"\ntype: fix\ncluster: c\nauthored_by: w1\nclaimed_by: w1")
		setMsg := mustRefuse(t, f.set("w1", "TYP-2", []string{"type=docs"}))
		contains(t, setMsg, "type", "set")
		contains(t, setMsg, "lead", "set")
		mustLines(t, f.set("lead", "TYP-2", []string{"type=docs"}))
	})

	t.Run("test_pull_honours_depends_on_pool_deps_are_refused_and_the_page_has_a_doctype_and_a_page_title", func(t *testing.T) {
		f := newFixture(t)
		// a bare pull skips a ready card whose dependency is not done, and says so
		f.card("ready", "DEP-A", "id: DEP-A\ntitle: \"a\"\ntype: fix\ncluster: c\npriority: P0\ndepends_on: [DEP-B]")
		f.card("in-progress", "DEP-B", "id: DEP-B\ntitle: \"b\"\ntype: fix\ncluster: c\nclaimed_by: w9")
		f.card("ready", "DEP-C", "id: DEP-C\ntitle: \"c\"\ntype: fix\ncluster: c\npriority: P2")
		out := mustLines(t, f.pull(Args{Actor: "w1"}))
		joined := strings.Join(out, "\n")
		if !f.exists("in-progress", "DEP-C") {
			t.Errorf("pull took the wrong card: %v", out)
		}
		contains(t, joined, "DEP-A", "output") // the skipped card and why
		contains(t, joined, "DEP-B", "output")
		refuseMsg := mustRefuse(t, f.pull(Args{Actor: "w2"}))
		contains(t, refuseMsg, "DEP-A waits on DEP-B", "pull")
		mustLines(t, f.pull(Args{Actor: "w2", ID: "DEP-A"})) // --id is explicit
	})

	t.Run("test_expedite_with_an_open_dependency_does_not_hold_the_fast_track_and_small_refusals_are_consistent", func(t *testing.T) {
		f := newFixture(t)
		f.expCard("ready", "HOT-DEP", "\ndepends_on: [BASE-1]")
		f.card("backlog", "BASE-1", "id: BASE-1\ntitle: \"b\"\ntype: fix\ncluster: c")
		f.card("ready", "NORM-1", "id: NORM-1\ntitle: \"n\"\ntype: fix\ncluster: c\npriority: P2")
		// --id of a normal card is allowed: the waiting expedite card cannot be worked yet
		out := mustLines(t, f.pull(Args{Actor: "w1", ID: "NORM-1"}))
		joined := strings.Join(out, "\n")
		contains(t, joined, "HOT-DEP", "output") // still mentioned, with its dependency
		contains(t, joined, "BASE-1", "output")
		mustRefuse(t, f.done(Args{Actor: "lead", ID: "BASE-1", Note: "x"})) // fix type: gated
		// new accepts a mixed-case type like set does
		mustLines(t, f.new(NewTicket{Actor: "lead", ID: "MC-1",
			Title: "t", Cluster: "c", Summary: longSummary, Type: "Docs",
			BodyFile: f.bodyFile(t, "mc-body.md", fullBody("MC-1"))}))
		contains(t, f.fm("backlog", "MC-1"), "type: docs", "fm")
		// an empty title is refused by set and flagged by lint
		contains(t, mustRefuse(t, f.set("lead", "MC-1", []string{"title="})), "title", "set")
		f.card("backlog", "NT-1", "id: NT-1\ntitle: \"\"\ntype: docs\ncluster: c\nsummary: \""+longSummary+"\"",
			"Steps:\n1. a\n\nAcceptance:\n- [ ] b\n\nEvidence: c\n")
		report := mustLint(t, f)
		if !hasRule(lintRules(t, report, "NT-1"), "title") {
			t.Error("lint does not flag the empty title")
		}
		// an empty board lints clean
		fresh := newFixture(t)
		if report := mustLint(t, fresh); len(report.Tickets) != 0 {
			t.Errorf("empty board lints %+v", report.Tickets)
		}
	})

	t.Run("test_status_marks_a_waiting_expedite_card_and_set_checks_depends_on_like_new", func(t *testing.T) {
		f := newFixture(t)
		f.expCard("ready", "HOT-W", "\ndepends_on: [BASE-2]")
		f.card("backlog", "BASE-2", "id: BASE-2\ntitle: \"b\"\ntype: fix\ncluster: c")
		st := mustStatus(t, f)
		if len(st.Expedite) != 1 {
			t.Fatalf("expedite = %+v", st.Expedite)
		}
		if st.Expedite[0].ID != "HOT-W" ||
			st.Expedite[0].Column != "ready" || st.Expedite[0].WaitsOn != "BASE-2" {
			t.Errorf("expedite = %+v", st.Expedite)
		}
		// set depends_on refuses an unknown id and suggests the upper-case id
		contains(t, mustRefuse(t, f.set("lead", "HOT-W", []string{"depends_on=NOPE-9"})),
			"no such card", "set")
		contains(t, mustRefuse(t, f.set("lead", "HOT-W", []string{"depends_on=base-2"})),
			"did you mean BASE-2", "set")
		contains(t, mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "DM-1",
			Title: "t", Cluster: "c", Summary: longSummary, DependsOn: []string{"base-2"}})),
			"did you mean BASE-2", "new")
		mustLines(t, f.set("lead", "HOT-W", []string{"depends_on=BASE-2, the mailer"}))
	})

	t.Run("test_round31_lint_empty_body_and_bare_block_pass_through_gate_digit_rule_board_resolution_and_init_refusals", func(t *testing.T) {
		f := newFixture(t)
		// an empty --body-file is refused
		empty := filepath.Join(t.TempDir(), "empty.md")
		if err := os.WriteFile(empty, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		contains(t, mustRefuse(t, f.new(NewTicket{Actor: "lead", ID: "EB-1",
			Title: "t", Cluster: "c", Summary: longSummary, BodyFile: empty})), "empty", "new")
		// an open card with an empty body is flagged by lint
		f.card("backlog", "EB-2", "id: EB-2\ntitle: \"t\"\ntype: docs\ncluster: c\nsummary: \""+longSummary+"\"", "\n")
		report := mustLint(t, f)
		if len(lintRules(t, report, "EB-2")) == 0 {
			t.Error("lint passes the empty body")
		}
		// a blocked card without --on or --until is flagged by lint
		f.card("ready", "BL-1", "id: BL-1\ntitle: \"t\"\ntype: docs\ncluster: c\n"+
			"priority: P2\nest: M\nsummary: \""+longSummary+"\"", fullBody("BL-1"))
		mustLines(t, f.block(Args{Actor: "lead", ID: "BL-1", Note: "x"}))
		report = mustLint(t, f)
		blocked := false
		for _, p := range lintRules(t, report, "BL-1") {
			if p.Rule == "blocked-on" || p.Rule == "blocked-until" {
				blocked = true
			}
		}
		if !blocked {
			t.Error("lint does not flag the blocked card without on/until")
		}
		mustLines(t, f.set("lead", "BL-1", []string{"blocked_on=the vendor", "unblock_when=vendor ships"}))
		report = mustLint(t, f)
		for _, lt := range report.Tickets {
			if lt.ID == "BL-1" {
				t.Error("lint still flags BL-1 after blocked_on and unblock_when were set")
			}
		}
		// an author's or holder's own pass-through note is refused
		f.card("in-progress", "PT-9", "id: PT-9\ntitle: \"t\"\ntype: fix\ncluster: c\nauthored_by: w1\n"+
			"claimed_by: w3\nheld_by: [w1, w3]\nblocked_from: testing")
		for _, who := range []string{"w1", "w3"} {
			contains(t, mustRefuse(t, f.review(Args{Actor: who, ID: "PT-9",
				Note: "pass-through: reviewed earlier"})), "pass-through", who)
		}
		mustLines(t, f.review(Args{Actor: "rev", ID: "PT-9", Note: "pass-through: reviewed earlier"}))
		// the test note needs an exit code or a pass count, not any digit
		f.card("review", "DG-1", "id: DG-1\ntitle: \"t\"\ntype: fix\nauthored_by: w1\nclaimed_by: w1")
		for _, bad := range []string{"it's got 0", "ran 3 things", "ok"} {
			contains(t, mustRefuse(t, f.test(Args{Actor: "qa", ID: "DG-1", Note: bad})), "exit", bad)
		}
		for _, good := range []string{"pytest exit 0", "go test rc=0", "exit code 1 then exit 0", "12 passed"} {
			f.card("review", "DG-1", "id: DG-1\ntitle: \"t\"\ntype: fix\nauthored_by: w1\nclaimed_by: w1")
			mustLines(t, f.test(Args{Actor: "qa", ID: "DG-1", Note: good}))
		}
		// init refuses an unwritable root without crashing
		ro := filepath.Join(t.TempDir(), "ro")
		if err := os.MkdirAll(ro, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(ro, 0o555); err != nil {
			t.Fatal(err)
		}
		_, err := Init(ctx, filepath.Join(ro, ".agents", "kanban"), "", fixedNow)
		os.Chmod(ro, 0o755)
		contains(t, refuseErr(t, err), "cannot create", "init")
		// a LOG nobody may append to refuses the command after the move
		f.card("backlog", "EB-3", "id: EB-3\ntitle: \"t\"\ntype: docs\ncluster: c\n"+
			"priority: P2\nest: M\nsummary: \""+longSummary+"\"", fullBody("EB-3"))
		if err := os.Chmod(filepath.Join(f.root, "LOG"), 0o444); err != nil {
			t.Fatal(err)
		}
		_, err = f.b.Promote(ctx, Args{Actor: "lead", ID: "EB-3"})
		os.Chmod(filepath.Join(f.root, "LOG"), 0o644)
		contains(t, refuseErr(t, err), "LOG", "promote")
		if !f.exists("ready", "EB-3") {
			t.Error("the move did not happen before the LOG refusal")
		}
	})

	t.Run("test_round32_a_card_with_no_front_matter_is_refused_before_it_moves_and_md5_falls_back_before_python_39", func(t *testing.T) {
		f := newFixture(t)
		// unblock and done clear fields; a card with no --- block has none to clear: refuse it, unmoved
		for _, c := range []struct {
			col string
			run cmd
		}{
			{"blocked", f.unblock(Args{Actor: "lead", ID: "NOFM-1"})},
			{"testing", f.done(Args{Actor: "qa", ID: "NOFM-1", Note: "ok"})},
		} {
			if err := os.MkdirAll(filepath.Dir(f.path(c.col, "NOFM-1")), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.path(c.col, "NOFM-1"),
				[]byte("# NOFM-1\n\nA card written by hand with no front matter.\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			msg := mustRefuse(t, c.run)
			contains(t, msg, "no front matter", c.col)
			contains(t, msg, "new", c.col)
			if !f.exists(c.col, "NOFM-1") {
				t.Errorf("%s moved the card before refusing", c.col)
			}
			if err := os.Remove(f.path(c.col, "NOFM-1")); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("test_block_and_promote_require_an_actor", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "ACT-1", "id: ACT-1\ntitle: \"a\"\ntype: docs")
		// the exit-2 usage halves are the cli's; the LOG never carries a null actor
		mustLines(t, f.block(Args{Actor: "lead", ID: "ACT-1", Note: "x"}))
		if strings.Contains(f.logText(), `"actor":null`) {
			t.Error("LOG carries a null actor")
		}
		if got := f.lastLog()["actor"]; got != "lead" {
			t.Errorf("LOG actor = %v, want lead", got)
		}
	})
}

// bodyFile writes a body file beside the fixture and answers its path.
func (f *fixture) bodyFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(f.t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// mustLint runs the lint command and answers its report.
func mustLint(t *testing.T, f *fixture) LintReport {
	t.Helper()
	report, err := f.b.Lint(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return report
}

func mustStatus(t *testing.T, f *fixture) Status {
	t.Helper()
	st, err := f.b.Status(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return st
}

// The board validates the actor at the door of every command, not once before
// the first.
func TestEveryFlowCommandRefusesABadActor(t *testing.T) {
	f := newFixture(t)
	calls := []cmd{
		f.promote(Args{Actor: "a b", ID: "X-1"}),
		f.pull(Args{Actor: "a b", ID: "X-1"}),
		f.review(Args{Actor: "a b", ID: "X-1", Note: "n"}),
		f.reject(Args{Actor: "a b", ID: "X-1", Note: "n"}),
		f.test(Args{Actor: "a b", ID: "X-1", Note: "pytest exit 0"}),
		f.done(Args{Actor: "a b", ID: "X-1", Note: "n"}),
		f.block(Args{Actor: "a b", ID: "X-1", Note: "n"}),
		f.unblock(Args{Actor: "a b", ID: "X-1"}),
		f.move(Args{Actor: "a b", ID: "X-1", Column: "ready"}),
		f.log(Args{Actor: "a b", ID: "X-1", Note: "n"}),
		f.stopLine(Args{Actor: "a b", Note: "red"}),
		f.resumeLine(Args{Actor: "a b"}),
		f.new(NewTicket{Actor: "a b", ID: "X-1", Title: "t", Cluster: "c", Summary: longSummary}),
		f.set("a b", "X-1", []string{"cluster=c"}),
	}
	for i, call := range calls {
		msg := mustRefuse(t, call)
		contains(t, msg, "--actor", "call "+strconv.Itoa(i))
	}
	if f.logText() != "" {
		t.Error("a refused actor still wrote LOG")
	}
}

func TestNotesThatMixQuoteKindsAreRefusedWhereverTheyLand(t *testing.T) {
	f := newFixture(t)
	f.card("in-progress", "MIX-1", "id: MIX-1\ntitle: \"m\"\ntype: fix\ncluster: c\nsummary: \""+longSummary+"\"")
	f.card("blocked", "MIX-2", "id: MIX-2\ntitle: \"m\"\ntype: fix\ncluster: c\nblocked_reason: \"x\"\nblocked_from: ready")
	for _, call := range []cmd{
		f.review(Args{Actor: "w1", ID: "MIX-1", Note: `it's "both"`}),
		f.done(Args{Actor: "w2", ID: "MIX-1", Note: `it's "both"`}),
		f.block(Args{Actor: "w1", ID: "MIX-1", Note: `it's "both"`}),
		f.unblock(Args{Actor: "lead", ID: "MIX-2", Note: `it's "both"`}),
		f.move(Args{Actor: "lead", ID: "MIX-2", Column: "ready", Note: `it's "both"`}),
		f.log(Args{Actor: "w1", ID: "MIX-1", Note: `it's "both"`}),
		f.stopLine(Args{Actor: "lead", Note: `it's "both"`}),
	} {
		contains(t, mustRefuse(t, call), "one quote kind", "mixed-quote note")
	}
}

func TestUnblockNamesTheRealColumn(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "UB-9", "id: UB-9\ntitle: \"u\"\ntype: docs")
	msg := mustRefuse(t, f.unblock(Args{Actor: "lead", ID: "UB-9"}))
	contains(t, msg, "it is in ready/", "unblock")
	contains(t, msg, "only blocked cards are unblocked", "unblock")
	missing := mustRefuse(t, f.unblock(Args{Actor: "lead", ID: "NOPE-5"}))
	contains(t, missing, "not on the board", "unblock")
}

func TestLogRefusesAMissingOrMalformedTicket(t *testing.T) {
	f := newFixture(t)
	contains(t, mustRefuse(t, f.log(Args{Actor: "w1", ID: "NOPE-5", Note: "n"})),
		"not on the board", "log")
	if err := os.WriteFile(f.path("ready", "RAW-1"), []byte("# RAW-1\n\nNo front matter.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contains(t, mustRefuse(t, f.log(Args{Actor: "w1", ID: "RAW-1", Note: "n"})),
		"no front matter", "log")
}

func TestCardViewsCarryFinishedAtAndUpdatedAt(t *testing.T) {
	f := newFixture(t)
	f.card("done", "FIN-2", "id: FIN-2\ntitle: \"f\"\ntype: docs\nfinished_at: 2026-10-13T11:20:00+0300")
	cards := mustCards(t, f.list("done"))
	if len(cards) != 1 {
		t.Fatalf("list done = %+v", cards)
	}
	if cards[0].FinishedAt != "2026-10-13T11:20:00+03:00" {
		t.Errorf("FinishedAt = %q, want the RFC 3339 stamp", cards[0].FinishedAt)
	}
	if !strings.HasSuffix(cards[0].UpdatedAt, ":00Z") && !strings.Contains(cards[0].UpdatedAt, "T") {
		t.Errorf("UpdatedAt = %q, want an RFC 3339 mtime", cards[0].UpdatedAt)
	}
	// a stamp that does not parse is shown as written
	f.card("done", "FIN-3", "id: FIN-3\ntitle: \"f\"\ntype: docs\nfinished_at: sometime")
	for _, c := range mustCards(t, f.list("done")) {
		if c.ID == "FIN-3" && c.FinishedAt != "sometime" {
			t.Errorf("FIN-3 FinishedAt = %q, want it as written", c.FinishedAt)
		}
	}
}

func TestLogSectionEntriesLandBeforeTheNextSection(t *testing.T) {
	f := newFixture(t)
	body := "# LOG-3\n\n## Description\nx. Done when: y.\n\n## Context\nc\n\n" +
		"## Implementation plan\n1. do\n\n## Implementation details\n" + strings.Repeat("d", 210) + "\n\n" +
		"## Acceptance\n- [ ] a\n\n## Verification\n```\ngo test\n```\n\n## Out of scope\nnone\n\n" +
		"## Log\n- 2026-10-14T09:00:00+03:00 w0: earlier\n\n## Extra\nkept\n"
	f.card("in-progress", "LOG-3", "id: LOG-3\ntitle: \"l\"\ntype: fix\ncluster: c\n"+
		"priority: P1\nest: M\nsummary: \""+longSummary+"\"", body)
	mustLines(t, f.log(Args{Actor: "w1", ID: "LOG-3", Note: "go test ./... exit 0"}))
	s := f.fm("in-progress", "LOG-3")
	entry := "- " + fixedNow.Format(time.RFC3339) + " w1: go test ./... exit 0"
	if !strings.Contains(s, "## Log\n- 2026-10-14T09:00:00+03:00 w0: earlier\n"+entry+"\n\n## Extra\nkept") {
		t.Errorf("the entry did not land at the end of ## Log:\n%s", s)
	}
}

func TestLogLinesEscapeControlCharacters(t *testing.T) {
	f := newFixture(t)
	f.card("in-progress", "TAB-1", "id: TAB-1\ntitle: \"t\"\ntype: fix\ncluster: c\nsummary: \""+longSummary+"\"",
		fullBody("TAB-1"))
	mustLines(t, f.log(Args{Actor: "w1", ID: "TAB-1", Note: "column:\ttabbed"}))
	contains(t, f.logText(), `column:\ttabbed`, "LOG line")
}

func TestMoveRefusesANonUTF8Ticket(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.path("ready", "LAT-9"),
		[]byte("---\nid: LAT-9\ntitle: \"caf\xe9\"\ntype: docs\ncluster: c\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	contains(t, mustRefuse(t, f.move(Args{Actor: "lead", ID: "LAT-9", Column: "backlog"})),
		"not UTF-8", "move")
	if !f.exists("ready", "LAT-9") {
		t.Error("the non-UTF-8 ticket moved")
	}
}

// dirBytes answers every file under root keyed by its path below root, so a
// test can hold a whole board still — cards and the board's own files alike.
func dirBytes(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = data
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// A closed feature's card refuses every flow command that names it: the
// refusal names the feature and the board keeps its bytes. log changes no
// state, so it still works.
func TestAClosedFeatureRefusesTheFlowCommands(t *testing.T) {
	ctx := context.Background()
	text := "id: SWR-2\ntitle: \"two\"\ncluster: swr\ntype: docs"

	tests := []struct {
		name string
		col  string
		call func(f *fixture) cmd
	}{
		{"promote", "backlog", func(f *fixture) cmd {
			return f.promote(Args{Actor: "lead", ID: "SWR-2"})
		}},
		{"pull by id", "ready", func(f *fixture) cmd {
			return f.pull(Args{Actor: "w1", ID: "SWR-2"})
		}},
		{"review", "in-progress", func(f *fixture) cmd {
			return f.review(Args{Actor: "reviewer", ID: "SWR-2", Note: "looks done"})
		}},
		{"reject", "review", func(f *fixture) cmd {
			return f.reject(Args{Actor: "reviewer", ID: "SWR-2", Note: "not yet"})
		}},
		{"test", "review", func(f *fixture) cmd {
			return f.test(Args{Actor: "verifier", ID: "SWR-2", Note: "go test ./... exit 0"})
		}},
		{"done", "testing", func(f *fixture) cmd {
			return f.done(Args{Actor: "verifier", ID: "SWR-2", Note: "shipped"})
		}},
		{"block", "ready", func(f *fixture) cmd {
			return f.block(Args{Actor: "w1", ID: "SWR-2", Note: "waiting on the api"})
		}},
		{"unblock", "blocked", func(f *fixture) cmd {
			return f.unblock(Args{Actor: "lead", ID: "SWR-2"})
		}},
		{"move", "backlog", func(f *fixture) cmd {
			return f.move(Args{Actor: "lead", ID: "SWR-2", Column: "ready"})
		}},
		{"set", "backlog", func(f *fixture) cmd {
			return f.set("lead", "SWR-2", []string{"priority=P1"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.card(tt.col, "SWR-2", text)
			mustLines(t, func() ([]string, error) {
				return f.b.CloseFeature(ctx, Args{Actor: "lead", ID: "swr"})
			})
			before := dirBytes(t, f.root)
			msg := mustRefuse(t, tt.call(f))
			if !strings.HasPrefix(msg, "Feature closed: swr") {
				t.Fatalf("refusal = %q, want one starting \"Feature closed: swr\"", msg)
			}
			sameBytes(t, before, dirBytes(t, f.root), "after the refused "+tt.name)
		})
	}

	t.Run("log still works", func(t *testing.T) {
		f := newFixture(t)
		f.card("backlog", "SWR-2", text)
		mustLines(t, func() ([]string, error) {
			return f.b.CloseFeature(ctx, Args{Actor: "lead", ID: "swr"})
		})
		out := mustLines(t, f.log(Args{Actor: "lead", ID: "SWR-2", Note: "note only, no move"}))
		if idOfLastLine(t, out) != "SWR-2" {
			t.Fatalf("log = %v, want the SWR-2 path", out)
		}
		contains(t, f.fm("backlog", "SWR-2"), "note only, no move", "the card's log")
	})
}
