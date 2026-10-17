package ticket

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// idRe is the shape of a ticket id: a letter, then A-Z and 0-9 groups joined by
// single dashes.
var idRe = regexp.MustCompile(`^[A-Z][A-Z0-9]*(-[A-Z0-9]+)*$`)

// Ticket is one card's parsed content: the front matter as named fields plus
// the raw map, and the body split into its sections.
type Ticket struct {
	ID, Title, Summary, Type, Priority, Est, Cluster string
	Tags, DependsOn                                  []string
	Expedite                                         bool
	Fields                                           map[string]string // every front-matter key, quotes removed
	Body                                             string
	Sections                                         []Section
	OldLayout                                        bool
}

// Section is one `## ` heading of the body and the text under it; Heading carries
// no "## " prefix. An old card's Goal, Steps and Evidence map onto Description,
// Implementation plan and Verification.
type Section struct{ Heading, Text string } // Heading without "## "

// Parse reads one ticket file. Problems appear only for input the format cannot
// describe: bytes that are not UTF-8, a missing front-matter block, a
// front-matter id that does not match the file name.
func Parse(fileName string, data []byte) (Ticket, []Problem) {
	if !utf8.Valid(data) {
		return Ticket{}, []Problem{{Severity: "failure", Rule: "utf-8", Message: "not UTF-8"}}
	}
	front, body, ok := Split(string(data))
	var problems []Problem
	if !ok {
		problems = append(problems, Problem{Severity: "failure", Rule: "front-matter", Message: "no front matter"})
	}
	t := Ticket{Fields: map[string]string{}, Body: body}
	t.Sections, t.OldLayout = parseSections(body)
	lines := strings.Split(front, "\n")
	for i := 0; i < len(lines); {
		key, head, is := fieldLine(lines[i])
		if !is {
			i++
			continue
		}
		if _, seen := t.Fields[key]; !seen {
			t.Fields[key] = valueAt(lines, i, head)
		}
		i = blockEnd(lines, i, head)
	}
	t.ID = t.Fields["id"]
	t.Title = t.Fields["title"]
	t.Summary = t.Fields["summary"]
	t.Type = t.Fields["type"]
	t.Priority = t.Fields["priority"]
	t.Est = t.Fields["est"]
	t.Cluster = t.Fields["cluster"]
	t.Tags = ParseList(t.Fields["tags"])
	t.DependsOn = ParseList(t.Fields["depends_on"])
	t.Expedite = isExpedite(t.Fields["expedite"])
	if ok {
		if fileID := strings.TrimSuffix(fileName, ".md"); t.ID != fileID {
			problems = append(problems, Problem{
				Severity: "failure",
				Rule:     "id",
				Message:  "id " + t.ID + " does not match the file name " + fileID,
			})
		}
	}
	return t, problems
}

func isExpedite(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on":
		return true
	}
	return false
}
