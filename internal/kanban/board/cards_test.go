package board

import (
	"reflect"
	"strings"
	"testing"
)

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
