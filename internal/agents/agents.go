// Package agents decides the Claude Code agent files huddle keeps in the user's Claude agents folder:
// one per Claude model family, named by the family's newest model, so the agent panel names the model
// a subagent runs on. It owns only the files it marks; a file without the marker is the user's.
package agents

import (
	"bytes"
	"slices"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/pricing"
)

// Marker is the front-matter line that marks a file as huddle's.
const Marker = "managed-by: huddle"

// agentBody is the one line every rendered file carries: the files pin the model and nothing else.
const agentBody = "Do the task in your prompt and report what you did."

// families lists the Claude model families that get an agent file, in this order.
var families = [...]string{"fable", "opus", "sonnet", "haiku"}

// Model is the newest model of one family.
type Model struct {
	Family  string // "opus"
	Version []int  // [5 5]
	ID      string // "claude-opus-5-5"
}

// Name spells the model for the agent panel: "opus-5.5"; a one-part version gives "opus-5".
func (m Model) Name() string {
	return m.Family + "-" + m.versionText()
}

// Title is the human name the file's description carries: "Claude Opus 5.5", family capitalised.
func (m Model) Title() string {
	return "Claude " + strings.ToUpper(m.Family[:1]) + m.Family[1:] + " " + m.versionText()
}

// FileName is the file the model gets in the agents folder: Name() + ".md".
func (m Model) FileName() string {
	return m.Name() + ".md"
}

// versionText joins the version's parts with dots: "5.5"; a one-part version gives "5".
func (m Model) versionText() string {
	parts := make([]string, len(m.Version))
	for i, number := range m.Version {
		parts[i] = strconv.Itoa(number)
	}
	return strings.Join(parts, ".")
}

// Newest picks the newest model of each family from model ids. An id counts when it matches
// claude-<family>-<major>[-<minor>] exactly (no date suffix, no other words); versions compare
// as numbers, so claude-opus-10-1 is newer than claude-opus-5-5. A family with no id is left
// out. Order: families' order.
func Newest(ids []string) []Model {
	models := make([]Model, 0, len(families))
	for _, family := range families {
		var best Model
		for _, id := range ids {
			version, ok := parseID(family, id)
			if !ok {
				continue
			}
			if best.ID != "" && !newer(version, best.Version) {
				continue
			}
			best = Model{Family: family, Version: version, ID: id}
		}
		if best.ID == "" {
			continue
		}
		models = append(models, best)
	}
	return models
}

// parseID reads the version out of "claude-<family>-<major>[-<minor>]"; false when the id is
// another family's or carries anything else, a date suffix included.
func parseID(family, id string) ([]int, bool) {
	rest, ok := strings.CutPrefix(id, "claude-"+family+"-")
	if !ok {
		return nil, false
	}
	parts := strings.Split(rest, "-")
	if len(parts) > 2 {
		return nil, false
	}
	version := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		version = append(version, number)
	}
	return version, true
}

// newer compares versions as numbers part by part; a missing part counts as zero, so
// [6 1] is newer than [6].
func newer(a, b []int) bool {
	for i := 0; i < len(a) || i < len(b); i++ {
		left, right := 0, 0
		if i < len(a) {
			left = a[i]
		}
		if i < len(b) {
			right = b[i]
		}
		if left != right {
			return left > right
		}
	}
	return false
}

// ClaudeIDs returns the model ids of the pricing table's Anthropic offering
// (pricing.OfferingAnthropic), sorted.
func ClaudeIDs() []string {
	for _, offering := range pricing.Offerings {
		if offering.ID != pricing.OfferingAnthropic {
			continue
		}
		ids := make([]string, 0, len(offering.Models))
		for id := range offering.Models {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		return ids
	}
	return nil
}

// Render is the file for one model: front matter with name, description (Title), model (ID) and
// the Marker line, then one blank line and the one-line body. No effort, tools or other keys.
func Render(m Model) []byte {
	return []byte("---\n" +
		"name: " + m.Name() + "\n" +
		"description: " + m.Title() + "\n" +
		"model: " + m.ID + "\n" +
		Marker + "\n" +
		"---\n" +
		"\n" +
		agentBody + "\n")
}

// Existing is one .md file already in the folder.
type Existing struct {
	Name string // file name, "opus-5.5.md"
	Body []byte
}

// Managed reports whether a file's front matter holds the Marker line.
func Managed(body []byte) bool {
	rest, ok := strings.CutPrefix(string(body), "---\n")
	if !ok {
		return false
	}
	front, _, found := strings.Cut(rest, "\n---")
	if !found {
		return false
	}
	for _, line := range strings.Split(front, "\n") {
		if line == Marker {
			return true
		}
	}
	return false
}

// Plan is what one pass does. Write holds only files whose bytes differ from what is there;
// Remove holds only managed files no family wants any more. Both sorted by name.
type Plan struct {
	Write  []File
	Remove []string
}

// File is one file the plan writes.
type File struct {
	Name string
	Body []byte
}

// Decide turns the wanted models and the folder's files into a plan:
//   - a wanted name with no file: write it;
//   - a wanted name whose file is managed and differs: write it; equal bytes: nothing;
//   - a wanted name whose file is not managed: nothing (the user's file wins, huddle writes no copy);
//   - a managed file whose name is not wanted: remove it;
//   - a file that is not managed: never written, never removed.
func Decide(models []Model, existing []Existing) Plan {
	current := make(map[string][]byte, len(existing))
	for _, file := range existing {
		current[file.Name] = file.Body
	}
	wanted := make(map[string]bool, len(models))
	var plan Plan
	for _, model := range models {
		name := model.FileName()
		wanted[name] = true
		body := Render(model)
		there, ok := current[name]
		if ok && (!Managed(there) || bytes.Equal(there, body)) {
			continue
		}
		plan.Write = append(plan.Write, File{Name: name, Body: body})
	}
	for _, file := range existing {
		if !wanted[file.Name] && Managed(file.Body) {
			plan.Remove = append(plan.Remove, file.Name)
		}
	}
	slices.SortFunc(plan.Write, func(a, b File) int { return strings.Compare(a.Name, b.Name) })
	slices.Sort(plan.Remove)
	return plan
}
