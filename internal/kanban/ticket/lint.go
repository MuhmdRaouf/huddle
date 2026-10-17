package ticket

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Problem is one finding of the ticket bar.
type Problem struct {
	Severity string // "failure" or "warning"
	Rule     string // short slug: "title", "summary", "section-missing", ...
	Message  string
}

// Context carries what only the board knows: the column the ticket sits in, and
// which ids are on the board. Known speaks for every file but the one being
// checked, so a true answer for the ticket's own id means a duplicate.
type Context struct {
	Column string               // the folder the ticket is in
	Known  func(id string) bool // is this id on the board
}

var (
	types      = []string{"fix", "feature", "test", "docs", "ops", "decision", "chore"}
	priorities = []string{"P0", "P1", "P2", "P3"}
	ests       = []string{"S", "M", "L"}
	clusterRe  = regexp.MustCompile(`^[a-z0-9]+( [a-z0-9]+)?$`)
	tagRe      = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
)

// Lint checks a ticket against the bar: the front-matter rules always, the body,
// the dependencies, the blocked fields and the warnings on every open column. A
// done ticket answers its front matter only, and there the field limits warn: a
// done ticket is never edited, so a bar raised after it landed could never be
// cleared. A duplicate id still fails — it names two files, not one ticket's
// fields.
func Lint(t Ticket, c Context) []Problem {
	var problems []Problem
	problems = append(problems, lintFields(t, c)...)
	if c.Column == "done" {
		for i, p := range problems {
			if p.Rule == "duplicate" {
				continue
			}
			problems[i] = Problem{"warning", p.Rule, p.Message + " (done tickets are not edited)"}
		}
		return problems
	}
	problems = append(problems, lintSections(t)...)
	problems = append(problems, lintDeps(t, c)...)
	problems = append(problems, lintBlocked(t, c)...)
	problems = append(problems, lintWarnings(t)...)
	return problems
}

func lintFields(t Ticket, c Context) []Problem {
	var problems []Problem
	if t.Title == "" {
		problems = append(problems, Problem{"failure", "title", "title is empty"})
	} else if n := utf8.RuneCountInString(t.Title); n > 90 {
		problems = append(problems, Problem{"failure", "title", fmt.Sprintf("title is %d characters; the limit is 90", n)})
	}
	if n := utf8.RuneCountInString(t.Summary); n < 80 || n > 600 {
		problems = append(problems, Problem{"failure", "summary", fmt.Sprintf("summary is %d characters; it needs 80 to 600", n)})
	}
	if !oneOf(types, t.Type) {
		problems = append(problems, Problem{"failure", "type",
			fmt.Sprintf("type %q: use one of fix, feature, test, docs, ops, decision, chore", t.Type)})
	}
	if !oneOf(priorities, t.Priority) {
		problems = append(problems, Problem{"failure", "priority",
			fmt.Sprintf("priority %q: use one of P0, P1, P2, P3", t.Priority)})
	}
	if !oneOf(ests, t.Est) {
		problems = append(problems, Problem{"failure", "est",
			fmt.Sprintf("est %q: use one of S, M, L", t.Est)})
	}
	if !clusterRe.MatchString(t.Cluster) {
		problems = append(problems, Problem{"failure", "cluster",
			fmt.Sprintf("cluster %q: one or two lowercase words", t.Cluster)})
	}
	for _, tag := range t.Tags {
		if !tagRe.MatchString(tag) || utf8.RuneCountInString(tag) > 24 {
			problems = append(problems, Problem{"failure", "tag",
				fmt.Sprintf("tag %q: lowercase words joined by dashes, at most 24 characters", tag)})
		}
	}
	if len(t.Tags) > 8 {
		problems = append(problems, Problem{"failure", "tags", fmt.Sprintf("%d tags; at most 8", len(t.Tags))})
	}
	if c.Known != nil && c.Known(t.ID) {
		problems = append(problems, Problem{"failure", "duplicate", fmt.Sprintf("duplicate id %s", t.ID)})
	}
	return problems
}

