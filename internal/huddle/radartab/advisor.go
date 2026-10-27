package radartab

// The model advisor's rows, read through the tab's own API for the insights route.

import "github.com/MuhmdRaouf/huddle/internal/radar/shared"

// ModelAdvice is one model's line in radar's advisor report, as the insights route reads it:
// the subagent runs on one main model that looked flash-sized, and what they cost as run and
// on the model's flash sibling.
type ModelAdvice struct {
	Model        string
	Flash        string   // the model's flash sibling; "" when it has none
	Runs         int64    // the flash-sized runs the advisor counted
	CostUsd      *float64 // the runs' own price; nil when none of them priced
	FlashCostUsd *float64 // the same runs on the sibling; nil without a sibling or a price
}

// ModelAdvice reads the tab's own advisor report, in process, one advice per ByModel row in
// the report's order. A tab without insights answers none.
func (t *Tab) ModelAdvice() []ModelAdvice {
	if t == nil || t.Insights == nil {
		return nil
	}
	report := t.Insights.Advisor()
	advice := make([]ModelAdvice, 0, len(report.ByModel))
	for _, row := range report.ByModel {
		entry := ModelAdvice{Model: row.Model, Runs: row.Runs}
		if row.Flash != nil {
			entry.Flash = *row.Flash
		}
		entry.CostUsd = usdPtr(row.CostUsd)
		entry.FlashCostUsd = usdPtr(row.FlashCostUsd)
		advice = append(advice, entry)
	}
	return advice
}

// usdPtr copies one price into a plain float pointer, so no cost type crosses into insights.
func usdPtr(value *shared.USD) *float64 {
	if value == nil {
		return nil
	}
	out := float64(*value)
	return &out
}
