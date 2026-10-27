package insights

import "fmt"

// A project whose spend today passes twice its 7-day average is worth one owner look: the
// numbers say which project and by how much. Today and the seven days before it are calendar
// days in the hub's time zone; a day with no row spent nothing.
const (
	projectCostFactor       = 2.0
	projectCostBaselineDays = 7
	projectCostFloorUSD     = 1.0 // below a dollar a day, doubling is noise
)

// projectCostTips names each project whose day passed twice its recent average. Seven days of
// history back the average — a younger project has no habit to break — and the pass is strict:
// twice exactly is no news.
func projectCostTips(in Input) []Tip {
	perProject := map[string][]ProjectDay{}
	for _, day := range in.ProjectDays {
		perProject[day.Project] = append(perProject[day.Project], day)
	}
	var tips []Tip
	for project, days := range perProject {
		var today, week float64
		sawWeek := false
		for _, day := range days {
			switch {
			case day.DaysAgo == 0:
				today = day.CostUSD
			case day.DaysAgo >= 1 && day.DaysAgo <= projectCostBaselineDays:
				week += day.CostUSD
			}
			if day.DaysAgo >= projectCostBaselineDays {
				sawWeek = true
			}
		}
		baseline := week / projectCostBaselineDays
		if !sawWeek || baseline <= 0 || today < projectCostFloorUSD || today <= projectCostFactor*baseline {
			continue
		}
		tips = append(tips, Tip{
			ID:   fmt.Sprintf("%s:%s:%s", RuleProjectCost, project, in.Now.UTC().Format("2006-01-02")),
			Rule: RuleProjectCost,
			Sentence: fmt.Sprintf("`%s` cost $%.2f today, %.1f times its 7-day average of $%.2f.",
				project, today, today/baseline, baseline),
			Count: 1,
		})
	}
	return tips
}
