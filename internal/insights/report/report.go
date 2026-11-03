package report

import (
	"fmt"
	"strings"
)

// MaxTips is the most tips a report holds in all.
const MaxTips = 3

// SectionTitles are the five sections of a report, in order. A report's headings spell them
// exactly.
var SectionTitles = [...]string{
	"Where the tokens went",
	"What blocked you",
	"Waste",
	"What went well",
	"Change since the last run",
}

const (
	headingPrefix = "## "
	tipPrefix     = "Tip:"
)

// Section is one section of a report: its title and its text without the tips.
type Section struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Report is a model's answer read into its five sections and its tips, each tip without its
// "Tip:" label, in the order they appear.
type Report struct {
	Sections []Section `json:"sections"`
	Tips     []string  `json:"tips"`
}

// InvalidReportError says a model's answer is not a report. Reason is one line.
type InvalidReportError struct{ Reason string }

func (e *InvalidReportError) Error() string { return "the answer is not a valid report: " + e.Reason }

func invalid(format string, args ...any) error {
	return &InvalidReportError{Reason: fmt.Sprintf(format, args...)}
}

// Parse reads an answer written as five "## <title>" sections in order. A line that starts with
// "Tip:" (after an optional "- ") is a tip; every other line is the section's text. Anything
// else is an error: nothing is repaired.
func Parse(answer string) (Report, error) {
	var r Report
	var body []string
	var hasTip bool
	closeSection := func() error {
		if len(r.Sections) == 0 {
			return nil
		}
		last := &r.Sections[len(r.Sections)-1]
		last.Body = strings.TrimSpace(strings.Join(body, "\n"))
		if last.Body == "" && !hasTip {
			return invalid("section %q has no text", last.Title)
		}
		body, hasTip = nil, false
		return nil
	}
	for _, line := range strings.Split(answer, "\n") {
		line = strings.TrimSpace(line)
		if title, ok := strings.CutPrefix(line, headingPrefix); ok {
			if err := closeSection(); err != nil {
				return Report{}, err
			}
			r.Sections = append(r.Sections, Section{Title: strings.TrimSpace(title)})
			continue
		}
		if len(r.Sections) == 0 {
			if line != "" {
				return Report{}, invalid("text before the first section")
			}
			continue
		}
		if tip, ok := strings.CutPrefix(strings.TrimPrefix(line, "- "), tipPrefix); ok {
			if tip = strings.TrimSpace(tip); tip == "" {
				return Report{}, invalid("an empty tip in section %q", r.Sections[len(r.Sections)-1].Title)
			}
			r.Tips, hasTip = append(r.Tips, tip), true
			continue
		}
		body = append(body, line)
	}
	if err := closeSection(); err != nil {
		return Report{}, err
	}
	if len(r.Sections) != len(SectionTitles) {
		return Report{}, invalid("found %d sections, want %d", len(r.Sections), len(SectionTitles))
	}
	for i, s := range r.Sections {
		if s.Title != SectionTitles[i] {
			return Report{}, invalid("section %d is %q, want %q", i+1, s.Title, SectionTitles[i])
		}
	}
	return r, nil
}
