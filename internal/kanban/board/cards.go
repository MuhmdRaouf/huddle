package board

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/ticket"
)

// Card is one ticket as the cli list and the server's snapshot show it; the
// body never rides along.
type Card struct {
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	Column        string         `json:"column"`
	Summary       string         `json:"summary"`
	Type          string         `json:"type"`
	Priority      string         `json:"priority"`
	Est           string         `json:"est"`
	Cluster       string         `json:"cluster"`
	FeatureClosed bool           `json:"feature_closed"`
	Tags          []string       `json:"tags"`
	Expedite      bool           `json:"expedite"`
	Holder        string         `json:"holder"`
	Author        string         `json:"author"`
	Deps          []Dep          `json:"deps"`
	Blocked       *Blocked       `json:"blocked"`
	Badges        []ticket.Badge `json:"badges"`
	Lint          CardLint       `json:"lint"`
	MovedByHand   string         `json:"movedByHand"`
	Branch        string         `json:"branch"`
	Worktree      string         `json:"worktree"`
	FinishedAt    string         `json:"finishedAt"`
	UpdatedAt     string         `json:"updatedAt"`
}

// Dep is one dependency with its state: a column, "missing" or "text".
type Dep struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// Blocked is a ticket's parked state.
type Blocked struct {
	Reason string   `json:"reason"`
	On     []string `json:"on"`
	Until  string   `json:"until"`
	From   string   `json:"from"`
}

// CardLint is a card's bar findings.
type CardLint struct {
	Failures []string `json:"failures"`
	Warnings []string `json:"warnings"`
}

// Status is the status command's answer.
type Status struct {
	Epoch    int
	Stopped  *StopLine
	Expedite []ExpediteEntry
	Counts   []ColumnCount
}

// StopLine is the STOP-LINE record while the line is stopped.
type StopLine struct {
	Epoch int    `json:"epoch"`
	Actor string `json:"actor"`
	Note  string `json:"note"`
	TS    string `json:"ts"`
}

// ExpediteEntry is one open expedite ticket in the fast track, furthest along
// first.
type ExpediteEntry struct{ ID, Column, WaitsOn string }

// ColumnCount is one column's ticket count.
type ColumnCount struct {
	Column string
	Count  int
}

// LintReport is every flagged ticket's bar findings, in column then file
// order.
type LintReport struct{ Tickets []LintTicket }

// LintTicket is one ticket's findings.
type LintTicket struct {
	ID, Column string
	Problems   []ticket.Problem
}

// CardOf derives a ticket's card view: dependency states, parked state,
// badges and bar findings. problems are the parse problems of the ticket's
// file, merged into the findings. A second file holding the same id flags both
// as duplicates.
func CardOf(t ticket.Ticket, column string, b Board, problems []ticket.Problem) Card {
	self, _, _ := b.Find(t.ID)
	card := CardOfLookup(t, column, self, problems,
		func(id string) (string, bool) {
			_, col, ok := b.Find(id)
			return col, ok
		},
		b.knownExcept(self))
	if self != "" {
		// the ticket's own time answers on the board's seam: a db board holds no
		// file for CardOfLookup to stat
		if info, ok := b.fs().stat(self); ok {
			card.UpdatedAt = info.ModTime.Format(time.RFC3339)
		}
	}
	return card
}

