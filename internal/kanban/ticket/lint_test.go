package ticket

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func cleanTicket() Ticket {
	return Ticket{
		ID:       "A-1",
		Title:    "a clean ticket title",
		Summary:  "Adds the retry to the router so a dead worker never strands a request in flight, because the front dies first. Done when the parity suite covers every restart case.",
		Type:     "feature",
		Priority: "P2",
		Est:      "M",
		Cluster:  "router",
		Fields:   map[string]string{},
		Sections: []Section{
			{"Description", "What changes and why, in prose."},
			{"Context", "Paths to read first."},
			{"Implementation plan", "1. one bounded action"},
			{"Implementation details", strings.Repeat("planned decision ", 15)},
			{"Acceptance", "- [ ] a box"},
			{"Verification", "```sh\nexit 0\n```"},
			{"Out of scope", "Nothing else."},
			{"Log", ""},
		},
	}
}

// problemText pairs a problem's severity with its message, so a row pins both.
func problemText(p Problem) string { return p.Severity + "|" + p.Message }

func TestLint(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Ticket, *Context)
		want   []string
	}{
		{"a clean ticket passes", nil, nil},
		{"title is empty", func(t *Ticket, _ *Context) { t.Title = "" },
			[]string{"failure|title is empty"}},
		{"title over 90 runes", func(t *Ticket, _ *Context) { t.Title = strings.Repeat("x", 91) },
			[]string{"failure|title is 91 characters; the limit is 90"}},
		{"title length counts runes, not bytes", func(t *Ticket, _ *Context) { t.Title = strings.Repeat("é", 91) },
			[]string{"failure|title is 91 characters; the limit is 90"}},
		{"summary too short", func(t *Ticket, _ *Context) { t.Summary = "Too short." },
			[]string{
				"failure|summary is 10 characters; it needs 80 to 600",
				`warning|summary has no "Done when"`,
			}},
		{"summary too long", func(t *Ticket, _ *Context) { t.Summary = strings.Repeat("sum", 200) + " Done when long." },
			[]string{"failure|summary is 616 characters; it needs 80 to 600"}},
		{"unknown type", func(t *Ticket, _ *Context) { t.Type = "bug" },
			[]string{`failure|type "bug": use one of fix, feature, test, docs, ops, decision, chore`}},
		{"unknown priority", func(t *Ticket, _ *Context) { t.Priority = "P9" },
			[]string{`failure|priority "P9": use one of P0, P1, P2, P3`}},
		{"unknown est", func(t *Ticket, _ *Context) { t.Est = "XL" },
			[]string{`failure|est "XL": use one of S, M, L`}},
		{"cluster with three words", func(t *Ticket, _ *Context) { t.Cluster = "Three Words" },
			[]string{`failure|cluster "Three Words": one or two lowercase words`}},
		{"cluster with a colon", func(t *Ticket, _ *Context) { t.Cluster = "a: b" },
			[]string{`failure|cluster "a: b": one or two lowercase words`}},
		{"tag with a space", func(t *Ticket, _ *Context) { t.Tags = []string{"Bad Tag"} },
			[]string{`failure|tag "Bad Tag": lowercase words joined by dashes, at most 24 characters`}},
		{"tag over 24 runes", func(t *Ticket, _ *Context) { t.Tags = []string{strings.Repeat("a", 25)} },
			[]string{`failure|tag "aaaaaaaaaaaaaaaaaaaaaaaaa": lowercase words joined by dashes, at most 24 characters`}},
		{"nine tags", func(t *Ticket, _ *Context) {
			t.Tags = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		}, []string{"failure|9 tags; at most 8"}},
		{"duplicate id", func(_ *Ticket, c *Context) {
			c.Known = func(id string) bool { return id == "A-1" }
		}, []string{"failure|duplicate id A-1"}},
		{"a missing section", func(t *Ticket, _ *Context) { t.Sections = t.Sections[1:] },
			[]string{"failure|section ## Description is missing"}},
		{"a section out of order", func(t *Ticket, _ *Context) {
			t.Sections = []Section{t.Sections[0], t.Sections[2], t.Sections[1], t.Sections[3],
				t.Sections[4], t.Sections[5], t.Sections[6], t.Sections[7]}
		}, []string{"failure|section ## Context is out of order"}},
		{"an unknown section before Out of scope", func(t *Ticket, _ *Context) {
			t.Sections = append([]Section{t.Sections[0], {"Notes", "extra"}}, t.Sections[1:]...)
		}, []string{"failure|section ## Notes is out of order"}},
		{"an unknown section after Out of scope is kept", func(t *Ticket, _ *Context) {
			t.Sections = append(t.Sections, Section{Heading: "Notes", Text: "extra"})
		}, nil},
		{"an empty section", func(t *Ticket, _ *Context) { t.Sections[0].Text = "" },
			[]string{"failure|section ## Description is empty"}},
		{"a fill comment", func(t *Ticket, _ *Context) { t.Sections[0].Text = "<!-- fill: what changes and why -->" },
			[]string{"failure|section ## Description still holds its fill comment"}},
		{"a fill comment inside a fence still counts", func(t *Ticket, _ *Context) {
			t.Sections[5].Text = "```sh\n<!-- fill: x -->\n```"
		}, []string{"failure|section ## Verification still holds its fill comment"}},
		{"a plan without a numbered step", func(t *Ticket, _ *Context) { t.Sections[2].Text = "do things" },
			[]string{"failure|## Implementation plan has no numbered step"}},
		{"a bare step number is not a step", func(t *Ticket, _ *Context) { t.Sections[2].Text = "1. " },
			[]string{"failure|## Implementation plan has no numbered step"}},
		{"acceptance without a box", func(t *Ticket, _ *Context) { t.Sections[4].Text = "- item" },
			[]string{`failure|## Acceptance has no "- [ ]" box`}},
		{"a bare box is not checked", func(t *Ticket, _ *Context) { t.Sections[4].Text = "- [ ]" },
			[]string{`failure|## Acceptance has no "- [ ]" box`}},
		{"verification without a fence", func(t *Ticket, _ *Context) { t.Sections[5].Text = "exit 0 expected" },
			[]string{"failure|## Verification has no fenced command block"}},
		{"an unclosed fence is not a block", func(t *Ticket, _ *Context) { t.Sections[5].Text = "```sh\nexit 0" },
			[]string{"failure|## Verification has no fenced command block"}},
		{"depends_on names an unknown id", func(t *Ticket, c *Context) {
			t.DependsOn = []string{"NOPE-9"}
			c.Known = func(string) bool { return false }
		}, []string{"failure|depends_on names NOPE-9, which is not on the board"}},
		{"plain-text dependencies are allowed", func(t *Ticket, c *Context) {
			t.DependsOn = []string{"the mailer being live", "AB-"}
			c.Known = func(string) bool { return false }
		}, nil},
		{"blocked without blocked_on and unblock_when", func(_ *Ticket, c *Context) {
			c.Column = "blocked"
		}, []string{
			"failure|blocked ticket has no blocked_on",
			"failure|blocked ticket has no unblock_when",
		}},
		{"blocked with both set", func(t *Ticket, c *Context) {
			c.Column = "blocked"
			t.Fields["blocked_on"] = "[WIN-1]"
			t.Fields["unblock_when"] = "legal says yes"
		}, nil},
		{"an old card is only warned about", func(t *Ticket, _ *Context) { t.OldLayout = true },
			[]string{"warning|old layout (Goal, Steps, Evidence)"}},
		{"an old card is not failed for the sections it lacks", func(t *Ticket, _ *Context) {
			t.OldLayout = true
			t.Sections = []Section{
				{"Description", "What changes."},
				{"Implementation plan", "1. one"},
				{"Acceptance", "- [ ] b"},
				{"Verification", "```sh\nexit 0\n```"},
			}
		}, []string{"warning|old layout (Goal, Steps, Evidence)"}},
		{"summary without a done-when", func(t *Ticket, _ *Context) { t.Summary = strings.Repeat("s", 90) },
			[]string{`warning|summary has no "Done when"`}},
		{"thin details on an M", func(t *Ticket, _ *Context) { t.Sections[3].Text = "short." },
			[]string{"warning|## Implementation details is under 200 characters for an M ticket"}},
		{"thin details on an L", func(t *Ticket, _ *Context) {
			t.Est = "L"
			t.Sections[3].Text = "short."
		}, []string{"warning|## Implementation details is under 200 characters for an L ticket"}},
		{"thin details on an S is fine", func(t *Ticket, _ *Context) {
			t.Est = "S"
			t.Sections[3].Text = "short."
		}, nil},
		{"a done ticket answers its front matter only", func(t *Ticket, c *Context) {
			c.Column = "done"
			t.Sections = nil
			t.Summary = "Too short."
		}, []string{"warning|summary is 10 characters; it needs 80 to 600 (done tickets are not edited)"}},
		{"a done old card gets no warning either", func(t *Ticket, c *Context) {
			c.Column = "done"
			t.OldLayout = true
			t.Sections = nil
			t.Summary = "Too short."
		}, []string{"warning|summary is 10 characters; it needs 80 to 600 (done tickets are not edited)"}},
		{"a backlog ticket gets the same checks", func(t *Ticket, c *Context) {
			c.Column = "backlog"
			t.Sections = t.Sections[1:]
		}, []string{"failure|section ## Description is missing"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tk := cleanTicket()
			ctx := Context{Column: "ready"}
			if tc.mutate != nil {
				tc.mutate(&tk, &ctx)
			}
			got := Lint(tk, ctx)
			var messages []string
			for _, p := range got {
				messages = append(messages, problemText(p))
			}
			sort.Strings(messages)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(messages, want) {
				t.Errorf("Lint(...) =\n%s\nwant\n%s", strings.Join(messages, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

func TestADoneTicketsFieldLimitsAreWarnings(t *testing.T) {
	tk := cleanTicket()
	tk.Title = strings.Repeat("x", 120)
	tk.Summary = strings.Repeat("s", 50)
	tk.Cluster = "hub-sec"
	got := Lint(tk, Context{Column: "done"})
	if len(got) != 3 {
		t.Fatalf("Lint(...) = %d problems (%v), want 3", len(got), got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if p.Severity != "warning" {
			t.Errorf("problem %s|%s on a done ticket, want severity warning", p.Severity, p.Message)
		}
		if !strings.HasSuffix(p.Message, " (done tickets are not edited)") {
			t.Errorf("message %q does not end in \" (done tickets are not edited)\"", p.Message)
		}
		seen[p.Rule] = true
	}
	for _, rule := range []string{"title", "summary", "cluster"} {
		if !seen[rule] {
			t.Errorf("Lint(...) holds no warning for %s: %v", rule, got)
		}
	}
}

func TestADoneTicketsDuplicateIdStillFails(t *testing.T) {
	tk := cleanTicket()
	got := Lint(tk, Context{Column: "done", Known: func(id string) bool { return id == tk.ID }})
	if len(got) != 1 {
		t.Fatalf("Lint(...) = %d problems (%v), want 1", len(got), got)
	}
	if got[0].Severity != "failure" || got[0].Rule != "duplicate" {
		t.Errorf("problem %s|%s, want a duplicate failure", got[0].Severity, got[0].Message)
	}
}

func TestAnOpenTicketsFieldLimitsStillFail(t *testing.T) {
	tk := cleanTicket()
	tk.Title = strings.Repeat("x", 120)
	for _, p := range Lint(tk, Context{Column: "ready"}) {
		if p.Rule == "title" && p.Severity != "failure" {
			t.Errorf("title problem %s|%s on an open ticket, want severity failure", p.Severity, p.Message)
		}
	}
}

func TestParseProblems(t *testing.T) {
	t.Run("not UTF-8", func(t *testing.T) {
		tk, problems := Parse("LAT-1.md", []byte("---\nid: LAT-1\ntitle: \"caf\xe9\"\n---\n"))
		if len(problems) != 1 || problemText(problems[0]) != "failure|not UTF-8" {
			t.Errorf("Parse(...) problems = %q", problems)
		}
		if tk.ID != "" {
			t.Errorf("Parse(...) ID = %q, want empty", tk.ID)
		}
	})
	t.Run("id does not match the file name", func(t *testing.T) {
		_, problems := Parse("RIGHT.md", []byte(card("id: WRONG\ntitle: \"a\"")))
		if len(problems) != 1 || problems[0].Message != "id WRONG does not match the file name RIGHT" {
			t.Errorf("Parse(...) problems = %q", problems)
		}
	})
	t.Run("no front matter", func(t *testing.T) {
		_, problems := Parse("X.md", []byte("# X\n\nA card written by hand.\n"))
		if len(problems) != 1 || problems[0].Message != "no front matter" {
			t.Errorf("Parse(...) problems = %q", problems)
		}
	})
	t.Run("a matching id reports nothing", func(t *testing.T) {
		if _, problems := Parse("A-1.md", []byte(card("id: A-1\ntitle: \"a\""))); len(problems) != 0 {
			t.Errorf("Parse(...) problems = %q, want none", problems)
		}
	})
}
