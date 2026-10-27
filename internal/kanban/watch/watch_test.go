package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// baseTime pins every stamp the fake clock answers; its +0300 offset matches
// the board suite's clock.
var baseTime = time.Date(2026, 10, 15, 12, 0, 0, 0, time.FixedZone("+0300", 3*60*60))

// clock is the injected time: scans and board stamps read it, a test moves it.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

// fixture is one throwaway board, its watcher and a counting reader.
type fixture struct {
	t     *testing.T
	root  string
	b     board.Board
	w     *Watcher
	clock *clock
	reads int
	fail  map[string]int // paths whose next n reads answer an error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agents", "kanban")
	for _, col := range board.Columns {
		if err := os.MkdirAll(filepath.Join(root, col), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{t: t, root: root, clock: &clock{now: baseTime}, fail: map[string]int{}}
	f.writeFile("EPOCH", "1\n")
	f.writeFile("LOG", "")
	f.b = board.Board{Root: root, Now: f.clock.Now}
	f.w = New(f.b, Options{ReadFile: f.read, Now: f.clock.Now})
	return f
}

// read counts every file the watcher opens; a path in fail answers an error
// while its count lasts.
func (f *fixture) read(path string) ([]byte, error) {
	if n := f.fail[path]; n > 0 {
		f.fail[path] = n - 1
		return nil, os.ErrPermission
	}
	f.reads++
	return os.ReadFile(path)
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
func (f *fixture) card(col, id, fm string) string {
	f.t.Helper()
	text := "---\n" + strings.TrimSpace(fm) + "\n---\n\n# " + id + "\n\nBody text.\n"
	if err := os.WriteFile(f.path(col, id), []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return f.path(col, id)
}

func (f *fixture) text(col, id string) string {
	f.t.Helper()
	data, err := os.ReadFile(f.path(col, id))
	if err != nil {
		f.t.Fatalf("read %s: %v", f.path(col, id), err)
	}
	return string(data)
}

// writeLog replaces the LOG with one line.
func (f *fixture) writeLog(line string) {
	f.t.Helper()
	f.writeFile("LOG", line+"\n")
}

// lastLog answers the last LOG line as a map, nil on an empty LOG.
func (f *fixture) lastLog() map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "LOG"))
	if err != nil {
		f.t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return nil
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		f.t.Fatalf("last LOG line %q: %v", lines[len(lines)-1], err)
	}
	return entry
}

// scan runs one pass and refuses a quiet one.
func (f *fixture) scan() Delta {
	f.t.Helper()
	d, changed := f.w.Scan(context.Background())
	if !changed {
		f.t.Fatal("Scan answered no change on a board that changed")
	}
	return d
}

// quietScan runs one pass and refuses a noisy one.
func (f *fixture) quietScan() {
	f.t.Helper()
	if d, changed := f.w.Scan(context.Background()); changed {
		f.t.Fatalf("Scan answered a change (rev %d) on an unchanged board", d.Rev)
	}
}

func cardIn(cards []board.Card, id string) (board.Card, bool) {
	for _, c := range cards {
		if c.ID == id {
			return c, true
		}
	}
	return board.Card{}, false
}

func hasBadge(c board.Card, kind string) bool {
	for _, b := range c.Badges {
		if b.Kind == kind {
			return true
		}
	}
	return false
}

func hasFailure(c board.Card, substr string) bool {
	for _, m := range c.Lint.Failures {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func TestInitialSnapshotOfASeededBoard(t *testing.T) {
	f := newFixture(t)
	f.card("backlog", "NOR-2", "id: NOR-2\ntitle: \"n2\"\ntype: docs\npriority: P2")
	f.card("backlog", "NOR-1", "id: NOR-1\ntitle: \"n1\"\ntype: docs\npriority: P1")
	f.card("backlog", "EXP-9", "id: EXP-9\ntitle: \"e9\"\ntype: docs\npriority: P3\nexpedite: true")
	f.card("done", "OLD-1", "id: OLD-1\ntitle: \"o1\"\ntype: docs\nfinished_at: 2026-10-14T10:00:00+0300")
	f.card("done", "OLD-2", "id: OLD-2\ntitle: \"o2\"\ntype: docs\nfinished_at: 2026-10-15T09:00:00+0300")

	d := f.scan()
	if d.Rev != 1 {
		t.Errorf("first delta rev = %d, want 1", d.Rev)
	}
	if len(d.Upsert) != 5 || len(d.Remove) != 0 {
		t.Fatalf("first delta upserts %d and removes %d; want 5 and 0", len(d.Upsert), len(d.Remove))
	}
	if d.Meta == nil || d.Meta.Epoch != 1 || d.Meta.StopLine != nil {
		t.Fatalf("first delta meta = %+v", d.Meta)
	}
	s := f.w.Snapshot()
	if s.Rev != 1 || s.GeneratedAt != baseTime.Format(time.RFC3339) {
		t.Errorf("snapshot rev/generatedAt = %d/%q", s.Rev, s.GeneratedAt)
	}
	abs, err := filepath.Abs(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if s.Board != abs {
		t.Errorf("snapshot board = %q, want %q", s.Board, abs)
	}
	if want := filepath.Base(filepath.Dir(filepath.Dir(abs))); s.Project != want {
		t.Errorf("snapshot project = %q, want %q", s.Project, want)
	}
	if len(s.Columns) != len(board.Columns) {
		t.Fatalf("snapshot holds %d columns, want %d", len(s.Columns), len(board.Columns))
	}
	for i, col := range board.Columns {
		want := 0
		switch col {
		case "backlog":
			want = 3
		case "done":
			want = 2
		}
		if c := s.Columns[i]; c.ID != col || c.Count != want || c.Title == "" {
			t.Errorf("column %d = %+v, want id %s count %d", i, c, col, want)
		}
	}
	if s.Columns[2].Title != "In Progress" {
		t.Errorf("in-progress title = %q, want In Progress", s.Columns[2].Title)
	}
	var order []string
	for _, c := range s.Cards {
		if c.Column == "backlog" {
			order = append(order, c.ID)
		}
	}
	if want := []string{"EXP-9", "NOR-1", "NOR-2"}; !slices.Equal(order, want) {
		t.Errorf("backlog order = %v, want %v", order, want)
	}
	order = nil
	for _, c := range s.Cards {
		if c.Column == "done" {
			order = append(order, c.ID)
		}
	}
	if want := []string{"OLD-2", "OLD-1"}; !slices.Equal(order, want) {
		t.Errorf("done order = %v, want %v (newest finish first)", order, want)
	}
	f.quietScan()
}

func TestEditUpsertsOneCard(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "EDIT-1", "id: EDIT-1\ntitle: \"before\"\ntype: docs")
	f.card("ready", "EDIT-2", "id: EDIT-2\ntitle: \"stay\"\ntype: docs")
	f.scan()

	f.card("ready", "EDIT-1", "id: EDIT-1\ntitle: \"after the edit\"\ntype: docs")
	d := f.scan()
	if d.Rev != 2 {
		t.Errorf("delta rev = %d, want 2", d.Rev)
	}
	if len(d.Upsert) != 1 || d.Upsert[0].ID != "EDIT-1" || d.Upsert[0].Title != "after the edit" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
	if len(d.Remove) != 0 {
		t.Errorf("remove = %v, want none", d.Remove)
	}
	if d.Meta != nil {
		t.Errorf("a same-column edit carried meta: %+v", d.Meta)
	}
	if strings.Contains(f.text("ready", "EDIT-1"), "moved_by_hand") {
		t.Error("a same-column edit stamped moved_by_hand")
	}
	if last := f.lastLog(); last != nil {
		t.Errorf("a same-column edit wrote a LOG line: %v", last)
	}
	f.quietScan()
}

func TestDeleteRemovesTheCard(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "GONE-1", "id: GONE-1\ntitle: \"g\"\ntype: docs")
	f.scan()

	if err := os.Remove(f.path("ready", "GONE-1")); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	if len(d.Remove) != 1 || d.Remove[0] != "GONE-1" {
		t.Fatalf("remove = %v, want [GONE-1]", d.Remove)
	}
	if len(d.Upsert) != 0 {
		t.Errorf("upsert = %+v, want none", d.Upsert)
	}
	if d.Meta == nil {
		t.Fatal("a delete carried no meta")
	}
	if got := d.Meta.Columns[1].Count; got != 0 {
		t.Errorf("ready count = %d, want 0", got)
	}
	if s := f.w.Snapshot(); len(s.Cards) != 0 {
		t.Errorf("snapshot still holds %d cards", len(s.Cards))
	}
	f.quietScan()
}

func TestHandMoveStampsTheTicketAndLogs(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "HAND-1", "id: HAND-1\ntitle: \"h\"\ntype: docs")
	f.scan()

	if err := os.Rename(f.path("ready", "HAND-1"), f.path("in-progress", "HAND-1")); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	card, ok := cardIn(d.Upsert, "HAND-1")
	if !ok || card.Column != "in-progress" || card.MovedByHand == "" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
	if !hasBadge(card, "hand") {
		t.Errorf("the hand badge is missing: %+v", card.Badges)
	}
	if want := "ready -> in-progress at " + baseTime.Format(time.RFC3339); card.MovedByHand != want {
		t.Errorf("moved_by_hand = %q, want %q", card.MovedByHand, want)
	}
	if len(d.Remove) != 0 {
		t.Errorf("remove = %v, want none (the id is still on the board)", d.Remove)
	}
	if d.Meta == nil {
		t.Error("a move between columns carried no meta")
	}
	if !strings.Contains(f.text("in-progress", "HAND-1"), "moved_by_hand") {
		t.Error("the ticket file carries no moved_by_hand")
	}
	last := f.lastLog()
	if last["event"] != "moved-by-hand" || last["ticket"] != "HAND-1" ||
		last["from"] != "ready" || last["to"] != "in-progress" {
		t.Errorf("hand-move LOG = %v", last)
	}
	f.quietScan()
}

func TestFlowCommandMoveIsNotAHandMove(t *testing.T) {
	f := newFixture(t)
	f.card("in-progress", "FLOW-1", "id: FLOW-1\ntitle: \"f\"\ntype: docs")
	f.scan()

	if _, err := f.b.Move(context.Background(), board.Args{
		Actor: "lead", ID: "FLOW-1", Column: "review", Note: "repair",
	}); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	if len(d.Upsert) != 1 || d.Upsert[0].ID != "FLOW-1" || d.Upsert[0].Column != "review" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
	if d.Upsert[0].MovedByHand != "" || hasBadge(d.Upsert[0], "hand") {
		t.Errorf("a cli move stamped a hand move: %+v", d.Upsert[0])
	}
	if strings.Contains(f.text("review", "FLOW-1"), "moved_by_hand") {
		t.Error("a cli move left a moved_by_hand on the ticket")
	}
	last := f.lastLog()
	if last["event"] != "move" || last["forced"] != true {
		t.Errorf("LOG = %v, want the cli move", last)
	}
	f.quietScan()
}

func TestHandMoveWindowSpansFiveSeconds(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "WIN-1", "id: WIN-1\ntitle: \"w\"\ntype: docs")
	f.scan()

	// a flow event 4 s old owns the move
	stamp := baseTime.Add(-4 * time.Second).Format("2006-01-02T15:04:05-0700")
	f.writeLog(`{"ts":"` + stamp + `","event":"pull","ticket":"WIN-1","actor":"w1"}`)
	if err := os.Rename(f.path("ready", "WIN-1"), f.path("in-progress", "WIN-1")); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	if card, ok := cardIn(d.Upsert, "WIN-1"); !ok || card.Column != "in-progress" ||
		card.MovedByHand != "" || hasBadge(card, "hand") {
		t.Fatalf("a fresh flow event still stamped a hand move: %+v", card)
	}

	// the same event 6 s old no longer owns it
	f.clock.advance(2 * time.Second)
	if err := os.Rename(f.path("in-progress", "WIN-1"), f.path("review", "WIN-1")); err != nil {
		t.Fatal(err)
	}
	d = f.scan()
	card, ok := cardIn(d.Upsert, "WIN-1")
	if !ok || card.Column != "review" || card.MovedByHand == "" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
	last := f.lastLog()
	if last["event"] != "moved-by-hand" || last["from"] != "in-progress" || last["to"] != "review" {
		t.Errorf("hand-move LOG = %v", last)
	}
}

