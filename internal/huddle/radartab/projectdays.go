package radartab

// The project days the owner's insights card reads: the radar tab's ledger, one row per
// project and calendar day, the costs carried over as figures.

// ProjectDay is one project's priced spend on one calendar day: DaysAgo 0 is today in the
// hub's time zone, 1 yesterday, and so on.
type ProjectDay struct {
	Project string
	DaysAgo int
	CostUsd float64
}

// projectCostWindow is the days ProjectDays reads: today plus the week the project-cost rule
// averages over.
const projectCostWindow = 8

// ProjectDays answers the ledger's priced spend per project and calendar day over today and
// the week before it; a tab without insights answers none.
func (t *Tab) ProjectDays() []ProjectDay {
	if t == nil || t.Insights == nil {
		return nil
	}
	days := t.Insights.ProjectDays(projectCostWindow)
	out := make([]ProjectDay, 0, len(days))
	for _, day := range days {
		out = append(out, ProjectDay{Project: day.Project, DaysAgo: day.DaysAgo, CostUsd: float64(day.CostUsd)})
	}
	return out
}
