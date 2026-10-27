package board

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// A closed feature's cards leave the status answer too: no expedite lane entry
// and no column count until the feature reopens. An open feature's card answers
// FeatureClosed false.
func TestStatusLeavesAClosedFeatureOut(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.card("ready", "SWR-1", "id: SWR-1\ntitle: \"one\"\ncluster: swr\ntype: docs\nexpedite: true")
	f.card("backlog", "WEB-1", "id: WEB-1\ntitle: \"web\"\ncluster: web\ntype: docs")
	st := mustStatus(t, f)
	if len(st.Expedite) != 1 || st.Expedite[0].ID != "SWR-1" {
		t.Fatalf("expedite lane before the close = %+v, want SWR-1", st.Expedite)
	}
	mustLines(t, func() ([]string, error) {
		return f.b.CloseFeature(ctx, Args{Actor: "lead", ID: "swr"})
	})
	st = mustStatus(t, f)
	if len(st.Expedite) != 0 {
		t.Errorf("expedite lane after the close = %+v, want it empty while swr is closed", st.Expedite)
	}
	for _, c := range st.Counts {
		want := map[string]int{"backlog": 1, "ready": 0}[c.Column]
		if c.Count != want {
			t.Errorf("count %s = %d, want %d while swr is closed", c.Column, c.Count, want)
		}
	}
	cards := mustCards(t, f.list(""))
	if len(cards) != 1 || cards[0].ID != "WEB-1" {
		t.Errorf("list after the close = %+v, want WEB-1 alone", cards)
	}
	if cards[0].FeatureClosed {
		t.Errorf("WEB-1 answers FeatureClosed true, want false")
	}
}

func TestCardOfLookupTakesTheBoardAnswersFromLookups(t *testing.T) {
	f := newFixture(t)
	f.card("ready", "LK-DEP", "id: LK-DEP\ntitle: \"d\"\ntype: docs")
	path := f.card("backlog", "LK-MAIN", "id: LK-MAIN\ntitle: \"m\"\ntype: docs\ndepends_on: [LK-DEP]")
	tick, problems := f.b.loadCard("backlog", "LK-MAIN")

	self, _, _ := f.b.Find("LK-MAIN")
	where := func(id string) (string, bool) {
		_, col, ok := f.b.Find(id)
		return col, ok
	}
	want := CardOf(tick, "backlog", f.b, problems)
	got := CardOfLookup(tick, "backlog", path, problems, where, f.b.knownExcept(self))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CardOfLookup = %+v\nCardOf       = %+v", got, want)
	}

	// a lookup that answers without the disk: the dependency's move and a
	// second file holding the id both reach the card
	moved := func(id string) (string, bool) {
		if id == "LK-DEP" {
			return "done", true
		}
		_, col, ok := f.b.Find(id)
		return col, ok
	}
	got = CardOfLookup(tick, "backlog", path, problems, moved, f.b.knownExcept(self))
	if len(got.Deps) != 1 || got.Deps[0].State != "done" {
		t.Errorf("dep state after the move = %+v, want done", got.Deps)
	}
	everythingKnown := func(string) bool { return true }
	got = CardOfLookup(tick, "backlog", path, problems, where, everythingKnown)
	if !failureSays(got, "duplicate id LK-MAIN") {
		t.Errorf("the duplicate failure is missing: %v", got.Lint.Failures)
	}
}

func failureSays(c Card, substr string) bool {
	for _, m := range c.Lint.Failures {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}