// CardOfLookup derives a ticket's card view with the board's answers supplied
// by the caller: where answers a dependency's column, self is the ticket's own
// path (its mtime stamps updatedAt), and known answers whether a file other
// than self holds an id. It is CardOf without the disk walks, for a caller
// that already holds the board's shape in memory.
func CardOfLookup(t ticket.Ticket, column, self string, problems []ticket.Problem,
	where func(id string) (string, bool), known func(id string) bool) Card {
	all := append(problems, ticket.Lint(t, ticket.Context{Column: column, Known: known})...)
	depState := func(id string) string {
		if col, ok := where(id); ok {
			return col
		}
		return "missing"
	}
	card := Card{
		ID: t.ID, Title: t.Title, Column: column, Summary: t.Summary,
		Type: t.Type, Priority: t.Priority, Est: t.Est, Cluster: t.Cluster,
		Tags: t.Tags, Expedite: t.Expedite,
		Holder:      strings.TrimSpace(t.Fields["claimed_by"]),
		Author:      strings.TrimSpace(t.Fields["authored_by"]),
		Deps:        []Dep{},
		Badges:      []ticket.Badge{},
		Lint:        CardLint{Failures: []string{}, Warnings: []string{}},
		MovedByHand: t.Fields["moved_by_hand"],
		Branch:      t.Fields["branch"],
		Worktree:    t.Fields["worktree"],
	}
	if card.Tags == nil {
		card.Tags = []string{}
	}
	for _, dep := range t.DependsOn {
		state := "text"
		if idRe.MatchString(dep) {
			state = depState(dep)
		}
		card.Deps = append(card.Deps, Dep{ID: dep, State: state})
	}
	if column == "blocked" || strings.TrimSpace(t.Fields["blocked_reason"]) != "" {
		card.Blocked = &Blocked{
			Reason: t.Fields["blocked_reason"],
			On:     ticket.ParseList(t.Fields["blocked_on"]),
			Until:  t.Fields["unblock_when"],
			From:   t.Fields["blocked_from"],
		}
		if card.Blocked.On == nil {
			card.Blocked.On = []string{}
		}
	}
	card.Badges = ticket.Badges(t, column, depState, all)
	for _, p := range all {
		if p.Severity == "failure" {
			card.Lint.Failures = append(card.Lint.Failures, p.Message)
		} else {
			card.Lint.Warnings = append(card.Lint.Warnings, p.Message)
		}
	}
	if self != "" {
		// the watcher's folder board: a real file answers with its mtime, and a
		// db board's path has none — CardOf stamps that one on the seam
		if info, err := os.Stat(self); err == nil {
			card.UpdatedAt = info.ModTime().Format(time.RFC3339)
		}
	}
	if finished := t.Fields["finished_at"]; finished != "" {
		card.FinishedAt = asRFC3339(finished)
	}
	return card
}

// asRFC3339 answers the stamp as RFC 3339, or as written when it does not
// parse.
func asRFC3339(stamp string) string {
	for _, layout := range []string{stampLayout, time.RFC3339} {
		if when, err := time.Parse(layout, stamp); err == nil {
			return when.Format(time.RFC3339)
		}
	}
	return stamp
}

// List answers the board's cards, one column's when named. A ticket file that
// fails to parse still answers, with its problems attached. A closed feature's
// cards stay out until the feature reopens.
func (b Board) List(ctx context.Context, column string) ([]Card, error) {
	if column != "" && !validColumn(column) {
		return nil, fmt.Errorf("kanban: %s is not a column", column)
	}
	cols := Columns
	if column != "" {
		cols = []string{column}
	}
	closed := b.closedSet()
	var cards []Card
	for _, col := range cols {
		for _, name := range b.names(col) {
			t, problems := b.loadCard(col, strings.TrimSuffix(name, ".md"))
			if closed[t.Cluster] {
				continue
			}
			cards = append(cards, CardOf(t, col, b, problems))
		}
	}
	return cards, nil
}

// Status answers the epoch, the stop-line, the open expedite lane (furthest
// along first) and the per-column counts. A closed feature's card holds neither
// a lane entry nor a count.
func (b Board) Status(ctx context.Context) (Status, error) {
	st := Status{Epoch: b.readEpoch(), Stopped: b.stopped()}
	closed := b.closedSet()
	for _, col := range openColumns {
		for _, name := range b.names(col) {
			t, _ := b.loadCard(col, strings.TrimSuffix(name, ".md"))
			if closed[t.Cluster] || !t.Expedite {
				continue
			}
			entry := ExpediteEntry{ID: t.ID, Column: col}
			if col == "ready" {
				// a waiting card with an open dependency holds nothing yet
				entry.WaitsOn = b.openDep(t)
			}
			st.Expedite = append(st.Expedite, entry)
		}
	}
	for _, col := range Columns {
		n := 0
		for _, name := range b.names(col) {
			if t, _ := b.loadCard(col, strings.TrimSuffix(name, ".md")); !closed[t.Cluster] {
				n++
			}
		}
		st.Counts = append(st.Counts, ColumnCount{Column: col, Count: n})
	}
	return st, nil
}

