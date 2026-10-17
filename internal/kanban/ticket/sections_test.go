package ticket

import (
	"reflect"
	"strings"
	"testing"
)

const canonicalBody = "# A-1: a title\n\n" +
	"## Description\nWhat changes.\n\n" +
	"## Context\nPaths to read.\n\n" +
	"## Implementation plan\n1. first\n\n" +
	"## Implementation details\nDecisions.\n\n" +
	"## Acceptance\n- [ ] a box\n\n" +
	"## Verification\n" + "```sh\nexit 0\n```\n\n" +
	"## Out of scope\nNothing.\n\n" +
	"## Log\n- 2026 note\n"

func TestParseSections(t *testing.T) {
	t.Run("the canonical layout", func(t *testing.T) {
		want := []Section{
			{"Description", "What changes.\n"},
			{"Context", "Paths to read.\n"},
			{"Implementation plan", "1. first\n"},
			{"Implementation details", "Decisions.\n"},
			{"Acceptance", "- [ ] a box\n"},
			{"Verification", "```sh\nexit 0\n```\n"},
			{"Out of scope", "Nothing.\n"},
			{"Log", "- 2026 note\n"},
		}
		got, old := parseSections(canonicalBody)
		if old {
			t.Error("parseSections(...).old = true, want false")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseSections(...) = %q, want %q", got, want)
		}
	})
	t.Run("an old card's paragraph markers map onto the sections", func(t *testing.T) {
		body := "# A-1 — old\n\nGoal: fix the flaky watch test.\n\nSteps:\n1. one\n2. two\n\nAcceptance:\n- [ ] b\n\nEvidence: pytest exit 0\n"
		want := []Section{
			{"Description", "fix the flaky watch test.\n"},
			{"Implementation plan", "1. one\n2. two\n"},
			{"Acceptance", "- [ ] b\n"},
			{"Verification", "pytest exit 0\n"},
		}
		got, old := parseSections(body)
		if !old {
			t.Error("parseSections(...).old = false, want true")
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parseSections(...) = %q, want %q", got, want)
		}
	})
	t.Run("a heading-form old marker maps too", func(t *testing.T) {
		got, old := parseSections("# A-1\n\nGoal: g\n\n## Steps\n\n1. one\n")
		want := []Section{{"Description", "g\n"}, {"Implementation plan", "\n1. one\n"}}
		if !old || !reflect.DeepEqual(got, want) {
			t.Errorf("parseSections(...) = %q, %v; want %q, true", got, old, want)
		}
	})
	t.Run("an unknown section is kept", func(t *testing.T) {
		got, _ := parseSections(canonicalBody + "## Notes\nextra\n")
		if len(got) != 9 || got[8].Heading != "Notes" || got[8].Text != "extra\n" {
			t.Errorf("parseSections(...) = %q, want the Notes section kept last", got)
		}
	})
	t.Run("text before the first heading is not a section", func(t *testing.T) {
		got, _ := parseSections("# A-1\n\nintro prose\n\n## Description\nD\n")
		if len(got) != 1 || got[0].Heading != "Description" {
			t.Errorf("parseSections(...) = %q, want one Description section", got)
		}
	})
	t.Run("a heading line without a name is not a section", func(t *testing.T) {
		got, _ := parseSections("# A-1\n\n## Description\nbefore\n##  \nafter\n")
		if len(got) != 1 || got[0].Text != "before\n##  \nafter\n" {
			t.Errorf("parseSections(...) = %q, want the bare marker kept as text", got)
		}
	})
}