func TestRewriteWithoutMoveIsAnUpsertNotAHandMove(t *testing.T) {
	f := newFixture(t)
	f.card("in-progress", "RW-1", "id: RW-1\ntitle: \"r\"\ntype: docs")
	f.scan()

	if _, err := f.b.Log(context.Background(), board.Args{
		Actor: "w1", ID: "RW-1", Note: "go test ./... exit 0",
	}); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	if len(d.Upsert) != 1 || d.Upsert[0].ID != "RW-1" || d.Upsert[0].Column != "in-progress" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
	if d.Upsert[0].MovedByHand != "" || hasBadge(d.Upsert[0], "hand") {
		t.Errorf("a rewrite in place stamped a hand move: %+v", d.Upsert[0])
	}
	if strings.Contains(f.text("in-progress", "RW-1"), "moved_by_hand") {
		t.Error("a rewrite in place left a moved_by_hand on the ticket")
	}
}

func TestEpochAndStopLineChangesCarryMeta(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "META-1", "id: META-1\ntitle: \"m\"\ntype: docs")
	f.scan()

	f.writeFile("EPOCH", "2\n")
	d := f.scan()
	if d.Meta == nil || d.Meta.Epoch != 2 || d.Meta.StopLine != nil {
		t.Fatalf("epoch delta meta = %+v", d.Meta)
	}
	if len(d.Upsert) != 0 || len(d.Remove) != 0 {
		t.Errorf("an epoch change moved cards: %+v / %v", d.Upsert, d.Remove)
	}

	f.writeFile("STOP-LINE", `{"epoch":2,"actor":"lead","note":"main is broken","ts":"2026-10-15T12:00:00+0300"}`)
	d = f.scan()
	if d.Meta == nil || d.Meta.StopLine == nil ||
		d.Meta.StopLine.Actor != "lead" || d.Meta.StopLine.Note != "main is broken" {
		t.Fatalf("stop-line delta meta = %+v", d.Meta)
	}
	if s := f.w.Snapshot(); s.StopLine == nil || s.Epoch != 2 {
		t.Errorf("snapshot epoch/stop = %d/%v", s.Epoch, s.StopLine)
	}

	if err := os.Remove(filepath.Join(f.root, "STOP-LINE")); err != nil {
		t.Fatal(err)
	}
	d = f.scan()
	if d.Meta == nil || d.Meta.StopLine != nil || d.Meta.Epoch != 2 {
		t.Fatalf("resume delta meta = %+v", d.Meta)
	}
	f.quietScan()
}