// Check answers the stop-line state and, with an id, the ticket's column,
// type, actors and gates. While the line is stopped, or for a missing ticket,
// the lines still answer and the error is the refusing line.
func (b Board) Check(ctx context.Context, id string) ([]string, error) {
	var lines []string
	var stopRefusal *Refused
	if rec := b.stopped(); rec != nil {
		lines = append(lines, fmt.Sprintf("STOPPED epoch=%d actor=%s: %s", rec.Epoch, rec.Actor, rec.Note))
		stopRefusal = &Refused{Message: lines[0]}
	} else {
		lines = append(lines, fmt.Sprintf("epoch=%d running", b.readEpoch()))
	}
	if id == "" {
		if stopRefusal != nil {
			return lines, stopRefusal
		}
		return lines, nil
	}
	path, col, ok := b.Find(id)
	if !ok {
		msg := id + ": not on the board; run list to see ids, or new to create it"
		return append(lines, msg), &Refused{Message: msg}
	}
	_ = path
	t, _ := b.loadCard(col, id)
	raw := strings.ToLower(strings.TrimSpace(t.Type))
	shown := raw
	if raw != normType(raw) {
		shown = raw + " (treated as implementation)"
		if raw == "" {
			shown = "(none) (treated as implementation)"
		}
	}
	or := func(key string) string {
		if v := t.Fields[key]; v != "" {
			return v
		}
		return "-"
	}
	line := fmt.Sprintf("%s: column=%s type=%s authored_by=%s claimed_by=%s",
		id, col, shown, or("authored_by"), or("claimed_by"))
	if v := t.Fields["blocked_from"]; v != "" {
		line += " blocked_from=" + v
	}
	if t.Expedite {
		line += " expedite=yes"
	}
	lines = append(lines, line)
	switch {
	case isImplementation(t.Type) && col != "done":
		lines = append(lines, "gates: review -> test -> done (test and done by actors other than the author or holder)")
	case col != "done":
		lines = append(lines, "gates: none; done finishes it from any column")
	}
	_ = path
	if stopRefusal != nil {
		return lines, stopRefusal
	}
	return lines, nil
}

// Lint checks every ticket against the bar and reports the flagged ones, in
// column then file order. Parse problems merge with the lint findings.
func (b Board) Lint(ctx context.Context) (LintReport, error) {
	var report LintReport
	for _, col := range Columns {
		for _, name := range b.names(col) {
			path := filepath.Join(b.Root, col, name)
			t, problems := b.loadCard(col, strings.TrimSuffix(name, ".md"))
			problems = append(problems, ticket.Lint(t, ticket.Context{
				Column: col,
				Known:  b.knownExcept(path),
			})...)
			if len(problems) == 0 {
				continue
			}
			report.Tickets = append(report.Tickets, LintTicket{
				ID: strings.TrimSuffix(name, ".md"), Column: col, Problems: problems,
			})
		}
	}
	return report, nil
}

// Shown is one ticket whole: its card view, every front-matter field, its body
// and its file exactly as it sits on disk.
type Shown struct {
	Card   Card
	Fields map[string]string
	Body   string
	Text   string
	Path   string
}

// Show answers one ticket wherever it sits, read leniently like every other
// reader. A ticket that is not on the board is a refusal.
func (b Board) Show(ctx context.Context, id string) (Shown, error) {
	path, col, ok := b.Find(id)
	if !ok {
		return Shown{}, refused("%s: not on the board; run list to see ids, or new to create it", id)
	}
	data, err := b.fs().readFile(path)
	if err != nil {
		return Shown{}, err
	}
	t, problems := parseFile(filepath.Base(path), data)
	card := CardOf(t, col, b, problems)
	card.FeatureClosed = b.closedSet()[t.Cluster]
	return Shown{
		Card:   card,
		Fields: t.Fields,
		Body:   t.Body,
		Text:   string(data),
		Path:   path,
	}, nil
}