func lintSections(t Ticket) []Problem {
	order := sectionOrder
	if t.OldLayout {
		order = oldSectionOrder
	}
	position := make(map[string]int, len(order))
	for i, name := range order {
		position[name] = i
	}
	present := make(map[string]*Section, len(order))
	maxPos := -1
	outOfScope := false
	var problems []Problem
	for i := range t.Sections {
		s := &t.Sections[i]
		p, known := position[s.Heading]
		if !known {
			// an unknown section may sit anywhere once Out of scope has passed
			if !t.OldLayout && !outOfScope {
				problems = append(problems, Problem{"failure", "section-order",
					fmt.Sprintf("section ## %s is out of order", s.Heading)})
			}
			continue
		}
		if s.Heading == "Out of scope" {
			outOfScope = true
		}
		if p <= maxPos {
			problems = append(problems, Problem{"failure", "section-order",
				fmt.Sprintf("section ## %s is out of order", s.Heading)})
		} else {
			maxPos = p
		}
		if _, dup := present[s.Heading]; !dup {
			present[s.Heading] = s
		}
	}
	for _, name := range order {
		s, ok := present[name]
		if !ok {
			problems = append(problems, Problem{"failure", "section-missing",
				fmt.Sprintf("section ## %s is missing", name)})
			continue
		}
		if name != "Log" && strings.TrimSpace(s.Text) == "" {
			problems = append(problems, Problem{"failure", "section-empty",
				fmt.Sprintf("section ## %s is empty", name)})
		}
		if strings.Contains(s.Text, "<!-- fill:") {
			problems = append(problems, Problem{"failure", "section-fill",
				fmt.Sprintf("section ## %s still holds its fill comment", name)})
		}
	}
	problems = append(problems, lintSectionShapes(present)...)
	return problems
}

func lintSectionShapes(present map[string]*Section) []Problem {
	var problems []Problem
	if s := present["Implementation plan"]; s != nil && !hasNumberedStep(s.Text) {
		problems = append(problems, Problem{"failure", "plan-step", "## Implementation plan has no numbered step"})
	}
	if s := present["Acceptance"]; s != nil && !hasCheckbox(s.Text) {
		problems = append(problems, Problem{"failure", "acceptance-box", `## Acceptance has no "- [ ]" box`})
	}
	if s := present["Verification"]; s != nil && !hasFence(s.Text) {
		problems = append(problems, Problem{"failure", "verification-fence", "## Verification has no fenced command block"})
	}
	return problems
}

func lintDeps(t Ticket, c Context) []Problem {
	if c.Known == nil {
		return nil
	}
	var problems []Problem
	for _, dep := range t.DependsOn {
		if idRe.MatchString(dep) && !c.Known(dep) {
			problems = append(problems, Problem{"failure", "depends-on",
				fmt.Sprintf("depends_on names %s, which is not on the board", dep)})
		}
	}
	return problems
}

func lintBlocked(t Ticket, c Context) []Problem {
	if c.Column != "blocked" {
		return nil
	}
	var problems []Problem
	if len(ParseList(t.Fields["blocked_on"])) == 0 {
		problems = append(problems, Problem{"failure", "blocked-on", "blocked ticket has no blocked_on"})
	}
	if strings.TrimSpace(t.Fields["unblock_when"]) == "" {
		problems = append(problems, Problem{"failure", "blocked-until", "blocked ticket has no unblock_when"})
	}
	return problems
}

func lintWarnings(t Ticket) []Problem {
	var problems []Problem
	if t.OldLayout {
		problems = append(problems, Problem{"warning", "old-layout", "old layout (Goal, Steps, Evidence)"})
	}
	if !strings.Contains(t.Summary, "Done when") {
		problems = append(problems, Problem{"warning", "summary-done-when", `summary has no "Done when"`})
	}
	if details := sectionByName(t.Sections, "Implementation details"); details != nil && (t.Est == "M" || t.Est == "L") {
		if utf8.RuneCountInString(strings.TrimSpace(details.Text)) < 200 {
			problems = append(problems, Problem{"warning", "details-thin",
				fmt.Sprintf("## Implementation details is under 200 characters for an %s ticket", t.Est)})
		}
	}
	return problems
}

func sectionByName(sections []Section, name string) *Section {
	for i := range sections {
		if sections[i].Heading == name {
			return &sections[i]
		}
	}
	return nil
}

// hasNumberedStep answers whether the text holds a "N. text" step with text.
func hasNumberedStep(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		i := 0
		for i < len(line) && line[i] >= '0' && line[i] <= '9' {
			i++
		}
		if i == 0 || i+1 >= len(line) || line[i] != '.' || line[i+1] != ' ' {
			continue
		}
		if strings.TrimSpace(line[i+2:]) != "" {
			return true
		}
	}
	return false
}

// hasCheckbox answers whether the text holds a "- [ ]" box with text.
func hasCheckbox(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "- [ ]"); ok && strings.TrimSpace(rest) != "" {
			return true
		}
	}
	return false
}

// hasFence answers whether the text holds a fenced block: an opening and a
// closing fence line.
func hasFence(text string) bool {
	marks := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			marks++
		}
	}
	return marks >= 2
}

func oneOf(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
