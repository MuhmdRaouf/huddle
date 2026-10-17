package ticket

import "strings"

// sectionOrder is the body's required section order.
var sectionOrder = []string{
	"Description", "Context", "Implementation plan", "Implementation details",
	"Acceptance", "Verification", "Out of scope", "Log",
}

// oldSectionOrder is what an old card is held to: it carries Goal, Steps and
// Evidence in place of Description, Implementation plan and Verification, and is
// not failed for the sections it lacks.
var oldSectionOrder = []string{"Description", "Implementation plan", "Acceptance", "Verification"}

// oldHeadings maps the old heading names onto the sections they count as;
// Acceptance exists in both layouts and maps to itself.
var oldHeadings = map[string]string{
	"Goal": "Description", "Steps": "Implementation plan",
	"Acceptance": "Acceptance", "Evidence": "Verification",
}

// oldOnly names the headings that mark an old card; Acceptance is shared.
var oldOnly = map[string]bool{"Goal": true, "Steps": true, "Evidence": true}

// oldMarkers lists the old paragraph markers in a fixed order.
var oldMarkers = []string{"Goal", "Steps", "Acceptance", "Evidence"}

// parseSections splits the body into its `## ` sections. An old card's
// paragraph marker (Goal:, Steps:, Acceptance:, Evidence:) opens a section too,
// with the text after the colon as its first line.
func parseSections(body string) ([]Section, bool) {
	lines := strings.Split(body, "\n")
	var sections []Section
	heading := ""
	sameLine := ""
	start := 0
	oldLayout := false
	flush := func(end int) {
		if heading == "" {
			return
		}
		text := strings.Join(lines[start:end], "\n")
		if sameLine != "" {
			text = sameLine + "\n" + text
		}
		sections = append(sections, Section{Heading: heading, Text: text})
		sameLine = ""
	}
	for i, line := range lines {
		name, marksOld, rest, is := sectionHeading(line)
		if !is {
			continue
		}
		flush(i)
		heading = name
		sameLine = rest
		start = i + 1
		oldLayout = oldLayout || marksOld
	}
	flush(len(lines))
	return sections, oldLayout
}

// sectionHeading answers the section a body line opens: `## Name`, or an old
// card's Name: paragraph marker, whose same-line text follows the colon.
func sectionHeading(line string) (name string, marksOld bool, sameLine string, ok bool) {
	if strings.HasPrefix(line, "## ") {
		h := strings.TrimSpace(line[3:])
		if h == "" {
			return "", false, "", false
		}
		if mapped, is := oldHeadings[h]; is {
			return mapped, oldOnly[h], "", true
		}
		return h, false, "", true
	}
	for _, marker := range oldMarkers {
		if strings.HasPrefix(line, marker+":") {
			return oldHeadings[marker], oldOnly[marker], strings.TrimSpace(line[len(marker)+1:]), true
		}
	}
	return "", false, "", false
}

// Skeleton is the body a new ticket starts from: every section in order, each
// holding a fill comment its author replaces.
func Skeleton(id, title string) string {
	return "# " + id + ": " + title + "\n\n" +
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
}