func TestParse(t *testing.T) {
	src := "---\n" +
		"id: A-1\n" +
		"title: \"a clean ticket\"\n" +
		"cluster: router\n" +
		"summary: >-\n  one two\n  three four\n" +
		"type: fix\n" +
		"priority: P1\n" +
		"est: S\n" +
		"tags: [core, go-port]\n" +
		"depends_on: [B-2, \"a push to GitHub\"]\n" +
		"expedite: true\n" +
		"claimed_by: w1\n" +
		"---\n\n# A-1: a clean ticket\n\n## Description\nD\n"
	t.Run("every field reads back", func(t *testing.T) {
		tk, problems := Parse("A-1.md", []byte(src))
		if len(problems) != 0 {
			t.Fatalf("Parse(...) problems = %q, want none", problems)
		}
		checks := []struct {
			name string
			got  any
			want any
		}{
			{"ID", tk.ID, "A-1"},
			{"Title", tk.Title, "a clean ticket"},
			{"Summary", tk.Summary, "one two three four"},
			{"Type", tk.Type, "fix"},
			{"Priority", tk.Priority, "P1"},
			{"Est", tk.Est, "S"},
			{"Cluster", tk.Cluster, "router"},
			{"Tags", tk.Tags, []string{"core", "go-port"}},
			{"DependsOn", tk.DependsOn, []string{"B-2", "a push to GitHub"}},
			{"Expedite", tk.Expedite, true},
		}
		for _, c := range checks {
			if !reflect.DeepEqual(c.got, c.want) {
				t.Errorf("Parse(...).%s = %v, want %v", c.name, c.got, c.want)
			}
		}
		if tk.Fields["claimed_by"] != "w1" || tk.Fields["expedite"] != "true" || len(tk.Fields) != 11 {
			t.Errorf("Parse(...).Fields = %q", tk.Fields)
		}
		if !strings.HasPrefix(tk.Body, "\n# A-1: a clean ticket") {
			t.Errorf("Parse(...).Body = %q", tk.Body)
		}
		if len(tk.Sections) != 1 || tk.Sections[0].Heading != "Description" || tk.OldLayout {
			t.Errorf("Parse(...).Sections = %q, old = %v", tk.Sections, tk.OldLayout)
		}
	})
	t.Run("expedite variants", func(t *testing.T) {
		for raw, want := range map[string]bool{"yes": true, "on": true, "false": false, "": false} {
			tk, _ := Parse("C-1.md", []byte(card("id: C-1\nexpedite: "+raw)))
			if tk.Expedite != want {
				t.Errorf("Parse(...) expedite %q = %v, want %v", raw, tk.Expedite, want)
			}
		}
	})
	t.Run("crlf bytes parse", func(t *testing.T) {
		tk, problems := Parse("A-1.md", []byte(strings.ReplaceAll(src, "\n", "\r\n")))
		if len(problems) != 0 || tk.ID != "A-1" || tk.Title != "a clean ticket" {
			t.Errorf("Parse(...) = %+v, problems %q", tk, problems)
		}
	})
	t.Run("an old card is read without change", func(t *testing.T) {
		tk, _ := Parse("A-1.md", []byte("---\nid: A-1\n---\n\n# A-1\n\nGoal: g\n\nSteps:\n1. one\n"))
		if !tk.OldLayout || len(tk.Sections) != 2 {
			t.Errorf("Parse(...) old = %v, sections %q", tk.OldLayout, tk.Sections)
		}
	})
	t.Run("stray front-matter lines are not fields", func(t *testing.T) {
		tk, _ := Parse("C-1.md", []byte(card("id: C-1\njust prose\n1bad: x\ntitle: \"a\"")))
		if len(tk.Fields) != 2 || tk.Fields["id"] != "C-1" || tk.Fields["title"] != "a" {
			t.Errorf("Parse(...).Fields = %q, want id and title only", tk.Fields)
		}
	})
}

func TestSkeleton(t *testing.T) {
	want := "# A-1: a title\n\n" +
		"## Description\n" +
		"<!-- fill: what changes and why, in prose. End with one line \"Done when: <objective condition>.\" -->\n\n" +
		"## Context\n" +
		"<!-- fill: what to read first (exact paths, cards, docs), decisions already made, constraints that bind. -->\n\n" +
		"## Implementation plan\n" +
		"<!-- fill: numbered steps; each is one bounded action naming exact paths. Tests first. -->\n\n" +
		"## Implementation details\n" +
		"<!-- fill: the decisions made while planning: signatures, types, data formats, exact messages, edge cases and\n" +
		"what to do in each, examples. A worker makes no design decision the ticket did not make. -->\n\n" +
		"## Acceptance\n" +
		"<!-- fill: \"- [ ]\" boxes, each objectively checkable by someone who did not do the work. -->\n\n" +
		"## Verification\n" +
		"<!-- fill: fenced commands, each followed by its expected exit code. -->\n\n" +
		"## Out of scope\n" +
		"<!-- fill: what not to touch, and where that work lives instead. -->\n\n" +
		"## Log\n"
	t.Run("byte for byte", func(t *testing.T) {
		if got := Skeleton("A-1", "a title"); got != want {
			t.Errorf("Skeleton(...) =\n%q\nwant\n%q", got, want)
		}
	})
	t.Run("parses into the eight sections in order", func(t *testing.T) {
		got, old := parseSections(Skeleton("A-1", "a title"))
		if old {
			t.Error("parseSections(Skeleton(...)) old = true, want false")
		}
		var headings []string
		for _, s := range got {
			headings = append(headings, s.Heading)
		}
		if !reflect.DeepEqual(headings, sectionOrder) {
			t.Errorf("parseSections(Skeleton(...)) headings = %q, want %q", headings, sectionOrder)
		}
	})
}
