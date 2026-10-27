package insights

import "fmt"

// modelOversizedRuns is how many flash-sized runs on one model make a tip: two may be a
// coincidence, three are a habit.
const modelOversizedRuns = 3

// modelOversizedTips turns each of radar's advisor rows into one owner tip: subagent runs on
// a main model that looked small enough for its flash sibling. The tip is the huddle's, not
// one session's; a model with no flash sibling is not a tip, since there is nothing to move
// the runs to.
func modelOversizedTips(in Input) []Tip {
	var tips []Tip
	for _, model := range in.Models {
		if model.Flash == "" || model.Runs < modelOversizedRuns {
			continue
		}
		sentence := fmt.Sprintf("%d subagent runs on `%s` looked small enough for `%s`.",
			model.Runs, model.Model, model.Flash)
		if model.Priced && model.SavingUsd > 0 {
			sentence = fmt.Sprintf("%d subagent runs on `%s` looked small enough for `%s`; they would have cost $%.2f less.",
				model.Runs, model.Model, model.Flash, model.SavingUsd)
		}
		tips = append(tips, Tip{
			ID:       fmt.Sprintf("%s:%s:%s", RuleModelOversized, model.Model, in.Now.UTC().Format("2006-01-02")),
			Rule:     RuleModelOversized,
			Session:  "",
			Sentence: sentence,
			Count:    model.Runs,
		})
	}
	return tips
}