func TestThousandUnchangedTicketsReadNothingOnRescan(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 1000; i++ {
		col := board.Columns[i%len(board.Columns)]
		id := fmt.Sprintf("BIG-%04d", i)
		text := "---\nid: " + id + "\ntitle: \"b\"\ntype: docs\n---\n\n# " + id + "\n"
		if err := os.WriteFile(f.path(col, id), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, changed := f.w.Scan(context.Background()); !changed {
		t.Fatal("the first scan of a seeded board answered no change")
	}
	if f.reads == 0 {
		t.Fatal("the first scan read no file")
	}
	f.reads = 0
	if d, changed := f.w.Scan(context.Background()); changed {
		t.Fatalf("an unchanged board answered delta %+v", d)
	}
	if f.reads != 0 {
		t.Errorf("the rescan read %d files; an unchanged board reads none", f.reads)
	}
}

func TestUnparsableFileShowsAsAnIncompleteCard(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(f.path("ready", "BROKEN-1"), []byte("not a ticket\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.scan()
	card, ok := cardIn(f.w.Snapshot().Cards, "BROKEN-1")
	if !ok {
		t.Fatal("the unparsable file shows no card")
	}
	if card.Title != "BROKEN-1.md" {
		t.Errorf("title = %q, want the file name", card.Title)
	}
	if !hasBadge(card, "incomplete") || len(card.Lint.Failures) == 0 {
		t.Errorf("the incomplete badge or the failures are missing: %+v", card)
	}

	// a hand move of a file that cannot be stamped still answers its card
	if err := os.Rename(f.path("ready", "BROKEN-1"), f.path("review", "BROKEN-1")); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	card, ok = cardIn(d.Upsert, "BROKEN-1")
	if !ok || card.Column != "review" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
}

func TestNonUTF8HandMoveIsSkippedButTheCardShows(t *testing.T) {
	f := newFixture(t)
	raw := append([]byte("---\nid: RAW-1\ntitle: \"r\"\n"), 0xff, 0xfe, '\n')
	raw = append(raw, []byte("---\n\n# RAW-1\n")...)
	if err := os.WriteFile(f.path("ready", "RAW-1"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	f.scan()

	if err := os.Rename(f.path("ready", "RAW-1"), f.path("testing", "RAW-1")); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	card, ok := cardIn(d.Upsert, "RAW-1")
	if !ok || card.Column != "testing" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
	if !hasBadge(card, "incomplete") {
		t.Errorf("the non-UTF-8 card carries no incomplete badge: %+v", card.Badges)
	}
	if last := f.lastLog(); last != nil {
		t.Errorf("a stamp that cannot land still logged: %v", last)
	}
}

func TestDuplicateIdShowsBothCards(t *testing.T) {
	f := newFixture(t)
	f.card("backlog", "DUP-1", "id: DUP-1\ntitle: \"one\"\ntype: docs")
	f.card("ready", "DUP-1", "id: DUP-1\ntitle: \"two\"\ntype: docs")
	f.scan()
	n := 0
	for _, card := range f.w.Snapshot().Cards {
		if card.ID != "DUP-1" {
			continue
		}
		n++
		if !hasFailure(card, "duplicate id DUP-1") {
			t.Errorf("the card in %s carries no duplicate failure: %v", card.Column, card.Lint.Failures)
		}
	}
	if n != 2 {
		t.Fatalf("%d cards named DUP-1, want 2", n)
	}
}

func TestOneMoveOnAThousandTicketBoardScansFast(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 1000; i++ {
		col := board.Columns[i%len(board.Columns)]
		id := fmt.Sprintf("BIG-%04d", i)
		fm := "id: " + id + "\ntitle: \"b\"\ntype: docs"
		if id == "BIG-0500" {
			fm += "\ndepends_on: [BIG-0200]"
		}
		text := "---\n" + fm + "\n---\n\n# " + id + "\n"
		if err := os.WriteFile(f.path(col, id), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f.scan()

	// BIG-0200 sits in testing (200 % 7); one hand move to done
	if err := os.Rename(f.path("testing", "BIG-0200"), f.path("done", "BIG-0200")); err != nil {
		t.Fatal(err)
	}
	f.reads = 0
	start := time.Now()
	d, changed := f.w.Scan(context.Background())
	elapsed := time.Since(start)
	if !changed {
		t.Fatal("the move answered no change")
	}
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("one move on a 1,000-ticket board took %s to scan; the budget is 300 ms", elapsed)
	}
	if f.reads != 1 {
		t.Errorf("the scan read %d files; a move rereads only the moved one", f.reads)
	}
	ids := map[string]bool{}
	for _, c := range d.Upsert {
		ids[c.ID] = true
	}
	if len(d.Upsert) != 2 || !ids["BIG-0200"] || !ids["BIG-0500"] {
		t.Fatalf("upserts = %v; want exactly the moved card and its dependant", ids)
	}
	if len(d.Remove) != 0 {
		t.Errorf("remove = %v, want none (the id still shows)", d.Remove)
	}
	for _, c := range d.Upsert {
		if c.ID == "BIG-0500" {
			if len(c.Deps) != 1 || c.Deps[0].State != "done" {
				t.Errorf("the dependant's dep state = %+v, want done", c.Deps)
			}
			if hasBadge(c, "waits") {
				t.Errorf("the waits badge outlived the dependency: %+v", c.Badges)
			}
		}
	}
	f.quietScan()
}

func TestADependantFollowsItsDependency(t *testing.T) {
	f := newFixture(t)
	f.card("backlog", "DEP-1", "id: DEP-1\ntitle: \"d\"\ntype: docs")
	f.card("ready", "USER-1", "id: USER-1\ntitle: \"u\"\ntype: docs\ndepends_on: [DEP-1]")
	f.scan()
	user, ok := cardIn(f.w.Snapshot().Cards, "USER-1")
	if !ok || len(user.Deps) != 1 || user.Deps[0].State != "backlog" {
		t.Fatalf("the dependant's deps before the move = %+v", user.Deps)
	}
	if !hasBadge(user, "waits") {
		t.Errorf("the waits badge is missing before the move: %+v", user.Badges)
	}

	if err := os.Rename(f.path("backlog", "DEP-1"), f.path("done", "DEP-1")); err != nil {
		t.Fatal(err)
	}
	d := f.scan()
	user, ok = cardIn(d.Upsert, "USER-1")
	if !ok {
		t.Fatalf("the dependant is missing from the upserts: %+v", d.Upsert)
	}
	if user.Deps[0].State != "done" {
		t.Errorf("the dependant's dep state = %q, want done", user.Deps[0].State)
	}
	if hasBadge(user, "waits") {
		t.Errorf("the waits badge outlived the dependency: %+v", user.Badges)
	}
	user, _ = cardIn(f.w.Snapshot().Cards, "USER-1")
	if user.Deps[0].State != "done" {
		t.Errorf("the snapshot's dep state = %q, want done", user.Deps[0].State)
	}
}

func TestAnUnreadableFileIsRetriedNextScan(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "SEED-1", "id: SEED-1\ntitle: \"s\"\ntype: docs")
	f.scan()

	path := f.card("backlog", "NEW-1", "id: NEW-1\ntitle: \"n\"\ntype: docs")
	f.fail[path] = 1
	d := f.scan() // the count changed; the read failed, so no card
	if d.Meta == nil {
		t.Fatal("the new file's count change carried no meta")
	}
	if _, ok := cardIn(f.w.Snapshot().Cards, "NEW-1"); ok {
		t.Fatal("the failed read still answered a card")
	}
	d = f.scan() // the retry lands
	if len(d.Upsert) != 1 || d.Upsert[0].ID != "NEW-1" {
		t.Fatalf("upsert = %+v", d.Upsert)
	}
	if _, ok := cardIn(f.w.Snapshot().Cards, "NEW-1"); !ok {
		t.Error("the retried file never showed up")
	}
}

func TestRunPublishesAChangeWithinTheBudget(t *testing.T) {
	f := newFixture(t)
	// the real loop runs on the real clock; a zero EveryMs answers the default
	// 300 ms interval
	f.w = New(f.b, Options{})
	f.card("ready", "LIVE-1", "id: LIVE-1\ntitle: \"before\"\ntype: docs")

	var mu sync.Mutex
	var got []Delta
	saw := make(chan int64, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.w.Run(ctx, func(d Delta) {
			mu.Lock()
			got = append(got, d)
			mu.Unlock()
			saw <- d.Rev
		})
	}()

	// a reader that races with the passes: Snapshot must be safe mid-scan
	stopRead := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopRead:
				return
			default:
				_ = f.w.Snapshot().Rev
				time.Sleep(time.Millisecond)
			}
		}
	}()

	select {
	case rev := <-saw:
		if rev != 1 {
			t.Errorf("first published rev = %d, want 1", rev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run published nothing")
	}

	// a change on disk reaches open pages within 500 ms; the margin absorbs a
	// slow CI runner without hiding a broken interval
	const budget = 500 * time.Millisecond
	margin := 4500 * time.Millisecond
	start := time.Now()
	f.card("ready", "LIVE-1", "id: LIVE-1\ntitle: \"after the write\"\ntype: docs")
	deadline := time.After(budget + margin)
	for {
		mu.Lock()
		seen := false
		for _, d := range got {
			if d.Rev > 1 {
				_, seen = cardIn(d.Upsert, "LIVE-1")
			}
		}
		mu.Unlock()
		if seen {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("the change never reached pages within %s", budget+margin)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if elapsed := time.Since(start); elapsed > budget+margin {
		t.Fatalf("the change took %s to reach pages, over the %s budget", elapsed, budget+margin)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	close(stopRead)
}

func TestDeltaAndSnapshotJSONNames(t *testing.T) {
	s := Snapshot{
		Rev: 7, Board: "/b/.agents/kanban", Project: "b", Epoch: 3,
		Features: Features{Closed: []ClosedFeature{{
			Name: "swr", ClosedAt: "2026-10-15T12:00:00+0300", Cards: 2,
		}}},
		Columns:     []Column{{ID: "backlog", Title: "Backlog", Count: 1}},
		Cards:       []board.Card{{ID: "A-1"}},
		GeneratedAt: "2026-10-15T12:00:00+03:00",
	}
	keys := marshalKeys(t, s)
	for _, key := range []string{"rev", "board", "project", "epoch", "stopLine", "features", "columns", "cards", "generatedAt"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("snapshot JSON lacks %q: %s", key, marshal(t, s))
		}
	}
	if v := keys["stopLine"]; v != nil {
		t.Errorf("stopLine = %v, want null", v)
	}
	featKeys := marshalKeys(t, s.Features.Closed[0])
	for _, key := range []string{"name", "closedAt", "cards"} {
		if _, ok := featKeys[key]; !ok {
			t.Errorf("closed feature JSON lacks %q", key)
		}
	}
	colKeys := marshalKeys(t, s.Columns[0])
	for _, key := range []string{"id", "title", "count"} {
		if _, ok := colKeys[key]; !ok {
			t.Errorf("column JSON lacks %q", key)
		}
	}

	d := Delta{Rev: 8, Upsert: []board.Card{}, Remove: []string{}}
	keys = marshalKeys(t, d)
	for _, key := range []string{"rev", "upsert", "remove"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("delta JSON lacks %q: %s", key, marshal(t, d))
		}
	}
	if _, ok := keys["meta"]; ok {
		t.Errorf("a delta without a meta change still carries meta: %s", marshal(t, d))
	}
	d.Meta = &Meta{Epoch: 3, Columns: []Column{}, Features: Features{Closed: []ClosedFeature{}}}
	keys = marshalKeys(t, d)
	meta, ok := keys["meta"].(map[string]any)
	if !ok {
		t.Fatalf("delta JSON carries no meta object: %s", marshal(t, d))
	}
	for _, key := range []string{"epoch", "stopLine", "features", "columns"} {
		if _, ok := meta[key]; !ok {
			t.Errorf("meta JSON lacks %q: %s", key, marshal(t, d))
		}
	}
	if metaFeatures := marshal(t, d.Meta.Features); string(metaFeatures) != `{"closed":[]}` {
		t.Errorf("an empty closed list encodes as %s, want {\"closed\":[]}", metaFeatures)
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func marshalKeys(t *testing.T, v any) map[string]any {
	t.Helper()
	var keys map[string]any
	if err := json.Unmarshal(marshal(t, v), &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}
