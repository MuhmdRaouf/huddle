package cost

import (
	"cmp"
	"slices"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// ProjectDay is one project's priced spend on one calendar day: DaysAgo 0 is today in the
// zone, 1 yesterday, and so on.
type ProjectDay struct {
	Project string
	DaysAgo int
	CostUsd shared.USD
}

// ProjectDays sums the rows' priced spend per project and calendar day over the last `days`
// days, today included. The day a call lands in is the zone's calendar day, so two calls on
// the same UTC day can sit on different days. A project-day with no priced cost is left out,
// and so is the session-less "(no project)" group; rows are summed in the order given, the
// ledger's oldest first. The answer is sorted by project, then day.
func ProjectDays(rows []LedgerRow, names LedgerNames, now int64, zone *time.Location, days int) []ProjectDay {
	today := clock.DayOf(time.UnixMilli(now), zone)
	perProject := map[string]map[int]*shared.USD{}
	for _, row := range rows {
		project, _ := keyAndLabel(row, ByProject, names)
		if project == "(no project)" {
			continue
		}
		c := RequestCost(pricedOfRow(row))
		if c == nil {
			continue
		}
		rowDay := clock.DayOf(time.UnixMilli(row.Ts), zone)
		// across a daylight-saving change two zone midnights are 23 h or 25 h apart, so the
		// days back rounds off the hour gap; truncation would count a 23 h yesterday as today
		ago := int((today.Sub(rowDay) + 12*time.Hour) / (24 * time.Hour))
		if ago < 0 || ago >= days {
			continue
		}
		daysOf := perProject[project]
		if daysOf == nil {
			daysOf = map[int]*shared.USD{}
			perProject[project] = daysOf
		}
		daysOf[ago] = AddCost(daysOf[ago], &c.USD)
	}
	out := make([]ProjectDay, 0, len(perProject))
	for project, daysOf := range perProject {
		for ago, sum := range daysOf {
			out = append(out, ProjectDay{Project: project, DaysAgo: ago, CostUsd: *sum})
		}
	}
	slices.SortFunc(out, func(a, b ProjectDay) int {
		if byProject := cmp.Compare(a.Project, b.Project); byProject != 0 {
			return byProject
		}
		return cmp.Compare(a.DaysAgo, b.DaysAgo)
	})
	return out
}
