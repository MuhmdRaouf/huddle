// Package usage builds the long-range usage report: zero-filled daily series of requests,
// tokens and estimated cost by model, project, agent kind and harness, the saving cached
// input made against the input rate, and each project's costliest sessions. Pure core: it
// reads the rows its caller hands it and prices them through the Pricer given — it imports
// neither the history nor the server.
package usage

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The agent kinds a history node carries. A main node's label is the session's own — the
// head of its first prompt — while a subagent's names the subagent.
const (
	KindMain     = "main"
	KindSubagent = "subagent"
	KindJob      = "job"
)

// dayLayout is the local-day shape every date in a report spells.
const dayLayout = "2006-01-02"

// topSessionsPerProject caps each project's list of costliest sessions.
const topSessionsPerProject = 20

// labelLimit is how many runes of a session's label the report keeps.
const labelLimit = 80

// Row is one request's usage facts, already joined with the node it ran on: its project,
// kind and label. Upstream rides along so pricing picks the same offering the Costs view
// does. Harness groups the row in ByHarness (empty reads as claude); CostUSD, when the
// harness itself reported one, wins over the pricer's estimate.
type Row struct {
	TS           int64
	Model        string
	Upstream     string
	Provider     string
	Project      string
	SessionID    string
	Kind         string
	Label        string
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite   int64
	CacheWrite1h int64
	Harness      string
	CostUSD      *float64
}

// RowPrice is one row's money: its estimated cost and the estimated saving its cache reads
// made against the input rate.
type RowPrice struct {
	CostUSD    float64
	SavingsUSD float64
}

// Pricer prices one row at the list prices; false when no price covers its model. The
// consumer declares the seam: the server prices through the cost package, tests through a
// rate table.
type Pricer interface {
	PriceOf(row Row) (RowPrice, bool)
}

// Totals is one group's usage: the requests, the token kinds and the money.
type Totals struct {
	Requests   int64      `json:"requests"`
	Input      int64      `json:"input"`
	Output     int64      `json:"output"`
	CacheRead  int64      `json:"cacheRead"`
	CacheWrite int64      `json:"cacheWrite"`
	CostUSD    shared.USD `json:"costUsd"`
	SavingsUSD shared.USD `json:"savingsUsd"`
}

// Window is the report's range in epoch milliseconds; the local days the two ends land on
// are the days the report carries.
type Window struct {
	From int64
	To   int64
}

// Day is one local day's totals; its date spells YYYY-MM-DD in the report's location.
type Day struct {
	Date string `json:"date"`
	Totals
}

// Slice is one key's totals and its zero-filled daily series; the rows of ByModel,
// ByProject and ByKind.
type Slice struct {
	Key string `json:"key"`
	Totals
	Days []Day `json:"days"`
}

// Session is one project's session in the top list, its label the head of the session's
// first prompt.
type Session struct {
	SessionID string `json:"sessionId"`
	Label     string `json:"label"`
	Project   string `json:"project"`
	// Harness is the agent CLI the session ran on; an untagged one reads as claude.
	Harness string `json:"harness"`
	LastTS  int64  `json:"lastTs"`
	Totals
}

// Report is one window's usage: the daily series, the totals, the breakdowns by model,
// project, kind and harness, each project's top sessions, and the honest flags about
// prices.
type Report struct {
	From            string               `json:"from"`
	To              string               `json:"to"`
	Days            []Day                `json:"days"`
	Totals          Totals               `json:"totals"`
	ByModel         []Slice              `json:"byModel"`
	ByProject       []Slice              `json:"byProject"`
	ByKind          []Slice              `json:"byKind"`
	ByHarness       []Slice              `json:"byHarness"`
	TopSessions     map[string][]Session `json:"topSessions"`
	Approximate     bool                 `json:"approximate"`
	Unpriced        int                  `json:"unpriced"`
	PricesRetrieved string               `json:"pricesRetrieved"`
}

// Aggregate sums the window's rows into one report: a zero-filled day per local date the
// window spans in loc, the grand totals, the breakdowns by model, project, kind and
// harness, each project's top sessions, and the price honesty flags. Slices order by cost,
// ties by key; sessions by cost, then last activity, then id. A row no price covers still
// counts its requests and tokens at no cost, sits in Unpriced, and turns Approximate on;
// a row whose harness reported its cost is priced by that.
func Aggregate(rows []Row, window Window, prices Pricer, loc *time.Location) Report {
	report := Report{
		From:            midnightOf(time.UnixMilli(window.From).In(loc)).Format(dayLayout),
		To:              midnightOf(time.UnixMilli(window.To).In(loc)).Format(dayLayout),
		Days:            daysOf(window, loc),
		ByModel:         []Slice{},
		ByProject:       []Slice{},
		ByKind:          []Slice{},
		ByHarness:       []Slice{},
		TopSessions:     map[string][]Session{},
		PricesRetrieved: cost.PricesRetrieved,
	}
	dayTotals := make(map[string]*Totals, len(report.Days))
	for i := range report.Days {
		dayTotals[report.Days[i].Date] = &report.Days[i].Totals
	}
	models := map[string]*group{}
	projects := map[string]*group{}
	kinds := map[string]*group{}
	harnesses := map[string]*group{}
	sessions := map[string]*sessionAcc{}
	for _, row := range rows {
		date := time.UnixMilli(row.TS).In(loc).Format(dayLayout)
		// a harness-reported cost wins over the list prices; nothing else is known about
		// the rates it saw, so it carries no saving
		price, priced := RowPrice{}, false
		if row.CostUSD != nil {
			price, priced = RowPrice{CostUSD: *row.CostUSD}, true
		} else {
			price, priced = prices.PriceOf(row)
		}
		if !priced {
			report.Unpriced++
			report.Approximate = true
			price = RowPrice{}
		}
		addRow(&report.Totals, row, price)
		if day, ok := dayTotals[date]; ok {
			addRow(day, row, price)
		}
		groupAt(models, row.Model).add(row, price, date)
		groupAt(projects, row.Project).add(row, price, date)
		groupAt(kinds, row.Kind).add(row, price, date)
		groupAt(harnesses, harnessOf(row.Harness)).add(row, price, date)
		sessionAt(sessions, row).add(row, price)
	}
	dates := make([]string, len(report.Days))
	for i, day := range report.Days {
		dates[i] = day.Date
	}
	report.ByModel = slicesOf(models, dates)
	report.ByProject = slicesOf(projects, dates)
	report.ByKind = slicesOf(kinds, dates)
	report.ByHarness = slicesOf(harnesses, dates)
	topSessions(report.TopSessions, sessions)
	return report
}

