package ticket

import (
	"reflect"
	"testing"
)

func TestBadges(t *testing.T) {
	t.Run("the display order", func(t *testing.T) {
		tk := Ticket{
			ID:        "A-1",
			Priority:  "P1",
			Type:      "fix",
			Est:       "M",
			Expedite:  true,
			DependsOn: []string{"DONE-1", "OPEN-1"},
			Fields: map[string]string{
				"claimed_by":     "ana",
				"blocked_reason": "CI is red",
				"moved_by_hand":  "backlog -> ready at 2026-10-15T20:00:00+0300",
			},
		}
		problems := []Problem{{Severity: "failure", Rule: "title", Message: "title is empty"}}
		depState := func(id string) string {
			if id == "DONE-1" {
				return "done"
			}
			return "ready"
		}
		want := []Badge{
			{"expedite", "EXPEDITE"},
			{"priority", "P1"},
			{"type", "fix"},
			{"est", "M"},
			{"blocked", "BLOCKED"},
			{"holder", "@ana"},
			{"waits", "waits on OPEN-1"},
			{"hand", "moved by hand"},
			{"incomplete", "incomplete"},
		}
		if got := Badges(tk, "blocked", depState, problems); !reflect.DeepEqual(got, want) {
			t.Errorf("Badges(...) = %q, want %q", got, want)
		}
	})
	t.Run("a plain ticket shows its vocabulary only", func(t *testing.T) {
		tk := Ticket{ID: "A-1", Priority: "P2", Type: "docs", Est: "S", Fields: map[string]string{}}
		want := []Badge{{"priority", "P2"}, {"type", "docs"}, {"est", "S"}}
		if got := Badges(tk, "ready", nil, nil); !reflect.DeepEqual(got, want) {
			t.Errorf("Badges(...) = %q, want %q", got, want)
		}
	})
	t.Run("a done dependency is not a wait", func(t *testing.T) {
		tk := Ticket{ID: "A-1", DependsOn: []string{"DONE-1"}, Fields: map[string]string{}}
		if got := Badges(tk, "ready", func(string) string { return "done" }, nil); len(got) != 0 {
			t.Errorf("Badges(...) = %q, want none", got)
		}
	})
	t.Run("a missing dependency still waits", func(t *testing.T) {
		tk := Ticket{ID: "A-1", DependsOn: []string{"NOPE-9"}, Fields: map[string]string{}}
		want := []Badge{{"waits", "waits on NOPE-9"}}
		if got := Badges(tk, "ready", func(string) string { return "missing" }, nil); !reflect.DeepEqual(got, want) {
			t.Errorf("Badges(...) = %q, want %q", got, want)
		}
	})
	t.Run("without a state every dependency waits", func(t *testing.T) {
		tk := Ticket{ID: "A-1", DependsOn: []string{"OPEN-1"}, Fields: map[string]string{}}
		want := []Badge{{"waits", "waits on OPEN-1"}}
		if got := Badges(tk, "ready", nil, nil); !reflect.DeepEqual(got, want) {
			t.Errorf("Badges(...) = %q, want %q", got, want)
		}
	})
	t.Run("plain-text dependencies carry no badge", func(t *testing.T) {
		tk := Ticket{ID: "A-1", DependsOn: []string{"the mailer being live", "AB-"}, Fields: map[string]string{}}
		if got := Badges(tk, "ready", func(string) string { return "missing" }, nil); len(got) != 0 {
			t.Errorf("Badges(...) = %q, want none", got)
		}
	})
	t.Run("warnings alone do not mark a ticket incomplete", func(t *testing.T) {
		tk := Ticket{ID: "A-1", Priority: "P2", Fields: map[string]string{}}
		problems := []Problem{{Severity: "warning", Rule: "old-layout", Message: "old layout (Goal, Steps, Evidence)"}}
		want := []Badge{{"priority", "P2"}}
		if got := Badges(tk, "ready", nil, problems); !reflect.DeepEqual(got, want) {
			t.Errorf("Badges(...) = %q, want %q", got, want)
		}
	})
	t.Run("a block reason marks the badge off the blocked column", func(t *testing.T) {
		tk := Ticket{ID: "A-1", Fields: map[string]string{"blocked_reason": "waiting"}}
		want := []Badge{{"blocked", "BLOCKED"}}
		if got := Badges(tk, "review", nil, nil); !reflect.DeepEqual(got, want) {
			t.Errorf("Badges(...) = %q, want %q", got, want)
		}
	})
}
