package board

import (
	"context"
	"strings"
	"testing"
	"time"
)

// archiveFixture is a board whose clock the test moves.
type archiveFixture struct {
	*fixture
	now time.Time
}

func newArchiveFixture(t *testing.T) *archiveFixture {
	t.Helper()
	a := &archiveFixture{fixture: newFixture(t), now: fixedNow}
	a.b.Now = func() time.Time { return a.now }
	return a
}

// done writes a done card of the board that finished the given time before the
// fixture's fixed instant.
func (a *archiveFixture) done(id, cluster string, ago time.Duration) {
	a.t.Helper()
	a.card("done", id, "id: "+id+"\ncluster: "+cluster+"\nfinished_at: "+fixedNow.Add(-ago).Format(stampLayout))
}

func (a *archiveFixture) archiveDue() []string {
	a.t.Helper()
	names, err := a.b.ArchiveDue(context.Background())
	if err != nil {
		a.t.Fatalf("ArchiveDue: %v", err)
	}
	return names
}

func (a *archiveFixture) closedNames() []string {
	var names []string
	for _, f := range a.b.ClosedFeatures() {
		names = append(names, f.Name)
	}
	return names
}

// A board whose cards are all done archives 24 hours after its latest finish:
// not a minute before, never while one card is unfinished, never while empty.
func TestABoardArchivesAt24HoursWithAFakeClock(t *testing.T) {
	cases := []struct {
		name  string
		setup func(a *archiveFixture)
		after time.Duration
		want  []string
	}{
		{"23h59m after the last finish stays open",
			func(a *archiveFixture) { a.done("WEB-1", "web", 0) }, 23*time.Hour + 59*time.Minute, nil},
		{"24h after the last finish archives",
			func(a *archiveFixture) { a.done("WEB-1", "web", 0) }, 24 * time.Hour, []string{"web"}},
		{"one card not done keeps the board open",
			func(a *archiveFixture) {
				a.done("WEB-1", "web", 0)
				a.card("review", "WEB-2", "id: WEB-2\ncluster: web")
			}, 48 * time.Hour, nil},
		{"a board with no cards stays open",
			func(a *archiveFixture) {
				mustLines(t, func() ([]string, error) {
					return a.b.CreateBoard(context.Background(), Args{Actor: "lead", ID: "web"})
				})
			}, 48 * time.Hour, nil},
		{"a done card with no finish time keeps the board open",
			func(a *archiveFixture) { a.card("done", "WEB-1", "id: WEB-1\ncluster: web") }, 48 * time.Hour, nil},
		{"each board decides alone",
			func(a *archiveFixture) {
				a.done("WEB-1", "web", 0)
				a.done("API-1", "api", 0)
				a.card("in-progress", "API-2", "id: API-2\ncluster: api")
			}, 24 * time.Hour, []string{"web"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newArchiveFixture(t)
			tc.setup(a)
			a.now = fixedNow.Add(tc.after)
			got := a.archiveDue()
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("ArchiveDue = %q, want %q", got, tc.want)
			}
			if closed := a.closedNames(); strings.Join(closed, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("closed features = %q, want %q", closed, tc.want)
			}
		})
	}
}

// The archive is the closed-feature record written by the system actor, once:
// a second pass finds nothing due and writes nothing.
func TestArchiveIsTheClosedFeatureRecordByTheSystem(t *testing.T) {
	a := newArchiveFixture(t)
	a.done("WEB-1", "web", 0)
	a.now = fixedNow.Add(24 * time.Hour)
	a.archiveDue()
	closed := a.b.ClosedFeatures()
	if len(closed) != 1 || closed[0].Actor != "system" || closed[0].ClosedAt != a.now.Format(stampLayout) {
		t.Fatalf("ClosedFeatures = %+v, want web closed by system at the archive time", closed)
	}
	logBefore := a.logText()
	if again := a.archiveDue(); len(again) != 0 {
		t.Fatalf("second ArchiveDue = %q, want none", again)
	}
	if a.logText() != logBefore {
		t.Fatal("the second pass wrote to the LOG")
	}
}

// The latest finish decides, not the first.
func TestFinishedAtIsTheLatestDone(t *testing.T) {
	a := newArchiveFixture(t)
	a.done("WEB-1", "web", 30*time.Hour)
	a.done("WEB-2", "web", 0)
	a.now = fixedNow.Add(23 * time.Hour)
	if got := a.archiveDue(); len(got) != 0 {
		t.Fatalf("ArchiveDue at 23h after the latest finish = %q, want none", got)
	}
	a.now = fixedNow.Add(24 * time.Hour)
	if got := a.archiveDue(); len(got) != 1 || got[0] != "web" {
		t.Fatalf("ArchiveDue at 24h after the latest finish = %q, want web", got)
	}
}

// A new card in an archived board reopens it, in the same write; a card the bar
// refuses leaves the board archived.
func TestANewCardReopensAnArchivedBoard(t *testing.T) {
	ctx := context.Background()
	a := newArchiveFixture(t)
	a.done("WEB-1", "web", 0)
	a.now = fixedNow.Add(24 * time.Hour)
	a.archiveDue()

	bad := NewTicket{Actor: "lead", ID: "WEB-2", Title: "", Cluster: "web", Summary: longSummary}
	mustRefuse(t, func() ([]string, error) { return a.b.New(ctx, bad) })
	if got := a.closedNames(); len(got) != 1 {
		t.Fatalf("closed after a refused new = %q, want web still closed", got)
	}

	good := NewTicket{Actor: "lead", ID: "WEB-2", Title: "Add the picker", Cluster: "web", Summary: longSummary}
	mustLines(t, func() ([]string, error) { return a.b.New(ctx, good) })
	if got := a.closedNames(); len(got) != 0 {
		t.Fatalf("closed after new = %q, want none", got)
	}
	if !strings.Contains(a.logText(), `"feature-reopen"`) {
		t.Fatalf("LOG has no feature-reopen line:\n%s", a.logText())
	}
	// a pass right after finds the board open again: the new card is not done
	if got := a.archiveDue(); len(got) != 0 {
		t.Fatalf("ArchiveDue after the reopen = %q, want none", got)
	}
}