// harnessOf is the harness a row groups under: an untagged row is Claude Code's.
func harnessOf(harness string) string {
	if harness == "" {
		return shared.HarnessClaude
	}
	return harness
}

// daysOf answers one zeroed day per local date the window spans, first to last.
func daysOf(window Window, loc *time.Location) []Day {
	first := midnightOf(time.UnixMilli(window.From).In(loc))
	last := midnightOf(time.UnixMilli(window.To).In(loc))
	days := []Day{}
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		days = append(days, Day{Date: day.Format(dayLayout)})
	}
	return days
}

// midnightOf is the moment's own day's midnight.
func midnightOf(at time.Time) time.Time {
	return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
}

// group is one breakdown axis's key: its totals and its per-day ones.
type group struct {
	totals Totals
	days   map[string]*Totals
}

// add folds one row into the key's totals and the row's day.
func (g *group) add(row Row, price RowPrice, date string) {
	addRow(&g.totals, row, price)
	day, ok := g.days[date]
	if !ok {
		day = &Totals{}
		g.days[date] = day
	}
	addRow(day, row, price)
}

// groupAt answers one key's group, creating it on first sight.
func groupAt(groups map[string]*group, key string) *group {
	acc, ok := groups[key]
	if !ok {
		acc = &group{days: map[string]*Totals{}}
		groups[key] = acc
	}
	return acc
}

// sessionAcc is one project's session accumulating its totals, its last activity and its
// label.
type sessionAcc struct {
	session   Session
	mainNamed bool
}

// add folds one row into the session; a main node's label is the session's and replaces
// any subagent's, whose own stands in only until a main one is seen.
func (a *sessionAcc) add(row Row, price RowPrice) {
	a.session.LastTS = max(a.session.LastTS, row.TS)
	if row.Label != "" && !a.mainNamed {
		if row.Kind == KindMain || a.session.Label == "" {
			a.session.Label = truncateLabel(row.Label)
		}
		a.mainNamed = row.Kind == KindMain
	}
	addRow(&a.session.Totals, row, price)
}

// sessionAt answers one session's accumulator, creating it on first sight with the harness
// its first row names.
func sessionAt(sessions map[string]*sessionAcc, row Row) *sessionAcc {
	key := row.Project + "\x00" + row.SessionID
	acc, ok := sessions[key]
	if !ok {
		acc = &sessionAcc{session: Session{
			SessionID: row.SessionID,
			Project:   row.Project,
			Harness:   harnessOf(row.Harness),
		}}
		sessions[key] = acc
	}
	return acc
}

// addRow folds one row's counts and money into a totals.
func addRow(totals *Totals, row Row, price RowPrice) {
	totals.Requests++
	totals.Input += row.Input
	totals.Output += row.Output
	totals.CacheRead += row.CacheRead
	totals.CacheWrite += row.CacheWrite
	totals.CostUSD += shared.USD(price.CostUSD)
	totals.SavingsUSD += shared.USD(price.SavingsUSD)
}

// slicesOf materializes one axis: every key's totals and its zero-filled days, the
// costliest key first, ties by key.
func slicesOf(groups map[string]*group, dates []string) []Slice {
	out := make([]Slice, 0, len(groups))
	for key, acc := range groups {
		slice := Slice{Key: key, Totals: acc.totals, Days: make([]Day, 0, len(dates))}
		for _, date := range dates {
			day := Day{Date: date}
			if totals, ok := acc.days[date]; ok {
				day.Totals = *totals
			}
			slice.Days = append(slice.Days, day)
		}
		out = append(out, slice)
	}
	slices.SortFunc(out, func(a, b Slice) int {
		if c := cmp.Compare(b.Totals.CostUSD, a.Totals.CostUSD); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

// topSessions fills each project's list with its costliest sessions, the cost deciding
// first, then the last activity, then the id.
func topSessions(into map[string][]Session, sessions map[string]*sessionAcc) {
	for _, acc := range sessions {
		project := acc.session.Project
		into[project] = append(into[project], acc.session)
	}
	for project, list := range into {
		slices.SortFunc(list, func(a, b Session) int {
			if c := cmp.Compare(b.Totals.CostUSD, a.Totals.CostUSD); c != 0 {
				return c
			}
			if c := cmp.Compare(b.LastTS, a.LastTS); c != 0 {
				return c
			}
			return strings.Compare(a.SessionID, b.SessionID)
		})
		if len(list) > topSessionsPerProject {
			list = list[:topSessionsPerProject]
		}
		into[project] = list
	}
}

// truncateLabel keeps a label's first 80 runes.
func truncateLabel(label string) string {
	runes := []rune(label)
	if len(runes) <= labelLimit {
		return label
	}
	return string(runes[:labelLimit])
}
